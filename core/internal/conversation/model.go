package conversation

import (
	"context"
	"encoding/json"

	"nori/internal/state"
)

// ReplyModel writes the assistant's reply for one conversation turn. Messages arrive in the saved conversation
// format (role, content, tool calls); an adapter turns them into its provider's request, so endpoints, reasoning
// options, usage pricing and provider error text stay out of the runner. A model never executes a tool: it only
// proposes task commands in Reply.Calls.
type ReplyModel interface {
	// Name is recorded on every attempt and trace.
	Name() string
	Respond(ctx context.Context, request ReplyRequest) (Reply, error)
}

// ReplyRequest is everything one turn gives the model. The runner builds a fresh request for each call, so adapters
// hold no per-call state.
type ReplyRequest struct {
	Messages []json.RawMessage
	// Stream receives cumulative visible text; returning an error stops provider I/O.
	Stream func(string) error
	// Tools are server-selected foreground tool schemas.
	Tools      json.RawMessage
	Foreground bool
	// CommandPlan independently plans dispatch/steer from user-authored context only.
	CommandPlan bool
	// NoDispatch disables every task command for introduction and notification-only turns.
	NoDispatch bool
	// WorkRequests are the open work requests on a turn with no user input. The model can only settle these.
	WorkRequests []WorkRequest
	// TaskIDs are the tasks shown in the supplied records; commands may only name these.
	TaskIDs []string
	// MemoryIDs are the saved memories; forget and corrections may only name these.
	MemoryIDs []string
	// ScheduleIDs are the open schedules; change_schedule may only name these.
	ScheduleIDs []string
	// TelemetryInput is the current user message, not the conversation history.
	TelemetryInput string
	// RecordUsage saves a usage entry before the provider is called and again once the result is known.
	RecordUsage func(context.Context, state.Usage) error
}

// WorkRequest is one task's open request_work question.
type WorkRequest struct{ TaskID, QuestionID string }

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Reply struct {
	Usage      state.Usage
	Message    json.RawMessage
	Text       string
	Calls      []ToolCall
	Structured bool
}

func modelMessage(value any) json.RawMessage {
	// All callers provide JSON-safe strings, structs, or string maps.
	data, _ := json.Marshal(value)
	return data
}
