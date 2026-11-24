package main

import (
	"context"
	"sync"
	"time"

	"nori/internal/state/docstore"
)

// simClock is a thread-safe offset-based ticking virtual clock for accelerated
// month-long simulations. Time advances continuously at 1x real time between
// explicit Set/Advance jumps so short debounce/retry windows (e.g. 1.5s burst
// coalescing in RestartConversation) naturally expire without stalling.
type simClock struct {
	mu       sync.RWMutex
	baseSim  time.Time
	baseWall time.Time
}

func newSimClock(start time.Time) *simClock {
	if start.IsZero() {
		start = time.Now().UTC()
	}
	return &simClock{baseSim: start.UTC(), baseWall: time.Now()}
}

func (c *simClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseSim.Add(time.Since(c.baseWall))
}

func (c *simClock) Set(t time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseSim = t.UTC()
	c.baseWall = time.Now()
	return c.baseSim
}

func (c *simClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.baseSim.Add(time.Since(c.baseWall))
	c.baseSim = now.Add(d)
	c.baseWall = time.Now()
	return c.baseSim
}

type userWakeKey struct {
	userID string
	lane   string
}

type taskWakeKey struct {
	userID string
	runID  string
}

// simClockBackend wraps a real Postgres docstore.Backend so virtual-clock wakeAt
// timestamps never prematurely hot-spin or stall against Postgres server now().
// Immediate wakes (wakeAt <= simClock.Now()) map to time.Time{} (Postgres now()),
// while future wakes (wakeAt > simClock.Now()) park in Postgres until simClock
// catches up and WakeDue releases them immediately.
type simClockBackend struct {
	inner docstore.Backend
	clock *simClock

	mu         sync.Mutex
	userWakes  map[userWakeKey]time.Time
	taskWakes  map[taskWakeKey]time.Time
	farHorizon time.Duration
}

func newSimClockBackend(inner docstore.Backend, clock *simClock) *simClockBackend {
	return &simClockBackend{
		inner:      inner,
		clock:      clock,
		userWakes:  map[userWakeKey]time.Time{},
		taskWakes:  map[taskWakeKey]time.Time{},
		farHorizon: 365 * 24 * time.Hour,
	}
}

var (
	_ docstore.Backend           = (*simClockBackend)(nil)
	_ docstore.BackendUnwrapper  = (*simClockBackend)(nil)
	_ docstore.LeaseStore        = (*simClockBackend)(nil)
	_ docstore.WakeListener      = (*simClockBackend)(nil)
	_ docstore.MemoryIndex       = (*simClockBackend)(nil)
	_ docstore.MemoryIndexWriter = (*simClockBackend)(nil)
	_ docstore.Sequencer         = (*simClockBackend)(nil)
	_ docstore.SequenceReader    = (*simClockBackend)(nil)
	_ docstore.Pinger            = (*simClockBackend)(nil)
	_ docstore.AdvisoryLocker    = (*simClockBackend)(nil)
)

func (b *simClockBackend) Unwrap() docstore.Backend { return b.inner }

func (b *simClockBackend) translateUserWake(userID, lane string, wakeAt time.Time) time.Time {
	now := b.clock.Now()
	key := userWakeKey{userID: userID, lane: lane}
	b.mu.Lock()
	defer b.mu.Unlock()
	if wakeAt.IsZero() || !wakeAt.After(now) {
		delete(b.userWakes, key)
		return time.Time{}
	}
	if prev, ok := b.userWakes[key]; !ok || wakeAt.Before(prev) {
		b.userWakes[key] = wakeAt.UTC()
	}
	return time.Now().UTC().Add(b.farHorizon)
}

