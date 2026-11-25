package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"nori/internal/sim/memstore"
	"nori/internal/state/docstore"
)

func TestSimClockFrozenDeterministic(t *testing.T) {
	start := time.Date(2027, 1, 10, 9, 0, 0, 0, time.UTC)
	clock := newSimClock(start)

	if got := clock.Now(); !got.Equal(start) {
		t.Fatalf("initial Now = %s, want %s", got, start)
	}
	time.Sleep(50 * time.Millisecond)
	if got := clock.Now(); !got.Equal(start) {
		t.Fatalf("Now drifted across wall-clock sleep: got %s, want %s", got, start)
	}

	adv := clock.Advance(90 * time.Second)
	if want := start.Add(90 * time.Second); !adv.Equal(want) || !clock.Now().Equal(want) {
		t.Fatalf("Advance(90s) = %s / Now=%s, want %s", adv, clock.Now(), want)
	}

	// AdvanceUntil with an earlier target must be a monotonic no-op.
	if got := clock.AdvanceUntil(start.Add(30 * time.Second)); !got.Equal(start.Add(90 * time.Second)) {
		t.Fatalf("AdvanceUntil(earlier) = %s, want %s", got, start.Add(90*time.Second))
	}

	// AdvanceUntil with a later target jumps to that exact timestamp.
	target := start.Add(5 * time.Minute)
	if got := clock.AdvanceUntil(target); !got.Equal(target) || !clock.Now().Equal(target) {
		t.Fatalf("AdvanceUntil(later) = %s / Now=%s, want %s", got, clock.Now(), target)
	}

	// Set resets to explicit timestamp.
	reset := start.Add(24 * time.Hour)
	if got := clock.Set(reset); !got.Equal(reset) || !clock.Now().Equal(reset) {
		t.Fatalf("Set = %s / Now=%s, want %s", got, clock.Now(), reset)
	}
}

func TestTranslateUserWakeThreshold(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2027, 1, 10, 9, 0, 0, 0, time.UTC)
	clock := newSimClock(start)
	mem := memstore.New(memstore.WithClock(time.Now))
	b := newSimClockBackend(mem, clock)

	// Zero and past wakes translate to time.Time{} and are not parked in b.userWakes.
	if got := b.translateUserWake("u1", docstore.LaneConversation, time.Time{}); !got.IsZero() {
		t.Fatalf("zero wakeAt translated to %s, want zero", got)
	}
	if got := b.translateUserWake("u1", docstore.LaneConversation, start.Add(-time.Second)); !got.IsZero() {
		t.Fatalf("past wakeAt translated to %s, want zero", got)
	}

	// Short user wake (2s ScheduleAnswerRetry / Pool backoff, and boundary 30s) passes through to ~200ms wall clock.
	for _, d := range []time.Duration{2 * time.Second, shortUserWakeThreshold} {
		beforeWall := time.Now().UTC()
		got := b.translateUserWake("u1", docstore.LaneScheduler, start.Add(d))
		afterWall := time.Now().UTC()
		if got.Before(beforeWall.Add(shortWakeWallDelay-10*time.Millisecond)) || got.After(afterWall.Add(shortWakeWallDelay+50*time.Millisecond)) {
			t.Fatalf("short delay %s translated to %s outside expected wall window [%s, %s]", d, got, beforeWall, afterWall)
		}
		b.mu.Lock()
		_, parked := b.userWakes[userWakeKey{userID: "u1", lane: docstore.LaneScheduler}]
		b.mu.Unlock()
		if parked {
			t.Fatalf("short delay %s unexpectedly parked in b.userWakes", d)
		}
	}

	// Delays > 30s (31s, 2m IdleExtractDelay / flushBackoff, 15m IdleDreamDelay) park at farHorizon in b.userWakes.
	wallBefore := time.Now().UTC()
	got2m := b.translateUserWake("u1", docstore.LaneConversation, start.Add(2*time.Minute))
	if got2m.Sub(wallBefore) < b.farHorizon-time.Minute {
		t.Fatalf("2m wake translated to %s, expected ~farHorizon (%s)", got2m, b.farHorizon)
	}
	got15m := b.translateUserWake("u2", docstore.LaneConversation, start.Add(15*time.Minute))
	if got15m.Sub(wallBefore) < b.farHorizon-time.Minute {
		t.Fatalf("15m wake translated to %s, expected ~farHorizon (%s)", got15m, b.farHorizon)
	}

	// WakeDue before clock advances must NOT release u1 or u2.
	if err := b.WakeDue(ctx); err != nil {
		t.Fatalf("WakeDue before advance: %v", err)
	}
	if wakes, err := mem.ListUserWakes(ctx, ""); err != nil || len(wakes) != 0 {
		t.Fatalf("expected 0 woken rows in memstore before advance, got %+v (err=%v)", wakes, err)
	}

	// Advance 2m -> WakeDue releases u1 (2m) while keeping u2 (15m) parked.
	clock.Advance(2 * time.Minute)
	if err := b.WakeDue(ctx); err != nil {
		t.Fatalf("WakeDue at +2m: %v", err)
	}
	u1Wakes, err := mem.ListUserWakes(ctx, "u1")
	if err != nil || len(u1Wakes) != 1 || !u1Wakes[0].Runnable {
		t.Fatalf("expected u1 wake released at +2m, got %+v (err=%v)", u1Wakes, err)
	}
	u2Wakes, err := mem.ListUserWakes(ctx, "u2")
	if err != nil || len(u2Wakes) != 0 {
		t.Fatalf("expected u2 (15m) still parked at +2m, got %+v (err=%v)", u2Wakes, err)
	}
}

