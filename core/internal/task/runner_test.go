package task_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/task"
	"nori/internal/transcript"
	"nori/internal/users"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock {
	return &fakeClock{now: t.UTC()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type testWakeHub struct {
	mu   sync.Mutex
	next int
	subs map[int]struct {
		userID string
		runID  string
		ch     chan struct{}
	}
}

func newTestWakeHub() *testWakeHub {
	return &testWakeHub{
		subs: make(map[int]struct {
			userID string
			runID  string
			ch     chan struct{}
		}),
	}
}

func (h *testWakeHub) Subscribe(userID, runID string) (<-chan struct{}, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	ch := make(chan struct{}, 1)
	h.subs[id] = struct {
		userID string
		runID  string
		ch     chan struct{}
	}{userID: userID, runID: runID, ch: ch}
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}
}

func (h *testWakeHub) NotifyTask(userID, runID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs {
		if s.userID == userID && (s.runID == runID || runID == "*") {
			select {
			case s.ch <- struct{}{}:
			default:
			}
		}
	}
}

func (h *testWakeHub) NotifyPause() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs {
		select {
		case s.ch <- struct{}{}:
		default:
		}
	}
}

type scriptedModel struct {
	mu    sync.Mutex
	calls int
	fn    func(ctx context.Context, callNum int, req agent.Request) (agent.Response, error)
}

func (m *scriptedModel) Provider() string { return "test" }
func (m *scriptedModel) Name() string     { return "test-model" }
func (m *scriptedModel) Inspect(context.Context, agent.InspectRequest) (agent.Inspection, error) {
	return agent.Inspection{Observation: "ok", Sent: true, CostKnown: true}, nil
}
func (m *scriptedModel) Respond(ctx context.Context, req agent.Request) (agent.Response, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	fn := m.fn
	m.mu.Unlock()
	return fn(ctx, n, req)
}
func (m *scriptedModel) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func toolCallResponse(id, name string, args map[string]any, cost float64) agent.Response {
	call := transcript.ToolCall{ID: id, Name: name, Args: args}
	msg := transcript.Message{
		Role:  transcript.RoleModel,
		Parts: []transcript.Part{{ToolCall: &call}},
	}
	return agent.Response{
		Message:   &msg,
		Calls:     []transcript.ToolCall{call},
		Cost:      cost,
		CostKnown: true,
	}
}

type runnerFixture struct {
	t          *testing.T
	clock      *fakeClock
	backend    *memstore.Backend
	artifacts  *memstore.Artifacts
	user       users.User
	usersDB    *records.Users
	db         *records.Database
	store      state.ConversationStore
	perms      *permissions.Service
	model      *scriptedModel
	wakes      *testWakeHub
	agentSvc   *agent.Service
	act        *task.Activities
	adapters   *invoke.Registry
	extSource  *testExtSource
	leaseMu    sync.Mutex
	leaseTick  chan time.Time
	leaseAck   chan struct{}
	lastRenewT time.Time
}

type testExtSource struct {
	defs []extensions.Definition
}

func (s *testExtSource) Inventory(context.Context) (extensions.Inventory, error) {
	return extensions.Inventory{Definitions: s.defs}, nil
}

func newRunnerFixture(t *testing.T, modelFn func(ctx context.Context, callNum int, req agent.Request) (agent.Response, error)) *runnerFixture {
	t.Helper()
	clock := newFakeClock(time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	backend := memstore.New(memstore.WithClock(clock.Now))
	artifacts := memstore.NewArtifacts()
	u := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "test@example.com",
		DisplayName: "Test User",
		TimeZone:    "UTC",
	}
	usersDB := records.NewSharedUsers(backend)
	if err := usersDB.Create(context.Background(), u); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	db := records.NewShared(backend, u, 5.0)
	db.Now = clock.Now
	store := state.Compose(db, artifacts)
	perms := &permissions.Service{
		Store:            store,
		UnattendedWrites: "confirm_writes",
		UserSends:        "ask",
		Now:              clock.Now,
	}
	model := &scriptedModel{fn: modelFn}
	extSource := &testExtSource{}
	adapters := invoke.NewRegistry()
	agentSvc := agent.New(store, model)
	agentSvc.Now = clock.Now
	agentSvc.Extensions = extSource
	agentSvc.Capabilities = agent.Capabilities{Location: time.UTC}
	wakes := newTestWakeHub()

	f := &runnerFixture{
		t:         t,
		clock:     clock,
		backend:   backend,
		artifacts: artifacts,
		user:      u,
		usersDB:   usersDB,
		db:        db,
		store:     store,
		perms:     perms,
		model:     model,
		wakes:     wakes,
		agentSvc:  agentSvc,
		adapters:  adapters,
		extSource: extSource,
	}

	sleepFn := func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d > 0 {
			clock.Advance(d)
			f.maybeRenewVirtualLease(ctx)
		}
		return ctx.Err()
	}
	noopTicker := func(time.Duration) (<-chan time.Time, func()) {
		return make(chan time.Time), func() {}
	}
	exec := &task.Executor{
		Adapters:    adapters,
		Permissions: perms,
		Now:         clock.Now,
		Sleep:       sleepFn,
	}
	f.act = &task.Activities{
		Agent:     agentSvc,
		Store:     store,
		Executor:  exec,
		Now:       clock.Now,
		Sleep:     sleepFn,
		NewTicker: noopTicker,
	}
	return f
}

func (f *runnerFixture) maybeRenewVirtualLease(ctx context.Context) {
	now := f.clock.Now()
	f.leaseMu.Lock()
	tick := f.leaseTick
	ack := f.leaseAck
	due := tick != nil && now.Sub(f.lastRenewT) >= 10*time.Second
	if due {
		f.lastRenewT = now
	}
	f.leaseMu.Unlock()
	if !due {
		return
	}
	select {
	case tick <- now:
	case <-ctx.Done():
		return
	}
	select {
	case <-ack:
	case <-ctx.Done():
	}
}

func (f *runnerFixture) startTask(ctx context.Context, runID, text string) state.Run {
	f.t.Helper()
	receipts, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
		ID:    "cmd-start-" + runID,
		RunID: runID,
		Kind:  state.CommandStart,
		Text:  text,
	}}, nil)
	if err != nil {
		f.t.Fatalf("AcceptCommandBatch start: %v", err)
	}
	return receipts[0].Run
}

func (f *runnerFixture) newRunner(owner string) *task.Runner {
	return &task.Runner{
		Activities: f.act,
		Leases:     f.backend,
		UserID:     f.user.ID,
		Owner:      owner,
		Wakes:      f.wakes,
		Now:        f.clock.Now,
		NewTicker: func(time.Duration) (<-chan time.Time, func()) {
			tick := make(chan time.Time)
			ack := make(chan struct{}, 1)
			f.leaseMu.Lock()
			f.leaseTick = tick
			f.leaseAck = ack
			f.lastRenewT = f.clock.Now()
			f.leaseMu.Unlock()
			return tick, func() {
				f.leaseMu.Lock()
				if f.leaseTick == tick {
					f.leaseTick = nil
					f.leaseAck = nil
				}
				f.leaseMu.Unlock()
			}
		},
		OnLeaseRenewed: func(_, _ string) {
			f.leaseMu.Lock()
			ack := f.leaseAck
			f.leaseMu.Unlock()
			if ack != nil {
				select {
				case ack <- struct{}{}:
				default:
				}
			}
		},
	}
}

