package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/app"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

func setupInvariantTestUser(t *testing.T, start time.Time) (context.Context, users.User, *app.App, *simClockBackend, *simClock, *docstore.Client) {
	t.Helper()
	ctx := context.Background()
	svc, _, simBackend, clock := newDrainTestEnv(t, start, "")
	user := mustSeedSimUser(t, svc, uuid.NewString(), "inv@example.com")
	userApp, release, err := svc.Acquire(ctx, user)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(release)
	configureSimAppClocks(userApp, clock)
	return ctx, user, userApp, simBackend, clock, docstore.New(simBackend)
}

func mustSetDoc(t *testing.T, ctx context.Context, ref *docstore.DocumentRef, val any) {
	t.Helper()
	if _, err := ref.Set(ctx, val); err != nil {
		t.Fatalf("Set %s: %v", ref.Path, err)
	}
}

func hasViolationPrefix(vs []invariantViolation, prefix string) bool {
	for _, v := range vs {
		if v.Invariant == prefix || strings.HasPrefix(v.Invariant, prefix+".") {
			return true
		}
	}
	return false
}

func TestInspectUserState_INV_MEM_01_And_08_ExtractedThroughAndExcerpts(t *testing.T) {
	start := time.Date(2027, 1, 10, 9, 0, 0, 0, time.UTC)
	ctx, user, userApp, backend, clock, client := setupInvariantTestUser(t, start)
	owner := user.DataOwner()
	sumRef := client.Collection("owners").Doc(owner).Collection("conversation").Doc("summary")
	turnsCol := client.Collection("owners").Doc(owner).Collection("conversation_turns")
	tracker := newSimInvariantTracker()

	t0 := start
	mustSetDoc(t, ctx, turnsCol.Doc("turn-1"), state.ConversationTurn{
		ID:        "turn-1",
		Status:    "completed",
		StartedAt: t0,
		Inputs: []state.ConversationInput{{
			ID:        "in-1",
			Text:      "Remember my passport expires in November 2028.",
			CreatedAt: t0,
		}},
	})
	clock.Set(t0.Add(state.IdleExtractDelay + time.Second))
	snap, err := inspectUserStateWithTracker(ctx, user, userApp, backend, clock.Now(), tracker)
	if err != nil {
		t.Fatalf("inspectUserState: %v", err)
	}
	if !hasViolationPrefix(snap.InvariantViolations, "INV-MEM-08.unextracted_after_idle") {
		t.Fatalf("expected INV-MEM-08.unextracted_after_idle, got %+v", snap.InvariantViolations)
	}

	mustSetDoc(t, ctx, sumRef, state.ConversationSummary{
		ExtractedThrough: t0,
		UpdatedAt:        clock.Now(),
	})
	snap, err = inspectUserStateWithTracker(ctx, user, userApp, backend, clock.Now(), tracker)
	if err != nil {
		t.Fatalf("inspectUserState clean: %v", err)
	}
	if !snap.InvariantsOK {
		t.Fatalf("expected clean snapshot after decoupled extraction, got %+v", snap.InvariantViolations)
	}

	mustSetDoc(t, ctx, sumRef, state.ConversationSummary{
		Through:          t0,
		FlushedThrough:   t0,
		ExtractedThrough: t0,
		Excerpts: []state.ConversationExcerpt{{
			MessageID:     state.UserMessageID("in-1"),
			Role:          "user",
			Text:          "stale excerpt",
			TurnStartedAt: t0,
		}},
		UpdatedAt: clock.Now(),
	})
	snap, err = inspectUserStateWithTracker(ctx, user, userApp, backend, clock.Now(), tracker)
	if err != nil {
		t.Fatalf("inspectUserState stale excerpts: %v", err)
	}
	if !hasViolationPrefix(snap.InvariantViolations, "INV-MEM-08.stale_excerpts") {
		t.Fatalf("expected INV-MEM-08.stale_excerpts, got %+v", snap.InvariantViolations)
	}

	mustSetDoc(t, ctx, sumRef, state.ConversationSummary{
		Through:          t0.Add(time.Minute),
		FlushedThrough:   t0.Add(time.Minute),
		ExtractedThrough: t0,
		UpdatedAt:        clock.Now(),
	})
	snap, err = inspectUserStateWithTracker(ctx, user, userApp, backend, clock.Now(), tracker)
	if err != nil {
		t.Fatalf("inspectUserState bad extracted: %v", err)
	}
	if !hasViolationPrefix(snap.InvariantViolations, "INV-MEM-01.watermark_bounds") {
		t.Fatalf("expected INV-MEM-01.watermark_bounds, got %+v", snap.InvariantViolations)
	}

	mustSetDoc(t, ctx, sumRef, state.ConversationSummary{
		ExtractedThrough:          t0,
		ExtractFailures:           1,
		LastExtractFailureThrough: t0,
		UpdatedAt:                 clock.Now(),
	})
	snap, err = inspectUserStateWithTracker(ctx, user, userApp, backend, clock.Now(), tracker)
	if err != nil {
		t.Fatalf("inspectUserState failure watermark: %v", err)
	}
	if !hasViolationPrefix(snap.InvariantViolations, "INV-MEM-01.failure_watermark_advance") {
		t.Fatalf("expected INV-MEM-01.failure_watermark_advance, got %+v", snap.InvariantViolations)
	}
}

