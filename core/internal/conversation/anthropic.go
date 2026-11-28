package conversation

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/contextview"
	"nori/internal/httpretry"
	"nori/internal/llmguard"
	"nori/internal/modelgateway"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/usage"
)

const (
	anthropicMessagesURL = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion  = "2023-06-01"
	anthropicProvider    = "anthropic"
)

type AnthropicModel struct {
	name              string
	key               string
	gatewayURL        string
	Client            *http.Client
	Guard             *llmguard.Guard
	UserID            string
	TestURL           string
	ReasoningEffort   string
	BYOKProviderLabel string
	BillingMode       string
	Now               func() time.Time
	NewID             func() string
}

func (m *AnthropicModel) billingMode() string {
	switch m.BillingMode {
	case state.UsageBillingHosted, state.UsageBillingBYOK, state.UsageBillingLocal:
		return m.BillingMode
	}
	if m.BYOKProviderLabel != "" {
		return state.UsageBillingBYOK
	}
	return state.UsageBillingHosted
}

func NewAnthropicModel(key, name string) *AnthropicModel {
	g := llmguard.New(llmguard.DefaultConfig())
	return &AnthropicModel{
		name:   name,
		key:    key,
		Guard:  g,
		Client: g.Client(75*time.Second, llmguard.CallMeta{Scope: llmguard.ScopeOuter, Provider: anthropicProvider, Model: name}),
	}
}

func NewGatewayAnthropicModel(baseURL, key, name string) *AnthropicModel {
	m := NewAnthropicModel(key, name)
	m.gatewayURL = strings.TrimRight(baseURL, "/") + "/messages"
	return m
}

func (m *AnthropicModel) provider() string {
	if m.gatewayURL != "" {
		return modelgateway.Provider
	}
	return anthropicProvider
}

func (m *AnthropicModel) guard() *llmguard.Guard {
	if m != nil && m.Guard != nil {
		return m.Guard
	}
	return llmguard.Default()
}

func (m *AnthropicModel) httpClient() *http.Client {
	billingMode := m.billingMode()
	return m.guard().WrapClient(m.Client, 75*time.Second, llmguard.CallMeta{
		UserID:   m.UserID,
		Scope:    llmguard.ScopeOuter,
		Provider: m.provider(),
		Model:    m.name,
		Billing:  billingMode,
		BYOK:     billingMode == state.UsageBillingBYOK,
	})
}

func (m *AnthropicModel) now() time.Time {
	if m != nil && m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *AnthropicModel) newID() string {
	if m != nil && m.NewID != nil {
		return m.NewID()
	}
	return uuid.NewString()
}

func (m *AnthropicModel) Name() string { return m.name }

func (m *AnthropicModel) endpoint() string {
	if m.gatewayURL != "" {
		return m.gatewayURL
	}
	if m.TestURL != "" {
		return strings.TrimRight(m.TestURL, "/") + "/v1/messages"
	}
	return anthropicMessagesURL
}

