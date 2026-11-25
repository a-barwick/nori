package outer_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"nori/internal/conversation"
	"nori/internal/sim/env"
	"nori/internal/sim/memstore"
	"nori/internal/sim/outer"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

var (
	outerSeed    = flag.Int64("outer-seed", -1, "single seed for TestOuterRandomizedSim (-1 runs 0..outer-seeds-1)")
	outerSeeds   = flag.Int("outer-seeds", 24, "number of seeds for TestOuterRandomizedSim when -outer-seed < 0")
	outerSteps   = flag.Int("outer-steps", 60, "number of steps per seed for TestOuterRandomizedSim")
	outerVerbose = flag.Bool("outer-verbose", false, "emit verbose internal logs during outer sim tests")
)

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

func silenceStdLog(t *testing.T) {
	t.Helper()
	if *outerVerbose {
		return
	}
	orig := log.Writer()
	log.SetOutput(io.Discard)
	t.Cleanup(func() {
		log.SetOutput(orig)
	})
}

type testRig struct {
	seed        int64
	rng         *rand.Rand
	clock       *env.Clock
	ids         *env.IDGen
	trace       *env.Trace
	backend     *memstore.Backend
	artifacts   *memstore.Artifacts
	storeFaults *memstore.FaultInjector
	user        users.User
	db          *records.Database
	store       state.ConversationStore
	harness     *outer.Harness
}

type rigOptions struct {
	numWorkers          int
	disableFaults       bool
	replyFaults         outer.ReplyFaultConfig
	classifierFaultProb float64
	summaryFaultProb    float64
}

func newTestRig(seed int64, opts rigOptions) *testRig {
	rootRNG := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))
	derive := func() *rand.Rand {
		return rand.New(rand.NewPCG(rootRNG.Uint64(), rootRNG.Uint64()))
	}

	workloadRNG := derive()
	harnessRNG := derive()
	ids := env.NewIDGen(derive())
	clock := env.NewClock(env.DefaultBaseTime)
	trace := env.NewTrace(4096)

	storeFaults := memstore.NewFaultInjector(memstore.FaultConfig{}, derive())
	storeFaults.SetEnabled(false)
	backend := memstore.New(memstore.WithFaultInjector(storeFaults), memstore.WithClock(clock.Now))
	artifacts := memstore.NewArtifacts(memstore.WithArtifactsFaultInjector(storeFaults))

	u := users.User{
		ID:          ids.Next(),
		Email:       "outer-sim@example.com",
		DisplayName: "Outer Sim User",
		TimeZone:    "UTC",
	}
	db := records.NewShared(backend, u, 1.0)
	db.Now = clock.Now
	store := state.Compose(db, artifacts)

	numWorkers := opts.numWorkers
	if numWorkers <= 0 {
		numWorkers = 3
	}
	h := outer.NewHarness(outer.Config{
		UserID:              u.ID,
		Store:               store,
		StoreFaults:         storeFaults,
		Clock:               clock,
		IDs:                 ids,
		RNG:                 harnessRNG,
		Trace:               trace,
		NumWorkers:          numWorkers,
		ReplyFaults:         opts.replyFaults,
		ClassifierFaultProb: opts.classifierFaultProb,
		SummaryFaultProb:    opts.summaryFaultProb,
		DisableFaults:       opts.disableFaults,
	})

	return &testRig{
		seed:        seed,
		rng:         workloadRNG,
		clock:       clock,
		ids:         ids,
		trace:       trace,
		backend:     backend,
		artifacts:   artifacts,
		storeFaults: storeFaults,
		user:        u,
		db:          db,
		store:       store,
		harness:     h,
	}
}

