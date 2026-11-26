package env

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"nori/internal/users"
	"nori/internal/wake"
)

var ErrInjectedWakeFault = errors.New("sim/env: injected wake fault")

type WakeKind string

const (
	WakeConversation WakeKind = "conversation"
	WakeDelayed      WakeKind = "delayed"
)

type WakeEvent struct {
	Seq    int64
	Kind   WakeKind
	UserID string
	DueAt  time.Time
}

type WakerFaults struct {
	EnqueueErrProb float64

	DropProb float64

	DuplicateProb float64

	MaxDelay time.Duration
}

type WakerStats struct {
	ConversationCalls int64
	DelayedCalls      int64
	Enqueued          int64
	Delivered         int64
	EnqueueErrors     int64
	Dropped           int64
	Duplicated        int64
	Delayed           int64
}

type Waker struct {
	mu            sync.Mutex
	clock         *Clock
	faultsEnabled bool
	faults        WakerFaults
	stats         WakerStats
	rng           *rand.Rand

	nextSeq int64
	queue   []WakeEvent
}

var (
	_ wake.Waker        = (*Waker)(nil)
	_ wake.DelayedWaker = (*Waker)(nil)
)

func NewWaker(clock *Clock, faults WakerFaults, rng *rand.Rand) *Waker {
	if clock == nil {
		clock = NewClock(time.Time{})
	}
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 1))
	}
	return &Waker{
		clock:         clock,
		faultsEnabled: true,
		faults:        faults,
		rng:           rng,
	}
}

func (w *Waker) ForUser(userID string) wake.Waker {
	return wake.ForUser(w, userID)
}

func (w *Waker) SetFaultsEnabled(enabled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.faultsEnabled = enabled
}

func (w *Waker) FaultsEnabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.faultsEnabled
}

func (w *Waker) SetFaults(cfg WakerFaults) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.faults = cfg
}

func (w *Waker) WakeConversation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.ConversationCalls++
	return w.enqueueLocked(WakeConversation, users.ID(ctx), w.clock.Now())
}

func (w *Waker) WakeConversationAt(ctx context.Context, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.DelayedCalls++
	base := at.UTC()
	if base.IsZero() {
		base = w.clock.Now()
	}
	return w.enqueueLocked(WakeDelayed, users.ID(ctx), base)
}

func (w *Waker) enqueueLocked(kind WakeKind, userID string, baseDueAt time.Time) error {
	if w.hitFaultLocked(w.faults.EnqueueErrProb) {
		w.stats.EnqueueErrors++
		return fmt.Errorf("%w: %s", ErrInjectedWakeFault, kind)
	}
	if w.hitFaultLocked(w.faults.DropProb) {
		w.stats.Dropped++
		return nil
	}

	copies := 1
	if w.hitFaultLocked(w.faults.DuplicateProb) {
		w.stats.Duplicated++
		copies = 2
	}

	for range copies {
		dueAt := baseDueAt.UTC()
		if w.faultsEnabled && w.faults.MaxDelay > 0 {
			delay := time.Duration(w.rngLocked().Int64N(int64(w.faults.MaxDelay) + 1))
			if delay > 0 {
				w.stats.Delayed++
				dueAt = dueAt.Add(delay)
			}
		}
		w.nextSeq++
		w.queue = append(w.queue, WakeEvent{
			Seq:    w.nextSeq,
			Kind:   kind,
			UserID: userID,
			DueAt:  dueAt,
		})
		w.stats.Enqueued++
	}
	slices.SortFunc(w.queue, compareWakeEvents)
	return nil
}

func (w *Waker) PopDue(now time.Time) []WakeEvent {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.queue) == 0 {
		return nil
	}
	cutoff := now.UTC()
	var due []WakeEvent
	var remaining []WakeEvent
	for _, ev := range w.queue {
		if !ev.DueAt.After(cutoff) {
			due = append(due, ev)
		} else {
			remaining = append(remaining, ev)
		}
	}
	w.queue = remaining
	w.stats.Delivered += int64(len(due))
	return due
}

func (w *Waker) Peek() []WakeEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) == 0 {
		return nil
	}
	return slices.Clone(w.queue)
}

func (w *Waker) NextDue() (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) == 0 {
		return time.Time{}, false
	}
	return w.queue[0].DueAt, true
}

func (w *Waker) PendingCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queue)
}

func (w *Waker) Stats() WakerStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

func (w *Waker) ResetStats() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats = WakerStats{}
}

func (w *Waker) Digest() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	h := sha256.New()
	writeTag(h, "sim.env.waker.v2")
	writeUint64(h, uint64(len(w.queue)))
	for _, ev := range w.queue {
		writeString(h, string(ev.Kind))
		writeString(h, ev.UserID)
		writeTime(h, ev.DueAt)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (w *Waker) rngLocked() *rand.Rand {
	if w.rng == nil {
		w.rng = rand.New(rand.NewPCG(1, 1))
	}
	return w.rng
}

func (w *Waker) hitFaultLocked(prob float64) bool {
	if !w.faultsEnabled || prob <= 0 {
		return false
	}
	return w.rngLocked().Float64() < prob
}

func compareWakeEvents(a, b WakeEvent) int {
	if a.DueAt.Before(b.DueAt) {
		return -1
	}
	if a.DueAt.After(b.DueAt) {
		return 1
	}
	if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
		return c
	}
	if c := cmp.Compare(a.UserID, b.UserID); c != 0 {
		return c
	}
	return cmp.Compare(a.Seq, b.Seq)
}
