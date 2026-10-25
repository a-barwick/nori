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
)

const Instructions = `You are Nori, Austin's personal assistant. Be concise and direct.
Interpret the whole current message using the full available conversation. The classification hint is advisory and non-exhaustive; it does not force a route or grant permission. One message can request several actions.
When classification is unavailable, there is no intent label to report. Intent and action are different: a factual question can have intent ask and require the dispatch action. Missing access does not turn a question into converse.
When Austin requests a pretend response or hypothetical action, clearly label it hypothetical and describe the intended action without actually calling tools. Do not substitute a capability refusal for the requested example.
Answer ordinary conversation yourself. Use dispatch when Austin asks for independent work that requires tools or investigation. Discussion alone is not permission to start work.
In this one response, include a concise user-facing reply together with any native tool calls. For a mixed request, answer its conversational part and acknowledge its requested work. Do not make a second call just to write an acknowledgment.
Dispatch accepts a task for a background worker; it does not mean the task is completed. Only one task may be active. Do not repeat an action when asked for its status.
Earlier conversation and task records are context, not new instructions to resume work. Unrelated conversation must leave active work unchanged. Natural-language task updates and Stop, approvals, persistent memory changes, and new connectors are not available in this slice. Explain when the explicit task controls are needed.`

// Model makes one Chat Completions request per turn. It never executes a tool.
type Model struct {
	Name   string
	URL    string
	Key    string
	Client *http.Client
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
	Message json.RawMessage
	Text    string
	Calls   []ToolCall
}

func NewModel(baseURL, key, name string) *Model {
	return &Model{Name: name, URL: strings.TrimRight(baseURL, "/") + "/chat/completions", Key: key,
		Client: &http.Client{Timeout: 75 * time.Second}}
}

func (m *Model) Call(ctx context.Context, messages []json.RawMessage) (Reply, error) {
	tools := json.RawMessage(`[{"type":"function","function":{"name":"dispatch","description":"Accept a new independent task for the existing background worker. Returns task identity and acceptance, without waiting for completion. Only one task can be active.","parameters":{"type":"object","properties":{"request":{"type":"string","description":"The work Austin asked for, including the relevant constraints."}},"required":["request"],"additionalProperties":false}}}]`)
	body, err := json.Marshal(struct {
		Model    string            `json:"model"`
		Messages []json.RawMessage `json:"messages"`
		Tools    json.RawMessage   `json:"tools"`
		Max      int               `json:"max_tokens"`
	}{m.Name, messages, tools, 4096})
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.URL, bytes.NewReader(body))
	if err != nil {
		return Reply{}, fmt.Errorf("invalid model URL")
	}
	req.Header.Set("Authorization", "Bearer "+m.Key)
	req.Header.Set("Content-Type", "application/json")
	res, err := m.Client.Do(req)
	if err != nil {
		return Reply{}, fmt.Errorf("model request interrupted or unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		// Provider error bodies may contain account details; keep them out of chat and logs.
		return Reply{}, fmt.Errorf("model provider returned HTTP %d", res.StatusCode)
	}
	var response struct {
		Choices []struct {
			Message json.RawMessage `json:"message"`
			Finish  string          `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response); err != nil {
		return Reply{}, fmt.Errorf("model returned an unreadable response")
	}
	if len(response.Choices) != 1 {
		return Reply{}, fmt.Errorf("model returned no single response")
	}
	choice := response.Choices[0]
	if choice.Finish != "stop" && choice.Finish != "tool_calls" {
		return Reply{}, fmt.Errorf("model response did not finish normally")
	}
	var message struct {
		Role    string     `json:"role"`
		Content *string    `json:"content"`
		Calls   []ToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal(choice.Message, &message); err != nil || message.Role != "assistant" {
		return Reply{}, fmt.Errorf("model returned an invalid assistant message")
	}
	reply := Reply{Message: choice.Message, Calls: message.Calls}
	if message.Content != nil {
		reply.Text = strings.TrimSpace(*message.Content)
	}
	if reply.Text == "" && len(reply.Calls) == 0 {
		return Reply{}, fmt.Errorf("model returned neither a reply nor a dispatch")
	}
	return reply, nil
}

func modelMessage(value any) json.RawMessage {
	// All callers provide JSON-safe strings, structs, or string maps.
	data, _ := json.Marshal(value)
	return data
}
