package env

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/users"
	"nori/internal/wake"
)

func TestClock(t *testing.T) {
	c := NewClock(time.Time{})
	if !c.Now().Equal(DefaultBaseTime) {
		t.Fatalf("default clock = %v, want %v", c.Now(), DefaultBaseTime)
	}
	if c.Now().Location() != time.UTC {
		t.Fatalf("clock location = %v, want UTC", c.Now().Location())
	}

	got := c.Advance(90 * time.Second)
	want := DefaultBaseTime.Add(90 * time.Second)
	if !got.Equal(want) || !c.Now().Equal(want) {
		t.Fatalf("after Advance(90s): got %v / %v, want %v", got, c.Now(), want)
	}

	loc := time.FixedZone("EST", -5*3600)
	custom := time.Date(2026, 2, 1, 8, 30, 0, 0, loc)
	c.Set(custom)
	if !c.Now().Equal(custom.UTC()) || c.Now().Location() != time.UTC {
		t.Fatalf("after Set: got %v (%v), want %v (UTC)", c.Now(), c.Now().Location(), custom.UTC())
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_ = c.Now()
				c.Advance(time.Millisecond)
			}
		}()
	}
	wg.Wait()
	wantAfter := custom.UTC().Add(400 * time.Millisecond)
	if !c.Now().Equal(wantAfter) {
		t.Fatalf("after concurrent Advance: got %v, want %v", c.Now(), wantAfter)
	}
}

func TestIDGenDeterministicCanonicalUUIDv4(t *testing.T) {
	g1 := NewIDGenFromSeed(42)
	g2 := NewIDGenFromSeed(42)
	g3 := NewIDGenFromSeed(43)

	seen := map[string]bool{}
	for i := range 64 {
		id1 := g1.Next()
		id2 := g2.Next()
		id3 := g3.Next()

		if id1 != id2 {
			t.Fatalf("iter %d: same seed produced %q != %q", i, id1, id2)
		}
		if id1 == id3 {
			t.Fatalf("iter %d: different seeds collided on %q", i, id1)
		}
		if seen[id1] {
			t.Fatalf("duplicate UUID generated: %q", id1)
		}
		seen[id1] = true

		parsed, err := uuid.Parse(id1)
		if err != nil {
			t.Fatalf("uuid.Parse(%q) failed: %v", id1, err)
		}
		if parsed.String() != id1 {
			t.Fatalf("non-canonical UUID %q != %q", id1, parsed.String())
		}
		if parsed.Version() != 4 {
			t.Fatalf("UUID %q version = %d, want 4", id1, parsed.Version())
		}
		if parsed.Variant() != uuid.RFC4122 {
			t.Fatalf("UUID %q variant = %v, want RFC4122", id1, parsed.Variant())
		}
		if !users.ValidID(id1) {
			t.Fatalf("users.ValidID(%q) = false, want true", id1)
		}
	}
}

func TestTraceRingBufferFormatAndDigest(t *testing.T) {
	clk := NewClock(time.Time{})
	ring := NewTrace(3)
	full := NewTrace(0)

	for i := 1; i <= 5; i++ {
		at := clk.Advance(time.Second)
		ring.Record(i, at, "u1", "dispatcher", "reconcile", fmt.Sprintf("iter=%d", i))
		full.Record(i, at, "u1", "dispatcher", "reconcile", fmt.Sprintf("iter=%d", i))
	}

	if ring.Len() != 3 || ring.Total() != 5 {
		t.Fatalf("ring Len/Total = %d/%d, want 3/5", ring.Len(), ring.Total())
	}
	if full.Len() != 5 || full.Total() != 5 {
		t.Fatalf("full Len/Total = %d/%d, want 5/5", full.Len(), full.Total())
	}
	if ring.Digest() != full.Digest() {
		t.Fatalf("ring digest %s != full digest %s", ring.Digest(), full.Digest())
	}

	entries := ring.Entries()
	if len(entries) != 3 || entries[0].Step != 3 || entries[1].Step != 4 || entries[2].Step != 5 {
		t.Fatalf("unexpected ring entries: %+v", entries)
	}

	last2 := ring.FormatLast(2)
	lines := strings.Split(last2, "\n")
	if len(lines) != 2 {
		t.Fatalf("FormatLast(2) lines = %d, want 2: %q", len(lines), last2)
	}
	if !strings.Contains(lines[0], "step=4") || !strings.Contains(lines[1], "step=5") {
		t.Fatalf("FormatLast(2) unexpected content:\n%s", last2)
	}
}

