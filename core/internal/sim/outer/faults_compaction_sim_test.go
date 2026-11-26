package outer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/conversation"
	"nori/internal/sim/env"
	"nori/internal/sim/memstore"
	"nori/internal/sim/outer"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func diffSnapshots(a, b memstore.BackendSnapshot) string {
	var out strings.Builder
	seenCols := map[string]bool{}
	for col := range a.Collections {
		seenCols[col] = true
	}
	for col := range b.Collections {
		seenCols[col] = true
	}
	for col := range seenCols {
		da := a.Collections[col]
		db := b.Collections[col]
		seenIDs := map[string]bool{}
		for id := range da {
			seenIDs[id] = true
		}
		for id := range db {
			seenIDs[id] = true
		}
		for id := range seenIDs {
			fa, oka := da[id]
			fb, okb := db[id]
			if !oka {
				raw, _ := json.Marshal(fb)
				fmt.Fprintf(&out, "  ONLY IN B: %s/%s = %s\n", col, id, raw)
				continue
			}
			if !okb {
				raw, _ := json.Marshal(fa)
				fmt.Fprintf(&out, "  ONLY IN A: %s/%s = %s\n", col, id, raw)
				continue
			}
			if !reflect.DeepEqual(fa, fb) {
				ra, _ := json.Marshal(fa)
				rb, _ := json.Marshal(fb)
				fmt.Fprintf(&out, "  DIFF %s/%s:\n    A=%s\n    B=%s\n", col, id, ra, rb)
			}
		}
	}
	return out.String()
}

type outerFaultRunResult struct {
	backend             *memstore.Backend
	artifacts           *memstore.Artifacts
	faultsDuringProcess memstore.FaultStats
	processCalls        uint64
}

