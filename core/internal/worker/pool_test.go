package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/app"
	"nori/internal/permissions"
	"nori/internal/platform"
	"nori/internal/scheduler"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/task"
	"nori/internal/transcript"
	"nori/internal/users"
	"nori/internal/worker"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock(t time.Time) *testClock {
	return &testClock{now: t.UTC()}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newPoolTestEnv(t *testing.T, clock *testClock) (*app.Service, *memstore.Backend) {
	t.Helper()
	backend := memstore.New(memstore.WithClock(clock.Now))
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore {
		return artifacts
	})
	cfg := settings.Settings{
		Cloud:         "local",
		Role:          "worker",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Models: settings.Models{
			OpenAIKey:     "test-openai-key",
			OpenRouterKey: "test-openrouter-key",
			Outer:         "gpt-6.1-sol",
			Inner:         "openai/gpt-6.1-terra",
		},
	}
	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	t.Cleanup(svc.Close)
	return svc, backend
}

func mustCreateUser(t *testing.T, svc *app.Service, id, email string) users.User {
	t.Helper()
	u := users.User{
		ID:          id,
		Email:       email,
		DisplayName: email,
		TimeZone:    "UTC",
	}
	if err := svc.Users.Create(context.Background(), u); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
	return u
}

func expectSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("expected signal for %s", label)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, check func() bool) bool {
	t.Helper()
	if check() {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if check() {
				return true
			}
		case <-timer.C:
			return check()
		}
	}
}

type manualTickerHub struct {
	mu       sync.Mutex
	channels []chan time.Time
}

func (h *manualTickerHub) NewTicker(_ time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time, 8)
	h.mu.Lock()
	h.channels = append(h.channels, ch)
	h.mu.Unlock()
	return ch, func() {}
}

func (h *manualTickerHub) Tick(at time.Time) {
	h.mu.Lock()
	chs := append([]chan time.Time(nil), h.channels...)
	h.mu.Unlock()
	for _, ch := range chs {
		select {
		case ch <- at:
		default:
		}
	}
}

func expectNoSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("unexpected signal for %s", label)
	default:
	}
}

func TestPoolWakeNotifierFanout(t *testing.T) {
	clock := newTestClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, _ := newPoolTestEnv(t, clock)
	pool, err := worker.New(svc, worker.Options{Owner: "pool-fanout", Now: clock.Now})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	chA1, unsubA1 := pool.Subscribe("uA", "r1")
	defer unsubA1()
	chA2, unsubA2 := pool.Subscribe("uA", "r2")
	defer unsubA2()
	chB1, unsubB1 := pool.Subscribe("uB", "r1")
	defer unsubB1()

	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.TaskWakePayload("uA", "r1")})
	expectSignal(t, chA1, "uA/r1 specific wake")
	expectNoSignal(t, chA2, "uA/r2 should not receive uA/r1 wake")
	expectNoSignal(t, chB1, "uB/r1 should not receive uA/r1 wake")

	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.UserTasksWakePayload("uA")})
	expectSignal(t, chA1, "uA/r1 user-wide wake")
	expectSignal(t, chA2, "uA/r2 user-wide wake")
	expectNoSignal(t, chB1, "uB/r1 should not receive uA:* wake")

	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.WakePayloadPause})
	expectSignal(t, chA1, "uA/r1 pause")
	expectSignal(t, chA2, "uA/r2 pause")
	expectSignal(t, chB1, "uB/r1 pause")

	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.WakePayloadResync})
	expectSignal(t, chA1, "uA/r1 resync")
	expectSignal(t, chA2, "uA/r2 resync")
	expectSignal(t, chB1, "uB/r1 resync")

	unsubA2()
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.WakePayloadAllTask})
	expectSignal(t, chA1, "uA/r1 global task wake")
	expectNoSignal(t, chA2, "unsubscribed uA/r2")
	expectSignal(t, chB1, "uB/r1 global task wake")
}

