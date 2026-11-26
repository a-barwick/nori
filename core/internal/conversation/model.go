package conversation

import (
	"context"
	"encoding/json"

	"nori/internal/state"
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
