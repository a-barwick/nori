package sim_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/sim"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/transcript"
)

type regressionKind string

const (
	regressionCoordination regressionKind = "coordination"
	regressionFullStack    regressionKind = "fullstack"
)

type regressionSeed struct {
	name        string
	kind        regressionKind
	seed        int64
	steps       int
	description string
}

// pinnedRegressionSeeds is the permanent registry of simulation seeds that
// previously uncovered real bugs in Nori. Every entry always runs in `go test`
// regardless of `-sim-seeds`.
var pinnedRegressionSeeds = []regressionSeed{
	{
		name:        "coord_seed_13_stop_paused_fault_undelivered_command",
		kind:        regressionCoordination,
		seed:        13,
		steps:       120,
		description: "RepairLeases must reconcile undelivered commands on terminal clean runs when StopPaused or task.Runner fails mid-delivery after persisting terminal state (fixed in core/internal/app/app.go)",
	},
	{
		name:        "coord_seed_491_finishing_parent_and_child_subtask",
		kind:        regressionCoordination,
		seed:        491,
		steps:       120,
		description: "finishing parent run with running child subtask must converge cleanly without stuck leases or undelivered commands",
	},
	{
		name:        "fullstack_seed_93_resume_with_pending_stop",
		kind:        regressionFullStack,
		seed:        93,
		steps:       100,
		description: "resume/repair after crash or pause with pending Stop command must converge cleanly",
	},
	{
		name:        "fullstack_seed_17_memory_mutations_and_rapid_bursts",
		kind:        regressionFullStack,
		seed:        17,
		steps:       120,
		description: "interleaved Core/Log/Working memory mutations, rapid chat bursts, and store faults must converge with FlushedThrough == Through",
	},
	{
		name:        "fullstack_seed_104_coupled_compaction_and_idle_wakes",
		kind:        regressionFullStack,
		seed:        104,
		steps:       120,
		description: "coupled pre-compaction flush, virtual-clock idle jumps (+2m/+15m/+12h), and delayed LaneConversation wakes must quiesce cleanly",
	},
	{
		name:        "fullstack_seed_188_autodream_and_core_overflow_chaos",
		kind:        regressionFullStack,
		seed:        188,
		steps:       120,
		description: "Core > 24 KiB overflow auto-demotion, pinned preservation, and AutoDream consolidation under full-stack chaos",
	},
	{
		name:        "fullstack_seed_207_short_session_idle_extract_and_inner_recall",
		kind:        regressionFullStack,
		seed:        207,
		steps:       120,
		description: "2m session-idle memory extraction, 3-turn fast threshold extract, +2m5s virtual-clock jumps, and concurrent inner-task memory_search recall must converge cleanly",
	},
	{
		name:        "fullstack_seed_33_repeat_log_promote_dedup",
		kind:        regressionFullStack,
		seed:        33,
		steps:       120,
		description: "promoting the same Daily Log entry twice with distinct request IDs must deduplicate in place rather than violating INV-MEM-05.duplicate_memory (fixed in core/internal/state/records/memory.go)",
	},
	{
		name:        "fullstack_seed_311_ask_rules_remember_rule_autoreview",
		kind:        regressionFullStack,
		seed:        311,
		steps:       120,
		description: "3-state permission rules (deny > ask > allow), atomic Always allow (remember_rule), 30m approval expiry, and action-boundary AutoReview under full-stack chaos",
	},
	{
		name:        "fullstack_seed_419_buffered_compaction_fast_extract_30m_expiry",
		kind:        regressionFullStack,
		seed:        419,
		steps:       120,
		description: "buffered compaction (16 user turns / 28 total), fast 3-turn/2m memory extraction, single-call outer follow-ups, and 30m expiry under multi-user faults",
	},
}

