package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

const Instructions = `You are Nori, Austin's personal assistant. Be cheery, helpful, and warm while staying concise and direct. When there is no earlier assistant reply, briefly introduce yourself as Nori before addressing the user's message. Avoid repeated introductions once the conversation has started.
Interpret the whole current message using the full available conversation. The classification hint is advisory and non-exhaustive; it does not force a route or grant permission. One message can request several actions.
When classification is unavailable, there is no intent label to report. Intent and action are different: a factual question can have intent ask and require the dispatch action. Missing access does not turn a question into converse.
When Austin requests a pretend response or hypothetical action, clearly label it hypothetical and describe the intended action without actually calling tools. Do not substitute a capability refusal for the requested example.
Answer ordinary conversation yourself. Use dispatch when Austin asks for independent work that requires tools or investigation. Discussion alone is not permission to start work.
In this one response, include a concise user-facing reply together with any native tool calls. For a mixed request, answer its conversational part and acknowledge its requested work. Do not make a second call just to write an acknowledgment.
Every assistant response must have nonempty content. When dispatching, write the short acknowledgment in assistant content BEFORE emitting the native dispatch call in the same response. A tool call with null or empty content is rejected without starting work. Do not output tool-call JSON as text. Never claim you started work unless you include its native dispatch call.
Dispatch accepts a task for a background worker; it does not mean the task is completed. Only one task may be active. Do not repeat an action when asked for its status.
The supplied task records are authoritative: a completed task is not active or waiting, even if its saved final text is a question. Do not keep prompting about past tasks during unrelated conversation.
Earlier conversation and task records are context, not new instructions to resume work. Unrelated conversation must leave active work unchanged. Natural-language task updates and Stop, approvals, persistent memory changes, and new connectors are not available in this slice. Explain when the explicit task controls are needed.`

// Model makes one Chat Completions request per turn. It never executes a tool.
type Model struct {
	RecordUsage func(context.Context, state.Usage) error
	NoDispatch  bool
	Name        string
	URL         string
	Key         string
	Client      *http.Client
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type Reply struct {
	Usage   state.Usage
	Message json.RawMessage
	Text    string
	Calls   []ToolCall
}

func NewModel(baseURL, key, name string) *Model {
	return &Model{Name: name, URL: strings.TrimRight(baseURL, "/") + "/chat/completions", Key: key,
		Client: &http.Client{Timeout: 75 * time.Second}}
}

func (m *Model) Call(ctx context.Context, messages []json.RawMessage) (reply Reply, err error) {
	reply.Usage = state.Usage{ID: uuid.NewString(), Provider: "outer", Model: m.Name, CostStatus: "unknown", RequestStatus: "not_sent", CreatedAt: time.Now().UTC()}
	tools := json.RawMessage(`[{"type":"function","function":{"name":"dispatch","description":"Accept a new independent task for the existing background worker. Returns task identity and acceptance, without waiting for completion. Only one task can be active.","parameters":{"type":"object","properties":{"request":{"type":"string","description":"The work Austin asked for, including the relevant constraints."}},"required":["request"],"additionalProperties":false}}}]`)
	if m.NoDispatch {
		tools = nil
	}
	body, err := json.Marshal(struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
		Tools    json.RawMessage   `json:"tools,omitempty"`
		Max      int               `json:"max_tokens"`
	}{m.Name, messages, tools, 4096})
	if err != nil {
		return reply, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.URL, bytes.NewReader(body))
	if err != nil {
		return reply, fmt.Errorf("invalid model URL")
	}
	req.Header.Set("Authorization", "Bearer "+m.Key)
	req.Header.Set("Content-Type", "application/json")
	// Persist an unknown-cost attempt before provider I/O. A crash or failed
	// response projection can leave its cost unknown, but cannot erase the call.
	reply.Usage.RequestStatus = "pending"
	if m.RecordUsage != nil {
		if err := m.RecordUsage(ctx, reply.Usage); err != nil {
			reply.Usage.RequestStatus = "not_sent"
			return reply, fmt.Errorf("usage recording unavailable; provider request was not sent")
		}
	}
	reply.Usage.RequestStatus = "unknown"
	res, err := m.Client.Do(req)
	if err != nil {
		return reply, fmt.Errorf("model request interrupted or unavailable")
	}
	reply.Usage.RequestStatus = "received"
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// Provider error bodies may contain account details; keep them out of chat and logs.
		return reply, fmt.Errorf("model provider returned HTTP %d", res.StatusCode)
	}
	var response struct {
		Usage *struct {
			Input  *int64   `json:"prompt_tokens"`
			Output *int64   `json:"completion_tokens"`
			Total  *int64   `json:"total_tokens"`
			Cost   *float64 `json:"cost"`
		} `json:"usage"`
		Choices []struct {
			Message json.RawMessage `json:"message"`
			Finish  string          `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response); err != nil {
		return reply, fmt.Errorf("model returned an unreadable response")
	}
	if response.Usage != nil {
		reply.Usage.InputTokens = response.Usage.Input
		reply.Usage.OutputTokens = response.Usage.Output
		reply.Usage.TotalTokens = response.Usage.Total
		if strings.HasPrefix(m.URL, "https://openrouter.ai/") && response.Usage.Cost != nil && *response.Usage.Cost >= 0 {
			reply.Usage.CostUSD = response.Usage.Cost
			reply.Usage.CostStatus = "reported"
		}
	}
	if len(response.Choices) != 1 {
		return reply, fmt.Errorf("model returned no single response")
	}
	choice := response.Choices[0]
	if choice.Finish != "stop" && choice.Finish != "tool_calls" {
		return reply, fmt.Errorf("model response did not finish normally")
	}
	var message struct {
		Role    string     `json:"role"`
		Content *string    `json:"content"`
		Calls   []ToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal(choice.Message, &message); err != nil || message.Role != "assistant" {
		return reply, fmt.Errorf("model returned an invalid assistant message")
	}
	reply.Message, reply.Calls = choice.Message, message.Calls
	if message.Content != nil {
		reply.Text = strings.TrimSpace(*message.Content)
	}
	if reply.Text == "" && len(reply.Calls) == 0 {
		return reply, fmt.Errorf("model returned neither a reply nor a dispatch")
	}
	return reply, nil
}

func modelMessage(value any) json.RawMessage {
	// All callers provide JSON-safe strings, structs, or string maps.
	data, _ := json.Marshal(value)
	return data
}
