package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestGatewayAnthropicModelUsesBearerAndMessagesPath(t *testing.T) {
	var gotURL, gotAuth, gotAPIKey, gotVersion, gotModel, gotEffort string
	m := NewGatewayAnthropicModel("https://ai-gateway.vercel.sh/v1", "vck_test_gateway_key", "anthropic/claude-opus-5.5")
	m.ReasoningEffort = "low"
	m.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		var payload struct {
			Model        string `json:"model"`
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotModel = payload.Model
		gotEffort = payload.OutputConfig.Effort

		respJSON := `{
			"id":"msg_1",
			"model":"claude-opus-5.5",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"Hello from gateway!"}],
			"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":50,"cache_creation_input_tokens":10}
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(respJSON)),
		}, nil
	})}

	reply, err := m.Respond(context.Background(), ReplyRequest{
		Messages:   []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)},
		NoDispatch: true,
	})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if gotURL != "https://ai-gateway.vercel.sh/v1/messages" {
		t.Fatalf("url=%q want https://ai-gateway.vercel.sh/v1/messages", gotURL)
	}
	if gotAuth != "Bearer vck_test_gateway_key" {
		t.Fatalf("Authorization=%q want Bearer token", gotAuth)
	}
	if gotAPIKey != "" {
		t.Fatalf("expected x-api-key to be omitted in gateway mode, got %q", gotAPIKey)
	}
	if gotVersion != anthropicAPIVersion {
		t.Fatalf("anthropic-version=%q want %q", gotVersion, anthropicAPIVersion)
	}
	if gotModel != "anthropic/claude-opus-5.5" || gotEffort != "low" {
		t.Fatalf("model=%q effort=%q", gotModel, gotEffort)
	}
	if reply.ServedModel != "claude-opus-5.5" {
		t.Fatalf("ServedModel=%q want claude-opus-5.5", reply.ServedModel)
	}
	if reply.Usage.CostUSD == nil || reply.Usage.CostStatus != "calculated" {
		t.Fatalf("expected calculated cost, got %+v", reply.Usage)
	}
}

func TestGatewayAnthropicModelEmitsModelCallCompletedWithCacheMetrics(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	m := NewGatewayAnthropicModel("https://ai-gateway.vercel.sh/v1", "vck_test_gateway_key", "anthropic/claude-opus-5.5")
	m.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		respJSON := `{
			"id":"msg_cache_1",
			"model":"claude-opus-5.5",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"Cached reply"}],
			"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":50,"cache_creation_input_tokens":10}
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(respJSON)),
		}, nil
	})}

	if _, err := m.Respond(context.Background(), ReplyRequest{
		Messages:   []json.RawMessage{json.RawMessage(`{"role":"user","content":"hi"}`)},
		NoDispatch: true,
	}); err != nil {
		t.Fatalf("Respond: %v", err)
	}

	var completed map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) == nil && entry["msg"] == "model.call.completed" {
			completed = entry
			break
		}
	}
	if completed == nil {
		t.Fatalf("expected model.call.completed log event, got: %s", buf.String())
	}
	if completed["gen_ai.provider.name"] != "gateway" || completed["gen_ai.request.model"] != "anthropic/claude-opus-5.5" {
		t.Fatalf("unexpected identity fields: %+v", completed)
	}
	if completed["gen_ai.usage.input_tokens"] != float64(160) ||
		completed["gen_ai.usage.cache_read.input_tokens"] != float64(50) ||
		completed["gen_ai.usage.cache_write.input_tokens"] != float64(10) ||
		completed["gen_ai.usage.output_tokens"] != float64(20) {
		t.Fatalf("unexpected token fields: %+v", completed)
	}
	if completed["nori.model.cache_hit_ratio"] != float64(50)/float64(160) {
		t.Fatalf("cache_hit_ratio = %v, want %v", completed["nori.model.cache_hit_ratio"], float64(50)/float64(160))
	}
	if cost, ok := completed["nori.cost.usd"].(float64); !ok || cost <= 0 {
		t.Fatalf("expected positive nori.cost.usd, got %v", completed["nori.cost.usd"])
	}
}