func TestOuterLeaseExpiryTakeoverAndStaleFenceRejection(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()
	rig := newTestRig(101, rigOptions{numWorkers: 2, disableFaults: true})
	h := rig.harness

	if _, err := h.AcceptInput(ctx, "Start a deployment check"); err != nil {
		t.Fatalf("AcceptInput: %v", err)
	}

	// Worker 0 proposes a dispatch, but pauses past the 30s conversation lease
	// while Worker 1 steals the expired lease and finishes the turn.
	takeoverRan := false
	var worker1Err error
	h.ReplyModels[0].SetScripted(func(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error, bool) {
		call := outer.DispatchCall("action_0", "stale worker 0 dispatch", "", "")
		return outer.BuildStructuredReply("Stale worker 0 reply", []conversation.ToolCall{call}, state.Usage{}), nil, true
	})
	h.ReplyModels[0].SetOnBeforeRespond(func(ctx context.Context, ev outer.BeforeRespondEvent) error {
		if takeoverRan {
			return nil
		}
		takeoverRan = true
		rig.clock.Advance(state.ConversationLease + 5*time.Second)
		worker1Err = h.ProcessWorker(ctx, 1)
		return nil
	})

	worker0Err := h.ProcessWorker(ctx, 0)
	if !takeoverRan {
		t.Fatal("expected competing worker 1 takeover hook to run")
	}
	if worker1Err != nil {
		t.Fatalf("expected worker 1 takeover to succeed cleanly, got %v", worker1Err)
	}
	if !errors.Is(worker0Err, state.ErrConversationBusy) {
		t.Fatalf("expected stale worker 0 to be rejected with ErrConversationBusy, got %v", worker0Err)
	}

	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants: %v", err)
	}

	// Stale worker 0's dispatch must NOT have been admitted.
	open, err := rig.store.OpenTasks(ctx)
	if err != nil {
		t.Fatalf("OpenTasks: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("stale worker 0 command leaked into store: open=%+v", open)
	}

	// Exactly one assistant message should exist for the turn.
	sess, err := h.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	assistantCount := 0
	for _, m := range sess.Messages {
		if m.Role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Fatalf("expected exactly 1 assistant message, got %d", assistantCount)
	}

	// Because worker 1 recovered an interrupted requestingReply turn as an error,
	// UserRoutingPending should be true until DrainConversation resolves it.
	pending, err := rig.store.UserRoutingPending(ctx)
	if err != nil {
		t.Fatalf("UserRoutingPending: %v", err)
	}
	if !pending {
		t.Fatal("expected UserRoutingPending=true after interrupted requestingReply takeover")
	}

	if err := h.DrainConversation(ctx); err != nil {
		t.Fatalf("DrainConversation: %v", err)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants after drain: %v", err)
	}
}

func TestOuterLeaseRenewalPreventsTakeover(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()
	rig := newTestRig(102, rigOptions{numWorkers: 2, disableFaults: true})
	h := rig.harness

	if _, err := h.AcceptInput(ctx, "Long running outer turn"); err != nil {
		t.Fatalf("AcceptInput: %v", err)
	}

	var competingErr error
	renewed := false
	h.ReplyModels[0].SetOnBeforeRespond(func(ctx context.Context, ev outer.BeforeRespondEvent) error {
		if ev.Request.CommandPlan || renewed {
			return nil
		}
		renewed = true
		// Advance past 10s lease-renew interval and explicitly tick watchLease.
		rig.clock.Advance(15 * time.Second)
		if !h.TickLeaseWatcher(ctx, 0, false) {
			return errors.New("expected active lease watcher on worker 0")
		}
		// Advance another 20s: 35s total (> original 30s TTL), but only 20s since renewal!
		rig.clock.Advance(20 * time.Second)
		competingErr = h.ProcessWorker(ctx, 1)
		return nil
	})

	if err := h.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("worker 0 should have kept lease via renewal, got %v", err)
	}
	if !renewed {
		t.Fatal("expected renewal hook to fire")
	}
	if !errors.Is(competingErr, state.ErrConversationBusy) {
		t.Fatalf("expected worker 1 to be blocked by renewed lease (ErrConversationBusy), got %v", competingErr)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants: %v", err)
	}
}

func TestOuterMidTurnInputRestartAndFreeze(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()
	rig := newTestRig(103, rigOptions{numWorkers: 2, disableFaults: true})
	h := rig.harness

	if _, err := h.AcceptInput(ctx, "message 1"); err != nil {
		t.Fatalf("AcceptInput 1: %v", err)
	}

	// Inject 4 successive mid-turn user inputs: first 3 trigger restarts (1..3),
	// 4th arrives after the turn freezes (Restarts == MaxTurnRestarts) and must
	// be processed as a second turn instead of restarting again.
	injected := 0
	h.ReplyModels[0].SetScripted(func(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error, bool) {
		return outer.BuildStructuredReply(fmt.Sprintf("plain reply %d", injected), nil, state.Usage{}), nil, true
	})
	h.ReplyModels[0].SetOnBeforeRespond(func(ctx context.Context, ev outer.BeforeRespondEvent) error {
		if injected >= 4 {
			return nil
		}
		injected++
		if _, err := h.AcceptInput(ctx, fmt.Sprintf("mid-turn message %d", injected+1)); err != nil {
			return err
		}
		// On the first injection, also test watchLease mid-flight cancellation!
		if injected == 1 {
			if !h.TickLeaseWatcher(ctx, 0, true) {
				return errors.New("expected watchLease to cancel stale callCtx")
			}
		}
		return nil
	})

	if err := h.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker: %v", err)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants: %v", err)
	}

	sess, err := h.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.Conversation.Pending || sess.Conversation.RoutingBlocked {
		t.Fatalf("expected drained conversation after ProcessWorker, got %+v", sess.Conversation)
	}

	var assistantMsgs []state.Message
	for _, m := range sess.Messages {
		if m.Role == "assistant" {
			assistantMsgs = append(assistantMsgs, m)
		}
	}
	// Turn 1 froze at 3 restarts covering messages 1..4; message 5 produced Turn 2.
	if len(assistantMsgs) != 2 {
		t.Fatalf("expected 2 assistant turns (frozen turn 1 + follow-up turn 2), got %d", len(assistantMsgs))
	}

	superseded := 0
	for _, tr := range sess.Traces {
		if tr.Status == "superseded" {
			superseded++
		}
	}
	if superseded < 3 {
		t.Fatalf("expected at least 3 superseded attempts across restarts, got %d", superseded)
	}
}

