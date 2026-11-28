package worker_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/app"
	"nori/internal/state/docstore"
	"nori/internal/worker"
)

func TestPoolConversationDelayedIdleWakeAndPark(t *testing.T) {
	clock := newTestClock(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	svc, backend := newPoolTestEnv(t, clock)
	u := mustCreateUser(t, svc, "11111111-1111-4111-8111-111111111111", "idle@example.com")

	ctx := context.Background()
	if err := backend.WakeUser(ctx, u.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser: %v", err)
	}

	var calls atomic.Int32
	doneFirst := make(chan struct{}, 4)
	doneSecond := make(chan struct{}, 4)
	delayedAt := clock.Now().Add(2 * time.Minute)

	pool, err := worker.New(svc, worker.Options{
		Owner:          "pool-idle-wake",
		Now:            clock.Now,
		PollInterval:   5 * time.Millisecond,
		RepairInterval: time.Hour,
		RunConversation: func(_ context.Context, _ *app.App) error {
			n := calls.Add(1)
			if n == 1 {
				doneFirst <- struct{}{}
			} else {
				doneSecond <- struct{}{}
			}
			return nil
		},
		NextConversationWake: func(_ context.Context, _ *app.App) (time.Time, error) {
			if calls.Load() == 1 {
				return delayedAt, nil
			}
			return time.Time{}, nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()

	expectSignal(t, doneFirst, "first conversation pass")
	deadline := time.Now().Add(2 * time.Second)
	for {
		wakes, err := backend.ListUserWakes(ctx, u.ID)
		if err != nil {
			t.Fatalf("ListUserWakes: %v", err)
		}
		if len(wakes) == 1 && wakes[0].Owner == "" {
			if !wakes[0].Runnable {
				t.Fatalf("expected LaneConversation to remain runnable for delayed idle wake, got parked: %+v", wakes[0])
			}
			if !wakes[0].WakeAt.Equal(delayedAt) {
				t.Fatalf("WakeAt = %v, want %v", wakes[0].WakeAt, delayedAt)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for delayed wake release, wakes=%+v", wakes)
		}
		time.Sleep(5 * time.Millisecond)
	}

	clock.Advance(2*time.Minute + time.Second)
	expectSignal(t, doneSecond, "delayed idle conversation pass")

	deadline = time.Now().Add(2 * time.Second)
	for {
		wakes, err := backend.ListUserWakes(ctx, u.ID)
		if err != nil {
			t.Fatalf("ListUserWakes: %v", err)
		}
		if len(wakes) == 1 && wakes[0].Owner == "" && !wakes[0].Runnable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for LaneConversation to park when NextConversationWake is zero, wakes=%+v", wakes)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
}

func TestPoolConversationDelayedWakeYieldsToConcurrentUserInput(t *testing.T) {
	clock := newTestClock(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	svc, backend := newPoolTestEnv(t, clock)
	u := mustCreateUser(t, svc, "22222222-2222-4222-8222-222222222222", "concurrent@example.com")

	ctx := context.Background()
	if err := backend.WakeUser(ctx, u.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser: %v", err)
	}

	var calls atomic.Int32
	secondPass := make(chan struct{}, 4)

	pool, err := worker.New(svc, worker.Options{
		Owner:          "pool-concurrent-wake",
		Now:            clock.Now,
		PollInterval:   5 * time.Millisecond,
		RepairInterval: time.Hour,
		RunConversation: func(runCtx context.Context, _ *app.App) error {
			n := calls.Add(1)
			if n == 1 {
				if err := backend.WakeUser(runCtx, u.ID, docstore.LaneConversation, time.Time{}); err != nil {
					return err
				}
			} else {
				secondPass <- struct{}{}
			}
			return nil
		},
		NextConversationWake: func(_ context.Context, _ *app.App) (time.Time, error) {
			if calls.Load() == 1 {
				return clock.Now().Add(15 * time.Minute), nil
			}
			return time.Time{}, nil
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()

	expectSignal(t, secondPass, "immediate wake for concurrent user input")

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
}

func TestPoolEntitlementWakeCancelsActiveWorkAndParksSuspendedUser(t *testing.T) {
	clock := newTestClock(time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC))
	svc, backend := newPoolTestEnv(t, clock)
	u := mustCreateUser(t, svc, "33333333-3333-4333-8333-333333333333", "suspend@example.com")

	ctx := context.Background()
	if err := backend.WakeUser(ctx, u.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser: %v", err)
	}

	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	entCallback := make(chan string, 1)

	pool, err := worker.New(svc, worker.Options{
		Owner:          "pool-entitlement-wake",
		Now:            clock.Now,
		PollInterval:   5 * time.Millisecond,
		RepairInterval: time.Hour,
		OnEntitlementChanged: func(uid string) {
			select {
			case entCallback <- uid:
			default:
			}
		},
		RunConversation: func(runCtx context.Context, _ *app.App) error {
			select {
			case started <- struct{}{}:
			default:
			}
			<-runCtx.Done()
			select {
			case canceled <- struct{}{}:
			default:
			}
			return runCtx.Err()
		},
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()

	expectSignal(t, started, "conversation started")

	// Suspend user in billing store -> emits WakeKindEntitlement -> pool cancels active work & parks wake.
	if _, err := svc.Billing().Suspend(ctx, u.ID, "abuse"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}

	expectSignal(t, canceled, "active conversation canceled on suspension")

	select {
	case uid := <-entCallback:
		if uid != u.ID {
			t.Fatalf("OnEntitlementChanged uid = %q, want %q", uid, u.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for OnEntitlementChanged callback")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		wakes, err := backend.ListUserWakes(ctx, u.ID)
		if err != nil {
			t.Fatalf("ListUserWakes: %v", err)
		}
		if len(wakes) == 1 && wakes[0].Owner == "" && !wakes[0].Runnable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected suspended user wake to be parked, got %+v", wakes)
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("pool.Run: %v", err)
	}
}
