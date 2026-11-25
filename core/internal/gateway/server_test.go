package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

	// 1. Reject missing/wrong token.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4.1-mini"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	// 2. Call 1: OpenAI chat completions -> injects calculated cost into usage.cost.
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

	// 3. Call 2: OpenRouter responses (model has "/") -> routes to openrouter with OpenRouter key.
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

	// 4. Call 3: Exceeds MaxCalls=2 -> immediately killed with HTTP 429 local_budget_exceeded!
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

	// 5. Stats endpoint reports budget_exceeded and cumulative spend.
	st := srv.Stats()
	if !st.BudgetExceeded || st.TotalAdmitted != 2 || st.BlockedCallLimit != 1 || st.RecordedActualUSD <= 0 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}