func TestPoolInnerTaskDispatchAndConcurrency(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now().UTC().Add(time.Second))
	svc, backend := newPoolTestEnv(t, clock)

	u1 := mustCreateUser(t, svc, users.LegacyID, "legacy@example.com")
	u2 := mustCreateUser(t, svc, uuid.NewString(), "u2@example.com")

	app1, rel1, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1: %v", err)
	}
	defer rel1()
	app2, rel2, err := svc.Acquire(ctx, u2)
	if err != nil {
		t.Fatalf("Acquire u2: %v", err)
	}
	defer rel2()

	var active atomic.Int32
	var maxActive atomic.Int32
	var completed atomic.Int32
	allDone := make(chan struct{})
	twoActive := make(chan struct{})
	var twoActiveOnce sync.Once
	const totalTasks = 6
	const maxConcurrency = 2

	pool, err := worker.New(svc, worker.Options{
		Owner:           "pool-inner",
		TaskConcurrency: maxConcurrency,
		PollInterval:    20 * time.Millisecond,
		RepairInterval:  time.Hour,
		DrainTimeout:    2 * time.Second,
		Now:             clock.Now,
		RunTask: func(ctx context.Context, userApp *app.App, claim docstore.TaskLease, drain <-chan struct{}) (task.RunOutcome, error) {
			if userApp.UserID != claim.UserID {
				t.Errorf("mismatched UserID: app=%s claim=%s", userApp.UserID, claim.UserID)
			}
			cur := active.Add(1)
			for {
				prev := maxActive.Load()
				if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
					break
				}
			}
			if cur >= maxConcurrency {
				twoActiveOnce.Do(func() { close(twoActive) })
			}
			select {
			case <-twoActive:
			case <-ctx.Done():
			}
			active.Add(-1)
			if _, err := backend.DeleteTaskLease(ctx, claim.UserID, claim.RunID, 0); err != nil {
				return task.OutcomeBackoff, err
			}
			if completed.Add(1) == totalTasks {
				close(allDone)
			}
			return task.OutcomeDone, nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()

	for i := range totalTasks {
		target := app1
		if i%2 == 1 {
			target = app2
		}
		if _, err := target.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: uuid.NewString(), Kind: state.CommandStart, Text: "task work",
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch %d: %v", i, err)
		}
	}

	select {
	case <-allDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for tasks; completed=%d/%d", completed.Load(), totalTasks)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
	if maxActive.Load() > maxConcurrency {
		t.Fatalf("maxActive = %d, exceeded TaskConcurrency = %d", maxActive.Load(), maxConcurrency)
	}
	if maxActive.Load() < 2 {
		t.Fatalf("expected concurrent execution (maxActive >= 2), got %d", maxActive.Load())
	}
}

func TestPoolOuterConversationClaimParkAndConcurrentWake(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, backend := newPoolTestEnv(t, clock)
	u := mustCreateUser(t, svc, uuid.NewString(), "conv@example.com")

	var runs atomic.Int32
	secondRunDone := make(chan struct{})

	pool, err := worker.New(svc, worker.Options{
		Owner:                   "pool-conv",
		ConversationConcurrency: 2,
		PollInterval:            25 * time.Millisecond,
		RepairInterval:          time.Hour,
		RenewInterval:           50 * time.Millisecond,
		DrainTimeout:            2 * time.Second,
		Now:                     clock.Now,
		RunConversation: func(ctx context.Context, userApp *app.App) error {
			n := runs.Add(1)
			if n == 1 {
				if err := backend.WakeUser(ctx, u.ID, docstore.LaneConversation, time.Time{}); err != nil {
					return err
				}
				return nil
			}
			if n == 2 {
				close(secondRunDone)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()

	if err := backend.WakeUser(ctx, u.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser: %v", err)
	}

	select {
	case <-secondRunDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("expected concurrent wake to prevent parking and trigger pass #2; runs=%d", runs.Load())
	}

	waitForCondition(t, time.Second, func() bool {
		wakes, err := backend.ListUserWakes(ctx, u.ID)
		return err == nil && len(wakes) == 1 && !wakes[0].Runnable && wakes[0].Owner == ""
	})
	wakes, err := backend.ListUserWakes(ctx, u.ID)
	if err != nil || len(wakes) != 1 || wakes[0].Runnable || wakes[0].Owner != "" {
		t.Fatalf("expected conversation wake to park cleanly after pass #2, got %+v (err=%v)", wakes, err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
}

func TestPoolOuterSchedulerDelayedClaimReArmAndPark(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, backend := newPoolTestEnv(t, clock)
	u := mustCreateUser(t, svc, uuid.NewString(), "sched@example.com")

	var pass atomic.Int32
	pass1Done := make(chan struct{})
	pass2Done := make(chan struct{})
	secondDue := clock.Now().Add(2 * time.Hour)

	tickers := &manualTickerHub{}
	pool, err := worker.New(svc, worker.Options{
		Owner:                "pool-sched",
		SchedulerConcurrency: 2,
		PollInterval:         20 * time.Millisecond,
		RepairInterval:       time.Hour,
		DrainTimeout:         2 * time.Second,
		Now:                  clock.Now,
		NewTicker:            tickers.NewTicker,
		RunScheduler: func(ctx context.Context, userApp *app.App) (*time.Time, error) {
			n := pass.Add(1)
			if n == 1 {
				close(pass1Done)
				next := secondDue
				return &next, nil
			}
			close(pass2Done)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	firstDue := clock.Now().Add(time.Hour)
	if err := backend.WakeUser(ctx, u.ID, docstore.LaneScheduler, firstDue); err != nil {
		t.Fatalf("WakeUser future: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()
	if err := backend.WaitForWakeSubscribers(ctx, 1); err != nil {
		t.Fatalf("WaitForWakeSubscribers: %v", err)
	}

	tickers.Tick(clock.Now())
	runtime.Gosched()
	if pass.Load() != 0 {
		t.Fatalf("scheduler ran before wake_at was due: pass=%d", pass.Load())
	}

	clock.Advance(time.Hour)
	tickers.Tick(clock.Now())
	select {
	case <-pass1Done:
	case <-time.After(2 * time.Second):
		t.Fatalf("scheduler pass 1 did not run after wake_at became due")
	}

	waitForCondition(t, time.Second, func() bool {
		wakes, _ := backend.ListUserWakes(ctx, u.ID)
		return len(wakes) == 1 && wakes[0].Owner == "" && wakes[0].Runnable && wakes[0].WakeAt.Equal(secondDue)
	})
	wakes, _ := backend.ListUserWakes(ctx, u.ID)
	if len(wakes) != 1 || !wakes[0].Runnable || !wakes[0].WakeAt.Equal(secondDue) || wakes[0].Owner != "" {
		t.Fatalf("expected re-armed scheduler wake at %s, got %+v", secondDue, wakes)
	}

	clock.Advance(time.Hour)
	tickers.Tick(clock.Now())
	select {
	case <-pass2Done:
	case <-time.After(2 * time.Second):
		t.Fatalf("scheduler pass 2 did not run")
	}

	waitForCondition(t, time.Second, func() bool {
		wakes, _ = backend.ListUserWakes(ctx, u.ID)
		return len(wakes) == 1 && !wakes[0].Runnable && wakes[0].Owner == ""
	})
	wakes, _ = backend.ListUserWakes(ctx, u.ID)
	if len(wakes) != 1 || wakes[0].Runnable || wakes[0].Owner != "" {
		t.Fatalf("expected scheduler wake to park when NextScheduled==nil, got %+v", wakes)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
}

func TestPoolRepairSweep(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now().UTC().Add(time.Second))
	svc, backend := newPoolTestEnv(t, clock)
	u := mustCreateUser(t, svc, uuid.NewString(), "repair@example.com")

	userApp, release, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	missingLeaseRunID := uuid.NewString()
	if _, err := userApp.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: missingLeaseRunID, Kind: state.CommandStart, Text: "open task",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch missingLeaseRunID: %v", err)
	}
	if _, err := backend.DeleteTaskLease(ctx, u.ID, missingLeaseRunID, 0); err != nil {
		t.Fatalf("DeleteTaskLease: %v", err)
	}

	orphanRunID := uuid.NewString()
	if err := backend.WakeTask(ctx, u.ID, orphanRunID, time.Time{}); err != nil {
		t.Fatalf("WakeTask orphan: %v", err)
	}
	unknownUserID := uuid.NewString()
	if err := backend.WakeTask(ctx, unknownUserID, uuid.NewString(), time.Time{}); err != nil {
		t.Fatalf("WakeTask unknown user: %v", err)
	}

	stuckReasons := []string{
		docstore.WaitPaused,
		docstore.WaitRoutingPending,
		docstore.WaitQuestion,
		docstore.WaitFinishing,
	}
	stuckRunIDs := make([]string, len(stuckReasons))
	for i, reason := range stuckReasons {
		rid := uuid.NewString()
		stuckRunIDs[i] = rid
		if _, err := userApp.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: rid, Kind: state.CommandStart, Text: "stuck " + reason,
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch %s: %v", reason, err)
		}
		claim, ok, err := backend.ClaimTaskByID(ctx, u.ID, rid, "old-worker", state.TaskLease)
		if err != nil || !ok {
			t.Fatalf("ClaimTaskByID %s: ok=%v err=%v", reason, ok, err)
		}
		parked, err := backend.ParkTask(ctx, u.ID, rid, "old-worker", claim.WakeSeq, reason)
		if err != nil || !parked {
			t.Fatalf("ParkTask %s: parked=%v err=%v", reason, parked, err)
		}
	}

	u2 := mustCreateUser(t, svc, uuid.NewString(), "repair2@example.com")
	userApp2, release2, err := svc.Acquire(ctx, u2)
	if err != nil {
		t.Fatalf("Acquire u2: %v", err)
	}
	defer release2()
	if err := userApp2.Store.AcceptConversationInput(ctx, uuid.NewString(), "hello repair", nil); err != nil {
		t.Fatalf("AcceptConversationInput: %v", err)
	}
	cw, ok, err := backend.ClaimUserWake(ctx, docstore.LaneConversation, "old-conv", state.ConversationLease)
	if err != nil || !ok {
		t.Fatalf("ClaimUserWake conv: ok=%v err=%v", ok, err)
	}
	if parked, err := backend.ParkUserWake(ctx, u2.ID, docstore.LaneConversation, "old-conv", cw.WakeSeq); err != nil || !parked {
		t.Fatalf("ParkUserWake conv: parked=%v err=%v", parked, err)
	}
	legitRoutingRunID := uuid.NewString()
	if _, err := userApp2.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: legitRoutingRunID, Kind: state.CommandStart, Text: "legit routing wait",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch legitRoutingRunID: %v", err)
	}
	rc, ok, err := backend.ClaimTaskByID(ctx, u2.ID, legitRoutingRunID, "old-worker", state.TaskLease)
	if err != nil || !ok {
		t.Fatalf("ClaimTaskByID legitRoutingRunID: ok=%v err=%v", ok, err)
	}
	if parked, err := backend.ParkTask(ctx, u2.ID, legitRoutingRunID, "old-worker", rc.WakeSeq, docstore.WaitRoutingPending); err != nil || !parked {
		t.Fatalf("ParkTask legitRoutingRunID: parked=%v err=%v", parked, err)
	}

	nextDue := clock.Now().Add(30 * time.Minute)
	autoBody, _ := json.Marshal(map[string]any{
		"id":      "auto-1",
		"title":   "Daily check",
		"prompt":  "Check status",
		"status":  "enabled",
		"next_at": nextDue.Format(time.RFC3339Nano),
	})
	if _, err := userApp.Store.PutResource(ctx, state.Resource{
		Collection: scheduler.CollectionAutomations,
		ID:         "auto-1",
		Body:       autoBody,
		Index:      map[string]string{"status": "enabled"},
	}, 0); err != nil {
		t.Fatalf("PutResource automation: %v", err)
	}

	pool, err := worker.New(svc, worker.Options{Owner: "pool-repair", Now: clock.Now})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}
	if err := pool.RepairOnce(ctx); err != nil {
		t.Fatalf("RepairOnce: %v", err)
	}

	leases, err := backend.ListTaskLeases(ctx, "")
	if err != nil {
		t.Fatalf("ListTaskLeases: %v", err)
	}
	byRun := map[string]docstore.TaskLease{}
	for _, l := range leases {
		if l.UserID == unknownUserID {
			t.Fatalf("expected unknown user orphan lease deleted, found %+v", l)
		}
		byRun[l.RunID] = l
	}
	if _, exists := byRun[orphanRunID]; exists {
		t.Fatalf("expected orphan run lease %s to be deleted", orphanRunID)
	}
	if l, ok := byRun[missingLeaseRunID]; !ok || !l.Runnable {
		t.Fatalf("expected missing runnable lease recreated, got ok=%v lease=%+v", ok, l)
	}
	for i, rid := range stuckRunIDs {
		l, ok := byRun[rid]
		if !ok || !l.Runnable || l.WaitingReason != docstore.WaitNone {
			t.Fatalf("stuck reason %s was not re-woken: ok=%v lease=%+v", stuckReasons[i], ok, l)
		}
	}

	if l, ok := byRun[legitRoutingRunID]; !ok || l.Runnable || l.WaitingReason != docstore.WaitRoutingPending {
		t.Fatalf("expected u2 legit routing_pending task to remain parked while RoutingPending=true, got ok=%v lease=%+v", ok, l)
	}

	userWakes1, err := backend.ListUserWakes(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListUserWakes u1: %v", err)
	}
	wakes1ByLane := map[string]docstore.UserWake{}
	for _, w := range userWakes1 {
		wakes1ByLane[w.Lane] = w
	}
	if sw, ok := wakes1ByLane[docstore.LaneScheduler]; !ok || !sw.Runnable || sw.WakeAt.After(nextDue) {
		t.Fatalf("expected scheduler wake <= %s, got ok=%v wake=%+v", nextDue, ok, sw)
	}

	userWakes2, err := backend.ListUserWakes(ctx, u2.ID)
	if err != nil {
		t.Fatalf("ListUserWakes u2: %v", err)
	}
	wakes2ByLane := map[string]docstore.UserWake{}
	for _, w := range userWakes2 {
		wakes2ByLane[w.Lane] = w
	}
	if cw, ok := wakes2ByLane[docstore.LaneConversation]; !ok || !cw.Runnable {
		t.Fatalf("expected u2 conversation wake re-armed, got ok=%v wake=%+v", ok, cw)
	}

	pausedStopRunID := uuid.NewString()
	if _, err := userApp.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: pausedStopRunID, Kind: state.CommandStart, Text: "stop while paused",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch start: %v", err)
	}
	if err := backend.Apply(ctx, docstore.Mutation{
		Kind:   "set",
		Path:   "system/control",
		Fields: map[string]any{"paused": true},
	}); err != nil {
		t.Fatalf("set paused: %v", err)
	}
	if _, err := userApp.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: pausedStopRunID, Kind: state.CommandStop,
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch stop: %v", err)
	}
	if err := pool.RepairOnce(ctx); err != nil {
		t.Fatalf("RepairOnce while paused: %v", err)
	}
	stoppedRun, err := userApp.Store.GetRun(ctx, pausedStopRunID)
	if err != nil {
		t.Fatalf("GetRun pausedStopRunID: %v", err)
	}
	if stoppedRun.Status != state.RunCancelled || stoppedRun.CleanupPending {
		t.Fatalf("expected paused stopped run cancelled & clean, got status=%s cleanup=%v", stoppedRun.Status, stoppedRun.CleanupPending)
	}
	leasesAfterPause, err := backend.ListTaskLeases(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListTaskLeases after pause: %v", err)
	}
	for _, l := range leasesAfterPause {
		if l.RunID == pausedStopRunID {
			t.Fatalf("expected lease for paused stopped run to be deleted, found %+v", l)
		}
	}
}

func TestPoolGracefulShutdownDrainAndTimeout(t *testing.T) {
	t.Run("drains cleanly before DrainTimeout", func(t *testing.T) {
		ctx := context.Background()
		clock := newTestClock(time.Now().UTC().Add(time.Second))
		svc, backend := newPoolTestEnv(t, clock)
		u := mustCreateUser(t, svc, uuid.NewString(), "drain@example.com")
		uApp, rel, err := svc.Acquire(ctx, u)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		defer rel()

		started := make(chan struct{})
		sawDrain := make(chan struct{})
		var workCtxCanceledBeforeDrain atomic.Bool

		pool, err := worker.New(svc, worker.Options{
			Owner:          "pool-drain",
			PollInterval:   20 * time.Millisecond,
			RepairInterval: time.Hour,
			DrainTimeout:   2 * time.Second,
			Now:            clock.Now,
			RunTask: func(workCtx context.Context, userApp *app.App, claim docstore.TaskLease, drain <-chan struct{}) (task.RunOutcome, error) {
				close(started)
				<-drain
				if workCtx.Err() != nil {
					workCtxCanceledBeforeDrain.Store(true)
				}
				close(sawDrain)
				_ = backend.ReleaseTask(workCtx, claim.UserID, claim.RunID, "pool-drain", 0, time.Time{})
				return task.OutcomeReleased, nil
			},
		})
		if err != nil {
			t.Fatalf("worker.New: %v", err)
		}

		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- pool.Run(runCtx) }()

		rid := uuid.NewString()
		if _, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: rid, Kind: state.CommandStart, Text: "drain test",
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}

		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("task never started")
		}

		cancel()
		select {
		case <-sawDrain:
		case <-time.After(2 * time.Second):
			t.Fatalf("task never observed drainCh close")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("pool.Run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("pool.Run did not exit after clean drain")
		}
		if workCtxCanceledBeforeDrain.Load() {
			t.Fatalf("workCtx was canceled before DrainTimeout expired")
		}
	})

	t.Run("cancels workCtx after DrainTimeout", func(t *testing.T) {
		ctx := context.Background()
		clock := newTestClock(time.Now().UTC().Add(time.Second))
		svc, backend := newPoolTestEnv(t, clock)
		u := mustCreateUser(t, svc, uuid.NewString(), "timeout@example.com")
		uApp, rel, err := svc.Acquire(ctx, u)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		defer rel()

		started := make(chan struct{})
		var sawWorkCancel atomic.Bool

		pool, err := worker.New(svc, worker.Options{
			Owner:          "pool-timeout",
			PollInterval:   20 * time.Millisecond,
			RepairInterval: time.Hour,
			DrainTimeout:   60 * time.Millisecond,
			CancelGrace:    time.Second,
			Now:            clock.Now,
			RunTask: func(workCtx context.Context, userApp *app.App, claim docstore.TaskLease, drain <-chan struct{}) (task.RunOutcome, error) {
				close(started)
				<-workCtx.Done()
				sawWorkCancel.Store(true)
				finCtx, finCancel := context.WithTimeout(context.WithoutCancel(workCtx), time.Second)
				defer finCancel()
				_ = backend.ReleaseTask(finCtx, claim.UserID, claim.RunID, "pool-timeout", 0, time.Time{})
				return task.OutcomeReleased, nil
			},
		})
		if err != nil {
			t.Fatalf("worker.New: %v", err)
		}

		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- pool.Run(runCtx) }()

		rid := uuid.NewString()
		if _, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: rid, Kind: state.CommandStart, Text: "timeout test",
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("task never started")
		}

		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("pool.Run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("pool.Run did not exit after DrainTimeout cancellation")
		}
		if !sawWorkCancel.Load() {
			t.Fatalf("expected workCtx to cancel after DrainTimeout")
		}
		leases, err := backend.ListTaskLeases(ctx, u.ID)
		if err != nil || len(leases) != 1 || leases[0].Owner != "" {
			t.Fatalf("expected lease released via detached context after cancel, got %+v (err=%v)", leases, err)
		}
	})
}

type poolFakeAgentModel struct {
	fn func(ctx context.Context, req agent.Request) (agent.Response, error)
}

func (m *poolFakeAgentModel) Provider() string { return "sim" }
func (m *poolFakeAgentModel) Name() string     { return "pool-fake-model" }
func (m *poolFakeAgentModel) Respond(ctx context.Context, req agent.Request) (agent.Response, error) {
	return m.fn(ctx, req)
}
func (m *poolFakeAgentModel) Inspect(context.Context, agent.InspectRequest) (agent.Inspection, error) {
	return agent.Inspection{Observation: "ok", CostKnown: true}, nil
}

func completeModelResp(callID, answer string) agent.Response {
	call := transcript.ToolCall{
		ID:   callID,
		Name: "complete",
		Args: map[string]any{
			"answer":   answer,
			"evidence": []any{},
			"unmet":    []any{},
		},
	}
	return agent.Response{
		Message:   &transcript.Message{Role: transcript.RoleModel, Parts: []transcript.Part{{ToolCall: &call}}},
		Calls:     []transcript.ToolCall{call},
		Cost:      0.01,
		CostKnown: true,
	}
}

func TestPoolSlowConversationAndSchedulerBeyondCancelGrace(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, backend := newPoolTestEnv(t, clock)
	u := mustCreateUser(t, svc, uuid.NewString(), "slow@example.com")

	convStarted := make(chan struct{})
	schedStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	convDone := make(chan struct{})
	schedDone := make(chan struct{})
	renewHub := &manualTickerHub{}

	pool, err := worker.New(svc, worker.Options{
		Owner:                   "pool-slow-grace",
		ConversationConcurrency: 1,
		SchedulerConcurrency:    1,
		PollInterval:            15 * time.Millisecond,
		RepairInterval:          time.Hour,
		RenewInterval:           30 * time.Millisecond,
		DrainTimeout:            2 * time.Second,
		CancelGrace:             25 * time.Millisecond,
		Now:                     clock.Now,
		NewTicker: func(d time.Duration) (<-chan time.Time, func()) {
			if d == 30*time.Millisecond {
				return renewHub.NewTicker(d)
			}
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
		RunConversation: func(ctx context.Context, _ *app.App) error {
			close(convStarted)
			select {
			case <-releaseSlow:
			case <-ctx.Done():
				return ctx.Err()
			}
			close(convDone)
			return nil
		},
		RunScheduler: func(ctx context.Context, _ *app.App) (*time.Time, error) {
			close(schedStarted)
			select {
			case <-releaseSlow:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			close(schedDone)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()
	if err := backend.WaitForWakeSubscribers(ctx, 1); err != nil {
		t.Fatalf("WaitForWakeSubscribers: %v", err)
	}

	if err := backend.WakeUser(ctx, u.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser conv: %v", err)
	}
	if err := backend.WakeUser(ctx, u.ID, docstore.LaneScheduler, time.Time{}); err != nil {
		t.Fatalf("WakeUser sched: %v", err)
	}

	select {
	case <-convStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for slow conversation start")
	}
	select {
	case <-schedStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for slow scheduler start")
	}
	// Advance past CancelGrace and fire renewal ticks to confirm leases renew cleanly.
	clock.Advance(100 * time.Millisecond)
	renewHub.Tick(clock.Now())
	renewHub.Tick(clock.Now())
	close(releaseSlow)

	select {
	case <-convDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for slow conversation turn")
	}
	select {
	case <-schedDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for slow scheduler turn")
	}

	waitForCondition(t, 2*time.Second, func() bool {
		wakes, err := backend.ListUserWakes(ctx, u.ID)
		return err == nil && len(wakes) == 2 &&
			!wakes[0].Runnable && wakes[0].Owner == "" && wakes[0].LeaseUntil == nil &&
			!wakes[1].Runnable && wakes[1].Owner == "" && wakes[1].LeaseUntil == nil
	})
	wakes, err := backend.ListUserWakes(ctx, u.ID)
	if err != nil || len(wakes) != 2 {
		t.Fatalf("expected 2 wakes, got %+v (err=%v)", wakes, err)
	}
	for _, w := range wakes {
		if w.Runnable || w.Owner != "" || w.LeaseUntil != nil {
			t.Fatalf("expected wake %s cleanly parked after turn > CancelGrace, got %+v", w.Lane, w)
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
}

func TestPoolRunNowOnceAutomationSettlementAndDeferredAnswer(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now().UTC().Add(time.Second))
	svc, backend := newPoolTestEnv(t, clock)
	u := mustCreateUser(t, svc, users.LegacyID, "auto@example.com")

	var deferTaskID atomic.Value
	deferTaskID.Store("")
	modelEntered := make(chan struct{})
	var modelEnteredOnce sync.Once
	allowDeferredComplete := make(chan struct{})
	svc.ConfigureApp = func(_ users.User, a *app.App) {
		if a.Scheduler != nil {
			a.Scheduler.Now = clock.Now
		}
		if a.Assistant != nil && a.Assistant.Permissions != nil {
			a.Assistant.Permissions.Now = clock.Now
		}
		if a.TaskActivities != nil && a.TaskActivities.Agent != nil {
			a.TaskActivities.Agent = agent.New(a.Store, &poolFakeAgentModel{
				fn: func(mCtx context.Context, req agent.Request) (agent.Response, error) {
					if target, _ := deferTaskID.Load().(string); target != "" {
						if run, err := a.Store.GetRun(mCtx, target); err == nil && run.Status == state.RunRunning {
							modelEnteredOnce.Do(func() { close(modelEntered) })
							select {
							case <-allowDeferredComplete:
							case <-mCtx.Done():
								return agent.Response{NotSent: true}, mCtx.Err()
							}
						}
					}
					return completeModelResp("done-1", "Completed cleanly via Postgres-native runtime."), nil
				},
			})
		}
	}

	userApp, release, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	pool, err := worker.New(svc, worker.Options{
		Owner:                   "pool-real-sched",
		TaskConcurrency:         2,
		ConversationConcurrency: 1,
		SchedulerConcurrency:    1,
		PollInterval:            time.Hour,
		RepairInterval:          time.Hour,
		DrainTimeout:            3 * time.Second,
		Now:                     clock.Now,
		RunConversation: func(context.Context, *app.App) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	if err := backend.WaitForWakeSubscribers(ctx, 1); err != nil {
		t.Fatalf("WaitForWakeSubscribers: %v", err)
	}

	farFuture := clock.Now().Add(48 * time.Hour)
	saveReq := uuid.NewString()
	saveRcpt, err := userApp.Scheduler.Save(ctx, "", saveReq, 0, scheduler.Editable{
		Title:          "One-shot status report",
		Instruction:    "Compile one-shot status report.",
		Trigger:        scheduler.Trigger{Kind: "once", At: &farFuture},
		MissedRun:      "skip",
		MaxActiveRuns:  1,
		BudgetUSD:      0.5,
		DestinationIDs: []string{scheduler.InAppDestination},
	})
	if err != nil {
		t.Fatalf("Scheduler.Save: %v", err)
	}
	var savedAuto scheduler.Automation
	if err := json.Unmarshal(saveRcpt.Resource, &savedAuto); err != nil {
		t.Fatalf("unmarshal automation: %v", err)
	}

	actRcpt, err := userApp.Scheduler.Act(ctx, savedAuto.ID, uuid.NewString(), savedAuto.Revision, scheduler.Action{Action: "run_now"})
	if err != nil {
		t.Fatalf("Scheduler.Act(run_now): %v", err)
	}
	if actRcpt.Work == nil || actRcpt.Work.TaskID == "" {
		t.Fatalf("expected run_now Work.TaskID, got %+v", actRcpt)
	}

	var inbox []scheduler.InboxItem
	var occs []scheduler.Occurrence
	waitForCondition(t, 5*time.Second, func() bool {
		items, ierr := userApp.Scheduler.Inbox(ctx, false)
		curOccs, oerr := userApp.Scheduler.Occurrences(ctx, savedAuto.ID)
		if ierr == nil && oerr == nil && len(items) == 1 && len(curOccs) == 1 && curOccs[0].Status == scheduler.OccurrenceCompleted {
			inbox = items
			occs = curOccs
			return true
		}
		return false
	})
	if len(inbox) != 1 || len(occs) != 1 || occs[0].Status != "completed" {
		curOccs, _ := userApp.Scheduler.Occurrences(ctx, savedAuto.ID)
		run, _ := userApp.Store.GetRun(ctx, actRcpt.Work.TaskID)
		t.Fatalf("expected 1 delivered InboxItem and settled completed occurrence after run_now+Publish wake, got inbox=%d occs=%+v (run=%+v)", len(inbox), curOccs, run)
	}

	taskID := uuid.NewString()
	deferTaskID.Store(taskID)
	if _, err := userApp.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: taskID, Kind: state.CommandStart, Text: "Task with pre-ask answer",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch deferTask: %v", err)
	}
	select {
	case <-modelEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for deferred-answer task to enter ModelRef")
	}
	defer func() {
		select {
		case <-allowDeferredComplete:
		default:
			close(allowDeferredComplete)
		}
	}()
	interID := "int_" + uuid.NewString()[:8]
	rev1 := int64(1)
	inter, err := userApp.Assistant.Permissions.Open(ctx, permissions.Interaction{
		ID:                  interID,
		TaskID:              taskID,
		InstructionRevision: &rev1,
		Kind:                "question",
		Prompt:              "Ready to proceed?",
	}, "question")
	if err != nil {
		t.Fatalf("Permissions.Open: %v", err)
	}

	reqBody, _ := json.Marshal(map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": inter.Revision,
		"value": map[string]any{
			"kind": "answer",
			"text": "Yes, proceed now",
		},
	})
	httpReq := httptest.NewRequest(http.MethodPost, "/users/"+u.ID+"/v1/interactions/"+inter.ID+"/responses", bytes.NewReader(reqBody))
	httpReq.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	svc.Handler(true).ServeHTTP(rec, httpReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/interactions/%s/responses: status=%d body=%s", inter.ID, rec.Code, rec.Body.String())
	}

	wakes, err := backend.ListUserWakes(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListUserWakes: %v", err)
	}
	foundSchedWake := false
	for _, w := range wakes {
		if w.Lane == docstore.LaneScheduler && w.Runnable && !w.WakeAt.After(clock.Now().Add(3*time.Second)) {
			foundSchedWake = true
		}
	}
	if !foundSchedWake {
		t.Fatalf("expected ScheduleAnswerRetry to arm LaneScheduler within 3s, got %+v", wakes)
	}

	if err := userApp.TaskActivities.Store.ProjectQuestion(ctx, taskID, 1, state.Question{
		ID:       inter.ID,
		Revision: 1,
		Kind:     "input",
		Text:     "Ready to proceed?",
	}); err != nil {
		t.Fatalf("ProjectQuestion(%s): %v", inter.ID, err)
	}

	clock.Advance(3 * time.Second)
	backend.EmitResync()

	resolved := waitForCondition(t, 5*time.Second, func() bool {
		r, rerr := userApp.Store.GetRun(ctx, taskID)
		if rerr != nil {
			return false
		}
		if r.Question != nil && r.Question.ResolvedBy != "" {
			return true
		}
		if r.Position >= 2 && r.Incorporated >= 2 {
			if undeliv, _, uerr := userApp.Assistant.Permissions.UndeliveredAnswers(ctx); uerr == nil && len(undeliv) == 0 {
				return true
			}
		}
		return false
	})
	close(allowDeferredComplete)
	if !resolved {
		r, _ := userApp.Store.GetRun(ctx, taskID)
		ans, _, _ := userApp.Assistant.Permissions.UndeliveredAnswers(ctx)
		t.Fatalf("expected LaneScheduler retry to resolve deferred answer for question %s, got run=%+v undelivered=%+v", inter.ID, r, ans)
	}

	var finalRun state.Run
	waitForCondition(t, 5*time.Second, func() bool {
		r, rerr := userApp.Store.GetRun(ctx, taskID)
		if rerr == nil && r.Status == state.RunCompleted && !r.CleanupPending {
			finalRun = r
			return true
		}
		return false
	})
	if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
		r, _ := userApp.Store.GetRun(ctx, taskID)
		t.Fatalf("expected deferred-answer task to complete cleanly, got run=%+v", r)
	}
}

func TestPoolLostNotifyAndResyncRecovery(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now().UTC().Add(time.Second))
	svc, backend := newPoolTestEnv(t, clock)
	u := mustCreateUser(t, svc, uuid.NewString(), "lostnotify@example.com")
	uApp, rel, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer rel()

	t.Run("fallback_poll_recovers_dropped_notify", func(t *testing.T) {
		backend.SetDropWakeEvents(true)
		defer backend.SetDropWakeEvents(false)

		taskDone := make(chan struct{})
		convDone := make(chan struct{})

		pool, err := worker.New(svc, worker.Options{
			Owner:          "pool-fallback",
			PollInterval:   20 * time.Millisecond,
			RepairInterval: time.Hour,
			DrainTimeout:   2 * time.Second,
			Now:            clock.Now,
			RunConversation: func(ctx context.Context, _ *app.App) error {
				select {
				case <-convDone:
				default:
					close(convDone)
				}
				return nil
			},
			RunTask: func(ctx context.Context, _ *app.App, claim docstore.TaskLease, _ <-chan struct{}) (task.RunOutcome, error) {
				_, _ = backend.DeleteTaskLease(ctx, claim.UserID, claim.RunID, 0)
				select {
				case <-taskDone:
				default:
					close(taskDone)
				}
				return task.OutcomeDone, nil
			},
		})
		if err != nil {
			t.Fatalf("worker.New: %v", err)
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- pool.Run(runCtx) }()
		if err := backend.WaitForWakeSubscribers(ctx, 1); err != nil {
			t.Fatalf("WaitForWakeSubscribers: %v", err)
		}

		if _, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: uuid.NewString(), Kind: state.CommandStart, Text: "dropped notify task",
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}
		if err := backend.WakeUser(ctx, u.ID, docstore.LaneConversation, time.Time{}); err != nil {
			t.Fatalf("WakeUser: %v", err)
		}

		select {
		case <-taskDone:
		case <-time.After(2 * time.Second):
			t.Fatal("fallback poll did not recover task when pg_notify was dropped")
		}
		select {
		case <-convDone:
		case <-time.After(2 * time.Second):
			t.Fatal("fallback poll did not recover conversation wake when pg_notify was dropped")
		}

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("pool.Run: %v", err)
		}
	})

	t.Run("emit_resync_wakes_pool_and_active_task_subscribers", func(t *testing.T) {
		backend.SetDropWakeEvents(true)
		defer backend.SetDropWakeEvents(false)

		taskRan := make(chan struct{})
		pool, err := worker.New(svc, worker.Options{
			Owner:          "pool-resync",
			PollInterval:   time.Hour,
			RepairInterval: time.Hour,
			DrainTimeout:   2 * time.Second,
			Now:            clock.Now,
			RunTask: func(ctx context.Context, _ *app.App, claim docstore.TaskLease, _ <-chan struct{}) (task.RunOutcome, error) {
				_, _ = backend.DeleteTaskLease(ctx, claim.UserID, claim.RunID, 0)
				select {
				case <-taskRan:
				default:
					close(taskRan)
				}
				return task.OutcomeDone, nil
			},
		})
		if err != nil {
			t.Fatalf("worker.New: %v", err)
		}
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- pool.Run(runCtx) }()
		if err := backend.WaitForWakeSubscribers(ctx, 1); err != nil {
			t.Fatalf("WaitForWakeSubscribers: %v", err)
		}

		subCh, unsub := pool.Subscribe(u.ID, "active-run")
		defer unsub()

		if _, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: uuid.NewString(), Kind: state.CommandStart, Text: "missed wake before resync",
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}
		runtime.Gosched()
		expectNoSignal(t, subCh, "subscriber should not fire while wakes are dropped")

		backend.EmitResync()
		expectSignal(t, subCh, "active task subscriber on resync")
		select {
		case <-taskRan:
		case <-time.After(2 * time.Second):
			t.Fatal("EmitResync did not wake pool claim loop")
		}

		cancel()
		if err := <-done; err != nil {
			t.Fatalf("pool.Run: %v", err)
		}
	})
}

type advisoryLockBackend struct {
	*memstore.Backend
	mu        sync.Mutex
	held      map[int64]bool
	lastKey   int64
	acquired  int
	skipped   int
	unlocked  int
	injectErr error
}

func newAdvisoryLockBackend(clock *testClock) *advisoryLockBackend {
	return &advisoryLockBackend{
		Backend: memstore.New(memstore.WithClock(clock.Now)),
		held:    map[int64]bool{},
	}
}

func (b *advisoryLockBackend) TryAdvisoryLock(_ context.Context, key int64) (func(), bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastKey = key
	if b.injectErr != nil {
		return nil, false, b.injectErr
	}
	if b.held[key] {
		b.skipped++
		return nil, false, nil
	}
	b.held[key] = true
	b.acquired++
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.held, key)
			b.unlocked++
			b.mu.Unlock()
		})
	}, true, nil
}

