package docstore

import (
	"context"
	"strings"
	"time"
)

const (
	WakeChannel = "nori_wake"

	LaneConversation = "conversation"
	LaneScheduler    = "scheduler"

	WaitNone           = ""
	WaitQuestion       = "question"
	WaitPaused         = "paused"
	WaitRoutingPending = "routing_pending"
	WaitFinishing      = "finishing"

	WakePayloadPause   = "pause"
	WakePayloadAllTask = "t:*"
	WakePayloadResync  = "resync"
)

type WakeEvent struct {
	Channel string
	Payload string
}

type WakeKind string

const (
	WakeKindUnknown      WakeKind = ""
	WakeKindTask         WakeKind = "task"
	WakeKindConversation WakeKind = "conversation"
	WakeKindScheduler    WakeKind = "scheduler"
	WakeKindPause        WakeKind = "pause"
	WakeKindResync       WakeKind = "resync"
)

type ParsedWake struct {
	Kind   WakeKind
	UserID string
	RunID  string
}

func TaskWakePayload(userID, runID string) string { return "t:" + userID + ":" + runID }
func UserTasksWakePayload(userID string) string   { return "t:" + userID + ":*" }

func UserWakePayload(userID, lane string) string {
	if lane == LaneScheduler {
		return "s:" + userID
	}
	return "c:" + userID
}

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

type WakeWriter interface {
	WakeTask(ctx context.Context, userID, runID string, wakeAt time.Time) error
	WakeWaitingTasks(ctx context.Context, userID, reason string) error
	WakeUser(ctx context.Context, userID, lane string, wakeAt time.Time) error
	NotifyPause(ctx context.Context) error
}

type TaskKey struct {
	UserID string
	RunID  string
}

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

type LeaseStore interface {
	ClaimTask(ctx context.Context, owner string, lease time.Duration) (TaskLease, bool, error)
	ClaimTaskByID(ctx context.Context, userID, runID, owner string, lease time.Duration) (TaskLease, bool, error)
	RefreshTaskClaim(ctx context.Context, userID, runID, owner string, lease time.Duration) (TaskLease, bool, error)
	RenewTaskLeases(ctx context.Context, owner string, keys []TaskKey, lease time.Duration) (int, error)
	ParkTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, reason string) (parked bool, err error)
	ReleaseTask(ctx context.Context, userID, runID, owner string, wakeSeq int64, nextWake time.Time) error
	DeleteTaskLease(ctx context.Context, userID, runID string, wakeSeq int64) (deleted bool, err error)
	EnsureTaskLease(ctx context.Context, userID, runID string, wakeAt time.Time) (created bool, err error)
	ListTaskLeases(ctx context.Context, userID string) ([]TaskLease, error)

	ClaimUserWake(ctx context.Context, lane, owner string, lease time.Duration) (UserWake, bool, error)
	RenewUserWake(ctx context.Context, userID, lane, owner string, lease time.Duration) (bool, error)
	ParkUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64) (parked bool, err error)
	ReleaseUserWake(ctx context.Context, userID, lane, owner string, wakeSeq int64, nextWake time.Time) error
	ListUserWakes(ctx context.Context, userID string) ([]UserWake, error)

	WakeWriter
}

type ScopedLeaseStore interface {
	LeaseStore
	ClaimTaskForUser(ctx context.Context, userID, owner string, lease time.Duration) (TaskLease, bool, error)
	ClaimUserWakeForUser(ctx context.Context, userID, lane, owner string, lease time.Duration) (UserWake, bool, error)
}

type WakeListener interface {
	ListenWakes(ctx context.Context, onEvent func(WakeEvent)) error
}

func (t *Transaction) HasWakeWriter() bool {
	_, ok := t.backend.(WakeWriter)
	return ok
}

func (t *Transaction) WakeTask(userID, runID string, wakeAt time.Time) error {
	w, ok := t.backend.(WakeWriter)
	if !ok {
		return nil
	}
	return w.WakeTask(t.ctx, userID, runID, wakeAt)
}

func (t *Transaction) WakeWaitingTasks(userID, reason string) error {
	w, ok := t.backend.(WakeWriter)
	if !ok {
		return nil
	}
	return w.WakeWaitingTasks(t.ctx, userID, reason)
}

func (t *Transaction) WakeUser(userID, lane string, wakeAt time.Time) error {
	w, ok := t.backend.(WakeWriter)
	if !ok {
		return nil
	}
	return w.WakeUser(t.ctx, userID, lane, wakeAt)
}

func (t *Transaction) NotifyPause() error {
	w, ok := t.backend.(WakeWriter)
	if !ok {
		return nil
	}
	return w.NotifyPause(t.ctx)
}
