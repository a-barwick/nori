package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"nori/internal/state/docstore"
)

const (
	shortUserWakeThreshold = 30 * time.Second
	shortTaskWakeThreshold = 2 * time.Minute
	shortWakeWallDelay     = 200 * time.Millisecond
)

type simClock struct {
	mu  sync.RWMutex
	now time.Time
}

func newSimClock(start time.Time) *simClock {
	if start.IsZero() {
		start = time.Now().UTC()
	}
	return &simClock{now: start.UTC()}
}

func (c *simClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *simClock) Set(t time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
	return c.now
}

func (c *simClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

func (c *simClock) AdvanceUntil(target time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := target.UTC()
	if c.now.Before(t) {
		c.now = t
	}
	return c.now
}

type userWakeKey struct {
	userID string
	lane   string
}

type taskWakeKey struct {
	userID string
	runID  string
}

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
	_ docstore.ScopedLeaseStore  = (*simClockBackend)(nil)
	_ docstore.WakeListener      = (*simClockBackend)(nil)
	_ docstore.MemoryIndex       = (*simClockBackend)(nil)
	_ docstore.MemoryIndexWriter = (*simClockBackend)(nil)
	_ docstore.Sequencer         = (*simClockBackend)(nil)
	_ docstore.SequenceReader    = (*simClockBackend)(nil)
	_ docstore.Pinger            = (*simClockBackend)(nil)
	_ docstore.AdvisoryLocker    = (*simClockBackend)(nil)
)

func (b *simClockBackend) Unwrap() docstore.Backend { return b.inner }

type stagedUserWakeOp struct {
	key    userWakeKey
	delete bool
	wakeAt time.Time
}

type stagedTaskWakeOp struct {
	key    taskWakeKey
	delete bool
	wakeAt time.Time
}

func (b *simClockBackend) planUserWake(userID, lane string, wakeAt time.Time) (time.Time, stagedUserWakeOp) {
	now := b.clock.Now()
	key := userWakeKey{userID: userID, lane: lane}
	if wakeAt.IsZero() || !wakeAt.After(now) {
		return time.Time{}, stagedUserWakeOp{key: key, delete: true}
	}
	if wakeAt.Sub(now) <= shortUserWakeThreshold {
		return time.Now().UTC().Add(shortWakeWallDelay), stagedUserWakeOp{key: key, delete: true}
	}
	return time.Now().UTC().Add(b.farHorizon), stagedUserWakeOp{key: key, wakeAt: wakeAt.UTC()}
}

func (b *simClockBackend) planTaskWake(userID, runID string, wakeAt time.Time) (time.Time, stagedTaskWakeOp) {
	now := b.clock.Now()
	key := taskWakeKey{userID: userID, runID: runID}
	if wakeAt.IsZero() || !wakeAt.After(now) {
		return time.Time{}, stagedTaskWakeOp{key: key, delete: true}
	}
	if wakeAt.Sub(now) <= shortTaskWakeThreshold {
		return time.Now().UTC().Add(shortWakeWallDelay), stagedTaskWakeOp{key: key, delete: true}
	}
	return time.Now().UTC().Add(b.farHorizon), stagedTaskWakeOp{key: key, wakeAt: wakeAt.UTC()}
}

func (b *simClockBackend) applyUserWakeOpLocked(op stagedUserWakeOp) {
	if op.delete {
		delete(b.userWakes, op.key)
		return
	}
	if prev, ok := b.userWakes[op.key]; !ok || op.wakeAt.Before(prev) {
		b.userWakes[op.key] = op.wakeAt
	}
}

func (b *simClockBackend) applyTaskWakeOpLocked(op stagedTaskWakeOp) {
	if op.delete {
		delete(b.taskWakes, op.key)
		return
	}
	if prev, ok := b.taskWakes[op.key]; !ok || op.wakeAt.Before(prev) {
		b.taskWakes[op.key] = op.wakeAt
	}
}

func (b *simClockBackend) applyCommittedWakes(stx *simClockTx) {
	if stx == nil || (len(stx.userOps) == 0 && len(stx.taskOps) == 0) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, op := range stx.userOps {
		b.applyUserWakeOpLocked(op)
	}
	for _, op := range stx.taskOps {
		b.applyTaskWakeOpLocked(op)
	}
}

func (b *simClockBackend) translateUserWake(userID, lane string, wakeAt time.Time) time.Time {
	translated, op := b.planUserWake(userID, lane, wakeAt)
	b.mu.Lock()
	b.applyUserWakeOpLocked(op)
	b.mu.Unlock()
	return translated
}

type dueUserWake struct {
	key userWakeKey
	at  time.Time
}

type dueTaskWake struct {
	key taskWakeKey
	at  time.Time
}

