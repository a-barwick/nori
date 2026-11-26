package anthropic

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
	"time"

	"nori/internal/agent"
	"nori/internal/contextview"
	"nori/internal/httpretry"
	"nori/internal/llmguard"
	"nori/internal/modelgateway"
	"nori/internal/transcript"
	"nori/internal/usage"
)

const (
	messagesURL = "https://api.anthropic.com/v1/messages"
	apiVersion  = "2023-06-01"
	provider    = "anthropic"
)

type Client struct {
	Key               string
	ModelName         string
	HTTP              *http.Client
	Guard             *llmguard.Guard
	UserID            string
	Meter             usage.Meter
	TestURL           string
	DefaultReasoning  string
	BYOKProviderLabel string
	gatewayURL        string
}

func New(key, model string, meter usage.Meter) *Client {
	g := llmguard.New(llmguard.DefaultConfig())
	return &Client{
		Key:       key,
		ModelName: model,
		Meter:     meter,
		Guard:     g,
		HTTP:      g.Client(2*time.Minute, llmguard.CallMeta{Scope: llmguard.ScopeTask, Provider: provider, Model: model}),
	}
}

func NewGateway(baseURL, key, model string, meter usage.Meter) *Client {
	c := New(key, model, meter)
	c.gatewayURL = strings.TrimRight(baseURL, "/") + "/messages"
	return c
}

func (c *Client) guard() *llmguard.Guard {
	if c != nil && c.Guard != nil {
		return c.Guard
	}
	return llmguard.Default()
}

func (c *Client) httpClient() *http.Client {
	return c.guard().WrapClient(c.HTTP, 2*time.Minute, llmguard.CallMeta{
		UserID:   c.UserID,
		Scope:    llmguard.ScopeTask,
		Provider: c.Provider(),
		Model:    c.ModelName,
	})
}

func (c *Client) Provider() string {
	if c.gatewayURL != "" {
		return modelgateway.Provider
	}
	return provider
}
func (c *Client) Name() string { return c.ModelName }

func (c *Client) endpoint() string {
	if c.gatewayURL != "" {
		return c.gatewayURL
	}
	if c.TestURL != "" {
		return strings.TrimRight(c.TestURL, "/") + "/v1/messages"
	}
	return messagesURL
}

func (c *Client) setAuthHeaders(httpReq *http.Request) {
	if c.gatewayURL != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Key)
	} else {
		httpReq.Header.Set("x-api-key", c.Key)
	}
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("Content-Type", "application/json")
}

