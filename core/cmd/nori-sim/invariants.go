package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"nori/internal/app"
	"nori/internal/permissions"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

const maxConversationExcerptBytes = 32 << 10

type invariantViolation struct {
	Invariant string `json:"invariant"`
	EntityID  string `json:"entity_id,omitempty"`
	Message   string `json:"message"`
}

type simUserSnapshot struct {
	SimNow              time.Time                 `json:"sim_now"`
	UserID              string                    `json:"user_id"`
	Email               string                    `json:"email"`
	ContextFrom         time.Time                 `json:"context_from,omitempty"`
	CoreMemories        []state.Memory            `json:"core_memories"`
	CoreBytes           int                       `json:"core_bytes"`
	DailyLogs           []state.MemoryLog         `json:"daily_logs"`
	DailyLogEntryCount  int                       `json:"daily_log_entry_count"`
	WorkingSummary      state.ConversationSummary `json:"working_summary"`
	FTSChunkCount       int                       `json:"fts_chunk_count"`
	ConversationPending bool                      `json:"conversation_pending"`
	ConversationActive  string                    `json:"conversation_active"`
	OpenTasks           []state.Run               `json:"open_tasks"`
	RecentTasks         []state.Run               `json:"recent_tasks"`
	PendingInteractions []permissions.Interaction `json:"pending_interactions"`
	LibraryCount        int                       `json:"library_count"`
	ModelUsage          []state.Usage             `json:"model_usage,omitempty"`
	InvariantViolations []invariantViolation      `json:"invariant_violations"`
	InvariantsOK        bool                      `json:"invariants_ok"`
}

type simMemoryBaseline struct {
	hadSummary                bool
	epoch                     time.Time
	flushedThrough            time.Time
	extractedThrough          time.Time
	through                   time.Time
	flushFailures             int
	lastFailureThrough        time.Time
	extractFailures           int
	lastExtractFailureThrough time.Time
	summaryItems              map[string]state.SummaryItem
	dismissedIDs              map[string]bool
	pinnedCore                map[string]state.Memory
}

type simInvariantTracker struct {
	mu        sync.Mutex
	baselines map[string]simMemoryBaseline
}

func newSimInvariantTracker() *simInvariantTracker {
	return &simInvariantTracker{
		baselines: make(map[string]simMemoryBaseline),
	}
}

