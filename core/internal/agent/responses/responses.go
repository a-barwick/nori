package responses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"nori/internal/agent"
	"nori/internal/contextview"
	"nori/internal/llmguard"
	"nori/internal/modelgateway"
	"nori/internal/settings"
	"nori/internal/transcript"
	"nori/internal/usage"
)

const (
	// Authorized models for inner tasks (OpenRouter vs direct OpenAI).
	routerModel = settings.DefaultOpenRouterInnerModel
	directModel = settings.DefaultInnerModel
	// stateFormat marks transcript.ProviderState written by this adapter. Its data is the
	// JSON array of output items from a response, which keeps encrypted reasoning and call
	// IDs exactly as the provider returned them.
	stateFormat = "nori.responses"
)

type Client struct {
	Key          string
	ModelName    string
	ProviderName string
	HTTP         *http.Client
	Guard        *llmguard.Guard
	UserID       string
	Meter        usage.Meter
	gatewayURL   string
	// TestURL replaces the provider endpoint with a local stub (settings.Models.TestURL).
	TestURL string
	// DefaultReasoning sets the reasoning effort for requests that do not specify one.
	DefaultReasoning string
	// Optional retry configuration hooks for transient provider errors (429, 5xx, network).
	MaxRetries     int
	RetryBaseDelay time.Duration
	MaxRetryAfter  time.Duration
	Now            func() time.Time
	Sleep          func(context.Context, time.Duration) error
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

func New(key, model string, meter usage.Meter) *Client {
	g := llmguard.New(llmguard.DefaultConfig())
	return &Client{
		Key:       key,
		Meter:     meter,
		ModelName: model,
		Guard:     g,
		HTTP:      g.Client(2*time.Minute, llmguard.CallMeta{Scope: llmguard.ScopeTask, Model: model}),
	}
}

func NewGateway(baseURL, key, model string, meter usage.Meter) *Client {
	client := New(key, model, meter)
	client.gatewayURL = strings.TrimRight(baseURL, "/") + "/responses"
	return client
}

func (c *Client) direct() bool {
	if c.gatewayURL != "" {
		return false
	}
	if c.ProviderName != "" {
		return c.ProviderName == "openai"
	}
	return c.ModelName == directModel
}

// SupportsNativeWebSearch reports whether this client executes web search natively
// through OpenAI's built-in Responses API web_search tool.
func (c *Client) SupportsNativeWebSearch() bool { return c.direct() }

func (c *Client) Provider() string {
	if c.gatewayURL != "" {
		return modelgateway.Provider
	}
	if c.direct() {
		return "openai"
	}
	return "openrouter"
}

func (c *Client) Name() string { return c.ModelName }

func (c *Client) endpoint() string {
	if c.gatewayURL != "" {
		return c.gatewayURL
	}
	if c.TestURL != "" {
		return strings.TrimRight(c.TestURL, "/") + "/v1/responses"
	}
	if c.direct() {
		return "https://api.openai.com/v1/responses"
	}
	return "https://openrouter.ai/api/v1/responses"
}

func (c *Client) format() string {
	if c.gatewayURL != "" {
		return "nori.gateway.responses"
	}
	if c.direct() {
		return "nori.openai.responses"
	}
	return stateFormat
}

func (c *Client) requestOptions(fields map[string]any) {
	if c.gatewayURL != "" {
		return
	}
	if c.direct() {
		fields["service_tier"] = "default"
		// Do NOT send prompt_cache_options:{"mode":"explicit"} without explicit cache
		// breakpoints; omitting it enables OpenAI's automatic prefix prompt caching
		// across instructions, tools, and conversation prefix.
		return
	}
	fields["provider"] = map[string]any{"max_price": map[string]float64{"prompt": 1, "completion": 5}, "require_parameters": true}
}

func (c *Client) validModel() bool {
	if c.gatewayURL != "" || c.direct() || c.ModelName == routerModel {
		return strings.TrimSpace(c.ModelName) != ""
	}
	return c.ProviderName != "" && strings.TrimSpace(c.ModelName) != ""
}

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
	nativeWebSearch := req.WebSearch && c.SupportsNativeWebSearch()
	for _, tool := range req.Tools {
		if nativeWebSearch && tool.Name == "web_search" {
			continue
		}
		if tool.Schema != nil {
			tools = append(tools, map[string]any{
				"type": "function", "name": tool.Name, "description": tool.Description, "strict": false, "parameters": tool.Schema,
			})
			continue
		}
		parameters := responseSchema(tool.Parameters)
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
		}
		tools = append(tools, map[string]any{
			"type": "function", "name": tool.Name, "description": tool.Description, "strict": true, "parameters": parameters,
		})
	}
	if nativeWebSearch {
		tools = append(tools, map[string]any{"type": "web_search"})
	}
	fields := map[string]any{
		"model": c.ModelName, "input": input, "instructions": req.Instructions,
		"tools": tools, "max_output_tokens": req.MaxOutputTokens, "store": false,
	}
	if !req.SingleTurn {
		fields["include"] = []string{"reasoning.encrypted_content"}
	}
	effort := req.Reasoning
	if effort == "" {
		effort = c.DefaultReasoning
	}
	if c.gatewayURL == "" && effort != "" {
		fields["reasoning"] = map[string]string{"effort": effort}
	}
	c.requestOptions(fields)
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
	reqCtx := llmguard.WithMeta(ctx, llmguard.CallMeta{
		UserID:          c.UserID,
		Scope:           scope,
		Provider:        c.Provider(),
		Model:           c.ModelName,
		MaxOutputTokens: req.MaxOutputTokens,
	})
	buildReq := func() (*http.Request, error) {
		httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.Key)
		httpReq.Header.Set("Content-Type", "application/json")
		return httpReq, nil
	}
	if _, err := buildReq(); err != nil {
		return out, err
	}
	record := usage.Attempt(ctx, c.Provider(), c.ModelName)
	if err := c.Meter.RecordUsage(ctx, record); err != nil {
		return out, err
	}
	record.RequestStatus = "unknown"
	defer func() { err = errors.Join(err, usage.Finish(ctx, c.Meter, record)) }()
	// Once handed to HTTP, a transport failure cannot prove it was not billed.
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
		if err := c.Meter.RecordUsage(ctx, record); err != nil {
			record.RequestStatus = "not_sent"
			return err
		}
		record.RequestStatus = "unknown"
		return nil
	}
	res, err := c.doWithRetry(reqCtx, buildReq, onRetry)
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
		if (res.StatusCode == 400 || res.StatusCode == 413) && contextview.Refusal(res.Body) {
			return out, contextview.ErrOverflow
		}
		if c.gatewayURL != "" {
			return out, modelgateway.HTTPError(res.StatusCode)
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
		nonReasoning := max(*u.Output-u.OutputDetails.Reasoning, 0)
		out.Usage = &agent.Usage{
			InputTokens: int32(*u.Input), OutputTokens: int32(nonReasoning),
			ReasoningTokens: int32(u.OutputDetails.Reasoning), CachedTokens: int32(u.InputDetails.Cached), TotalTokens: int32(*u.Total),
		}
	}
	if response.Status != "completed" {
		if c.gatewayURL != "" {
			return out, errors.New("model gateway must return a completed Responses API response before tools can run")
		}
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
	calls := []transcript.ToolCall{}
	text := []string{}
	webSearches := []agent.WebSearchCall{}
	citations := []agent.URLCitation{}
	seenCitations := map[string]bool{}
	for _, raw := range response.Output {
		var item struct {
			ID        string `json:"id"`
			Role      string `json:"role"`
			Type      string `json:"type"`
			Status    string `json:"status"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Action    *struct {
				Type    string   `json:"type"`
				Query   string   `json:"query"`
				Queries []string `json:"queries"`
				URL     string   `json:"url"`
				Pattern string   `json:"pattern"`
				Sources []struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				} `json:"sources"`
			} `json:"action"`
			Content []struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Annotations []struct {
					Type  string `json:"type"`
					URL   string `json:"url"`
					Title string `json:"title"`
				} `json:"annotations"`
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
			if c.gatewayURL != "" && !slices.ContainsFunc(req.Tools, func(tool transcript.Tool) bool { return tool.Name == call.Name }) {
				return out, errors.New("model gateway returned a function call for a tool absent from the request")
			}
			calls = append(calls, call)
			message.Parts = append(message.Parts, transcript.Part{ToolCall: &call})
		case "web_search_call":
			if c.gatewayURL != "" {
				return out, errors.New("model gateway returned an unsupported Responses output item. Check function calling compatibility")
			}
			ws := agent.WebSearchCall{
				ID:     strings.TrimSpace(item.ID),
				Status: strings.TrimSpace(item.Status),
			}
			if item.Action != nil {
				ws.Action = strings.TrimSpace(item.Action.Type)
				ws.Query = strings.TrimSpace(item.Action.Query)
				for _, q := range item.Action.Queries {
					if q = strings.TrimSpace(q); q != "" {
						ws.Queries = append(ws.Queries, q)
					}
				}
				ws.URL = strings.TrimSpace(item.Action.URL)
				ws.Pattern = strings.TrimSpace(item.Action.Pattern)
				for _, src := range item.Action.Sources {
					if u := strings.TrimSpace(src.URL); u != "" {
						ws.Sources = append(ws.Sources, u)
					}
				}
			}
			webSearches = append(webSearches, ws)
		case "message":
			if c.gatewayURL != "" && item.Role != "assistant" {
				return out, errors.New("model gateway returned a Responses message without the assistant role")
			}
			for _, content := range item.Content {
				if content.Type == "output_text" {
					text = append(text, content.Text)
					for _, ann := range content.Annotations {
						if ann.Type == "url_citation" {
							if u := strings.TrimSpace(ann.URL); u != "" && !seenCitations[u] {
								seenCitations[u] = true
								citations = append(citations, agent.URLCitation{URL: u, Title: strings.TrimSpace(ann.Title)})
							}
						}
					}
				} else if c.gatewayURL != "" {
					return out, errors.New("model gateway returned unsupported Responses message content. Check model output or access policy")
				}
			}
		case "reasoning":
			// Kept in provider state for stateless continuation.
		default:
			if c.gatewayURL != "" {
				return out, errors.New("model gateway returned an unsupported Responses output item. Check function calling compatibility")
			}
		}
	}
	if len(webSearches) > 0 {
		out.Cost = c.addWebSearchCost(&record, out.Cost, out.CostKnown, webSearches)
		out.WebSearches = webSearches
	}
	if out.CostKnown && out.Cost > 0 {
		c.guard().RecordActualCost(out.Cost)
	}
	if len(citations) > 0 {
		out.Citations = citations
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
	out.Message = message
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
