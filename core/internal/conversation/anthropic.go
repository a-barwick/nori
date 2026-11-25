package conversation

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

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/contextview"
	"nori/internal/llmguard"
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

// AnthropicModel implements ReplyModel using Anthropic's Messages API,
// routed exclusively through llmguard.
type AnthropicModel struct {
	name   string
	key    string
	Client *http.Client
	Guard  *llmguard.Guard
	UserID string
	// TestURL replaces the provider endpoint with a local stub (settings.Models.TestURL).
	TestURL string
	// ReasoningEffort optionally sets Anthropic output_config.effort ("low", "medium", "high", "max").
	ReasoningEffort string
	Now             func() time.Time
	NewID           func() string
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

func (m *AnthropicModel) guard() *llmguard.Guard {
	if m != nil && m.Guard != nil {
		return m.Guard
	}
	return llmguard.Default()
}

func (m *AnthropicModel) httpClient() *http.Client {
	return m.guard().WrapClient(m.Client, 75*time.Second, llmguard.CallMeta{
		UserID:   m.UserID,
		Scope:    llmguard.ScopeOuter,
		Provider: anthropicProvider,
		Model:    m.name,
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
		attribute.String("gen_ai.provider.name", anthropicProvider),
		attribute.String("gen_ai.request.model", m.name),
		attribute.String("llm.model_name", m.name),
	))
	defer span.End()

	reply.Usage = state.Usage{
		ID: m.newID(), Provider: "outer", Model: m.name, CostStatus: "unknown", RequestStatus: "not_sent", CreatedAt: m.now().UTC(),
	}

	body, forcedTool, err := m.buildRequestBody(request, 4096)
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
	reqCtx := llmguard.WithMeta(ctx, llmguard.CallMeta{
		UserID:          m.UserID,
		Scope:           scope,
		Provider:        anthropicProvider,
		Model:           m.name,
		MaxOutputTokens: 4096,
	})

	reply.Usage.RequestStatus = "pending"
	if request.RecordUsage != nil {
		if err := request.RecordUsage(ctx, reply.Usage); err != nil {
			reply.Usage.RequestStatus = "not_sent"
			if errors.Is(err, usage.ErrUserQuotaExceeded) {
				return reply, err
			}
			return reply, fmt.Errorf("usage recording unavailable; provider request was not sent")
		}
	}
	reply.Usage.RequestStatus = "unknown"

	res, err := m.doWithRetry(reqCtx, body)
	if err != nil {
		if llmguard.IsGuardError(err) {
			reply.Usage.RequestStatus = "not_sent"
			return reply, err
		}
		if ctx.Err() != nil {
			return reply, ctx.Err()
		}
		return reply, fmt.Errorf("model request interrupted or unavailable")
	}
	reply.Usage.RequestStatus = "received"
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		if logRefusal(ctx, res) && (res.StatusCode == 400 || res.StatusCode == 413) {
			return reply, contextview.ErrOverflow
		}
		return reply, providerHTTPError{res.StatusCode, fmt.Sprintf("model provider returned HTTP %d", res.StatusCode)}
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
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response); err != nil {
		return reply, fmt.Errorf("model returned an unreadable response")
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
		modelName := response.Model
		if modelName == "" {
			modelName = m.name
		}
		if cost, costErr := usage.AnthropicCost(modelName, response.Usage.Input, response.Usage.Output, cached, written); costErr == nil {
			reply.Usage.CostUSD, reply.Usage.CostStatus = &cost, "calculated"
			m.guard().RecordActualCost(cost)
		}
	}

	if response.StopReason != "" && response.StopReason != "end_turn" && response.StopReason != "tool_use" {
		return reply, fmt.Errorf("anthropic model did not finish normally (stop_reason=%s)", response.StopReason)
	}

	if forcedTool != "" {
		for _, blk := range response.Content {
			if blk.Type == "tool_use" && blk.Name == forcedTool {
				rawJSON := string(blk.Input)
				reply.Message = modelMessage(map[string]any{"role": "assistant", "content": rawJSON})
				if request.Foreground && len(request.Tools) > 0 {
					reply.Text, reply.Calls, err = decodeForeground(rawJSON)
					if err == nil && request.Stream != nil && reply.Text != "" {
						if streamErr := request.Stream(reply.Text); streamErr != nil {
							return reply, streamErr
						}
					}
					return reply, err
				}
				reply.Structured = true
				reply.Text, reply.Calls, err = decodeResponse(rawJSON)
				if err == nil && request.Stream != nil && reply.Text != "" {
					if streamErr := request.Stream(reply.Text); streamErr != nil {
						return reply, streamErr
					}
				}
				return reply, err
			}
		}
		return reply, errors.New("anthropic model did not return required structured tool response")
	}

	var texts []string
	for _, blk := range response.Content {
		if blk.Type == "text" && strings.TrimSpace(blk.Text) != "" {
			texts = append(texts, blk.Text)
		}
	}
	reply.Text = strings.TrimSpace(strings.Join(texts, "\n"))
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

func (m *AnthropicModel) doWithRetry(ctx context.Context, body []byte) (*http.Response, error) {
	client := m.httpClient()
	const maxRetries = 3
	baseDelay := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.endpoint(), bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("invalid model request")
		}
		httpReq.Header.Set("x-api-key", m.key)
		httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
		httpReq.Header.Set("Content-Type", "application/json")

		res, err := client.Do(httpReq)
		if err != nil {
			if attempt >= maxRetries || ctx.Err() != nil || llmguard.IsGuardError(err) {
				return nil, err
			}
			delay := baseDelay << attempt
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		retryable := res.StatusCode == http.StatusTooManyRequests ||
			res.StatusCode == 529 ||
			res.StatusCode >= 500
		if !retryable || attempt >= maxRetries {
			return res, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		delay := baseDelay << attempt
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func (m *AnthropicModel) buildRequestBody(request ReplyRequest, outputLimit int) ([]byte, string, error) {
	marked := make(map[int]bool, len(request.CacheBreakpoints))
	for _, i := range request.CacheBreakpoints {
		marked[i] = true
	}

	var systemBlocks []map[string]any
	var messages []map[string]any
	appendMsg := func(role, text string, cache bool) {
		if text == "" {
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

	// Only leading system messages go into top-level `system`. Mid-conversation
	// system notices (task notifications, receipts, advisory) stay in chronological
	// order inside `messages` so turn chronology and leading cache prefixes hold.
	inLeadingSystem := true
	for i, raw := range request.Messages {
		var message struct {
			Role    string          `json:"role"`
			Content *string         `json:"content"`
			Calls   json.RawMessage `json:"tool_calls"`
		}
		if json.Unmarshal(raw, &message) != nil {
			return nil, "", fmt.Errorf("invalid saved conversation message")
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
			return nil, "", fmt.Errorf("unsupported saved conversation role")
		}
	}
	if len(messages) == 0 {
		appendMsg("user", "Continue.", false)
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
	if len(systemBlocks) > 0 {
		body["system"] = systemBlocks
	}
	if effort := strings.ToLower(strings.TrimSpace(m.ReasoningEffort)); effort != "" && effort != "none" {
		body["output_config"] = map[string]string{"effort": effort}
	}

	forcedTool := ""
	if len(request.Tools) > 0 {
		if rf, ok := foregroundFormat(request.Tools).(map[string]any); ok {
			if js, ok := rf["json_schema"].(map[string]any); ok {
				forcedTool = "chat_response"
				body["tools"] = []map[string]any{{
					"name":         forcedTool,
					"description":  "Return the structured chat response.",
					"input_schema": js["schema"],
				}}
				body["tool_choice"] = map[string]any{"type": "tool", "name": forcedTool}
			}
		}
	}
	if !request.NoDispatch {
		stable := !request.CommandPlan
		if rf, ok := responseFormat(request.TaskIDs, request.WorkRequests, request.MemoryIDs, request.CommandPlan, false, stable).(map[string]any); ok {
			if js, ok := rf["json_schema"].(map[string]any); ok {
				forcedTool = "nori_response"
				body["tools"] = []map[string]any{{
					"name":         forcedTool,
					"description":  "Return the structured assistant reply and optional routing actions.",
					"input_schema": js["schema"],
				}}
				body["tool_choice"] = map[string]any{"type": "tool", "name": forcedTool}
			}
		}
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	return encoded, forcedTool, nil
}
