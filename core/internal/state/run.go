package state

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

var ErrSuperseded = errors.New("task revision changed; reconcile accepted input")
var ErrCancelled = errors.New("task cancellation requested")
var ErrRoutingPending = errors.New("accepted user input is awaiting conversation routing")
var ErrCommandInvalid = errors.New("invalid task command")
var ErrQuestionStale = errors.New("question is no longer current")

// ErrApprovalUsed means this write already left prepared, so an earlier attempt may have sent it.
var ErrApprovalUsed = errors.New("approval was already used")

// Every new task records these versions. No current code branches on them, and saved tasks with lower or missing
// values are read as stored. Earlier releases do branch on them, so a release rolled back after unpause still needs
// them.
const (
	TaskProtocolVersion = 2
	TaskWorkflowVersion = 3
)

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// Run is one task. The fields are grouped by what they describe; the stored and JSON field names do not depend on
// the order.
type Run struct {
	// Identity and request.
	ID      string `json:"id" firestore:"id"`
	Request string `json:"request,omitempty" firestore:"request,omitempty"`

	// Lifecycle. Waiting and Question describe what the task needs from Austin; Result or Error ends it. Progress is the
	// latest line from a long job the task is watching; it is replaced in place, so it never grows the saved task.
	Status          RunStatus  `json:"status" firestore:"status"`
	CancelRequested bool       `json:"cancel_requested" firestore:"cancel_requested"`
	StartedAt       time.Time  `json:"started_at" firestore:"started_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty" firestore:"finished_at,omitempty"`
	Waiting         string     `json:"waiting,omitempty" firestore:"waiting,omitempty"`
	Progress        string     `json:"progress,omitempty" firestore:"progress,omitempty"`
	Question        *Question  `json:"question,omitempty" firestore:"question,omitempty"`
	Result          string     `json:"result,omitempty" firestore:"result,omitempty"`
	Error           string     `json:"error,omitempty" firestore:"error,omitempty"`

	// Command ordering. Position counts accepted commands, Revision counts accepted instructions, and Incorporated is
	// the last position the worker has folded into its work. InterruptRevision is the revision of the latest
	// interrupting steer.
	Position          int64 `json:"position,omitempty" firestore:"position,omitempty"`
	Revision          int64 `json:"revision,omitempty" firestore:"revision,omitempty"`
	Incorporated      int64 `json:"incorporated,omitempty" firestore:"incorporated,omitempty"`
	InterruptRevision int64 `json:"interrupt_revision,omitempty" firestore:"interrupt_revision,omitempty"`

	// Model cost in USD from reported token usage. Budget grows only by Austin's explicit continue.
	Spent       float64 `json:"spent_usd,omitempty" firestore:"spent_usd,omitempty"`
	Budget      float64 `json:"budget_usd,omitempty" firestore:"budget_usd,omitempty"`
	CostUnknown bool    `json:"cost_unknown,omitempty" firestore:"cost_unknown,omitempty"`

	// Subtasks. ParentID names the task this one works for, and ReadOnly is the limit it inherited from that parent.
	// A parent's Subtasks are the ones it started, and SubtaskSpent is their cost, which counts against the parent's budget.
	ParentID     string        `json:"parent_id,omitempty" firestore:"parent_id,omitempty"`
	ReadOnly     bool          `json:"read_only,omitempty" firestore:"read_only,omitempty"`
	Subtasks     []SubtaskLink `json:"subtasks,omitempty" firestore:"subtasks,omitempty"`
	SubtaskSpent float64       `json:"subtask_spent_usd,omitempty" firestore:"subtask_spent_usd,omitempty"`

	// Hand-off to the outer loop. EventSequence numbers the events this task has put in the conversation inbox, Holds
	// are conversation questions waiting for the outcome, and publication is deferred while user input awaits routing.
	EventSequence         int64      `json:"event_sequence,omitempty" firestore:"event_sequence,omitempty"`
	Holds                 []TaskHold `json:"holds,omitempty" firestore:"holds,omitempty"`
	PublicationPending    bool       `json:"publication_pending,omitempty" firestore:"publication_pending,omitempty"`
	PublicationGeneration int64      `json:"publication_generation,omitempty" firestore:"publication_generation,omitempty"`

	// Admitted tool operations and whether the computer still needs cleanup.
	OperationRefs  []string `json:"operation_refs,omitempty" firestore:"operation_refs,omitempty"`
	CleanupPending bool     `json:"cleanup_pending,omitempty" firestore:"cleanup_pending,omitempty"`

	// Display projections.
	Events    []Event    `json:"events" firestore:"events"`
	Artifacts []Artifact `json:"artifacts" firestore:"artifacts"`

	// W3C trace parents for the latest sampled command and for the original acceptance. Never sent over JSON.
	TraceContext       map[string]string `json:"-" firestore:"trace_context,omitempty"`
	OriginTraceContext map[string]string `json:"-" firestore:"origin_trace_context,omitempty"`

	// Kept so saved tasks round-trip unchanged. New tasks record WorkflowVersion, ProtocolVersion and Temporal; none
	// sets Deadline, and nothing reads any of them to choose behavior.
	WorkflowVersion int       `json:"workflow_version,omitempty" firestore:"workflow_version,omitempty"`
	ProtocolVersion int       `json:"protocol_version,omitempty" firestore:"protocol_version,omitempty"`
	Temporal        bool      `json:"temporal,omitempty" firestore:"temporal,omitempty"`
	Deadline        time.Time `json:"deadline" firestore:"deadline"`
}