func (c *Client) Respond(ctx context.Context, req agent.Request) (out agent.Response, err error) {
	out.NotSent = true
	if strings.TrimSpace(c.ModelName) == "" {
		return out, errors.New("anthropic task model name must be configured")
	}

	messages, err := anthropicMessages(req.Messages, c.BYOKProviderLabel != "")
	if err != nil {
		return out, err
	}
	tools := make([]map[string]any, 0, len(req.Tools))
	for _, tool := range req.Tools {
		schema := tool.Schema
		if schema == nil {
			schema = toolSchema(tool.Parameters)
		}
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}
		} else if _, hasType := schema["type"]; !hasType {
			cloned := make(map[string]any, len(schema)+1)
			for k, v := range schema {
				cloned[k] = v
			}
			cloned["type"] = "object"
			schema = cloned
		}
		tools = append(tools, map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": schema,
		})
	}
	if len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
	}

	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	fields := map[string]any{
		"model":      c.ModelName,
		"max_tokens": maxTokens,
		"messages":   messages,
	}
	if strings.TrimSpace(req.Instructions) != "" {
		fields["system"] = []map[string]any{{
			"type":          "text",
			"text":          req.Instructions,
			"cache_control": map[string]string{"type": "ephemeral"},
		}}
	}
	if len(tools) > 0 {
		fields["tools"] = tools
	}
	effort := strings.ToLower(strings.TrimSpace(req.Reasoning))
	if effort == "" {
		effort = strings.ToLower(strings.TrimSpace(c.DefaultReasoning))
	}
	if effort != "" && effort != "none" {
		fields["output_config"] = map[string]string{"effort": effort}
	}

	body, err := json.Marshal(fields)
	if err != nil {
		return out, errors.New("task request could not be encoded")
	}
	if err := contextview.Check(len(body)); err != nil {
		return out, err
	}

	scope := llmguard.MetaFromContext(ctx).Scope
	if scope == "" {
		scope = llmguard.ScopeTask
	}
	meta := llmguard.CallMeta{
		UserID:          c.UserID,
		Scope:           scope,
		Provider:        c.Provider(),
		Model:           c.ModelName,
		MaxOutputTokens: maxTokens,
		BYOK:            c.BYOKProviderLabel != "",
	}
	reqCtx := llmguard.WithMeta(ctx, meta)
	record := usage.Attempt(ctx, c.Provider(), c.ModelName)
	if c.BYOKProviderLabel != "" {
		record.EstimatedExposureUSD = llmguard.EstimateCallUSD(meta, len(body))
	}
	if err := c.Meter.RecordUsage(ctx, record); err != nil {
		return out, err
	}
	record.RequestStatus = "unknown"
	defer func() { err = errors.Join(err, usage.Finish(ctx, c.Meter, record)) }()

	out.NotSent = false
	onRetry := func(statusCode int) error {
		prev := record
		if statusCode > 0 {
			prev.RequestStatus = "received"
		} else {
			prev.RequestStatus = "unknown"
		}
		_ = usage.Finish(ctx, c.Meter, prev)
		record = usage.Attempt(ctx, c.Provider(), c.ModelName)
		if c.BYOKProviderLabel != "" {
			record.EstimatedExposureUSD = llmguard.EstimateCallUSD(meta, len(body))
		}
		if err := c.Meter.RecordUsage(ctx, record); err != nil {
			record.RequestStatus = "not_sent"
			return err
		}
		record.RequestStatus = "unknown"
		return nil
	}
	res, err := c.doWithRetry(reqCtx, body, onRetry)
	if err != nil {
		if errors.Is(err, usage.ErrUserQuotaExceeded) {
			record.RequestStatus = "not_sent"
			return out, err
		}
		if llmguard.IsGuardError(err) {
			out.NotSent = true
			record.RequestStatus = "not_sent"
			return out, err
		}
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("task model request interrupted or unavailable")
	}
	record.RequestStatus = "received"
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		if c.BYOKProviderLabel != "" {
			if err := llmguard.ModelAccessError(res, c.BYOKProviderLabel); err != nil {
				return out, err
			}
		}
		if (res.StatusCode == 400 || res.StatusCode == 413) && contextview.Refusal(res.Body) {
			return out, contextview.ErrOverflow
		}
		return out, fmt.Errorf("task model returned HTTP %d", res.StatusCode)
	}

	var response struct {
		Model      string            `json:"model"`
		StopReason string            `json:"stop_reason"`
		Content    []json.RawMessage `json:"content"`
		Usage      *struct {
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response); err != nil {
		return out, errors.New("task model returned an unreadable response")
	}

	if u := response.Usage; u != nil {
		totalIn := u.Input + u.CacheRead + u.CacheWrite
		outTok := u.Output
		total := totalIn + outTok
		cached := u.CacheRead
		written := u.CacheWrite
		record.InputTokens = &totalIn
		record.OutputTokens = &outTok
		record.TotalTokens = &total
		record.CachedTokens = &cached
		record.CacheWriteTokens = &written
		out.Usage = &agent.Usage{
			InputTokens:  int32(totalIn),
			OutputTokens: int32(outTok),
			CachedTokens: int32(cached),
			TotalTokens:  int32(total),
		}
		modelName := response.Model
		if modelName == "" {
			modelName = c.ModelName
		}
		if cost, costErr := usage.AnthropicCost(modelName, u.Input, u.Output, cached, written); costErr == nil {
			record.CostUSD, record.CostStatus = &cost, "calculated"
			out.Cost, out.CostKnown = cost, true
			llmguard.RecordResponseCost(res, cost)
		}
	}

	if response.StopReason != "" && response.StopReason != "end_turn" && response.StopReason != "tool_use" {
		return out, fmt.Errorf("task model response incomplete (stop_reason=%s); no tools executed", response.StopReason)
	}

	message := &transcript.Message{Role: transcript.RoleModel}
	if c.BYOKProviderLabel != "" {
		saved, err := json.Marshal(response.Content)
		if err != nil {
			return out, errors.New("task model returned invalid content")
		}
		message.Parts = append(message.Parts, transcript.Part{Provider: &transcript.ProviderState{Format: "nori.anthropic.messages", Data: saved}})
	}
	var calls []transcript.ToolCall
	var texts []string
	for _, raw := range response.Content {
		var blk struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		if json.Unmarshal(raw, &blk) != nil {
			return out, errors.New("task model returned invalid content")
		}
		switch blk.Type {
		case "text":
			if blk.Text != "" {
				texts = append(texts, blk.Text)
			}
		case "tool_use":
			args := blk.Input
			if args == nil {
				args = map[string]any{}
			}
			call := transcript.ToolCall{ID: blk.ID, Name: blk.Name, Args: args}
			calls = append(calls, call)
			message.Parts = append(message.Parts, transcript.Part{ToolCall: &call})
		}
	}
	visibleText := strings.TrimSpace(strings.Join(texts, "\n"))
	if len(calls) == 0 {
		out.Answer = visibleText
	}
	if len(calls) == 0 && out.Answer == "" {
		return out, errors.New("task model returned no answer")
	}
	if visibleText != "" {
		message.Parts = append(message.Parts, transcript.Part{Text: visibleText})
	}
	out.Calls = calls
	out.Message = message
	return out, nil
}

