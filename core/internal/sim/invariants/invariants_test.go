package invariants_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/invariants"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

// fakeClock is a simple deterministic clock for unit tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type testUserEnv struct {
	user   users.User
	db     *records.Database
	store  state.ConversationStore
	perms  *permissions.Service
	sched  *scheduler.Service
	target invariants.UserTarget
}

type testWorld struct {
	backend *memstore.Backend
	arts    *memstore.Artifacts
	clock   *fakeClock
	checker *invariants.Checker
	alice   *testUserEnv
	bob     *testUserEnv
	targets []invariants.UserTarget
}

func deliverPending(t *testing.T, ctx context.Context, store state.ConversationStore) {
	t.Helper()
	after := ""
	for {
		page, err := store.Pending(ctx, after)
		if err != nil {
			t.Fatalf("Pending: %v", err)
		}
		for _, cmd := range page.Commands {
			if err := store.Delivered(ctx, cmd.ID); err != nil {
				t.Fatalf("Delivered(%s): %v", cmd.ID, err)
			}
		}
		if page.Next == "" {
			return
		}
		after = page.Next
	}
}

func newTestWorld(opts ...memstore.Option) *testWorld {
	clock := newFakeClock()
	allOpts := append([]memstore.Option{memstore.WithClock(clock.Now)}, opts...)
	backend := memstore.New(allOpts...)
	arts := memstore.NewArtifacts()

	aliceUser := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "alice@example.com",
		DisplayName: "Alice",
		TimeZone:    "UTC",
	}
	bobUser := users.User{
		ID:          "22222222-2222-4222-8222-222222222222",
		Email:       "bob@example.com",
		DisplayName: "Bob",
		TimeZone:    "UTC",
	}

	makeUser := func(u users.User) *testUserEnv {
		db := records.NewShared(backend, u, 5.0)
		db.Now = clock.Now
		store := state.Compose(db, arts)
		perms := &permissions.Service{Store: store, Now: clock.Now}
		sched := &scheduler.Service{
			Store:       store,
			Tasks:       store,
			Permissions: perms,
			Now:         clock.Now,
			Accept: func(ctx context.Context, cmd state.Command) (state.Run, error) {
				receipts, err := store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
				if err != nil {
					return state.Run{}, err
				}
				return receipts[0].Run, nil
			},
		}
		return &testUserEnv{
			user:  u,
			db:    db,
			store: store,
			perms: perms,
			sched: sched,
			target: invariants.UserTarget{
				User:        u,
				Store:       store,
				Permissions: perms,
				Scheduler:   sched,
			},
		}
	}

	alice := makeUser(aliceUser)
	bob := makeUser(bobUser)
	checker := invariants.New(invariants.Config{
		Backend: backend,
		Now:     clock.Now,
	})

	return &testWorld{
		backend: backend,
		arts:    arts,
		clock:   clock,
		checker: checker,
		alice:   alice,
		bob:     bob,
		targets: []invariants.UserTarget{alice.target, bob.target},
	}
}

func requireViolation(t *testing.T, err error, wantInvariant string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected invariant violation %q, got nil", wantInvariant)
	}
	var vs invariants.Violations
	if !errors.As(err, &vs) {
		t.Fatalf("expected invariants.Violations error, got %T: %v", err, err)
	}
	for _, v := range vs {
		if v.Invariant == wantInvariant {
			return
		}
	}
	t.Fatalf("expected invariant %q in violations, got:\n%v", wantInvariant, err)
}

func int64Ptr(v int64) *int64 { return &v }

func TestPositive_CleanMultiUserWorldAndQuiescence(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()

	// Step 1: Alice starts a top-level task, retries same command ID (idempotent receipt),
	// opens an approval interaction at rev 1, then steers to rev 2 (superseding the interaction).
	aliceRun1 := "aaaaaaaa-1111-4111-8111-111111111111"
	cmdStart1 := state.Command{ID: "cmd-a-1", RunID: aliceRun1, Kind: state.CommandStart, Text: "Do task 1"}
	receipts, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{cmdStart1}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch start: %v", err)
	}
	if err := w.checker.RecordCommandReceipts(w.alice.user.ID, receipts); err != nil {
		t.Fatalf("RecordCommandReceipts: %v", err)
	}
	// Idempotent retry must match.
	retryReceipts, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{cmdStart1}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch retry: %v", err)
	}
	if err := w.checker.RecordCommandReceipts(w.alice.user.ID, retryReceipts); err != nil {
		t.Fatalf("RecordCommandReceipts retry: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety after start: %v", err)
	}

	// Open interaction at rev 1, then steer to rev 2 (records supersedes it atomically).
	expAt := w.clock.Now().Add(10 * time.Minute)
	if _, err := w.alice.perms.Open(ctx, permissions.Interaction{
		ID:                  "int_alice_1",
		TaskID:              aliceRun1,
		InstructionRevision: int64Ptr(1),
		Kind:                "approval",
		Prompt:              "Approve send?",
		ActionDigest:        "dig-1",
		ExpiresAt:           &expAt,
	}, "send"); err != nil {
		t.Fatalf("perms.Open: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety with pending rev-1 interaction: %v", err)
	}

	cmdSteer := state.Command{ID: "cmd-a-2", RunID: aliceRun1, Kind: state.CommandSteer, Text: "Change plan"}
	receipts, err = w.alice.store.AcceptCommandBatch(ctx, []state.Command{cmdSteer}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch steer: %v", err)
	}
	if err := w.checker.RecordCommandReceipts(w.alice.user.ID, receipts); err != nil {
		t.Fatalf("RecordCommandReceipts steer: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety after steer superseded rev-1 interaction: %v", err)
	}

	// Incorporate commands on aliceRun1, then open a work question and spawn a subtask.
	if err := w.alice.store.Incorporate(ctx, state.IncorporateRequest{RunID: aliceRun1, Revision: 2, Position: 2}); err != nil {
		t.Fatalf("Incorporate: %v", err)
	}
	if err := w.alice.store.ProjectQuestion(ctx, aliceRun1, 2, state.Question{
		ID: "q-work-1", Kind: state.QuestionWork, Text: "Delegate", Revision: 2,
	}); err != nil {
		t.Fatalf("ProjectQuestion: %v", err)
	}

	aliceChild := "aaaaaaaa-2222-4222-8222-222222222222"
	_, err = w.alice.store.AcceptCommandBatch(ctx, []state.Command{{
		ID: "cmd-child-1", RunID: aliceChild, ParentID: aliceRun1, QuestionID: "q-work-1",
		Kind: state.CommandStart, Text: "Child work",
	}}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch subtask: %v", err)
	}
	if err := w.alice.store.Incorporate(ctx, state.IncorporateRequest{RunID: aliceChild, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("Incorporate child: %v", err)
	}

	// Admit and legally settle operations on the child:
	// op1: admitted -> attempted -> submitted -> confirmed
	// op2: admitted -> uncertain -> failed -> Cleared="answered"
	if err := w.alice.store.Admit(ctx, aliceChild, 1, "op-1", w.clock.Now().Add(time.Minute)); err != nil {
		t.Fatalf("Admit op-1: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety after unrecorded Admit: %v", err)
	}
	histRef1, err := w.alice.store.AppendHistory(ctx, aliceChild, "", []byte(`{"summary":"ok"}`))
	if err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	for _, outcome := range []string{"attempted", "submitted", "confirmed"} {
		op := state.OperationRecord{
			ID: "op-1", CallID: "call-1", Name: "send_mail", EffectClass: "sensitive_send",
			Outcome: outcome, Revision: 1, Attempt: 1,
		}
		if outcome == "confirmed" {
			op.ResultRef = histRef1
		}
		if err := w.alice.store.PutOperation(ctx, aliceChild, op); err != nil {
			t.Fatalf("PutOperation(%s): %v", outcome, err)
		}
		if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
			t.Fatalf("CheckSafety after op-1=%s: %v", outcome, err)
		}
	}

	// op2: uncertain -> failed -> cleared
	if err := w.alice.store.PutOperation(ctx, aliceChild, state.OperationRecord{
		ID: "op-2", CallID: "call-2", Name: "tool_x", EffectClass: "reversible_write",
		Outcome: "uncertain", Revision: 1, Attempt: 1,
	}); err != nil {
		t.Fatalf("PutOperation op-2 uncertain: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety after op-2 uncertain: %v", err)
	}
	if err := w.alice.store.PutOperation(ctx, aliceChild, state.OperationRecord{
		ID: "op-2", CallID: "call-2", Name: "tool_x", EffectClass: "reversible_write",
		Outcome: "failed", ResultRef: histRef1, Revision: 1, Attempt: 1,
	}); err != nil {
		t.Fatalf("PutOperation op-2 failed: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety after op-2 failed: %v", err)
	}
	if err := w.alice.store.PutOperation(ctx, aliceChild, state.OperationRecord{
		ID: "op-2", CallID: "call-2", Name: "tool_x", EffectClass: "reversible_write",
		Outcome: "failed", ResultRef: histRef1, Revision: 1, Attempt: 1, Cleared: state.ClearedAnswered,
	}); err != nil {
		t.Fatalf("PutOperation op-2 cleared: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety after op-2 cleared: %v", err)
	}

	// Mark pending commands delivered, finish child (enters Finishing because Admit set CleanupPending),
	// run FinishCleanup, and then finish parent.
	deliverPending(t, ctx, w.alice.store)
	if _, err := w.alice.store.Publish(ctx, state.PublishRequest{
		RunID: aliceChild, Revision: 1, Status: state.RunCompleted, Text: "child done", BypassRouting: true,
	}); err != nil {
		t.Fatalf("Publish child: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety with child Finishing: %v", err)
	}
	if err := w.alice.store.FinishCleanup(ctx, aliceChild, ""); err != nil {
		t.Fatalf("FinishCleanup child: %v", err)
	}

	// Mark subtask_result command delivered to parent and incorporate it, then publish parent.
	deliverPending(t, ctx, w.alice.store)
	if err := w.alice.store.Incorporate(ctx, state.IncorporateRequest{RunID: aliceRun1, Revision: 2, Position: 3}); err != nil {
		t.Fatalf("Incorporate parent subtask_result: %v", err)
	}
	if _, err := w.alice.store.Publish(ctx, state.PublishRequest{
		RunID: aliceRun1, Revision: 2, Status: state.RunCompleted, Text: "parent done", BypassRouting: true,
	}); err != nil {
		t.Fatalf("Publish parent: %v", err)
	}

	// Step 2: Bob opens an interaction that later expires, plus creates and ticks a due one-time automation,
	// plus publishes a conversation turn and advances ReadMark.
	bobRun := "bbbbbbbb-1111-4111-8111-111111111111"
	if _, err := w.bob.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "cmd-b-1", RunID: bobRun, Kind: state.CommandStart, Text: "Bob job"},
	}, nil); err != nil {
		t.Fatalf("Bob start: %v", err)
	}
	shortExp := w.clock.Now().Add(30 * time.Second)
	if _, err := w.bob.perms.Open(ctx, permissions.Interaction{
		ID:                  "int_bob_exp",
		TaskID:              bobRun,
		InstructionRevision: int64Ptr(1),
		Kind:                "approval",
		Prompt:              "Expiring action",
		ActionDigest:        "dig-b-1",
		ExpiresAt:           &shortExp,
	}, "test"); err != nil {
		t.Fatalf("Bob perms.Open: %v", err)
	}

	dueAt := w.clock.Now().Add(1 * time.Minute)
	if _, err := w.bob.sched.Save(ctx, "", "req-auto-bob-1", 0, scheduler.Editable{
		Title:                  "Bob Reminder",
		Instruction:            "Check status",
		Trigger:                scheduler.Trigger{Kind: "once", At: &dueAt},
		BudgetUSD:              1.0,
		MaxActiveRuns:          1,
		MissedRun:              "latest",
		ExecutionWindowSeconds: 3600,
		DestinationIDs:         []string{scheduler.InAppDestination},
	}); err != nil {
		t.Fatalf("Bob sched.Save: %v", err)
	}

	// Advance clock past both shortExp and dueAt!
	w.clock.Advance(2 * time.Minute)

	// Expired interaction is checked via permissions.Service in CheckSafety and must pass!
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety after interaction expiry: %v", err)
	}

	// Tick Bob's scheduler so due automation advances cursor and submits its task.
	if _, err := w.bob.sched.Tick(ctx); err != nil {
		t.Fatalf("Bob sched.Tick: %v", err)
	}
	deliverPending(t, ctx, w.bob.store)

	// Finish all of Bob's open runs (bobRun + automation task) and tick scheduler to settle occurrence.
	bobOpen, err := w.bob.store.OpenTasks(ctx)
	if err != nil {
		t.Fatalf("Bob OpenTasks: %v", err)
	}
	for _, r := range bobOpen {
		if err := w.bob.store.Incorporate(ctx, state.IncorporateRequest{RunID: r.ID, Revision: r.Revision, Position: state.TaskPosition(r)}); err != nil {
			t.Fatalf("Bob Incorporate %s: %v", r.ID, err)
		}
		if _, err := w.bob.store.Publish(ctx, state.PublishRequest{
			RunID: r.ID, Revision: r.Revision, Status: state.RunCompleted, Text: "done", BypassRouting: true,
		}); err != nil {
			t.Fatalf("Bob Publish %s: %v", r.ID, err)
		}
	}
	if _, err := w.bob.sched.Tick(ctx); err != nil {
		t.Fatalf("Bob sched.Tick settle: %v", err)
	}

	// Drain conversation turns for Alice and Bob so WorkVersion is "" and test ReadMark.
	for _, u := range []*testUserEnv{w.alice, w.bob} {
		for {
			ctrl, turn, err := u.store.AcquireConversation(ctx, "sim-worker", "turn-"+u.user.ID[:4]+"-"+w.clock.Now().Format("150405"))
			if err != nil {
				t.Fatalf("AcquireConversation: %v", err)
			}
			if turn == nil {
				break
			}
			fence := state.ConversationFence{
				Owner: ctrl.Owner, Fence: ctrl.Fence, TurnID: turn.ID,
				Through: turn.Through, UserThrough: turn.UserThrough, Frozen: turn.Frozen,
			}
			turn.Attempt = state.ConversationAttempt{ID: "att-" + turn.ID, TurnID: turn.ID, Number: 1, Status: "completed"}
			w.clock.Advance(time.Second)
			if err := u.store.PublishConversation(ctx, fence, *turn, "All done!"); err != nil {
				t.Fatalf("PublishConversation: %v", err)
			}
			if err := u.store.MarkSeen(ctx, state.AssistantMessageID(turn.ID)); err != nil {
				t.Fatalf("MarkSeen: %v", err)
			}
		}
		deliverPending(t, ctx, u.store)
	}

	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("final CheckSafety failed: %v", err)
	}
	if err := w.checker.CheckQuiescence(ctx, w.targets, w.clock.Now()); err != nil {
		t.Fatalf("CheckQuiescence failed: %v", err)
	}

	// Also verify Store-only mode (Backend == nil) passes cleanly on the same state.
	storeOnlyChecker := invariants.New(invariants.Config{Now: w.clock.Now})
	if err := storeOnlyChecker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("store-only CheckSafety failed: %v", err)
	}
}

func TestPositive_CheckSafetyFaultFreeUnderHeavyFaultInjection(t *testing.T) {
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(42, 99))
	inj := memstore.NewFaultInjector(memstore.FaultConfig{
		GetErrProb:        1.0,
		QueryErrProb:      1.0,
		ApplyErrProb:      1.0,
		TxConflictProb:    1.0,
		CommitThenErrProb: 1.0,
	}, rng)
	inj.SetEnabled(false)

	w := newTestWorld(memstore.WithFaultInjector(inj))
	runID := "cccccccc-1111-4111-8111-111111111111"
	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "cmd-1", RunID: runID, Kind: state.CommandStart, Text: "Fault resilience test"},
	}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}

	// Turn faults up to 100%! CheckSafety must still succeed via Snapshot and trigger ZERO faults.
	inj.SetEnabled(true)
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety failed with 100%% faults enabled: %v", err)
	}
	if got := inj.Stats().Total(); got != 0 {
		t.Fatalf("CheckSafety triggered %d faults (expected 0)", got)
	}
}

