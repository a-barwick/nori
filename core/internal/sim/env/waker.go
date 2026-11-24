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

// ErrInjectedWakeFault is returned when a configured wake enqueue fault triggers.
var ErrInjectedWakeFault = errors.New("sim/env: injected wake fault")

// WakeKind identifies the kind of queued wake notification.
type WakeKind string

const (
	WakeConversation WakeKind = "conversation"
	WakeWorker       WakeKind = "worker"
	WakeDelayed      WakeKind = "delayed"
)

// WakeEvent is a queued wake delivery scheduled for DueAt.
type WakeEvent struct {
	Seq         int64
	Kind        WakeKind
	UserID      string
	Build       string
	WorkflowIDs []string
	DueAt       time.Time
}

// WakerFaults configures deterministic wake transport faults.
type WakerFaults struct {
	// EnqueueErrProb is the probability that a wake returns ErrInjectedWakeFault
	// without recording any wake (caller must retry / retain intent).
	EnqueueErrProb float64

	// DropProb is the probability that a wake returns nil as if accepted by the
	// transport, then silently drops the wake (testing WorkVersion / Repairer).
	DropProb float64

	// DuplicateProb is the probability that an accepted wake enqueues two copies.
	DuplicateProb float64

	// MaxDelay adds a uniform deterministic delivery delay in [0, MaxDelay] to
	// each accepted wake when > 0.
	MaxDelay time.Duration
}

// WakerStats counts wake calls, deliveries, and injected transport faults.
type WakerStats struct {
	ConversationCalls int64
	WorkerCalls       int64
	DelayedCalls      int64
	Enqueued          int64
	Delivered         int64
	EnqueueErrors     int64
	Dropped           int64
	Duplicated        int64
	Delayed           int64
}

// Waker is a deterministic in-memory implementation of wake.Waker and
// wake.DelayedWaker with fault injection (enqueue errors, silent drops,
// duplicates, and delivery delays).
type Waker struct {
	mu              sync.Mutex
	clock           *Clock
	workersNeedWake bool
	faultsEnabled   bool
	faults          WakerFaults
	stats           WakerStats
	rng             *rand.Rand

	nextSeq int64
	queue   []WakeEvent
}

var (
	_ wake.Waker        = (*Waker)(nil)
	_ wake.DelayedWaker = (*Waker)(nil)
)

// NewWaker creates a Waker. If clock is nil a default Clock is created; if rng
// is nil a fixed deterministic PCG source is used.
func NewWaker(clock *Clock, workersNeedWake bool, faults WakerFaults, rng *rand.Rand) *Waker {
	if clock == nil {
		clock = NewClock(time.Time{})
	}
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 1))
	}
	return &Waker{
		clock:           clock,
		workersNeedWake: workersNeedWake,
		faultsEnabled:   true,
		faults:          faults,
		rng:             rng,
	}
}

// ForUser returns a user-scoped wake.Waker (which also implements
// wake.DelayedWaker via wake.ForUser).
func (w *Waker) ForUser(userID string) wake.Waker {
	return wake.ForUser(w, userID)
}

// WorkersNeedWake implements wake.Waker.
func (w *Waker) WorkersNeedWake() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.workersNeedWake
}

// SetWorkersNeedWake updates whether workers require explicit wake notifications.
func (w *Waker) SetWorkersNeedWake(need bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.workersNeedWake = need
}

// SetFaultsEnabled toggles fault injection without clearing fault config or stats.
func (w *Waker) SetFaultsEnabled(enabled bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.faultsEnabled = enabled
}

// FaultsEnabled reports whether fault injection is active.
func (w *Waker) FaultsEnabled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.faultsEnabled
}

// SetFaults replaces the configured wake fault parameters.
func (w *Waker) SetFaults(cfg WakerFaults) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.faults = cfg
}

// WakeConversation implements wake.Waker.
func (w *Waker) WakeConversation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.ConversationCalls++
	return w.enqueueLocked(WakeConversation, users.ID(ctx), "", nil, w.clock.Now())
}

// WakeWorker implements wake.Waker.
func (w *Waker) WakeWorker(ctx context.Context, build string, workflowIDs ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.WorkerCalls++
	return w.enqueueLocked(WakeWorker, users.ID(ctx), build, workflowIDs, w.clock.Now())
}

// WakeConversationAt implements wake.DelayedWaker.
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
	return w.enqueueLocked(WakeDelayed, users.ID(ctx), "", nil, base)
}

func (w *Waker) enqueueLocked(kind WakeKind, userID, build string, workflowIDs []string, baseDueAt time.Time) error {
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
			Seq:         w.nextSeq,
			Kind:        kind,
			UserID:      userID,
			Build:       build,
			WorkflowIDs: slices.Clone(workflowIDs),
			DueAt:       dueAt,
		})
		w.stats.Enqueued++
	}
	slices.SortFunc(w.queue, compareWakeEvents)
	return nil
}

// PopDue drains and returns all queued wakes with DueAt <= now in deterministic
// sorted order.
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
			due = append(due, cloneWakeEvent(ev))
		} else {
			remaining = append(remaining, ev)
		}
	}
	w.queue = remaining
	w.stats.Delivered += int64(len(due))
	return due
}

// Peek returns a deep-copied snapshot of all currently queued wakes in
// deterministic sorted order without modifying the queue.
func (w *Waker) Peek() []WakeEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) == 0 {
		return nil
	}
	out := make([]WakeEvent, len(w.queue))
	for i, ev := range w.queue {
		out[i] = cloneWakeEvent(ev)
	}
	return out
}

// NextDue returns the earliest DueAt among queued wakes, or (zero, false) if
// the queue is empty.
func (w *Waker) NextDue() (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.queue) == 0 {
		return time.Time{}, false
	}
	return w.queue[0].DueAt, true
}

// PendingCount returns the number of currently queued wakes.
func (w *Waker) PendingCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queue)
}

// Stats returns a snapshot of wake counters.
func (w *Waker) Stats() WakerStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

// ResetStats zeroes all wake counters without modifying the pending queue.
func (w *Waker) ResetStats() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats = WakerStats{}
}

// Digest returns a canonical hex-encoded SHA-256 hash of the waker's
// configuration and pending wake queue.
func (w *Waker) Digest() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	h := sha256.New()
	writeTag(h, "sim.env.waker.v1")
	writeBool(h, w.workersNeedWake)
	writeUint64(h, uint64(len(w.queue)))
	for _, ev := range w.queue {
		writeString(h, string(ev.Kind))
		writeString(h, ev.UserID)
		writeString(h, ev.Build)
		writeTime(h, ev.DueAt)
		writeUint64(h, uint64(len(ev.WorkflowIDs)))
		for _, id := range ev.WorkflowIDs {
			writeString(h, id)
		}
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

func cloneWakeEvent(ev WakeEvent) WakeEvent {
	ev.WorkflowIDs = slices.Clone(ev.WorkflowIDs)
	return ev
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
	if c := cmp.Compare(a.Build, b.Build); c != 0 {
		return c
	}
	if c := slices.Compare(a.WorkflowIDs, b.WorkflowIDs); c != 0 {
		return c
	}
	return cmp.Compare(a.Seq, b.Seq)
}