func TestInspectUserState_INV_MEM_02_03_04_05_06_Parity(t *testing.T) {
	start := time.Date(2027, 1, 11, 10, 0, 0, 0, time.UTC)
	ctx, user, userApp, backend, clock, client := setupInvariantTestUser(t, start)
	owner := user.DataOwner()
	ownerRef := client.Collection("owners").Doc(owner)
	tracker := newSimInvariantTracker()

	mustSetDoc(t, ctx, ownerRef.Collection("conversation_inputs").Doc("in-user-1"), state.ConversationInput{
		ID:        "in-user-1",
		Text:      "My dog is named Biscuit.",
		CreatedAt: start,
	})
	pinnedMem := state.Memory{
		ID:            "mem-pin-1",
		Text:          "Dog's name is Biscuit.",
		About:         "Biscuit",
		Section:       "Personal",
		Pinned:        true,
		Provenance:    state.ProvenanceUserExplicit,
		SourceInputID: "in-user-1",
		CreatedAt:     start,
		UpdatedAt:     start,
	}
	mustSetDoc(t, ctx, ownerRef.Collection("memories").Doc(pinnedMem.ID), pinnedMem)
	if err := backend.UpsertMemoryChunk(ctx, simExpectedCoreChunk(owner, pinnedMem)); err != nil {
		t.Fatalf("upsert core chunk: %v", err)
	}
	inTok, cachedTok, outTok := int64(500), int64(400), int64(50)
	costUSD := 0.0025
	mustSetDoc(t, ctx, ownerRef.Collection("model_usage").Doc("u-1"), state.Usage{
		Model:        "gpt-6.1-sol",
		Provider:     "openai",
		InputTokens:  &inTok,
		CachedTokens: &cachedTok,
		OutputTokens: &outTok,
		CostUSD:      &costUSD,
	})
	item := state.SummaryItem{
		InputID: "in-user-1",
		Quote:   "My dog is named Biscuit.",
		Note:    "User has a dog named Biscuit.",
	}
	itemID := state.SummaryItemID(item)
	mustSetDoc(t, ctx, ownerRef.Collection("conversation").Doc("summary"), state.ConversationSummary{
		From:             start,
		Through:          start,
		FlushedThrough:   start,
		ExtractedThrough: start,
		Items:            []state.SummaryItem{item},
		Gist:             "User mentioned their dog Biscuit.",
		Model:            "extractive",
		UpdatedAt:        start,
	})

	snap, err := inspectUserStateWithTracker(ctx, user, userApp, backend, clock.Now(), tracker)
	if err != nil {
		t.Fatalf("baseline inspect: %v", err)
	}
	if !snap.InvariantsOK {
		t.Fatalf("expected clean baseline snapshot, got %+v", snap.InvariantViolations)
	}
	if len(snap.ModelUsage) != 1 || snap.ModelUsage[0].CachedTokens == nil || *snap.ModelUsage[0].CachedTokens != 400 {
		t.Fatalf("expected 1 ModelUsage record with 400 cached tokens, got %+v", snap.ModelUsage)
	}

	badChunk := simExpectedCoreChunk(owner, pinnedMem)
	badChunk.Body = "Corrupted FTS body"
	if err := backend.UpsertMemoryChunk(ctx, badChunk); err != nil {
		t.Fatalf("upsert corrupted chunk: %v", err)
	}
	snap, _ = inspectUserStateWithTracker(ctx, user, userApp, backend, clock.Now(), tracker)
	if !hasViolationPrefix(snap.InvariantViolations, "INV-MEM-06.fts_chunk_mismatch") {
		t.Fatalf("expected INV-MEM-06.fts_chunk_mismatch, got %+v", snap.InvariantViolations)
	}
	_ = backend.UpsertMemoryChunk(ctx, simExpectedCoreChunk(owner, pinnedMem))

	mustSetDoc(t, ctx, ownerRef.Collection("conversation").Doc("summary"), state.ConversationSummary{
		From:             start,
		Through:          start,
		FlushedThrough:   start,
		ExtractedThrough: start,
		Items:            []state.SummaryItem{},
		Gist:             "Empty.",
		Model:            "extractive",
		UpdatedAt:        start.Add(time.Second),
	})
	_ = backend.DeleteBatch(ctx, []string{"owners/" + owner + "/memories/" + pinnedMem.ID})
	_ = backend.DeleteMemoryChunk(ctx, owner, docstore.MemoryCoreChunkID(pinnedMem.ID))
	demotedEntry := state.MemoryLogEntry{
		ID:            "demote_" + pinnedMem.ID,
		Text:          pinnedMem.Text,
		Provenance:    state.ProvenanceUserExplicit,
		SourceInputID: "in-user-1",
		CreatedAt:     start.Add(time.Second),
		UpdatedAt:     start.Add(time.Second),
	}
	mustSetDoc(t, ctx, ownerRef.Collection("memory_logs").Doc("2027-01-11"), state.MemoryLog{
		Date:    "2027-01-11",
		Entries: []state.MemoryLogEntry{demotedEntry},
	})
	_ = backend.UpsertMemoryChunk(ctx, simExpectedLogChunk(owner, "2027-01-11", demotedEntry))

	snap, _ = inspectUserStateWithTracker(ctx, user, userApp, backend, clock.Now(), tracker)
	if !hasViolationPrefix(snap.InvariantViolations, "INV-MEM-02.silent_eviction") {
		t.Fatalf("expected INV-MEM-02.silent_eviction for %q, got %+v", itemID, snap.InvariantViolations)
	}
	if !hasViolationPrefix(snap.InvariantViolations, "INV-MEM-03.pinned_auto_demoted") {
		t.Fatalf("expected INV-MEM-03.pinned_auto_demoted, got %+v", snap.InvariantViolations)
	}

	receiptPayload, _ := json.Marshal(map[string]any{
		"memory":    map[string]string{"id": pinnedMem.ID},
		"log_entry": map[string]string{"id": demotedEntry.ID},
	})
	mustSetDoc(t, ctx, ownerRef.Collection("mutation_receipts").Doc("rcpt-1"), map[string]any{
		"resource": string(receiptPayload),
	})
	snap, _ = inspectUserStateWithTracker(ctx, user, userApp, backend, clock.Now(), tracker)
	if hasViolationPrefix(snap.InvariantViolations, "INV-MEM-03.pinned_auto_demoted") {
		t.Fatalf("expected manual demote receipt to clear INV-MEM-03.pinned_auto_demoted, got %+v", snap.InvariantViolations)
	}

	badMem := state.Memory{
		ID:            "mem-bad-1",
		Text:          "Injected fact",
		Provenance:    state.ProvenancePreCompactFlush,
		SourceInputID: "missing-input",
		CreatedAt:     start,
		UpdatedAt:     start,
	}
	mustSetDoc(t, ctx, ownerRef.Collection("memories").Doc(badMem.ID), badMem)
	_ = backend.UpsertMemoryChunk(ctx, simExpectedCoreChunk(owner, badMem))
	mustSetDoc(t, ctx, ownerRef.Collection("conversation_attempts").Doc("att-failed"), state.ConversationAttempt{
		ID:     "att-failed",
		Status: "failed",
		Memory: []state.MemoryChange{{Kind: state.MemoryRemember, ID: badMem.ID}},
	})
	snap, _ = inspectUserStateWithTracker(ctx, user, userApp, backend, clock.Now(), tracker)
	if !hasViolationPrefix(snap.InvariantViolations, "INV-MEM-04.unverified_source_input") {
		t.Fatalf("expected INV-MEM-04.unverified_source_input, got %+v", snap.InvariantViolations)
	}
	if !hasViolationPrefix(snap.InvariantViolations, "INV-MEM-05.uncompleted_attempt_applied") {
		t.Fatalf("expected INV-MEM-05.uncompleted_attempt_applied, got %+v", snap.InvariantViolations)
	}
}

