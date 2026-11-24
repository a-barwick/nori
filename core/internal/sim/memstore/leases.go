package memstore

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"nori/internal/state/docstore"
)

const maxPerTaskWakeNotifications = 32

var errEmptyLeaseOwner = errors.New("memstore: lease owner is required")

type userWakeKey struct {
	userID string
	lane   string
}

type wakeOpKind int

const (
	wakeOpTask wakeOpKind = iota
	wakeOpWaitingTasks
	wakeOpUser
	wakeOpPause
)

type stagedWakeOp struct {
	kind   wakeOpKind
	userID string
	runID  string
	lane   string
	reason string
	wakeAt time.Time
}

// WithClock configures the time source used by Backend for lease and wake timestamps.
// Defaults to time.Now().UTC() when omitted or nil.
func WithClock(now func() time.Time) Option {
	return func(b *Backend) {
		b.nowFn = now
	}
}

func (b *Backend) now() time.Time {
	if b.nowFn == nil {
		return time.Now().UTC()
	}
	return b.nowFn().UTC()
}

func effectiveWakeAt(wakeAt, now time.Time) time.Time {
	if wakeAt.IsZero() {
		return now
	}
	return wakeAt.UTC()
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	cp := t.UTC()
	return &cp
}

func cloneTaskLease(row docstore.TaskLease) docstore.TaskLease {
	row.LeaseUntil = cloneTimePtr(row.LeaseUntil)
	row.WakeAt = row.WakeAt.UTC()
	row.UpdatedAt = row.UpdatedAt.UTC()
	return row
}

func cloneUserWake(row docstore.UserWake) docstore.UserWake {
	row.LeaseUntil = cloneTimePtr(row.LeaseUntil)
	row.WakeAt = row.WakeAt.UTC()
	row.UpdatedAt = row.UpdatedAt.UTC()
	return row
}

func compareTaskLeaseClaimOrder(a, b docstore.TaskLease) int {
	if c := a.WakeAt.Compare(b.WakeAt); c != 0 {
		return c
	}
	if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
		return c
	}
	if c := strings.Compare(a.UserID, b.UserID); c != 0 {
		return c
	}
	return strings.Compare(a.RunID, b.RunID)
}

func compareTaskLeaseKeyOrder(a, b docstore.TaskLease) int {
	if c := strings.Compare(a.UserID, b.UserID); c != 0 {
		return c
	}
	return strings.Compare(a.RunID, b.RunID)
}

func compareUserWakeClaimOrder(a, b docstore.UserWake) int {
	if c := a.WakeAt.Compare(b.WakeAt); c != 0 {
		return c
	}
	if c := a.UpdatedAt.Compare(b.UpdatedAt); c != 0 {
		return c
	}
	return strings.Compare(a.UserID, b.UserID)
}

func compareUserWakeKeyOrder(a, b docstore.UserWake) int {
	if c := strings.Compare(a.UserID, b.UserID); c != 0 {
		return c
	}
	return strings.Compare(a.Lane, b.Lane)
}

// SetDropWakeEvents toggles dropping post-commit wake notifications (simulating
// lost pg_notify packets or a temporarily disconnected LISTEN channel).
func (b *Backend) SetDropWakeEvents(drop bool) {
	b.mu.Lock()
	b.dropWakeEvents = drop
	b.mu.Unlock()
}

// EmitResync delivers a WakePayloadResync event to all active ListenWakes subscribers
// (simulating a LISTEN connection reconnect).
func (b *Backend) EmitResync() {
	b.emitWakeEventsForce([]docstore.WakeEvent{{
		Channel: docstore.WakeChannel,
		Payload: docstore.WakePayloadResync,
	}}, true)
}