func TestPoolRepairOncePreservesHotCache(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, backend := newPoolTestEnv(t, clock)
	svc.MaxCachedApps = 2

	uA := mustCreateUser(t, svc, uuid.NewString(), "pool-hot-a@example.com")
	uB := mustCreateUser(t, svc, uuid.NewString(), "pool-hot-b@example.com")

	coldUsers := make([]users.User, 8)
	for i := range coldUsers {
		coldUsers[i] = mustCreateUser(t, svc, uuid.NewString(), fmt.Sprintf("pool-cold-%d@example.com", i))
	}

	coldApp, relCold, err := svc.Acquire(ctx, coldUsers[0])
	if err != nil {
		t.Fatalf("Acquire cold[0]: %v", err)
	}
	missingRunID := uuid.NewString()
	if _, err := coldApp.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: missingRunID, Kind: state.CommandStart, Text: "needs repair",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}
	if _, err := backend.DeleteTaskLease(ctx, coldUsers[0].ID, missingRunID, 0); err != nil {
		t.Fatalf("DeleteTaskLease: %v", err)
	}
	relCold()

	clock.Advance(time.Second)
	appA, relA, err := svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	relA()
	clock.Advance(time.Second)
	appB, relB, err := svc.Acquire(ctx, uB)
	if err != nil {
		t.Fatalf("Acquire B: %v", err)
	}
	relB()

	if svc.CachedCount() != 2 {
		t.Fatalf("CachedCount before RepairOnce = %d, want 2", svc.CachedCount())
	}

	pool, err := worker.New(svc, worker.Options{Owner: "pool-cache-safe", Now: clock.Now})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}
	if err := pool.RepairOnce(ctx); err != nil {
		t.Fatalf("RepairOnce: %v", err)
	}

	if svc.CachedCount() != 2 {
		t.Fatalf("CachedCount after RepairOnce = %d, want 2", svc.CachedCount())
	}
	gotA, relGotA, err := svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("Acquire A after RepairOnce: %v", err)
	}
	relGotA()
	gotB, relGotB, err := svc.Acquire(ctx, uB)
	if err != nil {
		t.Fatalf("Acquire B after RepairOnce: %v", err)
	}
	relGotB()
	if gotA != appA || gotB != appB {
		t.Fatalf("expected hot users A & B to remain exact same cached *App pointers after RepairOnce")
	}

	leases, err := backend.ListTaskLeases(ctx, coldUsers[0].ID)
	if err != nil {
		t.Fatalf("ListTaskLeases: %v", err)
	}
	if len(leases) != 1 || leases[0].RunID != missingRunID || !leases[0].Runnable {
		t.Fatalf("expected cold user missing lease repaired, got %+v", leases)
	}
}