type retryTxBackend struct {
	*memstore.Backend
	failAttempt int
	attempt     int
	retryErr    error
}

func (r *retryTxBackend) RunTransaction(ctx context.Context, readOnly bool, maxAttempts int, fn func(docstore.TransactionBackend) error) error {
	return r.Backend.RunTransaction(ctx, readOnly, maxAttempts, func(tx docstore.TransactionBackend) error {
		r.attempt++
		if err := fn(tx); err != nil {
			return err
		}
		if r.attempt == r.failAttempt {
			return r.retryErr
		}
		return nil
	})
}

func TestSimClockTxRollbackAndRetry(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2027, 1, 10, 9, 0, 0, 0, time.UTC)

	t.Run("rollback_does_not_add_or_delete_wakes", func(t *testing.T) {
		clock := newSimClock(start)
		mem := memstore.New(memstore.WithClock(time.Now))
		b := newSimClockBackend(mem, clock)

		// Pre-seed a virtual wake at +10m.
		existingWake := start.Add(10 * time.Minute)
		if err := b.WakeUser(ctx, "u1", docstore.LaneConversation, existingWake); err != nil {
			t.Fatalf("WakeUser seed: %v", err)
		}
		if err := b.WakeTask(ctx, "u1", "r-existing", existingWake); err != nil {
			t.Fatalf("WakeTask seed: %v", err)
		}

		sentinel := errors.New("intentional rollback")
		err := b.RunTransaction(ctx, false, 1, func(tx docstore.TransactionBackend) error {
			w := tx.(docstore.WakeWriter)
			// Overwrite with immediate wake (would delete from maps if applied prematurely) and add a new future wake.
			if err := w.WakeUser(ctx, "u1", docstore.LaneConversation, time.Time{}); err != nil {
				return err
			}
			if err := w.WakeTask(ctx, "u1", "r-existing", time.Time{}); err != nil {
				return err
			}
			if err := w.WakeUser(ctx, "u2", docstore.LaneScheduler, start.Add(5*time.Minute)); err != nil {
				return err
			}
			if err := w.WakeTask(ctx, "u2", "r-new", start.Add(5*time.Minute)); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("RunTransaction rollback err = %v, want %v", err, sentinel)
		}

		b.mu.Lock()
		defer b.mu.Unlock()
		if got := b.userWakes[userWakeKey{userID: "u1", lane: docstore.LaneConversation}]; !got.Equal(existingWake) {
			t.Fatalf("rollback corrupted existing u1 userWake: got %s, want %s", got, existingWake)
		}
		if got := b.taskWakes[taskWakeKey{userID: "u1", runID: "r-existing"}]; !got.Equal(existingWake) {
			t.Fatalf("rollback corrupted existing u1 taskWake: got %s, want %s", got, existingWake)
		}
		if _, leaked := b.userWakes[userWakeKey{userID: "u2", lane: docstore.LaneScheduler}]; leaked {
			t.Fatal("rollback leaked new u2 userWake")
		}
		if _, leaked := b.taskWakes[taskWakeKey{userID: "u2", runID: "r-new"}]; leaked {
			t.Fatal("rollback leaked new u2 taskWake")
		}
	})

	t.Run("retry_discards_aborted_attempt_and_applies_only_committed_attempt", func(t *testing.T) {
		clock := newSimClock(start)
		mem := memstore.New(memstore.WithClock(time.Now))
		wrapper := &retryTxBackend{
			Backend:     mem,
			failAttempt: 1,
			retryErr:    memstore.ErrTxConflict,
		}
		b := newSimClockBackend(wrapper, clock)

		attempt1Wake := start.Add(5 * time.Minute)
		attempt2Wake := start.Add(10 * time.Minute)
		callCount := 0
		err := b.RunTransaction(ctx, false, 3, func(tx docstore.TransactionBackend) error {
			callCount++
			w := tx.(docstore.WakeWriter)
			if callCount == 1 {
				if err := w.WakeUser(ctx, "u1", docstore.LaneConversation, attempt1Wake); err != nil {
					return err
				}
				return w.WakeTask(ctx, "u1", "r1", attempt1Wake)
			}
			if err := w.WakeUser(ctx, "u1", docstore.LaneConversation, attempt2Wake); err != nil {
				return err
			}
			return w.WakeTask(ctx, "u1", "r1", attempt2Wake)
		})
		if err != nil {
			t.Fatalf("RunTransaction retry: %v", err)
		}
		if callCount != 2 {
			t.Fatalf("callCount = %d, want 2", callCount)
		}

		b.mu.Lock()
		gotU := b.userWakes[userWakeKey{userID: "u1", lane: docstore.LaneConversation}]
		gotT := b.taskWakes[taskWakeKey{userID: "u1", runID: "r1"}]
		b.mu.Unlock()
		if !gotU.Equal(attempt2Wake) {
			t.Fatalf("userWake = %s, want %s (attempt 1 %s leaked!)", gotU, attempt2Wake, attempt1Wake)
		}
		if !gotT.Equal(attempt2Wake) {
			t.Fatalf("taskWake = %s, want %s (attempt 1 %s leaked!)", gotT, attempt2Wake, attempt1Wake)
		}
	})
}