func (b *simClockBackend) translateTaskWake(userID, runID string, wakeAt time.Time) time.Time {
	now := b.clock.Now()
	key := taskWakeKey{userID: userID, runID: runID}
	b.mu.Lock()
	defer b.mu.Unlock()
	if wakeAt.IsZero() || !wakeAt.After(now) {
		delete(b.taskWakes, key)
		return time.Time{}
	}
	// Short task retries/backoffs (<= 2m virtual) should execute promptly on wall clock
	// rather than stalling until the next virtual-clock jump.
	if wakeAt.Sub(now) <= 2*time.Minute {
		delete(b.taskWakes, key)
		return time.Now().UTC().Add(200 * time.Millisecond)
	}
	if prev, ok := b.taskWakes[key]; !ok || wakeAt.Before(prev) {
		b.taskWakes[key] = wakeAt.UTC()
	}
	return time.Now().UTC().Add(b.farHorizon)
}

// WakeDue releases any delayed virtual-clock wakes that are now <= simClock.Now().
func (b *simClockBackend) WakeDue(ctx context.Context) error {
	now := b.clock.Now()
	var dueUsers []userWakeKey
	var dueTasks []taskWakeKey
	b.mu.Lock()
	for k, at := range b.userWakes {
		if !at.After(now) {
			dueUsers = append(dueUsers, k)
			delete(b.userWakes, k)
		}
	}
	for k, at := range b.taskWakes {
		if !at.After(now) {
			dueTasks = append(dueTasks, k)
			delete(b.taskWakes, k)
		}
	}
	b.mu.Unlock()

	leases, ok := b.inner.(docstore.LeaseStore)
	if !ok {
		return nil
	}
	for _, k := range dueUsers {
		if err := leases.WakeUser(ctx, k.userID, k.lane, time.Time{}); err != nil {
			return err
		}
	}
	for _, k := range dueTasks {
		if err := leases.WakeTask(ctx, k.userID, k.runID, time.Time{}); err != nil {
			return err
		}
	}
	return nil
}

// docstore.Backend methods
func (b *simClockBackend) Get(ctx context.Context, path string) (docstore.Record, error) {
	return b.inner.Get(ctx, path)
}

func (b *simClockBackend) Query(ctx context.Context, spec docstore.QuerySpec) ([]docstore.Record, error) {
	return b.inner.Query(ctx, spec)
}

func (b *simClockBackend) Apply(ctx context.Context, m docstore.Mutation) error {
	return b.inner.Apply(ctx, m)
}

func (b *simClockBackend) Watch(ctx context.Context, spec docstore.QuerySpec, onUpdate func([]docstore.Record) error) error {
	return b.inner.Watch(ctx, spec, onUpdate)
}

func (b *simClockBackend) DeleteBatch(ctx context.Context, paths []string) error {
	return b.inner.DeleteBatch(ctx, paths)
}

func (b *simClockBackend) Close() error {
	return b.inner.Close()
}

func (b *simClockBackend) RunTransaction(ctx context.Context, readOnly bool, maxAttempts int, fn func(docstore.TransactionBackend) error) error {
	return b.inner.RunTransaction(ctx, readOnly, maxAttempts, func(tx docstore.TransactionBackend) error {
		return fn(&simClockTx{inner: tx, parent: b})
	})
}

type simClockTx struct {
	inner  docstore.TransactionBackend
	parent *simClockBackend
}

var (
	_ docstore.TransactionBackend = (*simClockTx)(nil)
	_ docstore.WakeWriter         = (*simClockTx)(nil)
	_ docstore.MemoryIndexWriter  = (*simClockTx)(nil)
	_ docstore.Sequencer          = (*simClockTx)(nil)
)

func (t *simClockTx) Get(ctx context.Context, path string) (docstore.Record, error) {
	return t.inner.Get(ctx, path)
}

func (t *simClockTx) Query(ctx context.Context, spec docstore.QuerySpec) ([]docstore.Record, error) {
	return t.inner.Query(ctx, spec)
}

func (t *simClockTx) Apply(ctx context.Context, m docstore.Mutation) error {
	return t.inner.Apply(ctx, m)
}

func (t *simClockTx) WakeTask(ctx context.Context, userID, runID string, wakeAt time.Time) error {
	w, ok := t.inner.(docstore.WakeWriter)
	if !ok {
		return nil
	}
	return w.WakeTask(ctx, userID, runID, t.parent.translateTaskWake(userID, runID, wakeAt))
}