func TestRegressionSeeds(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	for _, tc := range pinnedRegressionSeeds {
		t.Run(tc.name, func(t *testing.T) {
			switch tc.kind {
			case regressionCoordination:
				cfg := configForSeed(tc.seed)
				w := sim.NewWorld(tc.seed, cfg)
				if err := w.RunSimulation(ctx, tc.steps); err != nil {
					if *simVerbose {
						t.Logf("\n%s\n--- full trace ---\n%s", w.DebugDump(ctx), w.Trace.FormatLast(0))
					}
					t.Fatalf(
						"regression seed failed (%s):\n%v\n\nRepro:\n  go test -v ./internal/sim -run TestCoordinationSim -sim-seed=%d -sim-steps=%d -sim-verbose",
						tc.description, err, tc.seed, tc.steps,
					)
				}

			case regressionFullStack:
				cfg := fullConfigForSeed(tc.seed)
				w := sim.NewFullWorld(tc.seed, cfg)
				if err := w.RunFullSimulation(ctx, tc.steps); err != nil {
					if *simVerbose {
						t.Logf("\n%s\n--- full trace ---\n%s", w.DebugDump(ctx), w.Trace.FormatLast(0))
					}
					t.Fatalf(
						"regression seed failed (%s):\n%v\n\nRepro:\n  go test -v ./internal/sim -run TestFullStackSim -sim-seed=%d -sim-steps=%d -sim-verbose",
						tc.description, err, tc.seed, tc.steps,
					)
				}

			default:
				t.Fatalf("unknown regression kind %q for %s", tc.kind, fmt.Sprint(tc.seed))
			}
		})
	}
}

