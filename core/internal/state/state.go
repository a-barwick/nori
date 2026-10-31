// Package state owns the durable single-owner conversation and private artifacts.
package state

import (
	"context"
	"errors"
	"time"
)

const Owner = "austinbarwickdavis@gmail.com"

// CleanupGrace reserves time for stopping computer work and persisting its result.
const CleanupGrace = 20 * time.Second

var ErrBusy = errors.New("task is not open")
var ErrDuplicate = errors.New("run ID already exists")
var ErrPaused = errors.New("task execution is paused")
var ErrNotFound = errors.New("not found")
var ErrCostUnknown = errors.New("Model cost is unknown for a provider request; task stopped before further model calls. Recorded spend includes only known costs.")

type Control struct {
	Paused bool `json:"paused" firestore:"paused"`
}

// Check fences execution without tying durable tasks to a temporary session.
func (c Control) Check() error {
	if c.Paused {
		return ErrPaused
	}
	return nil
}

type Message struct {
	ID        string    `json:"id" firestore:"id"`
	TurnID    string    `json:"turn_id,omitempty" firestore:"turn_id,omitempty"`
	RunID     string    `json:"run_id" firestore:"run_id"`
	Role      string    `json:"role" firestore:"role"`
	Text      string    `json:"text" firestore:"text"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
}
type Event struct {
	Kind      string    `json:"kind" firestore:"kind"`
	Text      string    `json:"text" firestore:"text"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
}
type Artifact struct {
	ID   string `json:"id" firestore:"id"`
	Kind string `json:"kind" firestore:"kind"`
}
type Run struct {
	TraceContext          map[string]string `json:"-" firestore:"trace_context,omitempty"`
	OriginTraceContext    map[string]string `json:"-" firestore:"origin_trace_context,omitempty"`
	PublicationPending    bool              `json:"publication_pending,omitempty" firestore:"publication_pending,omitempty"`
	PublicationGeneration int64             `json:"publication_generation,omitempty" firestore:"publication_generation,omitempty"`
	CleanupPending        bool              `json:"cleanup_pending,omitempty" firestore:"cleanup_pending,omitempty"`
	ProtocolVersion       int               `json:"protocol_version,omitempty" firestore:"protocol_version,omitempty"`
	Request               string            `json:"request,omitempty" firestore:"request,omitempty"`
	Position              int64             `json:"position,omitempty" firestore:"position,omitempty"`
	Question              *Question         `json:"question,omitempty" firestore:"question,omitempty"`
	EventSequence         int64             `json:"event_sequence,omitempty" firestore:"event_sequence,omitempty"`
	Result                string            `json:"result,omitempty" firestore:"result,omitempty"`
	OperationRefs         []string          `json:"operation_refs,omitempty" firestore:"operation_refs,omitempty"`
	Temporal              bool              `json:"temporal,omitempty" firestore:"temporal,omitempty"`
	Revision              int64             `json:"revision,omitempty" firestore:"revision,omitempty"`
	Incorporated          int64             `json:"incorporated,omitempty" firestore:"incorporated,omitempty"`
	Waiting               string            `json:"waiting,omitempty" firestore:"waiting,omitempty"`
	// Spent and Budget are model USD from reported token usage; Budget grows
	// only by Austin's explicit continue.
	Spent           float64    `json:"spent_usd,omitempty" firestore:"spent_usd,omitempty"`
	CostUnknown     bool       `json:"cost_unknown,omitempty" firestore:"cost_unknown,omitempty"`
	Budget          float64    `json:"budget_usd,omitempty" firestore:"budget_usd,omitempty"`
	ID              string     `json:"id" firestore:"id"`
	Status          string     `json:"status" firestore:"status"`
	CancelRequested bool       `json:"cancel_requested" firestore:"cancel_requested"`
	StartedAt       time.Time  `json:"started_at" firestore:"started_at"`
	Deadline        time.Time  `json:"deadline" firestore:"deadline"`
	FinishedAt      *time.Time `json:"finished_at,omitempty" firestore:"finished_at,omitempty"`
	Error           string     `json:"error,omitempty" firestore:"error,omitempty"`
	Events          []Event    `json:"events" firestore:"events"`
	Artifacts       []Artifact `json:"artifacts" firestore:"artifacts"`
}

// Usage records one provider request independently from the inner task budget.
// A nil cost is unknown, never a free request. IDs are stable across writes.
type Usage struct {
	ID            string    `json:"id" firestore:"id"`
	InputID       string    `json:"input_id" firestore:"input_id"`
	Provider      string    `json:"provider" firestore:"provider"`
	Model         string    `json:"model" firestore:"model"`
	InputTokens   *int64    `json:"input_tokens,omitempty" firestore:"input_tokens,omitempty"`
	OutputTokens  *int64    `json:"output_tokens,omitempty" firestore:"output_tokens,omitempty"`
	TotalTokens   *int64    `json:"total_tokens,omitempty" firestore:"total_tokens,omitempty"`
	CostUSD       *float64  `json:"cost_usd,omitempty" firestore:"cost_usd,omitempty"`
	CostStatus    string    `json:"cost_status" firestore:"cost_status"`
	RequestStatus string    `json:"request_status" firestore:"request_status"`
	CreatedAt     time.Time `json:"created_at" firestore:"created_at"`
}

type Session struct {
	Messages []Message `json:"messages"`
	Runs     []Run     `json:"runs"`
	Paused   bool      `json:"paused"`
	Usage    []Usage   `json:"usage,omitempty"`
}

// Store is the run-state surface the agent steps and task activities use.
// Conversation and task-command operations live on *Google directly.
type Store interface {
	Control(context.Context) (Control, error)
	Session(context.Context) (Session, error)
	GetRun(context.Context, string) (Run, error)
	Spend(context.Context, string, float64, float64, bool) error
	Artifact(context.Context, string, Artifact) error
	PutPNG(context.Context, string, string, []byte) error
}
