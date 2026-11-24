package main

import (
	"context"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"nori/internal/app"
	"nori/internal/permissions"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

type invariantViolation struct {
	Invariant string `json:"invariant"`
	EntityID  string `json:"entity_id,omitempty"`
	Message   string `json:"message"`
}

type simUserSnapshot struct {
	SimNow              time.Time                 `json:"sim_now"`
	UserID              string                    `json:"user_id"`
	Email               string                    `json:"email"`
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
	InvariantViolations []invariantViolation      `json:"invariant_violations"`
	InvariantsOK        bool                      `json:"invariants_ok"`
}

func inspectUserState(ctx context.Context, user users.User, userApp *app.App, backend *simClockBackend, now time.Time) (simUserSnapshot, error) {
	snap := simUserSnapshot{
		SimNow: now.UTC(),
		UserID: user.ID,
		Email:  user.Email,
	}

	conv, _, err := userApp.Store.ConversationStatus(ctx)
	if err != nil {
		return snap, fmt.Errorf("conversation: %w", err)
	}
	snap.ConversationPending = conv.Pending()
	snap.ConversationActive = conv.Active

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

	chunks, err := backend.ListMemoryChunks(ctx, user.DataOwner())
	if err != nil {
		return snap, fmt.Errorf("list_memory_chunks: %w", err)
	}
	snap.FTSChunkCount = len(chunks)

	var violations []invariantViolation

	// INV-MEM-01: Watermark ordering (From <= FlushedThrough <= Through)
	flushed := summary.EffectiveFlushedThrough()
	if !summary.From.IsZero() && !flushed.IsZero() && flushed.Before(summary.From) {
		violations = append(violations, invariantViolation{
			Invariant: "INV-MEM-01",
			Message:   fmt.Sprintf("FlushedThrough (%s) < From (%s)", flushed, summary.From),
		})
	}
	if !flushed.IsZero() && !summary.Through.IsZero() && flushed.After(summary.Through) {
		violations = append(violations, invariantViolation{
			Invariant: "INV-MEM-01",
			Message:   fmt.Sprintf("FlushedThrough (%s) > Through (%s)", flushed, summary.Through),
		})
	}

	// INV-MEM-03 & INV-MEM-04: Core Memory budget, UTF-8 validity, provenance & no task_outcome in Core
	expectedChunks := make(map[string]bool, len(memories)+len(logs)*4)
	coreBytes := 0
	for _, m := range memories {
		coreBytes += len(m.Text)
		expectedChunks[docstore.MemoryCoreChunkID(m.ID)] = true
		if len(m.Text) == 0 || len(m.Text) > state.MaxMemoryText {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-03",
				EntityID:  m.ID,
				Message:   fmt.Sprintf("core memory text length %d outside [1..%d]", len(m.Text), state.MaxMemoryText),
			})
		}
		if !utf8.ValidString(m.Text) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-03",
				EntityID:  m.ID,
				Message:   "core memory text is invalid UTF-8",
			})
		}
		if !m.CreatedAt.IsZero() && !m.UpdatedAt.IsZero() && m.UpdatedAt.Before(m.CreatedAt) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-03",
				EntityID:  m.ID,
				Message:   fmt.Sprintf("UpdatedAt (%s) < CreatedAt (%s)", m.UpdatedAt, m.CreatedAt),
			})
		}
		if m.EffectiveProvenance() == state.ProvenanceTaskOutcome {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-04",
				EntityID:  m.ID,
				Message:   "task_outcome provenance leaked into Core Memory",
			})
		}
	}
	snap.CoreBytes = coreBytes
	if coreBytes > state.CoreMemoryBytes {
		violations = append(violations, invariantViolation{
			Invariant: "INV-MEM-03",
			Message:   fmt.Sprintf("total Core Memory bytes %d exceeds CoreMemoryBytes %d", coreBytes, state.CoreMemoryBytes),
		})
	}

	logEntries := 0
	for _, logDoc := range logs {
		for _, entry := range logDoc.Entries {
			logEntries++
			expectedChunks[docstore.MemoryLogChunkID(logDoc.Date, entry.ID)] = true
			if !utf8.ValidString(entry.Text) {
				violations = append(violations, invariantViolation{
					Invariant: "INV-MEM-06",
					EntityID:  entry.ID,
					Message:   "daily log entry text is invalid UTF-8",
				})
			}
		}
	}
	snap.DailyLogEntryCount = logEntries

	// INV-MEM-06: Exact 1-to-1 bijection between nori_records (Core U DailyLogs) and nori_memory_chunks
	actualChunks := make(map[string]bool, len(chunks))
	for _, ch := range chunks {
		actualChunks[ch.ChunkID] = true
		if !expectedChunks[ch.ChunkID] {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-06",
				EntityID:  ch.ChunkID,
				Message:   "orphan chunk in nori_memory_chunks with no matching Core or Daily Log record",
			})
		}
	}
	missingIDs := make([]string, 0)
	for cid := range expectedChunks {
		if !actualChunks[cid] {
			missingIDs = append(missingIDs, cid)
		}
	}
	sort.Strings(missingIDs)
	for _, cid := range missingIDs {
		violations = append(violations, invariantViolation{
			Invariant: "INV-MEM-06",
			EntityID:  cid,
			Message:   "record missing from nori_memory_chunks FTS index",
		})
	}

	// INV-MEM-07: ContextFrom isolation on FTS search
	if !conv.ContextFrom.IsZero() {
		hits, sErr := backend.SearchMemory(ctx, docstore.MemorySearchQuery{
			Owner:        user.DataOwner(),
			Query:        "the",
			Layer:        docstore.MemoryLayerLog,
			CreatedAfter: conv.ContextFrom,
			Limit:        20,
			Now:          now,
		})
		if sErr == nil {
			for _, h := range hits {
				if h.Chunk.CreatedAt.Before(conv.ContextFrom) {
					violations = append(violations, invariantViolation{
						Invariant: "INV-MEM-07",
						EntityID:  h.Chunk.ChunkID,
						Message:   "pre-ContextFrom log chunk returned by SearchMemory",
					})
				}
			}
		}
	}

	// INV-MEM-08: Quiescence check (reported when conversation has no active/pending work)
	if !conv.Pending() && conv.Active == "" {
		if !summary.Through.IsZero() && flushed.Before(summary.Through) {
			violations = append(violations, invariantViolation{
				Invariant: "INV-MEM-08",
				Message:   fmt.Sprintf("unflushed turns remain after idle settle: FlushedThrough (%s) < Through (%s)", flushed, summary.Through),
			})
		}
	}

	snap.InvariantViolations = violations
	snap.InvariantsOK = len(violations) == 0
	return snap, nil
}