func (f *runnerFixture) claimAndRun(ctx context.Context, runID, owner string) (task.RunOutcome, error) {
	f.t.Helper()
	claim, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, runID, owner, state.TaskLease)
	if err != nil {
		f.t.Fatalf("ClaimTaskByID(%s): %v", runID, err)
	}
	if !ok {
		f.t.Fatalf("ClaimTaskByID(%s): expected claimable lease", runID)
	}
	return f.newRunner(owner).Run(ctx, claim)
}

func TestRunner_HappyPathAndPerStepDeadlines(t *testing.T) {
	ctx := context.Background()
	var seenModelDeadline time.Duration
	f := newRunnerFixture(t, func(ctx context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		if dl, ok := ctx.Deadline(); ok {
			seenModelDeadline = time.Until(dl)
		}
		if callNum == 1 {
			return toolCallResponse("c1", "memory_read", map[string]any{}, 0.12), nil
		}
		return toolCallResponse("c2", "complete", map[string]any{
			"answer":   "All done cleanly.",
			"evidence": []any{},
			"unmet":    []any{},
		}, 0.08), nil
	})

	run := f.startTask(ctx, "run-happy", "Check memory and complete")
	t0 := f.clock.Now()

	outcome, err := f.claimAndRun(ctx, run.ID, "worker-a")
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("Run = (%s, %v), want (done, nil)", outcome, err)
	}

	if seenModelDeadline <= 0 || seenModelDeadline > 2*time.Minute+time.Second {
		t.Fatalf("model ctx deadline = %v, want in (0, 2m]", seenModelDeadline)
	}

	ops, err := f.store.Operations(ctx, run.ID)
	if err != nil || len(ops) != 1 {
		t.Fatalf("Operations: len=%d err=%v", len(ops), err)
	}
	wantUntil := t0.Add(240 * time.Second).UTC().Truncate(time.Microsecond)
	if !ops[0].RecoveryUntil.Equal(wantUntil) {
		t.Fatalf("Operation.RecoveryUntil = %v, want %v (240s)", ops[0].RecoveryUntil, wantUntil)
	}

	finalRun, err := f.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
		t.Fatalf("unexpected final run: %+v", finalRun)
	}
	if finalRun.Spent < 0.199 || finalRun.Spent > 0.201 {
		t.Fatalf("final Run.Spent = %.4f, want 0.20", finalRun.Spent)
	}

	ctrl, err := f.store.GetTaskControl(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetTaskControl: %v", err)
	}
	if ctrl.Phase != state.TaskPhaseDone || ctrl.Owner != "" || ctrl.Models != 2 || ctrl.Tools != 1 || ctrl.Checks != 1 {
		t.Fatalf("unexpected TaskControl: %+v", ctrl)
	}

	leases, err := f.backend.ListTaskLeases(ctx, f.user.ID)
	if err != nil || len(leases) != 0 {
		t.Fatalf("expected 0 task leases after completion, got %+v (err=%v)", leases, err)
	}

	pending, err := f.store.Pending(ctx, "")
	if err != nil || len(pending.Commands) != 0 {
		t.Fatalf("expected 0 undelivered commands after completion, got %+v (err=%v)", pending.Commands, err)
	}
}

func TestRunner_LateWakeDeletesTerminalCleanLease(t *testing.T) {
	ctx := context.Background()
	f := newRunnerFixture(t, func(_ context.Context, _ int, _ agent.Request) (agent.Response, error) {
		return toolCallResponse("c1", "complete", map[string]any{
			"answer": "Done.", "evidence": []any{}, "unmet": []any{},
		}, 0.05), nil
	})
	run := f.startTask(ctx, "run-late-wake", "Finish once")
	if out, err := f.claimAndRun(ctx, run.ID, "worker-a"); err != nil || out != task.OutcomeDone {
		t.Fatalf("initial Run = (%s, %v), want (done, nil)", out, err)
	}
	callsBefore := f.model.Calls()

	if err := f.backend.WakeTask(ctx, f.user.ID, run.ID, f.clock.Now()); err != nil {
		t.Fatalf("WakeTask: %v", err)
	}
	out, err := f.claimAndRun(ctx, run.ID, "worker-b")
	if err != nil || out != task.OutcomeDone {
		t.Fatalf("late wake Run = (%s, %v), want (done, nil)", out, err)
	}
	if f.model.Calls() != callsBefore {
		t.Fatalf("late wake invoked model (%d -> %d)", callsBefore, f.model.Calls())
	}
	leases, _ := f.backend.ListTaskLeases(ctx, f.user.ID)
	if len(leases) != 0 {
		t.Fatalf("expected late wake lease deleted, got %+v", leases)
	}
}

type renewHookStore struct {
	state.TaskStore
	onRenew func(ctx context.Context, runID string, f state.TaskFence) error
}

func (s *renewHookStore) RenewTaskControl(ctx context.Context, runID string, f state.TaskFence) error {
	if s.onRenew != nil {
		return s.onRenew(ctx, runID, f)
	}
	return s.TaskStore.RenewTaskControl(ctx, runID, f)
}

type renewHookLeaseStore struct {
	docstore.LeaseStore
	onRenew func(ctx context.Context, owner string, keys []docstore.TaskKey, lease time.Duration) (int, error)
}

func (s *renewHookLeaseStore) RenewTaskLeases(ctx context.Context, owner string, keys []docstore.TaskKey, lease time.Duration) (int, error) {
	if s.onRenew != nil {
		return s.onRenew(ctx, owner, keys, lease)
	}
	return s.LeaseStore.RenewTaskLeases(ctx, owner, keys, lease)
}