func (b *simClockBackend) WakeDue(ctx context.Context) error {
	return b.WakeDueForUser(ctx, "")
}

func (b *simClockBackend) PendingUserWakeAt(userID, lane string) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	at, ok := b.userWakes[userWakeKey{userID: userID, lane: lane}]
	return at, ok
}

func (b *simClockBackend) WakeDueForUser(ctx context.Context, userID string) error {
	now := b.clock.Now()
	var dueUsers []dueUserWake
	var dueTasks []dueTaskWake
	b.mu.Lock()
	for k, at := range b.userWakes {
		if userID != "" && k.userID != userID {
			continue
		}
		if !at.After(now) {
			dueUsers = append(dueUsers, dueUserWake{key: k, at: at})
			delete(b.userWakes, k)
		}
	}
	for k, at := range b.taskWakes {
		if userID != "" && k.userID != userID {
			continue
		}
		if !at.After(now) {
			dueTasks = append(dueTasks, dueTaskWake{key: k, at: at})
			delete(b.taskWakes, k)
		}
	}
	b.mu.Unlock()

	if len(dueUsers) == 0 && len(dueTasks) == 0 {
		return nil
	}

	leases, ok := b.inner.(docstore.LeaseStore)
	if !ok {
		return nil
	}

	requeueRemaining := func(uIdx, tIdx int) {
		b.mu.Lock()
		defer b.mu.Unlock()
		for _, du := range dueUsers[uIdx:] {
			b.applyUserWakeOpLocked(stagedUserWakeOp{key: du.key, wakeAt: du.at})
		}
		for _, dt := range dueTasks[tIdx:] {
			b.applyTaskWakeOpLocked(stagedTaskWakeOp{key: dt.key, wakeAt: dt.at})
		}
	}

	for i, du := range dueUsers {
		if err := leases.WakeUser(ctx, du.key.userID, du.key.lane, time.Time{}); err != nil {
			requeueRemaining(i, 0)
			return err
		}
	}
	for i, dt := range dueTasks {
		if err := leases.WakeTask(ctx, dt.key.userID, dt.key.runID, time.Time{}); err != nil {
			requeueRemaining(len(dueUsers), i)
			return err
		}
	}
	return nil
}

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
	var committed *simClockTx
	err := b.inner.RunTransaction(ctx, readOnly, maxAttempts, func(tx docstore.TransactionBackend) error {
		stx := &simClockTx{inner: tx, parent: b}
		if err := fn(stx); err != nil {
			return err
		}
		committed = stx
		return nil
	})
	if err != nil {
		return err
	}
	if committed != nil {
		b.applyCommittedWakes(committed)
	}
	return nil
}

type simClockTx struct {
	inner   docstore.TransactionBackend
	parent  *simClockBackend
	userOps []stagedUserWakeOp
	taskOps []stagedTaskWakeOp
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
	translated, op := t.parent.planTaskWake(userID, runID, wakeAt)
	if err := w.WakeTask(ctx, userID, runID, translated); err != nil {
		return err
	}
	t.taskOps = append(t.taskOps, op)
	return nil
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
	translated, op := t.parent.planUserWake(userID, lane, wakeAt)
	if err := w.WakeUser(ctx, userID, lane, translated); err != nil {
		return err
	}
	t.userOps = append(t.userOps, op)
	return nil
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

func (b *simClockBackend) leases() (docstore.LeaseStore, error) {
	leases, ok := b.inner.(docstore.LeaseStore)
	if !ok {
		return nil, errors.New("simClockBackend: inner backend does not implement docstore.LeaseStore")
	}
	return leases, nil
}

func (b *simClockBackend) ClaimTask(ctx context.Context, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	leases, err := b.leases()
	if err != nil {
		return docstore.TaskLease{}, false, err
	}
	return leases.ClaimTask(ctx, owner, lease)
}

func (b *simClockBackend) ClaimTaskForUser(ctx context.Context, userID, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	scoped, ok := b.inner.(docstore.ScopedLeaseStore)
	if !ok {
		return docstore.TaskLease{}, false, errors.New("simClockBackend: inner backend does not implement docstore.ScopedLeaseStore")
	}
	return scoped.ClaimTaskForUser(ctx, userID, owner, lease)
}

func (b *simClockBackend) ClaimTaskByID(ctx context.Context, userID, runID, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	leases, err := b.leases()
	if err != nil {
		return docstore.TaskLease{}, false, err
	}
	return leases.ClaimTaskByID(ctx, userID, runID, owner, lease)
}

func (b *simClockBackend) RefreshTaskClaim(ctx context.Context, userID, runID, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	leases, err := b.leases()
	if err != nil {
		return docstore.TaskLease{}, false, err
	}
	return leases.RefreshTaskClaim(ctx, userID, runID, owner, lease)
}

func (b *simClockBackend) RenewTaskLeases(ctx context.Context, owner string, keys []docstore.TaskKey, lease time.Duration) (int, error) {
	leases, err := b.leases()
	if err != nil {
		return 0, err
	}
	return leases.RenewTaskLeases(ctx, owner, keys, lease)
}

func (b *simClockBackend) ParkTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, reason string) (bool, error) {
	leases, err := b.leases()
	if err != nil {
		return false, err
	}
	parked, err := leases.ParkTask(ctx, userID, runID, owner, wakeSeq, reason)
	if err == nil && parked {
		b.mu.Lock()
		delete(b.taskWakes, taskWakeKey{userID: userID, runID: runID})
		b.mu.Unlock()
	}
	return parked, err
}

