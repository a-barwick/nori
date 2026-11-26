package conversation

import (
	"context"
	"encoding/json"
	"errors"

	"nori/internal/state"
)

var (
	// ErrProviderUnavailable marks a transient transport/availability failure reaching the model provider.
	ErrProviderUnavailable = errors.New("model request interrupted or unavailable")
	// ErrStreamInterrupted marks an incomplete response stream from the model provider.
	ErrStreamInterrupted = errors.New("model stream interrupted; request will not be repeated")
	// ErrAttemptInterrupted marks an attempt interrupted before its model reply or plan was persisted.
	ErrAttemptInterrupted = errors.New("conversation attempt interrupted before reply was saved")
)

type ReplyModel interface {
	Name() string
	Respond(ctx context.Context, request ReplyRequest) (Reply, error)
}

type ReplyRequest struct {
	Messages         []json.RawMessage
	Stream           func(string) error
	Tools            json.RawMessage
	Foreground       bool
	CommandPlan      bool
	NoDispatch       bool
	WorkRequests     []WorkRequest
	TaskIDs          []string
	MemoryIDs        []string
	TelemetryInput   string
	CacheBreakpoints []int
	RecordUsage      func(context.Context, state.Usage) error
}

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
	Usage       state.Usage
	Message     json.RawMessage
	Text        string
	Calls       []ToolCall
	Structured  bool
	ServedModel string
}

func modelMessage(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}