func TestRunner_WatchTaskLeaseRenewalAndSelfStop(t *testing.T) {
	t.Run("renews_both_and_notifies", func(t *testing.T) {
		ctx := context.Background()
		inModel := make(chan struct{})
		releaseModel := make(chan struct{})
		f := newRunnerFixture(t, func(mCtx context.Context, _ int, _ agent.Request) (agent.Response, error) {
			close(inModel)
			select {
			case <-releaseModel:
				return toolCallResponse("c1", "complete", map[string]any{
					"answer": "Renewed.", "evidence": []any{}, "unmet": []any{},
				}, 0.05), nil
			case <-mCtx.Done():
				return agent.Response{NotSent: true}, mCtx.Err()
			}
		})
		run := f.startTask(ctx, "run-renew-ok", "Long model step")

		tickCh := make(chan time.Time)
		renewedCh := make(chan struct{}, 4)
		runner := f.newRunner("worker-a")
		runner.NewTicker = func(time.Duration) (<-chan time.Time, func()) {
			return tickCh, func() {}
		}
		runner.OnLeaseRenewed = func(_, _ string) {
			renewedCh <- struct{}{}
		}

		claim, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-a", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}

		doneCh := make(chan struct {
			out task.RunOutcome
			err error
		}, 1)
		go func() {
			out, err := runner.Run(ctx, claim)
			doneCh <- struct {
				out task.RunOutcome
				err error
			}{out, err}
		}()

		<-inModel
		f.clock.Advance(10 * time.Second)
		tickCh <- f.clock.Now()
		select {
		case <-renewedCh:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for first lease renewal")
		}
		f.clock.Advance(10 * time.Second)
		tickCh <- f.clock.Now()
		select {
		case <-renewedCh:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for second lease renewal")
		}

		ctrl, err := f.store.GetTaskControl(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetTaskControl: %v", err)
		}
		wantUntil := f.clock.Now().Add(state.TaskLease)
		if !ctrl.LeaseUntil.Equal(wantUntil) {
			t.Fatalf("TaskControl.LeaseUntil = %v, want %v", ctrl.LeaseUntil, wantUntil)
		}

		close(releaseModel)
		res := <-doneCh
		if res.err != nil || res.out != task.OutcomeDone {
			t.Fatalf("Run = (%s, %v), want (done, nil)", res.out, res.err)
		}
	})

	t.Run("renew_error_cancels_inflight_step_immediately", func(t *testing.T) {
		ctx := context.Background()
		inModel := make(chan struct{})
		modelCanceled := make(chan struct{})
		f := newRunnerFixture(t, func(mCtx context.Context, _ int, _ agent.Request) (agent.Response, error) {
			close(inModel)
			<-mCtx.Done()
			close(modelCanceled)
			return agent.Response{NotSent: true}, mCtx.Err()
		})
		run := f.startTask(ctx, "run-renew-err", "Renew error self-stop")

		dbErr := errors.New("simulated postgres renewal outage")
		wrappedAct := *f.act
		wrappedAct.Store = &renewHookStore{
			TaskStore: f.store,
			onRenew: func(context.Context, string, state.TaskFence) error {
				return dbErr
			},
		}

		tickCh := make(chan time.Time)
		runner := f.newRunner("worker-a")
		runner.Activities = &wrappedAct
		runner.NewTicker = func(time.Duration) (<-chan time.Time, func()) {
			return tickCh, func() {}
		}

		claim, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-a", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}

		doneCh := make(chan struct {
			out task.RunOutcome
			err error
		}, 1)
		go func() {
			out, err := runner.Run(ctx, claim)
			doneCh <- struct {
				out task.RunOutcome
				err error
			}{out, err}
		}()

		<-inModel
		tickCh <- f.clock.Now()

		select {
		case <-modelCanceled:
		case <-time.After(2 * time.Second):
			t.Fatal("watchTaskLease did not cancel in-flight model step on renewal error")
		}
		res := <-doneCh
		if res.out != task.OutcomeBackoff || !errors.Is(res.err, dbErr) {
			t.Fatalf("Run = (%s, %v), want (backoff, %v)", res.out, res.err, dbErr)
		}
		ctrl, err := f.store.GetTaskControl(ctx, run.ID)
		if err != nil || ctrl.PendingModelID == "" {
			t.Fatalf("expected PendingModelID preserved after renewal failure, got %+v (err=%v)", ctrl, err)
		}
	})

	t.Run("renew_lease_zero_cancels_immediately", func(t *testing.T) {
		ctx := context.Background()
		inModel := make(chan struct{})
		modelCanceled := make(chan struct{})
		f := newRunnerFixture(t, func(mCtx context.Context, _ int, _ agent.Request) (agent.Response, error) {
			close(inModel)
			<-mCtx.Done()
			close(modelCanceled)
			return agent.Response{NotSent: true}, mCtx.Err()
		})
		run := f.startTask(ctx, "run-renew-zero", "RenewTaskLeases zero self-stop")

		tickCh := make(chan time.Time)
		runner := f.newRunner("worker-a")
		runner.Leases = &renewHookLeaseStore{
			LeaseStore: f.backend,
			onRenew: func(context.Context, string, []docstore.TaskKey, time.Duration) (int, error) {
				return 0, nil
			},
		}
		runner.NewTicker = func(time.Duration) (<-chan time.Time, func()) {
			return tickCh, func() {}
		}

		claim, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-a", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}

		doneCh := make(chan struct {
			out task.RunOutcome
			err error
		}, 1)
		go func() {
			out, err := runner.Run(ctx, claim)
			doneCh <- struct {
				out task.RunOutcome
				err error
			}{out, err}
		}()

		<-inModel
		tickCh <- f.clock.Now()
		select {
		case <-modelCanceled:
		case <-time.After(2 * time.Second):
			t.Fatal("watchTaskLease did not cancel in-flight model when RenewTaskLeases returned 0")
		}
		res := <-doneCh
		if res.out != task.OutcomeBusy || res.err != nil {
			t.Fatalf("Run = (%s, %v), want (busy, nil)", res.out, res.err)
		}
	})
}

