package records

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) memories() *docstore.CollectionRef   { return d.root().Collection("memories") }
func (d *Database) memoryLogs() *docstore.CollectionRef { return d.root().Collection("memory_logs") }
func (d *Database) summary() *docstore.DocumentRef {
	return d.root().Collection("conversation").Doc("summary")
}

func (d *Database) Memories(ctx context.Context) ([]state.Memory, error) {
	it := d.memories().OrderBy("created_at", docstore.Asc).Documents(ctx)
	defer it.Stop()
	memories, err := documents[state.Memory](it)
	if err != nil {
		return nil, err
	}
	state.SortCoreMemories(memories)
	return memories, nil
}

func (d *Database) readAllCoreMemoriesTx(tx *docstore.Transaction) ([]state.Memory, error) {
	it := tx.Documents(d.memories().Query)
	defer it.Stop()
	return documents[state.Memory](it)
}

func demoteCoreToLogEntry(m state.Memory, now time.Time) state.MemoryLogEntry {
	nowUTC := now.UTC()
	section := m.Section
	if section == "" {
		section = "Archived"
	}
	return state.MemoryLogEntry{
		ID:            "demote_" + m.ID,
		Section:       section,
		Text:          m.Text,
		About:         m.About,
		Provenance:    m.EffectiveProvenance(),
		SourceInputID: m.SourceInputID,
		SourceText:    m.SourceText,
		CreatedAt:     nowUTC,
		UpdatedAt:     nowUTC,
	}
}

func validateCoreMemoryFields(text, about, section string) error {
	if strings.TrimSpace(text) == "" || len(text) > state.MaxMemoryText || !utf8.ValidString(text) {
		return state.ErrInvalidResource
	}
	if len(about) > state.MaxMemoryAbout || !utf8.ValidString(about) {
		return state.ErrInvalidResource
	}
	if len(section) > state.MaxMemorySection || !utf8.ValidString(section) {
		return state.ErrInvalidResource
	}
	return nil
}

func validMemoryID(id string) bool {
	return strings.TrimSpace(id) != "" && !strings.Contains(id, "/") && len(id) <= 256
}

type memoryChangeReads struct {
	byID    map[string]state.Memory
	logDate string
	shards  []state.MemoryLog
}

func (d *Database) readMemoryChanges(tx *docstore.Transaction, changes []state.MemoryChange) (memoryChangeReads, error) {
	if len(changes) == 0 {
		return memoryChangeReads{}, nil
	}
	for _, c := range changes {
		if c.Error != "" {
			return memoryChangeReads{}, nil
		}
	}
	existing, err := d.readAllCoreMemoriesTx(tx)
	if err != nil {
		return memoryChangeReads{}, err
	}
	byID := make(map[string]state.Memory, len(existing))
	for _, m := range existing {
		byID[m.ID] = m
	}
	logDate := d.now().UTC().Format("2006-01-02")
	shards, err := d.readDailyLogShards(tx, logDate)
	if err != nil {
		return memoryChangeReads{}, err
	}
	return memoryChangeReads{byID: byID, logDate: logDate, shards: shards}, nil
}

