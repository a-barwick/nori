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
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

const (
	routerModel = settings.DefaultOpenRouterInnerModel
	directModel = settings.DefaultInnerModel
	stateFormat = "nori.responses"
)

type Client struct {
	Key                   string
	ModelName             string
	ProviderName          string
	HTTP                  *http.Client
	Guard                 *llmguard.Guard
	UserID                string
	Meter                 usage.Meter
	gatewayURL            string
	TestURL               string
	DefaultReasoning      string
	AllowGatewayReasoning bool
	Byok                  bool
	BYOKProviderLabel     string
	LocalOnly             bool
	MaxRetries            int
	RetryBaseDelay        time.Duration
	MaxRetryAfter         time.Duration
	Now                   func() time.Time
	Sleep                 func(context.Context, time.Duration) error
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
	return c.ModelName == directModel || c.ModelName == "gpt-6.1-terra"
}

func (c *Client) SupportsNativeWebSearch() bool { return c.direct() }

func (c *Client) Provider() string {
	if c.ProviderName != "" {
		return c.ProviderName
	}
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
	if c.ProviderName == llmguard.ProviderHome {
		return "nori.home.responses"
	}
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
		return
	}
	if c.Byok {
		fields["provider"] = map[string]any{"require_parameters": true}
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

type responsesAPIResponse struct {
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Model  string            `json:"model"`
	Tier   string            `json:"service_tier"`
	Usage  *responseUsage    `json:"usage"`
}

func (c *Client) buildRequestBody(req agent.Request) ([]byte, error) {
	input, err := responseInput(req.Messages, c.format())
	if err != nil {
		return nil, err
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
	if !req.SingleTurn && c.ProviderName != llmguard.ProviderHome {
		fields["include"] = []string{"reasoning.encrypted_content"}
	}
	effort := req.Reasoning
	if effort == "" {
		effort = c.DefaultReasoning
	}
	if (c.gatewayURL == "" || c.AllowGatewayReasoning) && effort != "" {
		fields["reasoning"] = map[string]string{"effort": effort}
	}
	c.requestOptions(fields)
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, errors.New("task request could not be encoded")
	}
	if err := contextview.Check(len(body)); err != nil {
		return nil, err
	}
	return body, nil
}

func (c *Client) callMeta(ctx context.Context, maxOutputTokens int) llmguard.CallMeta {
	scope := llmguard.MetaFromContext(ctx).Scope
	if scope == "" {
		scope = llmguard.ScopeTask
	}
	return llmguard.CallMeta{
		UserID:          c.UserID,
		Scope:           scope,
		Provider:        c.Provider(),
		Model:           c.ModelName,
		MaxOutputTokens: maxOutputTokens,
		BYOK:            c.Byok,
	}
}

func (c *Client) mapHTTPError(res *http.Response) error {
	if c.BYOKProviderLabel != "" {
		if err := llmguard.ModelAccessError(res, c.BYOKProviderLabel); err != nil {
			return err
		}
	}
	if (res.StatusCode == 400 || res.StatusCode == 413) && contextview.Refusal(res.Body) {
		return contextview.ErrOverflow
	}
	if c.gatewayURL != "" {
		return modelgateway.HTTPError(res.StatusCode)
	}
	return fmt.Errorf("task model returned HTTP %d", res.StatusCode)
}

func (c *Client) applyResponseUsage(res *http.Response, record *state.Usage, response responsesAPIResponse, out *agent.Response) {
	out.Cost, out.CostKnown = c.recordCost(record, response.Model, response.Tier, response.Usage)
	if c.ProviderName == llmguard.ProviderHome {
		out.CostLocal = true
		llmguard.RecordResponseCost(res, 0)
	}
	if c.LocalOnly && !out.CostKnown {
		record.CostStatus = "local"
		out.CostLocal = true
	}
	if u := response.Usage; u != nil && u.Input != nil && u.Output != nil && u.Total != nil {
		nonReasoning := max(*u.Output-u.OutputDetails.Reasoning, 0)
		out.Usage = &agent.Usage{
			InputTokens: int32(*u.Input), OutputTokens: int32(nonReasoning),
			ReasoningTokens: int32(u.OutputDetails.Reasoning), CachedTokens: int32(u.InputDetails.Cached),
			CacheWriteTokens: int32(u.InputDetails.Write), TotalTokens: int32(*u.Total),
		}
	}
}

type decodedOutputItems struct {
	message     *transcript.Message
	calls       []transcript.ToolCall
	text        []string
	webSearches []agent.WebSearchCall
	citations   []agent.URLCitation
}

func (c *Client) decodeOutputItems(output []json.RawMessage, reqTools []transcript.Tool) (decodedOutputItems, error) {
	saved, err := json.Marshal(output)
	if err != nil {
		return decodedOutputItems{}, err
	}
	decoded := decodedOutputItems{
		message: &transcript.Message{
			Role:  transcript.RoleModel,
			Parts: []transcript.Part{{Provider: &transcript.ProviderState{Format: c.format(), Data: saved}}},
		},
		calls:       []transcript.ToolCall{},
		text:        []string{},
		webSearches: []agent.WebSearchCall{},
		citations:   []agent.URLCitation{},
	}
	seenCitations := map[string]bool{}
	for _, raw := range output {
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
			return decodedOutputItems{}, errors.New("invalid task response item")
		}
		switch item.Type {
		case "function_call":
			call := transcript.ToolCall{ID: item.CallID, Name: item.Name}
			if call.ID == "" || json.Unmarshal([]byte(item.Arguments), &call.Args) != nil || call.Args == nil {
				return decodedOutputItems{}, errors.New("invalid task tool call")
			}
			if c.gatewayURL != "" && !slices.ContainsFunc(reqTools, func(tool transcript.Tool) bool { return tool.Name == call.Name }) {
				return decodedOutputItems{}, errors.New("model gateway returned a function call for a tool absent from the request")
			}
			decoded.calls = append(decoded.calls, call)
			decoded.message.Parts = append(decoded.message.Parts, transcript.Part{ToolCall: &call})
		case "web_search_call":
			if c.gatewayURL != "" {
				return decodedOutputItems{}, errors.New("model gateway returned an unsupported Responses output item. Check function calling compatibility")
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
			decoded.webSearches = append(decoded.webSearches, ws)
		case "message":
			if c.gatewayURL != "" && item.Role != "assistant" {
				return decodedOutputItems{}, errors.New("model gateway returned a Responses message without the assistant role")
			}
			for _, content := range item.Content {
				if content.Type == "output_text" {
					decoded.text = append(decoded.text, content.Text)
					for _, ann := range content.Annotations {
						if ann.Type == "url_citation" {
							if u := strings.TrimSpace(ann.URL); u != "" && !seenCitations[u] {
								seenCitations[u] = true
								decoded.citations = append(decoded.citations, agent.URLCitation{URL: u, Title: strings.TrimSpace(ann.Title)})
							}
						}
					}
				} else if c.gatewayURL != "" {
					return decodedOutputItems{}, errors.New("model gateway returned unsupported Responses message content. Check model output or access policy")
				}
			}
		case "reasoning":
		default:
			if c.gatewayURL != "" {
				return decodedOutputItems{}, errors.New("model gateway returned an unsupported Responses output item. Check function calling compatibility")
			}
		}
	}
	return decoded, nil
}

func (c *Client) Respond(ctx context.Context, req agent.Request) (out agent.Response, err error) {
	out.NotSent = true
	if c.ProviderName == llmguard.ProviderHome {
		out.CostKnown, out.CostLocal = true, true
	}
	if !c.validModel() {
		return out, errors.New("task model must be the selected GPT-6.1 Terra")
	}
	body, err := c.buildRequestBody(req)
	if err != nil {
		return out, err
	}
	meta := c.callMeta(ctx, req.MaxOutputTokens)
	reqCtx := llmguard.WithMeta(ctx, meta)
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
	if c.ProviderName == llmguard.ProviderHome {
		c.recordCost(&record, "", "", nil)
	}
	if c.Byok {
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
		if c.Byok {
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
		if c.ProviderName == llmguard.ProviderHome {
			return out, err
		}
		return out, errors.New("task model request interrupted or unavailable")
	}
	record.RequestStatus = "received"
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return out, c.mapHTTPError(res)
	}
	var response responsesAPIResponse
	if json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response) != nil {
		return out, errors.New("task model returned an unreadable response")
	}
	c.applyResponseUsage(res, &record, response, &out)
	if response.Status != "completed" {
		if c.gatewayURL != "" {
			return out, errors.New("model gateway must return a completed Responses API response before tools can run")
		}
		return out, errors.New("task model response incomplete; no tools executed")
	}
	decoded, err := c.decodeOutputItems(response.Output, req.Tools)
	if err != nil {
		return out, err
	}
	if len(decoded.webSearches) > 0 {
		out.Cost = c.addWebSearchCost(&record, out.Cost, out.CostKnown, decoded.webSearches)
		out.WebSearches = decoded.webSearches
	}
	if out.CostKnown && out.Cost > 0 {
		llmguard.RecordResponseCost(res, out.Cost)
	}
	if len(decoded.citations) > 0 {
		out.Citations = decoded.citations
	}
	visibleText := strings.TrimSpace(strings.Join(decoded.text, "\n"))
	if len(decoded.calls) == 0 {
		out.Answer = visibleText
	}
	if len(decoded.calls) == 0 && out.Answer == "" {
		return out, errors.New("task model returned no answer")
	}
	out.Calls = decoded.calls
	if visibleText != "" {
		decoded.message.Parts = append(decoded.message.Parts, transcript.Part{Text: visibleText})
	}
	out.Message = decoded.message
	return out, nil
}

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