func TestNegative_RunRevisionDecrease(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()
	runID := "aaaaaaaa-1111-4111-8111-111111111111"
	_, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "c1", RunID: runID, Kind: state.CommandStart, Text: "Start"},
		{ID: "c2", RunID: runID, Kind: state.CommandSteer, Text: "Steer"},
	}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("baseline CheckSafety: %v", err)
	}

	// Corrupt revision downward from 2 to 1 directly in memstore.
	path := "owners/" + w.alice.user.DataOwner() + "/runs/" + runID
	if err := w.backend.Apply(ctx, docstore.Mutation{
		Kind:    "update",
		Path:    path,
		Updates: []docstore.Update{{Path: "revision", Value: int64(1)}},
	}); err != nil {
		t.Fatalf("corrupt revision: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "run.revision_monotonic")
}

func TestNegative_TerminalRunStatusMutation(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()
	runID := "aaaaaaaa-1111-4111-8111-111111111111"
	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "c1", RunID: runID, Kind: state.CommandStart, Text: "Start"},
	}, nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.alice.store.Incorporate(ctx, state.IncorporateRequest{RunID: runID, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("incorporate: %v", err)
	}
	if _, err := w.alice.store.Publish(ctx, state.PublishRequest{
		RunID: runID, Revision: 1, Status: state.RunCompleted, Text: "done", BypassRouting: true,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("baseline CheckSafety: %v", err)
	}

	path := "owners/" + w.alice.user.DataOwner() + "/runs/" + runID
	// 1. Revert terminal -> running.
	if err := w.backend.Apply(ctx, docstore.Mutation{
		Kind:    "update",
		Path:    path,
		Updates: []docstore.Update{{Path: "status", Value: string(state.RunRunning)}},
	}); err != nil {
		t.Fatalf("corrupt status to running: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "run.terminal_status_immutable")

	// 2. Change terminal completed -> failed.
	if err := w.backend.Apply(ctx, docstore.Mutation{
		Kind:    "update",
		Path:    path,
		Updates: []docstore.Update{{Path: "status", Value: string(state.RunFailed)}},
	}); err != nil {
		t.Fatalf("corrupt status to failed: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "run.terminal_status_immutable")
}

func TestNegative_EventLogGap(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()
	runID := "aaaaaaaa-1111-4111-8111-111111111111"
	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "c1", RunID: runID, Kind: state.CommandStart, Text: "Start"},
		{ID: "c2", RunID: runID, Kind: state.CommandSteer, Text: "Steer"},
	}, nil); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := w.alice.store.Incorporate(ctx, state.IncorporateRequest{RunID: runID, Revision: 2, Position: 2}); err != nil {
		t.Fatalf("incorporate: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("baseline CheckSafety: %v", err)
	}

	// Delete event seq 1 to create a gap in 1..EventPosition.
	evPath := "owners/" + w.alice.user.DataOwner() + "/events/00000000000000000001"
	if err := w.backend.DeleteBatch(ctx, []string{evPath}); err != nil {
		t.Fatalf("delete event: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "event_log.gap")
}

func TestNegative_ReadMarkRegression(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()

	// Create two assistant messages and advance ReadMark to the second.
	if err := w.alice.store.AcceptConversationInput(ctx, "in-1", "hi", nil); err != nil {
		t.Fatalf("AcceptConversationInput: %v", err)
	}
	ctrl, turn, err := w.alice.store.AcquireConversation(ctx, "w1", "t1")
	if err != nil || turn == nil {
		t.Fatalf("AcquireConversation 1: %v", err)
	}
	w.clock.Advance(5 * time.Second)
	fence := state.ConversationFence{Owner: ctrl.Owner, Fence: ctrl.Fence, TurnID: turn.ID, Through: turn.Through, UserThrough: turn.UserThrough}
	turn.Attempt = state.ConversationAttempt{ID: "a1", TurnID: turn.ID, Number: 1, Status: "completed"}
	if err := w.alice.store.PublishConversation(ctx, fence, *turn, "reply 1"); err != nil {
		t.Fatalf("PublishConversation 1: %v", err)
	}
	if err := w.alice.store.MarkSeen(ctx, state.AssistantMessageID("t1")); err != nil {
		t.Fatalf("MarkSeen 1: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("baseline CheckSafety: %v", err)
	}

	// Move ReadMark.Through backwards directly in memstore.
	seenPath := "owners/" + w.alice.user.DataOwner() + "/conversation/seen"
	if err := w.backend.Apply(ctx, docstore.Mutation{
		Kind: "update",
		Path: seenPath,
		Updates: []docstore.Update{
			{Path: "through", Value: w.clock.Now().Add(-1 * time.Hour)},
		},
	}); err != nil {
		t.Fatalf("corrupt readmark: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "readmark.through_monotonic")
}

func TestNegative_SettledOperationMutation(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()
	runID := "aaaaaaaa-1111-4111-8111-111111111111"
	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "c1", RunID: runID, Kind: state.CommandStart, Text: "Start"},
	}, nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	ref1, err := w.alice.store.AppendHistory(ctx, runID, "", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	if err := w.alice.store.SettleOperation(ctx, runID, state.OperationRecord{
		ID: "op-settled", CallID: "call-1", Name: "write_doc", EffectClass: "reversible_write",
		Outcome: "confirmed", ResultRef: ref1, Revision: 1, Attempt: 1,
	}); err != nil {
		t.Fatalf("SettleOperation: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("baseline CheckSafety: %v", err)
	}

	// Mutate settled outcome confirmed -> failed directly in memstore.
	opPath := "owners/" + w.alice.user.DataOwner() + "/runs/" + runID + "/operations/op-settled"
	if err := w.backend.Apply(ctx, docstore.Mutation{
		Kind:    "update",
		Path:    opPath,
		Updates: []docstore.Update{{Path: "outcome", Value: "failed"}},
	}); err != nil {
		t.Fatalf("corrupt operation: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "operation.single_settlement")
}

func TestNegative_CommandReceiptMismatch(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()

	r1 := state.CommandReceipt{
		Command: state.Command{ID: "cmd-dup", RunID: "run-1", Kind: state.CommandStart, Position: 1, Revision: 1},
		Run:     state.Run{ID: "run-1"},
	}
	if err := w.checker.RecordCommandReceipt(w.alice.user.ID, r1); err != nil {
		t.Fatalf("first receipt: %v", err)
	}
	r2 := state.CommandReceipt{
		Command: state.Command{ID: "cmd-dup", RunID: "run-1", Kind: state.CommandStart, Position: 2, Revision: 1},
		Run:     state.Run{ID: "run-1"},
	}
	requireViolation(t, w.checker.RecordCommandReceipt(w.alice.user.ID, r2), "command.receipt_idempotent")
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "command.receipt_idempotent")
}

func TestNegative_ExecutionStartedWhilePaused(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()

	// Pause system/control.
	if err := w.backend.Apply(ctx, docstore.Mutation{
		Kind:    "update",
		Path:    "system/control",
		Updates: []docstore.Update{{Path: "paused", Value: true}},
	}); err != nil {
		t.Fatalf("pause control: %v", err)
	}

	// Simulate worker starting a new execution while paused.
	w.checker.OnExecutionStart(w.alice.user.ID, "run-paused-start")
	if got := w.checker.PausedStartViolations(); got != 1 {
		t.Fatalf("PausedStartViolations = %d, want 1", got)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "control.execution_started_while_paused")
}

func TestNegative_UnknownOwnerPathAndCrossUserLeak(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()

	// Create a valid run for Bob.
	bobRun := "bbbbbbbb-1111-4111-8111-111111111111"
	if _, err := w.bob.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "cmd-b-1", RunID: bobRun, Kind: state.CommandStart, Text: "Bob private"},
	}, nil); err != nil {
		t.Fatalf("Bob start: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("baseline CheckSafety: %v", err)
	}

	// 1. Inject a document under an unknown owner path.
	if err := w.backend.Apply(ctx, docstore.Mutation{
		Kind:   "set",
		Path:   "owners/rogue-owner/runs/rogue-run",
		Fields: map[string]any{"id": "rogue-run", "status": "running"},
	}); err != nil {
		t.Fatalf("inject rogue owner: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "isolation.unknown_owner_path")

	// Clean up rogue owner doc, then leak Bob's run ID into an Alice interaction.
	if err := w.backend.DeleteBatch(ctx, []string{"owners/rogue-owner/runs/rogue-run"}); err != nil {
		t.Fatalf("delete rogue: %v", err)
	}
	if _, err := w.alice.perms.Open(ctx, permissions.Interaction{
		ID:     "int_leak",
		TaskID: bobRun, // belongs to Bob!
		Kind:   "question",
		Prompt: "Leaked ref",
	}, "leak"); err != nil {
		t.Fatalf("open leaking interaction: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "isolation.cross_user_reference")
}

func TestNegative_StalePendingInteractionAndExpiredApproved(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()
	runID := "aaaaaaaa-1111-4111-8111-111111111111"
	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "c1", RunID: runID, Kind: state.CommandStart, Text: "Start"},
		{ID: "c2", RunID: runID, Kind: state.CommandSteer, Text: "Steer to rev 2"},
	}, nil); err != nil {
		t.Fatalf("accept: %v", err)
	}

	// Open a pending interaction pinned to stale revision 1 while run is already at revision 2.
	if _, err := w.alice.perms.Open(ctx, permissions.Interaction{
		ID:                  "int_stale",
		TaskID:              runID,
		InstructionRevision: int64Ptr(1),
		Kind:                "approval",
		Prompt:              "Stale rev 1 approval",
		ActionDigest:        "dig-stale",
	}, "test"); err != nil {
		t.Fatalf("open stale interaction: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "interaction.stale_revision")

	// Now test an expired interaction falsely recorded as approved after ExpiresAt.
	w2 := newTestWorld()
	exp := w2.clock.Now().Add(-5 * time.Minute)
	resolved := w2.clock.Now()
	bodyBytes, _ := json.Marshal(permissions.Interaction{
		ID:           "int_exp_ok",
		Kind:         "approval",
		Status:       "resolved",
		Prompt:       "Late approval",
		ActionDigest: "dig-late",
		ExpiresAt:    &exp,
		ResolvedAt:   &resolved,
		Resolution:   &permissions.Resolution{Kind: "approve", At: resolved},
	})
	if err := w2.backend.Apply(ctx, docstore.Mutation{
		Kind: "set",
		Path: "owners/" + w2.alice.user.DataOwner() + "/r_interactions/int_exp_ok",
		Fields: map[string]any{
			"id":         "int_exp_ok",
			"revision":   int64(2),
			"body":       string(bodyBytes),
			"index":      map[string]any{"status": "resolved", "kind": "approval"},
			"created_at": exp.Add(-time.Minute),
			"updated_at": resolved,
		},
	}); err != nil {
		t.Fatalf("inject expired approved: %v", err)
	}
	requireViolation(t, w2.checker.CheckSafety(ctx, w2.targets), "interaction.expired_approved")
}

func TestNegative_SubtaskParentViolations(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()
	parentID := "aaaaaaaa-1111-4111-8111-111111111111"
	childID := "aaaaaaaa-2222-4222-8222-222222222222"

	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "c1", RunID: parentID, Kind: state.CommandStart, Text: "Parent"},
	}, nil); err != nil {
		t.Fatalf("start parent: %v", err)
	}
	if err := w.alice.store.Incorporate(ctx, state.IncorporateRequest{RunID: parentID, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("incorporate parent: %v", err)
	}
	if err := w.alice.store.ProjectQuestion(ctx, parentID, 1, state.Question{
		ID: "qw", Kind: state.QuestionWork, Text: "Delegate", Revision: 1,
	}); err != nil {
		t.Fatalf("ProjectQuestion: %v", err)
	}
	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "c2", RunID: childID, ParentID: parentID, QuestionID: "qw", Kind: state.CommandStart, Text: "Child"},
	}, nil); err != nil {
		t.Fatalf("start child: %v", err)
	}
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("baseline CheckSafety: %v", err)
	}

	// Corrupt parent directly to completed while child is still running!
	now := w.clock.Now()
	if err := w.backend.Apply(ctx, docstore.Mutation{
		Kind: "update",
		Path: "owners/" + w.alice.user.DataOwner() + "/runs/" + parentID,
		Updates: []docstore.Update{
			{Path: "status", Value: string(state.RunCompleted)},
			{Path: "finished_at", Value: now},
			{Path: "question", Value: docstore.Delete},
		},
	}); err != nil {
		t.Fatalf("corrupt parent terminal: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "subtask.terminal_parent_has_running_child")

	// Also test 2-level nested subtask violation.
	w2 := newTestWorld()
	owner := w2.alice.user.DataOwner()
	now2 := w2.clock.Now()
	for _, r := range []state.Run{
		{ID: "p1", Status: state.RunRunning, StartedAt: now2, Revision: 1, Position: 1, Subtasks: []state.SubtaskLink{{ID: "c1", Status: state.RunRunning}}},
		{ID: "c1", ParentID: "p1", Status: state.RunRunning, StartedAt: now2, Revision: 1, Position: 1, Subtasks: []state.SubtaskLink{{ID: "g1", Status: state.RunRunning}}},
		{ID: "g1", ParentID: "c1", Status: state.RunRunning, StartedAt: now2, Revision: 1, Position: 1},
	} {
		fields, err := docstore.Encode(r)
		if err != nil {
			t.Fatalf("encode run: %v", err)
		}
		if err := w2.backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "owners/" + owner + "/runs/" + r.ID,
			Fields: fields,
		}); err != nil {
			t.Fatalf("apply run: %v", err)
		}
	}
	requireViolation(t, w2.checker.CheckSafety(ctx, w2.targets), "subtask.max_depth_one")
}

func TestNegative_NegativeBudgetAndSpent(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()
	runID := "aaaaaaaa-1111-4111-8111-111111111111"
	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "c1", RunID: runID, Kind: state.CommandStart, Text: "Start"},
	}, nil); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.backend.Apply(ctx, docstore.Mutation{
		Kind:    "update",
		Path:    "owners/" + w.alice.user.DataOwner() + "/runs/" + runID,
		Updates: []docstore.Update{{Path: "spent_usd", Value: -0.25}},
	}); err != nil {
		t.Fatalf("corrupt spent: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "run.spent_valid")
}

func TestNegative_QuiescenceViolations(t *testing.T) {
	ctx := context.Background()

	t.Run("undelivered_command_and_work_version", func(t *testing.T) {
		w := newTestWorld()
		runID := "aaaaaaaa-1111-4111-8111-111111111111"
		if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
			{ID: "c-undelivered", RunID: runID, Kind: state.CommandStart, Text: "Undelivered"},
		}, nil); err != nil {
			t.Fatalf("start: %v", err)
		}
		err := w.checker.CheckQuiescence(ctx, w.targets, w.clock.Now())
		requireViolation(t, err, "quiescence.pending_commands")
		requireViolation(t, err, "quiescence.work_version_non_empty")
	})

	t.Run("stuck_cleanup_task", func(t *testing.T) {
		w := newTestWorld()
		runID := "aaaaaaaa-1111-4111-8111-111111111111"
		if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
			{ID: "c1", RunID: runID, Kind: state.CommandStart, Text: "Start"},
		}, nil); err != nil {
			t.Fatalf("start: %v", err)
		}
		deliverPending(t, ctx, w.alice.store)
		if err := w.alice.store.Incorporate(ctx, state.IncorporateRequest{RunID: runID, Revision: 1, Position: 1}); err != nil {
			t.Fatalf("inc: %v", err)
		}
		if err := w.alice.store.Admit(ctx, runID, 1, "op-1", w.clock.Now().Add(time.Minute)); err != nil {
			t.Fatalf("admit: %v", err)
		}
		// Publish enters Finishing because CleanupPending=true; fail to run FinishCleanup!
		if _, err := w.alice.store.Publish(ctx, state.PublishRequest{
			RunID: runID, Revision: 1, Status: state.RunCompleted, Text: "done", BypassRouting: true,
		}); err != nil {
			t.Fatalf("publish: %v", err)
		}
		err := w.checker.CheckQuiescence(ctx, w.targets, w.clock.Now())
		requireViolation(t, err, "quiescence.cleanup_stuck")
		requireViolation(t, err, "quiescence.finishing_stuck")
	})

	t.Run("stuck_publication_task", func(t *testing.T) {
		w := newTestWorld()
		runID := "aaaaaaaa-1111-4111-8111-111111111111"
		if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
			{ID: "c1", RunID: runID, Kind: state.CommandStart, Text: "Start"},
		}, nil); err != nil {
			t.Fatalf("start: %v", err)
		}
		deliverPending(t, ctx, w.alice.store)
		if err := w.alice.store.Incorporate(ctx, state.IncorporateRequest{RunID: runID, Revision: 1, Position: 1}); err != nil {
			t.Fatalf("inc: %v", err)
		}
		// Add a user conversation input so Publish defers publication, then route conversation without waking publication!
		if err := w.alice.store.AcceptConversationInput(ctx, "u-msg-1", "wait", nil); err != nil {
			t.Fatalf("AcceptConversationInput: %v", err)
		}
		_, pubErr := w.alice.store.Publish(ctx, state.PublishRequest{
			RunID: runID, Revision: 1, Status: state.RunCompleted, Text: "answer",
		})
		if !errors.Is(pubErr, state.ErrRoutingPending) {
			t.Fatalf("expected ErrRoutingPending, got %v", pubErr)
		}
		// Complete the conversation turn so UserRoutingPending becomes false, leaving publication_pending=true stuck!
		ctrl, turn, err := w.alice.store.AcquireConversation(ctx, "w1", "turn-1")
		if err != nil || turn == nil {
			t.Fatalf("AcquireConversation: %v", err)
		}
		fence := state.ConversationFence{Owner: ctrl.Owner, Fence: ctrl.Fence, TurnID: turn.ID, Through: turn.Through, UserThrough: turn.UserThrough}
		turn.Attempt = state.ConversationAttempt{ID: "att-1", TurnID: turn.ID, Number: 1, Status: "completed"}
		if err := w.alice.store.PublishConversation(ctx, fence, *turn, "routed"); err != nil {
			t.Fatalf("PublishConversation: %v", err)
		}

		err = w.checker.CheckQuiescence(ctx, w.targets, w.clock.Now())
		requireViolation(t, err, "quiescence.publication_stuck")
		requireViolation(t, err, "quiescence.work_version_non_empty")
	})

	t.Run("due_automation_not_advanced_and_duplicate_occurrence", func(t *testing.T) {
		w := newTestWorld()
		dueAt := w.clock.Now().Add(1 * time.Minute)
		receipt, err := w.alice.sched.Save(ctx, "", "req-auto-1", 0, scheduler.Editable{
			Title:                  "Missed Tick",
			Instruction:            "Run me",
			Trigger:                scheduler.Trigger{Kind: "once", At: &dueAt},
			BudgetUSD:              1.0,
			MaxActiveRuns:          1,
			MissedRun:              "latest",
			ExecutionWindowSeconds: 3600,
			DestinationIDs:         []string{scheduler.InAppDestination},
		})
		if err != nil {
			t.Fatalf("sched.Save: %v", err)
		}
		var auto scheduler.Automation
		if err := json.Unmarshal(receipt.Resource, &auto); err != nil {
			t.Fatalf("unmarshal auto: %v", err)
		}
		// Advance clock past dueAt without calling Tick!
		w.clock.Advance(5 * time.Minute)
		err = w.checker.CheckQuiescence(ctx, w.targets, w.clock.Now())
		requireViolation(t, err, "quiescence.due_automation_not_advanced")

		// Now inject two duplicate occurrences with the exact same (automation, generation, scheduled_at).
		slot := dueAt.UTC()
		for _, occID := range []string{"occ_dup_1", "occ_dup_2"} {
			body, _ := json.Marshal(scheduler.Occurrence{
				ID:           occID,
				AutomationID: auto.ID,
				Generation:   0,
				ScheduledAt:  slot,
				Status:       "completed",
				TaskIDs:      []string{},
				DeliveryIDs:  []string{},
			})
			if err := w.backend.Apply(ctx, docstore.Mutation{
				Kind: "set",
				Path: "owners/" + w.alice.user.DataOwner() + "/r_occurrences/" + occID,
				Fields: map[string]any{
					"id":         occID,
					"revision":   int64(1),
					"body":       string(body),
					"index":      map[string]any{"automation_id": auto.ID, "status": "completed"},
					"created_at": slot,
					"updated_at": slot,
				},
			}); err != nil {
				t.Fatalf("inject duplicate occ: %v", err)
			}
		}
		err = w.checker.CheckQuiescence(ctx, w.targets, w.clock.Now())
		requireViolation(t, err, "scheduler.duplicate_occurrence_key")
		if !strings.Contains(err.Error(), "duplicate occurrence") {
			t.Fatalf("expected descriptive duplicate occurrence message, got: %v", err)
		}
	})
}

