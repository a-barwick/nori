package state

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"nori/internal/prompts"
)

var ErrSuperseded = errors.New("task revision changed; reconcile accepted input")
var ErrCancelled = errors.New("task cancellation requested")
var ErrRoutingPending = errors.New("accepted user input is awaiting conversation routing")
var ErrCommandInvalid = errors.New("invalid task command")
var ErrQuestionStale = errors.New("question is no longer current")

type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

type Run struct {
	ID      string      `json:"id" firestore:"id"`
	Title   string      `json:"title,omitempty" firestore:"title,omitempty"`
	Request string      `json:"request,omitempty" firestore:"request,omitempty"`
	Origin  *TaskOrigin `json:"origin,omitempty" firestore:"origin,omitempty"`

	Status          RunStatus       `json:"status" firestore:"status"`
	CancelRequested bool            `json:"cancel_requested" firestore:"cancel_requested"`
	StartedAt       time.Time       `json:"started_at" firestore:"started_at"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty" firestore:"finished_at,omitempty"`
	Finishing       *PublishRequest `json:"finishing,omitempty" firestore:"finishing,omitempty"`
	Waiting         string          `json:"waiting,omitempty" firestore:"waiting,omitempty"`
	Progress        string          `json:"progress,omitempty" firestore:"progress,omitempty"`
	Question        *Question       `json:"question,omitempty" firestore:"question,omitempty"`
	Result          string          `json:"result,omitempty" firestore:"result,omitempty"`
	Error           string          `json:"error,omitempty" firestore:"error,omitempty"`

	Position          int64 `json:"position,omitempty" firestore:"position,omitempty"`
	Revision          int64 `json:"revision,omitempty" firestore:"revision,omitempty"`
	Incorporated      int64 `json:"incorporated,omitempty" firestore:"incorporated,omitempty"`
	InterruptRevision int64 `json:"interrupt_revision,omitempty" firestore:"interrupt_revision,omitempty"`

	Spent          float64         `json:"spent_usd,omitempty" firestore:"spent_usd,omitempty"`
	Budget         float64         `json:"budget_usd,omitempty" firestore:"budget_usd,omitempty"`
	CostUnknown    bool            `json:"cost_unknown,omitempty" firestore:"cost_unknown,omitempty"`
	ModelSelection *ModelSelection `json:"model_selection,omitempty" firestore:"model_selection,omitempty"`

	ParentID     string        `json:"parent_id,omitempty" firestore:"parent_id,omitempty"`
	ReadOnly     bool          `json:"read_only,omitempty" firestore:"read_only,omitempty"`
	Uploads      []Attachment  `json:"uploads,omitempty" firestore:"uploads,omitempty"`
	Revises      string        `json:"revises,omitempty" firestore:"revises,omitempty"`
	Subtasks     []SubtaskLink `json:"subtasks,omitempty" firestore:"subtasks,omitempty"`
	SubtaskSpent float64       `json:"subtask_spent_usd,omitempty" firestore:"subtask_spent_usd,omitempty"`

	EventSequence         int64      `json:"event_sequence,omitempty" firestore:"event_sequence,omitempty"`
	Holds                 []TaskHold `json:"holds,omitempty" firestore:"holds,omitempty"`
	PublicationPending    bool       `json:"publication_pending,omitempty" firestore:"publication_pending,omitempty"`
	PublicationGeneration int64      `json:"publication_generation,omitempty" firestore:"publication_generation,omitempty"`

	OperationRefs  []string `json:"operation_refs,omitempty" firestore:"operation_refs,omitempty"`
	CleanupPending bool     `json:"cleanup_pending,omitempty" firestore:"cleanup_pending,omitempty"`
	CleanupError   string   `json:"cleanup_error,omitempty" firestore:"cleanup_error,omitempty"`

	Events    []Event    `json:"events" firestore:"events"`
	Artifacts []Artifact `json:"artifacts" firestore:"artifacts"`

	TraceContext       map[string]string `json:"-" firestore:"trace_context,omitempty"`
	OriginTraceContext map[string]string `json:"-" firestore:"origin_trace_context,omitempty"`
}

type QuestionOption struct {
	ID    string `json:"id" firestore:"id"`
	Label string `json:"label" firestore:"label"`
}

type Question struct {
	ResolvedBy  string           `json:"resolved_by,omitempty" firestore:"resolved_by,omitempty"`
	ID          string           `json:"id" firestore:"id"`
	Kind        string           `json:"kind" firestore:"kind"`
	Text        string           `json:"text" firestore:"text"`
	CallID      string           `json:"call_id,omitempty" firestore:"call_id,omitempty"`
	Revision    int64            `json:"instruction_revision" firestore:"revision"`
	ReadOnly    bool             `json:"read_only,omitempty" firestore:"read_only,omitempty"`
	Clears      []string         `json:"clears,omitempty" firestore:"clears,omitempty"`
	Input       string           `json:"input,omitempty" firestore:"input,omitempty"`
	Options     []QuestionOption `json:"options,omitempty" firestore:"options,omitempty"`
	Interactive bool             `json:"interactive,omitempty" firestore:"interactive,omitempty"`
}