func runOuterWithStoreFaults(ctx context.Context, seed int64, steps int) (outerFaultRunResult, error) {
	rootRNG := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))
	derive := func() *rand.Rand {
		return rand.New(rand.NewPCG(rootRNG.Uint64(), rootRNG.Uint64()))
	}

	workloadRNG := derive()
	harnessRNG := derive()
	ids := env.NewIDGen(derive())
	clock := env.NewClock(env.DefaultBaseTime)
	trace := env.NewTrace(4096)

	storeCfg := memstore.FaultConfig{
		GetErrProb:        0.04,
		QueryErrProb:      0.04,
		ApplyErrProb:      0.04,
		TxConflictProb:    0.08,
		CommitThenErrProb: 0.04,
	}
	storeFaults := memstore.NewFaultInjector(storeCfg, derive())
	storeFaults.SetKeyedMode(true, uint64(seed)^0x517cc1b727220a95)
	backend := memstore.New(memstore.WithFaultInjector(storeFaults), memstore.WithClock(clock.Now))
	artifacts := memstore.NewArtifacts(memstore.WithArtifactsFaultInjector(storeFaults))

	u := users.User{
		ID:          ids.Next(),
		Email:       "outer-faults@example.com",
		DisplayName: "Outer Faults User",
		TimeZone:    "UTC",
	}
	db := records.NewShared(backend, u, 1.0)
	db.Now = clock.Now
	store := state.Compose(db, artifacts)

	h := outer.NewHarness(outer.Config{
		UserID:              u.ID,
		Store:               store,
		StoreFaults:         storeFaults,
		Clock:               clock,
		IDs:                 ids,
		RNG:                 harnessRNG,
		Trace:               trace,
		NumWorkers:          3,
		ReplyFaults:         outer.DefaultReplyFaultConfig(),
		ClassifierFaultProb: 0.12,
		SummaryFaultProb:    0.20,
	})

	var res outerFaultRunResult
	res.backend = backend
	res.artifacts = artifacts
	currentStep := 0

	runWorker := func(workerIdx int) error {
		res.processCalls++
		prevEpoch := storeFaults.KeyedEpoch()
		epoch := (uint64(currentStep+1) << 32) | res.processCalls
		storeFaults.SetKeyedEpoch(epoch)
		defer storeFaults.SetKeyedEpoch(prevEpoch)
		before := storeFaults.Stats()
		err := h.ProcessWorker(ctx, workerIdx)
		after := storeFaults.Stats()
		res.faultsDuringProcess.GetErrors += after.GetErrors - before.GetErrors
		res.faultsDuringProcess.QueryErrors += after.QueryErrors - before.QueryErrors
		res.faultsDuringProcess.ApplyErrors += after.ApplyErrors - before.ApplyErrors
		res.faultsDuringProcess.TxConflicts += after.TxConflicts - before.TxConflicts
		res.faultsDuringProcess.CommitThenErrors += after.CommitThenErrors - before.CommitThenErrors
		return err
	}

	hookDepth := 0
	h.SetOnBeforeRespond(func(ctx context.Context, ev outer.BeforeRespondEvent) error {
		if hookDepth > 0 {
			return nil
		}
		hookDepth++
		defer func() { hookDepth-- }()

		if ev.LeaseExpired && workloadRNG.IntN(100) < 75 {
			other := (ev.WorkerIdx + 1) % len(h.Workers)
			_ = runWorker(other)
			return nil
		}

		roll := workloadRNG.IntN(100)
		switch {
		case roll < 15:
			_, _ = h.AcceptInput(ctx, fmt.Sprintf("mid-fault-input step=%d call=%d", currentStep, ev.CallNum))
		case roll < 24:
			if c, _, err := store.ConversationStatus(ctx); err == nil && c.Active != "" {
				_ = h.Cancel(ctx, c.Active)
			}
		case roll < 36 && ev.SimulatedDelay >= 10*time.Second && !ev.LeaseExpired:
			h.TickLeaseWatcher(ctx, ev.WorkerIdx, false)
		}
		return nil
	})

	if seed%2 == 0 {
		storeFaults.SetKeyedEpoch(1)
		_ = h.EnsureIntroduction(ctx)
	}

	for step := range steps {
		currentStep = step
		h.SetStep(step)
		storeFaults.SetKeyedEpoch((uint64(step+1) << 32) | 0x80000000)
		roll := workloadRNG.IntN(100)
		switch {
		case roll < 36:
			_, _ = h.AcceptInput(ctx, fmt.Sprintf("fault-step %d msg #%d", step, workloadRNG.IntN(1000)))
		case roll < 80:
			workerIdx := workloadRNG.IntN(len(h.Workers))
			err := runWorker(workerIdx)
			if err != nil &&
				!errors.Is(err, state.ErrConversationBusy) &&
				!errors.Is(err, state.ErrConversationStale) &&
				!errors.Is(err, memstore.ErrInjectedFault) &&
				!errors.Is(err, memstore.ErrTxConflict) &&
				!errors.Is(err, context.Canceled) {
				return res, fmt.Errorf("step %d ProcessWorker(%d): %w", step, workerIdx, err)
			}
		case roll < 90:
			if workloadRNG.IntN(4) == 0 {
				clock.Advance(11 * time.Minute)
			} else {
				clock.Advance(time.Duration(1+workloadRNG.IntN(40)) * time.Second)
			}
		case roll < 96:
			if c, _, err := store.ConversationStatus(ctx); err == nil && c.Active != "" {
				_ = h.Cancel(ctx, c.Active)
			}
		default:
			_, _ = h.Session(ctx)
		}
		if err := h.CheckOuterInvariants(ctx); err != nil {
			return res, fmt.Errorf("step %d invariants: %w", step, err)
		}
	}

	clock.Advance(11 * time.Minute)
	if err := h.DrainConversation(ctx); err != nil {
		return res, fmt.Errorf("DrainConversation: %w", err)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		return res, fmt.Errorf("post-drain invariants: %w", err)
	}

	c, _, err := store.ConversationStatus(ctx)
	if err != nil {
		return res, fmt.Errorf("final ConversationStatus: %w", err)
	}
	routingPending, err := store.UserRoutingPending(ctx)
	if err != nil {
		return res, fmt.Errorf("final UserRoutingPending: %w", err)
	}
	if c.Pending() || c.Active != "" || routingPending {
		return res, fmt.Errorf("conversation not cleanly drained: pending=%v active=%q routingPending=%v",
			c.Pending(), c.Active, routingPending)
	}

	return res, nil
}