// TestFullWorldNextConversationWakeAndMemoryChaos verifies that FullWorld exercises
// memory mutations, rapid bursts, consolidation, and delayed NextConversationWake
// execution in LaneConversation using deterministic virtual time.
func TestFullWorldNextConversationWakeAndMemoryChaos(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	// 1. Verify weighted chaos coverage across a small batch of FullWorld seeds.
	var memMut, bursts, cons int
	for _, seed := range []int64{17, 104, 188} {
		w := sim.NewFullWorld(seed, fullConfigForSeed(seed))
		if err := w.RunFullSimulation(ctx, 120); err != nil {
			t.Fatalf("seed=%d RunFullSimulation: %v", seed, err)
		}
		memMut += w.Stats.MemoryMutations
		bursts += w.Stats.RapidBursts
		cons += w.Stats.ConsolidateCalls
	}
	if memMut == 0 || bursts == 0 || cons == 0 {
		t.Fatalf("expected non-zero memory/burst/consolidate chaos coverage, got memMut=%d bursts=%d cons=%d", memMut, bursts, cons)
	}

	// 2. Directly verify NextConversationWake (+2m IdleExtractDelay with zero premature idle compaction)
	// arm -> virtual clock jump -> claim -> extract -> park, followed by SoftCompactUserTurns compaction.
	w := sim.NewFullWorld(777, sim.FullWorldConfig{
		NumUsers:      1,
		DisableFaults: true,
	})
	uw := w.Users[0]

	// Complete 8 user turns (< SoftCompactUserTurns=16): turns 3 & 6 extract immediately via
	// ExtractTurnThreshold=3, turns 7..8 schedule +2m IdleExtractDelay, and ZERO compaction occurs.
	for i := range 8 {
		if _, err := uw.Outer.AcceptInput(ctx, fmt.Sprintf("idle wake turn %d remember project_falcon_%d", i+1, i+1)); err != nil {
			t.Fatalf("AcceptInput %d: %v", i, err)
		}
		if err := uw.Outer.ProcessWorker(ctx, 0); err != nil {
			t.Fatalf("ProcessWorker %d: %v", i, err)
		}
		w.Clock.Advance(5 * time.Second)
	}

	sumBeforeIdle, err := uw.Store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after 8 turns: %v", err)
	}
	if !sumBeforeIdle.Through.IsZero() || !sumBeforeIdle.EffectiveFlushedThrough().IsZero() {
		t.Fatalf("expected zero compaction for 8 user turns (< SoftCompactUserTurns=%d), got through=%v flushed=%v",
			state.SoftCompactUserTurns, sumBeforeIdle.Through, sumBeforeIdle.EffectiveFlushedThrough())
	}
	if sumBeforeIdle.EffectiveExtractedThrough().IsZero() {
		t.Fatalf("expected ExtractTurnThreshold=%d to have already extracted turns 1..6", state.ExtractTurnThreshold)
	}

	// Ensure LaneConversation wake is claimed and re-armed via NextConversationWake (+2m IdleExtractDelay).
	if err := w.Backend.WakeUser(ctx, uw.User.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser: %v", err)
	}
	owner1 := "sim-pool-1"
	claim1, ok, err := w.Backend.ClaimUserWake(ctx, docstore.LaneConversation, owner1, state.ConversationLease)
	if err != nil || !ok || claim1.UserID != uw.User.ID {
		t.Fatalf("ClaimUserWake #1: ok=%v claim=%+v err=%v", ok, claim1, err)
	}
	extractWakeAt, err := uw.Outer.Workers[0].NextConversationWake(ctx)
	if err != nil || extractWakeAt.IsZero() || !extractWakeAt.After(w.Clock.Now()) {
		t.Fatalf("expected future delayed NextConversationWake (+2m IdleExtractDelay), got wakeAt=%v now=%v err=%v", extractWakeAt, w.Clock.Now(), err)
	}
	if err := w.Backend.ReleaseUserWake(ctx, uw.User.ID, docstore.LaneConversation, owner1, claim1.WakeSeq, extractWakeAt); err != nil {
		t.Fatalf("ReleaseUserWake delayed: %v", err)
	}

	// Before extractWakeAt, ClaimUserWake must NOT claim the delayed wake.
	if _, claimedEarly, err := w.Backend.ClaimUserWake(ctx, docstore.LaneConversation, "sim-pool-early", state.ConversationLease); err != nil || claimedEarly {
		t.Fatalf("expected delayed LaneConversation wake not claimable before wakeAt, claimedEarly=%v err=%v", claimedEarly, err)
	}

	// Advance virtual clock past extractWakeAt (+2m IdleExtractDelay) and verify turns 7..8 extract
	// WITHOUT compacting (Through/FlushedThrough remain zero) and LaneConversation parks cleanly.
	w.Clock.Advance(extractWakeAt.Sub(w.Clock.Now()) + time.Second)
	owner2 := "sim-pool-2"
	claim2, ok, err := w.Backend.ClaimUserWake(ctx, docstore.LaneConversation, owner2, state.ConversationLease)
	if err != nil || !ok || claim2.UserID != uw.User.ID {
		t.Fatalf("ClaimUserWake #2 after +2m: ok=%v claim=%+v err=%v", ok, claim2, err)
	}
	if err := uw.Outer.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker idle extract: %v", err)
	}
	sumAfterExtract, err := uw.Store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after idle extract: %v", err)
	}
	if !sumAfterExtract.Through.IsZero() || !sumAfterExtract.EffectiveFlushedThrough().IsZero() {
		t.Fatalf("idle extract must never compact 8-turn thread: through=%v flushed=%v",
			sumAfterExtract.Through, sumAfterExtract.EffectiveFlushedThrough())
	}
	if !sumAfterExtract.EffectiveExtractedThrough().After(sumBeforeIdle.EffectiveExtractedThrough()) {
		t.Fatalf("expected EffectiveExtractedThrough to advance after +2m idle extract: before=%v after=%v",
			sumBeforeIdle.EffectiveExtractedThrough(), sumAfterExtract.EffectiveExtractedThrough())
	}
	nextWake, err := uw.Outer.Workers[0].NextConversationWake(ctx)
	if err != nil || !nextWake.IsZero() {
		t.Fatalf("expected NextConversationWake to be zero after idle extract completed, got %v err=%v", nextWake, err)
	}
	if parked, err := w.Backend.ParkUserWake(ctx, uw.User.ID, docstore.LaneConversation, owner2, claim2.WakeSeq); err != nil || !parked {
		t.Fatalf("expected ParkUserWake after idle extract, parked=%v err=%v", parked, err)
	}

	// Drive 8 more user turns (reaching SoftCompactUserTurns=16) and verify compaction triggers,
	// keeping CompactKeep (14) turns verbatim with FlushedThrough == Through > 0.
	for i := 8; i < state.SoftCompactUserTurns; i++ {
		if _, err := uw.Outer.AcceptInput(ctx, fmt.Sprintf("buffered compact turn %d remember project_falcon_%d", i+1, i+1)); err != nil {
			t.Fatalf("AcceptInput %d: %v", i, err)
		}
		if err := uw.Outer.ProcessWorker(ctx, 0); err != nil {
			t.Fatalf("ProcessWorker %d: %v", i, err)
		}
		w.Clock.Advance(5 * time.Second)
	}
	sumCompacted, err := uw.Store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after SoftCompactUserTurns: %v", err)
	}
	if sumCompacted.EffectiveFlushedThrough().IsZero() || !sumCompacted.EffectiveFlushedThrough().Equal(sumCompacted.Through) {
		t.Fatalf("expected FlushedThrough == Through > 0 at SoftCompactUserTurns=%d, got flushed=%v through=%v",
			state.SoftCompactUserTurns, sumCompacted.EffectiveFlushedThrough(), sumCompacted.Through)
	}

	if err := w.DrainToQuiescence(ctx); err != nil {
		t.Fatalf("DrainToQuiescence: %v", err)
	}
}

