package conversation

import (
	"context"
	"encoding/json"
	"io"
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