// WaitForWakeSubscribers blocks until at least minCount ListenWakes subscribers are
// registered or ctx is canceled.
func (b *Backend) WaitForWakeSubscribers(ctx context.Context, minCount int) error {
	for {
		b.mu.Lock()
		if len(b.wakeSubs) >= minCount {
			b.mu.Unlock()
			return nil
		}
		if b.wakeSubNotify == nil {
			b.wakeSubNotify = make(chan struct{})
		}
		ch := b.wakeSubNotify
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

func (b *Backend) emitWakeEvents(events []docstore.WakeEvent) {
	b.emitWakeEventsForce(events, false)
}

func (b *Backend) emitWakeEventsForce(events []docstore.WakeEvent, force bool) {
	if len(events) == 0 {
		return
	}
	b.mu.Lock()
	if (!force && b.dropWakeEvents) || len(b.wakeSubs) == 0 {
		b.mu.Unlock()
		return
	}
	subIDs := make([]uint64, 0, len(b.wakeSubs))
	for id := range b.wakeSubs {
		subIDs = append(subIDs, id)
	}
	slices.Sort(subIDs)
	subs := make([]chan docstore.WakeEvent, 0, len(subIDs))
	for _, id := range subIDs {
		subs = append(subs, b.wakeSubs[id])
	}
	b.mu.Unlock()

	for _, ev := range events {
		for _, ch := range subs {
			select {
			case ch <- ev:
			default:
				// Non-blocking fan-out: slow subscribers fall back to polling
				// and never deadlock transaction commits.
			}
		}
	}
}

// ListenWakes delivers post-commit wake events in order until ctx is canceled.
func (b *Backend) ListenWakes(ctx context.Context, onEvent func(docstore.WakeEvent)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ch := make(chan docstore.WakeEvent, 256)
	b.mu.Lock()
	if b.wakeSubs == nil {
		b.wakeSubs = map[uint64]chan docstore.WakeEvent{}
	}
	b.nextWakeSub++
	id := b.nextWakeSub
	b.wakeSubs[id] = ch
	if b.wakeSubNotify != nil {
		close(b.wakeSubNotify)
		b.wakeSubNotify = make(chan struct{})
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.wakeSubs, id)
		b.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-ch:
			if onEvent != nil {
				onEvent(ev)
			}
		}
	}
}

func (b *Backend) applyWakeOpLocked(op stagedWakeOp) []docstore.WakeEvent {
	switch op.kind {
	case wakeOpTask:
		return b.applyWakeTaskLocked(op.userID, op.runID, op.wakeAt, b.now())
	case wakeOpWaitingTasks:
		return b.applyWakeWaitingTasksLocked(op.userID, op.reason, b.now())
	case wakeOpUser:
		return b.applyWakeUserLocked(op.userID, op.lane, op.wakeAt, b.now())
	case wakeOpPause:
		return []docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.WakePayloadPause,
		}}
	default:
		return nil
	}
}

func (b *Backend) applyWakeTaskLocked(userID, runID string, wakeAt, now time.Time) []docstore.WakeEvent {
	at := effectiveWakeAt(wakeAt, now)
	key := docstore.TaskKey{UserID: userID, RunID: runID}
	if row, exists := b.taskLeases[key]; exists {
		if row.Runnable {
			if at.Before(row.WakeAt) {
				row.WakeAt = at
			}
		} else {
			row.WakeAt = at
		}
		row.Runnable = true
		row.WaitingReason = docstore.WaitNone
		row.WakeSeq++
		row.UpdatedAt = now
		b.taskLeases[key] = row
	} else {
		b.taskLeases[key] = docstore.TaskLease{
			UserID:        userID,
			RunID:         runID,
			Runnable:      true,
			WaitingReason: docstore.WaitNone,
			Owner:         "",
			WakeSeq:       1,
			LeaseUntil:    nil,
			WakeAt:        at,
			UpdatedAt:     now,
		}
	}
	if wakeAt.IsZero() || !at.After(now) {
		return []docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.TaskWakePayload(userID, runID),
		}}
	}
	return nil
}