func TestPoolRepairOnceLeaderElection(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	lockerBackend := newAdvisoryLockBackend(clock)
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(lockerBackend, func(users.User) state.ArtifactStore {
		return artifacts
	})
	cfg := settings.Settings{
		Cloud:         "local",
		Role:          "worker",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Models: settings.Models{
			OpenAIKey:     "test-openai-key",
			OpenRouterKey: "test-openrouter-key",
			Outer:         "gpt-6.1-sol",
			Inner:         "openai/gpt-6.1-terra",
		},
	}
	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	t.Cleanup(svc.Close)

	mustCreateUser(t, svc, uuid.NewString(), "leader-test@example.com")

	inSweep := make(chan struct{})
	releaseSweep := make(chan struct{})
	var sweepExecutions atomic.Int32
	var blockFirst atomic.Bool
	blockFirst.Store(true)

	svc.ConfigureApp = func(_ users.User, _ *app.App) {
		sweepExecutions.Add(1)
		if blockFirst.CompareAndSwap(true, false) {
			close(inSweep)
			<-releaseSweep
		}
	}

	pool1, err := worker.New(svc, worker.Options{Owner: "replica-1", Now: clock.Now})
	if err != nil {
		t.Fatalf("worker.New pool1: %v", err)
	}
	pool2, err := worker.New(svc, worker.Options{Owner: "replica-2", Now: clock.Now})
	if err != nil {
		t.Fatalf("worker.New pool2: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- pool1.RepairOnce(ctx) }()

	select {
	case <-inSweep:
	case <-time.After(2 * time.Second):
		t.Fatalf("replica-1 never entered sweep")
	}

	if err := pool2.RepairOnce(ctx); err != nil {
		t.Fatalf("replica-2 RepairOnce while lock held: %v", err)
	}
	if sweepExecutions.Load() != 1 {
		t.Fatalf("expected only 1 sweep execution while lock held, got %d", sweepExecutions.Load())
	}

	close(releaseSweep)
	if err := <-errCh; err != nil {
		t.Fatalf("replica-1 RepairOnce: %v", err)
	}

	if err := pool2.RepairOnce(ctx); err != nil {
		t.Fatalf("replica-2 RepairOnce after release: %v", err)
	}
	if sweepExecutions.Load() != 2 {
		t.Fatalf("expected 2nd sweep after release, got %d", sweepExecutions.Load())
	}

	lockerBackend.mu.Lock()
	lastKey := lockerBackend.lastKey
	acquired := lockerBackend.acquired
	skipped := lockerBackend.skipped
	unlocked := lockerBackend.unlocked
	lockerBackend.mu.Unlock()

	if lastKey == 0 || lastKey == docstore.MigrationAdvisoryLockKey {
		t.Fatalf("expected dedicated repair advisory lock key distinct from migration lock (%d), got %d", docstore.MigrationAdvisoryLockKey, lastKey)
	}
	if acquired != 2 || skipped != 1 || unlocked != 2 {
		t.Fatalf("advisory lock counts: acquired=%d skipped=%d unlocked=%d, want 2/1/2", acquired, skipped, unlocked)
	}

	wantErr := errors.New("advisory lock db error")
	lockerBackend.mu.Lock()
	lockerBackend.injectErr = wantErr
	lockerBackend.mu.Unlock()
	if err := pool1.RepairOnce(ctx); !errors.Is(err, wantErr) {
		t.Fatalf("RepairOnce error = %v, want %v", err, wantErr)
	}
}

func TestPoolLaneStatsConcurrentWork(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now().UTC().Add(time.Second))
	svc, backend := newPoolTestEnv(t, clock)

	u1 := mustCreateUser(t, svc, uuid.NewString(), "lanes-1@example.com")
	u2 := mustCreateUser(t, svc, uuid.NewString(), "lanes-2@example.com")
	app1, rel1, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1: %v", err)
	}
	defer rel1()

	taskStarted := make(chan struct{}, 2)
	convStarted := make(chan struct{}, 1)
	schedStarted := make(chan struct{}, 1)
	releaseAll := make(chan struct{})

	pool, err := worker.New(svc, worker.Options{
		Owner:                   "pool-lanes",
		TaskConcurrency:         3,
		ConversationConcurrency: 2,
		SchedulerConcurrency:    4,
		PollInterval:            20 * time.Millisecond,
		RepairInterval:          time.Hour,
		DrainTimeout:            2 * time.Second,
		Now:                     clock.Now,
		RunTask: func(ctx context.Context, _ *app.App, claim docstore.TaskLease, _ <-chan struct{}) (task.RunOutcome, error) {
			taskStarted <- struct{}{}
			<-releaseAll
			_, _ = backend.DeleteTaskLease(ctx, claim.UserID, claim.RunID, 0)
			return task.OutcomeDone, nil
		},
		RunConversation: func(ctx context.Context, _ *app.App) error {
			convStarted <- struct{}{}
			<-releaseAll
			return nil
		},
		RunScheduler: func(ctx context.Context, _ *app.App) (*time.Time, error) {
			schedStarted <- struct{}{}
			<-releaseAll
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	initial := pool.LaneStats()
	if initial.TaskCapacity != 3 || initial.ConversationCapacity != 2 || initial.SchedulerCapacity != 4 {
		t.Fatalf("initial LaneStats capacities = %+v, want 3/2/4", initial)
	}
	if initial.TaskActive != 0 || initial.ConversationActive != 0 || initial.SchedulerActive != 0 {
		t.Fatalf("initial LaneStats active = %+v, want 0/0/0", initial)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()

	for range 2 {
		if _, err := app1.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: uuid.NewString(), Kind: state.CommandStart, Text: "lane task",
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}
	}
	if err := backend.WakeUser(ctx, u1.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser conv: %v", err)
	}
	if err := backend.WakeUser(ctx, u2.ID, docstore.LaneScheduler, time.Time{}); err != nil {
		t.Fatalf("WakeUser sched: %v", err)
	}

	for range 2 {
		select {
		case <-taskStarted:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for task lane start")
		}
	}
	select {
	case <-convStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for conversation lane start")
	}
	select {
	case <-schedStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scheduler lane start")
	}

	busy := pool.LaneStats()
	if busy.TaskActive != 2 || busy.ConversationActive != 1 || busy.SchedulerActive != 1 {
		t.Fatalf("active LaneStats = %+v, want task=2 conv=1 sched=1", busy)
	}

	close(releaseAll)
	waitForCondition(t, 2*time.Second, func() bool {
		st := pool.LaneStats()
		return st.TaskActive == 0 && st.ConversationActive == 0 && st.SchedulerActive == 0
	})
	after := pool.LaneStats()
	if after.TaskActive != 0 || after.ConversationActive != 0 || after.SchedulerActive != 0 {
		t.Fatalf("post-completion LaneStats = %+v, want 0/0/0", after)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
}

func TestPoolAutoGeneratedOwner(t *testing.T) {
	clock := newTestClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, _ := newPoolTestEnv(t, clock)

	p1, err := worker.New(svc, worker.Options{Now: clock.Now})
	if err != nil {
		t.Fatalf("worker.New p1: %v", err)
	}
	p2, err := worker.New(svc, worker.Options{Now: clock.Now})
	if err != nil {
		t.Fatalf("worker.New p2: %v", err)
	}
	if p1.Owner() == "" || p2.Owner() == "" {
		t.Fatalf("expected non-empty auto-generated owners: %q, %q", p1.Owner(), p2.Owner())
	}
	if p1.Owner() == p2.Owner() {
		t.Fatalf("expected unique auto-generated owners across pools, got %q", p1.Owner())
	}
}

type unscopedLeaseBackend struct {
	docstore.Backend
	docstore.LeaseStore
}

func TestPoolScopedUserRequiresScopedLeaseStore(t *testing.T) {
	clock := newTestClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	mem := memstore.New(memstore.WithClock(clock.Now))
	unscoped := &unscopedLeaseBackend{Backend: mem, LeaseStore: mem}
	storage := platform.NewStorage(unscoped, func(users.User) state.ArtifactStore {
		return memstore.NewArtifacts()
	})
	svc := app.OpenServiceWithStorage(storage, nil, settings.Settings{
		Cloud: "local", Role: "worker", TaskBudgetUSD: 1.0,
		Features: settings.Features{DisableJev: true},
		Models:   settings.Models{OpenAIKey: "k", Outer: "gpt-6.1-sol", Inner: "gpt-6.1-terra"},
	})
	t.Cleanup(svc.Close)

	if _, err := worker.New(svc, worker.Options{UserID: uuid.NewString(), Now: clock.Now}); err == nil {
		t.Fatal("expected worker.New with UserID and non-ScopedLeaseStore backend to fail")
	}
}

func TestPoolScopedUserIsolationWakeAndRepair(t *testing.T) {
	ctx := context.Background()
	clock := newTestClock(time.Now().UTC().Add(time.Second))
	svc, backend := newPoolTestEnv(t, clock)

	u1 := mustCreateUser(t, svc, uuid.NewString(), "scoped-u1@example.com")
	u2 := mustCreateUser(t, svc, uuid.NewString(), "scoped-u2@example.com")

	app1, rel1, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1: %v", err)
	}
	defer rel1()
	app2, rel2, err := svc.Acquire(ctx, u2)
	if err != nil {
		t.Fatalf("Acquire u2: %v", err)
	}
	defer rel2()

	var u1TaskDone, u1ConvDone, u1SchedDone atomic.Int32
	var u2Touched atomic.Int32

	pool, err := worker.New(svc, worker.Options{
		Owner:                   "scoped-worker-u1",
		UserID:                  u1.ID,
		TaskConcurrency:         2,
		ConversationConcurrency: 1,
		SchedulerConcurrency:    1,
		PollInterval:            15 * time.Millisecond,
		RepairInterval:          time.Hour,
		DrainTimeout:            2 * time.Second,
		Now:                     clock.Now,
		RunTask: func(ctx context.Context, _ *app.App, claim docstore.TaskLease, _ <-chan struct{}) (task.RunOutcome, error) {
			if claim.UserID != u1.ID {
				u2Touched.Add(1)
			} else {
				u1TaskDone.Add(1)
			}
			_, _ = backend.DeleteTaskLease(ctx, claim.UserID, claim.RunID, 0)
			return task.OutcomeDone, nil
		},
		RunConversation: func(_ context.Context, ua *app.App) error {
			if ua.UserID != u1.ID {
				u2Touched.Add(1)
			} else {
				u1ConvDone.Add(1)
			}
			return nil
		},
		RunScheduler: func(_ context.Context, ua *app.App) (*time.Time, error) {
			if ua.UserID != u1.ID {
				u2Touched.Add(1)
			} else {
				u1SchedDone.Add(1)
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New scoped: %v", err)
	}

	subU1, unsubU1 := pool.Subscribe(u1.ID, "r1")
	defer unsubU1()
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.TaskWakePayload(u2.ID, "r1")})
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.UserTasksWakePayload(u2.ID)})
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.UserWakePayload(u2.ID, docstore.LaneConversation)})
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.UserWakePayload(u2.ID, docstore.LaneScheduler)})
	expectNoSignal(t, subU1, "u2 wake events must not notify u1 subscriber")

	for _, payload := range []string{docstore.WakePayloadAllTask, docstore.WakePayloadPause, docstore.WakePayloadResync} {
		pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: payload})
		expectSignal(t, subU1, "global wake "+payload)
	}

	u2RunID := uuid.NewString()
	if _, err := app2.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: u2RunID, Kind: state.CommandStart, Text: "u2 work",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch u2: %v", err)
	}
	if err := backend.WakeUser(ctx, u2.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser u2 conv: %v", err)
	}
	if err := backend.WakeUser(ctx, u2.ID, docstore.LaneScheduler, time.Time{}); err != nil {
		t.Fatalf("WakeUser u2 sched: %v", err)
	}

	u1RunID := uuid.NewString()
	if _, err := app1.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: u1RunID, Kind: state.CommandStart, Text: "u1 work",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch u1: %v", err)
	}
	if err := backend.WakeUser(ctx, u1.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser u1 conv: %v", err)
	}
	if err := backend.WakeUser(ctx, u1.ID, docstore.LaneScheduler, time.Time{}); err != nil {
		t.Fatalf("WakeUser u1 sched: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()

	waitForCondition(t, 3*time.Second, func() bool {
		return u1TaskDone.Load() >= 1 && u1ConvDone.Load() >= 1 && u1SchedDone.Load() >= 1
	})
	if u1TaskDone.Load() < 1 || u1ConvDone.Load() < 1 || u1SchedDone.Load() < 1 {
		t.Fatalf("u1 scoped pool did not complete all lanes: task=%d conv=%d sched=%d",
			u1TaskDone.Load(), u1ConvDone.Load(), u1SchedDone.Load())
	}
	runtime.Gosched()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
	if u2Touched.Load() != 0 {
		t.Fatalf("scoped pool for u1 touched u2 %d times", u2Touched.Load())
	}

	u2Tasks, err := backend.ListTaskLeases(ctx, u2.ID)
	if err != nil || len(u2Tasks) != 1 || !u2Tasks[0].Runnable || u2Tasks[0].Owner != "" {
		t.Fatalf("u2 task lease was modified by u1 pool: %+v (err=%v)", u2Tasks, err)
	}
	u2Wakes, err := backend.ListUserWakes(ctx, u2.ID)
	if err != nil || len(u2Wakes) != 2 {
		t.Fatalf("u2 user wakes unexpected: %+v (err=%v)", u2Wakes, err)
	}
	for _, w := range u2Wakes {
		if !w.Runnable || w.Owner != "" {
			t.Fatalf("u2 user wake was modified by u1 pool: %+v", w)
		}
	}

	u1MissingRun := uuid.NewString()
	if _, err := app1.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: u1MissingRun, Kind: state.CommandStart, Text: "u1 missing lease",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch u1 missing: %v", err)
	}
	if _, err := backend.DeleteTaskLease(ctx, u1.ID, u1MissingRun, 0); err != nil {
		t.Fatalf("DeleteTaskLease u1: %v", err)
	}
	u2MissingRun := uuid.NewString()
	if _, err := app2.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: u2MissingRun, Kind: state.CommandStart, Text: "u2 missing lease",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch u2 missing: %v", err)
	}
	if _, err := backend.DeleteTaskLease(ctx, u2.ID, u2MissingRun, 0); err != nil {
		t.Fatalf("DeleteTaskLease u2: %v", err)
	}

	if err := pool.RepairOnce(ctx); err != nil {
		t.Fatalf("pool.RepairOnce: %v", err)
	}
	u1LeasesAfter, err := backend.ListTaskLeases(ctx, u1.ID)
	if err != nil {
		t.Fatalf("ListTaskLeases u1: %v", err)
	}
	foundU1Missing := false
	for _, l := range u1LeasesAfter {
		if l.RunID == u1MissingRun && l.Runnable {
			foundU1Missing = true
		}
	}
	if !foundU1Missing {
		t.Fatalf("expected u1 missing lease %s repaired, got %+v", u1MissingRun, u1LeasesAfter)
	}
	u2LeasesAfter, err := backend.ListTaskLeases(ctx, u2.ID)
	if err != nil || len(u2LeasesAfter) != 1 || u2LeasesAfter[0].RunID != u2RunID {
		t.Fatalf("expected u2 existing lease preserved and u2 missing lease untouched, got %+v (err=%v)", u2LeasesAfter, err)
	}

	missingUserID := uuid.NewString()
	if err := backend.WakeTask(ctx, missingUserID, "orphan-run", time.Time{}); err != nil {
		t.Fatalf("WakeTask missingUserID: %v", err)
	}
	poolMissing, err := worker.New(svc, worker.Options{
		Owner:  "scoped-missing-user",
		UserID: missingUserID,
		Now:    clock.Now,
	})
	if err != nil {
		t.Fatalf("worker.New poolMissing: %v", err)
	}
	if err := poolMissing.RepairOnce(ctx); err != nil {
		t.Fatalf("poolMissing.RepairOnce: %v", err)
	}
	if orphanLeases, err := backend.ListTaskLeases(ctx, missingUserID); err != nil || len(orphanLeases) != 0 {
		t.Fatalf("expected missingUserID orphan lease cleaned up, got %+v (err=%v)", orphanLeases, err)
	}
	if u2LeasesStill, err := backend.ListTaskLeases(ctx, u2.ID); err != nil || len(u2LeasesStill) != 1 {
		t.Fatalf("expected u2 lease untouched by missingUserID RepairOnce, got %+v (err=%v)", u2LeasesStill, err)
	}

	lockerBackend := newAdvisoryLockBackend(clock)
	lockerStorage := platform.NewStorage(lockerBackend, func(users.User) state.ArtifactStore {
		return memstore.NewArtifacts()
	})
	lockerSvc := app.OpenServiceWithStorage(lockerStorage, nil, settings.Settings{
		Cloud: "local", Role: "worker", TaskBudgetUSD: 1.0,
		Features: settings.Features{DisableJev: true},
		Models:   settings.Models{OpenAIKey: "k", Outer: "gpt-6.1-sol", Inner: "gpt-6.1-terra"},
	})
	lockerSvc.Now = clock.Now
	t.Cleanup(lockerSvc.Close)
	lu1 := mustCreateUser(t, lockerSvc, uuid.NewString(), "lock-u1@example.com")
	lu2 := mustCreateUser(t, lockerSvc, uuid.NewString(), "lock-u2@example.com")

	inU1Sweep := make(chan struct{})
	releaseU1Sweep := make(chan struct{})
	var u2Swept atomic.Bool
	lockerSvc.ConfigureApp = func(u users.User, _ *app.App) {
		if u.ID == lu1.ID {
			close(inU1Sweep)
			<-releaseU1Sweep
		} else if u.ID == lu2.ID {
			u2Swept.Store(true)
		}
	}
	lp1, err := worker.New(lockerSvc, worker.Options{Owner: "lp1", UserID: lu1.ID, Now: clock.Now})
	if err != nil {
		t.Fatalf("worker.New lp1: %v", err)
	}
	lp2, err := worker.New(lockerSvc, worker.Options{Owner: "lp2", UserID: lu2.ID, Now: clock.Now})
	if err != nil {
		t.Fatalf("worker.New lp2: %v", err)
	}
	u1SweepErr := make(chan error, 1)
	go func() { u1SweepErr <- lp1.RepairOnce(ctx) }()
	select {
	case <-inU1Sweep:
	case <-time.After(2 * time.Second):
		t.Fatal("lp1 never entered RepairOnce sweep")
	}
	if err := lp2.RepairOnce(ctx); err != nil {
		t.Fatalf("lp2.RepairOnce: %v", err)
	}
	close(releaseU1Sweep)
	if err := <-u1SweepErr; err != nil {
		t.Fatalf("lp1.RepairOnce: %v", err)
	}
	if !u2Swept.Load() {
		t.Fatal("expected lp2.RepairOnce to run concurrently without advisory lock contention with lp1")
	}
}

