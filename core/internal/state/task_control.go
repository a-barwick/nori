package state

import (
	"errors"
	"time"

	"nori/internal/extensions"
)

var ErrTaskBusy = errors.New("task lease is held by another worker")

const TaskLease = 30 * time.Second

type TaskPhase string

const (
	TaskPhaseSync    TaskPhase = "sync"
	TaskPhaseModel   TaskPhase = "model"
	TaskPhaseCalls   TaskPhase = "calls"
	TaskPhaseAsk     TaskPhase = "ask"
	TaskPhasePublish TaskPhase = "publish"
	TaskPhaseCleanup TaskPhase = "cleanup"
	TaskPhaseDone    TaskPhase = "done"
)

type TaskFence struct {
	Owner string
	Fence int64
}

type TaskCallRef struct {
	ID    string           `firestore:"id"`
	Name  string           `firestore:"name"`
	Index int              `firestore:"index"`
	Exec  *extensions.Exec `firestore:"exec,omitempty"`
}

type TaskPublication struct {
	RunID              string       `firestore:"run_id,omitempty"`
	Revision           int64        `firestore:"revision,omitempty"`
	Status             RunStatus    `firestore:"status,omitempty"`
	Detail             string       `firestore:"detail,omitempty"`
	Text               string       `firestore:"text,omitempty"`
	Ref                string       `firestore:"ref,omitempty"`
	StopConfirmed      bool         `firestore:"stop_confirmed,omitempty"`
	CostUnknown        bool         `firestore:"cost_unknown,omitempty"`
	Spent              float64      `firestore:"spent,omitempty"`
	BypassRouting      bool         `firestore:"bypass_routing,omitempty"`
	ExpectedIncomplete bool         `firestore:"expected_incomplete,omitempty"`
	Answer             *ResultDraft `firestore:"answer,omitempty"`
	CompletionCall     *TaskCallRef `firestore:"completion_call,omitempty"`
}

type TaskControl struct {
	RunID string    `firestore:"run_id"`
	Phase TaskPhase `firestore:"phase"`

	Owner      string    `firestore:"owner,omitempty"`
	Fence      int64     `firestore:"fence,omitempty"`
	LeaseUntil time.Time `firestore:"lease_until,omitempty"`

	Ref      string `firestore:"ref,omitempty"`
	Revision int64  `firestore:"revision,omitempty"`
	Position int64  `firestore:"position,omitempty"`

	Request   string `firestore:"request,omitempty"`
	Subtask   bool   `firestore:"subtask,omitempty"`
	Inherited bool   `firestore:"inherited,omitempty"`

	Models int `firestore:"models,omitempty"`
	Tools  int `firestore:"tools,omitempty"`
	Checks int `firestore:"checks,omitempty"`

	ModelAttempts       int      `firestore:"model_attempts,omitempty"`
	PaidTools           int      `firestore:"paid_tools,omitempty"`
	ConsecutiveInvalid  int      `firestore:"consecutive_invalid,omitempty"`
	TotalInvalid        int      `firestore:"total_invalid,omitempty"`
	EstimatedUnknownUSD float64  `firestore:"estimated_unknown_usd,omitempty"`
	RecentToolSigs      []string `firestore:"recent_tool_sigs,omitempty"`

	PendingModelID     string     `firestore:"pending_model_id,omitempty"`
	ModelRetries       int        `firestore:"model_retries,omitempty"`
	ModelStepStartedAt *time.Time `firestore:"model_step_started_at,omitempty"`

	PendingCalls []TaskCallRef `firestore:"pending_calls,omitempty"`
	CallIndex    int           `firestore:"call_index,omitempty"`
	CallsChanged bool          `firestore:"calls_changed,omitempty"`
	CallsRevised bool          `firestore:"calls_revised,omitempty"`

	Question           *Question        `firestore:"question,omitempty"`
	QuestionPublished  bool             `firestore:"question_published,omitempty"`
	PendingPublication *TaskPublication `firestore:"pending_publication,omitempty"`
}

func CanAcquireTask(c TaskControl, now time.Time) error {
	if c.Owner != "" && c.LeaseUntil.After(now) {
		return ErrTaskBusy
	}
	return nil
}

func CheckTaskOwned(c TaskControl, f TaskFence, now time.Time) error {
	if f.Owner == "" || c.Owner != f.Owner || c.Fence != f.Fence || !c.LeaseUntil.After(now) {
		return ErrTaskBusy
	}
	return nil
}

func LeaseTask(c TaskControl, runID, owner string, now time.Time) (TaskControl, TaskFence) {
	if c.RunID == "" {
		c.RunID = runID
	}
	if c.Phase == "" {
		c.Phase = TaskPhaseSync
	}
	c.Owner = owner
	c.Fence++
	c.LeaseUntil = now.Add(TaskLease)
	return c, TaskFence{Owner: c.Owner, Fence: c.Fence}
}

func RenewTask(c TaskControl, f TaskFence, now time.Time) (TaskControl, error) {
	if err := CheckTaskOwned(c, f, now); err != nil {
		return c, err
	}
	c.LeaseUntil = now.Add(TaskLease)
	return c, nil
}

func ApplyTaskControlUpdate(stored, next TaskControl, f TaskFence, now time.Time) (TaskControl, error) {
	if err := CheckTaskOwned(stored, f, now); err != nil {
		return stored, err
	}
	if next.Models < stored.Models || next.Tools < stored.Tools || next.Checks < stored.Checks ||
		next.Position < stored.Position || next.Revision < stored.Revision {
		return stored, errors.New("task control counters and command watermarks cannot decrease")
	}
	next.RunID = stored.RunID
	next.Owner = stored.Owner
	next.Fence = stored.Fence
	next.LeaseUntil = stored.LeaseUntil
	if next.Phase == "" {
		next.Phase = TaskPhaseSync
	}
	return next, nil
}

func ReleaseTask(c TaskControl, f TaskFence, now time.Time) (TaskControl, error) {
	if err := CheckTaskOwned(c, f, now); err != nil {
		return c, err
	}
	c.Owner = ""
	c.LeaseUntil = time.Time{}
	return c, nil
}