func TestRunner_WakeDrivenBoundedCancellation(t *testing.T) {
	t.Run("stop_cancels_inflight_model_immediately", func(t *testing.T) {
		ctx := context.Background()
		inModel := make(chan struct{})
		modelCanceled := make(chan struct{})
		f := newRunnerFixture(t, func(mCtx context.Context, _ int, _ agent.Request) (agent.Response, error) {
			close(inModel)
			<-mCtx.Done()
			close(modelCanceled)
			return agent.Response{NotSent: true}, mCtx.Err()
		})
		run := f.startTask(ctx, "run-wake-stop", "Stop mid model via wake")

		claim, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-a", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		doneCh := make(chan struct {
			out task.RunOutcome
			err error
		}, 1)
		go func() {
			out, err := f.newRunner("worker-a").Run(ctx, claim)
			doneCh <- struct {
				out task.RunOutcome
				err error
			}{out, err}
		}()

		<-inModel
		if _, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
			ID: "cmd-stop", RunID: run.ID, Kind: state.CommandStop,
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch stop: %v", err)
		}
		f.wakes.NotifyTask(f.user.ID, run.ID)

		select {
		case <-modelCanceled:
		case <-time.After(2 * time.Second):
			t.Fatal("bounded() did not cancel in-flight model via WakeNotifier on Stop")
		}
		res := <-doneCh
		if res.err != nil || res.out != task.OutcomeDone {
			t.Fatalf("Run = (%s, %v), want (done, nil)", res.out, res.err)
		}
		finalRun, _ := f.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunCancelled {
			t.Fatalf("final status = %s, want cancelled", finalRun.Status)
		}
	})

	t.Run("pause_cancels_inflight_model_and_resumes_cleanly", func(t *testing.T) {
		ctx := context.Background()
		inModel := make(chan struct{})
		modelCanceled := make(chan struct{})
		f := newRunnerFixture(t, func(mCtx context.Context, callNum int, _ agent.Request) (agent.Response, error) {
			if callNum == 1 {
				close(inModel)
				<-mCtx.Done()
				close(modelCanceled)
				return agent.Response{NotSent: true}, mCtx.Err()
			}
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Resumed after wake-driven pause.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		run := f.startTask(ctx, "run-wake-pause", "Pause mid model via wake")

		claim, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-a", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		doneCh := make(chan struct {
			out task.RunOutcome
			err error
		}, 1)
		go func() {
			out, err := f.newRunner("worker-a").Run(ctx, claim)
			doneCh <- struct {
				out task.RunOutcome
				err error
			}{out, err}
		}()

		<-inModel
		if err := f.usersDB.SetPaused(ctx, true); err != nil {
			t.Fatalf("SetPaused(true): %v", err)
		}
		f.wakes.NotifyPause()

		select {
		case <-modelCanceled:
		case <-time.After(2 * time.Second):
			t.Fatal("bounded() did not cancel in-flight model via WakeNotifier on Pause")
		}
		res := <-doneCh
		if res.err != nil || res.out != task.OutcomeParked {
			t.Fatalf("Run = (%s, %v), want (parked, nil)", res.out, res.err)
		}

		if err := f.usersDB.SetPaused(ctx, false); err != nil {
			t.Fatalf("SetPaused(false): %v", err)
		}
		out2, err := f.claimAndRun(ctx, run.ID, "worker-b")
		if err != nil || out2 != task.OutcomeDone {
			t.Fatalf("resumed Run = (%s, %v), want (done, nil)", out2, err)
		}
		finalRun, _ := f.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunCompleted {
			t.Fatalf("final status = %s, want completed", finalRun.Status)
		}
	})

	t.Run("interrupting_steer_cancels_inflight_model_and_reincorporates", func(t *testing.T) {
		ctx := context.Background()
		inModel := make(chan struct{})
		modelCanceled := make(chan struct{})
		var sawSteer bool
		f := newRunnerFixture(t, func(mCtx context.Context, callNum int, req agent.Request) (agent.Response, error) {
			if callNum == 1 {
				close(inModel)
				<-mCtx.Done()
				close(modelCanceled)
				return agent.Response{NotSent: true}, mCtx.Err()
			}
			for _, m := range req.Messages {
				for _, p := range m.Parts {
					if p.Text == "Switch plan immediately" {
						sawSteer = true
					}
				}
			}
			return toolCallResponse("c-steered", "complete", map[string]any{
				"answer": "Followed interrupting steer.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		run := f.startTask(ctx, "run-wake-steer", "Initial plan")

		claim, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-a", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		doneCh := make(chan struct {
			out task.RunOutcome
			err error
		}, 1)
		go func() {
			out, err := f.newRunner("worker-a").Run(ctx, claim)
			doneCh <- struct {
				out task.RunOutcome
				err error
			}{out, err}
		}()

		<-inModel
		if _, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
			ID: "cmd-steer", RunID: run.ID, Kind: state.CommandSteer, Text: "Switch plan immediately", Interrupt: true,
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch steer: %v", err)
		}
		f.wakes.NotifyTask(f.user.ID, run.ID)

		select {
		case <-modelCanceled:
		case <-time.After(2 * time.Second):
			t.Fatal("bounded() did not cancel in-flight model via WakeNotifier on interrupting steer")
		}
		res := <-doneCh
		if res.err != nil || res.out != task.OutcomeDone {
			t.Fatalf("Run = (%s, %v), want (done, nil)", res.out, res.err)
		}
		if !sawSteer {
			t.Fatal("expected second model call to incorporate interrupting steer text")
		}
		finalRun, _ := f.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunCompleted || finalRun.Revision != 2 {
			t.Fatalf("unexpected final run: status=%s rev=%d", finalRun.Status, finalRun.Revision)
		}
	})
}

func TestRunner_CrashMidModelReceiptReuseAndD053Retry(t *testing.T) {
	t.Run("reuses_committed_checkpoint_after_crash_before_advance", func(t *testing.T) {
		ctx := context.Background()
		f := newRunnerFixture(t, func(_ context.Context, _ int, _ agent.Request) (agent.Response, error) {
			return toolCallResponse("c1", "complete", map[string]any{
				"answer": "Reused checkpoint answer.", "evidence": []any{}, "unmet": []any{},
			}, 0.15), nil
		})
		run := f.startTask(ctx, "run-model-reuse", "Crash after SaveTaskModelCheckpoint")

		claim1, ok, _ := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-1", state.TaskLease)
		if !ok {
			t.Fatal("expected claim1")
		}
		r1 := f.newRunner("worker-1")
		r1.StepBudget = 1
		if out, err := r1.Run(ctx, claim1); err != nil || out != task.OutcomeReleased {
			t.Fatalf("step 1 = (%s, %v), want (released, nil)", out, err)
		}

		_, ok, _ = f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-1", state.TaskLease)
		if !ok {
			t.Fatal("expected claim for worker-1 crash setup")
		}
		ctrl, fence, err := f.store.AcquireTaskControl(ctx, run.ID, "worker-1")
		if err != nil {
			t.Fatalf("AcquireTaskControl: %v", err)
		}
		started := f.clock.Now().UTC()
		ctrl.PendingModelID = "model-ckpt-1"
		ctrl.ModelStepStartedAt = &started
		ctrl, err = f.store.SaveTaskControl(ctx, fence, ctrl)
		if err != nil {
			t.Fatalf("SaveTaskControl: %v", err)
		}
		mCtx, mCancel := context.WithTimeout(ctx, 240*time.Second)
		_, err = f.act.ModelRef(mCtx, task.RefModelInput{
			Step:    task.Step{RunID: run.ID, Revision: ctrl.Revision},
			ModelID: ctrl.PendingModelID,
			Ref:     ctrl.Ref,
		})
		mCancel()
		if err != nil {
			t.Fatalf("ModelRef before crash: %v", err)
		}
		if f.model.Calls() != 1 {
			t.Fatalf("expected 1 model call before crash, got %d", f.model.Calls())
		}

		f.clock.Advance(state.TaskLease + time.Second)
		out, err := f.claimAndRun(ctx, run.ID, "worker-2")
		if err != nil || out != task.OutcomeDone {
			t.Fatalf("worker-2 Run = (%s, %v), want (done, nil)", out, err)
		}
		if f.model.Calls() != 1 {
			t.Fatalf("expected checkpoint reuse (still 1 LLM call), got %d", f.model.Calls())
		}
		finalRun, _ := f.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunCompleted || finalRun.Spent < 0.149 || finalRun.Spent > 0.151 {
			t.Fatalf("unexpected final run: status=%s spent=%.4f", finalRun.Status, finalRun.Spent)
		}
		finalCtrl, _ := f.store.GetTaskControl(ctx, run.ID)
		if finalCtrl.Fence <= fence.Fence {
			t.Fatalf("expected Fence incremented on takeover: old=%d new=%d", fence.Fence, finalCtrl.Fence)
		}
	})

	t.Run("crash_mid_model_d053_retry_within_9m", func(t *testing.T) {
		ctx := context.Background()
		f := newRunnerFixture(t, func(_ context.Context, _ int, _ agent.Request) (agent.Response, error) {
			return toolCallResponse("c-retry", "complete", map[string]any{
				"answer": "Succeeded on D-053 re-ask.", "evidence": []any{}, "unmet": []any{},
			}, 0.10), nil
		})
		run := f.startTask(ctx, "run-model-d053", "Crash mid LLM call")

		claim1, ok, _ := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-1", state.TaskLease)
		if !ok {
			t.Fatal("expected claim1")
		}
		r1 := f.newRunner("worker-1")
		r1.StepBudget = 1
		if _, err := r1.Run(ctx, claim1); err != nil {
			t.Fatalf("sync step: %v", err)
		}

		_, _, _ = f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-1", state.TaskLease)
		ctrl, fence, err := f.store.AcquireTaskControl(ctx, run.ID, "worker-1")
		if err != nil {
			t.Fatalf("AcquireTaskControl: %v", err)
		}
		started := f.clock.Now().UTC()
		ctrl.PendingModelID = "model-inflight-1"
		ctrl.ModelStepStartedAt = &started
		ctrl, err = f.store.SaveTaskControl(ctx, fence, ctrl)
		if err != nil {
			t.Fatalf("SaveTaskControl: %v", err)
		}
		if err := f.store.StartTaskModelCheckpoint(ctx, run.ID, state.TaskModelCheckpoint{
			ID:            ctrl.PendingModelID,
			Revision:      ctrl.Revision,
			Parent:        ctrl.Ref,
			RecoveryUntil: started.Add(240 * time.Second),
		}); err != nil {
			t.Fatalf("StartTaskModelCheckpoint: %v", err)
		}

		f.clock.Advance(35 * time.Second)
		out, err := f.claimAndRun(ctx, run.ID, "worker-2")
		if err != nil || out != task.OutcomeDone {
			t.Fatalf("worker-2 D-053 Run = (%s, %v), want (done, nil)", out, err)
		}
		if f.model.Calls() != 1 {
			t.Fatalf("expected 1 re-ask LLM call, got %d", f.model.Calls())
		}
		finalRun, _ := f.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunCompleted || !finalRun.CostUnknown {
			t.Fatalf("expected completed with CostUnknown=true from interrupted first attempt, got %+v", finalRun)
		}
		finalCtrl, _ := f.store.GetTaskControl(ctx, run.ID)
		if finalCtrl.ModelRetries != 1 {
			t.Fatalf("expected ModelRetries=1, got %d", finalCtrl.ModelRetries)
		}
	})

	t.Run("d053_deadline_exceeded_does_not_reask", func(t *testing.T) {
		ctx := context.Background()
		f := newRunnerFixture(t, func(_ context.Context, _ int, _ agent.Request) (agent.Response, error) {
			t.Error("LLM should NOT be re-asked once past 9m D-053 deadline")
			return agent.Response{}, errors.New("unexpected call")
		})
		run := f.startTask(ctx, "run-model-d053-expired", "Crash past D-053 deadline")

		claim1, _, _ := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-1", state.TaskLease)
		r1 := f.newRunner("worker-1")
		r1.StepBudget = 1
		_, _ = r1.Run(ctx, claim1)

		_, _, _ = f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-1", state.TaskLease)
		ctrl, fence, _ := f.store.AcquireTaskControl(ctx, run.ID, "worker-1")
		started := f.clock.Now().UTC()
		ctrl.PendingModelID = "model-expired-1"
		ctrl.ModelStepStartedAt = &started
		ctrl, _ = f.store.SaveTaskControl(ctx, fence, ctrl)
		_ = f.store.StartTaskModelCheckpoint(ctx, run.ID, state.TaskModelCheckpoint{
			ID:            ctrl.PendingModelID,
			Revision:      ctrl.Revision,
			Parent:        ctrl.Ref,
			RecoveryUntil: started.Add(240 * time.Second),
		})

		f.clock.Advance(8 * time.Minute)
		out, err := f.claimAndRun(ctx, run.ID, "worker-2")
		if err != nil || out != task.OutcomeDone {
			t.Fatalf("worker-2 expired D-053 Run = (%s, %v), want (done, nil)", out, err)
		}
		if f.model.Calls() != 0 {
			t.Fatalf("expected 0 LLM calls past D-053 deadline, got %d", f.model.Calls())
		}
		finalRun, _ := f.store.GetRun(ctx, run.ID)
		if finalRun.Status != state.RunFailed || !finalRun.CostUnknown {
			t.Fatalf("expected RunFailed + CostUnknown=true, got %+v", finalRun)
		}
	})
}