func TestPoolMaxPerUserConcurrencyCapAndNoStarvation(t *testing.T) {
	ctx := context.Background()
	// Use a real-time backend/pool clock so deferred leases become runnable naturally.
	backend := memstore.New()
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore {
		return artifacts
	})
	cfg := settings.Settings{
		Cloud:         "local",
		Role:          "worker",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Models: settings.Models{
			OpenAIKey:     "test-openai-key",
			OpenRouterKey: "test-openrouter-key",
			Outer:         "gpt-6.1-sol",
			Inner:         "openai/gpt-6.1-terra",
		},
	}
	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	t.Cleanup(svc.Close)

	u1 := mustCreateUser(t, svc, uuid.NewString(), "u1-cap@example.com")
	u2 := mustCreateUser(t, svc, uuid.NewString(), "u2-cap@example.com")

	app1, rel1, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1: %v", err)
	}
	defer rel1()
	app2, rel2, err := svc.Acquire(ctx, u2)
	if err != nil {
		t.Fatalf("Acquire u2: %v", err)
	}
	defer rel2()

	const (
		maxPerUser = 2
		u1Tasks    = 5
		u2Tasks    = 2
	)

	var (
		u1Active    atomic.Int32
		u1MaxActive atomic.Int32
		seenMu      sync.Mutex
		u1Seen      = map[string]bool{}
		u2Seen      = map[string]bool{}
	)
	u1Saturated := make(chan struct{})
	var saturatedOnce sync.Once
	releaseU1 := make(chan struct{})
	u2Done := make(chan struct{})
	var u2DoneOnce sync.Once
	allU1Done := make(chan struct{})
	var allU1DoneOnce sync.Once

	pool, err := worker.New(svc, worker.Options{
		Owner:                 "pool-per-user-cap",
		TaskConcurrency:       8,
		MaxPerUserConcurrency: maxPerUser,
		PollInterval:          15 * time.Millisecond,
		Backoff:               20 * time.Millisecond,
		RepairInterval:        time.Hour,
		DrainTimeout:          2 * time.Second,
		RunTask: func(ctx context.Context, userApp *app.App, claim docstore.TaskLease, drain <-chan struct{}) (task.RunOutcome, error) {
			if claim.UserID == u1.ID {
				cur := u1Active.Add(1)
				for {
					prev := u1MaxActive.Load()
					if cur <= prev || u1MaxActive.CompareAndSwap(prev, cur) {
						break
					}
				}
				if cur == maxPerUser {
					saturatedOnce.Do(func() { close(u1Saturated) })
				}
				// Hold the initial wave until u2 proves it is not starved.
				select {
				case <-releaseU1:
				case <-ctx.Done():
					u1Active.Add(-1)
					return task.OutcomeBackoff, ctx.Err()
				}
				u1Active.Add(-1)
				_, _ = backend.DeleteTaskLease(ctx, claim.UserID, claim.RunID, 0)
				seenMu.Lock()
				u1Seen[claim.RunID] = true
				n := len(u1Seen)
				seenMu.Unlock()
				if n == u1Tasks {
					allU1DoneOnce.Do(func() { close(allU1Done) })
				}
				return task.OutcomeDone, nil
			}

			// u2 tasks complete immediately without waiting on releaseU1.
			_, _ = backend.DeleteTaskLease(ctx, claim.UserID, claim.RunID, 0)
			seenMu.Lock()
			u2Seen[claim.RunID] = true
			n := len(u2Seen)
			seenMu.Unlock()
			if n == u2Tasks {
				u2DoneOnce.Do(func() { close(u2Done) })
			}
			return task.OutcomeDone, nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	// Enqueue 5 real tasks for u1 first (earlier wake_at than u2).
	for i := 0; i < u1Tasks; i++ {
		if _, err := app1.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: uuid.NewString(), Kind: state.CommandStart, Text: fmt.Sprintf("u1 task %d", i),
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch u1-%d: %v", i, err)
		}
	}
	// Enqueue 2 real tasks for u2 after u1's tasks.
	for i := 0; i < u2Tasks; i++ {
		if _, err := app2.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: uuid.NewString(), Kind: state.CommandStart, Text: fmt.Sprintf("u2 task %d", i),
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch u2-%d: %v", i, err)
		}
	}

	runCtx, cancel := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	go func() { runErr <- pool.Run(runCtx) }()

	// 1. Wait for u1 to reach its MaxPerUserConcurrency cap.
	select {
	case <-u1Saturated:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("u1 never reached MaxPerUserConcurrency=%d (active=%d)", maxPerUser, u1Active.Load())
	}

	// 2. While u1 is completely blocked at its cap, u2's tasks must still execute without starvation.
	select {
	case <-u2Done:
	case <-time.After(3 * time.Second):
		close(releaseU1)
		cancel()
		seenMu.Lock()
		n := len(u2Seen)
		seenMu.Unlock()
		t.Fatalf("u2 tasks starved while u1 was at MaxPerUserConcurrency (u2Completed=%d/%d)", n, u2Tasks)
	}

	// Verify u1 stayed strictly at or below MaxPerUserConcurrency while blocked.
	if got := u1MaxActive.Load(); got > maxPerUser {
		close(releaseU1)
		cancel()
		t.Fatalf("u1 peak concurrency = %d, want <= %d", got, maxPerUser)
	}

	// 3. Unblock u1 and verify all remaining deferred u1 tasks complete cleanly without ever exceeding the cap.
	close(releaseU1)
	select {
	case <-allU1Done:
	case <-time.After(5 * time.Second):
		cancel()
		seenMu.Lock()
		n := len(u1Seen)
		seenMu.Unlock()
		t.Fatalf("u1 deferred tasks did not finish (completed=%d/%d)", n, u1Tasks)
	}

	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}

	if got := u1MaxActive.Load(); got != maxPerUser {
		t.Fatalf("u1 peak concurrency = %d, want %d", got, maxPerUser)
	}
	seenMu.Lock()
	gotU1, gotU2 := len(u1Seen), len(u2Seen)
	seenMu.Unlock()
	if gotU1 != u1Tasks {
		t.Fatalf("u1 distinct completed = %d, want %d", gotU1, u1Tasks)
	}
	if gotU2 != u2Tasks {
		t.Fatalf("u2 distinct completed = %d, want %d", gotU2, u2Tasks)
	}
}

