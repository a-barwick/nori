package responses_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"

	"nori/internal/agent"
	"nori/internal/agent/responses"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type captureMeter struct {
	records []state.Usage
}

func (m *captureMeter) RecordUsage(_ context.Context, u state.Usage) error {
	m.records = append(m.records, u)
	return nil
}

func jsonHTTPResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func TestResponsesNativeWebSearchRequestAndParsing(t *testing.T) {
	ctx := context.Background()
	meter := &captureMeter{}
	client := responses.New("test-key", settings.DefaultInnerModel, meter)
	if !client.SupportsNativeWebSearch() {
		t.Fatalf("direct OpenAI client should support native web_search")
	}

	var capturedBody map[string]any
	client.HTTP = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read request body: %v", err)
			}
			if err := json.Unmarshal(raw, &capturedBody); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			payload := `{
				"status": "completed",
				"model": "gpt-6.1-terra-2026-04-01",
				"service_tier": "default",
				"usage": {
					"input_tokens": 10000,
					"output_tokens": 500,
					"total_tokens": 10500,
					"input_tokens_details": {"cached_tokens": 2000},
					"output_tokens_details": {"reasoning_tokens": 100}
				},
				"output": [
					{
						"id": "ws_search_1",
						"type": "web_search_call",
						"status": "completed",
						"action": {
							"type": "search",
							"query": "San Francisco weather Thursday forecast",
							"sources": [
								{"type": "url", "url": "https://weather.example.com/sf"}
							]
						}
					},
					{
						"id": "ws_open_2",
						"type": "web_search_call",
						"status": "completed",
						"action": {
							"type": "open_page",
							"url": "https://weather.example.com/sf"
						}
					},
					{
						"id": "ws_failed_3",
						"type": "web_search_call",
						"status": "failed",
						"action": {
							"type": "search",
							"query": "broken station"
						}
					},
					{
						"type": "message",
						"role": "assistant",
						"content": [
							{
								"type": "output_text",
								"text": "Thursday will be sunny with a high of 68F.",
								"annotations": [
									{
										"type": "url_citation",
										"url": "https://weather.example.com/sf",
										"title": "SF Weather Forecast"
									},
									{
										"type": "url_citation",
										"url": "https://weather.example.com/sf",
										"title": "Duplicate URL"
									}
								]
							}
						]
					},
					{
						"type": "function_call",
						"call_id": "call_complete_1",
						"name": "complete",
						"arguments": "{\"answer\":\"Thursday in SF will be sunny, high 68F (SF Weather Forecast).\",\"evidence\":[],\"unmet\":[]}"
					}
				]
			}`
			return jsonHTTPResponse(payload), nil
		}),
	}

	resp, err := client.Respond(ctx, agent.Request{
		Instructions: "test instructions",
		Messages:     []transcript.Message{transcript.UserText("What is the weather in SF Thursday?")},
		Tools: []transcript.Tool{
			{Name: "complete", Description: "finish"},
			{Name: "web_search", Description: "should be omitted when native web_search is enabled"},
		},
		MaxOutputTokens: 1024,
		WebSearch:       true,
	})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}

	// Verify request tools include {"type":"web_search"} and omit function web_search.
	rawTools, ok := capturedBody["tools"].([]any)
	if !ok {
		t.Fatalf("expected tools array in request, got %#v", capturedBody["tools"])
	}
	var sawNativeSearch, sawFunctionSearch, sawComplete bool
	for _, entry := range rawTools {
		toolMap, _ := entry.(map[string]any)
		switch toolMap["type"] {
		case "web_search":
			sawNativeSearch = true
		case "function":
			if toolMap["name"] == "web_search" {
				sawFunctionSearch = true
			}
			if toolMap["name"] == "complete" {
				sawComplete = true
			}
		}
	}
	if !sawNativeSearch {
		t.Fatalf("expected native {\"type\":\"web_search\"} tool in request, got %#v", rawTools)
	}
	if sawFunctionSearch {
		t.Fatalf("did not expect custom function web_search tool when native web_search is enabled: %#v", rawTools)
	}
	if !sawComplete {
		t.Fatalf("expected function tool complete to remain in request: %#v", rawTools)
	}

	// Verify parsed web_search_call items.
	if len(resp.WebSearches) != 3 {
		t.Fatalf("len(resp.WebSearches) = %d, want 3: %+v", len(resp.WebSearches), resp.WebSearches)
	}
	if resp.WebSearches[0].ID != "ws_search_1" || resp.WebSearches[0].Query != "San Francisco weather Thursday forecast" || len(resp.WebSearches[0].Sources) != 1 {
		t.Fatalf("unexpected first web_search_call: %+v", resp.WebSearches[0])
	}
	if resp.WebSearches[1].Action != "open_page" || resp.WebSearches[1].URL != "https://weather.example.com/sf" {
		t.Fatalf("unexpected second web_search_call: %+v", resp.WebSearches[1])
	}
	if resp.WebSearches[2].Status != "failed" || resp.WebSearches[2].Query != "broken station" {
		t.Fatalf("unexpected third web_search_call: %+v", resp.WebSearches[2])
	}

	// Verify deduplicated citations.
	if len(resp.Citations) != 1 || resp.Citations[0].URL != "https://weather.example.com/sf" || resp.Citations[0].Title != "SF Weather Forecast" {
		t.Fatalf("unexpected citations: %+v", resp.Citations)
	}

	// Verify complete function call still parsed.
	if len(resp.Calls) != 1 || resp.Calls[0].Name != "complete" {
		t.Fatalf("unexpected Calls: %+v", resp.Calls)
	}

	// Verify cost = TerraCost + 1 billed search call (open_page and failed search excluded).
	inTok, outTok, totTok := int64(10000), int64(500), int64(10500)
	tokenCost, err := usage.TerraCost("gpt-6.1-terra-2026-04-01", "default", &inTok, &outTok, &totTok, 2000)
	if err != nil {
		t.Fatalf("TerraCost: %v", err)
	}
	wantCost := tokenCost + usage.OpenAIWebSearchCallCostUSD
	if !resp.CostKnown || math.Abs(resp.Cost-wantCost) > 1e-12 {
		t.Fatalf("resp.Cost = (known=%v, %.12f), want %.12f", resp.CostKnown, resp.Cost, wantCost)
	}
	lastUsage := meter.records[len(meter.records)-1]
	if lastUsage.CostUSD == nil || math.Abs(*lastUsage.CostUSD-wantCost) > 1e-12 {
		t.Fatalf("recorded usage CostUSD = %v, want %.12f", lastUsage.CostUSD, wantCost)
	}

	// Verify ProviderState retains raw web_search_call items for replay.
	if resp.Message == nil || resp.Message.Provider() == nil {
		t.Fatalf("expected ProviderState on response message")
	}
	if !strings.Contains(string(resp.Message.Provider().Data), `"web_search_call"`) {
		t.Fatalf("expected ProviderState to preserve web_search_call items, got %s", string(resp.Message.Provider().Data))
	}
}