type Question struct {
	ResolvedBy string `json:"resolved_by,omitempty" firestore:"resolved_by,omitempty"`
	ID         string `json:"id" firestore:"id"`
	Kind       string `json:"kind" firestore:"kind"`
	Text       string `json:"text" firestore:"text"`
	CallID     string `json:"call_id,omitempty" firestore:"call_id,omitempty"`
	Revision   int64  `json:"instruction_revision" firestore:"revision"`
	// ReadOnly limits subtasks started for a work request: the parent could only read when it asked.
	ReadOnly bool `json:"read_only,omitempty" firestore:"read_only,omitempty"`
}

// TaskEvent is a task outcome or need that the outer loop reads from the conversation inbox.
type TaskEvent struct {
	Holds         []TaskHold `json:"holds,omitempty" firestore:"holds,omitempty"`
	ID            string     `json:"id" firestore:"id"`
	RunID         string     `json:"run_id" firestore:"run_id"`
	Kind          string     `json:"kind" firestore:"kind"`
	Sequence      int64      `json:"sequence" firestore:"sequence"`
	Revision      int64      `json:"instruction_revision" firestore:"revision"`
	Text          string     `json:"text,omitempty" firestore:"text,omitempty"`
	QuestionID    string     `json:"question_id,omitempty" firestore:"question_id,omitempty"`
	Reason        string     `json:"reason,omitempty" firestore:"reason,omitempty"`
	OperationRefs []string   `json:"operation_refs,omitempty" firestore:"operation_refs,omitempty"`
	CreatedAt     time.Time  `json:"created_at" firestore:"created_at"`
}

// TaskPosition is the number of commands the task has accepted. Saved tasks from the first command protocol counted
// accepted commands in Revision and may have no Position.
func TaskPosition(r Run) int64 { return max(r.Position, r.Revision) }

// GuardRun checks task ownership without execution admission. Publishing an already-paid answer may proceed during
// pause; stale revisions may not. Callers that start new paid work also check Control.Check.
func GuardRun(r Run, revision int64) error {
	if r.CancelRequested {
		return ErrCancelled
	}
	if r.Revision != revision || r.Incorporated != TaskPosition(r) {
		return ErrSuperseded
	}
	if r.Status != RunRunning {
		return ErrBusy
	}
	return nil
}

// IncorporateRequest records that a worker folded the task's accepted commands, through Position, into its work at
// Revision.
type IncorporateRequest struct {
	RunID    string
	Revision int64
	Position int64
}

// ProjectRequest adds a progress event to a task at Revision. Kind "waiting" also sets the task's waiting text.
type ProjectRequest struct {
	RunID    string
	Revision int64
	Kind     string
	Text     string
}

// Incorporation says what recording a worker's incorporated position changes.
type Incorporation struct {
	// Noop is set when a wake carried no new commands. The task is not rewritten while a user answers its question.
	Noop bool
	// ClearQuestion drops the waiting text and the question because new commands superseded them.
	ClearQuestion bool
}

// PlanIncorporation applies the revision and position fence for a worker folding accepted commands into its work.
func PlanIncorporation(r Run, revision, position int64) (Incorporation, error) {
	if r.Revision != revision {
		return Incorporation{}, ErrSuperseded
	}
	if position != TaskPosition(r) || position < r.Incorporated {
		return Incorporation{}, ErrSuperseded
	}
	if position == r.Incorporated {
		return Incorporation{Noop: true}, nil
	}
	plan := Incorporation{}
	if r.Question == nil || r.Question.ResolvedBy != "" || r.Question.Revision != revision {
		plan.ClearQuestion = true
	}
	return plan, nil
}

// ProjectionEvent returns the display event for a progress projection and the task's waiting text afterward.
func ProjectionEvent(kind, text string, now time.Time) (waiting string, event Event) {
	if kind == "waiting" {
		waiting = text
	}
	return waiting, Event{Kind: kind, Text: text, CreatedAt: now}
}

