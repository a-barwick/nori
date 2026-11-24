package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	// 1. Specific task wake reaches only (uA, r1).
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.TaskWakePayload("uA", "r1")})
	expectSignal(t, chA1, "uA/r1 specific wake")
	expectNoSignal(t, chA2, "uA/r2 should not receive uA/r1 wake")
	expectNoSignal(t, chB1, "uB/r1 should not receive uA/r1 wake")

	// 2. User-wide task wake reaches all uA subscribers only.
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.UserTasksWakePayload("uA")})
	expectSignal(t, chA1, "uA/r1 user-wide wake")
	expectSignal(t, chA2, "uA/r2 user-wide wake")
	expectNoSignal(t, chB1, "uB/r1 should not receive uA:* wake")

	// 3. Global pause notifies all active task subscribers immediately.
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.WakePayloadPause})
	expectSignal(t, chA1, "uA/r1 pause")
	expectSignal(t, chA2, "uA/r2 pause")
	expectSignal(t, chB1, "uB/r1 pause")

	// 4. Resync notifies all active task subscribers.
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.WakePayloadResync})
	expectSignal(t, chA1, "uA/r1 resync")
	expectSignal(t, chA2, "uA/r2 resync")
	expectSignal(t, chB1, "uB/r1 resync")

	// 5. Unsubscribe removes subscriber cleanly.
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
			time.Sleep(25 * time.Millisecond)
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
				// Simulate a concurrent wake arriving mid-turn (bumping wake_seq).
				// Because ConversationStatus is idle (nothing pending in records),
				// Pool will try ParkUserWake -> parked=false -> release immediately -> run #2!
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

	// Wait briefly for pass #2 to park cleanly.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		wakes, err := backend.ListUserWakes(ctx, u.ID)
		if err == nil && len(wakes) == 1 && !wakes[0].Runnable && wakes[0].Owner == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
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

	pool, err := worker.New(svc, worker.Options{
		Owner:                "pool-sched",
		SchedulerConcurrency: 2,
		PollInterval:         20 * time.Millisecond,
		RepairInterval:       time.Hour,
		DrainTimeout:         2 * time.Second,
		Now:                  clock.Now,
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

	// Schedule first run 1 hour in the future -> must NOT run yet.
	firstDue := clock.Now().Add(time.Hour)
	if err := backend.WakeUser(ctx, u.ID, docstore.LaneScheduler, firstDue); err != nil {
		t.Fatalf("WakeUser future: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()

	time.Sleep(60 * time.Millisecond)
	if pass.Load() != 0 {
		t.Fatalf("scheduler ran before wake_at was due: pass=%d", pass.Load())
	}

	// Advance clock to firstDue -> pass 1 runs and re-arms secondDue.
	clock.Advance(time.Hour)
	select {
	case <-pass1Done:
	case <-time.After(2 * time.Second):
		t.Fatalf("scheduler pass 1 did not run after wake_at became due")
	}

	// Verify re-armed wake_at == secondDue and not claimed yet.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		wakes, _ := backend.ListUserWakes(ctx, u.ID)
		if len(wakes) == 1 && wakes[0].Owner == "" && wakes[0].Runnable && wakes[0].WakeAt.Equal(secondDue) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	wakes, _ := backend.ListUserWakes(ctx, u.ID)
	if len(wakes) != 1 || !wakes[0].Runnable || !wakes[0].WakeAt.Equal(secondDue) || wakes[0].Owner != "" {
		t.Fatalf("expected re-armed scheduler wake at %s, got %+v", secondDue, wakes)
	}

	// Advance to secondDue -> pass 2 runs, returns nil, and parks cleanly.
	clock.Advance(time.Hour)
	select {
	case <-pass2Done:
	case <-time.After(2 * time.Second):
		t.Fatalf("scheduler pass 2 did not run")
	}

	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		wakes, _ = backend.ListUserWakes(ctx, u.ID)
		if len(wakes) == 1 && !wakes[0].Runnable && wakes[0].Owner == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
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

	// 1. Missing runnable task lease for an open run -> must be recreated.
	missingLeaseRunID := uuid.NewString()
	if _, err := userApp.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: missingLeaseRunID, Kind: state.CommandStart, Text: "open task",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch missingLeaseRunID: %v", err)
	}
	if _, err := backend.DeleteTaskLease(ctx, u.ID, missingLeaseRunID, 0); err != nil {
		t.Fatalf("DeleteTaskLease: %v", err)
	}

	// 2. Orphan lease for missing run and for unknown user -> must be deleted.
	orphanRunID := uuid.NewString()
	if err := backend.WakeTask(ctx, u.ID, orphanRunID, time.Time{}); err != nil {
		t.Fatalf("WakeTask orphan: %v", err)
	}
	unknownUserID := uuid.NewString()
	if err := backend.WakeTask(ctx, unknownUserID, uuid.NewString(), time.Time{}); err != nil {
		t.Fatalf("WakeTask unknown user: %v", err)
	}

	// 3. Stuck parked leases while !Paused: paused, routing_pending, question (no question on run), finishing (no children).
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

	// 4. On a second user: pending conversation input whose user wake was parked -> conversation wake must be recreated,
	// AND a routing_pending task on that user must STAY parked while conv.RoutingPending() is true.
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

	// 5. Enabled automation with NextAt and no scheduler wake -> must create scheduler wake <= NextAt.
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

	// 6. Global pause + CancelRequested -> RepairOnce invokes StopPaused and deletes lease.
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
				// Deliberately ignore drainCh to test DrainTimeout -> workCtx cancellation.
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

	convDone := make(chan struct{})
	schedDone := make(chan struct{})

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
		RunConversation: func(ctx context.Context, _ *app.App) error {
			// Exceed CancelGrace by >3x; finCtx MUST be constructed after return, not before!
			time.Sleep(90 * time.Millisecond)
			close(convDone)
			return nil
		},
		RunScheduler: func(ctx context.Context, _ *app.App) (*time.Time, error) {
			time.Sleep(90 * time.Millisecond)
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
	case <-convDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for slow conversation turn")
	}
	select {
	case <-schedDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for slow scheduler turn")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		wakes, err := backend.ListUserWakes(ctx, u.ID)
		if err == nil && len(wakes) == 2 &&
			!wakes[0].Runnable && wakes[0].Owner == "" && wakes[0].LeaseUntil == nil &&
			!wakes[1].Runnable && wakes[1].Owner == "" && wakes[1].LeaseUntil == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
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

	// Huge PollInterval & RepairInterval prove wakes alone (no fallback poll/repair!) drive
	// run_now -> task.Runner -> Publish -> LaneScheduler settlement + Inbox delivery, AND deferred answer retry.
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

	// 1. Create a "once" automation far in the future, then trigger run_now!
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

	// Wait for BOTH inbox delivery and occurrence settlement purely from wakes (PollInterval = 1h!).
	// scheduler.finish delivers InboxItem before settling Occurrence.Status="completed", so poll both.
	deadline := time.Now().Add(5 * time.Second)
	var inbox []scheduler.InboxItem
	var occs []scheduler.Occurrence
	for time.Now().Before(deadline) {
		items, ierr := userApp.Scheduler.Inbox(ctx, false)
		curOccs, oerr := userApp.Scheduler.Occurrences(ctx, savedAuto.ID)
		if ierr == nil && oerr == nil && len(items) == 1 && len(curOccs) == 1 && curOccs[0].Status == "completed" {
			inbox = items
			occs = curOccs
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if len(inbox) != 1 || len(occs) != 1 || occs[0].Status != "completed" {
		curOccs, _ := userApp.Scheduler.Occurrences(ctx, savedAuto.ID)
		run, _ := userApp.Store.GetRun(ctx, actRcpt.Work.TaskID)
		t.Fatalf("expected 1 delivered InboxItem and settled completed occurrence after run_now+Publish wake, got inbox=%d occs=%+v (run=%+v)", len(inbox), curOccs, run)
	}

	// 2. Deferred pre-ask interaction answer: create task + interaction BEFORE task asks,
	// respond via real HTTP handler (triggers ErrQuestionStale -> ScheduleAnswerRetry -> LaneScheduler at now+2s),
	// then record the matching Ask and advance clock so Pool retries deliverAnswers cleanly!
	taskID := uuid.NewString()
	deferTaskID.Store(taskID)
	if _, err := userApp.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: taskID, Kind: state.CommandStart, Text: "Task with pre-ask answer",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch deferTask: %v", err)
	}
	// Wait until SyncHistory has incorporated Position=1 and the task is deterministically
	// paused inside ModelRef before opening the pre-ask interaction and projecting the question.
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
	// Open a pending interaction for this task BEFORE it calls Ask (matching real pre-ask race).
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

	// Respond via real HTTP handler while task has NOT asked yet!
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

	// Verify LaneScheduler was armed with retry WakeAt ~ now+2s!
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

	// Record the matching Question ID on the run, then advance clock past retry delay and resync
	// so Pool runs Schedule -> deliverAnswers, delivering the deferred CommandAnswer!
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

	// Wait until deliverAnswers resolves the question on the run (or SyncHistory already
	// incorporated the delivered CommandAnswer and cleared the question), then let the model complete!
	deadline = time.Now().Add(5 * time.Second)
	resolved := false
	for time.Now().Before(deadline) {
		r, rerr := userApp.Store.GetRun(ctx, taskID)
		if rerr == nil {
			if r.Question != nil && r.Question.ResolvedBy != "" {
				resolved = true
				break
			}
			if r.Position >= 2 && r.Incorporated >= 2 {
				if undeliv, _, uerr := userApp.Assistant.Permissions.UndeliveredAnswers(ctx); uerr == nil && len(undeliv) == 0 {
					resolved = true
					break
				}
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	close(allowDeferredComplete)
	if !resolved {
		r, _ := userApp.Store.GetRun(ctx, taskID)
		ans, _, _ := userApp.Assistant.Permissions.UndeliveredAnswers(ctx)
		t.Fatalf("expected LaneScheduler retry to resolve deferred answer for question %s, got run=%+v undelivered=%+v", inter.ID, r, ans)
	}

	deadline = time.Now().Add(5 * time.Second)
	var finalRun state.Run
	for time.Now().Before(deadline) {
		r, rerr := userApp.Store.GetRun(ctx, taskID)
		if rerr == nil && r.Status == state.RunCompleted && !r.CleanupPending {
			finalRun = r
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
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
			PollInterval:   time.Hour, // Disable fallback polling to prove EmitResync wakes everything!
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
		// Because wake events are dropped and PollInterval=1h, the task sits unclaimed until resync.
		time.Sleep(40 * time.Millisecond)
		expectNoSignal(t, subCh, "subscriber should not fire while wakes are dropped")

		// EmitResync simulates LISTEN reconnect -> must wake both active task subscribers AND claim loop!
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

	// Seed a cold user with a missing lease before filling cache with hot users A & B.
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

	// Fill MaxCachedApps=2 with hot A & B.
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

	// Verify A & B remained cached with the exact same *App pointers (zero evictions).
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

	// Verify cold user's missing lease was repaired despite not entering cache.
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

	// Start replica-1 sweep and wait until it holds the advisory lock mid-sweep.
	errCh := make(chan error, 1)
	go func() { errCh <- pool1.RepairOnce(ctx) }()

	select {
	case <-inSweep:
	case <-time.After(2 * time.Second):
		t.Fatalf("replica-1 never entered sweep")
	}

	// Concurrent replica-2 RepairOnce must skip cleanly without executing a sweep.
	if err := pool2.RepairOnce(ctx); err != nil {
		t.Fatalf("replica-2 RepairOnce while lock held: %v", err)
	}
	if sweepExecutions.Load() != 1 {
		t.Fatalf("expected only 1 sweep execution while lock held, got %d", sweepExecutions.Load())
	}

	// Finish replica-1 sweep -> lock is released.
	close(releaseSweep)
	if err := <-errCh; err != nil {
		t.Fatalf("replica-1 RepairOnce: %v", err)
	}

	// Subsequent replica-2 sweep acquires the released lock and executes.
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

	if lastKey == 0 || lastKey == 7162384101 {
		t.Fatalf("expected dedicated repair advisory lock key distinct from migration lock (7162384101), got %d", lastKey)
	}
	if acquired != 2 || skipped != 1 || unlocked != 2 {
		t.Fatalf("advisory lock counts: acquired=%d skipped=%d unlocked=%d, want 2/1/2", acquired, skipped, unlocked)
	}

	// Lock backend error propagates from RepairOnce.
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

	// Enqueue 2 tasks, 1 conversation wake, and 1 scheduler wake.
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
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := pool.LaneStats()
		if st.TaskActive == 0 && st.ConversationActive == 0 && st.SchedulerActive == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	after := pool.LaneStats()
	if after.TaskActive != 0 || after.ConversationActive != 0 || after.SchedulerActive != 0 {
		t.Fatalf("post-completion LaneStats = %+v, want 0/0/0", after)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
}