func (d *Database) writeMemoryChanges(
	tx *docstore.Transaction, t state.ConversationTurn, reads memoryChangeReads, now time.Time,
) error {
	if len(t.Attempt.Memory) == 0 {
		return nil
	}
	for _, c := range t.Attempt.Memory {
		if c.Error != "" {
			return nil
		}
	}
	source := state.SourceInput(t.Inputs)
	byID := make(map[string]state.Memory, len(reads.byID))
	for id, m := range reads.byID {
		byID[id] = m
	}
	deleted := map[string]bool{}
	upserted := map[string]bool{}
	for _, c := range t.Attempt.Memory {
		if c.Kind == state.MemoryForget {
			if _, existed := byID[c.ID]; existed {
				delete(byID, c.ID)
				deleted[c.ID] = true
			}
			delete(upserted, c.ID)
			continue
		}
		var existing *state.Memory
		if old, ok := byID[c.ID]; ok {
			oldCopy := old
			existing = &oldCopy
		}
		m := state.ApplyMemoryChange(c, existing, source, now)
		byID[c.ID] = m
		upserted[c.ID] = true
		delete(deleted, c.ID)
	}

	all := make([]state.Memory, 0, len(byID))
	for _, m := range byID {
		all = append(all, m)
	}
	kept, demoted, err := state.DemoteCoreOverflow(all, state.CoreMemoryBytes)
	if err != nil {
		return err
	}
	keptByID := make(map[string]state.Memory, len(kept))
	for _, m := range kept {
		keptByID[m.ID] = m
	}
	demotedEntries := make([]state.MemoryLogEntry, 0, len(demoted))
	for _, m := range demoted {
		delete(upserted, m.ID)
		if _, existed := reads.byID[m.ID]; existed {
			deleted[m.ID] = true
		}
		demotedEntries = append(demotedEntries, demoteCoreToLogEntry(m, now))
	}

	if err := d.applyCoreAndLogWrites(tx, deleted, upserted, keptByID, reads.logDate, reads.shards, demotedEntries, now); err != nil {
		return err
	}

	if slot, ok := tx.Context().Value(logCtxKey{}).(**eventLog); ok && slot != nil && *slot != nil {
		if err := (*slot).add(state.EventMemory, ""); err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) applyCoreAndLogWrites(
	tx *docstore.Transaction,
	deleted map[string]bool,
	upserted map[string]bool,
	keptByID map[string]state.Memory,
	logDate string,
	logShards []state.MemoryLog,
	logEntries []state.MemoryLogEntry,
	now time.Time,
) error {
	owner := d.user.DataOwner()
	delIDs := make([]string, 0, len(deleted))
	for id := range deleted {
		delIDs = append(delIDs, id)
	}
	slices.Sort(delIDs)
	for _, id := range delIDs {
		if err := tx.Delete(d.memories().Doc(id)); err != nil {
			return err
		}
		if err := tx.DeleteMemoryChunk(owner, docstore.MemoryCoreChunkID(id)); err != nil {
			return err
		}
	}

	upsIDs := make([]string, 0, len(upserted))
	for id := range upserted {
		upsIDs = append(upsIDs, id)
	}
	slices.Sort(upsIDs)
	for _, id := range upsIDs {
		m := keptByID[id]
		if err := tx.Set(d.memories().Doc(id), m); err != nil {
			return err
		}
		if err := tx.UpsertMemoryChunk(coreChunk(owner, m)); err != nil {
			return err
		}
	}

	if len(logEntries) > 0 {
		if err := d.writeDailyLogAppend(tx, logDate, logShards, logEntries, now); err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) commitCoreMutationWithDemotion(
	tx *docstore.Transaction,
	origIDs map[string]bool,
	byID map[string]state.Memory,
	upsertedID string,
	logDate string,
	shards []state.MemoryLog,
	now time.Time,
) ([]state.Memory, error) {
	all := make([]state.Memory, 0, len(byID))
	for _, item := range byID {
		all = append(all, item)
	}
	kept, demoted, err := state.DemoteCoreOverflow(all, state.CoreMemoryBytes)
	if err != nil {
		return nil, err
	}
	keptByID := make(map[string]state.Memory, len(kept))
	for _, item := range kept {
		keptByID[item.ID] = item
	}
	deleted := map[string]bool{}
	upserted := map[string]bool{upsertedID: true}
	demotedEntries := make([]state.MemoryLogEntry, 0, len(demoted))
	for _, item := range demoted {
		delete(upserted, item.ID)
		if origIDs[item.ID] {
			deleted[item.ID] = true
		}
		demotedEntries = append(demotedEntries, demoteCoreToLogEntry(item, now))
	}
	if err := d.applyCoreAndLogWrites(tx, deleted, upserted, keptByID, logDate, shards, demotedEntries, now); err != nil {
		return nil, err
	}
	return demoted, nil
}

func (d *Database) ConversationSummary(ctx context.Context) (state.ConversationSummary, error) {
	var s state.ConversationSummary
	doc, err := d.summary().Get(ctx)
	if notFound(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = doc.DataTo(&s)
	return s, err
}

func (d *Database) SaveConversationSummary(ctx context.Context, s, expected state.ConversationSummary) error {
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		var saved state.ConversationSummary
		doc, err := tx.Get(d.summary())
		if err != nil && !notFound(err) {
			return err
		}
		if err == nil {
			if err = doc.DataTo(&saved); err != nil {
				return err
			}
		}
		control, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		if !control.ContextFrom.Equal(s.From) ||
			!saved.From.Equal(expected.From) ||
			!saved.ExtractedThrough.Equal(expected.ExtractedThrough) ||
			!saved.EffectiveExtractedThrough().Equal(expected.EffectiveExtractedThrough()) ||
			!saved.FlushedThrough.Equal(expected.FlushedThrough) ||
			!saved.Through.Equal(expected.Through) ||
			!saved.UpdatedAt.Equal(expected.UpdatedAt) {
			return state.ErrConversationStale
		}
		return tx.Set(d.summary(), s)
	})
}

func (d *Database) ConversationTurnsAfter(ctx context.Context, after time.Time) ([]state.ConversationTurn, error) {
	return d.turnsAfter(ctx, after, docstore.Desc)
}

func (d *Database) OldestConversationTurnsAfter(ctx context.Context, after time.Time) ([]state.ConversationTurn, error) {
	return d.turnsAfter(ctx, after, docstore.Asc)
}

func (d *Database) turnsAfter(ctx context.Context, after time.Time, dir docstore.Direction) ([]state.ConversationTurn, error) {
	query := d.conversationTurns().OrderBy("started_at", dir).Limit(state.HistoryTurnsExamined)
	if !after.IsZero() {
		query = query.Where("started_at", ">", after)
	}
	it := query.Documents(ctx)
	defer it.Stop()
	return documents[state.ConversationTurn](it)
}

func (d *Database) ReadConversationMessage(ctx context.Context, id string) (state.Message, error) {
	if !state.ValidEvidenceID(id) {
		return state.Message{}, state.ErrNotFound
	}
	if !strings.HasSuffix(id, "-user") && !strings.HasSuffix(id, "-assistant") {
		id = state.UserMessageID(id)
	}
	doc, err := d.messages().Doc(id).Get(ctx)
	if notFound(err) {
		return state.Message{}, state.ErrNotFound
	}
	if err != nil {
		return state.Message{}, err
	}
	var message state.Message
	if err = doc.DataTo(&message); err != nil {
		return message, err
	}
	control, _, err := d.ConversationStatus(ctx)
	if err != nil {
		return state.Message{}, err
	}
	if message.ID != id || message.CreatedAt.Before(control.ContextFrom) {
		return state.Message{}, state.ErrNotFound
	}
	return message, nil
}

func (d *Database) EditMemory(ctx context.Context, id string, expectedUpdated int64, text string) (state.Memory, error) {
	var out state.Memory
	if err := validateCoreMemoryFields(text, "", ""); err != nil {
		return out, err
	}
	err := d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		all, err := d.readAllCoreMemoriesTx(tx)
		if err != nil {
			return err
		}
		byID := make(map[string]state.Memory, len(all))
		for _, m := range all {
			byID[m.ID] = m
		}
		m, exists := byID[id]
		if !exists {
			return state.ErrNotFound
		}
		if m.UpdatedAt.UnixMilli() != expectedUpdated {
			return state.ErrRevisionConflict
		}
		now := d.now()
		logDate := now.UTC().Format("2006-01-02")
		shards, err := d.readDailyLogShards(tx, logDate)
		if err != nil {
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}

		m.Text = text
		m.UpdatedAt = monotonicRevisionTime(now, m.UpdatedAt)
		if m.UpdatedAt.Before(m.CreatedAt) {
			m.UpdatedAt = m.CreatedAt
		}
		byID[id] = m
		out = m

		allAfter := make([]state.Memory, 0, len(byID))
		for _, item := range byID {
			allAfter = append(allAfter, item)
		}
		kept, demoted, err := state.DemoteCoreOverflow(allAfter, state.CoreMemoryBytes)
		if err != nil {
			return err
		}
		keptByID := make(map[string]state.Memory, len(kept))
		for _, item := range kept {
			keptByID[item.ID] = item
		}
		deleted := map[string]bool{}
		upserted := map[string]bool{id: true}
		demotedEntries := make([]state.MemoryLogEntry, 0, len(demoted))
		for _, item := range demoted {
			delete(upserted, item.ID)
			deleted[item.ID] = true
			demotedEntries = append(demotedEntries, demoteCoreToLogEntry(item, now))
		}
		if err := d.applyCoreAndLogWrites(tx, deleted, upserted, keptByID, logDate, shards, demotedEntries, now); err != nil {
			return err
		}
		return log.add(state.EventMemory, "")
	})
	return out, err
}