func (c *Client) Inspect(ctx context.Context, req agent.InspectRequest) (out agent.Inspection, err error) {
	maxTokens := req.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	fields := map[string]any{
		"model":      c.ModelName,
		"max_tokens": maxTokens,
		"messages": []map[string]any{{
			"role": "user",
			"content": []map[string]any{
				{"type": "text", "text": req.Question},
				{
					"type": "image",
					"source": map[string]string{
						"type":       "base64",
						"media_type": req.MediaType,
						"data":       base64.StdEncoding.EncodeToString(req.Image),
					},
				},
			},
		}},
	}
	if strings.TrimSpace(req.Instructions) != "" {
		fields["system"] = req.Instructions
	}
	if c.BYOKProviderLabel != "" && c.DefaultReasoning != "" {
		fields["output_config"] = map[string]string{"effort": c.DefaultReasoning}
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return out, errors.New("inspection request could not be encoded")
	}
	meta := llmguard.CallMeta{
		UserID:          c.UserID,
		Scope:           llmguard.ScopeTask,
		Provider:        c.Provider(),
		Model:           c.ModelName,
		MaxOutputTokens: maxTokens,
		BYOK:            c.BYOKProviderLabel != "",
	}
	reqCtx := llmguard.WithMeta(ctx, meta)
	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	c.setAuthHeaders(httpReq)

	record := usage.Attempt(ctx, c.Provider(), c.ModelName)
	if c.BYOKProviderLabel != "" {
		record.EstimatedExposureUSD = llmguard.EstimateCallUSD(meta, len(body))
	}
	if err := c.Meter.RecordUsage(ctx, record); err != nil {
		return out, err
	}
	record.RequestStatus = "unknown"
	defer func() { err = errors.Join(err, usage.Finish(ctx, c.Meter, record)) }()
	out.Sent = true

	res, err := c.httpClient().Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		return out, errors.New("inspection request interrupted or unavailable")
	}
	record.RequestStatus = "received"
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		if c.BYOKProviderLabel != "" {
			if err := llmguard.ModelAccessError(res, c.BYOKProviderLabel); err != nil {
				return out, err
			}
		}
		return out, fmt.Errorf("inspection model returned HTTP %d", res.StatusCode)
	}
	var response struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage *struct {
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response); err != nil {
		return out, errors.New("inspection model returned an unreadable response")
	}
	if u := response.Usage; u != nil {
		totalIn := u.Input + u.CacheRead + u.CacheWrite
		outTok := u.Output
		total := totalIn + outTok
		cached := u.CacheRead
		written := u.CacheWrite
		record.InputTokens, record.OutputTokens, record.TotalTokens = &totalIn, &outTok, &total
		record.CachedTokens, record.CacheWriteTokens = &cached, &written
		modelName := response.Model
		if modelName == "" {
			modelName = c.ModelName
		}
		if cost, costErr := usage.AnthropicCost(modelName, u.Input, u.Output, cached, written); costErr == nil {
			record.CostUSD, record.CostStatus = &cost, "calculated"
			out.Cost, out.CostKnown = cost, true
			llmguard.RecordResponseCost(res, cost)
		}
	}
	var texts []string
	for _, blk := range response.Content {
		if blk.Type == "text" {
			texts = append(texts, blk.Text)
		}
	}
	out.Observation = strings.TrimSpace(strings.Join(texts, "\n"))
	return out, nil
}