// QuestionEvent validates a worker question and builds the inbox event that announces it. replay is set when the
// task already holds this question.
func QuestionEvent(run Run, revision int64, question Question, now time.Time) (event TaskEvent, replay bool, err error) {
	if question.ID == "" || question.Text == "" || question.Revision != revision ||
		(question.Kind != "input" && question.Kind != "budget" && question.Kind != "approval" && question.Kind != QuestionWork) {
		return TaskEvent{}, false, fmt.Errorf("invalid task question")
	}
	if run.Question != nil && run.Question.ID == question.ID {
		return TaskEvent{}, true, nil
	}
	event = TaskEvent{
		ID: question.ID, RunID: run.ID, Kind: "needs_input", Sequence: run.EventSequence + 1, Revision: revision,
		Text: question.Text, QuestionID: question.ID, Reason: question.Kind, OperationRefs: run.OperationRefs, CreatedAt: now,
	}
	return event, false, nil
}

// WithQuestion returns the run waiting on question after event was added to the inbox.
func WithQuestion(run Run, question Question, event TaskEvent) Run {
	run.Question, run.Waiting, run.EventSequence = &question, question.Text, event.Sequence
	return run
}

// PublishRequest asks to end a task with a result, failure or stop.
type PublishRequest struct {
	RunID    string
	Revision int64
	Status   RunStatus
	// Detail is the failure or stop reason. Text is the result shown to Austin.
	Detail string
	Text   string
	// CostUnknown marks the task as having an unpriced provider request.
	CostUnknown bool
	// Spent is the task's final cost, saved with its outcome so a parent's budget never misses it.
	Spent float64
	// BypassRouting publishes without waiting for pending user input to be routed.
	BypassRouting bool
}

// GatesOnRouting reports whether publication must wait for pending user input to be routed. A stop, an unknown cost
// and an explicit bypass publish immediately.
func (p PublishRequest) GatesOnRouting() bool {
	return !(p.Status == RunCancelled || p.CostUnknown || p.BypassRouting)
}

// AdmitPublication fences a publish against the stored run. It returns proceed=false for a task that already ended,
// and the request with the revision a requested stop applies to.
func AdmitPublication(run Run, req PublishRequest) (admitted PublishRequest, proceed bool, err error) {
	if run.Status != RunRunning {
		return req, false, nil
	}
	if req.Status == RunCancelled && run.CancelRequested {
		req.Revision = run.Revision
	}
	if req.Status == RunCompleted {
		if err := GuardRun(run, req.Revision); err != nil {
			return req, false, err
		}
	} else if run.Revision != req.Revision {
		return req, false, ErrSuperseded
	}
	return req, true, nil
}

// DeferPublication marks a task whose result waits for pending user input to be routed. The generation lets a wake
// clear only the deferral it was sent for.
func DeferPublication(run Run) Run {
	run.PublicationPending = true
	run.PublicationGeneration++
	return run
}

// FinishRun ends the task and builds the terminal inbox event. A requested stop wins over everything but failure. The
// caller adds the event to the inbox and, when it was newly added, sets EventSequence to the event's sequence.
func FinishRun(run Run, req PublishRequest, now time.Time) (Run, TaskEvent) {
	status, detail, text := req.Status, req.Detail, req.Text
	if run.CancelRequested && status != RunFailed {
		status, detail, text = RunCancelled, "Stopped by Austin.", ""
	}
	kind := "failure"
	switch status {
	case RunCompleted:
		kind = "result"
	case RunCancelled:
		kind = "stopped"
	}
	run.Holds = readyTaskHolds(run.Holds)
	event := TaskEvent{
		Holds: pendingTaskHolds(run.Holds), ID: req.RunID + "-terminal", RunID: req.RunID, Kind: kind,
		Sequence: run.EventSequence + 1, Revision: req.Revision, Text: text, Reason: detail,
		OperationRefs: run.OperationRefs, CreatedAt: now,
	}
	run.PublicationPending = false
	run.CostUnknown = run.CostUnknown || req.CostUnknown
	run.Spent = max(run.Spent, req.Spent)
	run.Status, run.Error, run.Result, run.Waiting, run.Question, run.FinishedAt = status, detail, text, "", nil, &now
	return run, event
}

// ShouldClearPublication reports whether a wake for generation still applies to the run's deferred publication.
func ShouldClearPublication(run Run, generation int64) bool {
	return run.PublicationPending && run.PublicationGeneration == generation
}

// OpenOnly keeps the runs that are still running.
func OpenOnly(runs []Run) []Run {
	return slices.DeleteFunc(runs, func(run Run) bool { return run.Status != RunRunning })
}

// UniqueRevisions returns the distinct revisions in ascending order.
func UniqueRevisions(revisions []int64) []int64 {
	seen := map[int64]bool{}
	unique := []int64{}
	for _, revision := range revisions {
		if !seen[revision] {
			seen[revision] = true
			unique = append(unique, revision)
		}
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	return unique
}

func sortRunsNewestFirst(runs []Run) {
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
}
