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
	"nori/internal/homeai"
	"nori/internal/llmguard"
	"nori/internal/modelgateway"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/usage"
)

const (
	openAIChatURL     = "https://api.openai.com/v1/chat/completions"
	openRouterChatURL = "https://openrouter.ai/api/v1/chat/completions"
	openAIProvider    = "openai"
	openAIModelName   = "gpt-6.1-sol"
)

type OpenAIModel struct {
	ProviderName          string
	name                  string
	key                   string
	gatewayURL            string
	openRouter            bool
	Client                *http.Client
	Guard                 *llmguard.Guard
	UserID                string
	TestURL               string
	ReasoningEffort       string
	AllowGatewayReasoning bool
	GoogleCompatibility   bool
	BYOKProviderLabel     string
	LocalOnly             bool
	Now                   func() time.Time
	NewID                 func() string
	MaxRetries            int
	RetryBaseDelay        time.Duration
	MaxRetryAfter         time.Duration
	Sleep                 func(context.Context, time.Duration) error
}

func (m *OpenAIModel) guard() *llmguard.Guard {
	if m != nil && m.Guard != nil {
		return m.Guard
	}
	return llmguard.Default()
}

func (m *OpenAIModel) httpClient() *http.Client {
	return m.guard().WrapClient(m.Client, 75*time.Second, llmguard.CallMeta{
		UserID:   m.UserID,
		Scope:    llmguard.ScopeOuter,
		Provider: m.provider(),
		Model:    m.name,
	})
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
	g := llmguard.New(llmguard.DefaultConfig())
	return &OpenAIModel{
		name:   name,
		key:    key,
		Guard:  g,
		Client: g.Client(75*time.Second, llmguard.CallMeta{Scope: llmguard.ScopeOuter, Model: name}),
	}
}

func NewGatewayModel(baseURL, key, name string) *OpenAIModel {
	model := NewOpenAIModel(key, name)
	model.gatewayURL = strings.TrimRight(baseURL, "/") + "/chat/completions"
	return model
}

func NewOpenRouterModel(key, name string) *OpenAIModel {
	model := NewOpenAIModel(key, name)
	model.openRouter = true
	return model
}

func (m *OpenAIModel) Name() string { return m.name }