func TestStandaloneLeaseStoreErrorAndGuardSafety(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2027, 1, 10, 9, 0, 0, 0, time.UTC)
	clock := newSimClock(start)
	inj := memstore.NewFaultInjector(memstore.FaultConfig{}, rand.New(rand.NewPCG(1, 2)))
	mem := memstore.New(memstore.WithClock(time.Now), memstore.WithFaultInjector(inj))
	b := newSimClockBackend(mem, clock)

	futureWake := start.Add(10 * time.Minute)
	laterWake := start.Add(20 * time.Minute)

	// 1. EnsureTaskLease: first succeeds (created=true) -> recorded; duplicate (created=false) -> ignored.
	created, err := b.EnsureTaskLease(ctx, "u1", "r-ens", futureWake)
	if err != nil || !created {
		t.Fatalf("EnsureTaskLease #1 = (%v, %v), want (true, nil)", created, err)
	}
	created, err = b.EnsureTaskLease(ctx, "u1", "r-ens", start.Add(5*time.Minute))
	if err != nil || created {
		t.Fatalf("EnsureTaskLease #2 = (%v, %v), want (false, nil)", created, err)
	}
	b.mu.Lock()
	gotEns := b.taskWakes[taskWakeKey{userID: "u1", runID: "r-ens"}]
	b.mu.Unlock()
	if !gotEns.Equal(futureWake) {
		t.Fatalf("EnsureTaskLease duplicate overwrote taskWake: got %s, want %s", gotEns, futureWake)
	}

	// 2. DeleteTaskLease with mismatched wakeSeq (deleted=false) preserves b.taskWakes.
	deleted, err := b.DeleteTaskLease(ctx, "u1", "r-ens", 999)
	if err != nil || deleted {
		t.Fatalf("DeleteTaskLease mismatched wakeSeq = (%v, %v), want (false, nil)", deleted, err)
	}
	b.mu.Lock()
	_, stillPresent := b.taskWakes[taskWakeKey{userID: "u1", runID: "r-ens"}]
	b.mu.Unlock()
	if !stillPresent {
		t.Fatal("DeleteTaskLease with deleted=false deleted b.taskWakes entry")
	}

	// Matching wakeSeq deletes both underlying lease and b.taskWakes entry.
	deleted, err = b.DeleteTaskLease(ctx, "u1", "r-ens", 1)
	if err != nil || !deleted {
		t.Fatalf("DeleteTaskLease matching wakeSeq = (%v, %v), want (true, nil)", deleted, err)
	}
	b.mu.Lock()
	_, stillPresent = b.taskWakes[taskWakeKey{userID: "u1", runID: "r-ens"}]
	b.mu.Unlock()
	if stillPresent {
		t.Fatal("DeleteTaskLease with deleted=true left stale b.taskWakes entry")
	}

	// 3. Failing underlying calls must NOT mutate b.userWakes or b.taskWakes.
	if err := b.WakeUser(ctx, "u1", docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser conv: %v", err)
	}
	cw, ok, err := b.ClaimUserWake(ctx, docstore.LaneConversation, "w1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimUserWake: ok=%v err=%v", ok, err)
	}
	if err := b.WakeTask(ctx, "u1", "r-fail", time.Time{}); err != nil {
		t.Fatalf("WakeTask r-fail: %v", err)
	}
	ct, ok, err := b.ClaimTaskByID(ctx, "u1", "r-fail", "w1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimTaskByID r-fail: ok=%v err=%v", ok, err)
	}

	inj.SetConfig(memstore.FaultConfig{LeaseErrProb: 1.0})
	if err := b.ReleaseUserWake(ctx, "u1", docstore.LaneConversation, "w1", cw.WakeSeq, laterWake); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("ReleaseUserWake err = %v, want ErrInjectedFault", err)
	}
	if err := b.ReleaseTask(ctx, "u1", "r-fail", "w1", ct.WakeSeq, laterWake); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("ReleaseTask err = %v, want ErrInjectedFault", err)
	}
	if err := b.WakeUser(ctx, "u2", docstore.LaneScheduler, laterWake); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("WakeUser err = %v, want ErrInjectedFault", err)
	}
	if err := b.WakeTask(ctx, "u2", "r-fail-2", laterWake); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("WakeTask err = %v, want ErrInjectedFault", err)
	}
	inj.SetEnabled(false)

	b.mu.Lock()
	if len(b.userWakes) != 0 || len(b.taskWakes) != 0 {
		b.mu.Unlock()
		t.Fatalf("failing LeaseStore calls mutated maps: userWakes=%+v taskWakes=%+v", b.userWakes, b.taskWakes)
	}
	b.mu.Unlock()

	// 4. ParkUserWake / ParkTask clears any future map entry when parked=true.
	if err := b.ReleaseUserWake(ctx, "u1", docstore.LaneConversation, "w1", cw.WakeSeq, laterWake); err != nil {
		t.Fatalf("ReleaseUserWake success: %v", err)
	}
	// Manually simulate parked=true after Claim (e.g. if an earlier future entry existed).
	b.mu.Lock()
	b.userWakes[userWakeKey{userID: "u1", lane: docstore.LaneConversation}] = laterWake
	b.mu.Unlock()
	if err := b.WakeUser(ctx, "u1", docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser immediate: %v", err)
	}
	cw2, ok, err := b.ClaimUserWake(ctx, docstore.LaneConversation, "w1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimUserWake #2: ok=%v err=%v", ok, err)
	}
	b.mu.Lock()
	b.userWakes[userWakeKey{userID: "u1", lane: docstore.LaneConversation}] = laterWake
	b.mu.Unlock()
	if parked, err := b.ParkUserWake(ctx, "u1", docstore.LaneConversation, "w1", cw2.WakeSeq); err != nil || !parked {
		t.Fatalf("ParkUserWake = (%v, %v), want (true, nil)", parked, err)
	}
	b.mu.Lock()
	_, hasParkedUserWake := b.userWakes[userWakeKey{userID: "u1", lane: docstore.LaneConversation}]
	b.mu.Unlock()
	if hasParkedUserWake {
		t.Fatal("ParkUserWake left stale entry in b.userWakes")
	}
}

