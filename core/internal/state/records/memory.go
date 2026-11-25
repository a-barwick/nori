package records

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
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

// Memories returns every saved Core memory in deterministic prompt order
// (Pinned DESC, CreatedAt ASC, ID ASC).
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

// MemoryLogs reads owners/<owner>/memory_logs/*, merges shards per date, and
// returns Daily Logs newest-date-first. When afterDate is non-empty, only dates
// >= afterDate are included.
func (d *Database) MemoryLogs(ctx context.Context, afterDate string, limit int) ([]state.MemoryLog, error) {
	q := d.memoryLogs().OrderBy(docstore.DocumentID, docstore.Desc)
	if afterDate != "" {
		q = q.Where(docstore.DocumentID, ">=", afterDate)
	}
	docs, err := q.Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	byDate := map[string][]state.MemoryLog{}
	for _, doc := range docs {
		var shard state.MemoryLog
		if err := doc.DataTo(&shard); err != nil {
			return nil, err
		}
		if shard.Date == "" {
			shard.Date = parseLogDateFromDocID(doc.Ref.ID)
		}
		if afterDate != "" && shard.Date < afterDate {
			continue
		}
		shard.Shard = effectiveLogShard(shard.Shard, doc.Ref.ID)
		byDate[shard.Date] = append(byDate[shard.Date], shard)
	}

	dates := make([]string, 0, len(byDate))
	for date := range byDate {
		dates = append(dates, date)
	}
	slices.SortFunc(dates, func(a, b string) int { return cmp.Compare(b, a) })

	out := make([]state.MemoryLog, 0, len(dates))
	for _, date := range dates {
		merged, ok := mergeDailyLogShards(date, byDate[date])
		if !ok {
			continue
		}
		out = append(out, merged)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// MemoryLog reads and merges all shards for one YYYY-MM-DD Daily Log.
func (d *Database) MemoryLog(ctx context.Context, date string) (state.MemoryLog, error) {
	if !state.ValidMemoryDate(date) {
		return state.MemoryLog{}, state.ErrNotFound
	}
	docs, err := d.memoryLogs().
		Where(docstore.DocumentID, ">=", date).
		Where(docstore.DocumentID, "<", date+"$").
		OrderBy(docstore.DocumentID, docstore.Asc).
		Documents(ctx).GetAll()
	if err != nil {
		return state.MemoryLog{}, err
	}
	shards := make([]state.MemoryLog, 0, len(docs))
	for _, doc := range docs {
		var shard state.MemoryLog
		if err := doc.DataTo(&shard); err != nil {
			return state.MemoryLog{}, err
		}
		if shard.Date == "" {
			shard.Date = parseLogDateFromDocID(doc.Ref.ID)
		}
		if shard.Date != date {
			continue
		}
		shard.Shard = effectiveLogShard(shard.Shard, doc.Ref.ID)
		shards = append(shards, shard)
	}
	merged, ok := mergeDailyLogShards(date, shards)
	if !ok {
		return state.MemoryLog{}, state.ErrNotFound
	}
	return merged, nil
}

func parseLogDateFromDocID(docID string) string {
	date, _, _ := strings.Cut(docID, "#")
	return date
}

func effectiveLogShard(shard int, docID string) int {
	if shard > 0 {
		return shard
	}
	if _, suffix, ok := strings.Cut(docID, "#"); ok {
		if n, err := strconv.Atoi(suffix); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

func mergeDailyLogShards(date string, shards []state.MemoryLog) (state.MemoryLog, bool) {
	if len(shards) == 0 {
		return state.MemoryLog{}, false
	}
	ordered := slices.Clone(shards)
	slices.SortFunc(ordered, func(a, b state.MemoryLog) int {
		return cmp.Compare(a.Shard, b.Shard)
	})
	var entries []state.MemoryLogEntry
	var maxUpdated time.Time
	for _, s := range ordered {
		entries = append(entries, s.Entries...)
		if s.UpdatedAt.After(maxUpdated) {
			maxUpdated = s.UpdatedAt
		}
	}
	if len(entries) == 0 {
		return state.MemoryLog{}, false
	}
	return state.MemoryLog{
		Date:      date,
		Shard:     1,
		Entries:   entries,
		UpdatedAt: maxUpdated,
	}, true
}

func (d *Database) readAllCoreMemoriesTx(tx *docstore.Transaction) ([]state.Memory, error) {
	it := tx.Documents(d.memories().Query)
	defer it.Stop()
	return documents[state.Memory](it)
}

func (d *Database) readDailyLogShards(tx *docstore.Transaction, date string) ([]state.MemoryLog, error) {
	if !state.ValidMemoryDate(date) {
		return nil, state.ErrInvalidResource
	}
	it := tx.Documents(d.memoryLogs().
		Where(docstore.DocumentID, ">=", date).
		Where(docstore.DocumentID, "<", date+"$").
		OrderBy(docstore.DocumentID, docstore.Asc))
	defer it.Stop()
	docs, err := it.GetAll()
	if err != nil {
		return nil, err
	}
	shards := make([]state.MemoryLog, 0, len(docs))
	for _, doc := range docs {
		var shard state.MemoryLog
		if err := doc.DataTo(&shard); err != nil {
			return nil, err
		}
		if shard.Date == "" {
			shard.Date = parseLogDateFromDocID(doc.Ref.ID)
		}
		if shard.Date != date {
			continue
		}
		shard.Shard = effectiveLogShard(shard.Shard, doc.Ref.ID)
		shards = append(shards, shard)
	}
	slices.SortFunc(shards, func(a, b state.MemoryLog) int {
		return cmp.Compare(a.Shard, b.Shard)
	})
	return shards, nil
}

func (d *Database) writeDailyLogAppend(
	tx *docstore.Transaction,
	date string,
	existing []state.MemoryLog,
	incoming []state.MemoryLogEntry,
	now time.Time,
) error {
	if len(incoming) == 0 {
		return nil
	}
	shards := make([]state.MemoryLog, len(existing))
	seen := map[string]bool{}
	for i, s := range existing {
		cp := s
		cp.Entries = slices.Clone(s.Entries)
		if cp.Date == "" {
			cp.Date = date
		}
		if cp.Shard <= 0 {
			cp.Shard = i + 1
		}
		shards[i] = cp
		for _, e := range cp.Entries {
			seen[e.ID] = true
		}
	}

	nowUTC := now.UTC()
	dirty := map[int]bool{}
	added := make([]state.MemoryLogEntry, 0, len(incoming))

	for _, entry := range incoming {
		if strings.TrimSpace(entry.ID) == "" || strings.TrimSpace(entry.Text) == "" {
			continue
		}
		if seen[entry.ID] {
			continue
		}
		if entry.CreatedAt.IsZero() {
			entry.CreatedAt = nowUTC
		} else {
			entry.CreatedAt = entry.CreatedAt.UTC()
		}
		if entry.UpdatedAt.IsZero() || entry.UpdatedAt.Before(entry.CreatedAt) {
			entry.UpdatedAt = entry.CreatedAt
		} else {
			entry.UpdatedAt = entry.UpdatedAt.UTC()
		}
		if entry.Provenance == "" {
			entry.Provenance = state.ProvenancePreCompactFlush
		}
		seen[entry.ID] = true

		needNewShard := len(shards) == 0
		if !needNewShard {
			last := &shards[len(shards)-1]
			if len(last.Entries) >= state.MaxDailyLogEntries {
				needNewShard = true
			} else if len(last.Entries) > 0 && dailyLogShardSizeWith(*last, entry, nowUTC) > state.MaxDailyLogBytes {
				needNewShard = true
			}
		}
		if needNewShard {
			nextShard := 1
			if len(shards) > 0 {
				nextShard = shards[len(shards)-1].Shard + 1
			}
			shards = append(shards, state.MemoryLog{
				Date:      date,
				Shard:     nextShard,
				Entries:   []state.MemoryLogEntry{entry},
				UpdatedAt: nowUTC,
			})
			dirty[len(shards)-1] = true
		} else {
			lastIdx := len(shards) - 1
			shards[lastIdx].Entries = append(shards[lastIdx].Entries, entry)
			shards[lastIdx].UpdatedAt = nowUTC
			dirty[lastIdx] = true
		}
		added = append(added, entry)
	}

	if len(added) == 0 {
		return nil
	}

	for i, shard := range shards {
		if !dirty[i] {
			continue
		}
		if err := tx.Set(d.memoryLogs().Doc(state.MemoryLogDocID(date, shard.Shard)), shard); err != nil {
			return err
		}
	}
	owner := d.user.DataOwner()
	for _, entry := range added {
		if err := tx.UpsertMemoryChunk(logEntryChunk(owner, date, entry)); err != nil {
			return err
		}
	}
	return nil
}

func dailyLogShardSizeWith(shard state.MemoryLog, extra state.MemoryLogEntry, updatedAt time.Time) int {
	candidate := state.MemoryLog{
		Date:      shard.Date,
		Shard:     shard.Shard,
		Entries:   append(slices.Clone(shard.Entries), extra),
		UpdatedAt: updatedAt,
	}
	raw, _ := json.Marshal(candidate)
	return len(raw)
}

func coreChunk(owner string, m state.Memory) docstore.MemoryChunk {
	createdAt := m.CreatedAt.UTC()
	updatedAt := m.UpdatedAt.UTC()
	if updatedAt.IsZero() || updatedAt.Before(createdAt) {
		updatedAt = createdAt
	}
	return docstore.MemoryChunk{
		Owner:       owner,
		ChunkID:     docstore.MemoryCoreChunkID(m.ID),
		Layer:       docstore.MemoryLayerCore,
		Section:     m.Section,
		About:       m.About,
		Body:        m.Text,
		Provenance:  string(m.EffectiveProvenance()),
		SourceInput: m.SourceInputID,
		Pinned:      m.Pinned,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
}

func logEntryChunk(owner, date string, e state.MemoryLogEntry) docstore.MemoryChunk {
	createdAt := e.CreatedAt.UTC()
	updatedAt := e.UpdatedAt.UTC()
	if updatedAt.IsZero() || updatedAt.Before(createdAt) {
		updatedAt = createdAt
	}
	prov := e.Provenance
	if prov == "" {
		prov = state.ProvenancePreCompactFlush
	}
	return docstore.MemoryChunk{
		Owner:       owner,
		ChunkID:     docstore.MemoryLogChunkID(date, e.ID),
		Layer:       docstore.MemoryLayerLog,
		LogDate:     date,
		Section:     e.Section,
		About:       e.About,
		Body:        e.Text,
		Provenance:  string(prov),
		SourceInput: e.SourceInputID,
		RunID:       e.RunID,
		Pinned:      false,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}
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

// readMemoryChanges reads all Core memories and today's Daily Log doc before
// PublishConversation writes anything (Firestore read-before-write rule).
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

// writeMemoryChanges applies a turn's remember/forget changes inside its publish
// transaction, auto-demoting unpinned Core overflow into today's Daily Log,
// syncing nori_memory_chunks, and emitting EventMemory.
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

// SaveConversationSummary replaces the summary unless a newer one is already saved: one for a later context, or one
// that covers more of this context.
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
		// A summary is derived from one exact predecessor and context cutoff.
		// A later summarizer or reset cannot be overwritten by stale maintenance.
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

// ConversationTurnsAfter returns up to HistoryTurnsExamined turns that started after a time, newest first. A zero time
// returns the newest turns.
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

// ReadConversationMessage accepts visible message IDs and raw summary input IDs.
// The retained-context cutoff is enforced in storage, including for task tools.
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

// EditMemory replaces a memory's text if it has not changed since updatedAt (the client's revision).
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

// ForgetMemory deletes a memory if it has not changed since updatedAt. It removes the memory from future use; it
// does not touch transcripts.
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

func (d *Database) SearchMemory(ctx context.Context, query docstore.MemorySearchQuery) ([]docstore.MemorySearchHit, error) {
	query.Owner = d.user.DataOwner()
	if query.Now.IsZero() {
		query.Now = d.now()
	}
	if hits, ok, err := d.db.SearchMemory(ctx, query); ok {
		return hits, err
	}
	return []docstore.MemorySearchHit{}, nil
}

// CommitFlushAndCompact atomically commits extracted Core memories, Daily Log
// entries, summary eviction archives, FTS chunks, and the next ConversationSummary.
func (d *Database) CommitFlushAndCompact(ctx context.Context, req state.FlushAndCompactRequest) (state.ConversationSummary, error) {
	return d.commitMemoryCompaction(ctx, req, state.ProvenancePreCompactFlush)
}

// CommitDreamConsolidate atomically commits an idle AutoDream consolidation pass.
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

func memoryMutationBinding(req state.MemoryMutationRequest) string {
	data, _ := json.Marshal([]any{
		"memory_mutation",
		req.Kind,
		req.TargetID,
		req.Date,
		req.ExpectedRevision,
		req.Text,
		req.About,
		req.Section,
		req.Pinned,
	})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func deterministicMemoryID(prefix, seed string) string {
	sum := sha256.Sum256([]byte(prefix + ":" + seed))
	return "mem_" + hex.EncodeToString(sum[:8])
}

// MutateMemory performs an idempotent, receipt-backed transactional mutation across
// Core Memory, Daily Logs, or Working Memory, syncing nori_memory_chunks and logging EventMemory.
func (d *Database) MutateMemory(ctx context.Context, req state.MemoryMutationRequest) (state.MutationReceipt, error) {
	if !state.ValidResourceID(req.RequestID) {
		return state.MutationReceipt{}, state.ErrInvalidResource
	}
	binding := req.Binding
	if binding == "" {
		binding = memoryMutationBinding(req)
	}
	var receipt state.MutationReceipt

	err := d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		receiptRef := d.receipts().Doc(req.RequestID)
		savedReceipt, err := tx.Get(receiptRef)
		if err == nil {
			prior, err := toReceipt(savedReceipt)
			if err != nil {
				return err
			}
			if prior.Binding != binding {
				return state.ErrIdempotencyConflict
			}
			receipt = prior
			return nil
		}
		if !notFound(err) {
			return err
		}

		now := d.now()
		todayDate := now.UTC().Format("2006-01-02")
		owner := d.user.DataOwner()
		httpStatus := 200
		var result state.MemoryMutationResult

		switch req.Kind {
		case state.MemoryMutateCreateCore:
			if req.ExpectedRevision != 0 {
				return state.ErrRevisionConflict
			}
			if err := validateCoreMemoryFields(req.Text, req.About, req.Section); err != nil {
				return err
			}
			targetID := req.TargetID
			if targetID == "" {
				targetID = deterministicMemoryID("create_core", req.RequestID)
			} else if !validMemoryID(targetID) {
				return state.ErrInvalidResource
			}
			existingCore, err := d.readAllCoreMemoriesTx(tx)
			if err != nil {
				return err
			}
			byID := make(map[string]state.Memory, len(existingCore)+1)
			origIDs := make(map[string]bool, len(existingCore))
			for _, m := range existingCore {
				byID[m.ID] = m
				origIDs[m.ID] = true
			}
			if existing, exists := byID[req.ReplacesID]; req.ReplacesID != "" && exists {
				shards, err := d.readDailyLogShards(tx, todayDate)
				if err != nil {
					return err
				}
				log, err := d.openLog(tx)
				if err != nil {
					return err
				}
				m := existing
				m.Text = req.Text
				m.About = cmp.Or(req.About, existing.About, "the user")
				m.Section = cmp.Or(req.Section, existing.Section)
				if req.Pinned != nil {
					m.Pinned = *req.Pinned
				}
				m.Provenance = state.ProvenanceUserExplicit
				m.SourceInputID = "ui"
				if req.SourceText != "" {
					m.SourceText = state.BoundedSource(req.SourceText)
				}
				m.UpdatedAt = monotonicRevisionTime(now, existing.UpdatedAt)
				if m.UpdatedAt.Before(m.CreatedAt) {
					m.UpdatedAt = m.CreatedAt
				}
				byID[m.ID] = m
				demoted, err := d.commitCoreMutationWithDemotion(tx, origIDs, byID, m.ID, todayDate, shards, now)
				if err != nil {
					return err
				}
				if err := log.add(state.EventMemory, ""); err != nil {
					return err
				}
				result = state.MemoryMutationResult{Memory: &m, Demoted: demoted}
				httpStatus = 200
				break
			}
			if _, exists := byID[targetID]; exists {
				return state.ErrRevisionConflict
			}
			shards, err := d.readDailyLogShards(tx, todayDate)
			if err != nil {
				return err
			}
			log, err := d.openLog(tx)
			if err != nil {
				return err
			}

			m := state.Memory{
				ID:            targetID,
				Text:          req.Text,
				About:         cmp.Or(req.About, "the user"),
				Section:       req.Section,
				Pinned:        req.Pinned != nil && *req.Pinned,
				Provenance:    state.ProvenanceUserExplicit,
				SourceInputID: "ui",
				SourceText:    state.BoundedSource(req.SourceText),
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			byID[m.ID] = m
			demoted, err := d.commitCoreMutationWithDemotion(tx, origIDs, byID, m.ID, todayDate, shards, now)
			if err != nil {
				return err
			}
			if err := log.add(state.EventMemory, ""); err != nil {
				return err
			}
			result = state.MemoryMutationResult{Memory: &m, Demoted: demoted}
			httpStatus = 201

		case state.MemoryMutateEditCore:
			if !validMemoryID(req.TargetID) {
				return state.ErrNotFound
			}
			if err := validateCoreMemoryFields(req.Text, req.About, req.Section); err != nil {
				return err
			}
			existingCore, err := d.readAllCoreMemoriesTx(tx)
			if err != nil {
				return err
			}
			byID := make(map[string]state.Memory, len(existingCore))
			origIDs := make(map[string]bool, len(existingCore))
			for _, m := range existingCore {
				byID[m.ID] = m
				origIDs[m.ID] = true
			}
			m, exists := byID[req.TargetID]
			if !exists {
				return state.ErrNotFound
			}
			if m.UpdatedAt.UnixMilli() != req.ExpectedRevision {
				return state.ErrRevisionConflict
			}
			shards, err := d.readDailyLogShards(tx, todayDate)
			if err != nil {
				return err
			}
			log, err := d.openLog(tx)
			if err != nil {
				return err
			}

			m.Text = req.Text
			if req.About != "" {
				m.About = req.About
			}
			if req.Section != "" {
				m.Section = req.Section
			}
			if req.Pinned != nil {
				m.Pinned = *req.Pinned
			}
			m.UpdatedAt = monotonicRevisionTime(now, m.UpdatedAt)
			if m.UpdatedAt.Before(m.CreatedAt) {
				m.UpdatedAt = m.CreatedAt
			}
			byID[m.ID] = m
			demoted, err := d.commitCoreMutationWithDemotion(tx, origIDs, byID, m.ID, todayDate, shards, now)
			if err != nil {
				return err
			}
			if err := log.add(state.EventMemory, ""); err != nil {
				return err
			}
			result = state.MemoryMutationResult{Memory: &m, Demoted: demoted}

		case state.MemoryMutateForgetCore:
			if !validMemoryID(req.TargetID) {
				return state.ErrNotFound
			}
			doc, err := tx.Get(d.memories().Doc(req.TargetID))
			if notFound(err) {
				return state.ErrNotFound
			}
			if err != nil {
				return err
			}
			var m state.Memory
			if err := doc.DataTo(&m); err != nil {
				return err
			}
			if m.UpdatedAt.UnixMilli() != req.ExpectedRevision {
				return state.ErrRevisionConflict
			}
			log, err := d.openLog(tx)
			if err != nil {
				return err
			}
			if err := tx.Delete(d.memories().Doc(m.ID)); err != nil {
				return err
			}
			if err := tx.DeleteMemoryChunk(owner, docstore.MemoryCoreChunkID(m.ID)); err != nil {
				return err
			}
			if err := log.add(state.EventMemory, ""); err != nil {
				return err
			}
			result = state.MemoryMutationResult{
				Memory: &m,
				Note:   "Nori will no longer use this. Conversation history and external documents are unchanged.",
			}

		case state.MemoryMutatePinCore, state.MemoryMutateUnpinCore:
			if !validMemoryID(req.TargetID) {
				return state.ErrNotFound
			}
			existingCore, err := d.readAllCoreMemoriesTx(tx)
			if err != nil {
				return err
			}
			byID := make(map[string]state.Memory, len(existingCore))
			for _, m := range existingCore {
				byID[m.ID] = m
			}
			m, exists := byID[req.TargetID]
			if !exists {
				return state.ErrNotFound
			}
			if m.UpdatedAt.UnixMilli() != req.ExpectedRevision {
				return state.ErrRevisionConflict
			}
			m.Pinned = req.Kind == state.MemoryMutatePinCore
			m.UpdatedAt = monotonicRevisionTime(now, m.UpdatedAt)
			if m.UpdatedAt.Before(m.CreatedAt) {
				m.UpdatedAt = m.CreatedAt
			}
			byID[m.ID] = m
			if m.Pinned {
				pinnedBytes := 0
				for _, item := range byID {
					if item.Pinned {
						pinnedBytes += len(item.Text)
					}
				}
				if pinnedBytes > state.CoreMemoryBytes {
					return state.ErrInvalidResource
				}
			}
			log, err := d.openLog(tx)
			if err != nil {
				return err
			}
			if err := tx.Set(d.memories().Doc(m.ID), m); err != nil {
				return err
			}
			if err := tx.UpsertMemoryChunk(coreChunk(owner, m)); err != nil {
				return err
			}
			if err := log.add(state.EventMemory, ""); err != nil {
				return err
			}
			result = state.MemoryMutationResult{Memory: &m}

		case state.MemoryMutateDemoteCore:
			if !validMemoryID(req.TargetID) {
				return state.ErrNotFound
			}
			logDate := req.Date
			if logDate == "" {
				logDate = todayDate
			}
			if !state.ValidMemoryDate(logDate) {
				return state.ErrInvalidResource
			}
			doc, err := tx.Get(d.memories().Doc(req.TargetID))
			if notFound(err) {
				return state.ErrNotFound
			}
			if err != nil {
				return err
			}
			var m state.Memory
			if err := doc.DataTo(&m); err != nil {
				return err
			}
			if m.UpdatedAt.UnixMilli() != req.ExpectedRevision {
				return state.ErrRevisionConflict
			}
			shards, err := d.readDailyLogShards(tx, logDate)
			if err != nil {
				return err
			}
			log, err := d.openLog(tx)
			if err != nil {
				return err
			}

			entry := demoteCoreToLogEntry(m, now)
			if dailyLogContainsEntryID(shards, entry.ID) {
				sum := sha256.Sum256([]byte(req.RequestID))
				entry.ID = entry.ID + "_" + hex.EncodeToString(sum[:4])
			}
			if err := tx.Delete(d.memories().Doc(m.ID)); err != nil {
				return err
			}
			if err := tx.DeleteMemoryChunk(owner, docstore.MemoryCoreChunkID(m.ID)); err != nil {
				return err
			}
			if err := d.writeDailyLogAppend(tx, logDate, shards, []state.MemoryLogEntry{entry}, now); err != nil {
				return err
			}
			if err := log.add(state.EventMemory, ""); err != nil {
				return err
			}
			result = state.MemoryMutationResult{Memory: &m, LogEntry: &entry}

		case state.MemoryMutatePromoteLog:
			if strings.TrimSpace(req.TargetID) == "" || !state.ValidMemoryDate(req.Date) {
				return state.ErrInvalidResource
			}
			shards, err := d.readDailyLogShards(tx, req.Date)
			if err != nil {
				return err
			}
			entry, logRev, found := findDailyLogEntry(shards, req.TargetID)
			if !found {
				return state.ErrNotFound
			}
			if req.ExpectedRevision != logRev && req.ExpectedRevision != entry.UpdatedAt.UnixMilli() {
				return state.ErrRevisionConflict
			}
			reauthored := strings.TrimSpace(req.Text) != ""
			if entry.Provenance == state.ProvenanceTaskOutcome && !reauthored {
				return state.ErrInvalidResource
			}
			text := entry.Text
			sourceInputID := cmp.Or(entry.SourceInputID, "ui")
			sourceText := entry.SourceText
			if reauthored {
				text = req.Text
				sourceInputID = "ui"
			}
			about := cmp.Or(req.About, entry.About, "the user")
			section := cmp.Or(req.Section, entry.Section)
			if err := validateCoreMemoryFields(text, about, section); err != nil {
				return err
			}

			existingCore, err := d.readAllCoreMemoriesTx(tx)
			if err != nil {
				return err
			}
			byID := make(map[string]state.Memory, len(existingCore)+1)
			origIDs := make(map[string]bool, len(existingCore))
			for _, item := range existingCore {
				byID[item.ID] = item
				origIDs[item.ID] = true
			}
			demoteShards := shards
			if todayDate != req.Date {
				demoteShards, err = d.readDailyLogShards(tx, todayDate)
				if err != nil {
					return err
				}
			}
			log, err := d.openLog(tx)
			if err != nil {
				return err
			}

			m := state.Memory{
				ID:            deterministicMemoryID("promote_log", req.RequestID),
				Text:          text,
				About:         about,
				Section:       section,
				Pinned:        req.Pinned != nil && *req.Pinned,
				Provenance:    state.ProvenanceUserExplicit,
				SourceInputID: sourceInputID,
				SourceText:    sourceText,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			// Deduplicate repeat promotions of the same Daily Log entry so INV-MEM-05 holds
			// even across distinct client request IDs.
			if sourceInputID != "" && sourceInputID != "ui" {
				for _, item := range existingCore {
					if item.EffectiveProvenance() == state.ProvenanceUserExplicit && item.SourceInputID == sourceInputID && item.Text == text {
						m.ID = item.ID
						m.CreatedAt = item.CreatedAt
						if req.Pinned == nil {
							m.Pinned = item.Pinned
						}
						m.UpdatedAt = monotonicRevisionTime(now, item.UpdatedAt)
						break
					}
				}
			}
			byID[m.ID] = m
			demoted, err := d.commitCoreMutationWithDemotion(tx, origIDs, byID, m.ID, todayDate, demoteShards, now)
			if err != nil {
				return err
			}
			if err := log.add(state.EventMemory, ""); err != nil {
				return err
			}
			result = state.MemoryMutationResult{Memory: &m, Demoted: demoted, LogEntry: &entry}

		case state.MemoryMutateForgetLog:
			if strings.TrimSpace(req.TargetID) == "" || !state.ValidMemoryDate(req.Date) {
				return state.ErrInvalidResource
			}
			shards, err := d.readDailyLogShards(tx, req.Date)
			if err != nil {
				return err
			}
			shardIdx, entryIdx, logRev, found := findDailyLogEntryPos(shards, req.TargetID)
			if !found {
				return state.ErrNotFound
			}
			entry := shards[shardIdx].Entries[entryIdx]
			if req.ExpectedRevision != logRev && req.ExpectedRevision != entry.UpdatedAt.UnixMilli() {
				return state.ErrRevisionConflict
			}
			log, err := d.openLog(tx)
			if err != nil {
				return err
			}

			targetShard := shards[shardIdx]
			targetShard.Entries = append(slices.Clone(targetShard.Entries[:entryIdx]), targetShard.Entries[entryIdx+1:]...)
			shardRef := d.memoryLogs().Doc(state.MemoryLogDocID(req.Date, targetShard.Shard))
			if len(targetShard.Entries) == 0 {
				if err := tx.Delete(shardRef); err != nil {
					return err
				}
			} else {
				targetShard.UpdatedAt = monotonicRevisionTime(now, targetShard.UpdatedAt)
				if err := tx.Set(shardRef, targetShard); err != nil {
					return err
				}
			}
			if err := tx.DeleteMemoryChunk(owner, docstore.MemoryLogChunkID(req.Date, entry.ID)); err != nil {
				return err
			}
			if err := log.add(state.EventMemory, ""); err != nil {
				return err
			}
			result = state.MemoryMutationResult{
				LogEntry: &entry,
				Note:     "Nori will no longer use this. Conversation history and external documents are unchanged.",
			}

		case state.MemoryMutatePromoteWorking:
			if strings.TrimSpace(req.TargetID) == "" {
				return state.ErrInvalidResource
			}
			summaryDoc, err := tx.Get(d.summary())
			if notFound(err) {
				return state.ErrNotFound
			}
			if err != nil {
				return err
			}
			var sum state.ConversationSummary
			if err := summaryDoc.DataTo(&sum); err != nil {
				return err
			}
			if sum.UpdatedAt.UnixMilli() != req.ExpectedRevision {
				return state.ErrRevisionConflict
			}
			itemIdx := -1
			for i, item := range sum.Items {
				if state.SummaryItemID(item) == req.TargetID {
					itemIdx = i
					break
				}
			}
			if itemIdx < 0 {
				return state.ErrNotFound
			}
			item := sum.Items[itemIdx]
			reauthored := strings.TrimSpace(req.Text) != ""
			text := item.Quote
			sourceInputID := cmp.Or(item.InputID, "ui")
			if reauthored {
				text = req.Text
				sourceInputID = "ui"
			}
			about := cmp.Or(req.About, "the user")
			section := req.Section
			if err := validateCoreMemoryFields(text, about, section); err != nil {
				return err
			}

			existingCore, err := d.readAllCoreMemoriesTx(tx)
			if err != nil {
				return err
			}
			byID := make(map[string]state.Memory, len(existingCore)+1)
			origIDs := make(map[string]bool, len(existingCore))
			for _, cm := range existingCore {
				byID[cm.ID] = cm
				origIDs[cm.ID] = true
			}
			shards, err := d.readDailyLogShards(tx, todayDate)
			if err != nil {
				return err
			}
			log, err := d.openLog(tx)
			if err != nil {
				return err
			}

			m := state.Memory{
				ID:            deterministicMemoryID("promote_working", req.RequestID),
				Text:          text,
				About:         about,
				Section:       section,
				Pinned:        req.Pinned != nil && *req.Pinned,
				Provenance:    state.ProvenanceUserExplicit,
				SourceInputID: sourceInputID,
				SourceText:    state.BoundedSource(item.Quote),
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			// Deduplicate repeat promotions of the same Working Memory item so INV-MEM-05 holds
			// even across distinct client request IDs.
			if sourceInputID != "" && sourceInputID != "ui" {
				for _, cm := range existingCore {
					if cm.EffectiveProvenance() == state.ProvenanceUserExplicit && cm.SourceInputID == sourceInputID && cm.Text == text {
						m.ID = cm.ID
						m.CreatedAt = cm.CreatedAt
						if req.Pinned == nil {
							m.Pinned = cm.Pinned
						}
						m.UpdatedAt = monotonicRevisionTime(now, cm.UpdatedAt)
						break
					}
				}
			}
			byID[m.ID] = m
			demoted, err := d.commitCoreMutationWithDemotion(tx, origIDs, byID, m.ID, todayDate, shards, now)
			if err != nil {
				return err
			}
			if err := log.add(state.EventMemory, ""); err != nil {
				return err
			}
			result = state.MemoryMutationResult{Memory: &m, Demoted: demoted, Summary: &sum}

		case state.MemoryMutateDismissWorking:
			if strings.TrimSpace(req.TargetID) == "" {
				return state.ErrInvalidResource
			}
			summaryDoc, err := tx.Get(d.summary())
			if notFound(err) {
				return state.ErrNotFound
			}
			if err != nil {
				return err
			}
			var sum state.ConversationSummary
			if err := summaryDoc.DataTo(&sum); err != nil {
				return err
			}
			if sum.UpdatedAt.UnixMilli() != req.ExpectedRevision {
				return state.ErrRevisionConflict
			}
			itemIdx := -1
			for i, item := range sum.Items {
				if state.SummaryItemID(item) == req.TargetID {
					itemIdx = i
					break
				}
			}
			if itemIdx < 0 {
				return state.ErrNotFound
			}
			existingCore, err := d.readAllCoreMemoriesTx(tx)
			if err != nil {
				return err
			}
			control, err := d.readConversation(tx)
			if err != nil {
				return err
			}
			log, err := d.openLog(tx)
			if err != nil {
				return err
			}
			sum.Items = append(slices.Clone(sum.Items[:itemIdx]), sum.Items[itemIdx+1:]...)
			if !slices.Contains(sum.DismissedItemIDs, req.TargetID) {
				sum.DismissedItemIDs = append(sum.DismissedItemIDs, req.TargetID)
			}
			sum.UpdatedAt = monotonicRevisionTime(now, sum.UpdatedAt)
			if err := tx.Set(d.summary(), sum); err != nil {
				return err
			}
			if err := log.add(state.EventMemory, ""); err != nil {
				return err
			}
			result = state.MemoryMutationResult{
				Summary:     &sum,
				CoreBytes:   state.CoreMemorySize(existingCore),
				ContextFrom: control.ContextFrom,
			}

		default:
			return state.ErrInvalidResource
		}

		var resourceJSON json.RawMessage
		if req.FormatResource != nil {
			resourceJSON, err = req.FormatResource(result)
			if err != nil {
				return err
			}
		} else {
			resourceJSON, err = json.Marshal(result)
			if err != nil {
				return err
			}
		}

		receipt = state.MutationReceipt{
			RequestID:  req.RequestID,
			Binding:    binding,
			Accepted:   true,
			Status:     "applied",
			Resource:   resourceJSON,
			HTTPStatus: httpStatus,
			CreatedAt:  now,
		}
		stored := receiptDoc{
			RequestID:  req.RequestID,
			Binding:    binding,
			Status:     receipt.Status,
			Resource:   string(receipt.Resource),
			HTTPStatus: receipt.HTTPStatus,
			CreatedAt:  now,
		}
		return tx.Create(receiptRef, stored)
	})
	if errors.Is(err, docstore.ErrExists) {
		saved, readErr := d.Receipt(ctx, req.RequestID)
		if readErr != nil {
			return state.MutationReceipt{}, err
		}
		if saved.Binding != binding {
			return state.MutationReceipt{}, state.ErrIdempotencyConflict
		}
		return saved, nil
	}
	return receipt, err
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

func dailyLogContainsEntryID(shards []state.MemoryLog, entryID string) bool {
	for _, s := range shards {
		for _, e := range s.Entries {
			if e.ID == entryID {
				return true
			}
		}
	}
	return false
}

func findDailyLogEntryPos(shards []state.MemoryLog, entryID string) (shardIdx, entryIdx int, logRev int64, found bool) {
	var maxUpdated time.Time
	shardIdx, entryIdx = -1, -1
	for i, s := range shards {
		if s.UpdatedAt.After(maxUpdated) {
			maxUpdated = s.UpdatedAt
		}
		for j, e := range s.Entries {
			if e.ID == entryID {
				shardIdx, entryIdx, found = i, j, true
			}
		}
	}
	return shardIdx, entryIdx, maxUpdated.UnixMilli(), found
}

func findDailyLogEntry(shards []state.MemoryLog, entryID string) (state.MemoryLogEntry, int64, bool) {
	si, ei, rev, ok := findDailyLogEntryPos(shards, entryID)
	if !ok {
		return state.MemoryLogEntry{}, rev, false
	}
	return shards[si].Entries[ei], rev, true
}

// SaveMemoryActionReceipt idempotently persists a durable MutationReceipt for
// out-of-band actions such as manual memory consolidation.
func (d *Database) SaveMemoryActionReceipt(ctx context.Context, requestID, binding string, resource json.RawMessage) (state.MutationReceipt, error) {
	if !state.ValidResourceID(requestID) || strings.TrimSpace(binding) == "" {
		return state.MutationReceipt{}, state.ErrInvalidResource
	}
	var receipt state.MutationReceipt
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		receiptRef := d.receipts().Doc(requestID)
		saved, err := tx.Get(receiptRef)
		if err == nil {
			prior, err := toReceipt(saved)
			if err != nil {
				return err
			}
			if prior.Binding != binding {
				return state.ErrIdempotencyConflict
			}
			receipt = prior
			return nil
		}
		if !notFound(err) {
			return err
		}
		now := d.now()
		receipt = state.MutationReceipt{
			RequestID:  requestID,
			Binding:    binding,
			Accepted:   true,
			Status:     "applied",
			Resource:   resource,
			HTTPStatus: 200,
			CreatedAt:  now,
		}
		stored := receiptDoc{
			RequestID:  requestID,
			Binding:    binding,
			Status:     receipt.Status,
			Resource:   string(receipt.Resource),
			HTTPStatus: receipt.HTTPStatus,
			CreatedAt:  now,
		}
		return tx.Create(receiptRef, stored)
	})
	if errors.Is(err, docstore.ErrExists) {
		saved, readErr := d.Receipt(ctx, requestID)
		if readErr != nil {
			return state.MutationReceipt{}, err
		}
		if saved.Binding != binding {
			return state.MutationReceipt{}, state.ErrIdempotencyConflict
		}
		return saved, nil
	}
	return receipt, err
}

// buildTaskOutcomeEntry formats a bounded factual summary for a completed task
// to be appended to Layer 2 Daily Logs inside Publish (never Core Memory).
func buildTaskOutcomeEntry(run state.Run, now time.Time) (state.MemoryLogEntry, bool) {
	if run.Status != state.RunCompleted || strings.TrimSpace(run.ID) == "" {
		return state.MemoryLogEntry{}, false
	}
	reqText := strings.Join(strings.Fields(run.Request), " ")
	resText := strings.Join(strings.Fields(run.Result), " ")
	var text string
	switch {
	case reqText != "" && resText != "":
		prefix := docstore.ClipUTF8(reqText, 160)
		text = docstore.ClipUTF8(prefix+": "+resText, state.MaxTaskOutcomeBytes)
	case resText != "":
		text = docstore.ClipUTF8(resText, state.MaxTaskOutcomeBytes)
	case reqText != "":
		text = docstore.ClipUTF8("Completed task: "+reqText, state.MaxTaskOutcomeBytes)
	default:
		text = docstore.ClipUTF8("Completed task "+run.ID, state.MaxTaskOutcomeBytes)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return state.MemoryLogEntry{}, false
	}
	nowUTC := now.UTC()
	return state.MemoryLogEntry{
		ID:         "task_" + run.ID,
		Section:    "Completed Work",
		Text:       text,
		About:      "task",
		Provenance: state.ProvenanceTaskOutcome,
		RunID:      run.ID,
		CreatedAt:  nowUTC,
		UpdatedAt:  nowUTC,
	}, true
}

// monotonicRevisionTime guarantees that consecutive mutations to the same record
// always advance UnixMilli strictly, even within the same millisecond or frozen clock tick.
func monotonicRevisionTime(now, prev time.Time) time.Time {
	nowUTC := now.UTC()
	if !prev.IsZero() && nowUTC.UnixMilli() <= prev.UnixMilli() {
		return time.UnixMilli(prev.UnixMilli() + 1).UTC()
	}
	return nowUTC
}