func (c *Client) doWithRetry(ctx context.Context, body []byte, onRetry func(statusCode int) error) (*http.Response, error) {
	return httpretry.Do(ctx, httpretry.Config{
		Client:    c.httpClient(),
		BaseDelay: 500 * time.Millisecond,
		ShouldStopRetry: func(res *http.Response) bool {
			return c.BYOKProviderLabel != "" && llmguard.ModelAccessError(res, c.BYOKProviderLabel) != nil
		},
		OnRetry:   onRetry,
		LogPrefix: "anthropic task model",
	}, func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		c.setAuthHeaders(httpReq)
		return httpReq, nil
	})
}

func sanitizeAnthropicToolID(id string, mapping map[string]string, seq *int) string {
	if mapped, ok := mapping[id]; ok {
		return mapped
	}
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	clean := b.String()
	if clean == "" {
		*seq++
		clean = fmt.Sprintf("toolu_%d", *seq)
	}
	mapping[id] = clean
	return clean
}

func anthropicMessages(messages []transcript.Message, preserveState bool) ([]map[string]any, error) {
	var out []map[string]any
	appendBlock := func(role string, blk map[string]any) {
		if len(out) > 0 && out[len(out)-1]["role"] == role {
			prev := out[len(out)-1]["content"].([]map[string]any)
			out[len(out)-1]["content"] = append(prev, blk)
			return
		}
		out = append(out, map[string]any{"role": role, "content": []map[string]any{blk}})
	}

	pending := map[string][]string{}
	idMap := map[string]string{}
	seq := 0
	for i, msg := range messages {
		role := "user"
		if msg.Role == transcript.RoleModel {
			role = "assistant"
		}
		if saved := msg.Provider(); preserveState && saved != nil && saved.Format == "nori.anthropic.messages" {
			var blocks []map[string]any
			if json.Unmarshal(saved.Data, &blocks) != nil {
				return nil, errors.New("invalid saved Anthropic response")
			}
			for _, block := range blocks {
				if block["type"] == "tool_use" {
					id, _ := block["id"].(string)
					name, _ := block["name"].(string)
					if id == "" || name == "" {
						return nil, errors.New("invalid saved Anthropic tool call")
					}
					idMap[id] = id
					pending[name] = append(pending[name], id)
				}
				appendBlock("assistant", block)
			}
			continue
		}
		for j, part := range msg.Parts {
			switch {
			case part.ToolCall != nil:
				call := part.ToolCall
				id := call.ID
				if id == "" {
					id = fmt.Sprintf("call_%d_%d", i, j)
				}
				cleanID := sanitizeAnthropicToolID(id, idMap, &seq)
				pending[call.Name] = append(pending[call.Name], cleanID)
				input := call.Args
				if input == nil {
					input = map[string]any{}
				}
				appendBlock("assistant", map[string]any{
					"type":  "tool_use",
					"id":    cleanID,
					"name":  call.Name,
					"input": input,
				})
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
				cleanID := sanitizeAnthropicToolID(id, idMap, &seq)
				encoded, err := json.Marshal(result.Output)
				if err != nil {
					return nil, err
				}
				appendBlock("user", map[string]any{
					"type":        "tool_result",
					"tool_use_id": cleanID,
					"content":     string(encoded),
				})
			case part.Text != "":
				appendBlock(role, map[string]any{"type": "text", "text": part.Text})
			}
		}
	}
	if len(out) == 0 {
		appendBlock("user", map[string]any{"type": "text", "text": "Continue."})
	} else if out[0]["role"] == "assistant" {
		out = append([]map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": "[Task resumed]"}},
		}}, out...)
	}
	if !preserveState && len(out) >= 2 {
		if blocks, ok := out[len(out)-2]["content"].([]map[string]any); ok && len(blocks) > 0 {
			blocks[len(blocks)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
		}
	}
	return out, nil
}

func toolSchema(schema *transcript.Schema) map[string]any {
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
			properties[name] = toolSchema(child)
		}
		required := schema.Required
		if required == nil {
			required = []string{}
		}
		result["properties"], result["required"] = properties, required
	}
	if schema.Type == transcript.TypeArray {
		items := toolSchema(schema.Items)
		if items == nil {
			items = map[string]any{"type": "string"}
		}
		result["items"] = items
	}
	return result
}