func (t *simInvariantTracker) get(userID string) simMemoryBaseline {
	if t == nil {
		return simMemoryBaseline{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.baselines[userID]
}

func (t *simInvariantTracker) set(userID string, b simMemoryBaseline) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.baselines[userID] = b
}

func inspectUserState(ctx context.Context, user users.User, userApp *app.App, backend *simClockBackend, now time.Time) (simUserSnapshot, error) {
	return inspectUserStateWithTracker(ctx, user, userApp, backend, now, nil)
}

func inspectUserStateWithTracker(ctx context.Context, user users.User, userApp *app.App, backend *simClockBackend, now time.Time, tracker *simInvariantTracker) (simUserSnapshot, error) {
	now = now.UTC()
	owner := user.DataOwner()
	snap := simUserSnapshot{
		SimNow: now,
		UserID: user.ID,
		Email:  user.Email,
	}

	conv, _, err := userApp.Store.ConversationStatus(ctx)
	if err != nil {
		return snap, fmt.Errorf("conversation: %w", err)
	}
	snap.ConversationPending = conv.Pending()
	snap.ConversationActive = conv.Active
	snap.ContextFrom = conv.ContextFrom

	memories, err := userApp.Store.Memories(ctx)
	if err != nil {
		return snap, fmt.Errorf("memories: %w", err)
	}
	snap.CoreMemories = memories

	logs, err := userApp.Store.MemoryLogs(ctx, "", 120)
	if err != nil {
		return snap, fmt.Errorf("memory_logs: %w", err)
	}
	snap.DailyLogs = logs

	summary, err := userApp.Store.ConversationSummary(ctx)
	if err != nil {
		return snap, fmt.Errorf("summary: %w", err)
	}
	snap.WorkingSummary = summary

	openTasks, err := userApp.Store.OpenTasks(ctx)
	if err != nil {
		return snap, fmt.Errorf("open_tasks: %w", err)
	}
	snap.OpenTasks = openTasks

	recentTasks, err := userApp.Store.RecentRuns(ctx, 100)
	if err != nil {
		return snap, fmt.Errorf("recent_tasks: %w", err)
	}
	snap.RecentTasks = recentTasks

	if userApp.Assistant != nil && userApp.Assistant.Permissions != nil {
		pending, _, err := userApp.Assistant.Permissions.Interactions(ctx, "pending", "", "", 50)
		if err == nil {
			snap.PendingInteractions = pending
		}
	}

	if results, err := userApp.Store.RecentResults(ctx, 200); err == nil {
		snap.LibraryCount = len(results)
	}

	chunks, err := backend.ListMemoryChunks(ctx, owner)
	if err != nil {
		return snap, fmt.Errorf("list_memory_chunks: %w", err)
	}
	snap.FTSChunkCount = len(chunks)

	var violations []invariantViolation

	allLogsMap, dVs := loadCollectionDocs[state.MemoryLog](ctx, backend, owner, "memory_logs", "INV-MEM-06.decode_log")
	violations = append(violations, dVs...)
	for docID, logDoc := range allLogsMap {
		if logDoc.Date == "" {
			date, _, _ := strings.Cut(docID, "#")
			logDoc.Date = date
			allLogsMap[docID] = logDoc
		}
	}

	allInputs, dVs := loadCollectionDocs[state.ConversationInput](ctx, backend, owner, "conversation_inputs", "INV-MEM-04.decode_input")
	violations = append(violations, dVs...)

	allTurns, dVs := loadCollectionDocs[state.ConversationTurn](ctx, backend, owner, "conversation_turns", "INV-MEM-01.decode_turn")
	violations = append(violations, dVs...)

	allAttempts, dVs := loadCollectionDocs[state.ConversationAttempt](ctx, backend, owner, "conversation_attempts", "INV-MEM-05.decode_attempt")
	violations = append(violations, dVs...)

	allMessages, _ := loadCollectionDocs[state.Message](ctx, backend, owner, "messages", "")

	usageMap, _ := loadCollectionDocs[state.Usage](ctx, backend, owner, "model_usage", "")
	if len(usageMap) > 0 {
		usageKeys := sortedMapKeys(usageMap)
		usageSlice := make([]state.Usage, 0, len(usageKeys))
		for _, k := range usageKeys {
			usageSlice = append(usageSlice, usageMap[k])
		}
		snap.ModelUsage = usageSlice
	}

	manualDemoted := loadManualDemotedIDs(ctx, backend, owner)
	summaryExists := summaryDocExists(ctx, backend, owner, summary)

	prev := tracker.get(user.ID)

	allLogEntriesByID := map[string]state.MemoryLogEntry{}
	evictedEntries := []state.MemoryLogEntry{}
	expectedChunks := map[string]docstore.MemoryChunk{}
	seenDateEntry := map[string]map[string]string{}
	var preContextLogEntries []state.MemoryLogEntry

	logEntries := 0
	for _, docID := range sortedMapKeys(allLogsMap) {
		logDoc := allLogsMap[docID]
		date := logDoc.Date
		if date == "" {
			date, _, _ = strings.Cut(docID, "#")
		}
		if seenDateEntry[date] == nil {
			seenDateEntry[date] = map[string]string{}
		}
		for _, entry := range logDoc.Entries {
			logEntries++
			if firstDoc, dup := seenDateEntry[date][entry.ID]; dup {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-05.duplicate_log_entry",
					EntityID:  date + "/" + entry.ID,
					Message:   fmt.Sprintf("duplicate Daily Log entry ID %q on %s (docs %q and %q)", entry.ID, date, firstDoc, docID),
				})
			} else {
				seenDateEntry[date][entry.ID] = docID
			}
			allLogEntriesByID[entry.ID] = entry
			if entry.Provenance == state.ProvenanceSummaryEvicted {
				evictedEntries = append(evictedEntries, entry)
			}
			if !utf8.ValidString(entry.Text) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-06",
					EntityID:  entry.ID,
					Message:   "daily log entry text is invalid UTF-8",
				})
			}
			if entry.Provenance != "" && !state.ValidMemoryProvenance(entry.Provenance) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-04.invalid_provenance",
					EntityID:  entry.ID,
					Message:   fmt.Sprintf("Daily Log entry has invalid provenance %q", entry.Provenance),
				})
			}
			if entry.SourceInputID != "" && entry.SourceInputID != "ui" {
				if in, exists := allInputs[entry.SourceInputID]; exists && !in.IsUser() {
					violations = append(violations, invariantViolation{
						Invariant: "INV-MEM-04.unverified_source_input",
						EntityID:  entry.ID,
						Message:   fmt.Sprintf("Daily Log entry cites non-user conversation_input %q", entry.SourceInputID),
					})
				}
			}
			if !conv.ContextFrom.IsZero() && !entry.CreatedAt.IsZero() && entry.CreatedAt.Before(conv.ContextFrom) {
				preContextLogEntries = append(preContextLogEntries, entry)
			}

			chunk := simExpectedLogChunk(owner, date, entry)
			expectedChunks[chunk.ChunkID] = chunk
		}
	}
	snap.DailyLogEntryCount = logEntries

	memByID := make(map[string]state.Memory, len(memories))
	currPinned := map[string]state.Memory{}
	seenTurnMem := map[string]string{}
	coreBytes := 0
	for _, m := range memories {
		memByID[m.ID] = m
		coreBytes += len(m.Text)
		if m.Pinned {
			currPinned[m.ID] = m
		}

		if strings.TrimSpace(m.Text) == "" || len(m.Text) < 1 || len(m.Text) > state.MaxMemoryText || !utf8.ValidString(m.Text) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-03.item_bounds",
				EntityID:  m.ID,
				Message:   fmt.Sprintf("Core memory text invalid (len=%d, valid_utf8=%v)", len(m.Text), utf8.ValidString(m.Text)),
			})
		}
		if len(m.About) > state.MaxMemoryAbout || !utf8.ValidString(m.About) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-03.item_bounds",
				EntityID:  m.ID,
				Message:   fmt.Sprintf("Core memory about invalid (len=%d, valid_utf8=%v)", len(m.About), utf8.ValidString(m.About)),
			})
		}
		if len(m.Section) > state.MaxMemorySection || !utf8.ValidString(m.Section) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-03.item_bounds",
				EntityID:  m.ID,
				Message:   fmt.Sprintf("Core memory section invalid (len=%d, valid_utf8=%v)", len(m.Section), utf8.ValidString(m.Section)),
			})
		}
		if !m.CreatedAt.IsZero() && !m.UpdatedAt.IsZero() && m.UpdatedAt.Before(m.CreatedAt) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-03.timestamps",
				EntityID:  m.ID,
				Message:   fmt.Sprintf("UpdatedAt (%s) < CreatedAt (%s)", m.UpdatedAt.UTC().Format(time.RFC3339Nano), m.CreatedAt.UTC().Format(time.RFC3339Nano)),
			})
		}

		if m.Provenance == state.ProvenanceTaskOutcome || m.EffectiveProvenance() == state.ProvenanceTaskOutcome {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-04.task_outcome_in_core",
				EntityID:  m.ID,
				Message:   "task_outcome provenance leaked into Core Memory",
			})
		} else if m.Provenance != "" && !state.ValidMemoryProvenance(m.Provenance) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-04.invalid_provenance",
				EntityID:  m.ID,
				Message:   fmt.Sprintf("Core memory has invalid provenance %q", m.Provenance),
			})
		} else {
			prov := m.EffectiveProvenance()
			switch prov {
			case state.ProvenancePreCompactFlush:
				in, ok := allInputs[m.SourceInputID]
				if m.SourceInputID == "" || m.SourceInputID == "ui" || !ok || !in.IsUser() {
					violations = append(violations, invariantViolation{
						Invariant: "INV-MEM-04.unverified_source_input",
						EntityID:  m.ID,
						Message:   fmt.Sprintf("pre_compact_flush Core memory cites invalid or non-user SourceInputID %q", m.SourceInputID),
					})
				}
			case state.ProvenanceUserExplicit:
				legacyEmpty := m.Provenance == "" && m.SourceInputID == ""
				if m.SourceInputID != "ui" && !legacyEmpty {
					in, ok := allInputs[m.SourceInputID]
					if m.SourceInputID == "" || !ok || !in.IsUser() {
						violations = append(violations, invariantViolation{
							Invariant: "INV-MEM-04.unverified_source_input",
							EntityID:  m.ID,
							Message:   fmt.Sprintf("user_explicit Core memory cites invalid or non-user SourceInputID %q", m.SourceInputID),
						})
					}
				}
			case state.ProvenanceDreamCurated:
				if m.SourceInputID != "ui" {
					in, ok := allInputs[m.SourceInputID]
					if m.SourceInputID == "" || !ok || !in.IsUser() {
						violations = append(violations, invariantViolation{
							Invariant: "INV-MEM-04.unverified_source_input",
							EntityID:  m.ID,
							Message:   fmt.Sprintf("dream_curated Core memory cites invalid or non-user SourceInputID %q", m.SourceInputID),
						})
					}
				}
			}
		}

		if m.SourceInputID != "" && m.SourceInputID != "ui" {
			dedupKey := string(m.EffectiveProvenance()) + "\x00" + m.SourceInputID + "\x00" + m.Text
			if firstID, dup := seenTurnMem[dedupKey]; dup {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-05.duplicate_memory",
					EntityID:  m.ID,
					Message:   fmt.Sprintf("duplicate Core memory %q mirrors %q for source_input_id %q", m.ID, firstID, m.SourceInputID),
				})
			} else {
				seenTurnMem[dedupKey] = m.ID
			}
		}

		chunk := simExpectedCoreChunk(owner, m)
		expectedChunks[chunk.ChunkID] = chunk
	}
	snap.CoreBytes = coreBytes

	if totalCore := state.CoreMemorySize(memories); totalCore > state.CoreMemoryBytes {
		violations = append(violations, invariantViolation{
			Invariant: "INV-MEM-03.core_budget",
			Message:   fmt.Sprintf("Core memory total bytes %d exceeds CoreMemoryBytes (%d)", totalCore, state.CoreMemoryBytes),
		})
	}

	for _, pinnedID := range sortedMapKeys(prev.pinnedCore) {
		if _, stillCore := memByID[pinnedID]; stillCore {
			continue
		}
		if manualDemoted[pinnedID] {
			continue
		}
		if _, wasDemoted := allLogEntriesByID["demote_"+pinnedID]; wasDemoted {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-03.pinned_auto_demoted",
				EntityID:  pinnedID,
				Message:   fmt.Sprintf("pinned Core memory %q was auto-demoted to Daily Logs without a manual demote receipt", pinnedID),
			})
		}
	}

	actualChunks := make(map[string]docstore.MemoryChunk, len(chunks))
	for _, ch := range chunks {
		actualChunks[ch.ChunkID] = ch
	}

	committedNewMemIDs := map[string]bool{}
	combinedAttempts := make([]state.ConversationAttempt, 0, len(allAttempts)+len(allTurns))
	for _, id := range sortedMapKeys(allAttempts) {
		combinedAttempts = append(combinedAttempts, allAttempts[id])
	}
	for _, id := range sortedMapKeys(allTurns) {
		t := allTurns[id]
		if t.Attempt.ID != "" {
			if _, exists := allAttempts[t.Attempt.ID]; !exists {
				combinedAttempts = append(combinedAttempts, t.Attempt)
			}
		}
	}
	for _, att := range combinedAttempts {
		if att.Status != "completed" || simHasRefusedMemoryChange(att.Memory) {
			continue
		}
		for _, ch := range att.Memory {
			if ch.Kind == state.MemoryRemember && !ch.Replaces && ch.ID != "" {
				committedNewMemIDs[ch.ID] = true
			}
		}
	}
	for _, att := range combinedAttempts {
		if len(att.Memory) == 0 {
			continue
		}
		refused := simHasRefusedMemoryChange(att.Memory)
		if att.Status == "completed" && !refused {
			continue
		}
		invName := "INV-MEM-05.uncompleted_attempt_applied"
		reason := fmt.Sprintf("non-completed attempt %q (status=%q)", att.ID, att.Status)
		if att.Status == "completed" && refused {
			invName = "INV-MEM-05.refused_change_applied"
			reason = fmt.Sprintf("attempt %q with refused memory change", att.ID)
		}
		for _, ch := range att.Memory {
			if ch.Kind != state.MemoryRemember || ch.Replaces || ch.ID == "" || committedNewMemIDs[ch.ID] {
				continue
			}
			if _, inCore := memByID[ch.ID]; inCore {
				violations = append(violations, invariantViolation{
					Invariant: invName,
					EntityID:  ch.ID,
					Message:   fmt.Sprintf("%s left Core memory %q in store", reason, ch.ID),
				})
			}
			if _, inLog := allLogEntriesByID["demote_"+ch.ID]; inLog {
				violations = append(violations, invariantViolation{
					Invariant: invName,
					EntityID:  ch.ID,
					Message:   fmt.Sprintf("%s left demoted memory %q in memory_logs", reason, ch.ID),
				})
			}
			if _, inIdx := actualChunks[docstore.MemoryCoreChunkID(ch.ID)]; inIdx {
				violations = append(violations, invariantViolation{
					Invariant: invName,
					EntityID:  ch.ID,
					Message:   fmt.Sprintf("%s left chunk %q in MemoryIndex", reason, docstore.MemoryCoreChunkID(ch.ID)),
				})
			}
		}
	}

	for _, chunkID := range sortedMapKeys(expectedChunks) {
		want := expectedChunks[chunkID]
		got, exists := actualChunks[chunkID]
		if !exists {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-06.fts_missing_chunk",
				EntityID:  chunkID,
				Message:   fmt.Sprintf("store memory record missing corresponding MemoryIndex chunk %q", chunkID),
			})
			continue
		}
		if !simMemoryChunksEqual(want, got) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-06.fts_chunk_mismatch",
				EntityID:  chunkID,
				Message:   fmt.Sprintf("MemoryIndex chunk %q disagrees with canonical store record", chunkID),
			})
		}
	}
	for _, chunkID := range sortedMapKeys(actualChunks) {
		if _, expected := expectedChunks[chunkID]; !expected {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-06.fts_orphan_chunk",
				EntityID:  chunkID,
				Message:   fmt.Sprintf("MemoryIndex contains orphan chunk %q with no backing store record", chunkID),
			})
		}
	}

	nextBase := simMemoryBaseline{
		hadSummary:                prev.hadSummary,
		epoch:                     prev.epoch,
		flushedThrough:            prev.flushedThrough,
		extractedThrough:          prev.extractedThrough,
		through:                   prev.through,
		flushFailures:             prev.flushFailures,
		lastFailureThrough:        prev.lastFailureThrough,
		extractFailures:           prev.extractFailures,
		lastExtractFailureThrough: prev.lastExtractFailureThrough,
		summaryItems:              prev.summaryItems,
		dismissedIDs:              prev.dismissedIDs,
		pinnedCore:                currPinned,
	}

	flushed := summary.EffectiveFlushedThrough()
	extracted := summary.EffectiveExtractedThrough()

	if summaryExists {
		sameEpoch := prev.hadSummary && prev.epoch.Equal(summary.From)

		if prev.hadSummary && !prev.epoch.IsZero() && summary.From.Before(prev.epoch) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-01.watermark_monotonic",
				Message:   fmt.Sprintf("summary.From regressed from %s to %s", prev.epoch.UTC().Format(time.RFC3339Nano), summary.From.UTC().Format(time.RFC3339Nano)),
			})
		}

		if summary.Through.IsZero() {
			if !summary.FlushedThrough.IsZero() || !summary.RefinementThrough.IsZero() {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_bounds",
					Message:   fmt.Sprintf("summary.Through is zero while FlushedThrough=%s RefinementThrough=%s", summary.FlushedThrough.UTC().Format(time.RFC3339Nano), summary.RefinementThrough.UTC().Format(time.RFC3339Nano)),
				})
			}
		} else {
			if !summary.From.IsZero() && flushed.Before(summary.From) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_bounds",
					Message:   fmt.Sprintf("EffectiveFlushedThrough (%s) precedes From (%s)", flushed.UTC().Format(time.RFC3339Nano), summary.From.UTC().Format(time.RFC3339Nano)),
				})
			}
			if !summary.From.IsZero() && summary.Through.Before(summary.From) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_bounds",
					Message:   fmt.Sprintf("Through (%s) precedes From (%s)", summary.Through.UTC().Format(time.RFC3339Nano), summary.From.UTC().Format(time.RFC3339Nano)),
				})
			}
			if flushed.After(summary.Through) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_bounds",
					Message:   fmt.Sprintf("EffectiveFlushedThrough (%s) exceeds Through (%s)", flushed.UTC().Format(time.RFC3339Nano), summary.Through.UTC().Format(time.RFC3339Nano)),
				})
			}
			if !summary.FlushedThrough.IsZero() && summary.FlushedThrough.After(summary.Through) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_bounds",
					Message:   fmt.Sprintf("FlushedThrough (%s) exceeds Through (%s)", summary.FlushedThrough.UTC().Format(time.RFC3339Nano), summary.Through.UTC().Format(time.RFC3339Nano)),
				})
			}
			if !summary.RefinementThrough.IsZero() && summary.RefinementThrough.After(summary.Through) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_bounds",
					Message:   fmt.Sprintf("RefinementThrough (%s) exceeds Through (%s)", summary.RefinementThrough.UTC().Format(time.RFC3339Nano), summary.Through.UTC().Format(time.RFC3339Nano)),
				})
			}
		}

		if !summary.From.IsZero() && extracted.Before(summary.From) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-01.watermark_bounds",
				Message:   fmt.Sprintf("EffectiveExtractedThrough (%s) precedes From (%s)", extracted.UTC().Format(time.RFC3339Nano), summary.From.UTC().Format(time.RFC3339Nano)),
			})
		}
		if extracted.Before(flushed) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-01.watermark_bounds",
				Message:   fmt.Sprintf("EffectiveExtractedThrough (%s) precedes EffectiveFlushedThrough (%s)", extracted.UTC().Format(time.RFC3339Nano), flushed.UTC().Format(time.RFC3339Nano)),
			})
		}
		if !summary.ExtractedThrough.IsZero() {
			if !summary.From.IsZero() && summary.ExtractedThrough.Before(summary.From) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_bounds",
					Message:   fmt.Sprintf("ExtractedThrough (%s) precedes From (%s)", summary.ExtractedThrough.UTC().Format(time.RFC3339Nano), summary.From.UTC().Format(time.RFC3339Nano)),
				})
			} else if summary.ExtractedThrough.Before(flushed) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_bounds",
					Message:   fmt.Sprintf("ExtractedThrough (%s) precedes EffectiveFlushedThrough (%s)", summary.ExtractedThrough.UTC().Format(time.RFC3339Nano), flushed.UTC().Format(time.RFC3339Nano)),
				})
			} else if !conv.ContextFrom.IsZero() && summary.From.Equal(conv.ContextFrom) && summary.ExtractedThrough.Before(conv.ContextFrom) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_bounds",
					Message:   fmt.Sprintf("ExtractedThrough (%s) precedes ContextFrom (%s)", summary.ExtractedThrough.UTC().Format(time.RFC3339Nano), conv.ContextFrom.UTC().Format(time.RFC3339Nano)),
				})
			}
		}

		if sameEpoch {
			if flushed.Before(prev.flushedThrough) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_monotonic",
					Message:   fmt.Sprintf("EffectiveFlushedThrough regressed from %s to %s", prev.flushedThrough.UTC().Format(time.RFC3339Nano), flushed.UTC().Format(time.RFC3339Nano)),
				})
			}
			if extracted.Before(prev.extractedThrough) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_monotonic",
					Message:   fmt.Sprintf("EffectiveExtractedThrough regressed from %s to %s", prev.extractedThrough.UTC().Format(time.RFC3339Nano), extracted.UTC().Format(time.RFC3339Nano)),
				})
			}
			if !prev.through.IsZero() && summary.Through.Before(prev.through) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.watermark_monotonic",
					Message:   fmt.Sprintf("Through regressed from %s to %s", prev.through.UTC().Format(time.RFC3339Nano), summary.Through.UTC().Format(time.RFC3339Nano)),
				})
			}
			if summary.FlushFailures > prev.flushFailures && flushed.After(prev.flushedThrough) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.failure_watermark_advance",
					Message:   fmt.Sprintf("FlushedThrough advanced from %s to %s while FlushFailures increased from %d to %d", prev.flushedThrough.UTC().Format(time.RFC3339Nano), flushed.UTC().Format(time.RFC3339Nano), prev.flushFailures, summary.FlushFailures),
				})
			}
			if summary.ExtractFailures > prev.extractFailures &&
				summary.LastExtractFailureThrough.Equal(prev.lastExtractFailureThrough) &&
				extracted.After(prev.extractedThrough) &&
				(!flushed.After(prev.flushedThrough) || !extracted.Equal(flushed)) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.failure_watermark_advance",
					Message:   fmt.Sprintf("EffectiveExtractedThrough advanced from %s to %s while ExtractFailures increased from %d to %d", prev.extractedThrough.UTC().Format(time.RFC3339Nano), extracted.UTC().Format(time.RFC3339Nano), prev.extractFailures, summary.ExtractFailures),
				})
			}
		}
		if summary.FlushFailures > 0 && !summary.LastFailureThrough.IsZero() && !flushed.Before(summary.LastFailureThrough) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-01.failure_watermark_advance",
				Message:   fmt.Sprintf("FlushedThrough (%s) reached or passed LastFailureThrough (%s) while FlushFailures=%d > 0", flushed.UTC().Format(time.RFC3339Nano), summary.LastFailureThrough.UTC().Format(time.RFC3339Nano), summary.FlushFailures),
			})
		}
		if summary.ExtractFailures > 0 && !summary.LastExtractFailureThrough.IsZero() && !extracted.Before(summary.LastExtractFailureThrough) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-01.failure_watermark_advance",
				Message:   fmt.Sprintf("EffectiveExtractedThrough (%s) reached or passed LastExtractFailureThrough (%s) while ExtractFailures=%d > 0", extracted.UTC().Format(time.RFC3339Nano), summary.LastExtractFailureThrough.UTC().Format(time.RFC3339Nano), summary.ExtractFailures),
			})
		}

		excerptsBytes := 0
		if len(summary.Excerpts) > 0 {
			rawEx, _ := json.Marshal(summary.Excerpts)
			excerptsBytes = len(rawEx)
			if excerptsBytes > maxConversationExcerptBytes || excerptsBytes > state.OuterInputBytes {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-01.excerpt_budget",
					Message:   fmt.Sprintf("summary.Excerpts size %d exceeds cap (%d)", excerptsBytes, maxConversationExcerptBytes),
				})
			}
		}
		if !summary.Through.IsZero() && flushed.Before(summary.Through) {
			violations = append(violations, simCheckUnflushedExcerptCoverage(summary, flushed, excerptsBytes, allTurns)...)
		}

		if len(summary.Items) > state.MaxSummaryItems {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-02.summary_budget",
				Message:   fmt.Sprintf("summary.Items count %d exceeds MaxSummaryItems (%d)", len(summary.Items), state.MaxSummaryItems),
			})
		}
		sumPayload, _ := json.Marshal(struct {
			Items []state.SummaryItem `json:"items"`
			Gist  string              `json:"gist"`
		}{summary.Items, summary.Gist})
		if len(sumPayload) > state.SummaryBytes {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-02.summary_budget",
				Message:   fmt.Sprintf("summary {items,gist} size %d exceeds SummaryBytes (%d)", len(sumPayload), state.SummaryBytes),
			})
		}

		dismissedSet := make(map[string]bool, len(summary.DismissedItemIDs))
		for _, id := range summary.DismissedItemIDs {
			dismissedSet[id] = true
		}
		currItems := make(map[string]state.SummaryItem, len(summary.Items))
		for _, item := range summary.Items {
			itemID := state.SummaryItemID(item)
			currItems[itemID] = item
			if dismissedSet[itemID] {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-02.dismissed_reappeared",
					EntityID:  itemID,
					Message:   fmt.Sprintf("dismissed summary item %q reappeared in summary.Items", itemID),
				})
			}
			if item.InputID != "" && !conv.ContextFrom.IsZero() && summary.From.Equal(conv.ContextFrom) {
				if in, ok := allInputs[item.InputID]; ok && !in.CreatedAt.IsZero() && in.CreatedAt.Before(conv.ContextFrom) {
					violations = append(violations, invariantViolation{
						Invariant: "INV-MEM-07.pre_context_from_leak",
						EntityID:  itemID,
						Message:   fmt.Sprintf("SummaryItem %q references pre-ContextFrom input %q (%s < %s)", itemID, item.InputID, in.CreatedAt.UTC().Format(time.RFC3339Nano), conv.ContextFrom.UTC().Format(time.RFC3339Nano)),
					})
				}
			}
		}

		if !conv.ContextFrom.IsZero() && summary.From.Equal(conv.ContextFrom) {
			for _, ex := range summary.Excerpts {
				if !ex.TurnStartedAt.IsZero() && ex.TurnStartedAt.Before(conv.ContextFrom) {
					violations = append(violations, invariantViolation{
						Invariant: "INV-MEM-07.pre_context_from_leak",
						EntityID:  ex.MessageID,
						Message:   fmt.Sprintf("summary excerpt %q has TurnStartedAt %s before ContextFrom %s", ex.MessageID, ex.TurnStartedAt.UTC().Format(time.RFC3339Nano), conv.ContextFrom.UTC().Format(time.RFC3339Nano)),
					})
				}
			}
		}

		if sameEpoch {
			for _, prevDismissed := range sortedMapKeys(prev.dismissedIDs) {
				if !dismissedSet[prevDismissed] {
					violations = append(violations, invariantViolation{
						Invariant: "INV-MEM-02.dismissed_reappeared",
						EntityID:  prevDismissed,
						Message:   fmt.Sprintf("previously dismissed summary item ID %q was dropped from DismissedItemIDs", prevDismissed),
					})
				}
			}
			semanticallySuperseded := flushed.After(prev.flushedThrough) && summary.Model != "" && summary.Model != "extractive"
			if !semanticallySuperseded {
				for _, prevID := range sortedMapKeys(prev.summaryItems) {
					if _, stillPresent := currItems[prevID]; stillPresent {
						continue
					}
					if dismissedSet[prevID] {
						continue
					}
					prevItem := prev.summaryItems[prevID]
					if !simIsPreservedAsEvictedLog(prevID, prevItem, evictedEntries) {
						violations = append(violations, invariantViolation{
							Invariant: "INV-MEM-02.silent_eviction",
							EntityID:  prevID,
							Message:   fmt.Sprintf("summary item %q disappeared without dismissal, semantic batch supersession, or summary_evicted Daily Log entry", prevID),
						})
					}
				}
			}
		}

		if len(violations) == 0 {
			nextBase.hadSummary = true
			nextBase.epoch = summary.From
			nextBase.flushedThrough = flushed
			nextBase.extractedThrough = extracted
			nextBase.through = summary.Through
			nextBase.flushFailures = summary.FlushFailures
			nextBase.lastFailureThrough = summary.LastFailureThrough
			nextBase.extractFailures = summary.ExtractFailures
			nextBase.lastExtractFailureThrough = summary.LastExtractFailureThrough
			nextBase.summaryItems = currItems
			nextBase.dismissedIDs = dismissedSet
		}
	}

	if !conv.ContextFrom.IsZero() {
		if len(preContextLogEntries) > 0 {
			qToken := simFirstSearchableToken(preContextLogEntries[0].Text)
			if qToken != "" {
				hits, sErr := backend.SearchMemory(ctx, docstore.MemorySearchQuery{
					Owner:        owner,
					Query:        qToken,
					Layer:        docstore.MemoryLayerLog,
					CreatedAfter: conv.ContextFrom,
					Limit:        docstore.MaxMemorySearchLimit,
					Now:          now,
				})
				if sErr == nil {
					for _, h := range hits {
						if h.Chunk.Owner != owner {
							violations = append(violations, invariantViolation{
								Invariant: "INV-MEM-07.cross_user_reference",
								EntityID:  h.Chunk.ChunkID,
								Message:   fmt.Sprintf("SearchMemory returned chunk owned by %q for owner %q", h.Chunk.Owner, owner),
							})
						}
						if !h.Chunk.CreatedAt.IsZero() && h.Chunk.CreatedAt.Before(conv.ContextFrom) {
							violations = append(violations, invariantViolation{
								Invariant: "INV-MEM-07.pre_context_from_leak",
								EntityID:  h.Chunk.ChunkID,
								Message:   fmt.Sprintf("SearchMemory returned pre-ContextFrom log chunk %q (%s < %s)", h.Chunk.ChunkID, h.Chunk.CreatedAt.UTC().Format(time.RFC3339Nano), conv.ContextFrom.UTC().Format(time.RFC3339Nano)),
							})
						}
					}
				}
			}
		}
		for _, id := range sortedMapKeys(allMessages) {
			m := allMessages[id]
			if m.Role == "user" && !m.CreatedAt.IsZero() && m.CreatedAt.Before(conv.ContextFrom) {
				if _, rErr := userApp.Store.ReadConversationMessage(ctx, m.ID); !errors.Is(rErr, state.ErrNotFound) {
					violations = append(violations, invariantViolation{
						Invariant: "INV-MEM-07.pre_context_from_leak",
						EntityID:  m.ID,
						Message:   fmt.Sprintf("ReadConversationMessage returned pre-ContextFrom message %q (err=%v)", m.ID, rErr),
					})
				}
				break
			}
		}
	}

	if !conv.Pending() && conv.Active == "" {
		var convWake docstore.UserWake
		hasConvWake := false
		if wakes, wErr := backend.ListUserWakes(ctx, user.ID); wErr == nil {
			for _, w := range wakes {
				if w.Lane == docstore.LaneConversation {
					convWake = w
					hasConvWake = true
					break
				}
			}
		}

		virtualWakeAt := time.Time{}
		hasVirtualFutureWake := false
		if hasConvWake && convWake.Runnable {
			if virtAt, ok := backend.PendingUserWakeAt(user.ID, docstore.LaneConversation); ok {
				virtualWakeAt = virtAt
				hasVirtualFutureWake = virtAt.After(now)
			} else {
				realNow := time.Now().UTC()
				if convWake.WakeAt.After(realNow.Add(shortUserWakeThreshold)) {
					hasVirtualFutureWake = true
					virtualWakeAt = convWake.WakeAt
				} else if convWake.WakeAt.After(now) {
					hasVirtualFutureWake = true
					virtualWakeAt = convWake.WakeAt
				} else {
					virtualWakeAt = convWake.WakeAt
				}
			}
		}

		if hasConvWake {
			realNow := time.Now().UTC()
			if convWake.Owner != "" && (convWake.LeaseUntil == nil || convWake.LeaseUntil.After(realNow)) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-08.wake_lease_hygiene",
					EntityID:  docstore.LaneConversation,
					Message:   fmt.Sprintf("conversation wake retains active lease at quiescence (owner=%q lease_until_set=%v)", convWake.Owner, convWake.LeaseUntil != nil),
				})
			}
			if convWake.Runnable && !hasVirtualFutureWake && !virtualWakeAt.IsZero() && !virtualWakeAt.After(now) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-08.wake_lease_hygiene",
					EntityID:  docstore.LaneConversation,
					Message:   fmt.Sprintf("conversation wake remains runnable with wake_at=%s <= now=%s at quiescence", virtualWakeAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)),
				})
			}
		}

		summaryInEpoch := summaryExists && summary.From.Equal(conv.ContextFrom)
		inFlushBackoff := summaryInEpoch && summary.FlushFailures > 0 && !summary.UpdatedAt.IsZero() && now.Before(summary.UpdatedAt.Add(10*time.Minute))
		inExtractBackoff := summaryInEpoch && (summary.ExtractFailures > 0 || summary.FlushFailures > 0) && !summary.UpdatedAt.IsZero() && now.Before(summary.UpdatedAt.Add(10*time.Minute))

		if summary.Applies(conv.ContextFrom) && !summary.Through.IsZero() {
			if flushed.Before(summary.Through) {
				if !hasVirtualFutureWake && !inFlushBackoff {
					violations = append(violations, invariantViolation{
						Invariant: "INV-MEM-08.unflushed_watermark",
						Message:   fmt.Sprintf("unflushed turns remain after idle settle: FlushedThrough (%s) < Through (%s) with no scheduled future wake or active failure backoff", flushed.UTC().Format(time.RFC3339Nano), summary.Through.UTC().Format(time.RFC3339Nano)),
					})
				}
			} else if flushed.Equal(summary.Through) && len(summary.Excerpts) > 0 {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-08.stale_excerpts",
					Message:   fmt.Sprintf("FlushedThrough == Through (%s) at quiescence but %d retired excerpt(s) remain", flushed.UTC().Format(time.RFC3339Nano), len(summary.Excerpts)),
				})
			}
		}

		effectiveExtracted := conv.ContextFrom
		if summaryInEpoch {
			if ex := summary.EffectiveExtractedThrough(); ex.After(effectiveExtracted) {
				effectiveExtracted = ex
			}
		}
		latestCompletedAt, hasUnextracted := simLatestUnextractedCompletedTurn(allTurns, conv.ContextFrom, effectiveExtracted)
		if hasUnextracted {
			idleAnchor := latestCompletedAt
			if conv.LatestAt.After(idleAnchor) {
				idleAnchor = conv.LatestAt
			}
			if !idleAnchor.IsZero() && !now.Before(idleAnchor.Add(state.IdleExtractDelay)) && !hasVirtualFutureWake && !inExtractBackoff {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-08.unextracted_after_idle",
					Message:   fmt.Sprintf("post-drain quiescence has unextracted completed turn(s) up to %s > EffectiveExtractedThrough (%s) after IdleExtractDelay (%s) with no scheduled future wake or active backoff", latestCompletedAt.UTC().Format(time.RFC3339Nano), effectiveExtracted.UTC().Format(time.RFC3339Nano), state.IdleExtractDelay),
				})
			}
		}
	}

	tracker.set(user.ID, nextBase)

	snap.InvariantViolations = violations
	snap.InvariantsOK = len(violations) == 0
	return snap, nil
}

