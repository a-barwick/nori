package conversation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/llmguard"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
)

func TestActionFreeStructuredPartial_BlocksActionsAndRawJSON(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		want   string
		wantOK bool
	}{
		{"incomplete_prefix", `{"act`, "", false},
		{"actions_open_not_closed", `{"actions":[`, "", false},
		{"non_empty_action", `{"actions":[{"name":"dispatch","arguments":{"request":"x"}}],"text":"On it!"}`, "", false},
		{"empty_actions_before_text", `{"actions":[],"text":"`, "", false},
		{"empty_actions_streaming_text", `{"actions":[],"text":"Hello world`, "Hello world", true},
		{"empty_actions_escaped_newline", `{"actions": [], "text": "Line 1\nLine 2\"quoted`, "Line 1\nLine 2\"quoted", true},
		{"empty_actions_complete", `{"actions":[],"text":"Thursday comes after Wednesday."}`, "Thursday comes after Wednesday.", true},
		{"text_before_actions_held", `{"text":"Premature draft","actions":[`, "", false},
	}
	for _, tc := range cases {
		got, ok := actionFreeStructuredPartial(tc.raw)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("%s: actionFreeStructuredPartial(%q) = (%q, %v), want (%q, %v)", tc.name, tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}

type optRoundTripFunc func(*http.Request) (*http.Response, error)

func (f optRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAnthropicStreaming_EmitsActionFreeOnlyAndBlocksActions(t *testing.T) {
	ssePayload := func(chunks []string) string {
		var b strings.Builder
		b.WriteString("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":100,\"cache_read_input_tokens\":500,\"cache_creation_input_tokens\":0,\"output_tokens\":1}}}\n\n")
		for _, c := range chunks {
			raw, _ := json.Marshal(c)
			b.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":" + string(raw) + "}}\n\n")
		}
		b.WriteString("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":25}}\n\n")
		b.WriteString("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		return b.String()
	}

	var mode string
	rt := optRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body string
		if mode == "action_free" {
			body = ssePayload([]string{`{"actions":[`, `],"text":"`, `Hello `, `Austin!"}`})
		} else {
			body = ssePayload([]string{`{"actions":[{"name":"stop","arguments":{"run_id":"00000000-0000-4000-8000-000000000001"}}],"text":"Stopping it now."}`})
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})

	m := NewAnthropicModel("test-key", "claude-opus-5-5")
	m.TestURL = "http://127.0.0.1:1"
	m.Client = &http.Client{Transport: rt}
	m.Guard = llmguard.New(llmguard.Config{AllowLiveInTests: true})

	mode = "action_free"
	var emitted []string
	reply, err := m.Respond(context.Background(), ReplyRequest{
		Messages: []json.RawMessage{modelMessage(map[string]string{"role": "user", "content": "hi"})},
		Stream: func(visible string) error {
			emitted = append(emitted, visible)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("action_free Respond: %v", err)
	}
	if reply.Text != "Hello Austin!" {
		t.Fatalf("reply.Text = %q, want Hello Austin!", reply.Text)
	}
	if len(emitted) < 2 || emitted[len(emitted)-1] != "Hello Austin!" {
		t.Fatalf("expected incremental clean deltas, got %v", emitted)
	}
	for _, s := range emitted {
		if strings.Contains(s, "{") || strings.Contains(s, `"actions"`) {
			t.Fatalf("raw JSON leaked in delta: %q", s)
		}
	}

	mode = "with_action"
	emitted = nil
	reply2, err := m.Respond(context.Background(), ReplyRequest{
		Messages: []json.RawMessage{modelMessage(map[string]string{"role": "user", "content": "stop task"})},
		TaskIDs:  []string{"00000000-0000-4000-8000-000000000001"},
		Stream: func(visible string) error {
			emitted = append(emitted, visible)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("with_action Respond: %v", err)
	}
	if len(emitted) != 0 {
		t.Fatalf("action-bearing reply must emit 0 streaming deltas, got %v", emitted)
	}
	if len(reply2.Calls) != 1 {
		t.Fatalf("expected 1 decoded action call, got %d", len(reply2.Calls))
	}
}

func TestOpenAIStreaming_DecodesNormalStructuredTurnAndBlocksRawJSON(t *testing.T) {
	mode := "action_free"
	m := &OpenAIModel{
		key:     "test-key",
		name:    "gpt-6.1-sol",
		TestURL: "https://openai.test/v1/chat/completions",
		Client: &http.Client{Transport: optRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			var chunks []string
			if mode == "action_free" {
				chunks = []string{
					`{"actions":`,
					`[],"text":"Hi `,
					`Austin!"}`,
				}
			} else {
				chunks = []string{
					`{"actions":[{"name":"stop","arguments":{"run_id":"00000000-0000-4000-8000-000000000001"}}],`,
					`"text":"Stopping it now."}`,
				}
			}
			var sb strings.Builder
			for _, c := range chunks {
				b, _ := json.Marshal(map[string]any{
					"id":    "chatcmpl-1",
					"model": "gpt-6.1-sol",
					"choices": []any{map[string]any{
						"delta": map[string]any{"content": c},
					}},
				})
				sb.WriteString("data: " + string(b) + "\n\n")
			}
			sb.WriteString(`data: {"id":"chatcmpl-1","model":"gpt-6.1-sol","choices":[{"finish_reason":"stop","delta":{}}]}` + "\n\n")
			sb.WriteString("data: [DONE]\n\n")
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(sb.String())),
			}, nil
		})},
	}

	var emitted []string
	reply, err := m.Respond(context.Background(), ReplyRequest{
		Messages: []json.RawMessage{modelMessage(map[string]string{"role": "user", "content": "hi"})},
		Stream: func(visible string) error {
			emitted = append(emitted, visible)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("openai action_free stream: %v", err)
	}
	if !reply.Structured || reply.Text != "Hi Austin!" || len(reply.Calls) != 0 {
		t.Fatalf("unexpected decoded action_free reply: structured=%v text=%q calls=%d", reply.Structured, reply.Text, len(reply.Calls))
	}
	if len(emitted) < 2 || emitted[len(emitted)-1] != "Hi Austin!" {
		t.Fatalf("expected clean incremental deltas, got %v", emitted)
	}
	for _, s := range emitted {
		if strings.Contains(s, "{") || strings.Contains(s, `"actions"`) {
			t.Fatalf("raw JSON leaked in OpenAI delta: %q", s)
		}
	}

	mode = "with_action"
	emitted = nil
	reply2, err := m.Respond(context.Background(), ReplyRequest{
		Messages: []json.RawMessage{modelMessage(map[string]string{"role": "user", "content": "stop"})},
		TaskIDs:  []string{"00000000-0000-4000-8000-000000000001"},
		Stream: func(visible string) error {
			emitted = append(emitted, visible)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("openai with_action stream: %v", err)
	}
	if !reply2.Structured || len(reply2.Calls) != 1 || reply2.Text != "Stopping it now." {
		t.Fatalf("unexpected decoded with_action reply: structured=%v text=%q calls=%d", reply2.Structured, reply2.Text, len(reply2.Calls))
	}
	if len(emitted) != 0 {
		t.Fatalf("action-bearing OpenAI stream must emit 0 deltas, got %v", emitted)
	}
}

func TestPureAckAllowlist_NeverSkipsPreferencesOrTasks(t *testing.T) {
	if !isPureAckText("Thanks!") || !isPureAckText("Got it.") || !isPureAckText("🙏") || !isPureAckText("Cheers!") {
		t.Fatalf("expected non-decision pure acks to match allowlist")
	}
	for _, nonAck := range []string{"No peanuts", "I'm vegetarian", "Call mom", "Yes, book Thursday", "ok", "sure", "yep", "👍", "?", ""} {
		if isPureAckText(nonAck) {
			t.Fatalf("short preference/decision/punctuation %q must NEVER be treated as pure ack", nonAck)
		}
	}
	taskTurn := state.ConversationTurn{
		Inputs: []state.ConversationInput{{ID: "ev1", Event: &state.TaskEvent{ID: "e1", RunID: "r1", Kind: "completed"}}},
	}
	if isPureAckBatch([]state.ConversationTurn{taskTurn}) {
		t.Fatalf("task-event turn must NEVER be skipped as pure ack")
	}
}

func TestCollapseActionFreeReplay_PreservesRejectionsAndDedupsCleanTurns(t *testing.T) {
	wrapped, _ := json.Marshal(map[string]any{"actions": []any{}, "text": "Hello!"})
	msgs := []json.RawMessage{
		modelMessage(map[string]string{"role": "system", "content": "sys"}),
		modelMessage(map[string]string{"role": "user", "content": "hi"}),
		modelMessage(map[string]string{"role": "assistant", "content": string(wrapped)}),
		modelMessage(map[string]string{"role": "assistant", "content": "Hello!"}),
		modelMessage(map[string]string{"role": "assistant", "content": string(wrapped)}),
		modelMessage(map[string]string{"role": "assistant", "content": "Action rejected."}),
	}
	got, bps := collapseActionFreeReplay(msgs, []int{0, 3})
	if len(got) != 5 {
		t.Fatalf("expected 5 messages (1 clean duplicate collapsed, rejection preserved), got %d", len(got))
	}
	if len(bps) != 2 || bps[0] != 0 || bps[1] != 2 {
		t.Fatalf("expected breakpoints shifted to [0 2], got %v", bps)
	}
}

func TestAtomicReserveUsage_PreventsConcurrentQuotaRace(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)
	backend := memstore.New()
	u := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "quota@example.com"}
	store := state.Compose(records.NewShared(backend, u, 1.0), memstore.NewArtifacts())

	meter := usage.WithAdmission(store, store.(usage.WindowReader), 100, 0.10, func() time.Time { return now })

	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed, blocked := 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := meter.RecordUsage(ctx, state.Usage{
				ID:            "call-" + string(rune('a'+idx)),
				Provider:      "anthropic",
				Model:         "claude-opus-5-5",
				CostStatus:    "unknown",
				RequestStatus: "pending",
				CreatedAt:     now,
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				allowed++
			} else if err == usage.ErrUserQuotaExceeded {
				blocked++
			} else {
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if allowed != 2 || blocked != workers-2 {
		t.Fatalf("atomic quota race: allowed=%d blocked=%d, want allowed=2 blocked=%d", allowed, blocked, workers-2)
	}
}