type TaskOrigin struct {
	ConversationID  string `json:"conversation_id,omitempty" firestore:"conversation_id,omitempty"`
	MessageID       string `json:"message_id,omitempty" firestore:"message_id,omitempty"`
	AutomationID    string `json:"automation_id,omitempty" firestore:"automation_id,omitempty"`
	OccurrenceID    string `json:"occurrence_id,omitempty" firestore:"occurrence_id,omitempty"`
	ContinuesTaskID string `json:"continues_task_id,omitempty" firestore:"continues_task_id,omitempty"`
}

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
	Parts         []Part     `json:"parts,omitempty" firestore:"parts,omitempty"`
	CreatedAt     time.Time  `json:"created_at" firestore:"created_at"`
}

func TaskPosition(r Run) int64 { return max(r.Position, r.Revision) }

func GuardRun(r Run, revision int64) error {
	if r.CancelRequested {
		return ErrCancelled
	}
	if r.Revision != revision || r.Incorporated != TaskPosition(r) {
		return ErrSuperseded
	}
	if r.Status != RunRunning || r.Finishing != nil {
		return ErrBusy
	}
	return nil
}

type IncorporateRequest struct {
	RunID    string
	Revision int64
	Position int64
}

type ProjectRequest struct {
	RunID    string
	Revision int64
	Kind     string
	Text     string
}

type Incorporation struct {
	Noop          bool
	ClearQuestion bool
}

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

func ProjectionEvent(kind, text string, now time.Time) (waiting string, event Event) {
	if kind == "waiting" {
		waiting = text
	}
	return waiting, Event{Kind: kind, Text: text, CreatedAt: now}
}

func QuestionEvent(run Run, revision int64, question Question, now time.Time) (event TaskEvent, replay bool, err error) {
	if question.ID == "" || question.Text == "" || question.Revision != revision ||
		(question.Kind != "input" && question.Kind != QuestionWork) {
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

func WithQuestion(run Run, question Question, event TaskEvent) Run {
	run.Question, run.Waiting = &question, question.Text
	if !question.Interactive && event.Sequence > 0 {
		run.EventSequence = event.Sequence
	}
	return run
}

type PublishRequest struct {
	ExpectedIncomplete bool         `json:"-" firestore:"expected_incomplete"`
	RunID              string       `json:"-" firestore:"run_id"`
	Revision           int64        `json:"-" firestore:"revision"`
	Status             RunStatus    `json:"status" firestore:"status"`
	Detail             string       `json:"-" firestore:"detail"`
	Text               string       `json:"-" firestore:"text"`
	CostUnknown        bool         `json:"-" firestore:"cost_unknown"`
	Spent              float64      `json:"-" firestore:"spent"`
	BypassRouting      bool         `json:"-" firestore:"bypass_routing"`
	Answer             *ResultDraft `json:"-" firestore:"answer,omitempty"`
	Parts              []Part       `json:"-" firestore:"parts,omitempty"`
}

func (p PublishRequest) GatesOnRouting() bool {
	return !(p.Status == RunCancelled || p.BypassRouting)
}

func AdmitPublication(run Run, req PublishRequest) (admitted PublishRequest, proceed bool, err error) {
	if run.Status != RunRunning {
		return req, false, nil
	}
	if run.Finishing != nil {
		req = *run.Finishing
		if run.CancelRequested {
			req.Revision = run.Revision
			if req.Status != RunFailed {
				req.Status = RunCancelled
				req.Text, req.Detail, req.Answer = "", prompts.Runtime("stopped", nil), nil
			}
		}
		return req, true, nil
	}
	if req.Status != RunCompleted && req.Status != RunFailed && req.Status != RunCancelled {
		return req, false, ErrCommandInvalid
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

func DeferPublication(run Run) Run {
	run.PublicationPending = true
	run.PublicationGeneration++
	return run
}

func FinishRun(run Run, req PublishRequest, now time.Time) (Run, TaskEvent) {
	status, detail, text := req.Status, req.Detail, req.Text
	if run.CancelRequested && status != RunFailed {
		if status != RunCancelled || detail == "" {
			detail = prompts.Runtime("stopped", nil)
		}
		status, text = RunCancelled, ""
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
		OperationRefs: run.OperationRefs, Parts: req.Parts, CreatedAt: now,
	}
	run.PublicationPending = false
	run.Finishing = nil
	run.CostUnknown = run.CostUnknown || req.CostUnknown
	run.Spent = max(run.Spent, req.Spent)
	run.Status, run.Error, run.Result, run.Waiting, run.Question, run.FinishedAt = status, detail, text, "", nil, &now
	return run, event
}

func ShouldClearPublication(run Run, generation int64) bool {
	return run.PublicationPending && run.PublicationGeneration == generation
}

func OpenOnly(runs []Run) []Run {
	return slices.DeleteFunc(runs, func(run Run) bool { return run.Status != RunRunning })
}

func sortRunsNewestFirst(runs []Run) {
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt.After(runs[j].StartedAt) })
}