func TestOuterProviderErrorAndUnroutedReplay(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()
	rig := newTestRig(104, rigOptions{
		numWorkers:    2,
		disableFaults: false,
		replyFaults: outer.ReplyFaultConfig{
			ErrorProb: 1.0,
		},
		classifierFaultProb: -1,
		summaryFaultProb:    -1,
	})
	h := rig.harness

	if _, err := h.AcceptInput(ctx, "Please check my calendar"); err != nil {
		t.Fatalf("AcceptInput: %v", err)
	}
	if err := h.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker on provider error should still publish error reply: %v", err)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants: %v", err)
	}

	c, _, err := rig.store.ConversationStatus(ctx)
	if err != nil {
		t.Fatalf("ConversationStatus: %v", err)
	}
	if c.Pending() {
		t.Fatalf("expected !Pending after error turn published, got %+v", c)
	}
	if !c.RoutingPending() {
		t.Fatalf("expected RoutingPending=true after error turn, got %+v", c)
	}

	// DrainConversation disables faults and resolves the unrouted input cleanly.
	if err := h.DrainConversation(ctx); err != nil {
		t.Fatalf("DrainConversation: %v", err)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants after drain: %v", err)
	}
	routingPending, err := rig.store.UserRoutingPending(ctx)
	if err != nil {
		t.Fatalf("UserRoutingPending: %v", err)
	}
	if routingPending {
		t.Fatal("expected UserRoutingPending=false after DrainConversation")
	}
}

func TestOuterCompactionFailurePauseAndRecovery(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()
	rig := newTestRig(105, rigOptions{
		numWorkers:          2,
		disableFaults:       false,
		replyFaults:         outer.ReplyFaultConfig{StreamChunks: 3},
		classifierFaultProb: -1,
		summaryFaultProb:    1.0,
	})
	h := rig.harness
	// Force plain replies so every turn completes cleanly in one attempt.
	h.ReplyModels[0].SetScripted(func(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error, bool) {
		return outer.BuildStructuredReply("Acknowledged: "+req.TelemetryInput, nil, state.Usage{}), nil, true
	})

	// Drive SoftCompactUserTurns (16) user turns so coupled flush_and_compact triggers on turn 16.
	// Keep summary faults disabled during earlier 3-turn decoupled extractions so faults target flush_and_compact.
	h.SummaryModels[0].SetFaultsEnabled(false)
	for i := range state.SoftCompactUserTurns {
		if i == state.SoftCompactUserTurns-1 {
			h.SummaryModels[0].SetFaultsEnabled(true)
		}
		rig.clock.Advance(time.Second)
		if _, err := h.AcceptInput(ctx, fmt.Sprintf("turn %d preference: always keep notes concise", i+1)); err != nil {
			t.Fatalf("AcceptInput %d: %v", i, err)
		}
		if err := h.ProcessWorker(ctx, 0); err != nil {
			t.Fatalf("ProcessWorker %d: %v", i, err)
		}
	}

	statsAfterFail1 := h.SummaryModels[0].Stats()
	if statsAfterFail1.Calls == 0 || statsAfterFail1.Errors == 0 {
		t.Fatalf("expected summary model to be called and fail at SoftCompactTurns, stats=%+v", statsAfterFail1)
	}

	// Coupled flush-before-compact invariant: FlushedThrough MUST NOT advance on failure #1.
	sum, err := rig.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after fail 1: %v", err)
	}
	if !sum.EffectiveFlushedThrough().IsZero() || sum.FlushFailures != 1 {
		t.Fatalf("expected FlushedThrough=0 and FlushFailures=1 after first failure, got flushed=%s failures=%d",
			sum.EffectiveFlushedThrough(), sum.FlushFailures)
	}

	// Within the 2-minute first-failure backoff, idle ProcessWorker must NOT call
	// the compaction model again.
	rig.clock.Advance(time.Minute)
	if err := h.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker during 2m backoff: %v", err)
	}
	if got := h.SummaryModels[0].Stats().Calls; got != statsAfterFail1.Calls {
		t.Fatalf("expected 2m backoff to skip compaction call, before=%d after=%d", statsAfterFail1.Calls, got)
	}

	// Advance past the 2m backoff -> failure #2 triggers 5m backoff while FlushedThrough stays 0.
	rig.clock.Advance(2 * time.Minute)
	if err := h.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker fail 2: %v", err)
	}
	statsAfterFail2 := h.SummaryModels[0].Stats()
	if statsAfterFail2.Errors != statsAfterFail1.Errors+1 {
		t.Fatalf("expected second failure after 2m backoff, before=%+v after=%+v", statsAfterFail1, statsAfterFail2)
	}
	sum, err = rig.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after fail 2: %v", err)
	}
	if !sum.EffectiveFlushedThrough().IsZero() || sum.FlushFailures != 2 {
		t.Fatalf("expected FlushedThrough=0 and FlushFailures=2 after second failure, got flushed=%s failures=%d",
			sum.EffectiveFlushedThrough(), sum.FlushFailures)
	}

	// Within the 5m second-failure backoff, idle ProcessWorker must NOT call the model again.
	rig.clock.Advance(3 * time.Minute)
	if err := h.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker during 5m backoff: %v", err)
	}
	if got := h.SummaryModels[0].Stats().Calls; got != statsAfterFail2.Calls {
		t.Fatalf("expected 5m backoff to skip compaction call, before=%d after=%d", statsAfterFail2.Calls, got)
	}

	// Advance past the 5m backoff, disable summary faults, and recover cleanly.
	rig.clock.Advance(3 * time.Minute)
	h.SummaryModels[0].SetFaultsEnabled(false)
	if err := h.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker recovery: %v", err)
	}

	statsAfterRecover := h.SummaryModels[0].Stats()
	if statsAfterRecover.Succeeded == 0 {
		t.Fatalf("expected compaction model to succeed after backoff expiry, stats=%+v", statsAfterRecover)
	}
	sum, err = rig.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after recovery: %v", err)
	}
	if sum.Model != h.SummaryModels[0].Name() || sum.EffectiveFlushedThrough().IsZero() || sum.FlushFailures != 0 || len(sum.Items) == 0 || strings.TrimSpace(sum.Gist) == "" {
		t.Fatalf("expected coupled flush summary after recovery, got model=%q flushed=%s failures=%d items=%d gist=%q",
			sum.Model, sum.EffectiveFlushedThrough(), sum.FlushFailures, len(sum.Items), sum.Gist)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants: %v", err)
	}
}