func (m *AnthropicModel) Respond(ctx context.Context, request ReplyRequest) (reply Reply, err error) {
	ctx, span := conversationTracer.Start(ctx, "chat "+m.name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("openinference.span.kind", "LLM"),
		attribute.String("session.id", observability.SessionID()),
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.provider.name", m.provider()),
		attribute.String("gen_ai.request.model", m.name),
		attribute.String("llm.model_name", m.name),
	))
	var requestStarted time.Time
	defer func() {
		if reply.Usage.RequestStatus != "not_sent" {
			cost, costKnown := 0.0, false
			if reply.Usage.CostUSD != nil {
				cost, costKnown = *reply.Usage.CostUSD, true
			}
			llmguard.ObserveCallCost(ctx, cost, costKnown, true)
		}
		observeOuterCall(ctx, span, m.provider(), m.name, m.now(), requestStarted, reply, err)
	}()

	billingMode := m.billingMode()
	reply.Usage = usage.AttemptWithMeta(ctx, "outer", m.name, billingMode, state.UsageCategoryLLM)
	if m.NewID != nil {
		reply.Usage.ID = m.newID()
	}
	if m.Now != nil {
		reply.Usage.CreatedAt = m.now().UTC()
	}
	reply.Usage.RequestStatus = "not_sent"

	body, structured, err := m.buildRequestBody(request, 4096)
	if err != nil {
		return reply, err
	}
	if err := contextview.Check(len(body)); err != nil {
		return reply, err
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
		Billing:         billingMode,
		MaxOutputTokens: 4096,
		BYOK:            billingMode == state.UsageBillingBYOK,
	}
	reqCtx := llmguard.WithMeta(ctx, meta)
	if billingMode != state.UsageBillingLocal {
		reply.Usage.EstimatedExposureUSD = llmguard.EstimateCallUSD(meta, len(body))
	}

	reply.Usage.RequestStatus = "pending"
	if request.RecordUsage != nil {
		if err := request.RecordUsage(ctx, reply.Usage); err != nil {
			reply.Usage.RequestStatus = "not_sent"
			return reply, usageRecordError(err)
		}
	}
	reply.Usage.RequestStatus = "unknown"

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
		nextUsage := usage.AttemptWithMeta(ctx, "outer", m.name, billingMode, state.UsageCategoryLLM)
		if m.NewID != nil {
			nextUsage.ID = m.newID()
		}
		if m.Now != nil {
			nextUsage.CreatedAt = m.now().UTC()
		}
		if billingMode != state.UsageBillingLocal {
			nextUsage.EstimatedExposureUSD = llmguard.EstimateCallUSD(meta, len(body))
		}
		reply.Usage = nextUsage
		if err := request.RecordUsage(ctx, reply.Usage); err != nil {
			reply.Usage.RequestStatus = "not_sent"
			return err
		}
		reply.Usage.RequestStatus = "unknown"
		return nil
	}

	requestStarted = m.now()
	res, err := m.doWithRetry(reqCtx, body, onRetry)
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
		return reply, ErrProviderUnavailable
	}
	reply.Usage.RequestStatus = "received"
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		if m.BYOKProviderLabel != "" {
			if err := llmguard.ModelAccessError(res, m.BYOKProviderLabel); err != nil {
				return reply, err
			}
		}
		if logRefusal(ctx, res) && (res.StatusCode == 400 || res.StatusCode == 413) {
			return reply, contextview.ErrOverflow
		}
		return reply, providerHTTPError{res.StatusCode, fmt.Sprintf("model provider returned HTTP %d", res.StatusCode)}
	}
	if request.Stream != nil && len(request.Tools) == 0 {
		return m.decodeAnthropicStream(res, reply, request, structured)
	}

	var response struct {
		ID         string `json:"id"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
		Usage *struct {
			Input      int64    `json:"input_tokens"`
			Output     int64    `json:"output_tokens"`
			CacheRead  int64    `json:"cache_read_input_tokens"`
			CacheWrite int64    `json:"cache_creation_input_tokens"`
			Cost       *float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response); err != nil {
		return reply, fmt.Errorf("model returned an unreadable response")
	}
	if response.Model != "" {
		reply.ServedModel = response.Model
	}
	if response.Usage != nil {
		totalIn := response.Usage.Input + response.Usage.CacheRead + response.Usage.CacheWrite
		outTok := response.Usage.Output
		total := totalIn + outTok
		cached := response.Usage.CacheRead
		written := response.Usage.CacheWrite
		reply.Usage.InputTokens = &totalIn
		reply.Usage.OutputTokens = &outTok
		reply.Usage.TotalTokens = &total
		reply.Usage.CachedTokens = &cached
		reply.Usage.CacheWriteTokens = &written
		if usage.RecordReportedCost(&reply.Usage, response.Usage.Cost) {
			llmguard.RecordResponseCost(res, *response.Usage.Cost)
		} else {
			for _, modelName := range []string{strings.TrimSpace(response.Model), strings.TrimSpace(m.name)} {
				if modelName == "" {
					continue
				}
				if cost, costErr := usage.AnthropicCost(modelName, response.Usage.Input, response.Usage.Output, cached, written); costErr == nil {
					reply.Usage.CostUSD, reply.Usage.CostStatus = &cost, "calculated"
					llmguard.RecordResponseCost(res, cost)
					break
				}
			}
		}
	}

	if response.StopReason != "" && response.StopReason != "end_turn" && response.StopReason != "tool_use" {
		return reply, fmt.Errorf("anthropic model did not finish normally (stop_reason=%s)", response.StopReason)
	}

	var texts []string
	for _, blk := range response.Content {
		if blk.Type == "text" && strings.TrimSpace(blk.Text) != "" {
			texts = append(texts, blk.Text)
		}
	}
	rawText := strings.TrimSpace(strings.Join(texts, "\n"))

	if structured {
		if rawText == "" {
			return reply, errors.New("anthropic model did not return required structured JSON response")
		}
		reply.Message = modelMessage(map[string]any{"role": "assistant", "content": rawText})
		if request.Foreground && len(request.Tools) > 0 {
			reply.Text, reply.Calls, err = decodeForeground(rawText)
			if err == nil && request.Stream != nil && reply.Text != "" {
				if streamErr := request.Stream(reply.Text); streamErr != nil {
					return reply, streamErr
				}
			}
			return reply, err
		}
		reply.Structured = true
		reply.Text, reply.Calls, err = decodeResponse(rawText)
		if err == nil && request.Stream != nil && reply.Text != "" {
			if streamErr := request.Stream(reply.Text); streamErr != nil {
				return reply, streamErr
			}
		}
		return reply, err
	}

	reply.Text = rawText
	reply.Message = modelMessage(map[string]any{"role": "assistant", "content": reply.Text})
	if reply.Text == "" {
		return reply, fmt.Errorf("model returned neither a reply nor a task command")
	}
	if request.Stream != nil {
		if err := request.Stream(reply.Text); err != nil {
			return reply, err
		}
	}
	return reply, nil
}

func (m *AnthropicModel) decodeAnthropicStream(res *http.Response, reply Reply, request ReplyRequest, structured bool) (Reply, error) {
	scanner := bufio.NewScanner(io.LimitReader(res.Body, 2<<20))
	scanner.Buffer(make([]byte, 4096), 256<<10)

	var (
		modelName   string
		stopReason  string
		inTok       int64
		outTok      int64
		cacheRead    int64
		cacheWrite   int64
		reportedCost *float64
		hasUsage     bool
		done         bool
		rawBuf       strings.Builder
		lastEmitted  string
	)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var evt struct {
			Type    string `json:"type"`
			Message *struct {
				Model string `json:"model"`
				Usage *struct {
					Input      int64    `json:"input_tokens"`
					Output     int64    `json:"output_tokens"`
					CacheRead  int64    `json:"cache_read_input_tokens"`
					CacheWrite int64    `json:"cache_creation_input_tokens"`
					Cost       *float64 `json:"cost"`
				} `json:"usage"`
			} `json:"message"`
			Delta *struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage *struct {
				Output int64    `json:"output_tokens"`
				Cost   *float64 `json:"cost"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &evt) != nil {
			return reply, errors.New("anthropic model returned an unreadable stream")
		}
		switch evt.Type {
		case "message_start":
			if evt.Message != nil {
				if evt.Message.Model != "" {
					modelName = evt.Message.Model
				}
				if u := evt.Message.Usage; u != nil {
					hasUsage = true
					inTok = u.Input
					cacheRead = u.CacheRead
					cacheWrite = u.CacheWrite
					if u.Output > outTok {
						outTok = u.Output
					}
					if u.Cost != nil {
						reportedCost = u.Cost
					}
				}
			}
		case "content_block_delta":
			if evt.Delta != nil && evt.Delta.Type == "text_delta" && evt.Delta.Text != "" {
				rawBuf.WriteString(evt.Delta.Text)
				if rawBuf.Len() > 32768 {
					return reply, errors.New("model reply exceeded limit")
				}
				accum := rawBuf.String()
				var visible string
				switch {
				case !structured:
					visible = accum
				case request.Foreground && len(request.Tools) > 0:
					visible = foregroundPartial(accum)
				default:
					if partial, ok := actionFreeStructuredPartial(accum); ok {
						visible = partial
					}
				}
				if visible != "" && visible != lastEmitted {
					lastEmitted = visible
					if err := request.Stream(visible); err != nil {
						return reply, err
					}
				}
			}
		case "message_delta":
			if evt.Delta != nil && evt.Delta.StopReason != "" {
				stopReason = evt.Delta.StopReason
			}
			if evt.Usage != nil {
				hasUsage = true
				if evt.Usage.Output > outTok {
					outTok = evt.Usage.Output
				}
				if evt.Usage.Cost != nil {
					reportedCost = evt.Usage.Cost
				}
			}
		case "message_stop":
			done = true
		case "error":
			return reply, errors.New("anthropic model stream returned an error")
		}
	}

	if modelName != "" {
		reply.ServedModel = modelName
	}
	if hasUsage && done && scanner.Err() == nil {
		totalIn := inTok + cacheRead + cacheWrite
		total := totalIn + outTok
		reply.Usage.InputTokens = &totalIn
		reply.Usage.OutputTokens = &outTok
		reply.Usage.TotalTokens = &total
		reply.Usage.CachedTokens = &cacheRead
		reply.Usage.CacheWriteTokens = &cacheWrite
		if usage.RecordReportedCost(&reply.Usage, reportedCost) {
			llmguard.RecordResponseCost(res, *reportedCost)
		} else {
			for _, candidate := range []string{strings.TrimSpace(modelName), strings.TrimSpace(m.name)} {
				if candidate == "" {
					continue
				}
				if cost, costErr := usage.AnthropicCost(candidate, inTok, outTok, cacheRead, cacheWrite); costErr == nil {
					reply.Usage.CostUSD, reply.Usage.CostStatus = &cost, "calculated"
					llmguard.RecordResponseCost(res, cost)
					break
				}
			}
		}
	}

	if scanner.Err() != nil || !done {
		return reply, ErrStreamInterrupted
	}
	if stopReason != "" && stopReason != "end_turn" {
		return reply, fmt.Errorf("anthropic model did not finish normally (stop_reason=%s)", stopReason)
	}

	rawText := strings.TrimSpace(rawBuf.String())
	if structured {
		if rawText == "" {
			return reply, errors.New("anthropic model did not return required structured JSON response")
		}
		reply.Message = modelMessage(map[string]any{"role": "assistant", "content": rawText})
		if request.Foreground && len(request.Tools) > 0 {
			var err error
			reply.Text, reply.Calls, err = decodeForeground(rawText)
			return reply, err
		}
		var err error
		reply.Structured = true
		reply.Text, reply.Calls, err = decodeResponse(rawText)
		return reply, err
	}
	reply.Text = rawText
	reply.Message = modelMessage(map[string]any{"role": "assistant", "content": reply.Text})
	if reply.Text == "" {
		return reply, fmt.Errorf("model returned neither a reply nor a task command")
	}
	return reply, nil
}

