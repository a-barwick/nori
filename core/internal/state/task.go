package state

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrSuperseded = errors.New("task revision changed; reconcile accepted input")
var ErrCancelled = errors.New("task cancellation requested")
var ErrRoutingPending = errors.New("accepted user input is awaiting conversation routing")
var ErrCommandInvalid = errors.New("invalid task command")
var ErrQuestionStale = errors.New("question is no longer current")

// TaskProtocolVersion tracks accepted-command and outer-publication semantics.
const TaskProtocolVersion = 2

// TaskWorkflowVersion tracks worker capabilities independently of that protocol.
// Missing versions belong to retained workers that cannot preserve M4 fields.
const TaskWorkflowVersion = 3

type Question struct {
	ResolvedBy string `json:"resolved_by,omitempty" firestore:"resolved_by,omitempty"`
	ID         string `json:"id" firestore:"id"`
	Kind       string `json:"kind" firestore:"kind"`
	Text       string `json:"text" firestore:"text"`
	CallID     string `json:"call_id,omitempty" firestore:"call_id,omitempty"`
	Revision   int64  `json:"instruction_revision" firestore:"revision"`
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
	CreatedAt     time.Time  `json:"created_at" firestore:"created_at"`
}

type CommandReceipt struct {
	Command Command `json:"command"`
	Run     Run     `json:"run"`
}

// Command and its delivery intent are committed in the acceptance transaction.
// ID is supplied by the caller and is stable across HTTP and relay retries.
type CommandKind string

const (
	CommandStart    CommandKind = "start"
	CommandSteer    CommandKind = "steer"
	CommandAnswer   CommandKind = "answer"
	CommandContinue CommandKind = "continue"
	CommandStop     CommandKind = "stop"
	CommandInput    CommandKind = "input"
	CommandCancel   CommandKind = "cancel"
	CommandHold     CommandKind = "hold"
)

type Command struct {
	Interrupt      bool        `json:"interrupt,omitempty" firestore:"interrupt,omitempty"`
	QuestionID     string      `json:"question_id,omitempty" firestore:"question_id,omitempty"`
	SourceInputIDs []string    `json:"source_input_ids,omitempty" firestore:"source_input_ids,omitempty"`
	ID             string      `json:"id" firestore:"id"`
	RunID          string      `json:"run_id" firestore:"run_id"`
	Kind           CommandKind `json:"kind" firestore:"kind"`
	Text           string      `json:"text" firestore:"text"`
	Position       int64       `json:"position" firestore:"position"`
	Revision       int64       `json:"revision" firestore:"revision"`
	Delivered      bool        `json:"delivered" firestore:"delivered"`
	CreatedAt      time.Time   `json:"created_at" firestore:"created_at"`
}

// InputKind makes a bare "continue" reply an explicit continue command. The
// task grants another budget only if it has asked its budget question;
// otherwise the command is ordinary input text.
func InputKind(message string) CommandKind {
	if strings.EqualFold(strings.TrimSpace(message), "continue") {
		return CommandContinue
	}
	return CommandInput
}

type TaskSnapshot struct {
	Run      Run
	Commands []Command
}

type TaskStore interface {
	GetPNG(ctx context.Context, run, id string) ([]byte, error)
	TaskSteps(ctx context.Context, runID string) ([]TaskStep, error)
	Store
	Accept(context.Context, Command) (Run, error)
	AcceptCommandBatch(context.Context, []Command, *ConversationFence) ([]CommandReceipt, error)
	OpenTasks(context.Context) ([]Run, error)
	PublicationTasks(context.Context) ([]Run, error)
	PublicationWoken(context.Context, string, int64) error
	CleanupTasks(context.Context) ([]Run, error)
	FinishCleanup(context.Context, string) error
	ProjectQuestion(context.Context, string, int64, Question) error
	OperationRevisions(context.Context, string) ([]int64, error)
	Pending(context.Context) ([]Command, error)
	Delivered(context.Context, string) error
	Reconcile(context.Context, string) (TaskSnapshot, error)
	Incorporate(context.Context, string, int64, int64) error
	Guard(context.Context, string, int64) (Run, error)
	Admit(context.Context, string, int64, string) error
	Publish(context.Context, string, int64, RunStatus, string, string, bool, bool) (Run, error)
	Project(context.Context, string, int64, string, string) error
	RecordTaskStep(context.Context, string, TaskStep) error
}