func TestOuterCancellation(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()
	rig := newTestRig(106, rigOptions{numWorkers: 2, disableFaults: true})
	h := rig.harness

	// 1. Pre-acquire cancellation by head queued input ID.
	id1, err := h.AcceptInput(ctx, "Cancel me before acquire")
	if err != nil {
		t.Fatalf("AcceptInput 1: %v", err)
	}
	if err := h.Cancel(ctx, id1); err != nil {
		t.Fatalf("Cancel pre-acquire: %v", err)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants after pre-acquire cancel: %v", err)
	}

	// 2. Mid-turn cancellation while Worker 0 is inside Respond.
	if _, err := h.AcceptInput(ctx, "Cancel me mid-flight"); err != nil {
		t.Fatalf("AcceptInput 2: %v", err)
	}
	cancelledMidFlight := false
	h.ReplyModels[0].SetOnBeforeRespond(func(ctx context.Context, ev outer.BeforeRespondEvent) error {
		if cancelledMidFlight {
			return nil
		}
		cancelledMidFlight = true
		c, _, err := rig.store.ConversationStatus(ctx)
		if err != nil {
			return err
		}
		if c.Active == "" {
			return errors.New("expected active turn during OnBeforeRespond")
		}
		return h.Cancel(ctx, c.Active)
	})

	err = h.ProcessWorker(ctx, 0)
	if !cancelledMidFlight {
		t.Fatal("expected mid-flight cancel hook to run")
	}
	if !errors.Is(err, state.ErrConversationBusy) {
		t.Fatalf("expected cancelled worker 0 to fail with ErrConversationBusy, got %v", err)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants after mid-flight cancel: %v", err)
	}

	sess, err := h.Session(ctx)
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	cancelledMsgs := 0
	for _, m := range sess.Messages {
		if m.Role == "assistant" && m.Status == "cancelled" {
			cancelledMsgs++
		}
	}
	if cancelledMsgs != 2 {
		t.Fatalf("expected 2 cancelled assistant messages, got %d", cancelledMsgs)
	}
}

