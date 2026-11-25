package conversation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/contextview"
	"nori/internal/modelgateway"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/usage"
)

// The default outer model is GPT-6.1 Sol through OpenAI's platform API.
const (
	openAIChatURL   = "https://api.openai.com/v1/chat/completions"
	openAIProvider  = "openai"
	openAIModelName = "gpt-6.1-sol"
)

type OpenAIModel struct {
	name       string
	key        string
	gatewayURL string
	Client     *http.Client
	// TestURL replaces the provider endpoint with a local stub (settings.Models.TestURL).
	TestURL string
	// ReasoningEffort overrides the direct OpenAI reasoning_effort (defaults to "low").
	ReasoningEffort string
	// Optional determinism hooks. Nil defaults to time.Now and uuid.NewString.
	Now   func() time.Time
	NewID func() string
}

func (m *OpenAIModel) now() time.Time {
	if m != nil && m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *OpenAIModel) newID() string {
	if m != nil && m.NewID != nil {
		return m.NewID()
	}
	return uuid.NewString()
}

func NewOpenAIModel(key, name string) *OpenAIModel {
	return &OpenAIModel{name: name, key: key, Client: &http.Client{Timeout: 75 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func NewGatewayModel(baseURL, key, name string) *OpenAIModel {
	model := NewOpenAIModel(key, name)
	model.gatewayURL = strings.TrimRight(baseURL, "/") + "/chat/completions"
	return model
}

func (m *OpenAIModel) Name() string { return m.name }

func (m *OpenAIModel) provider() string {
	if m.gatewayURL != "" {
		return modelgateway.Provider
	}
	return openAIProvider
}

func (m *OpenAIModel) Respond(ctx context.Context, request ReplyRequest) (reply Reply, err error) {
	var requestStarted time.Time
	ctx, span := conversationTracer.Start(ctx, "chat "+m.name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("openinference.span.kind", "LLM"), attribute.String("session.id", observability.SessionID()),
		attribute.String("gen_ai.operation.name", "chat"), attribute.String("gen_ai.provider.name", m.provider()),
		attribute.String("gen_ai.request.model", m.name), attribute.String("llm.model_name", m.name),
	))
	promptBytes := 0
	for _, message := range request.Messages {
		promptBytes += len(message)
	}
	span.SetAttributes(
		attribute.Int("nori.prompt.message_count", len(request.Messages)), attribute.Int("nori.prompt.bytes", promptBytes),
		attribute.Bool("nori.input.truncated", len(request.TelemetryInput) > 2048),
		attribute.Bool("nori.command_plan", request.CommandPlan),
	)
	if a, ok := observability.ContentAttribute("input.value", request.TelemetryInput); ok {
		span.SetAttributes(a)
	}
	defer func() {
		m.observeCall(ctx, span, requestStarted, reply, err)
	}()
	reply.Usage = state.Usage{
		ID: m.newID(), Provider: "outer", Model: m.name, CostStatus: "unknown", RequestStatus: "not_sent", CreatedAt: m.now().UTC(),
	}
	body, err := m.requestBody(request, 4096, span)
	if err != nil {
		return reply, err
	}
	if err := contextview.Check(len(body)); err != nil {
		return reply, err
	}
	span.SetAttributes(attribute.Int("nori.prompt.bytes", len(body)))
	endpoint := openAIChatURL
	if m.gatewayURL != "" {
		endpoint = m.gatewayURL
	} else if m.TestURL != "" {
		endpoint = strings.TrimRight(m.TestURL, "/") + "/v1/chat/completions"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return reply, fmt.Errorf("invalid model request")
	}
	req.Header.Set("Authorization", "Bearer "+m.key)
	req.Header.Set("Content-Type", "application/json")
	// Persist an unknown-cost attempt before provider I/O. A crash or failed
	// response projection can leave its cost unknown, but cannot erase the call.
	reply.Usage.RequestStatus = "pending"
	if request.RecordUsage != nil {
		if err := request.RecordUsage(ctx, reply.Usage); err != nil {
			reply.Usage.RequestStatus = "not_sent"
			return reply, fmt.Errorf("usage recording unavailable; provider request was not sent")
		}
	}
	reply.Usage.RequestStatus = "unknown"
	requestStarted = m.now()
	client := m.Client
	res, err := client.Do(req)
	if err != nil {
		return reply, fmt.Errorf("model request interrupted or unavailable")
	}
	reply.Usage.RequestStatus = "received"
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		if m.gatewayURL != "" {
			if (res.StatusCode == 400 || res.StatusCode == 413) && contextview.Refusal(res.Body) {
				return reply, contextview.ErrOverflow
			}
			return reply, modelgateway.HTTPError(res.StatusCode)
		}
		if logRefusal(ctx, res) && (res.StatusCode == 400 || res.StatusCode == 413) {
			return reply, contextview.ErrOverflow
		}
		return reply, providerHTTPError{res.StatusCode, fmt.Sprintf("model provider returned HTTP %d", res.StatusCode)}
	}
	if request.Stream != nil {
		return m.decodeOpenAIStream(res, reply, request, span)
	}
	return m.decodeOpenAIReply(res, reply, request.NoDispatch, span)
}

// logRefusal records why the provider refused a request: its error type and code and its rate-limit headers. The
// error's message is left out because it can quote the account.
func logRefusal(ctx context.Context, res *http.Response) bool {
	var body struct {
		Error struct {
			Type string          `json:"type"`
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(res.Body, 16<<10))
	_ = json.Unmarshal(data, &body)
	h := res.Header
	slog.WarnContext(ctx, "outer model refused",
		"status", res.StatusCode,
		"error_type", body.Error.Type,
		"error_code", string(body.Error.Code),
		"retry_after", h.Get("Retry-After"),
		"limit_requests", h.Get("X-Ratelimit-Limit-Requests"),
		"remaining_requests", h.Get("X-Ratelimit-Remaining-Requests"),
		"reset_requests", h.Get("X-Ratelimit-Reset-Requests"),
		"limit_tokens", h.Get("X-Ratelimit-Limit-Tokens"),
		"remaining_tokens", h.Get("X-Ratelimit-Remaining-Tokens"),
		"reset_tokens", h.Get("X-Ratelimit-Reset-Tokens"),
	)
	return contextview.Refusal(bytes.NewReader(data))
}

func isSupportedDirectOpenAIModel(name string) bool {
	return name == openAIModelName || name == "gpt-6.2-sol"
}

func (m *OpenAIModel) requestBody(request ReplyRequest, outputLimit int, span trace.Span) ([]byte, error) {
	if m.gatewayURL == "" && !isSupportedDirectOpenAIModel(m.name) {
		return nil, errors.New("unsupported direct OpenAI conversation model")
	}
	var breakpoints []int
	if m.gatewayURL == "" {
		breakpoints = request.CacheBreakpoints
	}
	input, err := openAIConversation(request.Messages, breakpoints)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"model": m.name, "messages": input, "max_completion_tokens": outputLimit, "store": false,
	}
	if m.gatewayURL == "" {
		effort := m.ReasoningEffort
		if effort == "" {
			effort = "low"
		}
		body["reasoning_effort"], body["service_tier"] = effort, "default"
		body["prompt_cache_options"] = map[string]string{"mode": "explicit"}
		span.SetAttributes(attribute.String("gen_ai.request.reasoning_effort", effort), attribute.String("gen_ai.request.service_tier", "default"))
	}
	if request.Stream != nil {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if len(request.Tools) > 0 {
		body["response_format"] = foregroundFormat(request.Tools)
	}
	if !request.NoDispatch {
		// Direct reply calls use an ID-free schema: per-turn ID enums would change the cached prompt prefix on every new
		// memory or task. Their IDs are validated against the request instead (validateReplyIDs).
		stable := m.gatewayURL == "" && !request.CommandPlan
		body["response_format"] = responseFormat(request.TaskIDs, request.WorkRequests, request.MemoryIDs, request.CommandPlan, m.gatewayURL != "", stable)
	}
	return json.Marshal(body)
}

// decodeOpenAIReply preserves observed usage even when the provider response is unusable.
func (m *OpenAIModel) decodeOpenAIReply(res *http.Response, reply Reply, noDispatch bool, span trace.Span) (Reply, error) {
	var err error
	var response struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Tier  string `json:"service_tier"`
		Usage *struct {
			Input        *int64          `json:"prompt_tokens"`
			Output       *int64          `json:"completion_tokens"`
			Total        *int64          `json:"total_tokens"`
			Cost         json.RawMessage `json:"cost"`
			InputDetails struct {
				Cached int64 `json:"cached_tokens"`
				Write  int64 `json:"cache_write_tokens"`
			} `json:"prompt_tokens_details"`
			OutputDetails struct {
				Reasoning int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
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
		cached, written := response.Usage.InputDetails.Cached, response.Usage.InputDetails.Write
		reply.Usage.CachedTokens, reply.Usage.CacheWriteTokens = &cached, &written
		if m.gatewayURL != "" {
			var cost *float64
			if json.Unmarshal(response.Usage.Cost, &cost) == nil {
				usage.RecordReportedCost(&reply.Usage, cost)
			}
		} else {
			cost, costErr := usage.SolCostWithWrites(
				response.Model, response.Tier, response.Usage.Input, response.Usage.Output, response.Usage.Total, cached, written,
			)
			if costErr == nil {
				reply.Usage.CostUSD, reply.Usage.CostStatus = &cost, "calculated"
			}
		}
		span.SetAttributes(
			attribute.Int64("gen_ai.usage.reasoning.output_tokens", response.Usage.OutputDetails.Reasoning),
			attribute.Int64("gen_ai.usage.cache_read.input_tokens", cached),
			attribute.Int64("gen_ai.usage.cache_write.input_tokens", written),
		)
	}
	if len(response.Choices) != 1 {
		return reply, fmt.Errorf("model returned no single response")
	}
	choice := response.Choices[0]
	if choice.Finish != "stop" && choice.Finish != "tool_calls" {
		// Retain the provider request identity without exposing error bodies,
		// which can contain account details or prompt content.
		reason := "unknown"
		switch choice.Finish {
		case "error", "length", "content_filter", "function_call", "cancelled":
			reason = choice.Finish
		}
		if m.gatewayURL != "" {
			return reply, fmt.Errorf("model gateway did not finish the Chat Completions response normally (finish_reason=%s)", reason)
		}
		id := "unavailable"
		if response.ID != "" && len(response.ID) <= 128 &&
			strings.Trim(response.ID, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == "" {
			id = response.ID
		}
		return reply, fmt.Errorf("model response did not finish normally (finish_reason=%s, request_id=%s)", reason, id)
	}
	var message struct {
		Role    string     `json:"role"`
		Content *string    `json:"content"`
		Calls   []ToolCall `json:"tool_calls"`
	}
	if err := json.Unmarshal(choice.Message, &message); err != nil || message.Role != "assistant" {
		return reply, fmt.Errorf("model returned an invalid assistant message")
	}
	if m.gatewayURL != "" && (choice.Finish != "stop" || len(message.Calls) != 0) {
		return reply, errors.New("model gateway must return Chat Completions text or a JSON schema response without native tool calls")
	}
	reply.Message, reply.Calls = choice.Message, message.Calls
	if message.Content != nil {
		reply.Text = strings.TrimSpace(*message.Content)
	}
	if !noDispatch {
		if len(reply.Calls) != 0 {
			return reply, errors.New("model returned native calls instead of the structured routing response")
		}
		reply.Structured = true
		reply.Text, reply.Calls, err = decodeResponse(reply.Text)
		return reply, err
	}
	if reply.Text == "" && len(reply.Calls) == 0 {
		return reply, fmt.Errorf("model returned neither a reply nor a task command")
	}
	return reply, nil
}

type providerHTTPError struct {
	status int
	detail string
}

func (e providerHTTPError) Error() string       { return e.detail }
func (e providerHTTPError) HTTPStatusCode() int { return e.status }

func (m *OpenAIModel) observeCall(ctx context.Context, span trace.Span, requestStarted time.Time, reply Reply, err error) {
	provider := attribute.String("gen_ai.provider.name", m.provider())
	model := attribute.String("gen_ai.request.model", m.name)
	span.SetAttributes(
		attribute.Bool("nori.output.present", reply.Text != ""), attribute.Int("nori.tool.call_count", len(reply.Calls)),
		attribute.String("nori.cost.status", reply.Usage.CostStatus),
	)
	if reply.Usage.InputTokens != nil {
		span.SetAttributes(
			attribute.Int64("gen_ai.usage.input_tokens", *reply.Usage.InputTokens), attribute.Int64("llm.token_count.prompt", *reply.Usage.InputTokens),
		)
		observability.Metric(ctx, "nori.model.input_tokens", float64(*reply.Usage.InputTokens))
	}
	if reply.Usage.OutputTokens != nil {
		span.SetAttributes(
			attribute.Int64("gen_ai.usage.output_tokens", *reply.Usage.OutputTokens), attribute.Int64("llm.token_count.completion", *reply.Usage.OutputTokens),
		)
		observability.Metric(ctx, "nori.model.output_tokens", float64(*reply.Usage.OutputTokens))
	}
	if reply.Usage.TotalTokens != nil {
		span.SetAttributes(attribute.Int64("llm.token_count.total", *reply.Usage.TotalTokens))
	}
	if reply.Usage.CostUSD != nil {
		span.SetAttributes(attribute.Float64("nori.cost.usd", *reply.Usage.CostUSD), attribute.Float64("llm.cost.total", *reply.Usage.CostUSD))
	}
	if a, ok := observability.ContentAttribute("output.value", reply.Text); ok {
		span.SetAttributes(a)
	}
	if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		observability.RecordErrorCategory(ctx, "model.outer", err)
		if !requestStarted.IsZero() {
			observability.Event(ctx, "model.call.failed", provider, model)
		}
	} else if err == nil && !requestStarted.IsZero() {
		observability.Event(ctx, "model.call.completed", provider, model)
	}
	if !requestStarted.IsZero() && !errors.Is(ctx.Err(), context.Canceled) {
		observability.Metric(ctx, "nori.model.duration_ms", float64(m.now().Sub(requestStarted).Milliseconds()))
	}
	span.End()
}

// openAIConversation sends only text and historical command facts to the structured-output model, without altering
// the stored originals. Retained attempts can contain another provider's opaque reasoning and native tool messages.
// openAIConversation renders saved messages for Chat Completions. A message whose index is in breakpoints is sent as
// one text part carrying an explicit prompt_cache_breakpoint, marking the end of a reusable prefix.
func openAIConversation(messages []json.RawMessage, breakpoints []int) ([]map[string]any, error) {
	marked := make(map[int]bool, len(breakpoints))
	for _, i := range breakpoints {
		marked[i] = true
	}
	input := make([]map[string]any, 0, len(messages))
	for i, raw := range messages {
		var message struct {
			Role    string          `json:"role"`
			Content *string         `json:"content"`
			Calls   json.RawMessage `json:"tool_calls"`
		}
		if json.Unmarshal(raw, &message) != nil {
			return nil, fmt.Errorf("invalid saved conversation message")
		}
		text := ""
		if message.Content != nil {
			text = *message.Content
		}
		switch message.Role {
		case "system", "user", "assistant":
		case "tool":
			message.Role = "system"
			text = prompts.Runtime("saved-command-history", nil) + text
		default:
			return nil, fmt.Errorf("unsupported saved conversation role")
		}
		if len(message.Calls) > 0 && string(message.Calls) != "null" && string(message.Calls) != "[]" {
			text += prompts.Runtime("historical-command-proposal", nil) + string(message.Calls)
		}
		if marked[i] && text != "" {
			input = append(input, map[string]any{"role": message.Role, "content": []map[string]any{{
				"type": "text", "text": text, "prompt_cache_breakpoint": map[string]string{"mode": "explicit"},
			}}})
			continue
		}
		input = append(input, map[string]any{"role": message.Role, "content": text})
	}
	return input, nil
}

// decodeOpenAIStream uses the same response validator and token pricing as the
// ordinary adapter. A truncated stream retains partial text and unknown cost.
func (m *OpenAIModel) decodeOpenAIStream(res *http.Response, reply Reply, request ReplyRequest, span trace.Span) (Reply, error) {
	scanner := bufio.NewScanner(io.LimitReader(res.Body, 2<<20))
	scanner.Buffer(make([]byte, 4096), 256<<10)
	text := ""
	finish := ""
	model := ""
	tier := ""
	id := ""
	var usage json.RawMessage
	calls := map[int]*ToolCall{}
	done := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var chunk struct {
			ID      string          `json:"id"`
			Model   string          `json:"model"`
			Tier    string          `json:"service_tier"`
			Usage   json.RawMessage `json:"usage"`
			Choices []struct {
				Finish *string `json:"finish_reason"`
				Delta  struct {
					Content string `json:"content"`
					Calls   []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return reply, errors.New("model returned an unreadable stream")
		}
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Tier != "" {
			tier = chunk.Tier
		}
		if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
			usage = chunk.Usage
		}
		if len(chunk.Choices) > 1 {
			return reply, errors.New("model returned multiple streamed choices")
		}
		for _, choice := range chunk.Choices {
			if choice.Finish != nil {
				finish = *choice.Finish
			}
			if choice.Delta.Content != "" {
				text += choice.Delta.Content
				reply.Text = text
				if len(text) > 16384 {
					return reply, errors.New("model reply exceeded limit")
				}
				visible := text
				if request.Foreground {
					visible = foregroundPartial(text)
				}
				if err := request.Stream(visible); err != nil {
					return reply, err
				}
			}
			for _, delta := range choice.Delta.Calls {
				if delta.Index < 0 || delta.Index > 15 {
					return reply, errors.New("too many tool calls")
				}
				call := calls[delta.Index]
				if call == nil {
					call = &ToolCall{}
					calls[delta.Index] = call
				}
				call.ID += delta.ID
				call.Type += delta.Type
				call.Function.Name += delta.Function.Name
				call.Function.Arguments += delta.Function.Arguments
				if len(call.Function.Arguments) > 16384 {
					return reply, errors.New("tool arguments exceeded limit")
				}
			}
		}
	}
	if scanner.Err() != nil || !done {
		return reply, errors.New("model stream interrupted; request will not be repeated")
	}
	ordered := []ToolCall{}
	for i := 0; i < len(calls); i++ {
		call := calls[i]
		if call == nil {
			return reply, errors.New("invalid tool sequence")
		}
		ordered = append(ordered, *call)
	}
	body, _ := json.Marshal(map[string]any{"id": id, "model": model, "service_tier": tier, "usage": usage, "choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{"role": "assistant", "content": text, "tool_calls": ordered}}}})
	copyResponse := *res
	copyResponse.Body = io.NopCloser(bytes.NewReader(body))
	decoded, err := m.decodeOpenAIReply(&copyResponse, reply, true, span)
	if err == nil && request.Foreground {
		decoded.Text, decoded.Calls, err = decodeForeground(decoded.Text)
	}
	return decoded, err
}

func foregroundFormat(tools json.RawMessage) any {
	var definitions []struct {
		Function struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Parameters  map[string]any `json:"parameters"`
		} `json:"function"`
	}
	_ = json.Unmarshal(tools, &definitions)
	variants := []any{}
	for _, tool := range definitions {
		variants = append(variants, actionVariant(tool.Function.Name, tool.Function.Description, tool.Function.Parameters))
	}
	return map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "chat_response", "strict": true, "schema": map[string]any{"type": "object", "properties": map[string]any{"actions": map[string]any{"type": "array", "maxItems": 1, "items": map[string]any{"anyOf": variants}}, "text": map[string]any{"type": "string", "maxLength": 16384}}, "required": []string{"actions", "text"}, "additionalProperties": false}}}
}
func decodeForeground(text string) (string, []ToolCall, error) {
	var response struct {
		Text    string `json:"text"`
		Actions []struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"actions"`
	}
	if json.Unmarshal([]byte(text), &response) != nil || len(response.Actions) > 1 {
		return "", nil, errors.New("invalid foreground response")
	}
	calls := []ToolCall{}
	for i, a := range response.Actions {
		call := ToolCall{ID: fmt.Sprintf("action_%d", i), Type: "function"}
		call.Function.Name = a.Name
		call.Function.Arguments = string(a.Arguments)
		calls = append(calls, call)
	}
	if response.Text == "" && len(calls) == 0 {
		return "", nil, errors.New("empty foreground response")
	}
	return response.Text, calls, nil
}

// Extract only a completed JSON string prefix, never the action arguments.
func foregroundPartial(text string) string {
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '{' || ch == '[' {
			depth++
		}
		if ch == '}' || ch == ']' {
			depth--
		}
		if ch != '"' {
			continue
		}
		if depth == 1 && strings.HasPrefix(text[i:], `"text"`) {
			rest := strings.TrimSpace(text[i+6:])
			if !strings.HasPrefix(rest, ":") {
				return ""
			}
			rest = strings.TrimSpace(rest[1:])
			if !strings.HasPrefix(rest, `"`) {
				return ""
			}
			for end := len(rest); end > 1; end-- {
				candidate := rest[:end]
				var value string
				if json.Unmarshal([]byte(candidate), &value) == nil {
					return value
				}
				if json.Unmarshal([]byte(candidate+`"`), &value) == nil {
					return value
				}
			}
			return ""
		}
		inString = true
	}
	return ""
}