func TestWakerFaultsDelaysAndDelayedWaker(t *testing.T) {
	ctx := context.Background()
	clk := NewClock(time.Time{})

	wErr := NewWaker(clk, WakerFaults{EnqueueErrProb: 1.0}, rand.New(rand.NewPCG(10, 20)))
	if err := wErr.ForUser("u1").WakeConversation(ctx); !errors.Is(err, ErrInjectedWakeFault) {
		t.Fatalf("WakeConversation with EnqueueErrProb=1 = %v, want ErrInjectedWakeFault", err)
	}
	if wErr.PendingCount() != 0 || wErr.Stats().EnqueueErrors != 1 {
		t.Fatalf("pending=%d stats=%+v", wErr.PendingCount(), wErr.Stats())
	}

	wDrop := NewWaker(clk, WakerFaults{DropProb: 1.0}, rand.New(rand.NewPCG(10, 20)))
	if err := wDrop.ForUser("u1").WakeConversation(ctx); err != nil {
		t.Fatalf("WakeConversation with DropProb=1 returned error: %v", err)
	}
	if wDrop.PendingCount() != 0 || wDrop.Stats().Dropped != 1 {
		t.Fatalf("pending=%d stats=%+v", wDrop.PendingCount(), wDrop.Stats())
	}

	wDup := NewWaker(clk, WakerFaults{
		DuplicateProb: 1.0,
		MaxDelay:      10 * time.Second,
	}, rand.New(rand.NewPCG(99, 100)))

	uWaker := wDup.ForUser("u1")
	t0 := clk.Now()
	if err := uWaker.WakeConversation(ctx); err != nil {
		t.Fatalf("WakeConversation = %v", err)
	}
	if wDup.PendingCount() != 2 || wDup.Stats().Duplicated != 1 || wDup.Stats().Enqueued != 2 {
		t.Fatalf("after dup: pending=%d stats=%+v", wDup.PendingCount(), wDup.Stats())
	}
	for _, ev := range wDup.Peek() {
		if ev.Kind != WakeConversation || ev.UserID != "u1" {
			t.Fatalf("unexpected WakeEvent: %+v", ev)
		}
		if ev.DueAt.Before(t0) || ev.DueAt.After(t0.Add(10*time.Second)) {
			t.Fatalf("DueAt %v outside [%v, %v]", ev.DueAt, t0, t0.Add(10*time.Second))
		}
	}

	wDup.SetFaultsEnabled(false)
	future := t0.Add(5 * time.Minute)
	if err := wake.At(ctx, uWaker, future); err != nil {
		t.Fatalf("wake.At(ForUser) = %v", err)
	}
	if err := wake.At(users.WithID(ctx, "u2"), wDup, future.Add(time.Minute)); err != nil {
		t.Fatalf("wake.At(direct) = %v", err)
	}
	if wDup.PendingCount() != 4 {
		t.Fatalf("PendingCount = %d, want 4", wDup.PendingCount())
	}

	due1 := wDup.PopDue(t0.Add(10 * time.Second))
	if len(due1) != 2 {
		t.Fatalf("PopDue(t0+10s) = %d events, want 2", len(due1))
	}
	nextDue, ok := wDup.NextDue()
	if !ok || !nextDue.Equal(future) {
		t.Fatalf("NextDue = (%v, %v), want (%v, true)", nextDue, ok, future)
	}

	due2 := wDup.PopDue(future)
	if len(due2) != 1 || due2[0].Kind != WakeDelayed || due2[0].UserID != "u1" {
		t.Fatalf("PopDue(future) = %+v", due2)
	}
	due3 := wDup.PopDue(future.Add(time.Minute))
	if len(due3) != 1 || due3[0].Kind != WakeDelayed || due3[0].UserID != "u2" {
		t.Fatalf("PopDue(future+1m) = %+v", due3)
	}
	if wDup.PendingCount() != 0 {
		t.Fatalf("PendingCount after draining all = %d", wDup.PendingCount())
	}
	if wDup.Stats().Delivered != 4 {
		t.Fatalf("Stats().Delivered = %d, want 4", wDup.Stats().Delivered)
	}
}