func TestOuterWithStoreFaultsAndDeterminism(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	var totals memstore.FaultStats
	seeds := []int64{0, 1, 2, 7, 19, 42}
	for _, seed := range seeds {
		r1, err := runOuterWithStoreFaults(ctx, seed, 60)
		if err != nil {
			t.Fatalf("seed=%d run1 failed: %v", seed, err)
		}
		r2, err := runOuterWithStoreFaults(ctx, seed, 60)
		if err != nil {
			t.Fatalf("seed=%d run2 failed: %v", seed, err)
		}
		if r1.faultsDuringProcess.Total() == 0 {
			t.Fatalf("seed=%d expected non-zero store faults during Runner.Process, got %+v", seed, r1.faultsDuringProcess)
		}
		if r1.backend.Digest() != r2.backend.Digest() {
			t.Fatalf("seed=%d Backend.Digest diverged (f1=%+v f2=%+v):\n%s",
				seed, r1.faultsDuringProcess, r2.faultsDuringProcess,
				diffSnapshots(r1.backend.Snapshot(), r2.backend.Snapshot()))
		}
		if memstore.CombinedDigest(r1.backend, r1.artifacts) != memstore.CombinedDigest(r2.backend, r2.artifacts) {
			t.Fatalf("seed=%d CombinedDigest diverged", seed)
		}
		totals.GetErrors += r1.faultsDuringProcess.GetErrors
		totals.QueryErrors += r1.faultsDuringProcess.QueryErrors
		totals.ApplyErrors += r1.faultsDuringProcess.ApplyErrors
		totals.TxConflicts += r1.faultsDuringProcess.TxConflicts
		totals.CommitThenErrors += r1.faultsDuringProcess.CommitThenErrors
	}
	if totals.GetErrors == 0 || totals.QueryErrors == 0 || totals.ApplyErrors == 0 || totals.TxConflicts == 0 || totals.CommitThenErrors == 0 {
		t.Fatalf("expected all memstore fault categories to trigger during Runner.Process, got %+v", totals)
	}
}

type hookedSummaryModel struct {
	base     *outer.FakeSummaryModel
	onBefore func(ctx context.Context, req agent.Request) error
	called   int
}

func (m *hookedSummaryModel) Name() string {
	return m.base.Name()
}

func (m *hookedSummaryModel) Respond(ctx context.Context, req agent.Request) (agent.Response, error) {
	m.called++
	if m.onBefore != nil {
		if err := m.onBefore(ctx, req); err != nil {
			return agent.Response{}, err
		}
	}
	return m.base.Respond(ctx, req)
}

func newPlainWorkerRunner(
	rig *testRig,
	seed int64,
	summarizer conversation.SummaryModel,
) *conversation.Runner {
	rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x123456789abcdef))
	ids := env.NewIDGen(rand.New(rand.NewPCG(rng.Uint64(), rng.Uint64())))
	replyModel := outer.NewFakeReplyModel(outer.ReplyModelConfig{
		Name:          "sim-outer-model",
		RNG:           rng,
		Clock:         rig.clock,
		IDs:           env.NewIDGen(rand.New(rand.NewPCG(rng.Uint64(), rng.Uint64()))),
		DisableFaults: true,
	})
	replyModel.SetScripted(func(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error, bool) {
		return outer.BuildStructuredReply("Acknowledged: "+req.TelemetryInput, nil, state.Usage{}), nil, true
	})
	tasks := &conversation.Tasks{
		Store: rig.store,
		Now:   rig.clock.Now,
	}
	r := conversation.New(replyModel, nil, summarizer, tasks)
	r.UserFacts = "Compaction simulation user."
	r.Clock = conversation.Clock{Location: time.UTC, Now: rig.clock.Now}
	r.Now = rig.clock.Now
	r.NewID = ids.Next
	r.SyncWork = true
	r.After = func(d time.Duration) <-chan time.Time {
		if d > 0 {
			rig.clock.Advance(d)
		}
		ch := make(chan time.Time, 1)
		ch <- rig.clock.Now()
		return ch
	}
	r.NewTicker = func(time.Duration) (<-chan time.Time, func()) {
		ch := make(chan time.Time)
		return ch, func() {}
	}
	return r
}