func TestTaskControlSafetyMonotonicityAndLeaseIsolation(t *testing.T) {
	ctx := context.Background()
	w := newTestWorld()

	aliceRun := "aaaaaaaa-1111-4111-8111-111111111111"
	bobRun := "bbbbbbbb-1111-4111-8111-111111111111"
	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "cmd-a-1", RunID: aliceRun, Kind: state.CommandStart, Text: "Alice task"},
	}, nil); err != nil {
		t.Fatalf("Alice start: %v", err)
	}
	if _, err := w.bob.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "cmd-b-1", RunID: bobRun, Kind: state.CommandStart, Text: "Bob task"},
	}, nil); err != nil {
		t.Fatalf("Bob start: %v", err)
	}

	tcPath := "owners/" + w.alice.user.DataOwner() + "/task_controls/" + aliceRun
	writeTC := func(tc state.TaskControl) {
		t.Helper()
		fields, err := docstore.Encode(tc)
		if err != nil {
			t.Fatalf("encode TaskControl: %v", err)
		}
		if err := w.backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   tcPath,
			Fields: fields,
		}); err != nil {
			t.Fatalf("apply TaskControl: %v", err)
		}
	}

	// Monotonic progression passes cleanly.
	writeTC(state.TaskControl{RunID: aliceRun, Phase: state.TaskPhaseModel, Fence: 2, Models: 3, Tools: 4, Checks: 1})
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety baseline TaskControl: %v", err)
	}
	writeTC(state.TaskControl{RunID: aliceRun, Phase: state.TaskPhaseCalls, Fence: 3, Models: 4, Tools: 5, Checks: 2})
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety advanced TaskControl: %v", err)
	}

	// Regression of each counter is caught individually.
	writeTC(state.TaskControl{RunID: aliceRun, Phase: state.TaskPhaseCalls, Fence: 2, Models: 4, Tools: 5, Checks: 2})
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "task_control.fence_monotonic")

	writeTC(state.TaskControl{RunID: aliceRun, Phase: state.TaskPhaseCalls, Fence: 3, Models: 3, Tools: 5, Checks: 2})
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "task_control.models_monotonic")

	writeTC(state.TaskControl{RunID: aliceRun, Phase: state.TaskPhaseCalls, Fence: 3, Models: 4, Tools: 4, Checks: 2})
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "task_control.tools_monotonic")

	writeTC(state.TaskControl{RunID: aliceRun, Phase: state.TaskPhaseCalls, Fence: 3, Models: 4, Tools: 5, Checks: 1})
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "task_control.checks_monotonic")

	// Negative counter is caught.
	writeTC(state.TaskControl{RunID: aliceRun, Phase: state.TaskPhaseCalls, Fence: -1, Models: 4, Tools: 5, Checks: 2})
	err := w.checker.CheckSafety(ctx, w.targets)
	requireViolation(t, err, "task_control.counters_non_negative")
	requireViolation(t, err, "task_control.fence_monotonic")

	// Restore valid TaskControl.
	writeTC(state.TaskControl{RunID: aliceRun, Phase: state.TaskPhaseCalls, Fence: 3, Models: 4, Tools: 5, Checks: 2})
	if err := w.checker.CheckSafety(ctx, w.targets); err != nil {
		t.Fatalf("CheckSafety restored TaskControl: %v", err)
	}

	// Unknown user in nori_task_leases is caught.
	if err := w.backend.WakeTask(ctx, "rogue-user", "rogue-run", w.clock.Now()); err != nil {
		t.Fatalf("WakeTask rogue: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "isolation.unknown_lease_user")
	_, _ = w.backend.DeleteTaskLease(ctx, "rogue-user", "rogue-run", 0)

	// Cross-user RunID in nori_task_leases (Alice lease pointing to Bob's run) is caught.
	if err := w.backend.WakeTask(ctx, w.alice.user.ID, bobRun, w.clock.Now()); err != nil {
		t.Fatalf("WakeTask cross-user: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "isolation.cross_user_reference")
	_, _ = w.backend.DeleteTaskLease(ctx, w.alice.user.ID, bobRun, 0)

	// Unknown user in nori_user_wakes is caught.
	if err := w.backend.WakeUser(ctx, "rogue-user", docstore.LaneConversation, w.clock.Now()); err != nil {
		t.Fatalf("WakeUser rogue: %v", err)
	}
	requireViolation(t, w.checker.CheckSafety(ctx, w.targets), "isolation.unknown_wake_user")
}

func TestLeaseQuiescence_ValidStatesAndFaultFree(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock()
	rng := rand.New(rand.NewPCG(77, 88))
	inj := memstore.NewFaultInjector(memstore.FaultConfig{
		GetErrProb:        1.0,
		QueryErrProb:      1.0,
		ApplyErrProb:      1.0,
		TxConflictProb:    1.0,
		CommitThenErrProb: 1.0,
	}, rng)
	inj.SetEnabled(false)

	w := newTestWorld(memstore.WithClock(clock.Now), memstore.WithFaultInjector(inj))
	w.clock = clock

	parkLease := func(userID, runID, reason string) {
		t.Helper()
		if err := w.backend.WakeTask(ctx, userID, runID, w.clock.Now()); err != nil {
			t.Fatalf("WakeTask %s: %v", runID, err)
		}
		claimed, ok, err := w.backend.ClaimTaskByID(ctx, userID, runID, "w1", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID %s: ok=%v err=%v", runID, ok, err)
		}
		parked, err := w.backend.ParkTask(ctx, userID, runID, "w1", claimed.WakeSeq, reason)
		if err != nil || !parked {
			t.Fatalf("ParkTask %s: parked=%v err=%v", runID, parked, err)
		}
	}

	// 1. Alice has a completed clean run (no lease row).
	doneRun := "aaaaaaaa-0000-4000-8000-000000000001"
	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "c-done", RunID: doneRun, Kind: state.CommandStart, Text: "Quick"},
	}, nil); err != nil {
		t.Fatalf("start doneRun: %v", err)
	}
	if err := w.alice.store.Incorporate(ctx, state.IncorporateRequest{RunID: doneRun, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("incorporate doneRun: %v", err)
	}
	if _, err := w.alice.store.Publish(ctx, state.PublishRequest{
		RunID: doneRun, Revision: 1, Status: state.RunCompleted, Text: "finished", BypassRouting: true,
	}); err != nil {
		t.Fatalf("publish doneRun: %v", err)
	}
	if _, err := w.backend.DeleteTaskLease(ctx, w.alice.user.ID, doneRun, 0); err != nil {
		t.Fatalf("delete doneRun lease: %v", err)
	}

	// 2. Alice has a validly parked question task (unresolved question, not cancelled).
	qRun := "aaaaaaaa-0000-4000-8000-000000000002"
	if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: "c-q", RunID: qRun, Kind: state.CommandStart, Text: "Ask user"},
	}, nil); err != nil {
		t.Fatalf("start qRun: %v", err)
	}
	if err := w.alice.store.Incorporate(ctx, state.IncorporateRequest{RunID: qRun, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("incorporate qRun: %v", err)
	}
	if err := w.alice.store.ProjectQuestion(ctx, qRun, 1, state.Question{
		ID: "q-1", Kind: "input", Text: "Need info", Revision: 1,
	}); err != nil {
		t.Fatalf("ProjectQuestion: %v", err)
	}
	parkLease(w.alice.user.ID, qRun, docstore.WaitQuestion)

	// Drain Alice's conversation inbox so conv.Pending() == false, and park conversation wake.
	for {
		ctrl, turn, err := w.alice.store.AcquireConversation(ctx, "w1", "turn-"+w.clock.Now().Format("150405"))
		if err != nil {
			t.Fatalf("AcquireConversation: %v", err)
		}
		if turn == nil {
			break
		}
		fence := state.ConversationFence{Owner: ctrl.Owner, Fence: ctrl.Fence, TurnID: turn.ID, Through: turn.Through, UserThrough: turn.UserThrough, Frozen: turn.Frozen}
		turn.Attempt = state.ConversationAttempt{ID: "att-" + turn.ID, TurnID: turn.ID, Number: 1, Status: "completed"}
		w.clock.Advance(time.Second)
		if err := w.alice.store.PublishConversation(ctx, fence, *turn, "Noted."); err != nil {
			t.Fatalf("PublishConversation: %v", err)
		}
	}
	if err := w.backend.WakeUser(ctx, w.alice.user.ID, docstore.LaneConversation, w.clock.Now()); err != nil {
		t.Fatalf("WakeUser conv: %v", err)
	}
	cw, ok, err := w.backend.ClaimUserWake(ctx, docstore.LaneConversation, "w1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimUserWake conv: ok=%v err=%v", ok, err)
	}
	if parked, err := w.backend.ParkUserWake(ctx, w.alice.user.ID, docstore.LaneConversation, "w1", cw.WakeSeq); err != nil || !parked {
		t.Fatalf("ParkUserWake conv: parked=%v err=%v", parked, err)
	}

	// 3. Bob has a future-scheduled automation and matching future scheduler wake (WakeAt > now).
	futureAt := w.clock.Now().Add(15 * time.Minute)
	if _, err := w.bob.sched.Save(ctx, "", "req-future-1", 0, scheduler.Editable{
		Title:                  "Future Job",
		Instruction:            "Run later",
		Trigger:                scheduler.Trigger{Kind: "once", At: &futureAt},
		BudgetUSD:              1.0,
		MaxActiveRuns:          1,
		MissedRun:              "latest",
		ExecutionWindowSeconds: 3600,
		DestinationIDs:         []string{scheduler.InAppDestination},
	}); err != nil {
		t.Fatalf("Bob sched.Save: %v", err)
	}
	if err := w.backend.WakeUser(ctx, w.bob.user.ID, docstore.LaneScheduler, futureAt); err != nil {
		t.Fatalf("WakeUser scheduler future: %v", err)
	}

	// Enable 100% faults: CheckLeaseQuiescence reads fault-free from Snapshot and triggers ZERO faults.
	inj.SetEnabled(true)
	if err := w.checker.CheckLeaseQuiescence(ctx, w.targets, w.clock.Now()); err != nil {
		t.Fatalf("CheckLeaseQuiescence on valid quiescent state failed: %v", err)
	}
	if got := inj.Stats().Total(); got != 0 {
		t.Fatalf("CheckLeaseQuiescence triggered %d faults, want 0", got)
	}
}