func (b *Backend) applyWakeWaitingTasksLocked(userID, reason string, now time.Time) []docstore.WakeEvent {
	// Match both parked rows for this waiting_reason AND any runnable row (owned or
	// still unclaimed) so memstore matches Postgres wakeWaitingTasksSQL and a
	// concurrent wake arriving around ClaimTask -> ParkTask always bumps wake_seq.
	var matched []docstore.TaskKey
	for key, row := range b.taskLeases {
		if userID != "" && row.UserID != userID {
			continue
		}
		if !row.Runnable && row.WaitingReason != reason {
			continue
		}
		matched = append(matched, key)
	}
	if len(matched) == 0 {
		return nil
	}
	slices.SortFunc(matched, func(a, b docstore.TaskKey) int {
		if c := cmp.Compare(a.UserID, b.UserID); c != 0 {
			return c
		}
		return cmp.Compare(a.RunID, b.RunID)
	})
	for _, key := range matched {
		row := b.taskLeases[key]
		if row.Runnable {
			if now.Before(row.WakeAt) {
				row.WakeAt = now
			}
		} else {
			row.WakeAt = now
		}
		row.Runnable = true
		row.WaitingReason = docstore.WaitNone
		row.WakeSeq++
		row.UpdatedAt = now
		b.taskLeases[key] = row
	}
	if userID == "" {
		return []docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.WakePayloadAllTask,
		}}
	}
	if len(matched) > maxPerTaskWakeNotifications {
		return []docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.UserTasksWakePayload(userID),
		}}
	}
	events := make([]docstore.WakeEvent, 0, len(matched))
	for _, key := range matched {
		events = append(events, docstore.WakeEvent{
			Channel: docstore.WakeChannel,
			Payload: docstore.TaskWakePayload(key.UserID, key.RunID),
		})
	}
	return events
}

func (b *Backend) applyWakeUserLocked(userID, lane string, wakeAt, now time.Time) []docstore.WakeEvent {
	at := effectiveWakeAt(wakeAt, now)
	key := userWakeKey{userID: userID, lane: lane}
	if row, exists := b.userWakes[key]; exists {
		if row.Runnable {
			if at.Before(row.WakeAt) {
				row.WakeAt = at
			}
		} else {
			row.WakeAt = at
		}
		row.Runnable = true
		row.WakeSeq++
		row.UpdatedAt = now
		b.userWakes[key] = row
	} else {
		b.userWakes[key] = docstore.UserWake{
			UserID:     userID,
			Lane:       lane,
			Runnable:   true,
			WakeSeq:    1,
			WakeAt:     at,
			Owner:      "",
			LeaseUntil: nil,
			UpdatedAt:  now,
		}
	}
	if wakeAt.IsZero() || !at.After(now) {
		return []docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.UserWakePayload(userID, lane),
		}}
	}
	return nil
}

// Standalone READ COMMITTED WakeWriter methods on *Backend.

func (b *Backend) WakeTask(ctx context.Context, userID, runID string, wakeAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.faultInjector().maybeLeaseErrFor("wake_task:" + userID + ":" + runID); err != nil {
		return err
	}
	b.mu.Lock()
	events := b.applyWakeTaskLocked(userID, runID, wakeAt, b.now())
	b.mu.Unlock()
	b.emitWakeEvents(events)
	return nil
}

func (b *Backend) WakeWaitingTasks(ctx context.Context, userID, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.faultInjector().maybeLeaseErrFor("wake_waiting:" + userID + ":" + reason); err != nil {
		return err
	}
	b.mu.Lock()
	events := b.applyWakeWaitingTasksLocked(userID, reason, b.now())
	b.mu.Unlock()
	b.emitWakeEvents(events)
	return nil
}

func (b *Backend) WakeUser(ctx context.Context, userID, lane string, wakeAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.faultInjector().maybeLeaseErrFor("wake_user:" + userID + ":" + lane); err != nil {
		return err
	}
	b.mu.Lock()
	events := b.applyWakeUserLocked(userID, lane, wakeAt, b.now())
	b.mu.Unlock()
	b.emitWakeEvents(events)
	return nil
}

func (b *Backend) NotifyPause(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.faultInjector().maybeLeaseErrFor("notify_pause"); err != nil {
		return err
	}
	b.emitWakeEvents([]docstore.WakeEvent{{
		Channel: docstore.WakeChannel,
		Payload: docstore.WakePayloadPause,
	}})
	return nil
}

// Inner task discovery (nori_task_leases), READ COMMITTED semantics.