func TestWakeDueForUserScopingAndRequeueOnError(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2027, 1, 10, 9, 0, 0, 0, time.UTC)
	clock := newSimClock(start)
	inj := memstore.NewFaultInjector(memstore.FaultConfig{}, rand.New(rand.NewPCG(3, 4)))
	mem := memstore.New(memstore.WithClock(time.Now), memstore.WithFaultInjector(inj))
	b := newSimClockBackend(mem, clock)

	dueAt := start.Add(2 * time.Minute)
	if err := b.WakeUser(ctx, "u1", docstore.LaneConversation, dueAt); err != nil {
		t.Fatalf("WakeUser u1: %v", err)
	}
	if err := b.WakeUser(ctx, "u2", docstore.LaneConversation, dueAt); err != nil {
		t.Fatalf("WakeUser u2: %v", err)
	}
	if err := b.WakeTask(ctx, "u1", "r1", start.Add(5*time.Minute)); err != nil {
		t.Fatalf("WakeTask u1: %v", err)
	}
	if err := b.WakeTask(ctx, "u2", "r2", start.Add(5*time.Minute)); err != nil {
		t.Fatalf("WakeTask u2: %v", err)
	}

	// Advance past both user wakes and task wakes, then WakeDueForUser("u1").
	clock.Advance(6 * time.Minute)
	if err := b.WakeDueForUser(ctx, "u1"); err != nil {
		t.Fatalf("WakeDueForUser(u1): %v", err)
	}

	// u1 should be woken in memstore; u2 should still be parked at farHorizon in b.userWakes/b.taskWakes and memstore.
	if w1, err := mem.ListUserWakes(ctx, "u1"); err != nil || len(w1) != 1 || !w1[0].Runnable || w1[0].WakeAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("expected u1 user wake released to now, got %+v (err=%v)", w1, err)
	}
	if t1, err := mem.ListTaskLeases(ctx, "u1"); err != nil || len(t1) != 1 || !t1[0].Runnable || t1[0].WakeAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("expected u1 task lease released to now, got %+v (err=%v)", t1, err)
	}
	if w2, err := mem.ListUserWakes(ctx, "u2"); err != nil || len(w2) != 1 || !w2[0].WakeAt.After(time.Now().UTC().Add(30*24*time.Hour)) {
		t.Fatalf("expected u2 user wake still parked at farHorizon, got %+v (err=%v)", w2, err)
	}
	if t2, err := mem.ListTaskLeases(ctx, "u2"); err != nil || len(t2) != 1 || !t2[0].WakeAt.After(time.Now().UTC().Add(30*24*time.Hour)) {
		t.Fatalf("expected u2 task lease still parked at farHorizon, got %+v (err=%v)", t2, err)
	}

	// Inject error during WakeDueForUser("u2"): unsent entries must be re-queued and delivered on retry.
	inj.SetConfig(memstore.FaultConfig{LeaseErrProb: 1.0})
	if err := b.WakeDueForUser(ctx, "u2"); !errors.Is(err, memstore.ErrInjectedFault) {
		t.Fatalf("WakeDueForUser(u2) under fault = %v, want ErrInjectedFault", err)
	}
	inj.SetEnabled(false)

	if err := b.WakeDueForUser(ctx, "u2"); err != nil {
		t.Fatalf("WakeDueForUser(u2) retry: %v", err)
	}
	if w2, err := mem.ListUserWakes(ctx, "u2"); err != nil || len(w2) != 1 || !w2[0].Runnable || w2[0].WakeAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("expected u2 user wake re-queued and released on retry, got %+v (err=%v)", w2, err)
	}
	if t2, err := mem.ListTaskLeases(ctx, "u2"); err != nil || len(t2) != 1 || !t2[0].Runnable || t2[0].WakeAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("expected u2 task lease re-queued and released on retry, got %+v (err=%v)", t2, err)
	}
}