func (m *OpenAIModel) provider() string {
	if m.ProviderName != "" {
		return m.ProviderName
	}
	if m.gatewayURL != "" {
		return modelgateway.Provider
	}
	if m.openRouter {
		return "openrouter"
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
	if m.ProviderName == llmguard.ProviderHome {
		zero := float64(0)
		reply.Usage.CostUSD, reply.Usage.CostStatus = &zero, "local"
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
	} else if m.openRouter {
		endpoint = openRouterChatURL
	}
	scope := llmguard.MetaFromContext(ctx).Scope
	if scope == "" {
		scope = llmguard.ScopeOuter
	}
	meta := llmguard.CallMeta{
		UserID:          m.UserID,
		Scope:           scope,
		Provider:        m.provider(),
		Model:           m.name,
		MaxOutputTokens: 4096,
		BYOK:            m.BYOKProviderLabel != "",
	}
	reqCtx := llmguard.WithMeta(ctx, meta)
	if m.BYOKProviderLabel != "" {
		reply.Usage.EstimatedExposureUSD = llmguard.EstimateCallUSD(meta, len(body))
	}
	if _, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body)); err != nil {
		return reply, fmt.Errorf("invalid model request")
	}
	reply.Usage.RequestStatus = "pending"
	if request.RecordUsage != nil {
		if err := request.RecordUsage(ctx, reply.Usage); err != nil {
			reply.Usage.RequestStatus = "not_sent"
			return reply, usageRecordError(err)
		}
	}
	reply.Usage.RequestStatus = "unknown"
	requestStarted = m.now()
	onRetry := func(statusCode int) error {
		if request.RecordUsage == nil {
			return nil
		}
		prev := reply.Usage
		if statusCode > 0 {
			prev.RequestStatus = "received"
		} else {
			prev.RequestStatus = "unknown"
		}
		_ = usage.Finish(ctx, usage.Func(request.RecordUsage), prev)
		nextUsage := prev
		nextUsage.ID = m.newID()
		nextUsage.CreatedAt = m.now().UTC()
		nextUsage.RequestStatus = "pending"
		nextUsage.CostStatus = "unknown"
		nextUsage.CostUSD = nil
		if m.BYOKProviderLabel == "" {
			nextUsage.EstimatedExposureUSD = 0
		}
		nextUsage.InputTokens = nil
		nextUsage.OutputTokens = nil
		nextUsage.TotalTokens = nil
		nextUsage.CachedTokens = nil
		nextUsage.CacheWriteTokens = nil
		reply.Usage = nextUsage
		if err := request.RecordUsage(ctx, reply.Usage); err != nil {
			reply.Usage.RequestStatus = "not_sent"
			return err
		}
		reply.Usage.RequestStatus = "unknown"
		return nil
	}
	res, err := m.doWithRetry(reqCtx, endpoint, body, onRetry)
	if err != nil {
		if errors.Is(err, usage.ErrUserQuotaExceeded) || isSupersededCallError(err) {
			reply.Usage.RequestStatus = "not_sent"
			return reply, err
		}
		if llmguard.IsGuardError(err) {
			reply.Usage.RequestStatus = "not_sent"
			return reply, err
		}
		if ctx.Err() != nil {
			return reply, ctx.Err()
		}
		if m.ProviderName == llmguard.ProviderHome {
			return reply, err
		}
		return reply, ErrProviderUnavailable
	}
	reply.Usage.RequestStatus = "received"
	defer func() {
		if m.ProviderName == llmguard.ProviderHome {
			llmguard.RecordResponseCost(res, 0)
		} else if reply.Usage.CostUSD != nil {
			llmguard.RecordResponseCost(res, *reply.Usage.CostUSD)
		}
		res.Body.Close()
	}()
	if res.StatusCode != http.StatusOK {
		if m.BYOKProviderLabel != "" {
			if err := llmguard.ModelAccessError(res, m.BYOKProviderLabel); err != nil {
				return reply, err
			}
		}
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

func logRefusal(ctx context.Context, res *http.Response) bool {
	var body struct {
		Error struct {
			Type    string          `json:"type"`
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(res.Body, 16<<10))
	_ = json.Unmarshal(data, &body)
	errMsg := ""
	if body.Error.Type == "invalid_request_error" {
		errMsg = body.Error.Message
		if len(errMsg) > 300 {
			errMsg = errMsg[:300]
		}
	}
	h := res.Header
	slog.WarnContext(ctx, "outer model refused",
		"status", res.StatusCode,
		"error_type", body.Error.Type,
		"error_code", string(body.Error.Code),
		"error_message", errMsg,
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
	return name == openAIModelName || name == "gpt-6.2-sol" || name == "gpt-6.1-terra" || name == "gpt-6-luna"
}

func (m *OpenAIModel) requestBody(request ReplyRequest, outputLimit int, span trace.Span) ([]byte, error) {
	if m.gatewayURL == "" && !m.openRouter && !isSupportedDirectOpenAIModel(m.name) {
		return nil, errors.New("unsupported direct OpenAI conversation model")
	}
	var breakpoints []int
	if m.gatewayURL == "" && !m.openRouter {
		breakpoints = request.CacheBreakpoints
	}
	input, err := openAIConversation(request.Messages, breakpoints)
	if err != nil {
		return nil, err
	}
	if m.GoogleCompatibility || strings.HasPrefix(strings.TrimPrefix(m.name, "google/"), "gemini-") {
		input = completeGeminiTurn(input)
	}
	body := map[string]any{
		"model": m.name, "messages": input, "max_completion_tokens": outputLimit, "store": false,
	}
	if m.GoogleCompatibility {
		delete(body, "store")
		delete(body, "max_completion_tokens")
		body["max_tokens"] = outputLimit
	}
	if m.gatewayURL == "" && !m.openRouter {
		effort := m.ReasoningEffort
		if effort == "" {
			effort = "low"
		}
		body["reasoning_effort"], body["service_tier"] = effort, "default"
		body["prompt_cache_options"] = map[string]string{"mode": "explicit"}
		span.SetAttributes(attribute.String("gen_ai.request.reasoning_effort", effort), attribute.String("gen_ai.request.service_tier", "default"))
	} else if (m.gatewayURL != "" || m.openRouter) && m.AllowGatewayReasoning && m.ReasoningEffort != "" {
		body["reasoning_effort"] = m.ReasoningEffort
		span.SetAttributes(attribute.String("gen_ai.request.reasoning_effort", m.ReasoningEffort))
	}
	if request.Stream != nil {
		body["stream"] = true
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if len(request.Tools) > 0 {
		body["response_format"] = foregroundFormat(request.Tools)
	}
	if !request.NoDispatch {
		stable := m.gatewayURL == "" && !request.CommandPlan
		body["response_format"] = responseFormat(request.TaskIDs, request.WorkRequests, request.MemoryIDs, request.CommandPlan, m.gatewayURL != "", stable)
		if m.ProviderName == llmguard.ProviderHome && !request.Foreground && !request.CommandPlan && len(request.WorkRequests) == 0 {
			body["messages"] = append(input, map[string]any{
				"role":    "system",
				"content": prompts.Runtime("home-chat-routing", nil),
			})
		}
	}
	return json.Marshal(body)
}

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
	if response.Model != "" {
		reply.ServedModel = response.Model
	}
	if response.Usage != nil {
		reply.Usage.InputTokens = response.Usage.Input
		reply.Usage.OutputTokens = response.Usage.Output
		reply.Usage.TotalTokens = response.Usage.Total
		cached, written := response.Usage.InputDetails.Cached, response.Usage.InputDetails.Write
		reply.Usage.CachedTokens, reply.Usage.CacheWriteTokens = &cached, &written
		if m.ProviderName == llmguard.ProviderHome {
			reply.Usage.CostStatus = "local"
		} else if m.gatewayURL != "" || m.openRouter {
			var cost *float64
			if json.Unmarshal(response.Usage.Cost, &cost) == nil {
				usage.RecordReportedCost(&reply.Usage, cost)
			}
			if reply.Usage.CostUSD == nil && m.LocalOnly {
				reply.Usage.CostStatus = "local"
			}
		} else {
			cost, costErr := usage.OpenAICostWithWrites(
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
	if reply.Usage.CostUSD == nil && m.LocalOnly {
		reply.Usage.CostStatus = "local"
	}
	if len(response.Choices) != 1 {
		return reply, fmt.Errorf("model returned no single response")
	}
	choice := response.Choices[0]
	if choice.Finish != "stop" && choice.Finish != "tool_calls" {
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
	observeOuterCall(ctx, span, m.provider(), m.name, m.now(), requestStarted, reply, err)
}

func observeOuterCall(ctx context.Context, span trace.Span, providerName, modelName string, now, requestStarted time.Time, reply Reply, err error) {
	provider := attribute.String("gen_ai.provider.name", providerName)
	model := attribute.String("gen_ai.request.model", modelName)
	eventAttrs := []attribute.KeyValue{provider, model}
	span.SetAttributes(
		attribute.Bool("nori.output.present", reply.Text != ""), attribute.Int("nori.tool.call_count", len(reply.Calls)),
		attribute.String("nori.cost.status", reply.Usage.CostStatus),
	)
	if reply.Usage.InputTokens != nil {
		input := *reply.Usage.InputTokens
		span.SetAttributes(
			attribute.Int64("gen_ai.usage.input_tokens", input), attribute.Int64("llm.token_count.prompt", input),
		)
		eventAttrs = append(eventAttrs, attribute.Int64("gen_ai.usage.input_tokens", input))
		observability.Metric(ctx, "nori.model.input_tokens", float64(input))
	}
	if reply.Usage.CachedTokens != nil {
		cached := *reply.Usage.CachedTokens
		span.SetAttributes(attribute.Int64("gen_ai.usage.cache_read.input_tokens", cached))
		eventAttrs = append(eventAttrs, attribute.Int64("gen_ai.usage.cache_read.input_tokens", cached))
		observability.Metric(ctx, "nori.model.cache_read_tokens", float64(cached))
		if reply.Usage.InputTokens != nil && *reply.Usage.InputTokens > 0 {
			ratio := float64(cached) / float64(*reply.Usage.InputTokens)
			span.SetAttributes(attribute.Float64("nori.model.cache_hit_ratio", ratio))
			eventAttrs = append(eventAttrs, attribute.Float64("nori.model.cache_hit_ratio", ratio))
			observability.Metric(ctx, "nori.model.cache_hit_ratio", ratio)
		}
	}
	if reply.Usage.CacheWriteTokens != nil {
		written := *reply.Usage.CacheWriteTokens
		span.SetAttributes(attribute.Int64("gen_ai.usage.cache_write.input_tokens", written))
		eventAttrs = append(eventAttrs, attribute.Int64("gen_ai.usage.cache_write.input_tokens", written))
		observability.Metric(ctx, "nori.model.cache_write_tokens", float64(written))
	}
	if reply.Usage.OutputTokens != nil {
		output := *reply.Usage.OutputTokens
		span.SetAttributes(
			attribute.Int64("gen_ai.usage.output_tokens", output), attribute.Int64("llm.token_count.completion", output),
		)
		eventAttrs = append(eventAttrs, attribute.Int64("gen_ai.usage.output_tokens", output))
		observability.Metric(ctx, "nori.model.output_tokens", float64(output))
	}
	if reply.Usage.TotalTokens != nil {
		span.SetAttributes(attribute.Int64("llm.token_count.total", *reply.Usage.TotalTokens))
	}
	if reply.Usage.CostUSD != nil {
		cost := *reply.Usage.CostUSD
		span.SetAttributes(attribute.Float64("nori.cost.usd", cost), attribute.Float64("llm.cost.total", cost))
		eventAttrs = append(eventAttrs, attribute.Float64("nori.cost.usd", cost))
		observability.Metric(ctx, "nori.model.cost_usd", cost)
	}
	if a, ok := observability.ContentAttribute("output.value", reply.Text); ok {
		span.SetAttributes(a)
	}
	if err != nil && !errors.Is(ctx.Err(), context.Canceled) && !isSupersededCallError(err) {
		observability.RecordErrorCategory(ctx, "model.outer", err)
		if !requestStarted.IsZero() {
			observability.Event(ctx, "model.call.failed", provider, model)
		}
	} else if err == nil && !requestStarted.IsZero() {
		observability.Event(ctx, "model.call.completed", eventAttrs...)
	}
	if !requestStarted.IsZero() && !errors.Is(ctx.Err(), context.Canceled) {
		observability.Metric(ctx, "nori.model.duration_ms", float64(now.Sub(requestStarted).Milliseconds()))
	}
	span.End()
}

func collapseActionFreeReplay(messages []json.RawMessage, breakpoints []int) ([]json.RawMessage, []int) {
	if len(messages) < 2 {
		return messages, breakpoints
	}
	marked := make(map[int]bool, len(breakpoints))
	for _, b := range breakpoints {
		marked[b] = true
	}
	out := make([]json.RawMessage, 0, len(messages))
	outBPs := make([]int, 0, len(breakpoints))
	for i := 0; i < len(messages); i++ {
		if i+1 < len(messages) && isDuplicateActionFreePair(messages[i], messages[i+1]) {
			if marked[i] {
				marked[i+1] = true
			}
			continue
		}
		if marked[i] {
			outBPs = append(outBPs, len(out))
		}
		out = append(out, messages[i])
	}
	return out, outBPs
}

func isDuplicateActionFreePair(first, second json.RawMessage) bool {
	var a, b struct {
		Role    string          `json:"role"`
		Content *string         `json:"content"`
		Calls   json.RawMessage `json:"tool_calls"`
	}
	if json.Unmarshal(first, &a) != nil || json.Unmarshal(second, &b) != nil {
		return false
	}
	if a.Role != "assistant" || b.Role != "assistant" || a.Content == nil || b.Content == nil {
		return false
	}
	if len(a.Calls) > 0 && string(a.Calls) != "null" && string(a.Calls) != "[]" {
		return false
	}
	var env struct {
		Actions []json.RawMessage `json:"actions"`
		Text    string            `json:"text"`
	}
	if json.Unmarshal([]byte(*a.Content), &env) != nil || len(env.Actions) != 0 {
		return false
	}
	return strings.TrimSpace(env.Text) == strings.TrimSpace(*b.Content)
}

func completeGeminiTurn(messages []map[string]any) []map[string]any {
	// Gemini extracts system messages before validating the final conversation role.
	for i := len(messages) - 1; i >= 0; i-- {
		switch messages[i]["role"] {
		case "system":
			continue
		case "user":
			return messages
		}
		break
	}
	return append(messages, map[string]any{"role": "user", "content": prompts.Runtime("gemini-current-turn", nil)})
}

func openAIConversation(messages []json.RawMessage, breakpoints []int) ([]map[string]any, error) {
	messages, breakpoints = collapseActionFreeReplay(messages, breakpoints)
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
				} else if !request.NoDispatch {
					partial, ok := actionFreeStructuredPartial(text)
					if !ok {
						visible = ""
					} else {
						visible = partial
					}
				}
				if visible != "" {
					if err := request.Stream(visible); err != nil {
						return reply, err
					}
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
		if m.ProviderName == llmguard.ProviderHome {
			return reply, homeai.ErrInterrupted
		}
		return reply, ErrStreamInterrupted
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
	decoded, err := m.decodeOpenAIReply(&copyResponse, reply, request.NoDispatch || request.Foreground, span)
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