func TestInspectUserState_INV_MEM_07_RealTokenContextFromIsolation(t *testing.T) {
	start := time.Date(2027, 1, 12, 8, 0, 0, 0, time.UTC)
	ctx, user, userApp, backend, clock, client := setupInvariantTestUser(t, start)
	owner := user.DataOwner()
	ownerRef := client.Collection("owners").Doc(owner)

	entry := state.MemoryLogEntry{
		ID:            "log-pre-1",
		Text:          "Zirconium prototype serial ZX9912 shipped to Zurich.",
		Provenance:    state.ProvenancePreCompactFlush,
		SourceInputID: "ui",
		CreatedAt:     start,
		UpdatedAt:     start,
	}
	mustSetDoc(t, ctx, ownerRef.Collection("memory_logs").Doc("2027-01-12"), state.MemoryLog{
		Date:    "2027-01-12",
		Entries: []state.MemoryLogEntry{entry},
	})
	_ = backend.UpsertMemoryChunk(ctx, simExpectedLogChunk(owner, "2027-01-12", entry))

	mustSetDoc(t, ctx, ownerRef.Collection("messages").Doc("msg-pre-1"), state.Message{
		ID:        "msg-pre-1",
		Role:      "user",
		Text:      "Zirconium prototype serial ZX9912.",
		CreatedAt: start,
	})

	cutoff := start.Add(time.Hour)
	clock.Set(cutoff)
	mustSetDoc(t, ctx, ownerRef.Collection("conversation").Doc("control"), state.ConversationControl{
		ContextFrom: cutoff,
	})

	if tok := simFirstSearchableToken(entry.Text); tok != "Zirconium" {
		t.Fatalf("simFirstSearchableToken = %q, want Zirconium", tok)
	}

	snap, err := inspectUserState(ctx, user, userApp, backend, clock.Now())
	if err != nil {
		t.Fatalf("inspectUserState: %v", err)
	}
	if !snap.InvariantsOK {
		t.Fatalf("expected clean ContextFrom isolation, got %+v", snap.InvariantViolations)
	}
	if !snap.ContextFrom.Equal(cutoff) {
		t.Fatalf("snap.ContextFrom = %v, want %v", snap.ContextFrom, cutoff)
	}
}
