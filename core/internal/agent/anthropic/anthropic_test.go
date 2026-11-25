package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"nori/internal/agent"
	"nori/internal/llmguard"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClient_RespondToolCallAndUsage(t *testing.T) {
	var seenBody map[string]any
	g := llmguard.New(llmguard.DefaultConfig())
	var records []state.Usage
	meter := usage.Func(func(_ context.Context, u state.Usage) error {
		records = append(records, u)
		return nil
	})
	client := New("sk-ant-inner", "claude-sonnet-4-5", meter)
	client.Guard = g
	client.HTTP = &http.Client{Transport: rtFunc(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &seenBody)
		resp := `{
			"model": "claude-sonnet-4-5",
			"stop_reason": "tool_use",
			"content": [
				{"type": "text", "text": "Checking calendar."},
				{"type": "tool_use", "id": "tu_1", "name": "list_events", "input": {"day": "today"}}
			],
			"usage": {"input_tokens": 500, "output_tokens": 100, "cache_read_input_tokens": 2000}
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(resp)),
		}, nil
	})}

	res, err := client.Respond(context.Background(), agent.Request{
		Instructions: "Be helpful.",
		Messages: []transcript.Message{{
			Role:  transcript.RoleUser,
			Parts: []transcript.Part{{Text: "What is on my calendar?"}},
		}},
		Tools: []transcript.Tool{{
			Name:        "list_events",
			Description: "List events",
			Parameters:  &transcript.Schema{Type: transcript.TypeObject},
		}},
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if len(res.Calls) != 1 || res.Calls[0].Name != "list_events" || res.Calls[0].ID != "tu_1" {
		t.Fatalf("unexpected tool calls: %+v", res.Calls)
	}
	if !res.CostKnown || res.Cost <= 0 {
		t.Fatalf("expected known positive cost, got %+v", res)
	}
	if len(records) < 2 || records[0].RequestStatus != "pending" || records[len(records)-1].RequestStatus != "received" {
		t.Fatalf("unexpected usage lifecycle records: %+v", records)
	}
	if st := g.Stats(); st.TotalAdmitted != 1 || st.RecordedActualUSD <= 0 {
		t.Fatalf("expected llmguard accounting, got %+v", st)
	}
}