func (b *simClockBackend) ReleaseTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, nextWake time.Time) error {
	leases, err := b.leases()
	if err != nil {
		return err
	}
	if owner == "" || nextWake.IsZero() {
		return leases.ReleaseTask(ctx, userID, runID, owner, wakeSeq, nextWake)
	}
	translated, op := b.planTaskWake(userID, runID, nextWake)
	if err := leases.ReleaseTask(ctx, userID, runID, owner, wakeSeq, translated); err != nil {
		return err
	}
	b.mu.Lock()
	b.applyTaskWakeOpLocked(op)
	b.mu.Unlock()
	return nil
}

func (b *simClockBackend) DeleteTaskLease(ctx context.Context, userID, runID string, wakeSeq int64) (bool, error) {
	leases, err := b.leases()
	if err != nil {
		return false, err
	}
	deleted, err := leases.DeleteTaskLease(ctx, userID, runID, wakeSeq)
	if err == nil && deleted {
		b.mu.Lock()
		delete(b.taskWakes, taskWakeKey{userID: userID, runID: runID})
		b.mu.Unlock()
	}
	return deleted, err
}

func (b *simClockBackend) EnsureTaskLease(ctx context.Context, userID, runID string, wakeAt time.Time) (bool, error) {
	leases, err := b.leases()
	if err != nil {
		return false, err
	}
	translated, op := b.planTaskWake(userID, runID, wakeAt)
	created, err := leases.EnsureTaskLease(ctx, userID, runID, translated)
	if err == nil && created {
		b.mu.Lock()
		b.applyTaskWakeOpLocked(op)
		b.mu.Unlock()
	}
	return created, err
}

func (b *simClockBackend) ListTaskLeases(ctx context.Context, userID string) ([]docstore.TaskLease, error) {
	leases, err := b.leases()
	if err != nil {
		return nil, err
	}
	return leases.ListTaskLeases(ctx, userID)
}

func (b *simClockBackend) ClaimUserWake(ctx context.Context, lane, owner string, lease time.Duration) (docstore.UserWake, bool, error) {
	leases, err := b.leases()
	if err != nil {
		return docstore.UserWake{}, false, err
	}
	return leases.ClaimUserWake(ctx, lane, owner, lease)
}

func (b *simClockBackend) ClaimUserWakeForUser(ctx context.Context, userID, lane, owner string, lease time.Duration) (docstore.UserWake, bool, error) {
	scoped, ok := b.inner.(docstore.ScopedLeaseStore)
	if !ok {
		return docstore.UserWake{}, false, errors.New("simClockBackend: inner backend does not implement docstore.ScopedLeaseStore")
	}
	return scoped.ClaimUserWakeForUser(ctx, userID, lane, owner, lease)
}

func (b *simClockBackend) RenewUserWake(ctx context.Context, userID, lane, owner string, lease time.Duration) (bool, error) {
	leases, err := b.leases()
	if err != nil {
		return false, err
	}
	return leases.RenewUserWake(ctx, userID, lane, owner, lease)
}

func (b *simClockBackend) ParkUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64) (bool, error) {
	leases, err := b.leases()
	if err != nil {
		return false, err
	}
	parked, err := leases.ParkUserWake(ctx, userID, lane, owner, wakeSeq)
	if err == nil && parked {
		b.mu.Lock()
		delete(b.userWakes, userWakeKey{userID: userID, lane: lane})
		b.mu.Unlock()
	}
	return parked, err
}

func (b *simClockBackend) ReleaseUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64, nextWake time.Time) error {
	leases, err := b.leases()
	if err != nil {
		return err
	}
	if owner == "" || nextWake.IsZero() {
		return leases.ReleaseUserWake(ctx, userID, lane, owner, wakeSeq, nextWake)
	}
	translated, op := b.planUserWake(userID, lane, nextWake)
	if err := leases.ReleaseUserWake(ctx, userID, lane, owner, wakeSeq, translated); err != nil {
		return err
	}
	b.mu.Lock()
	b.applyUserWakeOpLocked(op)
	b.mu.Unlock()
	return nil
}