// TestFullWorldShortSessionIdleExtractAndInnerRecall pins a deterministic E2E
// FullWorld scenario exercising 1..5 turn short-session +20m idle extraction
// (keeping all verbatim turns uncompacted) followed by a +10h overnight gap and
// an inner task calling memory_search to recall the extracted memory.
func TestFullWorldShortSessionIdleExtractAndInnerRecall(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	const pinnedSeed = 778
	d1 := runShortSessionIdleExtractAndRecall(t, ctx, pinnedSeed)
	d2 := runShortSessionIdleExtractAndRecall(t, ctx, pinnedSeed)
	if d1 != d2 {
		t.Fatalf("non-deterministic digest for seed=%d: run1=%s run2=%s", pinnedSeed, d1, d2)
	}
}

func runShortSessionIdleExtractAndRecall(t *testing.T, ctx context.Context, seed int64) string {
	t.Helper()

	w := sim.NewFullWorld(seed, sim.FullWorldConfig{
		NumUsers:      1,
		DisableFaults: true,
	})
	uw := w.Users[0]

	// 1. Short 2-turn session (< ExtractTurnThreshold=3, so extraction defers to +2m IdleExtractDelay
	// and zero compaction occurs).
	const token = "aurora_beacon_942"
	prompts := []string{
		"remember preference " + token + " for quarterly engineering reports",
		"project " + token + " stores durable facts in postgres",
	}
	for i, msg := range prompts {
		if _, err := uw.Outer.AcceptInput(ctx, msg); err != nil {
			t.Fatalf("AcceptInput %d: %v", i, err)
		}
		if err := uw.Outer.ProcessWorker(ctx, 0); err != nil {
			t.Fatalf("ProcessWorker %d: %v", i, err)
		}
		w.Clock.Advance(5 * time.Second)
	}

	turns, err := uw.Store.ConversationTurnsAfter(ctx, time.Time{})
	if err != nil {
		t.Fatalf("ConversationTurnsAfter: %v", err)
	}
	var newestTurnAt time.Time
	for _, tr := range turns {
		if tr.Status == "completed" {
			newestTurnAt = tr.StartedAt
			break
		}
	}
	if newestTurnAt.IsZero() {
		t.Fatalf("expected completed turns after 3-turn short session")
	}

	// NextConversationWake must schedule +20m IdleExtractDelay (not +2m).
	wakeAt, err := uw.Outer.Workers[0].NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake: %v", err)
	}
	wantWake := newestTurnAt.Add(state.IdleExtractDelay)
	if !wakeAt.Equal(wantWake) {
		t.Fatalf("NextConversationWake = %v, want %v (newestTurnAt + %s)", wakeAt, wantWake, state.IdleExtractDelay)
	}

	// Arm LaneConversation with the +20m delayed wake.
	if err := w.Backend.WakeUser(ctx, uw.User.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser: %v", err)
	}
	claim1, ok, err := w.Backend.ClaimUserWake(ctx, docstore.LaneConversation, "sim-short-1", state.ConversationLease)
	if err != nil || !ok {
		t.Fatalf("ClaimUserWake #1: ok=%v err=%v", ok, err)
	}
	if err := w.Backend.ReleaseUserWake(ctx, uw.User.ID, docstore.LaneConversation, "sim-short-1", claim1.WakeSeq, wakeAt); err != nil {
		t.Fatalf("ReleaseUserWake: %v", err)
	}

	// 2. Advance half of IdleExtractDelay: must NOT claim or extract early.
	w.Clock.Advance(state.IdleExtractDelay / 2)
	if _, claimedEarly, err := w.Backend.ClaimUserWake(ctx, docstore.LaneConversation, "sim-short-early", state.ConversationLease); err != nil || claimedEarly {
		t.Fatalf("expected +%s wake not claimable early, claimedEarly=%v err=%v", state.IdleExtractDelay, claimedEarly, err)
	}
	if err := uw.Outer.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker before IdleExtractDelay: %v", err)
	}
	sumEarly, err := uw.Store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary before IdleExtractDelay: %v", err)
	}
	if !sumEarly.EffectiveExtractedThrough().IsZero() || !sumEarly.Through.IsZero() || !sumEarly.FlushedThrough.IsZero() {
		t.Fatalf("expected zero watermarks before IdleExtractDelay, got extracted=%v through=%v flushed=%v",
			sumEarly.EffectiveExtractedThrough(), sumEarly.Through, sumEarly.FlushedThrough)
	}

	// 3. Advance past +20m: LaneConversation wakes, extracts Core + Daily Log memories, leaves Through/FlushedThrough zero, and parks.
	w.Clock.Advance(wakeAt.Sub(w.Clock.Now()) + time.Second)
	claim2, ok, err := w.Backend.ClaimUserWake(ctx, docstore.LaneConversation, "sim-short-2", state.ConversationLease)
	if err != nil || !ok || claim2.UserID != uw.User.ID {
		t.Fatalf("ClaimUserWake #2 at +20m: ok=%v claim=%+v err=%v", ok, claim2, err)
	}
	if err := uw.Outer.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker at +20m: %v", err)
	}
	sumExtracted, err := uw.Store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary at +20m: %v", err)
	}
	if !sumExtracted.EffectiveExtractedThrough().Equal(newestTurnAt) {
		t.Fatalf("expected EffectiveExtractedThrough=%v, got %v", newestTurnAt, sumExtracted.EffectiveExtractedThrough())
	}
	if !sumExtracted.Through.IsZero() || !sumExtracted.FlushedThrough.IsZero() {
		t.Fatalf("short-session idle extract must keep Through and FlushedThrough zero, got through=%v flushed=%v",
			sumExtracted.Through, sumExtracted.FlushedThrough)
	}
	nextWake, err := uw.Outer.Workers[0].NextConversationWake(ctx)
	if err != nil || !nextWake.IsZero() {
		t.Fatalf("expected zero NextConversationWake after short-session extraction, got %v err=%v", nextWake, err)
	}
	if parked, err := w.Backend.ParkUserWake(ctx, uw.User.ID, docstore.LaneConversation, "sim-short-2", claim2.WakeSeq); err != nil || !parked {
		t.Fatalf("expected ParkUserWake after short-session extraction, parked=%v err=%v", parked, err)
	}
	if err := w.CheckAllSafety(ctx); err != nil {
		t.Fatalf("CheckAllSafety after +20m idle extract: %v", err)
	}

	// 4. Advance +10h overnight gap, then run an inner task that calls memory_search for token and verifies recall.
	w.Clock.Advance(10 * time.Hour)

	recalledHits := 0
	var recalledText string
	var callSeq int
	w.Inner.Model.SetScript(func(_ context.Context, _ string, req agent.Request) (agent.Response, error, bool) {
		for _, m := range req.Messages {
			for _, p := range m.Parts {
				if p.ToolResult != nil && p.ToolResult.Name == "memory_search" {
					raw, _ := json.Marshal(p.ToolResult.Output)
					if results, ok := p.ToolResult.Output["results"].([]any); ok {
						recalledHits = len(results)
					}
					recalledText = string(raw)
					callSeq++
					doneCall := transcript.ToolCall{
						ID:   fmt.Sprintf("recall-complete-%d", callSeq),
						Name: "complete",
						Args: map[string]any{
							"answer":   "Recalled " + token + " from session-idle extracted memory.",
							"evidence": []any{},
							"unmet":    []any{},
						},
					}
					msg := transcript.Message{
						Role:  transcript.RoleModel,
						Parts: []transcript.Part{{ToolCall: &doneCall}},
					}
					return agent.Response{
						Message:   &msg,
						Calls:     []transcript.ToolCall{doneCall},
						Cost:      0.05,
						CostKnown: true,
					}, nil, true
				}
			}
		}

		callSeq++
		searchCall := transcript.ToolCall{
			ID:   fmt.Sprintf("recall-search-%d", callSeq),
			Name: "memory_search",
			Args: map[string]any{
				"query": token,
				"limit": "5",
			},
		}
		msg := transcript.Message{
			Role:  transcript.RoleModel,
			Parts: []transcript.Part{{ToolCall: &searchCall}},
		}
		return agent.Response{
			Message:   &msg,
			Calls:     []transcript.ToolCall{searchCall},
			Cost:      0.05,
			CostKnown: true,
		}, nil, true
	})
	defer w.Inner.Model.SetScript(nil)

	runID := w.IDs.Next()
	receipts, err := uw.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID:    w.IDs.Next(),
		RunID: runID,
		Kind:  state.CommandStart,
		Text:  "Search memory for " + token + " after the 10h gap.",
	}}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch start recall task: %v", err)
	}
	if err := w.Checker.RecordCommandReceipts(uw.User.ID, receipts); err != nil {
		t.Fatalf("RecordCommandReceipts: %v", err)
	}

	if err := w.DrainToQuiescence(ctx); err != nil {
		t.Fatalf("DrainToQuiescence after +10h inner recall: %v", err)
	}
	if recalledHits == 0 || !strings.Contains(recalledText, token) {
		t.Fatalf("expected inner task memory_search to recall %q across +20m/+10h gap, hits=%d output=%s", token, recalledHits, recalledText)
	}

	return w.Digest()
}
