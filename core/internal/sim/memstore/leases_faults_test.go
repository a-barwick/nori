package memstore_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"nori/internal/sim/memstore"
	"nori/internal/state/docstore"
)

func TestBackendFutureWakeWithoutClockDoesNotNotifyOrSkewUpdatedAt(t *testing.T) {
	ctx := context.Background()
	b := memstore.New()

	events := make(chan docstore.WakeEvent, 8)
	listenCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = b.ListenWakes(listenCtx, func(ev docstore.WakeEvent) {
			events <- ev
		})
	}()
	if err := b.WaitForWakeSubscribers(ctx, 1); err != nil {
		t.Fatalf("WaitForWakeSubscribers: %v", err)
	}

	before := time.Now().UTC()
	future := before.Add(2 * time.Hour)
	err := b.RunTransaction(ctx, false, 3, func(tx docstore.TransactionBackend) error {
		w := tx.(docstore.WakeWriter)
		if err := w.WakeTask(ctx, "u1", "r-future", future); err != nil {
			return err
		}
		return w.WakeUser(ctx, "u1", docstore.LaneScheduler, future)
	})
	if err != nil {
		t.Fatalf("RunTransaction: %v", err)
	}
	after := time.Now().UTC()

	select {
	case ev := <-events:
		t.Fatalf("future wake emitted unexpected immediate event: %+v", ev)
	case <-time.After(30 * time.Millisecond):
	}

	leases, err := b.ListTaskLeases(ctx, "u1")
	if err != nil || len(leases) != 1 {
		t.Fatalf("expected 1 task lease, got %+v (err=%v)", leases, err)
	}
	if !leases[0].WakeAt.Equal(future) {
		t.Fatalf("expected task WakeAt=%s, got %s", future, leases[0].WakeAt)
	}
	if leases[0].UpdatedAt.Before(before.Add(-time.Second)) || leases[0].UpdatedAt.After(after.Add(time.Second)) {
		t.Fatalf("task UpdatedAt=%s skewed outside [%s, %s]", leases[0].UpdatedAt, before, after)
	}

	wakes, err := b.ListUserWakes(ctx, "u1")
	if err != nil || len(wakes) != 1 {
		t.Fatalf("expected 1 user wake, got %+v (err=%v)", wakes, err)
	}
	if !wakes[0].WakeAt.Equal(future) {
		t.Fatalf("expected user WakeAt=%s, got %s", future, wakes[0].WakeAt)
	}
	if wakes[0].UpdatedAt.Before(before.Add(-time.Second)) || wakes[0].UpdatedAt.After(after.Add(time.Second)) {
		t.Fatalf("user UpdatedAt=%s skewed outside [%s, %s]", wakes[0].UpdatedAt, before, after)
	}
}

func TestBackendLeaseFaultInjectionAndDropResync(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	inj := memstore.NewFaultInjector(memstore.FaultConfig{LeaseErrProb: 1.0}, rand.New(rand.NewPCG(7, 9)))
	b := memstore.New(
		memstore.WithClock(func() time.Time { return now }),
		memstore.WithFaultInjector(inj),
	)

	if err := b.WakeTask(ctx, "u1", "r1", time.Time{}); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("expected WakeTask fault, got %v", err)
	}
	if _, _, err := b.ClaimTask(ctx, "w1", 30*time.Second); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("expected ClaimTask fault, got %v", err)
	}
	if _, _, err := b.ClaimTaskForUser(ctx, "u1", "w1", 30*time.Second); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("expected ClaimTaskForUser fault, got %v", err)
	}
	if _, _, err := b.ClaimUserWakeForUser(ctx, "u1", docstore.LaneConversation, "w1", 30*time.Second); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("expected ClaimUserWakeForUser fault, got %v", err)
	}
	if _, err := b.ListUserWakes(ctx, "u1"); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("expected ListUserWakes fault, got %v", err)
	}
	stats := b.FaultStats()
	if stats.LeaseErrors != 5 || stats.Total() != 5 {
		t.Fatalf("expected LeaseErrors=5 Total=5, got %+v", stats)
	}

	inj.SetEnabled(false)
	events := make(chan docstore.WakeEvent, 8)
	listenCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = b.ListenWakes(listenCtx, func(ev docstore.WakeEvent) {
			events <- ev
		})
	}()
	if err := b.WaitForWakeSubscribers(ctx, 1); err != nil {
		t.Fatalf("WaitForWakeSubscribers: %v", err)
	}

	b.SetDropWakeEvents(true)
	if err := b.WakeTask(ctx, "u1", "r1", time.Time{}); err != nil {
		t.Fatalf("WakeTask: %v", err)
	}
	select {
	case ev := <-events:
		t.Fatalf("expected wake event dropped, got %+v", ev)
	case <-time.After(25 * time.Millisecond):
	}

	b.EmitResync()
	select {
	case ev := <-events:
		if ev.Payload != docstore.WakePayloadResync {
			t.Fatalf("expected WakePayloadResync, got %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for EmitResync event")
	}
}
