package conversation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"nori/internal/llmguard"
	"nori/internal/state"
)

func TestAnthropicModel_StructuredResponseAndGuardAccounting(t *testing.T) {
	var seenHeaders http.Header
	var seenBody map[string]any
	g := llmguard.New(llmguard.DefaultConfig())
	model := NewAnthropicModel("sk-ant-test", "claude-sonnet-4-5")
	model.Guard = g
	model.Client = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seenHeaders = req.Header.Clone()
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &seenBody)
		respJSON := `{
			"id": "msg_123",
			"model": "claude-sonnet-4-5",
			"stop_reason": "tool_use",
			"content": [{
				"type": "tool_use",
				"id": "toolu_1",
				"name": "nori_response",
				"input": {"text": "Hello from Claude!", "actions": []}
			}],
			"usage": {
				"input_tokens": 1000,
				"output_tokens": 200,
				"cache_read_input_tokens": 5000,
				"cache_creation_input_tokens": 500
			}
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(respJSON)),
		}, nil
	})}

	var recorded []state.Usage
	reply, err := model.Respond(context.Background(), ReplyRequest{
		Messages: []json.RawMessage{
			modelMessage(map[string]string{"role": "system", "content": "You are Nori."}),
			modelMessage(map[string]string{"role": "user", "content": "Hi"}),
		},
		CacheBreakpoints: []int{0},
		RecordUsage: func(_ context.Context, u state.Usage) error {
			recorded = append(recorded, u)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if seenHeaders.Get("x-api-key") != "sk-ant-test" || seenHeaders.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("unexpected headers: %+v", seenHeaders)
	}
	if reply.Text != "Hello from Claude!" || !reply.Structured {
		t.Fatalf("unexpected reply: %+v", reply)
	}
	if len(recorded) != 1 || recorded[0].RequestStatus != "pending" {
		t.Fatalf("expected pre-IO pending usage record, got %+v", recorded)
	}
	if reply.Usage.CostUSD == nil || *reply.Usage.CostUSD <= 0 {
		t.Fatalf("expected calculated Anthropic cost, got %+v", reply.Usage)
	}
	if st := g.Stats(); st.TotalAdmitted != 1 || st.RecordedActualUSD <= 0 {
		t.Fatalf("expected llmguard to record call and actual cost, got %+v", st)
	}
}
