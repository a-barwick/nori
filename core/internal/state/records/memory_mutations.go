package records

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

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

type memoryMutateEnv struct {
	tx        *docstore.Transaction
	req       state.MemoryMutationRequest
	now       time.Time
	todayDate string
	owner     string
}

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
		env := memoryMutateEnv{
			tx:        tx,
			req:       req,
			now:       now,
			todayDate: now.UTC().Format("2006-01-02"),
			owner:     d.user.DataOwner(),
		}

		var result state.MemoryMutationResult
		httpStatus := 200
		switch req.Kind {
		case state.MemoryMutateCreateCore:
			result, httpStatus, err = d.mutateCreateCore(env)
		case state.MemoryMutateEditCore:
			result, err = d.mutateEditCore(env)
		case state.MemoryMutateForgetCore:
			result, err = d.mutateForgetCore(env)
		case state.MemoryMutatePinCore, state.MemoryMutateUnpinCore:
			result, err = d.mutatePinCore(env)
		case state.MemoryMutateDemoteCore:
			result, err = d.mutateDemoteCore(env)
		case state.MemoryMutatePromoteLog:
			result, err = d.mutatePromoteLog(env)
		case state.MemoryMutateForgetLog:
			result, err = d.mutateForgetLog(env)
		case state.MemoryMutatePromoteWorking:
			result, err = d.mutatePromoteWorking(env)
		case state.MemoryMutateDismissWorking:
			result, err = d.mutateDismissWorking(env)
		default:
			return state.ErrInvalidResource
		}
		if err != nil {
			return err
		}

		var resourceJSON json.RawMessage
		if req.FormatResource != nil {
			resourceJSON, err = req.FormatResource(result)
		} else {
			resourceJSON, err = json.Marshal(result)
		}
		if err != nil {
			return err
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

func (d *Database) mutateCreateCore(env memoryMutateEnv) (state.MemoryMutationResult, int, error) {
	req := env.req
	if req.ExpectedRevision != 0 {
		return state.MemoryMutationResult{}, 0, state.ErrRevisionConflict
	}
	if err := validateCoreMemoryFields(req.Text, req.About, req.Section); err != nil {
		return state.MemoryMutationResult{}, 0, err
	}
	targetID := req.TargetID
	if targetID == "" {
		targetID = deterministicMemoryID("create_core", req.RequestID)
	} else if !validMemoryID(targetID) {
		return state.MemoryMutationResult{}, 0, state.ErrInvalidResource
	}
	existingCore, err := d.readAllCoreMemoriesTx(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, 0, err
	}
	byID := make(map[string]state.Memory, len(existingCore)+1)
	origIDs := make(map[string]bool, len(existingCore))
	for _, m := range existingCore {
		byID[m.ID] = m
		origIDs[m.ID] = true
	}
	if existing, exists := byID[req.ReplacesID]; req.ReplacesID != "" && exists {
		shards, err := d.readDailyLogShards(env.tx, env.todayDate)
		if err != nil {
			return state.MemoryMutationResult{}, 0, err
		}
		log, err := d.openLog(env.tx)
		if err != nil {
			return state.MemoryMutationResult{}, 0, err
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
		m.UpdatedAt = monotonicRevisionTime(env.now, existing.UpdatedAt)
		if m.UpdatedAt.Before(m.CreatedAt) {
			m.UpdatedAt = m.CreatedAt
		}
		byID[m.ID] = m
		demoted, err := d.commitCoreMutationWithDemotion(env.tx, origIDs, byID, m.ID, env.todayDate, shards, env.now)
		if err != nil {
			return state.MemoryMutationResult{}, 0, err
		}
		if err := log.add(state.EventMemory, ""); err != nil {
			return state.MemoryMutationResult{}, 0, err
		}
		return state.MemoryMutationResult{Memory: &m, Demoted: demoted}, 200, nil
	}
	if _, exists := byID[targetID]; exists {
		return state.MemoryMutationResult{}, 0, state.ErrRevisionConflict
	}
	shards, err := d.readDailyLogShards(env.tx, env.todayDate)
	if err != nil {
		return state.MemoryMutationResult{}, 0, err
	}
	log, err := d.openLog(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, 0, err
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
		CreatedAt:     env.now,
		UpdatedAt:     env.now,
	}
	byID[m.ID] = m
	demoted, err := d.commitCoreMutationWithDemotion(env.tx, origIDs, byID, m.ID, env.todayDate, shards, env.now)
	if err != nil {
		return state.MemoryMutationResult{}, 0, err
	}
	if err := log.add(state.EventMemory, ""); err != nil {
		return state.MemoryMutationResult{}, 0, err
	}
	return state.MemoryMutationResult{Memory: &m, Demoted: demoted}, 201, nil
}

func (d *Database) mutateEditCore(env memoryMutateEnv) (state.MemoryMutationResult, error) {
	req := env.req
	if !validMemoryID(req.TargetID) {
		return state.MemoryMutationResult{}, state.ErrNotFound
	}
	if err := validateCoreMemoryFields(req.Text, req.About, req.Section); err != nil {
		return state.MemoryMutationResult{}, err
	}
	existingCore, err := d.readAllCoreMemoriesTx(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	byID := make(map[string]state.Memory, len(existingCore))
	origIDs := make(map[string]bool, len(existingCore))
	for _, m := range existingCore {
		byID[m.ID] = m
		origIDs[m.ID] = true
	}
	m, exists := byID[req.TargetID]
	if !exists {
		return state.MemoryMutationResult{}, state.ErrNotFound
	}
	if m.UpdatedAt.UnixMilli() != req.ExpectedRevision {
		return state.MemoryMutationResult{}, state.ErrRevisionConflict
	}
	shards, err := d.readDailyLogShards(env.tx, env.todayDate)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	log, err := d.openLog(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
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
	m.UpdatedAt = monotonicRevisionTime(env.now, m.UpdatedAt)
	if m.UpdatedAt.Before(m.CreatedAt) {
		m.UpdatedAt = m.CreatedAt
	}
	byID[m.ID] = m
	demoted, err := d.commitCoreMutationWithDemotion(env.tx, origIDs, byID, m.ID, env.todayDate, shards, env.now)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := log.add(state.EventMemory, ""); err != nil {
		return state.MemoryMutationResult{}, err
	}
	return state.MemoryMutationResult{Memory: &m, Demoted: demoted}, nil
}

func (d *Database) mutateForgetCore(env memoryMutateEnv) (state.MemoryMutationResult, error) {
	req := env.req
	if !validMemoryID(req.TargetID) {
		return state.MemoryMutationResult{}, state.ErrNotFound
	}
	doc, err := env.tx.Get(d.memories().Doc(req.TargetID))
	if notFound(err) {
		return state.MemoryMutationResult{}, state.ErrNotFound
	}
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	var m state.Memory
	if err := doc.DataTo(&m); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if m.UpdatedAt.UnixMilli() != req.ExpectedRevision {
		return state.MemoryMutationResult{}, state.ErrRevisionConflict
	}
	log, err := d.openLog(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := env.tx.Delete(d.memories().Doc(m.ID)); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := env.tx.DeleteMemoryChunk(env.owner, docstore.MemoryCoreChunkID(m.ID)); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := log.add(state.EventMemory, ""); err != nil {
		return state.MemoryMutationResult{}, err
	}
	return state.MemoryMutationResult{
		Memory: &m,
		Note:   "Nori will no longer use this. Conversation history and external documents are unchanged.",
	}, nil
}

func (d *Database) mutatePinCore(env memoryMutateEnv) (state.MemoryMutationResult, error) {
	req := env.req
	if !validMemoryID(req.TargetID) {
		return state.MemoryMutationResult{}, state.ErrNotFound
	}
	existingCore, err := d.readAllCoreMemoriesTx(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	byID := make(map[string]state.Memory, len(existingCore))
	for _, m := range existingCore {
		byID[m.ID] = m
	}
	m, exists := byID[req.TargetID]
	if !exists {
		return state.MemoryMutationResult{}, state.ErrNotFound
	}
	if m.UpdatedAt.UnixMilli() != req.ExpectedRevision {
		return state.MemoryMutationResult{}, state.ErrRevisionConflict
	}
	m.Pinned = req.Kind == state.MemoryMutatePinCore
	m.UpdatedAt = monotonicRevisionTime(env.now, m.UpdatedAt)
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
			return state.MemoryMutationResult{}, state.ErrInvalidResource
		}
	}
	log, err := d.openLog(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := env.tx.Set(d.memories().Doc(m.ID), m); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := env.tx.UpsertMemoryChunk(coreChunk(env.owner, m)); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := log.add(state.EventMemory, ""); err != nil {
		return state.MemoryMutationResult{}, err
	}
	return state.MemoryMutationResult{Memory: &m}, nil
}

func (d *Database) mutateDemoteCore(env memoryMutateEnv) (state.MemoryMutationResult, error) {
	req := env.req
	if !validMemoryID(req.TargetID) {
		return state.MemoryMutationResult{}, state.ErrNotFound
	}
	logDate := req.Date
	if logDate == "" {
		logDate = env.todayDate
	}
	if !state.ValidMemoryDate(logDate) {
		return state.MemoryMutationResult{}, state.ErrInvalidResource
	}
	doc, err := env.tx.Get(d.memories().Doc(req.TargetID))
	if notFound(err) {
		return state.MemoryMutationResult{}, state.ErrNotFound
	}
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	var m state.Memory
	if err := doc.DataTo(&m); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if m.UpdatedAt.UnixMilli() != req.ExpectedRevision {
		return state.MemoryMutationResult{}, state.ErrRevisionConflict
	}
	shards, err := d.readDailyLogShards(env.tx, logDate)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	log, err := d.openLog(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}

	entry := demoteCoreToLogEntry(m, env.now)
	if dailyLogContainsEntryID(shards, entry.ID) {
		sum := sha256.Sum256([]byte(req.RequestID))
		entry.ID = entry.ID + "_" + hex.EncodeToString(sum[:4])
	}
	if err := env.tx.Delete(d.memories().Doc(m.ID)); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := env.tx.DeleteMemoryChunk(env.owner, docstore.MemoryCoreChunkID(m.ID)); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := d.writeDailyLogAppend(env.tx, logDate, shards, []state.MemoryLogEntry{entry}, env.now); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := log.add(state.EventMemory, ""); err != nil {
		return state.MemoryMutationResult{}, err
	}
	return state.MemoryMutationResult{Memory: &m, LogEntry: &entry}, nil
}

func (d *Database) mutatePromoteLog(env memoryMutateEnv) (state.MemoryMutationResult, error) {
	req := env.req
	if strings.TrimSpace(req.TargetID) == "" || !state.ValidMemoryDate(req.Date) {
		return state.MemoryMutationResult{}, state.ErrInvalidResource
	}
	shards, err := d.readDailyLogShards(env.tx, req.Date)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	entry, logRev, found := findDailyLogEntry(shards, req.TargetID)
	if !found {
		return state.MemoryMutationResult{}, state.ErrNotFound
	}
	if req.ExpectedRevision != logRev && req.ExpectedRevision != entry.UpdatedAt.UnixMilli() {
		return state.MemoryMutationResult{}, state.ErrRevisionConflict
	}
	reauthored := strings.TrimSpace(req.Text) != ""
	if entry.Provenance == state.ProvenanceTaskOutcome && !reauthored {
		return state.MemoryMutationResult{}, state.ErrInvalidResource
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
		return state.MemoryMutationResult{}, err
	}

	existingCore, err := d.readAllCoreMemoriesTx(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	byID := make(map[string]state.Memory, len(existingCore)+1)
	origIDs := make(map[string]bool, len(existingCore))
	for _, item := range existingCore {
		byID[item.ID] = item
		origIDs[item.ID] = true
	}
	demoteShards := shards
	if env.todayDate != req.Date {
		demoteShards, err = d.readDailyLogShards(env.tx, env.todayDate)
		if err != nil {
			return state.MemoryMutationResult{}, err
		}
	}
	log, err := d.openLog(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
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
		CreatedAt:     env.now,
		UpdatedAt:     env.now,
	}
	if sourceInputID != "" && sourceInputID != "ui" {
		for _, item := range existingCore {
			if item.EffectiveProvenance() == state.ProvenanceUserExplicit && item.SourceInputID == sourceInputID && item.Text == text {
				m.ID = item.ID
				m.CreatedAt = item.CreatedAt
				if req.Pinned == nil {
					m.Pinned = item.Pinned
				}
				m.UpdatedAt = monotonicRevisionTime(env.now, item.UpdatedAt)
				break
			}
		}
	}
	byID[m.ID] = m
	demoted, err := d.commitCoreMutationWithDemotion(env.tx, origIDs, byID, m.ID, env.todayDate, demoteShards, env.now)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := log.add(state.EventMemory, ""); err != nil {
		return state.MemoryMutationResult{}, err
	}
	return state.MemoryMutationResult{Memory: &m, Demoted: demoted, LogEntry: &entry}, nil
}

func (d *Database) mutateForgetLog(env memoryMutateEnv) (state.MemoryMutationResult, error) {
	req := env.req
	if strings.TrimSpace(req.TargetID) == "" || !state.ValidMemoryDate(req.Date) {
		return state.MemoryMutationResult{}, state.ErrInvalidResource
	}
	shards, err := d.readDailyLogShards(env.tx, req.Date)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	shardIdx, entryIdx, logRev, found := findDailyLogEntryPos(shards, req.TargetID)
	if !found {
		return state.MemoryMutationResult{}, state.ErrNotFound
	}
	entry := shards[shardIdx].Entries[entryIdx]
	if req.ExpectedRevision != logRev && req.ExpectedRevision != entry.UpdatedAt.UnixMilli() {
		return state.MemoryMutationResult{}, state.ErrRevisionConflict
	}
	log, err := d.openLog(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}

	targetShard := shards[shardIdx]
	targetShard.Entries = append(slices.Clone(targetShard.Entries[:entryIdx]), targetShard.Entries[entryIdx+1:]...)
	shardRef := d.memoryLogs().Doc(state.MemoryLogDocID(req.Date, targetShard.Shard))
	if len(targetShard.Entries) == 0 {
		if err := env.tx.Delete(shardRef); err != nil {
			return state.MemoryMutationResult{}, err
		}
	} else {
		targetShard.UpdatedAt = monotonicRevisionTime(env.now, targetShard.UpdatedAt)
		if err := env.tx.Set(shardRef, targetShard); err != nil {
			return state.MemoryMutationResult{}, err
		}
	}
	if err := env.tx.DeleteMemoryChunk(env.owner, docstore.MemoryLogChunkID(req.Date, entry.ID)); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := log.add(state.EventMemory, ""); err != nil {
		return state.MemoryMutationResult{}, err
	}
	return state.MemoryMutationResult{
		LogEntry: &entry,
		Note:     "Nori will no longer use this. Conversation history and external documents are unchanged.",
	}, nil
}

func (d *Database) loadWorkingSummaryItem(tx *docstore.Transaction, targetID string, expectedRev int64) (state.ConversationSummary, int, error) {
	if strings.TrimSpace(targetID) == "" {
		return state.ConversationSummary{}, -1, state.ErrInvalidResource
	}
	summaryDoc, err := tx.Get(d.summary())
	if notFound(err) {
		return state.ConversationSummary{}, -1, state.ErrNotFound
	}
	if err != nil {
		return state.ConversationSummary{}, -1, err
	}
	var sum state.ConversationSummary
	if err := summaryDoc.DataTo(&sum); err != nil {
		return state.ConversationSummary{}, -1, err
	}
	if sum.UpdatedAt.UnixMilli() != expectedRev {
		return state.ConversationSummary{}, -1, state.ErrRevisionConflict
	}
	for i, item := range sum.Items {
		if state.SummaryItemID(item) == targetID {
			return sum, i, nil
		}
	}
	return state.ConversationSummary{}, -1, state.ErrNotFound
}

func (d *Database) mutatePromoteWorking(env memoryMutateEnv) (state.MemoryMutationResult, error) {
	req := env.req
	sum, itemIdx, err := d.loadWorkingSummaryItem(env.tx, req.TargetID, req.ExpectedRevision)
	if err != nil {
		return state.MemoryMutationResult{}, err
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
		return state.MemoryMutationResult{}, err
	}

	existingCore, err := d.readAllCoreMemoriesTx(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	byID := make(map[string]state.Memory, len(existingCore)+1)
	origIDs := make(map[string]bool, len(existingCore))
	for _, cm := range existingCore {
		byID[cm.ID] = cm
		origIDs[cm.ID] = true
	}
	shards, err := d.readDailyLogShards(env.tx, env.todayDate)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	log, err := d.openLog(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
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
		CreatedAt:     env.now,
		UpdatedAt:     env.now,
	}
	if sourceInputID != "" && sourceInputID != "ui" {
		for _, cm := range existingCore {
			if cm.EffectiveProvenance() == state.ProvenanceUserExplicit && cm.SourceInputID == sourceInputID && cm.Text == text {
				m.ID = cm.ID
				m.CreatedAt = cm.CreatedAt
				if req.Pinned == nil {
					m.Pinned = cm.Pinned
				}
				m.UpdatedAt = monotonicRevisionTime(env.now, cm.UpdatedAt)
				break
			}
		}
	}
	byID[m.ID] = m
	demoted, err := d.commitCoreMutationWithDemotion(env.tx, origIDs, byID, m.ID, env.todayDate, shards, env.now)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := log.add(state.EventMemory, ""); err != nil {
		return state.MemoryMutationResult{}, err
	}
	return state.MemoryMutationResult{Memory: &m, Demoted: demoted, Summary: &sum}, nil
}

func (d *Database) mutateDismissWorking(env memoryMutateEnv) (state.MemoryMutationResult, error) {
	req := env.req
	sum, itemIdx, err := d.loadWorkingSummaryItem(env.tx, req.TargetID, req.ExpectedRevision)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	existingCore, err := d.readAllCoreMemoriesTx(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	control, err := d.readConversation(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	log, err := d.openLog(env.tx)
	if err != nil {
		return state.MemoryMutationResult{}, err
	}
	sum.Items = append(slices.Clone(sum.Items[:itemIdx]), sum.Items[itemIdx+1:]...)
	if !slices.Contains(sum.DismissedItemIDs, req.TargetID) {
		sum.DismissedItemIDs = append(sum.DismissedItemIDs, req.TargetID)
	}
	sum.UpdatedAt = monotonicRevisionTime(env.now, sum.UpdatedAt)
	if err := env.tx.Set(d.summary(), sum); err != nil {
		return state.MemoryMutationResult{}, err
	}
	if err := log.add(state.EventMemory, ""); err != nil {
		return state.MemoryMutationResult{}, err
	}
	return state.MemoryMutationResult{
		Summary:     &sum,
		CoreBytes:   state.CoreMemorySize(existingCore),
		ContextFrom: control.ContextFrom,
	}, nil
}

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
