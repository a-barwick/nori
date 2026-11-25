package docstore

import (
	"context"
	"strings"
	"time"
)

const (
	// WakeChannel is the Postgres LISTEN/NOTIFY channel for post-commit wake hints.
	WakeChannel = "nori_wake"

	// Outer discovery lanes in nori_user_wakes.
	LaneConversation = "conversation"
	LaneScheduler    = "scheduler"

	// Inner task parked waiting_reason values in nori_task_leases.
	WaitNone           = ""
	WaitQuestion       = "question"
	WaitPaused         = "paused"
	WaitRoutingPending = "routing_pending"
	WaitFinishing      = "finishing"

	// Broadcast / control payloads on WakeChannel.
	WakePayloadPause   = "pause"
	WakePayloadAllTask = "t:*"
	WakePayloadResync  = "resync"
)

// WakeEvent is buffered during a transaction and emitted via pg_notify AFTER commit.
type WakeEvent struct {
	Channel string // "nori_wake"
	Payload string // "t:<user>:<run>" | "t:<user>:*" | "t:*" | "c:<user>" | "s:<user>" | "pause" | "resync"
}

// WakeKind classifies a parsed WakeEvent payload.
type WakeKind string

const (
	WakeKindUnknown      WakeKind = ""
	WakeKindTask         WakeKind = "task"
	WakeKindConversation WakeKind = "conversation"
	WakeKindScheduler    WakeKind = "scheduler"
	WakeKindPause        WakeKind = "pause"
	WakeKindResync       WakeKind = "resync"
)

// ParsedWake is the structured form of a WakeEvent payload.
type ParsedWake struct {
	Kind   WakeKind
	UserID string // Empty for global wakes ("pause", "t:*").
	RunID  string // Specific run ID, "*" for user-wide task wake, or "" for global/non-task wakes.
}

func TaskWakePayload(userID, runID string) string { return "t:" + userID + ":" + runID }
func UserTasksWakePayload(userID string) string   { return "t:" + userID + ":*" }

func UserWakePayload(userID, lane string) string {
	if lane == LaneScheduler {
		return "s:" + userID
	}
	return "c:" + userID
}

// ParseWakePayload decodes a WakeEvent payload produced by TaskWakePayload,
// UserTasksWakePayload, UserWakePayload, WakePayloadAllTask, or WakePayloadPause.
func ParseWakePayload(payload string) ParsedWake {
	if payload == WakePayloadPause {
		return ParsedWake{Kind: WakeKindPause}
	}
	if payload == WakePayloadResync {
		return ParsedWake{Kind: WakeKindResync}
	}
	if payload == WakePayloadAllTask {
		return ParsedWake{Kind: WakeKindTask}
	}
	if rest, ok := strings.CutPrefix(payload, "t:"); ok {
		userID, runID, ok := strings.Cut(rest, ":")
		if !ok || userID == "" || runID == "" {
			return ParsedWake{}
		}
		return ParsedWake{Kind: WakeKindTask, UserID: userID, RunID: runID}
	}
	if userID, ok := strings.CutPrefix(payload, "c:"); ok && userID != "" {
		return ParsedWake{Kind: WakeKindConversation, UserID: userID}
	}
	if userID, ok := strings.CutPrefix(payload, "s:"); ok && userID != "" {
		return ParsedWake{Kind: WakeKindScheduler, UserID: userID}
	}
	return ParsedWake{}
}

// WakeWriter writes durable wake rows inside the same transaction as document mutations
// (or standalone in READ COMMITTED when called on a LeaseStore) and buffers post-commit
// WakeEvent notifications. Implemented by both Postgres (same pgx.Tx) and sim/memstore.
type WakeWriter interface {
	WakeTask(ctx context.Context, userID, runID string, wakeAt time.Time) error
	// WakeWaitingTasks marks parked tasks with waiting_reason=reason runnable again.
	// An empty userID wakes matching tasks across all users (e.g. global unpause).
	WakeWaitingTasks(ctx context.Context, userID, reason string) error
	WakeUser(ctx context.Context, userID, lane string, wakeAt time.Time) error
	// NotifyPause buffers a post-commit "pause" notification so active tasks stop promptly.
	NotifyPause(ctx context.Context) error
}

// TaskKey identifies one task lease row in nori_task_leases.
type TaskKey struct {
	UserID string
	RunID  string
}

// TaskLease mirrors one row of nori_task_leases.
type TaskLease struct {
	UserID        string
	RunID         string
	Runnable      bool
	WaitingReason string
	Owner         string
	WakeSeq       int64
	LeaseUntil    *time.Time
	WakeAt        time.Time
	UpdatedAt     time.Time
}

// UserWake mirrors one row of nori_user_wakes.
type UserWake struct {
	UserID     string
	Lane       string
	Runnable   bool
	WakeSeq    int64
	WakeAt     time.Time
	Owner      string
	LeaseUntil *time.Time
	UpdatedAt  time.Time
}