func TestOuterConcurrentReplicaCompactionMerge(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()
	rig := newTestRig(202, rigOptions{numWorkers: 2, disableFaults: true})
	h := rig.harness

	worker1 := newPlainWorkerRunner(rig, 301, nil)

	initialTurns := state.RecentTurnLimit + 2
	for i := range initialTurns {
		rig.clock.Advance(time.Second)
		if _, err := h.AcceptInput(ctx, fmt.Sprintf("initial turn %d constraint: keep summaries exact", i+1)); err != nil {
			t.Fatalf("AcceptInput %d: %v", i, err)
		}
		if err := worker1.Process(ctx); err != nil {
			t.Fatalf("worker1.Process %d: %v", i, err)
		}
	}

	sumBefore, err := rig.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary before: %v", err)
	}
	if sumBefore.Through.IsZero() || sumBefore.Turns == 0 || sumBefore.Model != "extractive" {
		t.Fatalf("expected extractive hot-window summary before flush, got %+v", sumBefore)
	}
	if !sumBefore.EffectiveFlushedThrough().IsZero() || len(sumBefore.Excerpts) == 0 {
		t.Fatalf("expected FlushedThrough=0 and staged Excerpts before flush, got flushed=%s excerpts=%d",
			sumBefore.EffectiveFlushedThrough(), len(sumBefore.Excerpts))
	}

	baseSummarizer := outer.NewFakeSummaryModel(
		"sim-summary-model",
		rand.New(rand.NewPCG(401, 402)),
		rig.clock,
		env.NewIDGenFromSeed(403),
		0,
	)
	var midSummary state.ConversationSummary
	hookRan := false
	hooked := &hookedSummaryModel{
		base: baseSummarizer,
		onBefore: func(hookCtx context.Context, req agent.Request) error {
			if hookRan {
				return nil
			}
			if len(req.Tools) == 0 || req.Tools[0].Name != "flush_and_compact" {
				return nil
			}
			hookRan = true
			concurrentTurns := state.RecentTurnLimit - state.CompactKeep + 2
			for i := range concurrentTurns {
				rig.clock.Advance(time.Second)
				if _, err := h.AcceptInput(hookCtx, fmt.Sprintf("concurrent turn %d preference: prefer terse updates", i+1)); err != nil {
					return err
				}
				if err := worker1.Process(hookCtx); err != nil {
					return err
				}
			}
			var err error
			midSummary, err = rig.store.ConversationSummary(hookCtx)
			return err
		},
	}

	worker0 := newPlainWorkerRunner(rig, 300, hooked)
	if err := worker0.Process(ctx); err != nil {
		t.Fatalf("worker0.Process: %v", err)
	}

	if !hookRan {
		t.Fatal("expected concurrent worker1 indexing hook to run while worker0 flushed")
	}
	if !midSummary.Through.After(sumBefore.Through) || midSummary.Turns <= sumBefore.Turns {
		t.Fatalf("expected worker1 to advance summary during worker0 flush: before=(%s,%d) mid=(%s,%d)",
			sumBefore.Through, sumBefore.Turns, midSummary.Through, midSummary.Turns)
	}

	finalSummary, err := rig.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary final: %v", err)
	}
	if finalSummary.Model != hooked.Name() {
		t.Fatalf("expected merged semantic summary model %q, got %q", hooked.Name(), finalSummary.Model)
	}
	if len(finalSummary.Items) == 0 || strings.TrimSpace(finalSummary.Gist) == "" {
		t.Fatalf("expected semantic flush items and gist to survive rebase merge, got items=%d gist=%q",
			len(finalSummary.Items), finalSummary.Gist)
	}
	if !finalSummary.Through.Equal(midSummary.Through) {
		t.Fatalf("summary Through regressed or diverged: expected %s got %s", midSummary.Through, finalSummary.Through)
	}
	if finalSummary.Turns != midSummary.Turns {
		t.Fatalf("summary Turns regressed or diverged: expected %d got %d", midSummary.Turns, finalSummary.Turns)
	}
	flushed := finalSummary.EffectiveFlushedThrough()
	if flushed.Before(sumBefore.Through) || !flushed.Before(midSummary.Through) {
		t.Fatalf("expected FlushedThrough in [%s, %s) after rebase merge, got %s",
			sumBefore.Through, midSummary.Through, flushed)
	}
	if len(finalSummary.Excerpts) == 0 {
		t.Fatalf("expected newer unflushed excerpts in (FlushedThrough, Through] to be preserved")
	}
	for _, ex := range finalSummary.Excerpts {
		if !ex.TurnStartedAt.After(flushed) {
			t.Fatalf("expected retired excerpt %+v (<= %s) to be pruned", ex, flushed)
		}
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants after merge: %v", err)
	}

	for range 4 {
		if err := worker0.Process(ctx); err != nil {
			t.Fatalf("idle catch-up worker0.Process: %v", err)
		}
	}
	caughtUp, err := rig.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary caughtUp: %v", err)
	}
	if caughtUp.EffectiveFlushedThrough().Before(midSummary.Through) {
		t.Fatalf("expected subsequent idle flush to catch up FlushedThrough to at least %s, got %s",
			midSummary.Through, caughtUp.EffectiveFlushedThrough())
	}
	if len(caughtUp.Excerpts) != 0 {
		t.Fatalf("expected retired excerpts cleared once FlushedThrough == Through, got %d", len(caughtUp.Excerpts))
	}
	if len(caughtUp.Items) < len(finalSummary.Items) {
		t.Fatalf("expected caught-up flush to retain prior items: before=%d after=%d",
			len(finalSummary.Items), len(caughtUp.Items))
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants after catch-up: %v", err)
	}
}
