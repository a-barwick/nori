package responses

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"nori/internal/agent"
	"nori/internal/contextview"
	"nori/internal/llmguard"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

const googleChatBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
const googleStateFormat = "nori.google.chat"

type GoogleClient struct {
	*Client
	baseURL string
}

func NewGoogle(baseURL, key, model string, meter usage.Meter) *GoogleClient {
	if baseURL == "" {
		baseURL = googleChatBaseURL
	}
	client := New(key, model, meter)
	client.ProviderName = "google"
	client.BYOKProviderLabel = "Google"
	client.BillingMode = state.UsageBillingBYOK
	return &GoogleClient{Client: client, baseURL: strings.TrimRight(baseURL, "/")}
}

type googleResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Finish  string          `json:"finish_reason"`
		Message json.RawMessage `json:"message"`
	} `json:"choices"`
	Usage *struct {
		Input        *int64   `json:"prompt_tokens"`
		Output       *int64   `json:"completion_tokens"`
		Total        *int64   `json:"total_tokens"`
		Cost         *float64 `json:"cost"`
		InputDetails struct {
			Cached int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		OutputDetails struct {
			Reasoning int64 `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func (c *GoogleClient) complete(ctx context.Context, fields map[string]any, maxTokens int) (response googleResponse, sent bool, cost float64, costKnown bool, err error) {
	body, err := json.Marshal(fields)
	if err != nil {
		return response, false, 0, false, errors.New("task request could not be encoded")
	}
	if err := contextview.Check(len(body)); err != nil {
		return response, false, 0, false, err
	}
	scope := llmguard.MetaFromContext(ctx).Scope
	if scope == "" {
		scope = llmguard.ScopeTask
	}
	billingMode := c.billingMode()
	meta := llmguard.CallMeta{
		UserID: c.UserID, Scope: scope, Provider: "google", Model: c.ModelName,
		Billing: billingMode, MaxOutputTokens: maxTokens, BYOK: billingMode == state.UsageBillingBYOK,
	}
	reqCtx := llmguard.WithMeta(ctx, meta)
	buildReq := func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Key)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}
	record := usage.AttemptWithMeta(ctx, "google", c.ModelName, billingMode, state.UsageCategoryLLM)
	if billingMode != state.UsageBillingLocal {
		record.EstimatedExposureUSD = llmguard.EstimateCallUSD(meta, len(body))
	}
	if err := c.Meter.RecordUsage(ctx, record); err != nil {
		return response, false, 0, false, err
	}
	record.RequestStatus = "unknown"
	defer func() {
		llmguard.ObserveCallCost(ctx, cost, costKnown, sent)
		err = errors.Join(err, usage.Finish(ctx, c.Meter, record))
	}()
	onRetry := func(status int) error {
		previous := record
		if status > 0 {
			previous.RequestStatus = "received"
		}
		_ = usage.Finish(ctx, c.Meter, previous)
		record = usage.AttemptWithMeta(ctx, "google", c.ModelName, billingMode, state.UsageCategoryLLM)
		if billingMode != state.UsageBillingLocal {
			record.EstimatedExposureUSD = llmguard.EstimateCallUSD(meta, len(body))
		}
		if err := c.Meter.RecordUsage(ctx, record); err != nil {
			record.RequestStatus = "not_sent"
			return err
		}
		record.RequestStatus = "unknown"
		return nil
	}
	res, err := c.doWithRetry(reqCtx, buildReq, onRetry)
	if err != nil {
		if llmguard.IsGuardError(err) || errors.Is(err, usage.ErrUserQuotaExceeded) {
			record.RequestStatus = "not_sent"
			return response, false, 0, false, err
		}
		if ctx.Err() != nil {
			return response, true, 0, false, ctx.Err()
		}
		return response, true, 0, false, errors.New("Google model request interrupted or unavailable")
	}
	defer res.Body.Close()
	record.RequestStatus = "received"
	if res.StatusCode != http.StatusOK {
		if c.BYOKProviderLabel != "" {
			if accessErr := llmguard.ModelAccessError(res, c.BYOKProviderLabel); accessErr != nil {
				return response, true, 0, false, accessErr
			}
		}
		if (res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusRequestEntityTooLarge) && contextview.Refusal(res.Body) {
			return response, true, 0, false, contextview.ErrOverflow
		}
		return response, true, 0, false, fmt.Errorf("Google model returned HTTP %d", res.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response) != nil {
		return response, true, 0, false, errors.New("Google model returned an unreadable response")
	}
	if response.Usage != nil {
		record.InputTokens = response.Usage.Input
		record.OutputTokens = response.Usage.Output
		record.TotalTokens = response.Usage.Total
		cached := response.Usage.InputDetails.Cached
		record.CachedTokens = &cached
		if usage.RecordReportedCost(&record, response.Usage.Cost) {
			cost, costKnown = *response.Usage.Cost, true
		} else if response.Usage.Input != nil && response.Usage.Output != nil {
			reasoning := response.Usage.OutputDetails.Reasoning
			separateReasoning := response.Usage.Total != nil && reasoning > 0 &&
				*response.Usage.Total == *response.Usage.Input+*response.Usage.Output+reasoning
			for _, candidate := range []string{strings.TrimSpace(response.Model), strings.TrimSpace(c.ModelName)} {
				if candidate == "" {
					continue
				}
				var calc float64
				var calcErr error
				if separateReasoning {
					calc, calcErr = usage.GoogleCostWithReasoning(candidate, *response.Usage.Input, *response.Usage.Output, reasoning, cached)
				} else {
					calc, calcErr = usage.GoogleCost(candidate, *response.Usage.Input, *response.Usage.Output, cached)
				}
				if calcErr == nil {
					record.CostUSD, record.CostStatus = &calc, "calculated"
					cost, costKnown = calc, true
					break
				}
			}
		}
		if costKnown {
			llmguard.RecordResponseCost(res, cost)
		}
	}
	return response, true, cost, costKnown, nil
}

func (c *GoogleClient) Respond(ctx context.Context, req agent.Request) (out agent.Response, err error) {
	out.NotSent = true
	messages, err := googleMessages(req.Instructions, req.Messages)
	if err != nil {
		return out, err
	}
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	fields := map[string]any{"model": c.ModelName, "messages": messages, "max_tokens": maxTokens}
	effort := req.Reasoning
	if effort == "" {
		effort = c.DefaultReasoning
	}
	if effort != "" {
		fields["reasoning_effort"] = effort
	}
	tools := make([]any, 0, len(req.Tools))
	for _, tool := range req.Tools {
		schema := tool.Schema
		if schema == nil {
			schema = responseSchema(tool.Parameters)
		}
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": schema}})
	}
	if len(tools) > 0 {
		fields["tools"] = tools
	}
	response, sent, cost, costKnown, err := c.complete(ctx, fields, maxTokens)
	out.NotSent = !sent
	out.Cost, out.CostKnown = cost, costKnown
	if err != nil {
		return out, err
	}
	if len(response.Choices) != 1 || (response.Choices[0].Finish != "stop" && response.Choices[0].Finish != "tool_calls") {
		return out, errors.New("Google model response incomplete; no tools executed")
	}
	choice := response.Choices[0]
	var message struct {
		Role    string  `json:"role"`
		Content *string `json:"content"`
		Calls   []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(choice.Message, &message) != nil || message.Role != "assistant" {
		return out, errors.New("Google model returned an invalid assistant message")
	}
	saved := &transcript.Message{Role: transcript.RoleModel, Parts: []transcript.Part{{Provider: &transcript.ProviderState{Format: googleStateFormat, Data: choice.Message}}}}
	for _, call := range message.Calls {
		known := false
		for _, tool := range req.Tools {
			if tool.Name == call.Function.Name {
				known = true
				break
			}
		}
		parsed := transcript.ToolCall{ID: call.ID, Name: call.Function.Name}
		if !known || call.Type != "function" || parsed.ID == "" || json.Unmarshal([]byte(call.Function.Arguments), &parsed.Args) != nil || parsed.Args == nil {
			return out, errors.New("Google model returned an invalid tool call")
		}
		out.Calls = append(out.Calls, parsed)
		saved.Parts = append(saved.Parts, transcript.Part{ToolCall: &parsed})
	}
	if message.Content != nil && strings.TrimSpace(*message.Content) != "" {
		text := strings.TrimSpace(*message.Content)
		saved.Parts = append(saved.Parts, transcript.Part{Text: text})
		if len(out.Calls) == 0 {
			out.Answer = text
		}
	}
	if len(out.Calls) == 0 && out.Answer == "" {
		return out, errors.New("Google model returned no answer")
	}
	if response.Usage != nil && response.Usage.Input != nil && response.Usage.Output != nil && response.Usage.Total != nil {
		reasoning := response.Usage.OutputDetails.Reasoning
		out.Usage = &agent.Usage{InputTokens: int32(*response.Usage.Input), OutputTokens: int32(max(*response.Usage.Output-reasoning, 0)), ReasoningTokens: int32(reasoning), TotalTokens: int32(*response.Usage.Total)}
	}
	out.Message = saved
	return out, nil
}

func googleMessages(instructions string, messages []transcript.Message) ([]any, error) {
	out := []any{}
	if strings.TrimSpace(instructions) != "" {
		out = append(out, map[string]any{"role": "system", "content": instructions})
	}
	pending := map[string][]string{}
	for i, message := range messages {
		if saved := message.Provider(); saved != nil && saved.Format == googleStateFormat {
			var prior struct {
				Role  string `json:"role"`
				Calls []struct {
					ID       string `json:"id"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			}
			if json.Unmarshal(saved.Data, &prior) != nil || prior.Role != "assistant" {
				return nil, errors.New("invalid saved Google response")
			}
			for _, call := range prior.Calls {
				pending[call.Function.Name] = append(pending[call.Function.Name], call.ID)
			}
			out = append(out, json.RawMessage(saved.Data))
			continue
		}
		role := string(message.Role)
		if message.Role == transcript.RoleModel {
			role = "assistant"
		}
		var calls []any
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
				calls = append(calls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": call.Name, "arguments": string(args)}})
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
				encoded, err := json.Marshal(result.Output)
				if err != nil {
					return nil, err
				}
				out = append(out, map[string]any{"role": "tool", "tool_call_id": id, "content": string(encoded)})
			case part.Text != "":
				out = append(out, map[string]any{"role": role, "content": part.Text})
			}
		}
		if len(calls) > 0 {
			out = append(out, map[string]any{"role": "assistant", "tool_calls": calls})
		}
	}
	return out, nil
}

func (c *GoogleClient) Inspect(ctx context.Context, req agent.InspectRequest) (agent.Inspection, error) {
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	image := "data:" + req.MediaType + ";base64," + base64.StdEncoding.EncodeToString(req.Image)
	messages := []any{}
	if req.Instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.Instructions})
	}
	messages = append(messages, map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": req.Question}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": image}}}})
	fields := map[string]any{"model": c.ModelName, "messages": messages, "max_tokens": maxTokens}
	if c.DefaultReasoning != "" {
		fields["reasoning_effort"] = c.DefaultReasoning
	}
	response, sent, cost, costKnown, err := c.complete(ctx, fields, maxTokens)
	out := agent.Inspection{Sent: sent, Cost: cost, CostKnown: costKnown}
	if err != nil {
		return out, err
	}
	if len(response.Choices) != 1 || response.Choices[0].Finish != "stop" {
		return out, errors.New("Google inspection response incomplete")
	}
	var message struct {
		Content *string `json:"content"`
	}
	if json.Unmarshal(response.Choices[0].Message, &message) != nil || message.Content == nil {
		return out, errors.New("Google inspection response was unreadable")
	}
	out.Observation = strings.TrimSpace(*message.Content)
	return out, nil
}
