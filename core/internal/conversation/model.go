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
Answer conversation and questions yourself when the available context is sufficient.
When Austin asks for independent work, call dispatch with a clear request.
Dispatch only saves a request for later execution. No worker is connected yet. Never claim a dispatched request is running or completed.
Do not treat a discussion about possible work as permission to dispatch it.`

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
	tools := json.RawMessage(`[{"type":"function","function":{"name":"dispatch","description":"Save an independent work request for later execution. No worker executes it in this runner.","parameters":{"type":"object","properties":{"request":{"type":"string","description":"The work Austin asked for, including the relevant constraints."}},"required":["request"],"additionalProperties":false}}}]`)
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