func (t *simClockTx) WakeWaitingTasks(ctx context.Context, userID, reason string) error {
	w, ok := t.inner.(docstore.WakeWriter)
	if !ok {
		return nil
	}
	return w.WakeWaitingTasks(ctx, userID, reason)
}

func (t *simClockTx) WakeUser(ctx context.Context, userID, lane string, wakeAt time.Time) error {
	w, ok := t.inner.(docstore.WakeWriter)
	if !ok {
		return nil
	}
	return w.WakeUser(ctx, userID, lane, t.parent.translateUserWake(userID, lane, wakeAt))
}

func (t *simClockTx) NotifyPause(ctx context.Context) error {
	w, ok := t.inner.(docstore.WakeWriter)
	if !ok {
		return nil
	}
	return w.NotifyPause(ctx)
}

func (t *simClockTx) UpsertMemoryChunk(ctx context.Context, chunk docstore.MemoryChunk) error {
	w, ok := t.inner.(docstore.MemoryIndexWriter)
	if !ok {
		return nil
	}
	return w.UpsertMemoryChunk(ctx, chunk)
}

func (t *simClockTx) DeleteMemoryChunk(ctx context.Context, owner, chunkID string) error {
	w, ok := t.inner.(docstore.MemoryIndexWriter)
	if !ok {
		return nil
	}
	return w.DeleteMemoryChunk(ctx, owner, chunkID)
}

func (t *simClockTx) NextSequence(ctx context.Context, path string, count int) (int64, error) {
	s, ok := t.inner.(docstore.Sequencer)
	if !ok {
		return 0, nil
	}
	return s.NextSequence(ctx, path, count)
}

// docstore.LeaseStore & WakeWriter methods
func (b *simClockBackend) leases() docstore.LeaseStore {
	return b.inner.(docstore.LeaseStore)
}

func (b *simClockBackend) ClaimTask(ctx context.Context, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	return b.leases().ClaimTask(ctx, owner, lease)
}

func (b *simClockBackend) ClaimTaskByID(ctx context.Context, userID, runID, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	return b.leases().ClaimTaskByID(ctx, userID, runID, owner, lease)
}

func (b *simClockBackend) RefreshTaskClaim(ctx context.Context, userID, runID, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	return b.leases().RefreshTaskClaim(ctx, userID, runID, owner, lease)
}

func (b *simClockBackend) RenewTaskLeases(ctx context.Context, owner string, keys []docstore.TaskKey, lease time.Duration) (int, error) {
	return b.leases().RenewTaskLeases(ctx, owner, keys, lease)
}

func (b *simClockBackend) ParkTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, reason string) (bool, error) {
	return b.leases().ParkTask(ctx, userID, runID, owner, wakeSeq, reason)
}

func (b *simClockBackend) ReleaseTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, nextWake time.Time) error {
	if !nextWake.IsZero() {
		nextWake = b.translateTaskWake(userID, runID, nextWake)
	}
	return b.leases().ReleaseTask(ctx, userID, runID, owner, wakeSeq, nextWake)
}

func (b *simClockBackend) DeleteTaskLease(ctx context.Context, userID, runID string, wakeSeq int64) (bool, error) {
	b.mu.Lock()
	delete(b.taskWakes, taskWakeKey{userID: userID, runID: runID})
	b.mu.Unlock()
	return b.leases().DeleteTaskLease(ctx, userID, runID, wakeSeq)
}

func (b *simClockBackend) EnsureTaskLease(ctx context.Context, userID, runID string, wakeAt time.Time) (bool, error) {
	return b.leases().EnsureTaskLease(ctx, userID, runID, b.translateTaskWake(userID, runID, wakeAt))
}

func (b *simClockBackend) ListTaskLeases(ctx context.Context, userID string) ([]docstore.TaskLease, error) {
	return b.leases().ListTaskLeases(ctx, userID)
}