func TestSimClockBackendSafeOptionalDelegationsAndScopedLeaseStore(t *testing.T) {
	ctx := context.Background()
	clock := newSimClock(time.Date(2027, 1, 10, 9, 0, 0, 0, time.UTC))
	mem := memstore.New(memstore.WithClock(time.Now))
	b := newSimClockBackend(mem, clock)

	// memstore.Backend does not implement Pinger or AdvisoryLocker -> must NOT panic.
	if err := b.Ping(ctx); err != nil {
		t.Fatalf("Ping over memstore = %v, want nil", err)
	}
	unlock, acquired, err := b.TryAdvisoryLock(ctx, 12345)
	if err != nil || !acquired || unlock == nil {
		t.Fatalf("TryAdvisoryLock over memstore = (%v, %v, %v), want (non-nil, true, nil)", unlock != nil, acquired, err)
	}
	unlock()

	// ScopedLeaseStore delegation works cleanly.
	if err := b.WakeTask(ctx, "u2", "r2", time.Time{}); err != nil {
		t.Fatalf("WakeTask u2: %v", err)
	}
	if err := b.WakeTask(ctx, "u1", "r1", time.Time{}); err != nil {
		t.Fatalf("WakeTask u1: %v", err)
	}
	claimed, ok, err := b.ClaimTaskForUser(ctx, "u1", "w1", 30*time.Second)
	if err != nil || !ok || claimed.UserID != "u1" || claimed.RunID != "r1" {
		t.Fatalf("ClaimTaskForUser(u1) = (%+v, %v, %v)", claimed, ok, err)
	}
	if err := b.WakeUser(ctx, "u1", docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser u1: %v", err)
	}
	uw, ok, err := b.ClaimUserWakeForUser(ctx, "u1", docstore.LaneConversation, "w1", 30*time.Second)
	if err != nil || !ok || uw.UserID != "u1" || uw.Lane != docstore.LaneConversation {
		t.Fatalf("ClaimUserWakeForUser(u1) = (%+v, %v, %v)", uw, ok, err)
	}
}