func (b *Backend) ClaimTask(ctx context.Context, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	if err := ctx.Err(); err != nil {
		return docstore.TaskLease{}, false, err
	}
	if owner == "" {
		return docstore.TaskLease{}, false, errEmptyLeaseOwner
	}
	if err := b.faultInjector().maybeLeaseErrFor("claim_task:" + owner); err != nil {
		return docstore.TaskLease{}, false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	var best docstore.TaskLease
	found := false
	for _, row := range b.taskLeases {
		if !row.Runnable || row.WakeAt.After(now) {
			continue
		}
		if row.LeaseUntil != nil && row.LeaseUntil.After(now) {
			continue
		}
		if !found || compareTaskLeaseClaimOrder(row, best) < 0 {
			best = row
			found = true
		}
	}
	if !found {
		return docstore.TaskLease{}, false, nil
	}
	until := now.Add(lease)
	best.Owner = owner
	best.LeaseUntil = &until
	best.UpdatedAt = now
	b.taskLeases[docstore.TaskKey{UserID: best.UserID, RunID: best.RunID}] = best
	return cloneTaskLease(best), true, nil
}

func (b *Backend) ClaimTaskByID(ctx context.Context, userID, runID, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	if err := ctx.Err(); err != nil {
		return docstore.TaskLease{}, false, err
	}
	if owner == "" {
		return docstore.TaskLease{}, false, errEmptyLeaseOwner
	}
	if err := b.faultInjector().maybeLeaseErrFor("claim_task_id:" + userID + ":" + runID); err != nil {
		return docstore.TaskLease{}, false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	key := docstore.TaskKey{UserID: userID, RunID: runID}
	row, ok := b.taskLeases[key]
	if !ok || !row.Runnable || row.WakeAt.After(now) {
		return docstore.TaskLease{}, false, nil
	}
	if row.LeaseUntil != nil && row.LeaseUntil.After(now) {
		return docstore.TaskLease{}, false, nil
	}
	until := now.Add(lease)
	row.Owner = owner
	row.LeaseUntil = &until
	row.UpdatedAt = now
	b.taskLeases[key] = row
	return cloneTaskLease(row), true, nil
}

func (b *Backend) RefreshTaskClaim(ctx context.Context, userID, runID, owner string, lease time.Duration) (docstore.TaskLease, bool, error) {
	if err := ctx.Err(); err != nil {
		return docstore.TaskLease{}, false, err
	}
	if owner == "" {
		return docstore.TaskLease{}, false, nil
	}
	if err := b.faultInjector().maybeLeaseErrFor("refresh_task:" + userID + ":" + runID); err != nil {
		return docstore.TaskLease{}, false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	key := docstore.TaskKey{UserID: userID, RunID: runID}
	row, ok := b.taskLeases[key]
	if !ok || row.Owner != owner || !row.Runnable || row.LeaseUntil == nil || !row.LeaseUntil.After(now) {
		return docstore.TaskLease{}, false, nil
	}
	until := now.Add(lease)
	row.LeaseUntil = &until
	row.UpdatedAt = now
	b.taskLeases[key] = row
	return cloneTaskLease(row), true, nil
}

func (b *Backend) RenewTaskLeases(ctx context.Context, owner string, keys []docstore.TaskKey, lease time.Duration) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if owner == "" || len(keys) == 0 {
		return 0, nil
	}
	if err := b.faultInjector().maybeLeaseErrFor("renew_task:" + owner); err != nil {
		return 0, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	seen := make(map[docstore.TaskKey]bool, len(keys))
	renewed := 0
	for _, key := range keys {
		if seen[key] {
			continue
		}
		seen[key] = true
		row, ok := b.taskLeases[key]
		if !ok || row.Owner != owner || !row.Runnable || row.LeaseUntil == nil || !row.LeaseUntil.After(now) {
			continue
		}
		until := now.Add(lease)
		row.LeaseUntil = &until
		row.UpdatedAt = now
		b.taskLeases[key] = row
		renewed++
	}
	return renewed, nil
}

func (b *Backend) ParkTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, reason string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if owner == "" {
		return false, nil
	}
	if err := b.faultInjector().maybeLeaseErrFor("park_task:" + userID + ":" + runID); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	key := docstore.TaskKey{UserID: userID, RunID: runID}
	row, ok := b.taskLeases[key]
	if !ok || row.Owner != owner || row.WakeSeq != wakeSeq || row.LeaseUntil == nil || !row.LeaseUntil.After(now) {
		return false, nil
	}
	row.Runnable = false
	row.WaitingReason = reason
	row.Owner = ""
	row.LeaseUntil = nil
	row.UpdatedAt = now
	b.taskLeases[key] = row
	return true, nil
}

func (b *Backend) ReleaseTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, nextWake time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner == "" {
		return nil
	}
	if err := b.faultInjector().maybeLeaseErrFor("release_task:" + userID + ":" + runID); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	key := docstore.TaskKey{UserID: userID, RunID: runID}
	row, ok := b.taskLeases[key]
	if !ok || row.Owner != owner {
		return nil
	}
	now := b.now()
	row.Owner = ""
	row.LeaseUntil = nil
	row.UpdatedAt = now
	if !nextWake.IsZero() && (wakeSeq <= 0 || row.WakeSeq == wakeSeq) {
		row.WakeAt = nextWake.UTC()
	}
	b.taskLeases[key] = row
	return nil
}

func (b *Backend) DeleteTaskLease(ctx context.Context, userID, runID string, wakeSeq int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := b.faultInjector().maybeLeaseErrFor("delete_task:" + userID + ":" + runID); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	key := docstore.TaskKey{UserID: userID, RunID: runID}
	row, ok := b.taskLeases[key]
	if !ok {
		return false, nil
	}
	if wakeSeq > 0 && row.WakeSeq != wakeSeq {
		return false, nil
	}
	delete(b.taskLeases, key)
	return true, nil
}

func (b *Backend) EnsureTaskLease(ctx context.Context, userID, runID string, wakeAt time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := b.faultInjector().maybeLeaseErrFor("ensure_task:" + userID + ":" + runID); err != nil {
		return false, err
	}
	b.mu.Lock()
	key := docstore.TaskKey{UserID: userID, RunID: runID}
	if _, exists := b.taskLeases[key]; exists {
		b.mu.Unlock()
		return false, nil
	}
	now := b.now()
	at := effectiveWakeAt(wakeAt, now)
	b.taskLeases[key] = docstore.TaskLease{
		UserID:        userID,
		RunID:         runID,
		Runnable:      true,
		WaitingReason: docstore.WaitNone,
		Owner:         "",
		WakeSeq:       1,
		LeaseUntil:    nil,
		WakeAt:        at,
		UpdatedAt:     now,
	}
	b.mu.Unlock()

	if wakeAt.IsZero() || !at.After(now) {
		b.emitWakeEvents([]docstore.WakeEvent{{
			Channel: docstore.WakeChannel,
			Payload: docstore.TaskWakePayload(userID, runID),
		}})
	}
	return true, nil
}

func (b *Backend) ListTaskLeases(ctx context.Context, userID string) ([]docstore.TaskLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.faultInjector().maybeLeaseErrFor("list_task:" + userID); err != nil {
		return nil, err
	}
	b.mu.Lock()
	out := make([]docstore.TaskLease, 0, len(b.taskLeases))
	for _, row := range b.taskLeases {
		if userID != "" && row.UserID != userID {
			continue
		}
		out = append(out, cloneTaskLease(row))
	}
	b.mu.Unlock()

	slices.SortFunc(out, compareTaskLeaseKeyOrder)
	return out, nil
}

// Outer conversation & scheduler discovery (nori_user_wakes), READ COMMITTED semantics.

func (b *Backend) ClaimUserWake(ctx context.Context, lane, owner string, lease time.Duration) (docstore.UserWake, bool, error) {
	if err := ctx.Err(); err != nil {
		return docstore.UserWake{}, false, err
	}
	if owner == "" {
		return docstore.UserWake{}, false, errEmptyLeaseOwner
	}
	if err := b.faultInjector().maybeLeaseErrFor("claim_user:" + lane + ":" + owner); err != nil {
		return docstore.UserWake{}, false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	var best docstore.UserWake
	found := false
	for _, row := range b.userWakes {
		if row.Lane != lane || !row.Runnable || row.WakeAt.After(now) {
			continue
		}
		if row.LeaseUntil != nil && row.LeaseUntil.After(now) {
			continue
		}
		if !found || compareUserWakeClaimOrder(row, best) < 0 {
			best = row
			found = true
		}
	}
	if !found {
		return docstore.UserWake{}, false, nil
	}
	until := now.Add(lease)
	best.Owner = owner
	best.LeaseUntil = &until
	best.UpdatedAt = now
	b.userWakes[userWakeKey{userID: best.UserID, lane: best.Lane}] = best
	return cloneUserWake(best), true, nil
}

func (b *Backend) RenewUserWake(ctx context.Context, userID, lane, owner string, lease time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if owner == "" {
		return false, nil
	}
	if err := b.faultInjector().maybeLeaseErrFor("renew_user:" + userID + ":" + lane); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	key := userWakeKey{userID: userID, lane: lane}
	row, ok := b.userWakes[key]
	if !ok || row.Owner != owner || !row.Runnable || row.LeaseUntil == nil || !row.LeaseUntil.After(now) {
		return false, nil
	}
	until := now.Add(lease)
	row.LeaseUntil = &until
	row.UpdatedAt = now
	b.userWakes[key] = row
	return true, nil
}

func (b *Backend) ParkUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if owner == "" {
		return false, nil
	}
	if err := b.faultInjector().maybeLeaseErrFor("park_user:" + userID + ":" + lane); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	key := userWakeKey{userID: userID, lane: lane}
	row, ok := b.userWakes[key]
	if !ok || row.Owner != owner || row.WakeSeq != wakeSeq || row.LeaseUntil == nil || !row.LeaseUntil.After(now) {
		return false, nil
	}
	row.Runnable = false
	row.Owner = ""
	row.LeaseUntil = nil
	row.UpdatedAt = now
	b.userWakes[key] = row
	return true, nil
}

func (b *Backend) ReleaseUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64, nextWake time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner == "" {
		return nil
	}
	if err := b.faultInjector().maybeLeaseErrFor("release_user:" + userID + ":" + lane); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	key := userWakeKey{userID: userID, lane: lane}
	row, ok := b.userWakes[key]
	if !ok || row.Owner != owner {
		return nil
	}
	now := b.now()
	row.Owner = ""
	row.LeaseUntil = nil
	row.UpdatedAt = now
	if !nextWake.IsZero() {
		nw := nextWake.UTC()
		if wakeSeq <= 0 || row.WakeSeq == wakeSeq {
			row.WakeAt = nw
		} else if nw.Before(row.WakeAt) {
			row.WakeAt = nw
		}
	}
	b.userWakes[key] = row
	return nil
}