func TestWakerWithRepairerSmoke(t *testing.T) {
	ctx := context.Background()
	clk := NewClock(time.Time{})
	w := NewWaker(clk, WakerFaults{EnqueueErrProb: 1.0}, rand.New(rand.NewPCG(3, 4)))

	repairer := &wake.Repairer{
		Waker:   w.ForUser("u1"),
		Pending: func(context.Context) (string, error) { return "v1", nil },
		Now:     clk.Now,
	}

	if err := repairer.Repair(ctx); !errors.Is(err, ErrInjectedWakeFault) {
		t.Fatalf("Repair with EnqueueErrProb=1 = %v, want ErrInjectedWakeFault", err)
	}

	w.SetFaultsEnabled(false)
	if err := repairer.Repair(ctx); err != nil {
		t.Fatalf("Repair after clearing fault = %v", err)
	}
	if w.PendingCount() != 1 {
		t.Fatalf("PendingCount = %d, want 1", w.PendingCount())
	}

	clk.Advance(30 * time.Second)
	if err := repairer.Repair(ctx); err != nil {
		t.Fatalf("Repair within throttle window = %v", err)
	}
	if w.PendingCount() != 1 {
		t.Fatalf("PendingCount should stay 1 while throttled, got %d", w.PendingCount())
	}

	clk.Advance(31 * time.Second)
	if err := repairer.Repair(ctx); err != nil {
		t.Fatalf("Repair after 1 minute = %v", err)
	}
	if w.PendingCount() != 2 {
		t.Fatalf("PendingCount after throttle expiry = %d, want 2", w.PendingCount())
	}
}

func TestDeterministicReplayDigests(t *testing.T) {
	runOnce := func(seed uint64) (traceDigest, wakerDigest string) {
		ctx := context.Background()
		clk := NewClock(time.Time{})
		ids := NewIDGenFromSeed(seed)
		trace := NewTrace(128)
		w := NewWaker(clk, WakerFaults{
			EnqueueErrProb: 0.15,
			DropProb:       0.15,
			DuplicateProb:  0.25,
			MaxDelay:       5 * time.Second,
		}, rand.New(rand.NewPCG(seed+2, seed+3)))

		stepRNG := rand.New(rand.NewPCG(seed+4, seed+5))
		userIDs := []string{ids.Next(), ids.Next()}

		for step := range 80 {
			clk.Advance(time.Duration(stepRNG.IntN(10)+1) * time.Second)
			uid := userIDs[stepRNG.IntN(len(userIDs))]
			uw := w.ForUser(uid)

			switch stepRNG.IntN(2) {
			case 0:
				err := uw.WakeConversation(ctx)
				trace.Record(step, clk.Now(), uid, "waker", "wake_conv", fmt.Sprintf("err=%v", err))
			case 1:
				err := wake.At(ctx, uw, clk.Now().Add(3*time.Second))
				trace.Record(step, clk.Now(), uid, "waker", "wake_at", fmt.Sprintf("err=%v", err))
			}

			due := w.PopDue(clk.Now())
			if len(due) > 0 {
				trace.Record(step, clk.Now(), "", "waker", "pop_due", fmt.Sprintf("count=%d", len(due)))
			}
		}

		return trace.Digest(), w.Digest()
	}

	t1, w1 := runOnce(2026)
	t2, w2 := runOnce(2026)
	t3, w3 := runOnce(2027)

	if t1 != t2 || w1 != w2 {
		t.Fatalf("same-seed replay diverged:\nrun1: trace=%s waker=%s\nrun2: trace=%s waker=%s",
			t1, w1, t2, w2)
	}
	if t1 == t3 && w1 == w3 {
		t.Fatalf("different seeds produced identical digests")
	}
}

func TestDigestOrderIndependence(t *testing.T) {
	ctx := context.Background()
	clkA := NewClock(time.Time{})
	clkB := NewClock(time.Time{})

	wA := NewWaker(clkA, WakerFaults{}, nil)
	wB := NewWaker(clkB, WakerFaults{}, nil)

	_ = wA.ForUser("u1").WakeConversation(ctx)
	_ = wake.At(ctx, wA.ForUser("u2"), clkA.Now().Add(time.Minute))

	_ = wake.At(ctx, wB.ForUser("u2"), clkB.Now().Add(time.Minute))
	_ = wB.ForUser("u1").WakeConversation(ctx)

	if wA.Digest() != wB.Digest() {
		t.Fatalf("Waker Digest depended on enqueue order: %s != %s", wA.Digest(), wB.Digest())
	}
}