func TestLeaseQuiescence_Violations(t *testing.T) {
	ctx := context.Background()

	newLeaseWorld := func() *testWorld {
		clock := newFakeClock()
		w := newTestWorld(memstore.WithClock(clock.Now))
		w.clock = clock
		return w
	}

	parkLease := func(t *testing.T, w *testWorld, userID, runID, reason string) {
		t.Helper()
		if err := w.backend.WakeTask(ctx, userID, runID, w.clock.Now()); err != nil {
			t.Fatalf("WakeTask: %v", err)
		}
		c, ok, err := w.backend.ClaimTaskByID(ctx, userID, runID, "w1", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		if parked, err := w.backend.ParkTask(ctx, userID, runID, "w1", c.WakeSeq, reason); err != nil || !parked {
			t.Fatalf("ParkTask: parked=%v err=%v", parked, err)
		}
	}

	t.Run("missing_lease_for_running_and_cleanup_pending_runs", func(t *testing.T) {
		w := newLeaseWorld()
		r1 := "aaaaaaaa-1111-4111-8111-111111111111"
		if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
			{ID: "c1", RunID: r1, Kind: state.CommandStart, Text: "Running without lease"},
		}, nil); err != nil {
			t.Fatalf("start: %v", err)
		}
		if _, err := w.backend.DeleteTaskLease(ctx, w.alice.user.ID, r1, 0); err != nil {
			t.Fatalf("delete r1 lease: %v", err)
		}
		requireViolation(t, w.checker.CheckLeaseQuiescence(ctx, w.targets, w.clock.Now()), "quiescence.lease_missing_for_run")

		// Terminal run with CleanupPending=true also requires a lease.
		w2 := newLeaseWorld()
		r2 := "aaaaaaaa-2222-4222-8222-222222222222"
		now := w2.clock.Now()
		fields, err := docstore.Encode(state.Run{
			ID:             r2,
			Status:         state.RunCompleted,
			StartedAt:      now,
			FinishedAt:     &now,
			CleanupPending: true,
		})
		if err != nil {
			t.Fatalf("encode run: %v", err)
		}
		if err := w2.backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "owners/" + w2.alice.user.DataOwner() + "/runs/" + r2,
			Fields: fields,
		}); err != nil {
			t.Fatalf("apply run: %v", err)
		}
		requireViolation(t, w2.checker.CheckLeaseQuiescence(ctx, w2.targets, w2.clock.Now()), "quiescence.lease_missing_for_run")
	})

	t.Run("orphan_and_stale_task_leases", func(t *testing.T) {
		w := newLeaseWorld()
		// Orphan lease pointing to non-existent run.
		parkLease(t, w, w.alice.user.ID, "nonexistent-run", docstore.WaitQuestion)
		requireViolation(t, w.checker.CheckLeaseQuiescence(ctx, w.targets, w.clock.Now()), "quiescence.lease_orphan_run")
		_, _ = w.backend.DeleteTaskLease(ctx, w.alice.user.ID, "nonexistent-run", 0)

		// Stale lease for terminal clean run.
		rDone := "aaaaaaaa-3333-4333-8333-333333333333"
		now := w.clock.Now()
		fields, _ := docstore.Encode(state.Run{
			ID:         rDone,
			Status:     state.RunCompleted,
			StartedAt:  now,
			FinishedAt: &now,
		})
		_ = w.backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "owners/" + w.alice.user.DataOwner() + "/runs/" + rDone,
			Fields: fields,
		})
		parkLease(t, w, w.alice.user.ID, rDone, docstore.WaitQuestion)
		requireViolation(t, w.checker.CheckLeaseQuiescence(ctx, w.targets, w.clock.Now()), "quiescence.lease_stale_for_terminal_run")
	})

	t.Run("leaked_owner_and_undrained_runnable_task_lease", func(t *testing.T) {
		w := newLeaseWorld()
		r1 := "aaaaaaaa-4444-4444-8444-444444444444"
		if _, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
			{ID: "c1", RunID: r1, Kind: state.CommandStart, Text: "Runnable"},
		}, nil); err != nil {
			t.Fatalf("start: %v", err)
		}
		if err := w.backend.WakeTask(ctx, w.alice.user.ID, r1, w.clock.Now()); err != nil {
			t.Fatalf("WakeTask: %v", err)
		}
		// Unclaimed but runnable with WakeAt <= now -> undrained.
		requireViolation(t, w.checker.CheckLeaseQuiescence(ctx, w.targets, w.clock.Now()), "quiescence.task_lease_undrained_runnable")

		// Claimed -> leaked owner + still runnable.
		if _, ok, err := w.backend.ClaimTaskByID(ctx, w.alice.user.ID, r1, "leaked-worker", 30*time.Second); err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		err := w.checker.CheckLeaseQuiescence(ctx, w.targets, w.clock.Now())
		requireViolation(t, err, "quiescence.task_lease_leaked_owner")
		requireViolation(t, err, "quiescence.task_lease_undrained_runnable")
	})

	t.Run("stuck_parked_waiting_reasons", func(t *testing.T) {
		// 1. waiting_reason = paused while Control.Paused = false
		w := newLeaseWorld()
		r1 := "aaaaaaaa-5555-4555-8555-555555555551"
		_, _ = w.alice.store.AcceptCommandBatch(ctx, []state.Command{
			{ID: "c1", RunID: r1, Kind: state.CommandStart, Text: "Paused task"},
		}, nil)
		parkLease(t, w, w.alice.user.ID, r1, docstore.WaitPaused)
		requireViolation(t, w.checker.CheckLeaseQuiescence(ctx, w.targets, w.clock.Now()), "quiescence.task_lease_stuck_paused")

		// 2. waiting_reason = routing_pending while UserRoutingPending = false
		w2 := newLeaseWorld()
		r2 := "aaaaaaaa-5555-4555-8555-555555555552"
		_, _ = w2.alice.store.AcceptCommandBatch(ctx, []state.Command{
			{ID: "c1", RunID: r2, Kind: state.CommandStart, Text: "Routing task"},
		}, nil)
		parkLease(t, w2, w2.alice.user.ID, r2, docstore.WaitRoutingPending)
		requireViolation(t, w2.checker.CheckLeaseQuiescence(ctx, w2.targets, w2.clock.Now()), "quiescence.task_lease_stuck_routing_pending")

		// 3a. waiting_reason = question when Run.Question == nil
		w3 := newLeaseWorld()
		r3 := "aaaaaaaa-5555-4555-8555-555555555553"
		_, _ = w3.alice.store.AcceptCommandBatch(ctx, []state.Command{
			{ID: "c1", RunID: r3, Kind: state.CommandStart, Text: "No question"},
		}, nil)
		parkLease(t, w3, w3.alice.user.ID, r3, docstore.WaitQuestion)
		requireViolation(t, w3.checker.CheckLeaseQuiescence(ctx, w3.targets, w3.clock.Now()), "quiescence.task_lease_stuck_question")

		// 3b. waiting_reason = question when Run.Question.ResolvedBy != ""
		w4 := newLeaseWorld()
		r4 := "aaaaaaaa-5555-4555-8555-555555555554"
		now := w4.clock.Now()
		fields, _ := docstore.Encode(state.Run{
			ID:        r4,
			Status:    state.RunRunning,
			StartedAt: now,
			Question:  &state.Question{ID: "q1", Kind: "input", Text: "Q", ResolvedBy: "cmd-ans"},
		})
		_ = w4.backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "owners/" + w4.alice.user.DataOwner() + "/runs/" + r4,
			Fields: fields,
		})
		parkLease(t, w4, w4.alice.user.ID, r4, docstore.WaitQuestion)
		requireViolation(t, w4.checker.CheckLeaseQuiescence(ctx, w4.targets, w4.clock.Now()), "quiescence.task_lease_stuck_question")

		// 3c. waiting_reason = question when Run.CancelRequested == true
		w5 := newLeaseWorld()
		r5 := "aaaaaaaa-5555-4555-8555-555555555555"
		fields5, _ := docstore.Encode(state.Run{
			ID:              r5,
			Status:          state.RunRunning,
			StartedAt:       now,
			CancelRequested: true,
			Question:        &state.Question{ID: "q1", Kind: "input", Text: "Q"},
		})
		_ = w5.backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "owners/" + w5.alice.user.DataOwner() + "/runs/" + r5,
			Fields: fields5,
		})
		parkLease(t, w5, w5.alice.user.ID, r5, docstore.WaitQuestion)
		requireViolation(t, w5.checker.CheckLeaseQuiescence(ctx, w5.targets, w5.clock.Now()), "quiescence.task_lease_stuck_question")

		// 4. waiting_reason = finishing when all children are terminal & clean
		w6 := newLeaseWorld()
		parentID := "aaaaaaaa-5555-4555-8555-555555555556"
		childID := "aaaaaaaa-5555-4555-8555-555555555557"
		parentFields, _ := docstore.Encode(state.Run{
			ID:        parentID,
			Status:    state.RunRunning,
			StartedAt: now,
			Finishing: &state.PublishRequest{RunID: parentID, Status: state.RunCompleted},
			Subtasks:  []state.SubtaskLink{{ID: childID, Status: state.RunCompleted}},
		})
		childFields, _ := docstore.Encode(state.Run{
			ID:         childID,
			ParentID:   parentID,
			Status:     state.RunCompleted,
			StartedAt:  now,
			FinishedAt: &now,
		})
		_ = w6.backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "owners/" + w6.alice.user.DataOwner() + "/runs/" + parentID,
			Fields: parentFields,
		})
		_ = w6.backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "owners/" + w6.alice.user.DataOwner() + "/runs/" + childID,
			Fields: childFields,
		})
		parkLease(t, w6, w6.alice.user.ID, parentID, docstore.WaitFinishing)
		requireViolation(t, w6.checker.CheckLeaseQuiescence(ctx, w6.targets, w6.clock.Now()), "quiescence.task_lease_stuck_finishing")
	})

	t.Run("user_wake_leaked_undrained_and_missing_due_wakes", func(t *testing.T) {
		// 1. Undrained runnable user wake + leaked owner.
		w := newLeaseWorld()
		_ = w.backend.WakeUser(ctx, w.alice.user.ID, docstore.LaneConversation, w.clock.Now())
		requireViolation(t, w.checker.CheckLeaseQuiescence(ctx, w.targets, w.clock.Now()), "quiescence.user_wake_undrained_runnable")

		if _, ok, err := w.backend.ClaimUserWake(ctx, docstore.LaneConversation, "leaked-outer", 30*time.Second); err != nil || !ok {
			t.Fatalf("ClaimUserWake: ok=%v err=%v", ok, err)
		}
		err := w.checker.CheckLeaseQuiescence(ctx, w.targets, w.clock.Now())
		requireViolation(t, err, "quiescence.user_wake_leaked_owner")
		requireViolation(t, err, "quiescence.user_wake_undrained_runnable")

		// 2. Missing (parked) conversation wake when conv.Pending() == true.
		w2 := newLeaseWorld()
		if err := w2.alice.store.AcceptConversationInput(ctx, "msg-pending", "hello", nil); err != nil {
			t.Fatalf("AcceptConversationInput: %v", err)
		}
		if cw, ok, err := w2.backend.ClaimUserWake(ctx, docstore.LaneConversation, "w-missing", 30*time.Second); err != nil || !ok {
			t.Fatalf("ClaimUserWake: ok=%v err=%v", ok, err)
		} else if parked, err := w2.backend.ParkUserWake(ctx, w2.alice.user.ID, docstore.LaneConversation, "w-missing", cw.WakeSeq); err != nil || !parked {
			t.Fatalf("ParkUserWake: parked=%v err=%v", parked, err)
		}
		requireViolation(t, w2.checker.CheckLeaseQuiescence(ctx, w2.targets, w2.clock.Now()), "quiescence.user_wake_missing_conversation")

		// 3. Missing scheduler wake when enabled automation is due (NextAt <= now).
		w3 := newLeaseWorld()
		dueAt := w3.clock.Now().Add(time.Minute)
		if _, err := w3.alice.sched.Save(ctx, "", "req-due-1", 0, scheduler.Editable{
			Title:                  "Due Job",
			Instruction:            "Do work",
			Trigger:                scheduler.Trigger{Kind: "once", At: &dueAt},
			BudgetUSD:              1.0,
			MaxActiveRuns:          1,
			MissedRun:              "latest",
			ExecutionWindowSeconds: 3600,
			DestinationIDs:         []string{scheduler.InAppDestination},
		}); err != nil {
			t.Fatalf("sched.Save: %v", err)
		}
		w3.clock.Advance(2 * time.Minute)
		requireViolation(t, w3.checker.CheckLeaseQuiescence(ctx, w3.targets, w3.clock.Now()), "quiescence.user_wake_missing_scheduler")

		// 4. Unknown user IDs in leases and wakes.
		w4 := newLeaseWorld()
		_ = w4.backend.WakeTask(ctx, "ghost-user", "ghost-run", w4.clock.Now().Add(time.Hour))
		_ = w4.backend.WakeUser(ctx, "ghost-user", docstore.LaneScheduler, w4.clock.Now().Add(time.Hour))
		err = w4.checker.CheckLeaseQuiescence(ctx, w4.targets, w4.clock.Now())
		requireViolation(t, err, "quiescence.lease_unknown_user")
		requireViolation(t, err, "quiescence.user_wake_unknown_user")
	})

	t.Run("check_quiescence_runs_lease_check_when_config_enabled", func(t *testing.T) {
		w := newLeaseWorld()
		_ = w.backend.WakeTask(ctx, w.alice.user.ID, "orphan-run", w.clock.Now().Add(time.Hour))

		// Default Config{CheckLeases: false} skips lease quiescence inside CheckQuiescence.
		if err := w.checker.CheckQuiescence(ctx, w.targets, w.clock.Now()); err != nil {
			t.Fatalf("expected default CheckLeases=false to skip lease quiescence, got: %v", err)
		}

		// Config{CheckLeases: true} runs CheckLeaseQuiescence automatically inside CheckQuiescence.
		leaseChecker := invariants.New(invariants.Config{
			Backend:     w.backend,
			Now:         w.clock.Now,
			CheckLeases: true,
		})
		requireViolation(t, leaseChecker.CheckQuiescence(ctx, w.targets, w.clock.Now()), "quiescence.lease_orphan_run")
	})

	t.Run("invalid_empty_waiting_reason_and_parent_waiting_on_subtasks", func(t *testing.T) {
		// Parked with empty or unknown waiting_reason must be flagged.
		w := newLeaseWorld()
		rBad := "aaaaaaaa-6666-4666-8666-666666666661"
		_, err := w.alice.store.AcceptCommandBatch(ctx, []state.Command{
			{ID: "c-bad-reason", RunID: rBad, Kind: state.CommandStart, Text: "Bad wait reason"},
		}, nil)
		if err != nil {
			t.Fatalf("start rBad: %v", err)
		}
		parkLease(t, w, w.alice.user.ID, rBad, "")
		requireViolation(t, w.checker.CheckLeaseQuiescence(ctx, w.targets, w.clock.Now()), "quiescence.task_lease_invalid_waiting_reason")

		// Parent parked with WaitQuestion on a resolved QuestionWork while child subtask is still running is valid.
		w2 := newLeaseWorld()
		now := w2.clock.Now()
		parentID := "aaaaaaaa-6666-4666-8666-666666666662"
		childID := "aaaaaaaa-6666-4666-8666-666666666663"
		parentFields, _ := docstore.Encode(state.Run{
			ID:        parentID,
			Status:    state.RunRunning,
			Revision:  1,
			Question:  &state.Question{ID: "wq-1", Kind: state.QuestionWork, Text: "delegate", Revision: 1, ResolvedBy: "cmd-sub-1"},
			Subtasks:  []state.SubtaskLink{{ID: childID, QuestionID: "wq-1", Status: state.RunRunning}},
			StartedAt: now,
		})
		childFields, _ := docstore.Encode(state.Run{
			ID:        childID,
			ParentID:  parentID,
			Status:    state.RunRunning,
			Revision:  1,
			Question:  &state.Question{ID: "q-child", Kind: "input", Text: "child needs input", Revision: 1},
			StartedAt: now,
		})
		_ = w2.backend.Apply(ctx, docstore.Mutation{Kind: "set", Path: "owners/" + w2.alice.user.DataOwner() + "/runs/" + parentID, Fields: parentFields})
		_ = w2.backend.Apply(ctx, docstore.Mutation{Kind: "set", Path: "owners/" + w2.alice.user.DataOwner() + "/runs/" + childID, Fields: childFields})
		parkLease(t, w2, w2.alice.user.ID, parentID, docstore.WaitQuestion)
		parkLease(t, w2, w2.alice.user.ID, childID, docstore.WaitQuestion)
		if err := w2.checker.CheckLeaseQuiescence(ctx, w2.targets, w2.clock.Now()); err != nil {
			t.Fatalf("parent waiting on running subtask should pass quiescence, got: %v", err)
		}
	})

	t.Run("safety_subtask_rollup_and_task_control_fence_owner_and_watermarks", func(t *testing.T) {
		w := newLeaseWorld()
		now := w.clock.Now()
		parentID := "aaaaaaaa-7777-4777-8777-777777777771"
		childID := "aaaaaaaa-7777-4777-8777-777777777772"
		parentFields, _ := docstore.Encode(state.Run{
			ID:           parentID,
			Status:       state.RunRunning,
			StartedAt:    now,
			Subtasks:     []state.SubtaskLink{{ID: childID, Status: state.RunRunning}},
			SubtaskSpent: 0.50, // drifted / double-counted vs child.Spent=0.25
		})
		childFields, _ := docstore.Encode(state.Run{
			ID:          childID,
			ParentID:    parentID,
			Status:      state.RunRunning,
			StartedAt:   now,
			Spent:       0.25,
			CostUnknown: true,
		})
		_ = w.backend.Apply(ctx, docstore.Mutation{Kind: "set", Path: "owners/" + w.alice.user.DataOwner() + "/runs/" + parentID, Fields: parentFields})
		_ = w.backend.Apply(ctx, docstore.Mutation{Kind: "set", Path: "owners/" + w.alice.user.DataOwner() + "/runs/" + childID, Fields: childFields})
		err := w.checker.CheckSafetyAt(ctx, w.targets, now)
		requireViolation(t, err, "subtask.spent_rollup_consistent")
		requireViolation(t, err, "subtask.cost_unknown_rollup_consistent")

		// TaskControl: same fence with conflicting owner, plus Position/Revision regression.
		w2 := newLeaseWorld()
		rID := "aaaaaaaa-7777-4777-8777-777777777773"
		tcPath := "owners/" + w2.alice.user.DataOwner() + "/task_controls/" + rID
		tc1, _ := docstore.Encode(state.TaskControl{RunID: rID, Phase: state.TaskPhaseSync, Owner: "worker-a", Fence: 2, LeaseUntil: now.Add(30 * time.Second), Position: 3, Revision: 2})
		_ = w2.backend.Apply(ctx, docstore.Mutation{Kind: "set", Path: tcPath, Fields: tc1})
		if err := w2.checker.CheckSafetyAt(ctx, w2.targets, now); err != nil {
			t.Fatalf("baseline CheckSafetyAt: %v", err)
		}
		tc2, _ := docstore.Encode(state.TaskControl{RunID: rID, Phase: state.TaskPhaseSync, Owner: "worker-b", Fence: 2, LeaseUntil: now.Add(30 * time.Second), Position: 2, Revision: 1})
		_ = w2.backend.Apply(ctx, docstore.Mutation{Kind: "set", Path: tcPath, Fields: tc2})
		err = w2.checker.CheckSafetyAt(ctx, w2.targets, now)
		requireViolation(t, err, "task_control.fence_owner_unique")
		requireViolation(t, err, "task_control.position_monotonic")
		requireViolation(t, err, "task_control.revision_monotonic")
	})

	t.Run("legacy_id_distinct_data_owner_lease_parity", func(t *testing.T) {
		clock := newFakeClock()
		backend := memstore.New(memstore.WithClock(clock.Now))
		arts := memstore.NewArtifacts()
		legacyUser := users.User{
			ID:          users.LegacyID,
			Email:       "legacy-owner@example.com",
			DisplayName: "Legacy Owner",
			TimeZone:    "UTC",
		}
		if legacyUser.ID == legacyUser.DataOwner() {
			t.Fatalf("expected LegacyID (%q) != DataOwner (%q)", legacyUser.ID, legacyUser.DataOwner())
		}
		db := records.NewShared(backend, legacyUser, 5.0)
		db.Now = clock.Now
		store := state.Compose(db, arts)
		perms := &permissions.Service{Store: store, Now: clock.Now}
		sched := &scheduler.Service{Store: store, Tasks: store, Permissions: perms, Now: clock.Now}
		targets := []invariants.UserTarget{{User: legacyUser, Store: store, Permissions: perms, Scheduler: sched}}
		checker := invariants.New(invariants.Config{Backend: backend, Now: clock.Now, CheckLeases: true})

		runID := "aaaaaaaa-8888-4888-8888-888888888881"
		if _, err := store.AcceptCommandBatch(ctx, []state.Command{
			{ID: "cmd-legacy-1", RunID: runID, Kind: state.CommandStart, Text: "Legacy task"},
		}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}
		deliverPending(t, ctx, store)
		if err := store.Incorporate(ctx, state.IncorporateRequest{RunID: runID, Revision: 1, Position: 1}); err != nil {
			t.Fatalf("Incorporate: %v", err)
		}
		if err := store.ProjectQuestion(ctx, runID, 1, state.Question{
			ID: "q-legacy", Kind: "input", Text: "Need input", Revision: 1,
		}); err != nil {
			t.Fatalf("ProjectQuestion: %v", err)
		}
		// Claim and park the lease keyed by legacyUser.ID ("owner"), NOT DataOwner ("legacy-owner@example.com").
		claim, ok, err := backend.ClaimTaskByID(ctx, legacyUser.ID, runID, "worker-legacy", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID(%q): ok=%v err=%v", legacyUser.ID, ok, err)
		}
		if parked, err := backend.ParkTask(ctx, legacyUser.ID, runID, "worker-legacy", claim.WakeSeq, docstore.WaitQuestion); err != nil || !parked {
			t.Fatalf("ParkTask: parked=%v err=%v", parked, err)
		}
		// Drain conversation wake emitted by ProjectQuestion.
		for {
			ctrl, turn, err := store.AcquireConversation(ctx, "worker-legacy", "turn-"+clock.Now().Format("150405"))
			if err != nil {
				t.Fatalf("AcquireConversation: %v", err)
			}
			if turn == nil {
				break
			}
			fence := state.ConversationFence{Owner: ctrl.Owner, Fence: ctrl.Fence, TurnID: turn.ID, Through: turn.Through, UserThrough: turn.UserThrough, Frozen: turn.Frozen}
			turn.Attempt = state.ConversationAttempt{ID: "att-" + turn.ID, TurnID: turn.ID, Number: 1, Status: "completed"}
			clock.Advance(time.Second)
			if err := store.PublishConversation(ctx, fence, *turn, "Noted."); err != nil {
				t.Fatalf("PublishConversation: %v", err)
			}
		}
		if cw, ok, err := backend.ClaimUserWake(ctx, docstore.LaneConversation, "worker-legacy", state.TaskLease); err == nil && ok {
			_, _ = backend.ParkUserWake(ctx, legacyUser.ID, docstore.LaneConversation, "worker-legacy", cw.WakeSeq)
		}
		if err := checker.CheckSafetyAt(ctx, targets, clock.Now()); err != nil {
			t.Fatalf("CheckSafetyAt legacy user failed: %v", err)
		}
		if err := checker.CheckQuiescence(ctx, targets, clock.Now()); err != nil {
			t.Fatalf("CheckQuiescence legacy user failed: %v", err)
		}
	})
}

