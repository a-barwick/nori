package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

type stubRT func(*http.Request) (*http.Response, error)

func (f stubRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGateway_AuthRoutingCostInjectionAndKillSwitch(t *testing.T) {
	var seenTargets []string
	var seenAuth []string

	srv, err := New(Config{
		Token:         "ephemeral-tok-123",
		OpenAIKey:     "sk-test-openai",
		OpenRouterKey: "sk-test-openrouter",
		AnthropicKey:  "sk-test-anthropic",
		MaxUSD:        0.05,
		MaxCalls:      2,
		Transport: stubRT(func(req *http.Request) (*http.Response, error) {
			seenTargets = append(seenTargets, req.URL.String())
			seenAuth = append(seenAuth, req.Header.Get("Authorization")+req.Header.Get("x-api-key"))
			if strings.Contains(req.URL.Host, "openrouter") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"status":"completed","output":[],"usage":{"input_tokens":1000,"output_tokens":100,"total_tokens":1100,"cost":0.012}}`)),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl-1","model":"gpt-4.1-mini","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500}}`)),
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	handler := srv.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4.1-mini"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer ephemeral-tok-123")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("call 1 expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body1 map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body1); err != nil {
		t.Fatalf("unmarshal call 1: %v", err)
	}
	usage1 := body1["usage"].(map[string]any)
	if cost, ok := usage1["cost"].(float64); !ok || cost <= 0 {
		t.Fatalf("expected injected usage.cost > 0, got %+v", usage1)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"openai/gpt-6.1-terra","input":[]}`))
	req.Header.Set("Authorization", "Bearer ephemeral-tok-123")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("call 2 expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(seenTargets) != 2 || !strings.Contains(seenTargets[0], "api.openai.com") || !strings.Contains(seenTargets[1], "openrouter.ai") {
		t.Fatalf("unexpected upstream targets: %v", seenTargets)
	}
	if seenAuth[0] != "Bearer sk-test-openai" || seenAuth[1] != "Bearer sk-test-openrouter" {
		t.Fatalf("unexpected upstream auth headers: %v", seenAuth)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-4.1-mini","messages":[{"role":"user","content":"third"}]}`))
	req.Header.Set("Authorization", "Bearer ephemeral-tok-123")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("call 3 expected 429, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "local_budget_exceeded") {
		t.Fatalf("expected local_budget_exceeded error code, got %s", rec.Body.String())
	}
	if len(seenTargets) != 2 {
		t.Fatalf("expected call 3 blocked before upstream, got %d upstream calls", len(seenTargets))
	}

	st := srv.Stats()
	if !st.BudgetExceeded || st.TotalAdmitted != 2 || st.BlockedCallLimit != 1 || st.RecordedActualUSD <= 0 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestGateway_LunaCostCalculation(t *testing.T) {
	srv, err := New(Config{
		Token:     "tok",
		OpenAIKey: "sk-test",
		Transport: stubRT(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"chatcmpl-luna","model":"gpt-6-luna","service_tier":"default","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000}}`,
				)),
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-6-luna","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer tok")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	u := body["usage"].(map[string]any)
	cost, ok := u["cost"].(float64)
	if !ok || cost < 0.949 || cost > 0.951 {
		t.Fatalf("expected gpt-6-luna cost ~0.95, got %v (usage=%+v)", cost, u)
	}
}

func TestStreamUsageAcrossLongAndPartialStreams(t *testing.T) {
	anthropic := streamUsage{provider: "anthropic", model: "claude-sonnet-4-6"}
	anthropic.write([]byte("data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":20}}}\r\n"))
	content := []byte("data: " + strings.Repeat("x", 180<<10) + "\n")
	for i := 0; i < 4; i++ {
		for chunk := range slices.Chunk(content, 4096) {
			anthropic.write(chunk)
		}
	}
	anthropic.write([]byte("data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":"))
	anthropic.write([]byte("30}}\n\n"))
	if _, _, _, ok := anthropic.cost(); ok {
		t.Fatal("incomplete Anthropic stream reported final cost")
	}
	anthropic.write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	if cost, in, out, ok := anthropic.cost(); !ok || cost <= 0 || in != 30 || out != 30 {
		t.Fatalf("Anthropic cost = %v, %d, %d, %v", cost, in, out, ok)
	}

	openai := streamUsage{provider: "openai", model: "gpt-4.1"}
	openai.write([]byte("data: {\"usage\":{}}\n"))
	if _, _, _, ok := openai.cost(); ok {
		t.Fatal("empty usage reported final cost")
	}
	for i := 0; i < 4; i++ {
		openai.write(content)
	}
	openai.write([]byte("data: {\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":30}}\n\ndata: [DONE]\n"))
	if cost, in, out, ok := openai.cost(); !ok || cost <= 0 || in != 20 || out != 30 {
		t.Fatalf("OpenAI cost = %v, %d, %d, %v", cost, in, out, ok)
	}
}
