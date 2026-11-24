package state

import (
	"errors"
	"time"

	"nori/internal/extensions"
)

var ErrTaskBusy = errors.New("task lease is held by another worker")

const TaskLease = 30 * time.Second

// TaskPhase identifies the authoritative inner-loop execution phase persisted in TaskControl.
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

// TaskFence identifies one worker's authoritative lease claim on a task.
type TaskFence struct {
	Owner string
	Fence int64
}

// TaskCallRef is the durable reference to one model-emitted tool call in a batch.
type TaskCallRef struct {
	ID    string           `firestore:"id"`
	Name  string           `firestore:"name"`
	Index int              `firestore:"index"`
	Exec  *extensions.Exec `firestore:"exec,omitempty"`
}

// TaskPublication is the durable pending terminal publication state for TaskPhasePublish.
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
	// CompletionCall records the complete() call that initiated this publication so a
	// superseded publish can close the call in transcript history.
	CompletionCall *TaskCallRef `firestore:"completion_call,omitempty"`
}

// TaskControl stores the authoritative lease, fence, and execution cursor for one task
// under owners/<user>/task_controls/<runID>. It is private runtime state omitted from public JSON.
type TaskControl struct {
	RunID string    `firestore:"run_id"`
	Phase TaskPhase `firestore:"phase"` // sync | model | calls | ask | publish | cleanup | done

	// Authoritative Lease & Fence (checked inside nori_records tx on EVERY state write).
	Owner      string    `firestore:"owner,omitempty"`
	Fence      int64     `firestore:"fence,omitempty"`
	LeaseUntil time.Time `firestore:"lease_until,omitempty"`

	// History chain head & command watermark.
	Ref      string `firestore:"ref,omitempty"`
	Revision int64  `firestore:"revision,omitempty"`
	Position int64  `firestore:"position,omitempty"`

	// Cached task metadata populated from SyncHistory.
	Request   string `firestore:"request,omitempty"`
	Subtask   bool   `firestore:"subtask,omitempty"`
	Inherited bool   `firestore:"inherited,omitempty"`

	// Deterministic counters for operation / question / completion-check IDs.
	Models int `firestore:"models,omitempty"`
	Tools  int `firestore:"tools,omitempty"`
	Checks int `firestore:"checks,omitempty"`

	// In-flight model step state (replaces Temporal modelActivityID + D-053 9m deadline).
	PendingModelID     string     `firestore:"pending_model_id,omitempty"`
	ModelRetries       int        `firestore:"model_retries,omitempty"`
	ModelStepStartedAt *time.Time `firestore:"model_step_started_at,omitempty"`

	// In-flight tool batch state (survives crash between call i and call i+1).
	PendingCalls []TaskCallRef `firestore:"pending_calls,omitempty"`
	CallIndex    int           `firestore:"call_index,omitempty"`
	CallsChanged bool          `firestore:"calls_changed,omitempty"`
	CallsRevised bool          `firestore:"calls_revised,omitempty"`

	// Pending question / publication.
	Question           *Question        `firestore:"question,omitempty"`
	QuestionPublished  bool             `firestore:"question_published,omitempty"`
	PendingPublication *TaskPublication `firestore:"pending_publication,omitempty"`
}

// CanAcquireTask reports ErrTaskBusy while another live owner holds the authoritative lease.
func CanAcquireTask(c TaskControl, now time.Time) error {
	if c.Owner != "" && c.LeaseUntil.After(now) {
		return ErrTaskBusy
	}
	return nil
}

// CheckTaskOwned verifies that f still holds the authoritative live lease for c.
func CheckTaskOwned(c TaskControl, f TaskFence, now time.Time) error {
	if f.Owner == "" || c.Owner != f.Owner || c.Fence != f.Fence || !c.LeaseUntil.After(now) {
		return ErrTaskBusy
	}
	return nil
}

// LeaseTask grants a new authoritative lease for owner, incrementing Fence monotonically.
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

// RenewTask extends LeaseUntil when f still owns c.
func RenewTask(c TaskControl, f TaskFence, now time.Time) (TaskControl, error) {
	if err := CheckTaskOwned(c, f, now); err != nil {
		return c, err
	}
	c.LeaseUntil = now.Add(TaskLease)
	return c, nil
}

// ApplyTaskControlUpdate verifies f owns stored and returns next while preserving the
// authoritative Owner, Fence, and LeaseUntil from stored (so concurrent lease renewals
// are never rewound by cursor saves). It also rejects monotonic counter regressions.
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

// ReleaseTask clears Owner and LeaseUntil while preserving Fence and cursor state.
func ReleaseTask(c TaskControl, f TaskFence, now time.Time) (TaskControl, error) {
	if err := CheckTaskOwned(c, f, now); err != nil {
		return c, err
	}
	c.Owner = ""
	c.LeaseUntil = time.Time{}
	return c, nil
}