type failGetTaskControlStore struct {
	state.ConversationStore
	err error
}

func (s failGetTaskControlStore) GetTaskControl(context.Context, string) (state.TaskControl, error) {
	return state.TaskControl{}, s.err
}

func TestControlDecodeViolationAndStoreGetTaskControlError(t *testing.T) {
	ctx := context.Background()

	t.Run("corrupt_system_control_surfaces_violation", func(t *testing.T) {
		clock := newFakeClock()
		backend := memstore.New(memstore.WithClock(clock.Now))
		arts := memstore.NewArtifacts()
		u := users.User{ID: "11111111-aaaa-4aaa-8aaa-111111111111", Email: "ctrl@example.com", DisplayName: "Ctrl", TimeZone: "UTC"}
		db := records.NewShared(backend, u, 5.0)
		db.Now = clock.Now
		store := state.Compose(db, arts)
		perms := &permissions.Service{Store: store, Now: clock.Now}
		sched := &scheduler.Service{Store: store, Tasks: store, Permissions: perms, Now: clock.Now}
		targets := []invariants.UserTarget{{User: u, Store: store, Permissions: perms, Scheduler: sched}}

		// Corrupt system/control with a wrong field type so docstore.Decode fails.
		if err := backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "system/control",
			Fields: map[string]any{"paused": "not-a-bool"},
		}); err != nil {
			t.Fatalf("corrupt system/control: %v", err)
		}

		checker := invariants.New(invariants.Config{Backend: backend, Now: clock.Now, CheckLeases: true})
		err := checker.CheckSafetyAt(ctx, targets, clock.Now())
		requireViolation(t, err, "control.decode")

		qErr := checker.CheckLeaseQuiescence(ctx, targets, clock.Now())
		requireViolation(t, qErr, "control.decode")

		// OnExecutionStart must also record a sticky control.decode violation even if control is later repaired.
		hookChecker := invariants.New(invariants.Config{Backend: backend, Now: clock.Now})
		hookChecker.OnExecutionStart(u.ID, "run-1")
		if err := backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "system/control",
			Fields: map[string]any{"paused": false},
		}); err != nil {
			t.Fatalf("repair system/control: %v", err)
		}
		err = hookChecker.CheckSafetyAt(ctx, targets, clock.Now())
		requireViolation(t, err, "control.decode")
	})

	t.Run("store_mode_get_task_control_errors_surface_except_not_found", func(t *testing.T) {
		clock := newFakeClock()
		backend := memstore.New(memstore.WithClock(clock.Now))
		arts := memstore.NewArtifacts()
		u := users.User{ID: "22222222-bbbb-4bbb-8bbb-222222222222", Email: "tc@example.com", DisplayName: "TC", TimeZone: "UTC"}
		db := records.NewShared(backend, u, 5.0)
		db.Now = clock.Now
		baseStore := state.Compose(db, arts)
		perms := &permissions.Service{Store: baseStore, Now: clock.Now}
		sched := &scheduler.Service{Store: baseStore, Tasks: baseStore, Permissions: perms, Now: clock.Now}

		runID := "aaaaaaaa-9999-4999-9999-999999999991"
		if _, err := baseStore.AcceptCommandBatch(ctx, []state.Command{
			{ID: "cmd-tc-1", RunID: runID, Kind: state.CommandStart, Text: "Task without control"},
		}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}

		// Missing TaskControl (state.ErrNotFound) is expected for newly accepted runs and must not fail CheckSafety.
		okChecker := invariants.New(invariants.Config{Now: clock.Now})
		if err := okChecker.CheckSafetyAt(ctx, []invariants.UserTarget{{
			User: u, Store: baseStore, Permissions: perms, Scheduler: sched,
		}}, clock.Now()); err != nil {
			t.Fatalf("CheckSafetyAt with missing TaskControl failed: %v", err)
		}

		// Unexpected error from GetTaskControl must surface as a store.read violation rather than being swallowed.
		boom := errors.New("corrupt task control")
		errChecker := invariants.New(invariants.Config{Now: clock.Now})
		err := errChecker.CheckSafetyAt(ctx, []invariants.UserTarget{{
			User:        u,
			Store:       failGetTaskControlStore{ConversationStore: baseStore, err: boom},
			Permissions: perms,
			Scheduler:   sched,
		}}, clock.Now())
		requireViolation(t, err, "store.read")
		if !strings.Contains(err.Error(), "GetTaskControl") || !strings.Contains(err.Error(), boom.Error()) {
			t.Fatalf("expected store.read violation to include GetTaskControl error, got: %v", err)
		}
	})
}