func TestResponsesWebSearchDisabledForCompactionAndNonDirectClients(t *testing.T) {
	ctx := context.Background()

	// 1. Direct client with req.WebSearch == false must NOT send web_search.
	direct := responses.New("test-key", settings.DefaultInnerModel, &captureMeter{})
	var directTools []any
	direct.HTTP = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			directTools, _ = body["tools"].([]any)
			return jsonHTTPResponse(`{"status":"completed","model":"gpt-6.1-terra","service_tier":"default","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"summary"}]}]}`), nil
		}),
	}
	if _, err := direct.Respond(ctx, agent.Request{
		Instructions:    "summarize",
		Messages:        []transcript.Message{transcript.UserText("hi")},
		MaxOutputTokens: 128,
		WebSearch:       false,
	}); err != nil {
		t.Fatalf("direct Respond(WebSearch=false): %v", err)
	}
	if len(directTools) != 0 {
		t.Fatalf("expected no tools when WebSearch=false, got %#v", directTools)
	}

	// 2. Gateway and OpenRouter clients report SupportsNativeWebSearch == false and keep function web_search.
	gateway := responses.NewGateway("https://gateway.local", "gw-key", "custom/model", &captureMeter{})
	if gateway.SupportsNativeWebSearch() {
		t.Fatalf("gateway client should not support native web_search")
	}
	openrouter := responses.New("or-key", settings.DefaultOpenRouterInnerModel, &captureMeter{})
	if openrouter.SupportsNativeWebSearch() {
		t.Fatalf("openrouter client should not support native web_search")
	}

	var gwTools []any
	gateway.HTTP = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			gwTools, _ = body["tools"].([]any)
			return jsonHTTPResponse(`{"status":"completed","model":"custom/model","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"cost":0.001},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`), nil
		}),
	}
	if _, err := gateway.Respond(ctx, agent.Request{
		Instructions:    "test",
		Messages:        []transcript.Message{transcript.UserText("hi")},
		Tools:           []transcript.Tool{{Name: "web_search", Description: "brave fallback"}},
		MaxOutputTokens: 128,
		WebSearch:       true,
	}); err != nil {
		t.Fatalf("gateway Respond: %v", err)
	}
	if len(gwTools) != 1 {
		t.Fatalf("expected 1 function tool on gateway, got %#v", gwTools)
	}
	toolMap, _ := gwTools[0].(map[string]any)
	if toolMap["type"] != "function" || toolMap["name"] != "web_search" {
		t.Fatalf("expected gateway to retain function web_search, got %#v", toolMap)
	}
}

func TestAgentDecideWiresNativeWebSearchWithoutBraveKey(t *testing.T) {
	ctx := context.Background()
	client := responses.New("test-key", settings.DefaultInnerModel, &captureMeter{})

	var captured map[string]any
	client.HTTP = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			_ = json.NewDecoder(r.Body).Decode(&captured)
			return jsonHTTPResponse(`{
				"status": "completed",
				"model": "gpt-6.1-terra",
				"service_tier": "default",
				"usage": {"input_tokens": 100, "output_tokens": 20, "total_tokens": 120},
				"output": [
					{"type": "function_call", "call_id": "c1", "name": "complete", "arguments": "{\"answer\":\"done\",\"evidence\":[],\"unmet\":[]}"}
				]
			}`), nil
		}),
	}

	svc := agent.New(nil, client)
	// Intentionally leave svc.Capabilities.SearchKey empty.
	if _, err := svc.Decide(ctx, []transcript.Message{transcript.UserText("Check the weather")}, true, true); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	instructions, _ := captured["instructions"].(string)
	if strings.Contains(instructions, "no API key on this worker") {
		t.Fatalf("expected Web search capability to be available without Brave key on native OpenAI client, got instructions: %s", instructions)
	}
	rawTools, _ := captured["tools"].([]any)
	var sawNative, sawFunc bool
	for _, entry := range rawTools {
		m, _ := entry.(map[string]any)
		if m["type"] == "web_search" {
			sawNative = true
		}
		if m["type"] == "function" && m["name"] == "web_search" {
			sawFunc = true
		}
	}
	if !sawNative || sawFunc {
		t.Fatalf("expected native web_search=%v and function web_search=%v (want true, false); tools=%#v", sawNative, sawFunc, rawTools)
	}
}