func TestPoolPerUserConcurrencySharedAcrossTaskAndConversationLanes(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore {
		return artifacts
	})
	cfg := settings.Settings{
		Cloud:         "local",
		Role:          "worker",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Models: settings.Models{
			OpenAIKey:     "test-openai-key",
			OpenRouterKey: "test-openrouter-key",
			Outer:         "gpt-6.1-sol",
			Inner:         "openai/gpt-6.1-terra",
		},
	}
	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	t.Cleanup(svc.Close)

	u1 := mustCreateUser(t, svc, uuid.NewString(), "u1-cross@example.com")
	u2 := mustCreateUser(t, svc, uuid.NewString(), "u2-cross@example.com")

	app1, rel1, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1: %v", err)
	}
	defer rel1()

	u1TaskRunning := make(chan struct{})
	var u1TaskOnce sync.Once
	releaseU1Task := make(chan struct{})
	u1ConvRan := make(chan struct{}, 1)
	u2ConvRan := make(chan struct{}, 1)
	var u1Active atomic.Int32
	var u1MaxActive atomic.Int32

	pool, err := worker.New(svc, worker.Options{
		Owner:                   "pool-cross-lane",
		TaskConcurrency:         4,
		ConversationConcurrency: 4,
		MaxPerUserConcurrency:   1,
		PollInterval:            15 * time.Millisecond,
		Backoff:                 20 * time.Millisecond,
		RepairInterval:          time.Hour,
		DrainTimeout:            2 * time.Second,
		RunTask: func(ctx context.Context, userApp *app.App, claim docstore.TaskLease, drain <-chan struct{}) (task.RunOutcome, error) {
			cur := u1Active.Add(1)
			for {
				prev := u1MaxActive.Load()
				if cur <= prev || u1MaxActive.CompareAndSwap(prev, cur) {
					break
				}
			}
			u1TaskOnce.Do(func() { close(u1TaskRunning) })
			select {
			case <-releaseU1Task:
			case <-ctx.Done():
				u1Active.Add(-1)
				return task.OutcomeBackoff, ctx.Err()
			}
			u1Active.Add(-1)
			_, _ = backend.DeleteTaskLease(ctx, claim.UserID, claim.RunID, claim.WakeSeq)
			return task.OutcomeDone, nil
		},
		RunConversation: func(ctx context.Context, userApp *app.App) error {
			if userApp.UserID == u1.ID {
				cur := u1Active.Add(1)
				for {
					prev := u1MaxActive.Load()
					if cur <= prev || u1MaxActive.CompareAndSwap(prev, cur) {
						break
					}
				}
				u1Active.Add(-1)
				select {
				case u1ConvRan <- struct{}{}:
				default:
				}
				return nil
			}
			select {
			case u2ConvRan <- struct{}{}:
			default:
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	if _, err := app1.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: uuid.NewString(), Kind: state.CommandStart, Text: "u1 background task",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch u1: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	go func() { runErr <- pool.Run(runCtx) }()

	// Wait for u1's task to hold its sole per-user concurrency slot.
	select {
	case <-u1TaskRunning:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("u1 task never started")
	}

	// Enqueue conversation wakes for both u1 and u2.
	if err := backend.WakeUser(ctx, u1.ID, docstore.LaneConversation, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatalf("WakeUser u1: %v", err)
	}
	if err := backend.WakeUser(ctx, u2.ID, docstore.LaneConversation, time.Now().UTC().Add(-500*time.Millisecond)); err != nil {
		t.Fatalf("WakeUser u2: %v", err)
	}

	// u2's conversation must execute immediately without being starved by u1.
	select {
	case <-u2ConvRan:
	case <-time.After(2 * time.Second):
		close(releaseU1Task)
		cancel()
		t.Fatal("u2 conversation starved while u1 held its per-user cap")
	}

	// Conversation lane is exempt from MaxPerUserConcurrency so u1's interactive
	// chat turn STILL runs immediately while u1's task lane is at cap.
	select {
	case <-u1ConvRan:
	case <-time.After(2 * time.Second):
		close(releaseU1Task)
		cancel()
		t.Fatal("u1 conversation blocked while u1 task lane was at MaxPerUserConcurrency")
	}

	// Release u1's task and shut down cleanly.
	close(releaseU1Task)
	cancel()
	if err := <-runErr; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
	if got := u1MaxActive.Load(); got < 2 {
		t.Fatalf("expected u1 conversation to overlap with u1 task (peak >= 2), got %d", got)
	}
}
