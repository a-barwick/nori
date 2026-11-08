package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"nori/internal/agent"
	"nori/internal/contextview"
	"nori/internal/transcript"
	"nori/internal/usage"
)

const (
	// selectedModel is the only model authorized for inner tasks.
	routerModel = "openai/gpt-6.1-terra"
	directModel = "gpt-6.1-terra"
	// stateFormat marks transcript.ProviderState written by this adapter. Its data is the
	// JSON array of output items from a response, which keeps encrypted reasoning and call
	// IDs exactly as the provider returned them.
	stateFormat = "nori.responses"
)

type Client struct {
	Key       string
	ModelName string
	HTTP      *http.Client
	Meter     usage.Meter
}

func New(key, model string, meter usage.Meter) *Client {
	return &Client{
		Key:       key,
		Meter:     meter,
		ModelName: model,
		HTTP: &http.Client{
			Timeout:       2 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c *Client) direct() bool { return c.ModelName == directModel }

func (c *Client) Provider() string {
	if c.direct() {
		return "openai"
	}
	return "openrouter"
}

func (c *Client) Name() string { return c.ModelName }

func (c *Client) endpoint() string {
	if c.direct() {
		return "https://api.openai.com/v1/responses"
	}
	return "https://openrouter.ai/api/v1/responses"
}

func (c *Client) format() string {
	if c.direct() {
		return "nori.openai.responses"
	}
	return stateFormat
}

func (c *Client) requestOptions(fields map[string]any) {
	if c.direct() {
		fields["service_tier"] = "default"
		fields["prompt_cache_options"] = map[string]string{"mode": "explicit"}
		return
	}
	fields["provider"] = map[string]any{"max_price": map[string]float64{"prompt": 1, "completion": 5}, "require_parameters": true}
}

func (c *Client) validModel() bool { return c.direct() || c.ModelName == routerModel }

func (c *Client) Respond(ctx context.Context, req agent.Request) (out agent.Response, err error) {
	out.NotSent = true
	if !c.validModel() {
		return out, errors.New("task model must be the selected GPT-6.1 Terra")
	}
	input, err := responseInput(req.Messages, c.format())
	if err != nil {
		return out, err
	}
	tools := []any{}
	for _, tool := range req.Tools {
		parameters := responseSchema(tool.Parameters)
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
		}
		tools = append(tools, map[string]any{
			"type": "function", "name": tool.Name, "description": tool.Description, "strict": true, "parameters": parameters,
		})
	}
	fields := map[string]any{
		"model": c.ModelName, "input": input, "instructions": req.Instructions,
		"tools": tools, "max_output_tokens": req.MaxOutputTokens, "store": false,
		"include": []string{"reasoning.encrypted_content"},
	}
	if req.Reasoning != "" {
		fields["reasoning"] = map[string]string{"effort": req.Reasoning}
	}
	c.requestOptions(fields)
	body, err := json.Marshal(fields)
	if err != nil {
		return out, errors.New("task request could not be encoded")
	}
	if err := contextview.Check(len(body)); err != nil {
		return out, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.Key)
	httpReq.Header.Set("Content-Type", "application/json")
	record := usage.Attempt(ctx, c.Provider(), c.ModelName)
	if err := c.Meter.RecordUsage(ctx, record); err != nil {
		return out, err
	}
	record.RequestStatus = "unknown"
	defer func() { err = errors.Join(err, usage.Finish(ctx, c.Meter, record)) }()
	// Once handed to HTTP, a transport failure cannot prove it was not billed.
	out.NotSent = false
	res, err := c.HTTP.Do(httpReq)
	if err != nil {
		return out, errors.New("task model request interrupted or unavailable")
	}
	record.RequestStatus = "received"
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if (res.StatusCode == 400 || res.StatusCode == 413) && contextview.Refusal(res.Body) {
			return out, contextview.ErrOverflow
		}
		return out, fmt.Errorf("task model returned HTTP %d", res.StatusCode)
	}
	var response struct {
		Status string            `json:"status"`
		Output []json.RawMessage `json:"output"`
		Model  string            `json:"model"`
		Tier   string            `json:"service_tier"`
		Usage  *responseUsage    `json:"usage"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response) != nil {
		return out, errors.New("task model returned an unreadable response")
	}
	out.Cost, out.CostKnown = c.recordCost(&record, response.Model, response.Tier, response.Usage)
	if u := response.Usage; u != nil && u.Input != nil && u.Output != nil && u.Total != nil {
		out.Usage = &agent.Usage{
			InputTokens: int32(*u.Input), OutputTokens: int32(*u.Output - u.OutputDetails.Reasoning),
			ReasoningTokens: int32(u.OutputDetails.Reasoning), CachedTokens: int32(u.InputDetails.Cached), TotalTokens: int32(*u.Total),
		}
	}
	if response.Status != "completed" {
		return out, errors.New("task model response incomplete; no tools executed")
	}
	saved, err := json.Marshal(response.Output)
	if err != nil {
		return out, err
	}
	message := &transcript.Message{
		Role:  transcript.RoleModel,
		Parts: []transcript.Part{{Provider: &transcript.ProviderState{Format: c.format(), Data: saved}}},
	}
	out.Message = message
	calls := []transcript.ToolCall{}
	text := []string{}
	for _, raw := range response.Output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			return out, errors.New("invalid task response item")
		}
		switch item.Type {
		case "function_call":
			call := transcript.ToolCall{ID: item.CallID, Name: item.Name}
			if call.ID == "" || json.Unmarshal([]byte(item.Arguments), &call.Args) != nil || call.Args == nil {
				return out, errors.New("invalid task tool call")
			}
			calls = append(calls, call)
			message.Parts = append(message.Parts, transcript.Part{ToolCall: &call})
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" {
					text = append(text, content.Text)
				}
			}
		}
	}
	visibleText := strings.TrimSpace(strings.Join(text, "\n"))
	if len(calls) == 0 {
		out.Answer = visibleText
	}
	if len(calls) == 0 && out.Answer == "" {
		return out, errors.New("task model returned no answer")
	}
	out.Calls = calls
	// Preserve user-facing text that accompanies a control call. The exact
	// provider items remain authoritative for replay; this projection lets an
	// input question retain its explanation without exposing reasoning.
	if visibleText != "" {
		message.Parts = append(message.Parts, transcript.Part{Text: visibleText})
	}
	return out, nil
}

// responseInput builds the Responses input items from a transcript. A model turn that
// carries this adapter's provider state is replayed from it exactly. Everything else,
// including turns saved by an earlier provider, is rebuilt from its portable parts.
func responseInput(messages []transcript.Message, format string) ([]any, error) {
	input := []any{}
	pending := map[string][]string{}
	for i, message := range messages {
		if state := message.Provider(); state != nil && state.Format == format {
			var items []json.RawMessage
			if json.Unmarshal(state.Data, &items) != nil {
				return nil, errors.New("invalid saved model response")
			}
			for _, item := range items {
				input = append(input, item)
			}
			continue
		}
		for j, part := range message.Parts {
			switch {
			case part.ToolCall != nil:
				call := part.ToolCall
				id := call.ID
				if id == "" {
					id = fmt.Sprintf("legacy_call_%d_%d", i, j)
				}
				pending[call.Name] = append(pending[call.Name], id)
				args, err := json.Marshal(call.Args)
				if err != nil {
					return nil, err
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": id, "name": call.Name, "arguments": string(args)})
			case part.ToolResult != nil:
				result := part.ToolResult
				id := result.CallID
				if ids := pending[result.Name]; len(ids) > 0 {
					if id == "" {
						id = ids[0]
					}
					pending[result.Name] = ids[1:]
				}
				if id == "" {
					return nil, errors.New("saved tool response has no call identity")
				}
				output, err := json.Marshal(result.Output)
				if err != nil {
					return nil, err
				}
				input = append(input, map[string]any{"type": "function_call_output", "call_id": id, "output": string(output)})
			case part.Text != "":
				role := string(message.Role)
				if message.Role == transcript.RoleModel {
					role = "assistant"
				}
				input = append(input, map[string]any{"role": role, "content": part.Text})
			}
		}
	}
	return input, nil
}

func responseSchema(schema *transcript.Schema) map[string]any {
	if schema == nil {
		return nil
	}
	result := map[string]any{"type": string(schema.Type)}
	if schema.Description != "" {
		result["description"] = schema.Description
	}
	if len(schema.Enum) > 0 {
		result["enum"] = schema.Enum
	}
	if schema.Type == transcript.TypeObject {
		properties := map[string]any{}
		for name, child := range schema.Properties {
			properties[name] = responseSchema(child)
		}
		required := schema.Required
		if required == nil {
			required = []string{}
		}
		result["properties"], result["required"], result["additionalProperties"] = properties, required, false
	}
	if schema.Type == transcript.TypeArray {
		result["items"] = responseSchema(schema.Items)
	}
	return result
}