func loadCollectionDocs[T any](ctx context.Context, backend docstore.Backend, owner, collection, decodeInvariant string) (map[string]T, []invariantViolation) {
	out := make(map[string]T)
	var vs []invariantViolation
	recs, err := backend.Query(ctx, docstore.QuerySpec{
		Collection: "owners/" + owner + "/" + collection,
	})
	if err != nil {
		return out, vs
	}
	for _, rec := range recs {
		docID := rec.Path
		if idx := strings.LastIndex(docID, "/"); idx >= 0 {
			docID = docID[idx+1:]
		}
		var val T
		if err := docstore.Decode(rec.Fields, &val); err != nil {
			if decodeInvariant != "" {
				vs = append(vs, invariantViolation{
					Invariant: decodeInvariant,
					EntityID:  docID,
					Message:   fmt.Sprintf("failed to decode %s document: %v", collection, err),
				})
			}
			continue
		}
		out[docID] = val
	}
	return out, vs
}

func summaryDocExists(ctx context.Context, backend docstore.Backend, owner string, summary state.ConversationSummary) bool {
	if _, err := backend.Get(ctx, "owners/"+owner+"/conversation/summary"); err == nil {
		return true
	}
	return !summary.From.IsZero() ||
		!summary.Through.IsZero() ||
		!summary.FlushedThrough.IsZero() ||
		!summary.RefinementThrough.IsZero() ||
		!summary.ExtractedThrough.IsZero() ||
		summary.FlushFailures > 0 ||
		summary.ExtractFailures > 0 ||
		!summary.LastDreamAt.IsZero() ||
		!summary.UpdatedAt.IsZero()
}