// LeaseStore provides READ COMMITTED discovery, claim, renewal, park, release,
// deletion, and repair operations over nori_task_leases and nori_user_wakes.
// Implemented by both state/postgres and sim/memstore.
type LeaseStore interface {
	// Inner task discovery (nori_task_leases).
	ClaimTask(ctx context.Context, owner string, lease time.Duration) (TaskLease, bool, error)
	ClaimTaskByID(ctx context.Context, userID, runID, owner string, lease time.Duration) (TaskLease, bool, error)
	// RefreshTaskClaim re-reads wake_seq and extends lease_until for an already-owned
	// runnable task lease. Call only at the top of a new turn before reading commands/state
	// (e.g. when ParkTask returned parked=false due to a concurrent wake).
	RefreshTaskClaim(ctx context.Context, userID, runID, owner string, lease time.Duration) (TaskLease, bool, error)
	RenewTaskLeases(ctx context.Context, owner string, keys []TaskKey, lease time.Duration) (int, error)
	// ParkTask transitions an owned task to runnable=false only if owner and wakeSeq still match.
	// When parked is false, a concurrent wake arrived after claim; the caller must not park.
	ParkTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, reason string) (parked bool, err error)
	// ReleaseTask clears owner/lease_until for an owned task. When nextWake is non-zero,
	// wake_at is updated to nextWake only when wakeSeq <= 0 or wake_seq still matches wakeSeq.
	ReleaseTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, nextWake time.Time) error
	// DeleteTaskLease removes a completed task lease. When wakeSeq > 0, deletion is guarded
	// by wake_seq = wakeSeq; when wakeSeq <= 0, deletion is unconditional.
	DeleteTaskLease(ctx context.Context, userID, runID string, wakeSeq int64) (deleted bool, err error)
	// EnsureTaskLease inserts a runnable lease row only if none exists (ON CONFLICT DO NOTHING),
	// returning created=true if a missing row was recreated by repair sweep.
	EnsureTaskLease(ctx context.Context, userID, runID string, wakeAt time.Time) (created bool, err error)
	ListTaskLeases(ctx context.Context, userID string) ([]TaskLease, error)

	// Outer conversation & scheduler discovery (nori_user_wakes).
	ClaimUserWake(ctx context.Context, lane, owner string, lease time.Duration) (UserWake, bool, error)
	RenewUserWake(ctx context.Context, userID, lane, owner string, lease time.Duration) (bool, error)
	ParkUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64) (parked bool, err error)
	// ReleaseUserWake clears owner/lease_until. When nextWake is non-zero, sets wake_at
	// (preserving an earlier wake_at if wake_seq changed concurrently).
	ReleaseUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64, nextWake time.Time) error
	ListUserWakes(ctx context.Context, userID string) ([]UserWake, error)

	// Standalone READ COMMITTED wake writes (with post-commit NOTIFY).
	WakeWriter
}

// ScopedLeaseStore is an optional LeaseStore extension supporting single-user
// task and user-wake claiming (used when worker.Options.UserID is set).
type ScopedLeaseStore interface {
	LeaseStore
	ClaimTaskForUser(ctx context.Context, userID, owner string, lease time.Duration) (TaskLease, bool, error)
	ClaimUserWakeForUser(ctx context.Context, userID, lane, owner string, lease time.Duration) (UserWake, bool, error)
}

// WakeListener delivers post-commit wake events (Postgres LISTEN nori_wake or
// in-memory memstore fan-out) until ctx is canceled.
type WakeListener interface {
	ListenWakes(ctx context.Context, onEvent func(WakeEvent)) error
}

// HasWakeWriter reports whether the transaction backend supports same-tx durable wakes.
func (t *Transaction) HasWakeWriter() bool {
	_, ok := t.backend.(WakeWriter)
	return ok
}

// WakeTask writes a durable task wake inside the current transaction when supported.
func (t *Transaction) WakeTask(userID, runID string, wakeAt time.Time) error {
	w, ok := t.backend.(WakeWriter)
	if !ok {
		return nil
	}
	return w.WakeTask(t.ctx, userID, runID, wakeAt)
}

// WakeWaitingTasks wakes parked tasks matching (userID, reason) inside the current transaction.
func (t *Transaction) WakeWaitingTasks(userID, reason string) error {
	w, ok := t.backend.(WakeWriter)
	if !ok {
		return nil
	}
	return w.WakeWaitingTasks(t.ctx, userID, reason)
}

// WakeUser writes a durable user wake for lane inside the current transaction when supported.
func (t *Transaction) WakeUser(userID, lane string, wakeAt time.Time) error {
	w, ok := t.backend.(WakeWriter)
	if !ok {
		return nil
	}
	return w.WakeUser(t.ctx, userID, lane, wakeAt)
}

// NotifyPause buffers a post-commit pause notification inside the current transaction when supported.
func (t *Transaction) NotifyPause() error {
	w, ok := t.backend.(WakeWriter)
	if !ok {
		return nil
	}
	return w.NotifyPause(t.ctx)
}