func TestOuterStreamingAndStreamCancellation(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()
	clock := env.NewClock(env.DefaultBaseTime)
	model := outer.NewFakeReplyModel(outer.ReplyModelConfig{
		RNG:           rand.New(rand.NewPCG(11, 22)),
		Clock:         clock,
		IDs:           env.NewIDGenFromSeed(11),
		DisableFaults: true,
		Faults:        outer.ReplyFaultConfig{StreamChunks: 4},
	})

	var chunks []string
	reply, err := model.Respond(ctx, conversation.ReplyRequest{
		NoDispatch: true,
		Stream: func(s string) error {
			chunks = append(chunks, s)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("streaming Respond: %v", err)
	}
	if len(chunks) != 4 {
		t.Fatalf("expected 4 cumulative chunks, got %d (%v)", len(chunks), chunks)
	}
	for i := 1; i < len(chunks); i++ {
		if !strings.HasPrefix(chunks[i], chunks[i-1]) || len(chunks[i]) < len(chunks[i-1]) {
			t.Fatalf("chunks not cumulative: %q -> %q", chunks[i-1], chunks[i])
		}
	}
	if chunks[len(chunks)-1] != reply.Text {
		t.Fatalf("final streamed chunk %q != reply.Text %q", chunks[len(chunks)-1], reply.Text)
	}

	// Stream cancellation stops provider output and surfaces the stream error.
	streamStop := errors.New("client disconnected")
	calls := 0
	_, err = model.Respond(ctx, conversation.ReplyRequest{
		NoDispatch: true,
		Stream: func(s string) error {
			calls++
			if calls == 2 {
				return streamStop
			}
			return nil
		},
	})
	if !errors.Is(err, streamStop) {
		t.Fatalf("expected stream cancellation error, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected streaming to stop immediately on chunk 2, got %d calls", calls)
	}
}

func TestOuterIntroductionAndWorkRequests(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()
	rig := newTestRig(107, rigOptions{numWorkers: 2, disableFaults: true})
	h := rig.harness

	// 1. Introduction turn (NoDispatch: true).
	if err := h.EnsureIntroduction(ctx); err != nil {
		t.Fatalf("EnsureIntroduction: %v", err)
	}
	if err := h.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker intro: %v", err)
	}

	// 2. Start a task via user turn, then publish a QuestionWork event so the
	// outer loop receives WorkRequests and settles it.
	h.ReplyModels[0].SetScripted(func(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error, bool) {
		if len(req.WorkRequests) > 0 {
			wr := req.WorkRequests[0]
			call := outer.DispatchCall("action_0", "Subtask for parent", wr.TaskID, wr.QuestionID)
			return outer.BuildStructuredReply("Started subtask for work request.", []conversation.ToolCall{call}, state.Usage{}), nil, true
		}
		if req.CommandPlan {
			call := outer.DispatchCall("action_0", "Parent task", "", "")
			return outer.BuildStructuredReply("Started parent task.", []conversation.ToolCall{call}, state.Usage{}), nil, true
		}
		call := outer.DispatchCall("action_0", "Parent task", "", "")
		return outer.BuildStructuredReply("Starting parent task.", []conversation.ToolCall{call}, state.Usage{}), nil, true
	})

	if _, err := h.AcceptInput(ctx, "Do a multi-step research task"); err != nil {
		t.Fatalf("AcceptInput: %v", err)
	}
	if err := h.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker user turn: %v", err)
	}

	open, err := rig.store.OpenTasks(ctx)
	if err != nil || len(open) != 1 {
		t.Fatalf("expected 1 open parent task, got %d (err=%v)", len(open), err)
	}
	parent := open[0]

	// Parent incorporates its start command, then publishes a QuestionWork request into the conversation inbox.
	if err := rig.store.Incorporate(ctx, state.IncorporateRequest{
		RunID:    parent.ID,
		Position: parent.Position,
		Revision: parent.Revision,
	}); err != nil {
		t.Fatalf("Incorporate parent: %v", err)
	}
	rig.clock.Advance(time.Second)
	err = rig.store.ProjectQuestion(ctx, parent.ID, parent.Revision, state.Question{
		ID:       rig.ids.Next(),
		Kind:     state.QuestionWork,
		Text:     "Need a helper subtask to fetch documents",
		Revision: parent.Revision,
	})
	if err != nil {
		t.Fatalf("ProjectQuestion QuestionWork: %v", err)
	}

	if err := h.ProcessWorker(ctx, 0); err != nil {
		t.Fatalf("ProcessWorker work request: %v", err)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		t.Fatalf("CheckOuterInvariants: %v", err)
	}

	open, err = rig.store.OpenTasks(ctx)
	if err != nil {
		t.Fatalf("OpenTasks after work request: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("expected parent + subtask (2 open tasks), got %d", len(open))
	}
}

type simSummary struct {
	digest         string
	messages       []state.Message
	traces         []conversation.Trace
	traceDigest    string
	finalClock     time.Time
	replyCalls     int
	replyErrors    int
	leaseTakeovers int
	midTurnInputs  int
	midTurnCancels int
	commands       int
	memories       int
	classifierErrs int
	summaryCalls   int
	extractCalls   int
	summaryErrs    int
	summarySuccess int
}

func runRandomizedOuterSim(ctx context.Context, seed int64, steps int) (simSummary, error) {
	rig := newTestRig(seed, rigOptions{
		numWorkers: 3,
		replyFaults: outer.ReplyFaultConfig{
			ErrorProb:           0.10,
			OverflowProb:        0.05,
			SlowLeaseRenewProb:  0.10,
			SlowLeaseExpireProb: 0.14,
			PlanMismatchProb:    0.10,
			StreamChunks:        3,
		},
		classifierFaultProb: 0.12,
		summaryFaultProb:    0.25,
	})
	h := rig.harness
	checker, targets := newMemoryInvariantChecker(rig)
	var out simSummary

	// Optional introduction on even seeds.
	if seed%2 == 0 {
		if err := h.EnsureIntroduction(ctx); err != nil {
			return out, fmt.Errorf("EnsureIntroduction: %w", err)
		}
	}

	// Guard re-entrancy depth so mid-flight competing worker hooks don't recurse infinitely.
	hookDepth := 0
	h.SetOnBeforeRespond(func(ctx context.Context, ev outer.BeforeRespondEvent) error {
		if hookDepth > 0 {
			return nil
		}
		hookDepth++
		defer func() { hookDepth-- }()

		// 1. If the simulated slow turn expired the conversation lease, let a
		// competing worker try to steal the lease mid-flight!
		if ev.LeaseExpired {
			other := (ev.WorkerIdx + 1) % len(h.Workers)
			if rig.rng.IntN(100) < 80 {
				out.leaseTakeovers++
				_ = h.ProcessWorker(ctx, other)
				return nil
			}
		}

		// 2. Otherwise deterministically inject mid-flight contention / inputs / cancels.
		roll := rig.rng.IntN(100)
		switch {
		case roll < 14:
			// Competing worker attempts Process while lease is live (must see ErrConversationBusy).
			other := (ev.WorkerIdx + 1) % len(h.Workers)
			_ = h.ProcessWorker(ctx, other)
		case roll < 30:
			// Mid-turn user message triggering RestartConversation.
			out.midTurnInputs++
			_, _ = h.AcceptInput(ctx, fmt.Sprintf("mid-flight input depth=%d call=%d", hookDepth, ev.CallNum))
			if rig.rng.IntN(3) == 0 {
				if _, activeTurn, err := rig.store.ConversationStatus(ctx); err == nil && activeTurn != nil && !activeTurn.Frozen {
					h.TickLeaseWatcher(ctx, ev.WorkerIdx, true)
				}
			}
		case roll < 36:
			// Mid-turn cancellation of the active turn.
			if c, _, err := rig.store.ConversationStatus(ctx); err == nil && c.Active != "" {
				out.midTurnCancels++
				_ = h.Cancel(ctx, c.Active)
			}
		case roll < 44 && ev.SimulatedDelay >= 10*time.Second && !ev.LeaseExpired:
			// Explicitly exercise watchLease renewal when clock crossed 10s.
			h.TickLeaseWatcher(ctx, ev.WorkerIdx, false)
		}
		return nil
	})

	for step := range steps {
		h.SetStep(step)
		roll := rig.rng.IntN(100)
		switch {
		case roll < 34:
			// Accept a new user message.
			text := fmt.Sprintf("step %d request #%d", step, rig.rng.IntN(1000))
			if _, err := h.AcceptInput(ctx, text); err != nil {
				return out, fmt.Errorf("step %d AcceptInput: %w", step, err)
			}
		case roll < 76:
			// Run a randomly selected competing worker.
			workerIdx := rig.rng.IntN(len(h.Workers))
			err := h.ProcessWorker(ctx, workerIdx)
			if err != nil &&
				!errors.Is(err, state.ErrConversationBusy) &&
				!errors.Is(err, state.ErrConversationStale) {
				return out, fmt.Errorf("step %d ProcessWorker(%d): %w", step, workerIdx, err)
			}
		case roll < 84:
			// Advance virtual time (exercises lease expiry, 2m fast flush, 5m idle extract, and 10m compaction pause).
			switch rig.rng.IntN(4) {
			case 0:
				rig.clock.Advance(21 * time.Minute)
			case 1:
				rig.clock.Advance(3 * time.Minute)
			default:
				rig.clock.Advance(time.Duration(1+rig.rng.IntN(40)) * time.Second)
			}
		case roll < 90:
			// Idempotent replay or conflict check on a previously accepted input.
			accepted := h.AcceptedInputs()
			if len(accepted) > 0 {
				id := accepted[rig.rng.IntN(len(accepted))]
				if rig.rng.IntN(2) == 0 {
					// Conflict test with different text must return ErrConversationConflict.
					if err := h.AcceptInputWithID(ctx, id, "conflicting text payload"); err != nil && !errors.Is(err, state.ErrConversationConflict) {
						return out, fmt.Errorf("step %d conflict check: %w", step, err)
					}
				}
			}
		case roll < 96:
			// Cancel active turn or a queued head input if present.
			c, _, err := rig.store.ConversationStatus(ctx)
			if err != nil {
				return out, fmt.Errorf("step %d ConversationStatus: %w", step, err)
			}
			if c.Active != "" {
				_ = h.Cancel(ctx, c.Active)
			} else if accepted := h.AcceptedInputs(); len(accepted) > 0 {
				_ = h.Cancel(ctx, accepted[len(accepted)-1])
			}
		default:
			// Snapshot read check.
			if _, err := h.Session(ctx); err != nil {
				return out, fmt.Errorf("step %d Session: %w", step, err)
			}
		}

		if err := h.CheckOuterInvariants(ctx); err != nil {
			return out, fmt.Errorf("step %d invariant failure: %w", step, err)
		}
		if err := checker.CheckSafetyAt(ctx, targets, rig.clock.Now()); err != nil {
			return out, fmt.Errorf("step %d safety/memory invariant failure: %w", step, err)
		}
	}

	// Advance clock past any compaction pause so drain can also finish pending compaction cleanly.
	rig.clock.Advance(11 * time.Minute)
	if err := h.DrainConversation(ctx); err != nil {
		return out, fmt.Errorf("DrainConversation: %w", err)
	}
	if err := h.CheckOuterInvariants(ctx); err != nil {
		return out, fmt.Errorf("post-drain invariant failure: %w", err)
	}
	if err := checker.CheckSafetyAt(ctx, targets, rig.clock.Now()); err != nil {
		return out, fmt.Errorf("post-drain safety/memory invariant failure: %w", err)
	}

	c, _, err := rig.store.ConversationStatus(ctx)
	if err != nil {
		return out, fmt.Errorf("final ConversationStatus: %w", err)
	}
	routingPending, err := rig.store.UserRoutingPending(ctx)
	if err != nil {
		return out, fmt.Errorf("final UserRoutingPending: %w", err)
	}
	if c.Pending() || c.Active != "" || routingPending {
		return out, fmt.Errorf("conversation not cleanly drained: pending=%v active=%q routingPending=%v",
			c.Pending(), c.Active, routingPending)
	}

	sess, err := h.Session(ctx)
	if err != nil {
		return out, fmt.Errorf("final Session: %w", err)
	}
	allMsgs, err := readAllMessages(ctx, rig.store)
	if err != nil {
		return out, fmt.Errorf("readAllMessages: %w", err)
	}

	for i := range h.Workers {
		rs := h.ReplyModels[i].Stats()
		out.replyCalls += rs.Calls
		out.replyErrors += rs.Errors
		out.commands += rs.CommandsEmitted
		out.memories += rs.MemoriesEmitted
		out.classifierErrs += h.Classifiers[i].Stats().Errors
		ss := h.SummaryModels[i].Stats()
		out.summaryCalls += ss.Calls
		out.extractCalls += ss.ExtractCalls
		out.summaryErrs += ss.Errors
		out.summarySuccess += ss.Succeeded
	}
	out.digest = memstore.CombinedDigest(rig.backend, rig.artifacts)
	out.messages = allMsgs
	out.traces = sess.Traces
	out.traceDigest = rig.trace.Digest()
	out.finalClock = rig.clock.Now().UTC()
	return out, nil
}

func readAllMessages(ctx context.Context, store state.ConversationStore) ([]state.Message, error) {
	var pages [][]state.Message
	cursor := ""
	for {
		page, next, err := store.ConversationMessages(ctx, cursor)
		if err != nil {
			return nil, err
		}
		pages = append(pages, page)
		if next == "" {
			break
		}
		cursor = next
	}
	var all []state.Message
	for i := len(pages) - 1; i >= 0; i-- {
		all = append(all, pages[i]...)
	}
	return all, nil
}

func TestOuterRandomizedSim(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	var seeds []int64
	if *outerSeed >= 0 {
		seeds = []int64{*outerSeed}
	} else {
		n := *outerSeeds
		if n <= 0 {
			n = 1
		}
		seeds = make([]int64, n)
		for i := range n {
			seeds[i] = int64(i)
		}
	}
	steps := *outerSteps
	if steps <= 0 {
		steps = 60
	}

	var (
		totalCalls, totalErrors, totalTakeovers, totalMidInputs, totalCancels int
		totalCommands, totalMemories, totalClassErrs                          int
		totalSumCalls, totalExtractCalls, totalSumErrs, totalSumOK            int
	)

	start := time.Now()
	for _, seed := range seeds {
		res, err := runRandomizedOuterSim(ctx, seed, steps)
		if err != nil {
			t.Fatalf("outer sim failed (seed=%d steps=%d): %v\nRepro:\n  go test -v -race ./internal/sim/outer -run TestOuterRandomizedSim -outer-seed=%d -outer-steps=%d -outer-verbose",
				seed, steps, err, seed, steps)
		}
		totalCalls += res.replyCalls
		totalErrors += res.replyErrors
		totalTakeovers += res.leaseTakeovers
		totalMidInputs += res.midTurnInputs
		totalCancels += res.midTurnCancels
		totalCommands += res.commands
		totalMemories += res.memories
		totalClassErrs += res.classifierErrs
		totalSumCalls += res.summaryCalls
		totalExtractCalls += res.extractCalls
		totalSumErrs += res.summaryErrs
		totalSumOK += res.summarySuccess
	}

	t.Logf("completed %d seed(s) x %d steps in %s (calls=%d errs=%d takeovers=%d midInputs=%d cancels=%d cmds=%d mems=%d classErrs=%d sumCalls=%d extractCalls=%d sumErrs=%d sumOK=%d)",
		len(seeds), steps, time.Since(start).Round(time.Millisecond),
		totalCalls, totalErrors, totalTakeovers, totalMidInputs, totalCancels,
		totalCommands, totalMemories, totalClassErrs, totalSumCalls, totalExtractCalls, totalSumErrs, totalSumOK)

	if len(seeds) >= 8 {
		if totalCalls == 0 || totalErrors == 0 || totalTakeovers == 0 || totalMidInputs == 0 || totalCancels == 0 {
			t.Fatalf("insufficient outer fault/interleaving coverage: calls=%d errs=%d takeovers=%d midInputs=%d cancels=%d",
				totalCalls, totalErrors, totalTakeovers, totalMidInputs, totalCancels)
		}
		if totalCommands == 0 || totalMemories == 0 || totalClassErrs == 0 {
			t.Fatalf("insufficient action/classifier coverage: cmds=%d mems=%d classErrs=%d",
				totalCommands, totalMemories, totalClassErrs)
		}
		if totalSumCalls == 0 || totalExtractCalls == 0 || totalSumOK == 0 {
			t.Fatalf("insufficient compaction/extraction coverage: sumCalls=%d extractCalls=%d sumErrs=%d sumOK=%d",
				totalSumCalls, totalExtractCalls, totalSumErrs, totalSumOK)
		}
	}
}

func TestOuterDeterminism(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	seeds := []int64{0, 1, 7, 42, 1337}
	if *outerSeed >= 0 {
		seeds = []int64{*outerSeed}
	}
	steps := *outerSteps
	if steps <= 0 {
		steps = 60
	}

	for _, seed := range seeds {
		r1, err := runRandomizedOuterSim(ctx, seed, steps)
		if err != nil {
			t.Fatalf("seed=%d run1 failed: %v", seed, err)
		}
		r2, err := runRandomizedOuterSim(ctx, seed, steps)
		if err != nil {
			t.Fatalf("seed=%d run2 failed: %v", seed, err)
		}

		if r1.digest != r2.digest {
			t.Fatalf("seed=%d memstore.CombinedDigest diverged:\n  run1=%s\n  run2=%s", seed, r1.digest, r2.digest)
		}
		if !reflect.DeepEqual(r1.messages, r2.messages) {
			t.Fatalf("seed=%d ConversationMessages diverged (len %d vs %d)", seed, len(r1.messages), len(r2.messages))
		}
		if !reflect.DeepEqual(r1.traces, r2.traces) {
			t.Fatalf("seed=%d Traces diverged (len %d vs %d)", seed, len(r1.traces), len(r2.traces))
		}
		if r1.traceDigest != r2.traceDigest {
			t.Fatalf("seed=%d harness trace digest diverged: %s vs %s", seed, r1.traceDigest, r2.traceDigest)
		}
		if !r1.finalClock.Equal(r2.finalClock) {
			t.Fatalf("seed=%d final virtual clock diverged: %s vs %s", seed, r1.finalClock, r2.finalClock)
		}
		t.Logf("seed=%d deterministic digest=%s msgs=%d traces=%d", seed, r1.digest[:16], len(r1.messages), len(r1.traces))
	}
}

func TestOuterNoGoroutineLeaks(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	// Warm up once so any package-level background goroutines are already counted.
	if _, err := runRandomizedOuterSim(ctx, 77, 25); err != nil {
		t.Fatalf("warmup sim failed: %v", err)
	}
	for range 10 {
		runtime.Gosched()
	}
	before := runtime.NumGoroutine()

	for seed := int64(80); seed < 86; seed++ {
		if _, err := runRandomizedOuterSim(ctx, seed, 40); err != nil {
			t.Fatalf("seed=%d failed: %v", seed, err)
		}
	}

	// Allow any already-cancelled compact.go watcher goroutine to finish its exit path.
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	after := runtime.NumGoroutine()
	if after > before+1 {
		t.Fatalf("goroutine leak detected: before=%d after=%d", before, after)
	}
}
