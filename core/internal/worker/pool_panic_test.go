package worker_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/app"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/task"
	"nori/internal/worker"
)

func TestPoolTaskPanicRecoveryReleasesSlotAndLease(t *testing.T) {
	ctx := context.Background()
	startTime := time.Now().UTC().Add(time.Second)
	clock := newTestClock(startTime)
	svc, backend := newPoolTestEnv(t, clock)

	u := mustCreateUser(t, svc, uuid.NewString(), "panic-task@example.com")
	uApp, rel, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire user: %v", err)
	}
	defer rel()

	const backoff = 5 * time.Second
	run1 := uuid.NewString()
	run2 := uuid.NewString()

	var run1Attempts atomic.Int32
	run1Panicked := make(chan struct{})
	run1Recovered := make(chan struct{})
	run2Done := make(chan struct{})

	pool, err := worker.New(svc, worker.Options{
		Owner:                 "pool-panic-task",
		TaskConcurrency:       1,
		MaxPerUserConcurrency: 1,
		PollInterval:          10 * time.Millisecond,
		RepairInterval:        time.Hour,
		DrainTimeout:          2 * time.Second,
		Backoff:               backoff,
		Now:                   clock.Now,
		RunTask: func(ctx context.Context, userApp *app.App, claim docstore.TaskLease, drain <-chan struct{}) (task.RunOutcome, error) {
			switch claim.RunID {
			case run1:
				if run1Attempts.Add(1) == 1 {
					close(run1Panicked)
					panic("boom in task handler")
				}
				if _, err := backend.DeleteTaskLease(ctx, claim.UserID, claim.RunID, 0); err != nil {
					return task.OutcomeBackoff, err
				}
				close(run1Recovered)
				return task.OutcomeDone, nil
			case run2:
				if _, err := backend.DeleteTaskLease(ctx, claim.UserID, claim.RunID, 0); err != nil {
					return task.OutcomeBackoff, err
				}
				close(run2Done)
				return task.OutcomeDone, nil
			default:
				return task.OutcomeDone, nil
			}
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	poolDone := make(chan error, 1)
	go func() { poolDone <- pool.Run(runCtx) }()

	if _, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: run1, Kind: state.CommandStart, Text: "panicking task",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch run1: %v", err)
	}

	expectSignal(t, run1Panicked, "run1 panic")

	// Verify active gauge drops back to 0 and the claimed lease is released with backoff.
	if !waitForCondition(t, 2*time.Second, func() bool {
		if pool.LaneStats().TaskActive != 0 {
			return false
		}
		leases, err := backend.ListTaskLeases(ctx, u.ID)
		if err != nil {
			return false
		}
		for _, l := range leases {
			if l.RunID == run1 {
				return l.Owner == "" && l.LeaseUntil == nil && !l.WakeAt.Before(startTime.Add(backoff))
			}
		}
		return false
	}) {
		leases, _ := backend.ListTaskLeases(ctx, u.ID)
		t.Fatalf("expected run1 lease released with backoff and TaskActive=0; active=%d leases=%+v", pool.LaneStats().TaskActive, leases)
	}

	// Enqueue a second task for the SAME user while MaxPerUserConcurrency=1.
	// If the per-user slot or lane semaphore had leaked on panic, run2 would stall forever.
	if _, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: run2, Kind: state.CommandStart, Text: "subsequent task",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch run2: %v", err)
	}
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.TaskWakePayload(u.ID, run2)})
	expectSignal(t, run2Done, "subsequent task for same user")

	// Advance clock past backoff so the panicked run1 retries and succeeds.
	clock.Advance(backoff + time.Second)
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.TaskWakePayload(u.ID, run1)})
	expectSignal(t, run1Recovered, "run1 retry after backoff")

	cancel()
	select {
	case err := <-poolDone:
		if err != nil {
			t.Fatalf("pool.Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("pool.Run did not shut down cleanly after panic recovery")
	}
}

func TestPoolConversationAndSchedulerPanicRecovery(t *testing.T) {
	ctx := context.Background()
	startTime := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	clock := newTestClock(startTime)
	svc, backend := newPoolTestEnv(t, clock)

	u := mustCreateUser(t, svc, uuid.NewString(), "panic-wakes@example.com")
	const backoff = 5 * time.Second

	var convAttempts atomic.Int32
	var schedAttempts atomic.Int32
	convPanicked := make(chan struct{})
	convDone := make(chan struct{})
	schedPanicked := make(chan struct{})
	schedDone := make(chan struct{})

	pool, err := worker.New(svc, worker.Options{
		Owner:                   "pool-panic-wakes",
		ConversationConcurrency: 1,
		SchedulerConcurrency:    1,
		MaxPerUserConcurrency:   1,
		PollInterval:            10 * time.Millisecond,
		RepairInterval:          time.Hour,
		RenewInterval:           50 * time.Millisecond,
		DrainTimeout:            2 * time.Second,
		Backoff:                 backoff,
		Now:                     clock.Now,
		RunConversation: func(ctx context.Context, userApp *app.App) error {
			if convAttempts.Add(1) == 1 {
				close(convPanicked)
				panic("boom in conversation handler")
			}
			close(convDone)
			return nil
		},
		RunScheduler: func(ctx context.Context, userApp *app.App) (*time.Time, error) {
			if schedAttempts.Add(1) == 1 {
				close(schedPanicked)
				panic("boom in scheduler handler")
			}
			close(schedDone)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	poolDone := make(chan error, 1)
	go func() { poolDone <- pool.Run(runCtx) }()

	if err := backend.WakeUser(ctx, u.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser conversation: %v", err)
	}
	if err := backend.WakeUser(ctx, u.ID, docstore.LaneScheduler, time.Time{}); err != nil {
		t.Fatalf("WakeUser scheduler: %v", err)
	}
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.UserWakePayload(u.ID, docstore.LaneConversation)})
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.UserWakePayload(u.ID, docstore.LaneScheduler)})

	expectSignal(t, convPanicked, "conversation panic")
	expectSignal(t, schedPanicked, "scheduler panic")

	if !waitForCondition(t, 2*time.Second, func() bool {
		stats := pool.LaneStats()
		if stats.ConversationActive != 0 || stats.SchedulerActive != 0 {
			return false
		}
		wakes, err := backend.ListUserWakes(ctx, u.ID)
		if err != nil {
			return false
		}
		var convOK, schedOK bool
		for _, w := range wakes {
			if w.Lane == docstore.LaneConversation && w.Owner == "" && w.LeaseUntil == nil && !w.WakeAt.Before(startTime.Add(backoff)) {
				convOK = true
			}
			if w.Lane == docstore.LaneScheduler && w.Owner == "" && w.LeaseUntil == nil && !w.WakeAt.Before(startTime.Add(backoff)) {
				schedOK = true
			}
		}
		return convOK && schedOK
	}) {
		wakes, _ := backend.ListUserWakes(ctx, u.ID)
		t.Fatalf("expected conversation and scheduler wakes released with backoff; stats=%+v wakes=%+v", pool.LaneStats(), wakes)
	}

	clock.Advance(backoff + time.Second)
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.UserWakePayload(u.ID, docstore.LaneConversation)})
	pool.HandleWake(docstore.WakeEvent{Channel: docstore.WakeChannel, Payload: docstore.UserWakePayload(u.ID, docstore.LaneScheduler)})

	expectSignal(t, convDone, "conversation retry after backoff")
	expectSignal(t, schedDone, "scheduler retry after backoff")

	cancel()
	select {
	case err := <-poolDone:
		if err != nil {
			t.Fatalf("pool.Run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("pool.Run did not shut down cleanly after wake panic recovery")
	}
}