type mockWriteAdapter struct {
	execCalls atomic.Int32
}

func (a *mockWriteAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
	spec := map[string]string{"target": invoke.String(call.Args, "target")}
	return invoke.Prepared{
		Summary: "Mock write " + spec["target"],
		Effect:  extensions.EffectUnknown,
		Spec:    spec,
		Keys:    map[string]string{"action_key": invoke.ActionKey(call, spec)},
	}, nil
}

func (a *mockWriteAdapter) Execute(_ context.Context, _ invoke.Call, _ invoke.Prepared) invoke.Result {
	a.execCalls.Add(1)
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"ok": true}}
}

func (a *mockWriteAdapter) Reconcile(_ context.Context, _ invoke.Call, _ invoke.Prepared) (invoke.Result, bool) {
	return invoke.Result{}, false
}

func TestRunner_CrashMidToolSafeReadRerunVsUncertainWrite(t *testing.T) {
	t.Run("safe_read_reruns_after_crash", func(t *testing.T) {
		ctx := context.Background()
		f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
			if callNum == 1 {
				return toolCallResponse("r1", "memory_read", map[string]any{}, 0.05), nil
			}
			return toolCallResponse("r2", "complete", map[string]any{
				"answer": "Recovered safe read.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		run := f.startTask(ctx, "run-tool-read-crash", "Crash during safe read")

		claim1, _, _ := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-1", state.TaskLease)
		r1 := f.newRunner("worker-1")
		r1.StepBudget = 2
		if out, err := r1.Run(ctx, claim1); err != nil || out != task.OutcomeReleased {
			t.Fatalf("steps 1-2 = (%s, %v), want (released, nil)", out, err)
		}

		ctrl, _ := f.store.GetTaskControl(ctx, run.ID)
		opID := fmt.Sprintf("%s-m%d-t%d", run.ID, ctrl.Models, ctrl.Tools+1)
		until := f.clock.Now().Add(240 * time.Second).UTC().Truncate(time.Microsecond)
		if err := f.store.Admit(ctx, run.ID, ctrl.Revision, opID, until); err != nil {
			t.Fatalf("Admit: %v", err)
		}
		if err := f.store.PutOperation(ctx, run.ID, state.OperationRecord{
			ID: opID, CallID: "r1", Name: "memory_read", Revision: ctrl.Revision,
			EffectClass: "read", Outcome: "pending", Attempt: 1, RecoveryUntil: until,
		}); err != nil {
			t.Fatalf("PutOperation: %v", err)
		}

		out, err := f.claimAndRun(ctx, run.ID, "worker-2")
		if err != nil || out != task.OutcomeDone {
			t.Fatalf("worker-2 Run = (%s, %v), want (done, nil)", out, err)
		}
		op, _ := f.store.Operation(ctx, run.ID, opID)
		if op.Outcome != "confirmed" || op.Attempt != 2 {
			t.Fatalf("expected safe read rerun confirmed (attempt=2), got %+v", op)
		}
	})

	t.Run("interrupted_write_settled_uncertain_never_double_executed", func(t *testing.T) {
		ctx := context.Background()
		writeDef, err := extensions.Seal(extensions.Definition{
			Ref:             extensions.DefinitionRef{ID: "mock/tools/write", Version: "1"},
			Extension:       extensions.DefinitionRef{ID: "mock/ext", Version: "1"},
			ModelName:       "mock_write",
			Title:           "Mock Write",
			Description:     "Non-rerunnable write tool",
			Capability:      "mock",
			CapabilityTitle: "Mock",
			InputSchema:     json.RawMessage(`{"type":"object","properties":{"target":{"type":"string"}},"required":["target"]}`),
			Binding:         extensions.Binding{Adapter: extensions.AdapterNative, Target: "mock/write", Version: "1"},
			Policy:          extensions.Policy{Effect: extensions.EffectUnknown, Retry: "reconcile_only", TimeoutMS: 30_000, Location: extensions.LocationRuntime},
			Exposure:        extensions.ExposureDirect,
		})
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}

		f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
			if callNum == 1 {
				return toolCallResponse("w1", "mock_write", map[string]any{"target": "doc-1"}, 0.05), nil
			}
			return toolCallResponse("w2", "complete", map[string]any{
				"answer": "Done.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		adapter := &mockWriteAdapter{}
		f.extSource.defs = []extensions.Definition{writeDef}
		f.adapters.Register(extensions.AdapterNative, "mock", adapter)
		_, err = f.perms.SaveRule(ctx, "", "req-rule-1", 0, permissions.RuleEditable{
			Effect: "allow", ToolIDs: []string{"mock/tools/write"},
			ConnectionIDs: []string{}, ResourceIDs: []string{"*"}, ActionClasses: []string{"unknown"},
			Title: "Allow mock_write",
		})
		if err != nil {
			t.Fatalf("SaveRule: %v", err)
		}

		run := f.startTask(ctx, "run-tool-write-crash", "Crash during write")
		claim1, _, _ := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-1", state.TaskLease)
		r1 := f.newRunner("worker-1")
		r1.StepBudget = 2
		if out, err := r1.Run(ctx, claim1); err != nil || out != task.OutcomeReleased {
			t.Fatalf("steps 1-2 = (%s, %v), want (released, nil)", out, err)
		}

		ctrl, _ := f.store.GetTaskControl(ctx, run.ID)
		opID := fmt.Sprintf("%s-m%d-t%d", run.ID, ctrl.Models, ctrl.Tools+1)
		until := f.clock.Now().Add(240 * time.Second).UTC().Truncate(time.Microsecond)
		if err := f.store.Admit(ctx, run.ID, ctrl.Revision, opID, until); err != nil {
			t.Fatalf("Admit: %v", err)
		}
		if err := f.store.PutOperation(ctx, run.ID, state.OperationRecord{
			ID: opID, CallID: "w1", Name: "mock_write", Revision: ctrl.Revision,
			EffectClass: "unknown", Outcome: "pending", Attempt: 1, RecoveryUntil: until,
			Action: &state.Action{Digest: "d1", Text: "Mock write doc-1", Intent: true, Spec: map[string]string{"target": "doc-1"}},
		}); err != nil {
			t.Fatalf("PutOperation: %v", err)
		}

		out, err := f.claimAndRun(ctx, run.ID, "worker-2")
		if err != nil || out != task.OutcomeParked {
			t.Fatalf("worker-2 Run = (%s, %v), want (parked, nil)", out, err)
		}
		if adapter.execCalls.Load() != 0 {
			t.Fatalf("interrupted write MUST NOT re-execute, got %d execute calls", adapter.execCalls.Load())
		}
		op, _ := f.store.Operation(ctx, run.ID, opID)
		if op.Outcome != "uncertain" {
			t.Fatalf("expected operation settled as uncertain, got %q", op.Outcome)
		}
		midRun, _ := f.store.GetRun(ctx, run.ID)
		if midRun.Question == nil || len(midRun.Question.Clears) != 1 || midRun.Question.Clears[0] != opID {
			t.Fatalf("expected uncertain-action question clearing %s, got %+v", opID, midRun.Question)
		}
	})
}

type parkHookLeaseStore struct {
	docstore.LeaseStore
	beforePark func(ctx context.Context, userID, runID, owner string, wakeSeq int64, reason string)
}

func (s *parkHookLeaseStore) ParkTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, reason string) (bool, error) {
	if s.beforePark != nil {
		s.beforePark(ctx, userID, runID, owner, wakeSeq, reason)
	}
	return s.LeaseStore.ParkTask(ctx, userID, runID, owner, wakeSeq, reason)
}

func TestRunner_WakeSeqRaceOnPark(t *testing.T) {
	t.Run("concurrent_answer_during_ask_does_not_park", func(t *testing.T) {
		ctx := context.Background()
		f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
			if callNum == 1 {
				return toolCallResponse("q1", "request_input", map[string]any{"question": "Proceed?"}, 0.05), nil
			}
			return toolCallResponse("c1", "complete", map[string]any{
				"answer": "Proceeded without parking.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		run := f.startTask(ctx, "run-race-answer", "Race on Ask park")

		var fired atomic.Bool
		hookLeases := &parkHookLeaseStore{
			LeaseStore: f.backend,
			beforePark: func(ctx context.Context, _, runID, _ string, _ int64, reason string) {
				if reason == docstore.WaitQuestion && fired.CompareAndSwap(false, true) {
					cur, _ := f.store.GetRun(ctx, runID)
					if cur.Question != nil {
						_, _ = f.store.AcceptCommandBatch(ctx, []state.Command{{
							ID: "cmd-ans-race", RunID: runID, Kind: state.CommandAnswer,
							QuestionID: cur.Question.ID, Text: "Yes, go ahead!",
						}}, nil)
					}
				}
			},
		}

		claim, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-a", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		runner := f.newRunner("worker-a")
		runner.Leases = hookLeases
		out, err := runner.Run(ctx, claim)
		if err != nil || out != task.OutcomeDone {
			t.Fatalf("Run = (%s, %v), want (done, nil) without parking", out, err)
		}
		if !fired.Load() {
			t.Fatal("expected concurrent answer race hook to fire")
		}
	})

	t.Run("concurrent_routing_clear_does_not_park", func(t *testing.T) {
		ctx := context.Background()
		f := newRunnerFixture(t, func(_ context.Context, _ int, _ agent.Request) (agent.Response, error) {
			return toolCallResponse("c1", "complete", map[string]any{
				"answer": "Published right after routing cleared.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		run := f.startTask(ctx, "run-race-routing", "Race on routing_pending park")
		if err := f.store.AcceptConversationInput(ctx, "in-route-1", "Hold on", nil); err != nil {
			t.Fatalf("AcceptConversationInput: %v", err)
		}

		var fired atomic.Bool
		hookLeases := &parkHookLeaseStore{
			LeaseStore: f.backend,
			beforePark: func(ctx context.Context, _, _, _ string, _ int64, reason string) {
				if reason == docstore.WaitRoutingPending && fired.CompareAndSwap(false, true) {
					ctrl, turn, err := f.store.AcquireConversation(ctx, "router-1", "turn-1")
					if err == nil && turn != nil {
						fin := f.clock.Now().UTC()
						turn.Attempt = state.ConversationAttempt{
							ID: "turn-1-att-1", TurnID: turn.ID, Number: 1, Status: "completed",
							StartedAt: turn.StartedAt, FinishedAt: &fin,
						}
						_ = f.store.PublishConversation(ctx, state.ConversationFence{
							Owner: ctrl.Owner, Fence: ctrl.Fence, TurnID: turn.ID,
							Through: turn.Through, UserThrough: turn.UserThrough, Frozen: turn.Frozen,
						}, *turn, "Routed.")
					}
				}
			},
		}

		claim, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-a", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		runner := f.newRunner("worker-a")
		runner.Leases = hookLeases
		out, err := runner.Run(ctx, claim)
		if err != nil || out != task.OutcomeDone {
			t.Fatalf("Run = (%s, %v), want (done, nil) without parking", out, err)
		}
		if !fired.Load() {
			t.Fatal("expected routing clear race hook to fire")
		}
	})

	t.Run("concurrent_unpause_does_not_park", func(t *testing.T) {
		ctx := context.Background()
		f := newRunnerFixture(t, func(_ context.Context, _ int, _ agent.Request) (agent.Response, error) {
			return toolCallResponse("c1", "complete", map[string]any{
				"answer": "Completed after concurrent unpause.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		run := f.startTask(ctx, "run-race-unpause", "Race on paused park")
		if err := f.usersDB.SetPaused(ctx, true); err != nil {
			t.Fatalf("SetPaused(true): %v", err)
		}

		var fired atomic.Bool
		hookLeases := &parkHookLeaseStore{
			LeaseStore: f.backend,
			beforePark: func(ctx context.Context, _, _, _ string, _ int64, reason string) {
				if reason == docstore.WaitPaused && fired.CompareAndSwap(false, true) {
					_ = f.usersDB.SetPaused(ctx, false)
				}
			},
		}

		claim, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-a", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID: ok=%v err=%v", ok, err)
		}
		runner := f.newRunner("worker-a")
		runner.Leases = hookLeases
		out, err := runner.Run(ctx, claim)
		if err != nil || out != task.OutcomeDone {
			t.Fatalf("Run = (%s, %v), want (done, nil) without parking", out, err)
		}
		if !fired.Load() {
			t.Fatal("expected unpause race hook to fire")
		}
	})
}

func TestRunner_GracefulDrainAndCtxCancel(t *testing.T) {
	t.Run("drain_stops_at_checkpoint_boundary_and_hands_off", func(t *testing.T) {
		ctx := context.Background()
		drainCh := make(chan struct{})
		f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
			if callNum == 1 {
				close(drainCh)
				return toolCallResponse("m1", "memory_read", map[string]any{}, 0.05), nil
			}
			return toolCallResponse("m2", "complete", map[string]any{
				"answer": "Finished on replica 2.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		run := f.startTask(ctx, "run-drain", "Graceful drain mid-task")

		claim, ok, _ := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "replica-1", state.TaskLease)
		if !ok {
			t.Fatal("expected claim")
		}
		r1 := f.newRunner("replica-1")
		r1.Drain = drainCh
		out1, err := r1.Run(ctx, claim)
		if err != nil || out1 != task.OutcomeReleased {
			t.Fatalf("replica-1 Run = (%s, %v), want (released, nil)", out1, err)
		}
		ctrl, _ := f.store.GetTaskControl(ctx, run.ID)
		if ctrl.Owner != "" || ctrl.Phase != state.TaskPhaseCalls {
			t.Fatalf("expected released TaskControl in calls phase, got %+v", ctrl)
		}
		leases, _ := f.backend.ListTaskLeases(ctx, f.user.ID)
		if len(leases) != 1 || leases[0].Owner != "" || leases[0].WakeAt.After(f.clock.Now()) {
			t.Fatalf("expected immediately claimable unowned lease, got %+v", leases)
		}

		out2, err := f.claimAndRun(ctx, run.ID, "replica-2")
		if err != nil || out2 != task.OutcomeDone {
			t.Fatalf("replica-2 Run = (%s, %v), want (done, nil)", out2, err)
		}
	})

	t.Run("ctx_cancel_releases_leases_via_without_cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		inModel := make(chan struct{})
		f := newRunnerFixture(t, func(mCtx context.Context, _ int, _ agent.Request) (agent.Response, error) {
			close(inModel)
			<-mCtx.Done()
			return agent.Response{NotSent: true}, mCtx.Err()
		})
		run := f.startTask(context.Background(), "run-ctx-cancel", "Ctx cancel mid-step")
		claim, ok, _ := f.backend.ClaimTaskByID(context.Background(), f.user.ID, run.ID, "worker-1", state.TaskLease)
		if !ok {
			t.Fatal("expected claim")
		}

		doneCh := make(chan struct {
			out task.RunOutcome
			err error
		}, 1)
		go func() {
			out, err := f.newRunner("worker-1").Run(ctx, claim)
			doneCh <- struct {
				out task.RunOutcome
				err error
			}{out, err}
		}()

		<-inModel
		cancel()
		res := <-doneCh
		if res.out != task.OutcomeReleased || !errors.Is(res.err, context.Canceled) {
			t.Fatalf("Run = (%s, %v), want (released, context.Canceled)", res.out, res.err)
		}
		ctrl, _ := f.store.GetTaskControl(context.Background(), run.ID)
		if ctrl.Owner != "" {
			t.Fatalf("expected TaskControl released on ctx cancel, got owner=%q", ctrl.Owner)
		}
		leases, _ := f.backend.ListTaskLeases(context.Background(), f.user.ID)
		if len(leases) != 1 || leases[0].Owner != "" {
			t.Fatalf("expected discovery lease released on ctx cancel, got %+v", leases)
		}
	})
}

func TestRunner_GlobalPauseAndStopPaused(t *testing.T) {
	ctx := context.Background()
	f := newRunnerFixture(t, func(_ context.Context, _ int, _ agent.Request) (agent.Response, error) {
		t.Error("LLM must NOT be called while paused")
		return agent.Response{}, errors.New("unexpected")
	})
	run := f.startTask(ctx, "run-pause-stop", "Paused then stopped")
	if err := f.usersDB.SetPaused(ctx, true); err != nil {
		t.Fatalf("SetPaused(true): %v", err)
	}

	out, err := f.claimAndRun(ctx, run.ID, "worker-a")
	if err != nil || out != task.OutcomeParked {
		t.Fatalf("paused Run = (%s, %v), want (parked, nil)", out, err)
	}
	leases, _ := f.backend.ListTaskLeases(ctx, f.user.ID)
	if len(leases) != 1 || leases[0].Runnable || leases[0].WaitingReason != docstore.WaitPaused {
		t.Fatalf("expected lease parked as paused, got %+v", leases)
	}

	if _, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
		ID: "cmd-stop-paused", RunID: run.ID, Kind: state.CommandStop,
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch stop: %v", err)
	}
	out2, err := f.claimAndRun(ctx, run.ID, "worker-b")
	if err != nil || out2 != task.OutcomeDone {
		t.Fatalf("StopPaused Run = (%s, %v), want (done, nil)", out2, err)
	}
	if f.model.Calls() != 0 {
		t.Fatalf("expected 0 LLM calls for StopPaused, got %d", f.model.Calls())
	}
	finalRun, _ := f.store.GetRun(ctx, run.ID)
	if finalRun.Status != state.RunCancelled || finalRun.CleanupPending {
		t.Fatalf("expected cancelled & clean run, got %+v", finalRun)
	}
	leases, _ = f.backend.ListTaskLeases(ctx, f.user.ID)
	if len(leases) != 0 {
		t.Fatalf("expected 0 leases after StopPaused, got %+v", leases)
	}
}

func TestRunner_ParentFinishingSubtaskDrainNoLivelock(t *testing.T) {
	ctx := context.Background()
	f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		if callNum == 1 {
			return toolCallResponse("p-read", "memory_read", map[string]any{}, 0.05), nil
		}
		if callNum == 2 {
			return toolCallResponse("p-work", "request_work", map[string]any{
				"request": "Subtask work", "why": "Test finishing barrier",
			}, 0.05), nil
		}
		return toolCallResponse("c-done", "complete", map[string]any{
			"answer": "Child done", "evidence": []any{}, "unmet": []any{},
		}, 0.05), nil
	})
	parent := f.startTask(ctx, "run-parent-fin", "Parent with cleanup + subtask")
	if out, err := f.claimAndRun(ctx, parent.ID, "worker-a"); err != nil || out != task.OutcomeParked {
		t.Fatalf("parent initial Run = (%s, %v), want (parked, nil)", out, err)
	}
	gotParent, _ := f.store.GetRun(ctx, parent.ID)
	if gotParent.Question == nil || gotParent.Question.Kind != state.QuestionWork || !gotParent.CleanupPending {
		t.Fatalf("expected parent parked on QuestionWork with CleanupPending=true, got %+v", gotParent)
	}

	_, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
		ID: "cmd-child-start", RunID: "run-child-fin", ParentID: parent.ID,
		QuestionID: gotParent.Question.ID, Kind: state.CommandStart, Text: "Do child work",
	}}, nil)
	if err != nil {
		t.Fatalf("spawn child: %v", err)
	}

	_, err = f.store.AcceptCommandBatch(ctx, []state.Command{{
		ID: "cmd-parent-stop", RunID: parent.ID, Kind: state.CommandStop,
	}}, nil)
	if err != nil {
		t.Fatalf("stop parent: %v", err)
	}

	out, err := f.claimAndRun(ctx, parent.ID, "worker-a")
	if err != nil || out != task.OutcomeParked {
		t.Fatalf("parent finishing Run = (%s, %v), want (parked, nil)", out, err)
	}
	midParent, _ := f.store.GetRun(ctx, parent.ID)
	if midParent.Status != state.RunRunning || midParent.Finishing == nil || midParent.CleanupPending {
		t.Fatalf("expected parent in Finishing with CleanupPending=false, got %+v", midParent)
	}
	leases, _ := f.backend.ListTaskLeases(ctx, f.user.ID)
	foundParentWait := false
	for _, l := range leases {
		if l.RunID == parent.ID && !l.Runnable && l.WaitingReason == docstore.WaitFinishing {
			foundParentWait = true
		}
	}
	if !foundParentWait {
		t.Fatalf("expected parent lease parked as WaitFinishing, got %+v", leases)
	}

	if outC, err := f.claimAndRun(ctx, "run-child-fin", "worker-b"); err != nil || outC != task.OutcomeDone {
		t.Fatalf("child Run = (%s, %v), want (done, nil)", outC, err)
	}
	if outP, err := f.claimAndRun(ctx, parent.ID, "worker-b"); err != nil || outP != task.OutcomeDone {
		t.Fatalf("parent final Run = (%s, %v), want (done, nil)", outP, err)
	}
	finalParent, _ := f.store.GetRun(ctx, parent.ID)
	if finalParent.Status != state.RunCancelled || finalParent.Finishing != nil || finalParent.CleanupPending {
		t.Fatalf("expected parent RunCancelled & clean, got %+v", finalParent)
	}
	leases, _ = f.backend.ListTaskLeases(ctx, f.user.ID)
	if len(leases) != 0 {
		t.Fatalf("expected 0 leases after parent+child finished, got %+v", leases)
	}
}

func TestRunner_StaleWorkerFencedOut(t *testing.T) {
	ctx := context.Background()
	worker1Entered := make(chan struct{})
	worker1Resume := make(chan struct{})
	var calls atomic.Int32
	f := newRunnerFixture(t, func(_ context.Context, _ int, _ agent.Request) (agent.Response, error) {
		n := calls.Add(1)
		if n == 1 {
			close(worker1Entered)
			<-worker1Resume
			return toolCallResponse("stale-c1", "complete", map[string]any{
				"answer": "Stale worker answer.", "evidence": []any{}, "unmet": []any{},
			}, 0.25), nil
		}
		return toolCallResponse("fresh-c1", "complete", map[string]any{
			"answer": "Fresh worker answer.", "evidence": []any{}, "unmet": []any{},
		}, 0.10), nil
	})
	run := f.startTask(ctx, "run-stale-fence", "Stale worker vs takeover worker")

	claim1, ok, err := f.backend.ClaimTaskByID(ctx, f.user.ID, run.ID, "worker-1", state.TaskLease)
	if err != nil || !ok {
		t.Fatalf("ClaimTaskByID worker-1: ok=%v err=%v", ok, err)
	}
	r1 := f.newRunner("worker-1")
	r1.NewTicker = func(time.Duration) (<-chan time.Time, func()) {
		return make(chan time.Time), func() {}
	}

	done1 := make(chan struct {
		out task.RunOutcome
		err error
	}, 1)
	go func() {
		out, err := r1.Run(ctx, claim1)
		done1 <- struct {
			out task.RunOutcome
			err error
		}{out, err}
	}()

	<-worker1Entered
	ctrlBefore, err := f.store.GetTaskControl(ctx, run.ID)
	if err != nil || ctrlBefore.Owner != "worker-1" {
		t.Fatalf("expected worker-1 to hold fence, got %+v (err=%v)", ctrlBefore, err)
	}

	f.clock.Advance(state.TaskLease + 5*time.Second)

	out2, err := f.claimAndRun(ctx, run.ID, "worker-2")
	if err != nil || out2 != task.OutcomeDone {
		t.Fatalf("worker-2 Run = (%s, %v), want (done, nil)", out2, err)
	}
	finalRunBeforeStale, _ := f.store.GetRun(ctx, run.ID)
	finalCtrlBeforeStale, _ := f.store.GetTaskControl(ctx, run.ID)
	if finalRunBeforeStale.Status != state.RunCompleted || finalRunBeforeStale.Result != "Fresh worker answer." {
		t.Fatalf("expected worker-2 completion, got status=%s result=%q", finalRunBeforeStale.Status, finalRunBeforeStale.Result)
	}
	if finalCtrlBeforeStale.Fence <= ctrlBefore.Fence {
		t.Fatalf("expected Fence+1 on takeover: %d -> %d", ctrlBefore.Fence, finalCtrlBeforeStale.Fence)
	}

	close(worker1Resume)
	res1 := <-done1
	if res1.out != task.OutcomeBusy || res1.err != nil {
		t.Fatalf("stalled worker-1 Run = (%s, %v), want (busy, nil)", res1.out, res1.err)
	}

	finalRunAfterStale, _ := f.store.GetRun(ctx, run.ID)
	finalCtrlAfterStale, _ := f.store.GetTaskControl(ctx, run.ID)
	if finalRunAfterStale.Spent != finalRunBeforeStale.Spent {
		t.Fatalf("stale worker mutated spend: before=%.4f after=%.4f", finalRunBeforeStale.Spent, finalRunAfterStale.Spent)
	}
	if finalCtrlAfterStale.Phase != state.TaskPhaseDone || finalCtrlAfterStale.Fence != finalCtrlBeforeStale.Fence || finalCtrlAfterStale.Models != finalCtrlBeforeStale.Models {
		t.Fatalf("stale worker overwrote TaskControl: before=%+v after=%+v", finalCtrlBeforeStale, finalCtrlAfterStale)
	}
}