func (m *AnthropicModel) doWithRetry(ctx context.Context, body []byte, onRetry func(statusCode int) error) (*http.Response, error) {
	return httpretry.Do(ctx, httpretry.Config{
		Client:    m.httpClient(),
		BaseDelay: 500 * time.Millisecond,
		Now:       m.now,
		ShouldStopRetry: func(res *http.Response) bool {
			return m.BYOKProviderLabel != "" && llmguard.ModelAccessError(res, m.BYOKProviderLabel) != nil
		},
		OnRetry:   onRetry,
		LogPrefix: "outer anthropic model",
	}, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint(), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("invalid model request")
		}
		if m.gatewayURL != "" {
			httpReq.Header.Set("Authorization", "Bearer "+m.key)
		} else {
			httpReq.Header.Set("x-api-key", m.key)
		}
		httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
		httpReq.Header.Set("Content-Type", "application/json")
		return httpReq, nil
	})
}

func (m *AnthropicModel) buildRequestBody(request ReplyRequest, outputLimit int) ([]byte, bool, error) {
	rawMessages, rawBreakpoints := collapseActionFreeReplay(request.Messages, request.CacheBreakpoints)
	marked := make(map[int]bool, len(rawBreakpoints))
	for _, i := range rawBreakpoints {
		marked[i] = true
	}

	var systemBlocks []map[string]any
	var messages []map[string]any
	appendMsg := func(role, text string, cache bool) {
		if strings.TrimSpace(text) == "" {
			return
		}
		block := map[string]any{"type": "text", "text": text}
		if cache {
			block["cache_control"] = map[string]string{"type": "ephemeral"}
		}
		if len(messages) > 0 && messages[len(messages)-1]["role"] == role {
			prev := messages[len(messages)-1]["content"].([]map[string]any)
			messages[len(messages)-1]["content"] = append(prev, block)
			return
		}
		messages = append(messages, map[string]any{"role": role, "content": []map[string]any{block}})
	}

	inLeadingSystem := true
	for i, raw := range rawMessages {
		var message struct {
			Role    string          `json:"role"`
			Content *string         `json:"content"`
			Calls   json.RawMessage `json:"tool_calls"`
		}
		if json.Unmarshal(raw, &message) != nil {
			return nil, false, fmt.Errorf("invalid saved conversation message")
		}
		text := ""
		if message.Content != nil {
			text = *message.Content
		}
		if len(message.Calls) > 0 && string(message.Calls) != "null" && string(message.Calls) != "[]" {
			text += prompts.Runtime("historical-command-proposal", nil) + string(message.Calls)
		}
		switch message.Role {
		case "system":
			if text != "" {
				if inLeadingSystem {
					blk := map[string]any{"type": "text", "text": text}
					if marked[i] {
						blk["cache_control"] = map[string]string{"type": "ephemeral"}
					}
					systemBlocks = append(systemBlocks, blk)
				} else {
					appendMsg("user", text, marked[i])
				}
			}
		case "tool":
			inLeadingSystem = false
			appendMsg("user", prompts.Runtime("saved-command-history", nil)+text, marked[i])
		case "user", "assistant":
			inLeadingSystem = false
			appendMsg(message.Role, text, marked[i])
		default:
			return nil, false, fmt.Errorf("unsupported saved conversation role")
		}
	}
	if len(messages) == 0 {
		if request.NoDispatch && len(request.Tools) == 0 {
			appendMsg("user", "[System turn — follow the instructions above and reply in plain text.]", false)
		} else {
			appendMsg("user", "Continue.", false)
		}
	} else if messages[0]["role"] == "assistant" {
		messages = append([]map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": "[Conversation resumed]"}},
		}}, messages...)
	}

	body := map[string]any{
		"model":      m.name,
		"max_tokens": outputLimit,
		"messages":   messages,
	}
	if request.Stream != nil && len(request.Tools) == 0 {
		body["stream"] = true
	}
	if len(systemBlocks) > 0 {
		body["system"] = systemBlocks
	}
	outputConfig := map[string]any{}
	if effort := strings.ToLower(strings.TrimSpace(m.ReasoningEffort)); effort != "" && effort != "none" {
		outputConfig["effort"] = effort
	}

	structured := false
	if len(request.Tools) > 0 {
		if rf, ok := foregroundFormat(request.Tools).(map[string]any); ok {
			if js, ok := rf["json_schema"].(map[string]any); ok {
				outputConfig["format"] = map[string]any{
					"type":   "json_schema",
					"schema": sanitizeAnthropicSchema(js["schema"]),
				}
				structured = true
			}
		}
	}
	if !request.NoDispatch {
		stable := !request.CommandPlan
		if rf, ok := responseFormat(request.TaskIDs, request.WorkRequests, request.MemoryIDs, request.CommandPlan, false, stable).(map[string]any); ok {
			if js, ok := rf["json_schema"].(map[string]any); ok {
				outputConfig["format"] = map[string]any{
					"type":   "json_schema",
					"schema": sanitizeAnthropicSchema(js["schema"]),
				}
				structured = true
			}
		}
	}
	if len(outputConfig) > 0 {
		body["output_config"] = outputConfig
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, false, err
	}
	return encoded, structured, nil
}

func sanitizeAnthropicSchema(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			switch k {
			case "maxItems", "minItems", "maxLength", "minLength", "maximum", "minimum", "exclusiveMaximum", "exclusiveMinimum", "multipleOf":
				continue
			}
			out[k] = sanitizeAnthropicSchema(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = sanitizeAnthropicSchema(val)
		}
		return out
	default:
		return v
	}
}