func (d *Database) ForgetMemory(ctx context.Context, id string, expectedUpdated int64) (state.Memory, error) {
	var out state.Memory
	err := d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(d.memories().Doc(id))
		if notFound(err) {
			return state.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err = doc.DataTo(&out); err != nil {
			return err
		}
		if out.UpdatedAt.UnixMilli() != expectedUpdated {
			return state.ErrRevisionConflict
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		if err := tx.Delete(d.memories().Doc(id)); err != nil {
			return err
		}
		if err := tx.DeleteMemoryChunk(d.user.DataOwner(), docstore.MemoryCoreChunkID(id)); err != nil {
			return err
		}
		return log.add(state.EventMemory, "")
	})
	return out, err
}

func (d *Database) ContextFrom(ctx context.Context) (time.Time, error) {
	control, _, err := d.ConversationStatus(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return control.ContextFrom, nil
}

func (d *Database) CommitFlushAndCompact(ctx context.Context, req state.FlushAndCompactRequest) (state.ConversationSummary, error) {
	return d.commitMemoryCompaction(ctx, req, state.ProvenancePreCompactFlush)
}

func (d *Database) CommitDreamConsolidate(ctx context.Context, req state.DreamConsolidateRequest) (state.ConversationSummary, error) {
	return d.commitMemoryCompaction(ctx, state.FlushAndCompactRequest(req), state.ProvenanceDreamCurated)
}

func (d *Database) commitMemoryCompaction(
	ctx context.Context,
	req state.FlushAndCompactRequest,
	defaultProvenance state.MemoryProvenance,
) (state.ConversationSummary, error) {
	for _, m := range req.CoreUpserts {
		if m.Provenance == state.ProvenanceTaskOutcome {
			return state.ConversationSummary{}, state.ErrInvalidResource
		}
		if !validMemoryID(m.ID) {
			return state.ConversationSummary{}, state.ErrInvalidResource
		}
		if err := validateCoreMemoryFields(m.Text, m.About, m.Section); err != nil {
			return state.ConversationSummary{}, err
		}
	}
	if req.DailyLogDate != "" && !state.ValidMemoryDate(req.DailyLogDate) {
		return state.ConversationSummary{}, state.ErrInvalidResource
	}
	for _, e := range req.DailyLogEntries {
		if strings.TrimSpace(e.ID) == "" || strings.TrimSpace(e.Text) == "" || !utf8.ValidString(e.Text) {
			return state.ConversationSummary{}, state.ErrInvalidResource
		}
	}

	var committed state.ConversationSummary
	err := d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		control, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		var saved state.ConversationSummary
		doc, err := tx.Get(d.summary())
		if err != nil && !notFound(err) {
			return err
		}
		if err == nil {
			if err = doc.DataTo(&saved); err != nil {
				return err
			}
		}

		if !control.ContextFrom.Equal(req.NextSummary.From) {
			return state.ErrConversationStale
		}
		if req.CheckPending && control.Pending() {
			return state.ErrConversationStale
		}
		if !saved.From.Equal(req.ExpectedSummary.From) ||
			!saved.ExtractedThrough.Equal(req.ExpectedSummary.ExtractedThrough) ||
			!saved.EffectiveExtractedThrough().Equal(req.ExpectedSummary.EffectiveExtractedThrough()) ||
			!saved.FlushedThrough.Equal(req.ExpectedSummary.FlushedThrough) ||
			!saved.EffectiveFlushedThrough().Equal(req.ExpectedSummary.EffectiveFlushedThrough()) ||
			!saved.Through.Equal(req.ExpectedSummary.Through) ||
			!saved.UpdatedAt.Equal(req.ExpectedSummary.UpdatedAt) {
			return state.ErrConversationStale
		}

		now := d.now()
		logDate := req.DailyLogDate
		if logDate == "" {
			logDate = now.UTC().Format("2006-01-02")
		}

		existingCore, err := d.readAllCoreMemoriesTx(tx)
		if err != nil {
			return err
		}
		shards, err := d.readDailyLogShards(tx, logDate)
		if err != nil {
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}

		origCoreIDs := make(map[string]bool, len(existingCore))
		coreByID := make(map[string]state.Memory, len(existingCore))
		for _, m := range existingCore {
			origCoreIDs[m.ID] = true
			coreByID[m.ID] = m
		}

		deleted := map[string]bool{}
		upserted := map[string]bool{}

		for _, id := range req.CoreDeletes {
			if _, existed := coreByID[id]; existed {
				delete(coreByID, id)
				deleted[id] = true
			}
		}

		for _, m := range req.CoreUpserts {
			if m.About == "" {
				m.About = "the user"
			}
			if m.Provenance == "" {
				m.Provenance = defaultProvenance
			}
			if existing, existed := coreByID[m.ID]; existed {
				if m.CreatedAt.IsZero() {
					m.CreatedAt = existing.CreatedAt
				}
				if existing.Pinned {
					m.Pinned = true
				}
			}
			if m.CreatedAt.IsZero() {
				m.CreatedAt = now
			}
			if m.UpdatedAt.IsZero() {
				m.UpdatedAt = now
			}
			if m.UpdatedAt.Before(m.CreatedAt) {
				m.UpdatedAt = m.CreatedAt
			}
			coreByID[m.ID] = m
			upserted[m.ID] = true
			delete(deleted, m.ID)
		}

		allCore := make([]state.Memory, 0, len(coreByID))
		for _, m := range coreByID {
			allCore = append(allCore, m)
		}
		kept, demoted, err := state.DemoteCoreOverflow(allCore, state.CoreMemoryBytes)
		if err != nil {
			return err
		}
		keptByID := make(map[string]state.Memory, len(kept))
		for _, m := range kept {
			keptByID[m.ID] = m
		}
		logEntries := slices.Clone(req.DailyLogEntries)
		for _, m := range demoted {
			delete(upserted, m.ID)
			if origCoreIDs[m.ID] {
				deleted[m.ID] = true
			}
			logEntries = append(logEntries, demoteCoreToLogEntry(m, now))
		}

		nextSummary := req.NextSummary
		if nextSummary.UpdatedAt.IsZero() {
			nextSummary.UpdatedAt = now
		}

		if err := d.applyCoreAndLogWrites(tx, deleted, upserted, keptByID, logDate, shards, logEntries, now); err != nil {
			return err
		}
		if err := tx.Set(d.summary(), nextSummary); err != nil {
			return err
		}
		if err := log.add(state.EventMemory, ""); err != nil {
			return err
		}
		committed = nextSummary
		return nil
	})
	return committed, err
}

func monotonicRevisionTime(now, prev time.Time) time.Time {
	nowUTC := now.UTC()
	if !prev.IsZero() && nowUTC.UnixMilli() <= prev.UnixMilli() {
		return time.UnixMilli(prev.UnixMilli() + 1).UTC()
	}
	return nowUTC
}