func TestMemoryInvariants_INV_MEM_01_to_08(t *testing.T) {
	ctx := context.Background()

	seedUserInput := func(t *testing.T, w *testWorld, u *testUserEnv, id, text string) time.Time {
		t.Helper()
		w.clock.Advance(time.Second)
		if err := u.store.AcceptConversationInput(ctx, id, text, nil); err != nil {
			t.Fatalf("AcceptConversationInput(%s): %v", id, err)
		}
		return w.clock.Now().UTC()
	}

	writeDoc := func(t *testing.T, w *testWorld, path string, val any) {
		t.Helper()
		fields, err := docstore.Encode(val)
		if err != nil {
			t.Fatalf("docstore.Encode(%s): %v", path, err)
		}
		if err := w.backend.Apply(ctx, docstore.Mutation{Kind: "set", Path: path, Fields: fields}); err != nil {
			t.Fatalf("backend.Apply(%s): %v", path, err)
		}
	}

	t.Run("positive_coupled_memory_lifecycle", func(t *testing.T) {
		w := newTestWorld()
		owner := w.alice.user.DataOwner()

		// 1. User message + explicit UI create & pin + manual demote of pinned.
		in1 := "11111111-1111-4111-8111-000000000001"
		ts1 := seedUserInput(t, w, w.alice, in1, "Remember I prefer dark roast coffee and live in Seattle.")
		pinned := true
		rCreate, err := w.alice.store.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-create-pin-1",
			Kind:      state.MemoryMutateCreateCore,
			TargetID:  "mem-pinned-1",
			Text:      "User prefers dark roast coffee.",
			About:     "the user",
			Section:   "Preferences",
			Pinned:    &pinned,
		})
		if err != nil {
			t.Fatalf("MutateMemory create_core: %v", err)
		}
		var createRes state.MemoryMutationResult
		if err := json.Unmarshal(rCreate.Resource, &createRes); err != nil || createRes.Memory == nil {
			t.Fatalf("unmarshal create_core: %v", err)
		}
		if err := w.checker.CheckSafetyAt(ctx, w.targets, w.clock.Now()); err != nil {
			t.Fatalf("CheckSafetyAt after create_core: %v", err)
		}

		// Manual UI demotion of a pinned memory is permitted because a manual receipt exists.
		w.clock.Advance(time.Second)
		if _, err := w.alice.store.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID:        "req-demote-pin-1",
			ExpectedRevision: createRes.Memory.UpdatedAt.UnixMilli(),
			Kind:             state.MemoryMutateDemoteCore,
			TargetID:         createRes.Memory.ID,
		}); err != nil {
			t.Fatalf("MutateMemory demote_core: %v", err)
		}
		if err := w.checker.CheckSafetyAt(ctx, w.targets, w.clock.Now()); err != nil {
			t.Fatalf("CheckSafetyAt after manual demote of pinned memory: %v", err)
		}

		// 2. Coupled flush+compact with verified Core upsert, Daily Log entry, and SummaryItem.
		item1 := state.SummaryItem{
			ID:      "si_clean_1",
			Kind:    "preference",
			Quote:   "dark roast coffee",
			InputID: in1,
			Note:    "coffee preference",
		}
		now := w.clock.Now().UTC()
		sum1, err := w.alice.store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			NextSummary: state.ConversationSummary{
				FlushedThrough: ts1,
				Through:        ts1,
				Items:          []state.SummaryItem{item1},
				Gist:           "User discussed coffee preference.",
				Turns:          1,
				Model:          "sim-compaction",
				UpdatedAt:      now,
			},
			CoreUpserts: []state.Memory{{
				ID:            "mem-flush-1",
				Text:          "User lives in Seattle.",
				About:         "the user",
				Section:       "Context",
				Provenance:    state.ProvenancePreCompactFlush,
				SourceInputID: in1,
				SourceText:    "Remember I prefer dark roast coffee and live in Seattle.",
				CreatedAt:     now,
				UpdatedAt:     now,
			}},
			DailyLogDate: now.Format("2006-01-02"),
			DailyLogEntries: []state.MemoryLogEntry{{
				ID:            "log-flush-1",
				Section:       "Context",
				Text:          "Mentioned Seattle residency.",
				About:         "the user",
				Provenance:    state.ProvenancePreCompactFlush,
				SourceInputID: in1,
				CreatedAt:     now,
				UpdatedAt:     now,
			}},
		})
		if err != nil {
			t.Fatalf("CommitFlushAndCompact: %v", err)
		}
		if err := w.checker.CheckSafetyAt(ctx, w.targets, w.clock.Now()); err != nil {
			t.Fatalf("CheckSafetyAt after CommitFlushAndCompact: %v", err)
		}

		// 3. Dismiss summary item via MutateMemory -> INV-MEM-02 permits removal.
		w.clock.Advance(time.Second)
		if _, err := w.alice.store.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID:        "req-dismiss-1",
			ExpectedRevision: sum1.UpdatedAt.UnixMilli(),
			Kind:             state.MemoryMutateDismissWorking,
			TargetID:         item1.ID,
		}); err != nil {
			t.Fatalf("MutateMemory dismiss_working: %v", err)
		}
		if err := w.checker.CheckSafetyAt(ctx, w.targets, w.clock.Now()); err != nil {
			t.Fatalf("CheckSafetyAt after dismiss_working: %v", err)
		}

		// 4. Verify active future wake with FlushedThrough < Through does NOT false-positive CheckQuiescence.
		_ = owner
		if err := w.checker.CheckQuiescence(ctx, w.targets, w.clock.Now()); err != nil {
			t.Fatalf("CheckQuiescence clean: %v", err)
		}
	})

	t.Run("INV_MEM_01_atomic_watermark_violations", func(t *testing.T) {
		// 1a. FlushedThrough > Through.
		w1 := newTestWorld()
		owner := w1.alice.user.DataOwner()
		t1 := w1.clock.Now().UTC()
		t2 := t1.Add(time.Minute)
		writeDoc(t, w1, "owners/"+owner+"/conversation/summary", state.ConversationSummary{
			From:           t1,
			FlushedThrough: t2,
			Through:        t1,
			Items:          []state.SummaryItem{},
			UpdatedAt:      t2,
		})
		requireViolation(t, w1.checker.CheckSafetyAt(ctx, w1.targets, w1.clock.Now()), "INV-MEM-01.watermark_bounds")

		// 1b. Monotonicity regression of FlushedThrough in same epoch.
		w2 := newTestWorld()
		owner2 := w2.alice.user.DataOwner()
		b1 := w2.clock.Now().UTC()
		b2 := b1.Add(time.Minute)
		b3 := b2.Add(time.Minute)
		writeDoc(t, w2, "owners/"+owner2+"/conversation/summary", state.ConversationSummary{
			From:           b1,
			FlushedThrough: b3,
			Through:        b3,
			Items:          []state.SummaryItem{},
			Model:          "sim",
			UpdatedAt:      b3,
		})
		if err := w2.checker.CheckSafetyAt(ctx, w2.targets, w2.clock.Now()); err != nil {
			t.Fatalf("baseline CheckSafetyAt: %v", err)
		}
		writeDoc(t, w2, "owners/"+owner2+"/conversation/summary", state.ConversationSummary{
			From:           b1,
			FlushedThrough: b2,
			Through:        b3,
			Items:          []state.SummaryItem{},
			Model:          "sim",
			UpdatedAt:      b3.Add(time.Second),
		})
		requireViolation(t, w2.checker.CheckSafetyAt(ctx, w2.targets, w2.clock.Now()), "INV-MEM-01.watermark_monotonic")

		// 1c. Completed turn in (FlushedThrough, Through] missing from Excerpts.
		w3 := newTestWorld()
		owner3 := w3.alice.user.DataOwner()
		inID := "11111111-1111-4111-8111-000000000010"
		tsIn := seedUserInput(t, w3, w3.alice, inID, "Important turn that must be in excerpts.")
		turnStart := tsIn.Add(time.Second)
		writeDoc(t, w3, "owners/"+owner3+"/conversation_turns/turn-1", state.ConversationTurn{
			ID:        "turn-1",
			Status:    "completed",
			StartedAt: turnStart,
			Inputs:    []state.ConversationInput{{ID: inID, Text: "Important turn that must be in excerpts.", CreatedAt: tsIn}},
		})
		writeDoc(t, w3, "owners/"+owner3+"/conversation/summary", state.ConversationSummary{
			From:           tsIn.Add(-time.Minute),
			FlushedThrough: tsIn,
			Through:        turnStart,
			Excerpts:       nil,
			Items:          []state.SummaryItem{},
			Model:          "extractive",
			UpdatedAt:      turnStart,
		})
		requireViolation(t, w3.checker.CheckSafetyAt(ctx, w3.targets, w3.clock.Now()), "INV-MEM-01.excerpt_coverage")

		// 1d. FlushedThrough advanced on failure (FlushFailures > 0 with FlushedThrough >= LastFailureThrough).
		w4 := newTestWorld()
		owner4 := w4.alice.user.DataOwner()
		f0 := w4.clock.Now().UTC()
		f1 := f0.Add(time.Minute)
		writeDoc(t, w4, "owners/"+owner4+"/conversation/summary", state.ConversationSummary{
			From:               f0,
			FlushedThrough:     f1,
			Through:            f1,
			FlushFailures:      1,
			LastFailureThrough: f1,
			Items:              []state.SummaryItem{},
			Model:              "sim",
			UpdatedAt:          f1,
		})
		requireViolation(t, w4.checker.CheckSafetyAt(ctx, w4.targets, w4.clock.Now()), "INV-MEM-01.failure_watermark_advance")
	})

	t.Run("INV_MEM_02_no_silent_summary_eviction", func(t *testing.T) {
		w := newTestWorld()
		owner := w.alice.user.DataOwner()
		inID := "11111111-1111-4111-8111-000000000020"
		ts := seedUserInput(t, w, w.alice, inID, "Keep this constraint active.")
		item := state.SummaryItem{
			ID:      "si_keep_1",
			Kind:    "constraint",
			Quote:   "Keep this constraint active.",
			InputID: inID,
		}
		writeDoc(t, w, "owners/"+owner+"/conversation/summary", state.ConversationSummary{
			From:           ts.Add(-time.Second),
			FlushedThrough: ts,
			Through:        ts,
			Items:          []state.SummaryItem{item},
			Model:          "sim",
			UpdatedAt:      ts,
		})
		if err := w.checker.CheckSafetyAt(ctx, w.targets, w.clock.Now()); err != nil {
			t.Fatalf("baseline CheckSafetyAt: %v", err)
		}

		// Silently drop item without advancing FlushedThrough, dismissing, or archiving summary_evicted.
		writeDoc(t, w, "owners/"+owner+"/conversation/summary", state.ConversationSummary{
			From:           ts.Add(-time.Second),
			FlushedThrough: ts,
			Through:        ts,
			Items:          []state.SummaryItem{},
			Model:          "sim",
			UpdatedAt:      ts.Add(time.Second),
		})
		requireViolation(t, w.checker.CheckSafetyAt(ctx, w.targets, w.clock.Now()), "INV-MEM-02.silent_eviction")

		// Dismissed item reappearing in Items must also trigger INV-MEM-02.
		w2 := newTestWorld()
		owner2 := w2.alice.user.DataOwner()
		inID2 := "11111111-1111-4111-8111-000000000021"
		ts2 := seedUserInput(t, w2, w2.alice, inID2, "Dismissed quote.")
		item2 := state.SummaryItem{ID: "si_dis_1", Kind: "fact", Quote: "Dismissed quote.", InputID: inID2}
		writeDoc(t, w2, "owners/"+owner2+"/conversation/summary", state.ConversationSummary{
			From:             ts2.Add(-time.Second),
			FlushedThrough:   ts2,
			Through:          ts2,
			Items:            []state.SummaryItem{item2},
			DismissedItemIDs: []string{item2.ID},
			Model:            "sim",
			UpdatedAt:        ts2,
		})
		requireViolation(t, w2.checker.CheckSafetyAt(ctx, w2.targets, w2.clock.Now()), "INV-MEM-02.dismissed_reappeared")
	})

	t.Run("INV_MEM_03_core_budget_bounds_and_pinning", func(t *testing.T) {
		// 3a. Core total > 24 KiB.
		w1 := newTestWorld()
		owner1 := w1.alice.user.DataOwner()
		now := w1.clock.Now().UTC()
		bigText := strings.Repeat("a", 1900)
		for i := range 14 {
			id := "mem-over-" + string(rune('a'+i))
			m := state.Memory{
				ID: id, Text: bigText, About: "the user", Provenance: state.ProvenanceUserExplicit,
				SourceInputID: "ui", CreatedAt: now, UpdatedAt: now,
			}
			writeDoc(t, w1, "owners/"+owner1+"/memories/"+id, m)
			_ = w1.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
				Owner: owner1, ChunkID: docstore.MemoryCoreChunkID(id), Layer: docstore.MemoryLayerCore,
				About: m.About, Body: m.Text, Provenance: string(m.Provenance), SourceInput: "ui",
				CreatedAt: now, UpdatedAt: now,
			})
		}
		requireViolation(t, w1.checker.CheckSafetyAt(ctx, w1.targets, w1.clock.Now()), "INV-MEM-03.core_budget")

		// 3b. Item > MaxMemoryText (2000B) & CreatedAt > UpdatedAt.
		w2 := newTestWorld()
		owner2 := w2.alice.user.DataOwner()
		now2 := w2.clock.Now().UTC()
		badMem := state.Memory{
			ID: "mem-bad-1", Text: strings.Repeat("x", state.MaxMemoryText+1), About: "the user",
			Provenance: state.ProvenanceUserExplicit, SourceInputID: "ui",
			CreatedAt: now2.Add(time.Minute), UpdatedAt: now2,
		}
		writeDoc(t, w2, "owners/"+owner2+"/memories/mem-bad-1", badMem)
		_ = w2.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: owner2, ChunkID: docstore.MemoryCoreChunkID(badMem.ID), Layer: docstore.MemoryLayerCore,
			About: badMem.About, Body: badMem.Text, Provenance: string(badMem.Provenance), SourceInput: "ui",
			CreatedAt: badMem.CreatedAt, UpdatedAt: badMem.CreatedAt,
		})
		err := w2.checker.CheckSafetyAt(ctx, w2.targets, w2.clock.Now())
		requireViolation(t, err, "INV-MEM-03.item_bounds")
		requireViolation(t, err, "INV-MEM-03.timestamps")

		// 3c. Pinned Core item auto-demoted without manual receipt.
		w3 := newTestWorld()
		owner3 := w3.alice.user.DataOwner()
		pinned := true
		if _, err := w3.alice.store.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-pin-guard-1",
			Kind:      state.MemoryMutateCreateCore,
			TargetID:  "mem-pinned-guard",
			Text:      "Never auto-demote this pinned preference.",
			About:     "the user",
			Pinned:    &pinned,
		}); err != nil {
			t.Fatalf("MutateMemory: %v", err)
		}
		if err := w3.checker.CheckSafetyAt(ctx, w3.targets, w3.clock.Now()); err != nil {
			t.Fatalf("baseline CheckSafetyAt: %v", err)
		}
		// Simulate buggy background pass deleting pinned Core doc and writing demote_<id> log.
		_ = w3.backend.Apply(ctx, docstore.Mutation{Kind: "delete", Path: "owners/" + owner3 + "/memories/mem-pinned-guard"})
		_ = w3.backend.DeleteMemoryChunk(ctx, owner3, docstore.MemoryCoreChunkID("mem-pinned-guard"))
		now3 := w3.clock.Now().UTC()
		date3 := now3.Format("2006-01-02")
		entry := state.MemoryLogEntry{
			ID: "demote_mem-pinned-guard", Section: "Archived", Text: "Never auto-demote this pinned preference.",
			About: "the user", Provenance: state.ProvenanceUserExplicit, SourceInputID: "ui",
			CreatedAt: now3, UpdatedAt: now3,
		}
		writeDoc(t, w3, "owners/"+owner3+"/memory_logs/"+date3, state.MemoryLog{
			Date: date3, Shard: 1, Entries: []state.MemoryLogEntry{entry}, UpdatedAt: now3,
		})
		_ = w3.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: owner3, ChunkID: docstore.MemoryLogChunkID(date3, entry.ID), Layer: docstore.MemoryLayerLog,
			LogDate: date3, Section: entry.Section, About: entry.About, Body: entry.Text,
			Provenance: string(entry.Provenance), SourceInput: entry.SourceInputID, CreatedAt: now3, UpdatedAt: now3,
		})
		requireViolation(t, w3.checker.CheckSafetyAt(ctx, w3.targets, w3.clock.Now()), "INV-MEM-03.pinned_auto_demoted")
	})

	t.Run("INV_MEM_04_provenance_and_injection_isolation", func(t *testing.T) {
		// 4a. task_outcome in Core memories.
		w1 := newTestWorld()
		owner1 := w1.alice.user.DataOwner()
		now := w1.clock.Now().UTC()
		m := state.Memory{
			ID: "mem-inj-1", Text: "Injected from untrusted task output.", About: "the user",
			Provenance: state.ProvenanceTaskOutcome, CreatedAt: now, UpdatedAt: now,
		}
		writeDoc(t, w1, "owners/"+owner1+"/memories/"+m.ID, m)
		_ = w1.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: owner1, ChunkID: docstore.MemoryCoreChunkID(m.ID), Layer: docstore.MemoryLayerCore,
			About: m.About, Body: m.Text, Provenance: string(m.Provenance), CreatedAt: now, UpdatedAt: now,
		})
		requireViolation(t, w1.checker.CheckSafetyAt(ctx, w1.targets, w1.clock.Now()), "INV-MEM-04.task_outcome_in_core")

		// 4b. pre_compact_flush Core memory citing fabricated / non-existent input_id.
		w2 := newTestWorld()
		owner2 := w2.alice.user.DataOwner()
		now2 := w2.clock.Now().UTC()
		m2 := state.Memory{
			ID: "mem-fake-src", Text: "Fabricated citation.", About: "the user",
			Provenance: state.ProvenancePreCompactFlush, SourceInputID: "non-existent-input",
			CreatedAt: now2, UpdatedAt: now2,
		}
		writeDoc(t, w2, "owners/"+owner2+"/memories/"+m2.ID, m2)
		_ = w2.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: owner2, ChunkID: docstore.MemoryCoreChunkID(m2.ID), Layer: docstore.MemoryLayerCore,
			About: m2.About, Body: m2.Text, Provenance: string(m2.Provenance), SourceInput: m2.SourceInputID,
			CreatedAt: now2, UpdatedAt: now2,
		})
		requireViolation(t, w2.checker.CheckSafetyAt(ctx, w2.targets, w2.clock.Now()), "INV-MEM-04.unverified_source_input")

		// 4c. user_explicit Core memory citing a task-event (non-user) conversation_input.
		w3 := newTestWorld()
		owner3 := w3.alice.user.DataOwner()
		now3 := w3.clock.Now().UTC()
		writeDoc(t, w3, "owners/"+owner3+"/conversation_inputs/ev-1", state.ConversationInput{
			ID: "ev-1", Position: 1, CreatedAt: now3,
			Event: &state.TaskEvent{ID: "ev-1", RunID: "run-1", Kind: "completed", Text: "Task finished"},
		})
		m3 := state.Memory{
			ID: "mem-task-src", Text: "Sneaky task event memory.", About: "the user",
			Provenance: state.ProvenanceUserExplicit, SourceInputID: "ev-1",
			CreatedAt: now3, UpdatedAt: now3,
		}
		writeDoc(t, w3, "owners/"+owner3+"/memories/"+m3.ID, m3)
		_ = w3.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: owner3, ChunkID: docstore.MemoryCoreChunkID(m3.ID), Layer: docstore.MemoryLayerCore,
			About: m3.About, Body: m3.Text, Provenance: string(m3.Provenance), SourceInput: m3.SourceInputID,
			CreatedAt: now3, UpdatedAt: now3,
		})
		requireViolation(t, w3.checker.CheckSafetyAt(ctx, w3.targets, w3.clock.Now()), "INV-MEM-04.unverified_source_input")
	})

	t.Run("INV_MEM_05_turn_non_application_and_duplicates", func(t *testing.T) {
		// 5a. Superseded attempt left its new MemoryRemember in store.
		w1 := newTestWorld()
		owner1 := w1.alice.user.DataOwner()
		inID := "11111111-1111-4111-8111-000000000050"
		ts := seedUserInput(t, w1, w1.alice, inID, "Remember tea.")
		fin := ts.Add(time.Second)
		writeDoc(t, w1, "owners/"+owner1+"/conversation_attempts/t1-attempt-1", state.ConversationAttempt{
			ID: "t1-attempt-1", TurnID: "t1", Number: 1, Status: "superseded", StartedAt: ts, FinishedAt: &fin,
			Memory: []state.MemoryChange{{Kind: state.MemoryRemember, ID: "mem-superseded-1", Text: "Likes tea"}},
		})
		m := state.Memory{
			ID: "mem-superseded-1", Text: "Likes tea", About: "the user",
			Provenance: state.ProvenanceUserExplicit, SourceInputID: inID, CreatedAt: ts, UpdatedAt: ts,
		}
		writeDoc(t, w1, "owners/"+owner1+"/memories/"+m.ID, m)
		_ = w1.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: owner1, ChunkID: docstore.MemoryCoreChunkID(m.ID), Layer: docstore.MemoryLayerCore,
			About: m.About, Body: m.Text, Provenance: string(m.Provenance), SourceInput: m.SourceInputID,
			CreatedAt: ts, UpdatedAt: ts,
		})
		requireViolation(t, w1.checker.CheckSafetyAt(ctx, w1.targets, w1.clock.Now()), "INV-MEM-05.uncompleted_attempt_applied")

		// 5b. Duplicate Core memory for same (Provenance, SourceInputID, Text) (checkpoint replay duplicate).
		w2 := newTestWorld()
		owner2 := w2.alice.user.DataOwner()
		inID2 := "11111111-1111-4111-8111-000000000051"
		ts2 := seedUserInput(t, w2, w2.alice, inID2, "Remember green tea.")
		for _, id := range []string{"mem-dup-a", "mem-dup-b"} {
			dm := state.Memory{
				ID: id, Text: "Likes green tea", About: "the user",
				Provenance: state.ProvenanceUserExplicit, SourceInputID: inID2, CreatedAt: ts2, UpdatedAt: ts2,
			}
			writeDoc(t, w2, "owners/"+owner2+"/memories/"+id, dm)
			_ = w2.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
				Owner: owner2, ChunkID: docstore.MemoryCoreChunkID(id), Layer: docstore.MemoryLayerCore,
				About: dm.About, Body: dm.Text, Provenance: string(dm.Provenance), SourceInput: dm.SourceInputID,
				CreatedAt: ts2, UpdatedAt: ts2,
			})
		}
		requireViolation(t, w2.checker.CheckSafetyAt(ctx, w2.targets, w2.clock.Now()), "INV-MEM-05.duplicate_memory")

		// 5c. Duplicate Daily Log entry ID across shards of the same date.
		w3 := newTestWorld()
		owner3 := w3.alice.user.DataOwner()
		now3 := w3.clock.Now().UTC()
		date3 := now3.Format("2006-01-02")
		e := state.MemoryLogEntry{
			ID: "dup-entry-1", Section: "Context", Text: "Duplicate log entry",
			About: "the user", Provenance: state.ProvenancePreCompactFlush, CreatedAt: now3, UpdatedAt: now3,
		}
		writeDoc(t, w3, "owners/"+owner3+"/memory_logs/"+date3, state.MemoryLog{
			Date: date3, Shard: 1, Entries: []state.MemoryLogEntry{e}, UpdatedAt: now3,
		})
		writeDoc(t, w3, "owners/"+owner3+"/memory_logs/"+date3+"#2", state.MemoryLog{
			Date: date3, Shard: 2, Entries: []state.MemoryLogEntry{e}, UpdatedAt: now3,
		})
		_ = w3.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: owner3, ChunkID: docstore.MemoryLogChunkID(date3, e.ID), Layer: docstore.MemoryLayerLog,
			LogDate: date3, Section: e.Section, About: e.About, Body: e.Text,
			Provenance: string(e.Provenance), CreatedAt: now3, UpdatedAt: now3,
		})
		requireViolation(t, w3.checker.CheckSafetyAt(ctx, w3.targets, w3.clock.Now()), "INV-MEM-05.duplicate_log_entry")
	})

	t.Run("INV_MEM_06_store_fts_bijection", func(t *testing.T) {
		w := newTestWorld()
		owner := w.alice.user.DataOwner()
		if _, err := w.alice.store.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID: "req-biject-1",
			Kind:      state.MemoryMutateCreateCore,
			TargetID:  "mem-biject-1",
			Text:      "Bijection test fact.",
			About:     "the user",
		}); err != nil {
			t.Fatalf("MutateMemory: %v", err)
		}
		if err := w.checker.CheckSafetyAt(ctx, w.targets, w.clock.Now()); err != nil {
			t.Fatalf("baseline CheckSafetyAt: %v", err)
		}

		// 6a. Missing chunk in MemoryIndex.
		if err := w.backend.DeleteMemoryChunk(ctx, owner, docstore.MemoryCoreChunkID("mem-biject-1")); err != nil {
			t.Fatalf("DeleteMemoryChunk: %v", err)
		}
		requireViolation(t, w.checker.CheckSafetyAt(ctx, w.targets, w.clock.Now()), "INV-MEM-06.fts_missing_chunk")

		// 6b. Mismatched chunk body in MemoryIndex.
		now := w.clock.Now().UTC()
		if err := w.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: owner, ChunkID: docstore.MemoryCoreChunkID("mem-biject-1"), Layer: docstore.MemoryLayerCore,
			About: "the user", Body: "CORRUPTED BODY", Provenance: string(state.ProvenanceUserExplicit),
			SourceInput: "ui", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("UpsertMemoryChunk mismatch: %v", err)
		}
		requireViolation(t, w.checker.CheckSafetyAt(ctx, w.targets, w.clock.Now()), "INV-MEM-06.fts_chunk_mismatch")

		// 6c. Orphan chunk in MemoryIndex.
		if err := w.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: owner, ChunkID: docstore.MemoryCoreChunkID("ghost-id"), Layer: docstore.MemoryLayerCore,
			About: "the user", Body: "Ghost chunk", Provenance: string(state.ProvenanceUserExplicit),
			SourceInput: "ui", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("UpsertMemoryChunk orphan: %v", err)
		}
		requireViolation(t, w.checker.CheckSafetyAt(ctx, w.targets, w.clock.Now()), "INV-MEM-06.fts_orphan_chunk")
	})

	t.Run("INV_MEM_07_tenant_and_context_from_isolation", func(t *testing.T) {
		// 7a. Unknown chunk owner + cross-user input reference.
		w1 := newTestWorld()
		aliceInputID := "11111111-1111-4111-8111-000000000070"
		ts := seedUserInput(t, w1, w1.alice, aliceInputID, "Alice private secret.")
		bobOwner := w1.bob.user.DataOwner()
		bobMem := state.Memory{
			ID: "bob-leak-1", Text: "Leaked Alice secret", About: "the user",
			Provenance: state.ProvenancePreCompactFlush, SourceInputID: aliceInputID,
			CreatedAt: ts, UpdatedAt: ts,
		}
		writeDoc(t, w1, "owners/"+bobOwner+"/memories/"+bobMem.ID, bobMem)
		_ = w1.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: bobOwner, ChunkID: docstore.MemoryCoreChunkID(bobMem.ID), Layer: docstore.MemoryLayerCore,
			About: bobMem.About, Body: bobMem.Text, Provenance: string(bobMem.Provenance), SourceInput: aliceInputID,
			CreatedAt: ts, UpdatedAt: ts,
		})
		_ = w1.backend.UpsertMemoryChunk(ctx, docstore.MemoryChunk{
			Owner: "unknown-owner-xyz", ChunkID: docstore.MemoryCoreChunkID("stray"), Layer: docstore.MemoryLayerCore,
			Body: "stray", Provenance: string(state.ProvenanceUserExplicit), CreatedAt: ts, UpdatedAt: ts,
		})
		err := w1.checker.CheckSafetyAt(ctx, w1.targets, w1.clock.Now())
		requireViolation(t, err, "INV-MEM-07.unknown_chunk_owner")
		requireViolation(t, err, "INV-MEM-07.cross_user_reference")

		// 7b. Pre-ContextFrom summary item leaked into post-cutoff summary.
		w2 := newTestWorld()
		owner2 := w2.alice.user.DataOwner()
		oldInputID := "11111111-1111-4111-8111-000000000071"
		_ = seedUserInput(t, w2, w2.alice, oldInputID, "Old pre-cutoff preference.")
		w2.clock.Advance(10 * time.Minute)
		cutoff := w2.clock.Now().UTC()
		w2.clock.Advance(time.Minute)
		newTS := w2.clock.Now().UTC()
		ctrl, _, _ := w2.alice.store.ConversationStatus(ctx)
		ctrl.ContextFrom = cutoff
		writeDoc(t, w2, "owners/"+owner2+"/conversation/control", ctrl)
		writeDoc(t, w2, "owners/"+owner2+"/conversation/summary", state.ConversationSummary{
			From:           cutoff,
			FlushedThrough: newTS,
			Through:        newTS,
			Items:          []state.SummaryItem{{ID: "si_old", Kind: "preference", Quote: "Old pre-cutoff preference.", InputID: oldInputID}},
			Model:          "sim",
			UpdatedAt:      newTS,
		})
		requireViolation(t, w2.checker.CheckSafetyAt(ctx, w2.targets, w2.clock.Now()), "INV-MEM-07.pre_context_from_leak")
	})

	t.Run("INV_MEM_08_post_drain_quiescence", func(t *testing.T) {
		// 8a. FlushedThrough < Through with no future wake and no active backoff -> violation.
		w1 := newTestWorld()
		owner1 := w1.alice.user.DataOwner()
		t0 := w1.clock.Now().UTC()
		t1 := t0.Add(time.Minute)
		writeDoc(t, w1, "owners/"+owner1+"/conversation/summary", state.ConversationSummary{
			FlushedThrough: t0,
			Through:        t1,
			Excerpts:       []state.ConversationExcerpt{{MessageID: "m1", Role: "user", Text: "hello", TurnStartedAt: t1}},
			Items:          []state.SummaryItem{},
			Model:          "extractive",
			UpdatedAt:      t1,
		})
		w1.clock.Advance(3 * time.Minute)
		requireViolation(t, w1.checker.CheckQuiescence(ctx, w1.targets, w1.clock.Now()), "INV-MEM-08.unflushed_watermark")

		// 8b. Active future LaneConversation wake (WakeAt > now) suppresses false positive until due.
		if err := w1.backend.WakeUser(ctx, w1.alice.user.ID, docstore.LaneConversation, w1.clock.Now().Add(time.Minute)); err != nil {
			t.Fatalf("WakeUser future: %v", err)
		}
		if err := w1.checker.CheckQuiescence(ctx, w1.targets, w1.clock.Now()); err != nil {
			t.Fatalf("expected future scheduled wake to satisfy active-wake quiescence, got: %v", err)
		}

		// 8c. Once clock advances past WakeAt without draining, CheckLeases=true flags wake_lease_hygiene.
		w1.clock.Advance(2 * time.Minute)
		leaseChecker := invariants.New(invariants.Config{Backend: w1.backend, Now: w1.clock.Now, CheckLeases: true})
		err := leaseChecker.CheckQuiescence(ctx, w1.targets, w1.clock.Now())
		requireViolation(t, err, "INV-MEM-08.wake_lease_hygiene")
		requireViolation(t, err, "INV-MEM-08.unflushed_watermark")

		// 8d. FlushedThrough == Through with leftover retired Excerpts -> INV-MEM-08.stale_excerpts.
		w2 := newTestWorld()
		owner2 := w2.alice.user.DataOwner()
		s0 := w2.clock.Now().UTC()
		writeDoc(t, w2, "owners/"+owner2+"/conversation/summary", state.ConversationSummary{
			FlushedThrough: s0,
			Through:        s0,
			Excerpts:       []state.ConversationExcerpt{{MessageID: "m1", Role: "user", Text: "stale", TurnStartedAt: s0}},
			Items:          []state.SummaryItem{},
			Model:          "sim",
			UpdatedAt:      s0,
		})
		requireViolation(t, w2.checker.CheckQuiescence(ctx, w2.targets, w2.clock.Now()), "INV-MEM-08.stale_excerpts")

		// 8e. Unextracted completed turn past IdleExtractDelay (+20m) triggers INV-MEM-08.unextracted_after_idle,
		// while before +20m, with future LaneConversation wake, or during active extract failure backoff it passes.
		w3 := newTestWorld()
		owner3 := w3.alice.user.DataOwner()
		e0 := w3.clock.Now().UTC()
		ctrl3, _, _ := w3.alice.store.ConversationStatus(ctx)
		ctrl3.ContextFrom = e0
		writeDoc(t, w3, "owners/"+owner3+"/conversation/control", ctrl3)
		e1 := e0.Add(time.Minute)
		e2 := e0.Add(2 * time.Minute)
		writeDoc(t, w3, "owners/"+owner3+"/conversation_turns/turn-e1", state.ConversationTurn{
			ID:        "turn-e1",
			Status:    "completed",
			StartedAt: e1,
		})
		writeDoc(t, w3, "owners/"+owner3+"/conversation_turns/turn-e2", state.ConversationTurn{
			ID:        "turn-e2",
			Status:    "completed",
			StartedAt: e2,
		})
		writeDoc(t, w3, "owners/"+owner3+"/conversation/summary", state.ConversationSummary{
			From:             e0,
			ExtractedThrough: e1,
			Items:            []state.SummaryItem{},
			UpdatedAt:        e1,
		})
		// Before IdleExtractDelay (halfway past e2): passes cleanly without needing a wake.
		w3.clock.Advance(2*time.Minute + state.IdleExtractDelay/2)
		if err := w3.checker.CheckQuiescence(ctx, w3.targets, w3.clock.Now()); err != nil {
			t.Fatalf("expected unextracted turn before IdleExtractDelay to pass quiescence, got: %v", err)
		}
		// Past IdleExtractDelay after e2 with no future wake and no active backoff: violation!
		w3.clock.Advance(state.IdleExtractDelay)
		requireViolation(t, w3.checker.CheckQuiescence(ctx, w3.targets, w3.clock.Now()), "INV-MEM-08.unextracted_after_idle")
		// Store-only mode (Backend == nil) also catches INV-MEM-08.unextracted_after_idle.
		storeOnlyChecker := invariants.New(invariants.Config{Now: w3.clock.Now})
		requireViolation(t, storeOnlyChecker.CheckQuiescence(ctx, w3.targets, w3.clock.Now()), "INV-MEM-08.unextracted_after_idle")

		// Scheduled future LaneConversation wake suppresses false positive until due.
		if err := w3.backend.WakeUser(ctx, w3.alice.user.ID, docstore.LaneConversation, w3.clock.Now().Add(2*time.Minute)); err != nil {
			t.Fatalf("WakeUser future extract: %v", err)
		}
		if err := w3.checker.CheckQuiescence(ctx, w3.targets, w3.clock.Now()); err != nil {
			t.Fatalf("expected future scheduled wake to satisfy quiescence after IdleExtractDelay, got: %v", err)
		}

		// Clear future wake and enter active extract retry backoff (< 10m since UpdatedAt): passes cleanly.
		if cw, ok, err := w3.backend.ClaimUserWake(ctx, docstore.LaneConversation, "w-clear", 30*time.Second); err == nil && ok {
			_, _ = w3.backend.ParkUserWake(ctx, w3.alice.user.ID, docstore.LaneConversation, "w-clear", cw.WakeSeq)
		}
		w3.clock.Advance(3 * time.Minute)
		if cw, ok, err := w3.backend.ClaimUserWake(ctx, docstore.LaneConversation, "w-clear2", 30*time.Second); err == nil && ok {
			_, _ = w3.backend.ParkUserWake(ctx, w3.alice.user.ID, docstore.LaneConversation, "w-clear2", cw.WakeSeq)
		}
		backoffNow := w3.clock.Now().UTC()
		writeDoc(t, w3, "owners/"+owner3+"/conversation/summary", state.ConversationSummary{
			From:                      e0,
			ExtractedThrough:          e1,
			ExtractFailures:           1,
			LastExtractFailureThrough: e2,
			Items:                     []state.SummaryItem{},
			UpdatedAt:                 backoffNow.Add(-2 * time.Minute),
		})
		if err := w3.checker.CheckQuiescence(ctx, w3.targets, w3.clock.Now()); err != nil {
			t.Fatalf("expected active extract failure backoff to suppress unextracted_after_idle, got: %v", err)
		}
		// Once backoff window expires (>= 10m after UpdatedAt), unextracted_after_idle fires again.
		w3.clock.Advance(9 * time.Minute)
		requireViolation(t, w3.checker.CheckQuiescence(ctx, w3.targets, w3.clock.Now()), "INV-MEM-08.unextracted_after_idle")
	})

	t.Run("positive_decoupled_extracted_through_states", func(t *testing.T) {
		// 1. Valid uncompacted session-idle summary:
		// Through == 0, FlushedThrough == 0, ExtractedThrough == T3 > From, Items/Gist/Excerpts/Turns empty.
		w1 := newTestWorld()
		owner1 := w1.alice.user.DataOwner()
		from := w1.clock.Now().UTC()
		ctrl1, _, _ := w1.alice.store.ConversationStatus(ctx)
		ctrl1.ContextFrom = from
		writeDoc(t, w1, "owners/"+owner1+"/conversation/control", ctrl1)

		in1 := "11111111-1111-4111-8111-000000000101"
		in2 := "11111111-1111-4111-8111-000000000102"
		in3 := "11111111-1111-4111-8111-000000000103"
		t1 := seedUserInput(t, w1, w1.alice, in1, "Remember I fly out of SEA.")
		t2 := seedUserInput(t, w1, w1.alice, in2, "I prefer window seats.")
		t3 := seedUserInput(t, w1, w1.alice, in3, "My dog is named Mochi.")
		// Mark inputs consumed so conv.Pending() == false for CheckQuiescence.
		ctrl1, _, _ = w1.alice.store.ConversationStatus(ctx)
		ctrl1.ContextFrom = from
		ctrl1.Consumed = ctrl1.Received
		ctrl1.UserRouted = ctrl1.UserReceived
		writeDoc(t, w1, "owners/"+owner1+"/conversation/control", ctrl1)

		for idx, pair := range []struct {
			id  string
			in  string
			ts  time.Time
			txt string
		}{
			{"turn-idle-1", in1, t1, "Remember I fly out of SEA."},
			{"turn-idle-2", in2, t2, "I prefer window seats."},
			{"turn-idle-3", in3, t3, "My dog is named Mochi."},
		} {
			_ = idx
			writeDoc(t, w1, "owners/"+owner1+"/conversation_turns/"+pair.id, state.ConversationTurn{
				ID:        pair.id,
				Status:    "completed",
				StartedAt: pair.ts,
				Inputs:    []state.ConversationInput{{ID: pair.in, Text: pair.txt, CreatedAt: pair.ts}},
			})
		}

		// Commit uncompacted session-idle summary + extracted Core memory & Daily Log atomically.
		w1.clock.Advance(state.IdleExtractDelay + time.Second)
		now1 := w1.clock.Now().UTC()
		sumIdle, err := w1.alice.store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			NextSummary: state.ConversationSummary{
				From:             from,
				ExtractedThrough: t3,
				Items:            []state.SummaryItem{},
				UpdatedAt:        now1,
			},
			CoreUpserts: []state.Memory{{
				ID:            "mem-idle-1",
				Text:          "User prefers window seats and flies out of SEA.",
				About:         "the user",
				Section:       "Preferences",
				Provenance:    state.ProvenancePreCompactFlush,
				SourceInputID: in2,
				SourceText:    "I prefer window seats.",
				CreatedAt:     now1,
				UpdatedAt:     now1,
			}},
			DailyLogDate: now1.Format("2006-01-02"),
			DailyLogEntries: []state.MemoryLogEntry{{
				ID:            "log-idle-1",
				Section:       "Context",
				Text:          "User mentioned dog named Mochi.",
				About:         "the user",
				Provenance:    state.ProvenancePreCompactFlush,
				SourceInputID: in3,
				CreatedAt:     now1,
				UpdatedAt:     now1,
			}},
		})
		if err != nil {
			t.Fatalf("CommitFlushAndCompact uncompacted idle extract: %v", err)
		}
		if !sumIdle.Through.IsZero() || !sumIdle.FlushedThrough.IsZero() || !sumIdle.ExtractedThrough.Equal(t3) {
			t.Fatalf("unexpected uncompacted summary watermarks: %+v", sumIdle)
		}
		if err := w1.checker.CheckSafetyAt(ctx, w1.targets, w1.clock.Now()); err != nil {
			t.Fatalf("CheckSafetyAt uncompacted session-idle summary failed: %v", err)
		}
		if err := w1.checker.CheckQuiescence(ctx, w1.targets, w1.clock.Now()); err != nil {
			t.Fatalf("CheckQuiescence uncompacted session-idle summary failed: %v", err)
		}

		// Uncompacted summary with ExtractFailures > 0 (on a newer turn t4 > t3) and LastDreamAt > 0 also passes.
		t4 := w1.clock.Now().UTC().Add(time.Second)
		writeDoc(t, w1, "owners/"+owner1+"/conversation/summary", state.ConversationSummary{
			From:                      from,
			ExtractedThrough:          t3,
			ExtractFailures:           1,
			LastExtractFailureThrough: t4,
			LastDreamAt:               now1,
			Items:                     []state.SummaryItem{},
			UpdatedAt:                 now1.Add(time.Second),
		})
		if err := w1.checker.CheckSafetyAt(ctx, w1.targets, w1.clock.Now()); err != nil {
			t.Fatalf("CheckSafetyAt uncompacted summary with ExtractFailures & LastDreamAt failed: %v", err)
		}

		// 2. Valid compacted + ahead-extracted summary:
		// From < FlushedThrough == Through < ExtractedThrough.
		w2 := newTestWorld()
		owner2 := w2.alice.user.DataOwner()
		c0 := w2.clock.Now().UTC()
		inA := "11111111-1111-4111-8111-000000000201"
		inB := "11111111-1111-4111-8111-000000000202"
		c1 := seedUserInput(t, w2, w2.alice, inA, "Use Go 1.24 for services.")
		c2 := seedUserInput(t, w2, w2.alice, inB, "Keep tests deterministic.")
		ctrl2, _, _ := w2.alice.store.ConversationStatus(ctx)
		ctrl2.ContextFrom = c0
		ctrl2.Consumed = ctrl2.Received
		ctrl2.UserRouted = ctrl2.UserReceived
		writeDoc(t, w2, "owners/"+owner2+"/conversation/control", ctrl2)
		writeDoc(t, w2, "owners/"+owner2+"/conversation_turns/turn-c1", state.ConversationTurn{
			ID: "turn-c1", Status: "completed", StartedAt: c1,
			Inputs: []state.ConversationInput{{ID: inA, Text: "Use Go 1.24 for services.", CreatedAt: c1}},
		})
		writeDoc(t, w2, "owners/"+owner2+"/conversation_turns/turn-c2", state.ConversationTurn{
			ID: "turn-c2", Status: "completed", StartedAt: c2,
			Inputs: []state.ConversationInput{{ID: inB, Text: "Keep tests deterministic.", CreatedAt: c2}},
		})

		w2.clock.Advance(state.IdleExtractDelay + time.Second)
		now2 := w2.clock.Now().UTC()
		// Step 1: Compact turn c1 only (FlushedThrough == Through == c1, ExtractedThrough == c1).
		itemA := state.SummaryItem{ID: "si_ahead_1", Kind: "constraint", Quote: "Use Go 1.24 for services.", InputID: inA}
		writeDoc(t, w2, "owners/"+owner2+"/conversation/summary", state.ConversationSummary{
			From:             c0,
			FlushedThrough:   c1,
			Through:          c1,
			ExtractedThrough: c1,
			Items:            []state.SummaryItem{itemA},
			Gist:             "Discussed Go version.",
			Turns:            1,
			Model:            "sim-compaction",
			UpdatedAt:        now2,
		})
		if err := w2.checker.CheckSafetyAt(ctx, w2.targets, w2.clock.Now()); err != nil {
			t.Fatalf("CheckSafetyAt baseline compacted summary failed: %v", err)
		}
		// Step 2: Session-idle extraction advances ExtractedThrough to c2 > Through (c1) without compacting c2!
		writeDoc(t, w2, "owners/"+owner2+"/conversation/summary", state.ConversationSummary{
			From:             c0,
			FlushedThrough:   c1,
			Through:          c1,
			ExtractedThrough: c2,
			Items:            []state.SummaryItem{itemA},
			Gist:             "Discussed Go version.",
			Turns:            1,
			Model:            "sim-compaction",
			UpdatedAt:        now2.Add(time.Second),
		})
		if err := w2.checker.CheckSafetyAt(ctx, w2.targets, w2.clock.Now()); err != nil {
			t.Fatalf("CheckSafetyAt compacted + ahead-extracted summary failed: %v", err)
		}
		if err := w2.checker.CheckQuiescence(ctx, w2.targets, w2.clock.Now()); err != nil {
			t.Fatalf("CheckQuiescence compacted + ahead-extracted summary failed: %v", err)
		}
	})

	t.Run("INV_MEM_01_extracted_through_negative_mutations", func(t *testing.T) {
		// 1. ExtractedThrough < EffectiveFlushedThrough -> INV-MEM-01.watermark_bounds.
		w1 := newTestWorld()
		owner1 := w1.alice.user.DataOwner()
		t0 := w1.clock.Now().UTC()
		t1 := t0.Add(time.Minute)
		t2 := t0.Add(2 * time.Minute)
		writeDoc(t, w1, "owners/"+owner1+"/conversation/summary", state.ConversationSummary{
			From:             t0,
			FlushedThrough:   t2,
			Through:          t2,
			ExtractedThrough: t1,
			Items:            []state.SummaryItem{},
			Model:            "sim-compaction",
			UpdatedAt:        t2,
		})
		requireViolation(t, w1.checker.CheckSafetyAt(ctx, w1.targets, w1.clock.Now()), "INV-MEM-01.watermark_bounds")

		// 2. ExtractedThrough < ContextFrom in current epoch -> INV-MEM-01.watermark_bounds.
		w2 := newTestWorld()
		owner2 := w2.alice.user.DataOwner()
		cutoff := w2.clock.Now().UTC().Add(10 * time.Minute)
		ctrl2, _, _ := w2.alice.store.ConversationStatus(ctx)
		ctrl2.ContextFrom = cutoff
		writeDoc(t, w2, "owners/"+owner2+"/conversation/control", ctrl2)
		writeDoc(t, w2, "owners/"+owner2+"/conversation/summary", state.ConversationSummary{
			From:             cutoff,
			ExtractedThrough: cutoff.Add(-time.Minute),
			Items:            []state.SummaryItem{},
			UpdatedAt:        cutoff,
		})
		requireViolation(t, w2.checker.CheckSafetyAt(ctx, w2.targets, w2.clock.Now()), "INV-MEM-01.watermark_bounds")

		// 3. EffectiveExtractedThrough regressing backwards across steps in the same epoch -> INV-MEM-01.watermark_monotonic.
		w3 := newTestWorld()
		owner3 := w3.alice.user.DataOwner()
		b0 := w3.clock.Now().UTC()
		b1 := b0.Add(time.Minute)
		b2 := b0.Add(2 * time.Minute)
		b3 := b0.Add(3 * time.Minute)
		writeDoc(t, w3, "owners/"+owner3+"/conversation/summary", state.ConversationSummary{
			From:             b0,
			FlushedThrough:   b1,
			Through:          b1,
			ExtractedThrough: b3,
			Items:            []state.SummaryItem{},
			Model:            "sim-compaction",
			UpdatedAt:        b3,
		})
		if err := w3.checker.CheckSafetyAt(ctx, w3.targets, w3.clock.Now()); err != nil {
			t.Fatalf("baseline CheckSafetyAt for ExtractedThrough regression: %v", err)
		}
		writeDoc(t, w3, "owners/"+owner3+"/conversation/summary", state.ConversationSummary{
			From:             b0,
			FlushedThrough:   b1,
			Through:          b1,
			ExtractedThrough: b2,
			Items:            []state.SummaryItem{},
			Model:            "sim-compaction",
			UpdatedAt:        b3.Add(time.Second),
		})
		requireViolation(t, w3.checker.CheckSafetyAt(ctx, w3.targets, w3.clock.Now()), "INV-MEM-01.watermark_monotonic")

		// 4. Uncompacted EffectiveExtractedThrough regressing backwards across steps -> INV-MEM-01.watermark_monotonic.
		w4 := newTestWorld()
		owner4 := w4.alice.user.DataOwner()
		u0 := w4.clock.Now().UTC()
		u1 := u0.Add(time.Minute)
		u2 := u0.Add(2 * time.Minute)
		writeDoc(t, w4, "owners/"+owner4+"/conversation/summary", state.ConversationSummary{
			From:             u0,
			ExtractedThrough: u2,
			Items:            []state.SummaryItem{},
			UpdatedAt:        u2,
		})
		if err := w4.checker.CheckSafetyAt(ctx, w4.targets, w4.clock.Now()); err != nil {
			t.Fatalf("baseline CheckSafetyAt uncompacted: %v", err)
		}
		writeDoc(t, w4, "owners/"+owner4+"/conversation/summary", state.ConversationSummary{
			From:             u0,
			ExtractedThrough: u1,
			Items:            []state.SummaryItem{},
			UpdatedAt:        u2.Add(time.Second),
		})
		requireViolation(t, w4.checker.CheckSafetyAt(ctx, w4.targets, w4.clock.Now()), "INV-MEM-01.watermark_monotonic")

		// 5. ExtractedThrough advancing on extraction failure -> INV-MEM-01.failure_watermark_advance.
		w5 := newTestWorld()
		owner5 := w5.alice.user.DataOwner()
		f0 := w5.clock.Now().UTC()
		f1 := f0.Add(time.Minute)
		f2 := f0.Add(2 * time.Minute)
		f3 := f0.Add(3 * time.Minute)
		// 5a: Single-snapshot jump to LastExtractFailureThrough.
		writeDoc(t, w5, "owners/"+owner5+"/conversation/summary", state.ConversationSummary{
			From:                      f0,
			ExtractedThrough:          f2,
			ExtractFailures:           1,
			LastExtractFailureThrough: f2,
			Items:                     []state.SummaryItem{},
			UpdatedAt:                 f2,
		})
		requireViolation(t, w5.checker.CheckSafetyAt(ctx, w5.targets, w5.clock.Now()), "INV-MEM-01.failure_watermark_advance")

		// 5b: Cross-step advance while ExtractFailures increments on the same LastExtractFailureThrough.
		w6 := newTestWorld()
		owner6 := w6.alice.user.DataOwner()
		writeDoc(t, w6, "owners/"+owner6+"/conversation/summary", state.ConversationSummary{
			From:                      f0,
			ExtractedThrough:          f1,
			ExtractFailures:           1,
			LastExtractFailureThrough: f3,
			Items:                     []state.SummaryItem{},
			UpdatedAt:                 f1,
		})
		if err := w6.checker.CheckSafetyAt(ctx, w6.targets, w6.clock.Now()); err != nil {
			t.Fatalf("baseline CheckSafetyAt extract failure: %v", err)
		}
		writeDoc(t, w6, "owners/"+owner6+"/conversation/summary", state.ConversationSummary{
			From:                      f0,
			ExtractedThrough:          f2,
			ExtractFailures:           2,
			LastExtractFailureThrough: f3,
			Items:                     []state.SummaryItem{},
			UpdatedAt:                 f2,
		})
		requireViolation(t, w6.checker.CheckSafetyAt(ctx, w6.targets, w6.clock.Now()), "INV-MEM-01.failure_watermark_advance")
	})
}