func loadManualDemotedIDs(ctx context.Context, backend docstore.Backend, owner string) map[string]bool {
	recs, err := backend.Query(ctx, docstore.QuerySpec{
		Collection: "owners/" + owner + "/mutation_receipts",
	})
	if err != nil || len(recs) == 0 {
		return nil
	}
	demoted := map[string]bool{}
	for _, rec := range recs {
		raw, _ := rec.Fields["resource"].(string)
		if raw == "" {
			continue
		}
		var payload struct {
			Memory *struct {
				ID string `json:"id"`
			} `json:"memory"`
			Demoted  json.RawMessage `json:"demoted"`
			LogEntry *struct {
				ID string `json:"id"`
			} `json:"log_entry"`
		}
		if json.Unmarshal([]byte(raw), &payload) != nil || payload.LogEntry == nil || payload.LogEntry.ID == "" {
			continue
		}
		if payload.Memory != nil && payload.Memory.ID != "" {
			demoted[payload.Memory.ID] = true
		}
		if len(payload.Demoted) > 0 && payload.Demoted[0] == '{' {
			var single struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(payload.Demoted, &single) == nil && single.ID != "" {
				demoted[single.ID] = true
			}
		}
	}
	return demoted
}

func simLatestUnextractedCompletedTurn(
	turns map[string]state.ConversationTurn,
	contextFrom time.Time,
	effectiveExtracted time.Time,
) (latestCompletedAt time.Time, hasUnextracted bool) {
	for _, id := range sortedMapKeys(turns) {
		turn := turns[id]
		if turn.Status != "completed" || turn.StartedAt.IsZero() {
			continue
		}
		if !contextFrom.IsZero() && turn.StartedAt.Before(contextFrom) {
			continue
		}
		if turn.StartedAt.After(latestCompletedAt) {
			latestCompletedAt = turn.StartedAt
		}
		if turn.StartedAt.After(effectiveExtracted) {
			hasUnextracted = true
		}
	}
	return latestCompletedAt, hasUnextracted
}