func (b *simClockBackend) ListUserWakes(ctx context.Context, userID string) ([]docstore.UserWake, error) {
	leases, err := b.leases()
	if err != nil {
		return nil, err
	}
	return leases.ListUserWakes(ctx, userID)
}

func (b *simClockBackend) WakeTask(ctx context.Context, userID, runID string, wakeAt time.Time) error {
	leases, err := b.leases()
	if err != nil {
		return err
	}
	translated, op := b.planTaskWake(userID, runID, wakeAt)
	if err := leases.WakeTask(ctx, userID, runID, translated); err != nil {
		return err
	}
	b.mu.Lock()
	b.applyTaskWakeOpLocked(op)
	b.mu.Unlock()
	return nil
}

func (b *simClockBackend) WakeWaitingTasks(ctx context.Context, userID, reason string) error {
	leases, err := b.leases()
	if err != nil {
		return err
	}
	return leases.WakeWaitingTasks(ctx, userID, reason)
}

func (b *simClockBackend) WakeUser(ctx context.Context, userID, lane string, wakeAt time.Time) error {
	leases, err := b.leases()
	if err != nil {
		return err
	}
	translated, op := b.planUserWake(userID, lane, wakeAt)
	if err := leases.WakeUser(ctx, userID, lane, translated); err != nil {
		return err
	}
	b.mu.Lock()
	b.applyUserWakeOpLocked(op)
	b.mu.Unlock()
	return nil
}

func (b *simClockBackend) NotifyPause(ctx context.Context) error {
	leases, err := b.leases()
	if err != nil {
		return err
	}
	return leases.NotifyPause(ctx)
}

func (b *simClockBackend) ListenWakes(ctx context.Context, onEvent func(docstore.WakeEvent)) error {
	wl, ok := b.inner.(docstore.WakeListener)
	if !ok {
		return errors.New("simClockBackend: inner backend does not implement docstore.WakeListener")
	}
	return wl.ListenWakes(ctx, onEvent)
}

func (b *simClockBackend) SearchMemory(ctx context.Context, query docstore.MemorySearchQuery) ([]docstore.MemorySearchHit, error) {
	idx, ok := b.inner.(docstore.MemoryIndex)
	if !ok {
		return nil, errors.New("simClockBackend: inner backend does not implement docstore.MemoryIndex")
	}
	if query.Now.IsZero() {
		query.Now = b.clock.Now()
	}
	return idx.SearchMemory(ctx, query)
}

func (b *simClockBackend) ListMemoryChunks(ctx context.Context, owner string) ([]docstore.MemoryChunk, error) {
	idx, ok := b.inner.(docstore.MemoryIndex)
	if !ok {
		return nil, errors.New("simClockBackend: inner backend does not implement docstore.MemoryIndex")
	}
	return idx.ListMemoryChunks(ctx, owner)
}

func (b *simClockBackend) UpsertMemoryChunk(ctx context.Context, chunk docstore.MemoryChunk) error {
	w, ok := b.inner.(docstore.MemoryIndexWriter)
	if !ok {
		return errors.New("simClockBackend: inner backend does not implement docstore.MemoryIndexWriter")
	}
	return w.UpsertMemoryChunk(ctx, chunk)
}

func (b *simClockBackend) DeleteMemoryChunk(ctx context.Context, owner, chunkID string) error {
	w, ok := b.inner.(docstore.MemoryIndexWriter)
	if !ok {
		return errors.New("simClockBackend: inner backend does not implement docstore.MemoryIndexWriter")
	}
	return w.DeleteMemoryChunk(ctx, owner, chunkID)
}

func (b *simClockBackend) NextSequence(ctx context.Context, path string, count int) (int64, error) {
	s, ok := b.inner.(docstore.Sequencer)
	if !ok {
		return 0, errors.New("simClockBackend: inner backend does not implement docstore.Sequencer")
	}
	return s.NextSequence(ctx, path, count)
}

func (b *simClockBackend) CurrentSequence(ctx context.Context, path string) (int64, error) {
	sr, ok := b.inner.(docstore.SequenceReader)
	if !ok {
		return 0, errors.New("simClockBackend: inner backend does not implement docstore.SequenceReader")
	}
	return sr.CurrentSequence(ctx, path)
}

func (b *simClockBackend) Ping(ctx context.Context) error {
	p, ok := b.inner.(docstore.Pinger)
	if !ok {
		return nil
	}
	return p.Ping(ctx)
}

func (b *simClockBackend) TryAdvisoryLock(ctx context.Context, key int64) (func(), bool, error) {
	l, ok := b.inner.(docstore.AdvisoryLocker)
	if !ok {
		return func() {}, true, nil
	}
	return l.TryAdvisoryLock(ctx, key)
}