func (b *Backend) ListUserWakes(ctx context.Context, userID string) ([]docstore.UserWake, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.faultInjector().maybeLeaseErrFor("list_user:" + userID); err != nil {
		return nil, err
	}
	b.mu.Lock()
	out := make([]docstore.UserWake, 0, len(b.userWakes))
	for _, row := range b.userWakes {
		if userID != "" && row.UserID != userID {
			continue
		}
		out = append(out, cloneUserWake(row))
	}
	b.mu.Unlock()

	slices.SortFunc(out, compareUserWakeKeyOrder)
	return out, nil
}

// Transactional WakeWriter methods on *transaction.
// All wake mutations and notifications are buffered and applied atomically
// ONLY when commitTx succeeds; WakeEvents are emitted ONLY AFTER b.mu unlocks.

func (t *transaction) WakeTask(ctx context.Context, userID, runID string, wakeAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.readOnly {
		return ErrReadOnly
	}
	t.wakeOps = append(t.wakeOps, stagedWakeOp{
		kind:   wakeOpTask,
		userID: userID,
		runID:  runID,
		wakeAt: wakeAt,
	})
	return nil
}

func (t *transaction) WakeWaitingTasks(ctx context.Context, userID, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.readOnly {
		return ErrReadOnly
	}
	t.wakeOps = append(t.wakeOps, stagedWakeOp{
		kind:   wakeOpWaitingTasks,
		userID: userID,
		reason: reason,
	})
	return nil
}

func (t *transaction) WakeUser(ctx context.Context, userID, lane string, wakeAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.readOnly {
		return ErrReadOnly
	}
	t.wakeOps = append(t.wakeOps, stagedWakeOp{
		kind:   wakeOpUser,
		userID: userID,
		lane:   lane,
		wakeAt: wakeAt,
	})
	return nil
}

func (t *transaction) NotifyPause(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.readOnly {
		return ErrReadOnly
	}
	t.wakeOps = append(t.wakeOps, stagedWakeOp{
		kind: wakeOpPause,
	})
	return nil
}