func simCheckUnflushedExcerptCoverage(
	sum state.ConversationSummary,
	flushed time.Time,
	excerptsBytes int,
	turns map[string]state.ConversationTurn,
) []invariantViolation {
	var unflushed []state.ConversationTurn
	for _, id := range sortedMapKeys(turns) {
		t := turns[id]
		if t.Status != "completed" {
			continue
		}
		if !sum.From.IsZero() && t.StartedAt.Before(sum.From) {
			continue
		}
		if !t.StartedAt.After(flushed) || t.StartedAt.After(sum.Through) {
			continue
		}
		if simTurnHasExcerptableContent(t) {
			unflushed = append(unflushed, t)
		}
	}
	if len(unflushed) == 0 {
		return nil
	}
	slices.SortFunc(unflushed, func(a, b state.ConversationTurn) int {
		if a.StartedAt.Before(b.StartedAt) {
			return -1
		}
		if a.StartedAt.After(b.StartedAt) {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})

	if len(sum.Excerpts) == 0 {
		return []invariantViolation{{
			Invariant: "INV-MEM-01.excerpt_coverage",
			EntityID:  unflushed[0].ID,
			Message:   fmt.Sprintf("%d completed turn(s) exist in (FlushedThrough=%s, Through=%s] but Summary.Excerpts is empty", len(unflushed), flushed.UTC().Format(time.RFC3339Nano), sum.Through.UTC().Format(time.RFC3339Nano)),
		}}
	}

	excerptMsgIDs := make(map[string]bool, len(sum.Excerpts))
	var earliestKept time.Time
	for _, ex := range sum.Excerpts {
		if ex.MessageID != "" {
			excerptMsgIDs[ex.MessageID] = true
		}
		if !ex.TurnStartedAt.IsZero() && (earliestKept.IsZero() || ex.TurnStartedAt.Before(earliestKept)) {
			earliestKept = ex.TurnStartedAt
		}
	}

	var vs []invariantViolation
	for i := len(unflushed) - 1; i >= 0; i-- {
		t := unflushed[i]
		if simTurnRepresentedInExcerpts(t, sum.Excerpts, excerptMsgIDs) {
			continue
		}
		olderThanEarliest := !earliestKept.IsZero() && t.StartedAt.Before(earliestKept)
		estBytes := simEstimateTurnExcerptBytes(t)
		if !olderThanEarliest || excerptsBytes+estBytes <= maxConversationExcerptBytes {
			vs = append(vs, invariantViolation{
				Invariant: "INV-MEM-01.excerpt_coverage",
				EntityID:  t.ID,
				Message:   fmt.Sprintf("completed turn %q (started_at=%s) in (FlushedThrough, Through] is missing from Summary.Excerpts (excerpts_bytes=%d)", t.ID, t.StartedAt.UTC().Format(time.RFC3339Nano), excerptsBytes),
			})
		}
	}
	return vs
}

func simTurnHasExcerptableContent(t state.ConversationTurn) bool {
	for _, in := range t.Inputs {
		if in.IsUser() && strings.TrimSpace(in.Text) != "" {
			return true
		}
	}
	return simPublishedTurnReply(t) != ""
}

func simTurnRepresentedInExcerpts(t state.ConversationTurn, excerpts []state.ConversationExcerpt, msgIDs map[string]bool) bool {
	if msgIDs[state.AssistantMessageID(t.ID)] {
		return true
	}
	for _, in := range t.Inputs {
		if in.IsUser() && msgIDs[state.UserMessageID(in.ID)] {
			return true
		}
	}
	for _, ex := range excerpts {
		if !ex.TurnStartedAt.IsZero() && ex.TurnStartedAt.Equal(t.StartedAt) {
			return true
		}
	}
	return false
}

func simEstimateTurnExcerptBytes(t state.ConversationTurn) int {
	var sample []state.ConversationExcerpt
	for _, in := range t.Inputs {
		if in.IsUser() && strings.TrimSpace(in.Text) != "" {
			txt := in.Text
			if len(txt) > state.MaxSummaryQuote {
				txt = docstore.ClipUTF8(txt, state.MaxSummaryQuote)
			}
			sample = append(sample, state.ConversationExcerpt{
				MessageID:     state.UserMessageID(in.ID),
				Role:          "user",
				Text:          txt,
				TurnStartedAt: t.StartedAt,
			})
		}
	}
	if reply := simPublishedTurnReply(t); reply != "" {
		if len(reply) > state.MaxSummaryQuote {
			reply = docstore.ClipUTF8(reply, state.MaxSummaryQuote)
		}
		sample = append(sample, state.ConversationExcerpt{
			MessageID:     state.AssistantMessageID(t.ID),
			Role:          "assistant",
			Text:          reply,
			TurnStartedAt: t.StartedAt,
		})
	}
	raw, _ := json.Marshal(sample)
	return len(raw)
}

func simPublishedTurnReply(t state.ConversationTurn) string {
	for i := len(t.Protocol) - 1; i >= 0; i-- {
		var m struct {
			Role    string  `json:"role"`
			Content *string `json:"content"`
		}
		if json.Unmarshal([]byte(t.Protocol[i]), &m) != nil || m.Role != "assistant" || m.Content == nil {
			continue
		}
		var structured struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal([]byte(*m.Content), &structured) == nil && structured.Text != nil {
			return *structured.Text
		}
		return *m.Content
	}
	return ""
}

func simIsPreservedAsEvictedLog(itemID string, item state.SummaryItem, evicted []state.MemoryLogEntry) bool {
	wantID := "evict_" + itemID
	for _, e := range evicted {
		if e.ID == wantID {
			return true
		}
		if item.InputID != "" && e.SourceInputID == item.InputID && item.Quote != "" && strings.Contains(e.Text, item.Quote) {
			return true
		}
	}
	return false
}

func simHasRefusedMemoryChange(changes []state.MemoryChange) bool {
	for _, ch := range changes {
		if ch.Error != "" {
			return true
		}
	}
	return false
}

func simExpectedCoreChunk(owner string, m state.Memory) docstore.MemoryChunk {
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

func simExpectedLogChunk(owner, date string, e state.MemoryLogEntry) docstore.MemoryChunk {
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

func simMemoryChunksEqual(a, b docstore.MemoryChunk) bool {
	return a.Owner == b.Owner &&
		a.ChunkID == b.ChunkID &&
		a.Layer == b.Layer &&
		a.LogDate == b.LogDate &&
		a.Section == b.Section &&
		a.About == b.About &&
		a.Body == b.Body &&
		a.Provenance == b.Provenance &&
		a.SourceInput == b.SourceInput &&
		a.RunID == b.RunID &&
		a.Pinned == b.Pinned &&
		a.CreatedAt.Truncate(time.Microsecond).Equal(b.CreatedAt.Truncate(time.Microsecond)) &&
		a.UpdatedAt.Truncate(time.Microsecond).Equal(b.UpdatedAt.Truncate(time.Microsecond))
}

func simFirstSearchableToken(text string) string {
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
	}) {
		if len(field) >= 3 {
			return field
		}
	}
	return strings.TrimSpace(text)
}

func sortedMapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