func (b *simClockBackend) ClaimUserWake(ctx context.Context, lane, owner string, lease time.Duration) (docstore.UserWake, bool, error) {
	return b.leases().ClaimUserWake(ctx, lane, owner, lease)
}

func (b *simClockBackend) RenewUserWake(ctx context.Context, userID, lane, owner string, lease time.Duration) (bool, error) {
	return b.leases().RenewUserWake(ctx, userID, lane, owner, lease)
}

func (b *simClockBackend) ParkUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64) (bool, error) {
	return b.leases().ParkUserWake(ctx, userID, lane, owner, wakeSeq)
}

func (b *simClockBackend) ReleaseUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64, nextWake time.Time) error {
	if !nextWake.IsZero() {
		nextWake = b.translateUserWake(userID, lane, nextWake)
	}
	return b.leases().ReleaseUserWake(ctx, userID, lane, owner, wakeSeq, nextWake)
}

func (b *simClockBackend) ListUserWakes(ctx context.Context, userID string) ([]docstore.UserWake, error) {
	return b.leases().ListUserWakes(ctx, userID)
}

func (b *simClockBackend) WakeTask(ctx context.Context, userID, runID string, wakeAt time.Time) error {
	return b.leases().WakeTask(ctx, userID, runID, b.translateTaskWake(userID, runID, wakeAt))
}

func (b *simClockBackend) WakeWaitingTasks(ctx context.Context, userID, reason string) error {
	return b.leases().WakeWaitingTasks(ctx, userID, reason)
}

func (b *simClockBackend) WakeUser(ctx context.Context, userID, lane string, wakeAt time.Time) error {
	return b.leases().WakeUser(ctx, userID, lane, b.translateUserWake(userID, lane, wakeAt))
}

func (b *simClockBackend) NotifyPause(ctx context.Context) error {
	return b.leases().NotifyPause(ctx)
}

// WakeListener, MemoryIndex, Sequencer, SequenceReader, Pinger, AdvisoryLocker
func (b *simClockBackend) ListenWakes(ctx context.Context, onEvent func(docstore.WakeEvent)) error {
	return b.inner.(docstore.WakeListener).ListenWakes(ctx, onEvent)
}

func (b *simClockBackend) SearchMemory(ctx context.Context, query docstore.MemorySearchQuery) ([]docstore.MemorySearchHit, error) {
	if query.Now.IsZero() {
		query.Now = b.clock.Now()
	}
	return b.inner.(docstore.MemoryIndex).SearchMemory(ctx, query)
}

func (b *simClockBackend) ListMemoryChunks(ctx context.Context, owner string) ([]docstore.MemoryChunk, error) {
	return b.inner.(docstore.MemoryIndex).ListMemoryChunks(ctx, owner)
}

func (b *simClockBackend) UpsertMemoryChunk(ctx context.Context, chunk docstore.MemoryChunk) error {
	return b.inner.(docstore.MemoryIndexWriter).UpsertMemoryChunk(ctx, chunk)
}

func (b *simClockBackend) DeleteMemoryChunk(ctx context.Context, owner, chunkID string) error {
	return b.inner.(docstore.MemoryIndexWriter).DeleteMemoryChunk(ctx, owner, chunkID)
}

func (b *simClockBackend) NextSequence(ctx context.Context, path string, count int) (int64, error) {
	return b.inner.(docstore.Sequencer).NextSequence(ctx, path, count)
}

func (b *simClockBackend) CurrentSequence(ctx context.Context, path string) (int64, error) {
	return b.inner.(docstore.SequenceReader).CurrentSequence(ctx, path)
}

func (b *simClockBackend) Ping(ctx context.Context) error {
	return b.inner.(docstore.Pinger).Ping(ctx)
}

func (b *simClockBackend) TryAdvisoryLock(ctx context.Context, key int64) (func(), bool, error) {
	return b.inner.(docstore.AdvisoryLocker).TryAdvisoryLock(ctx, key)
}
