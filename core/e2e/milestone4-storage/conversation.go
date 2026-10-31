package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/google/uuid"
	"nori/internal/conversation"
	"nori/internal/state"
)

// Exercise the HTTP response -> command transaction -> visible reply boundary
// with a deterministic tool-only response. Real-model journeys cover dispatch;
// this fixture does not depend on a model choosing to omit its content.
func conversationBoundary(ctx context.Context, store *state.Google, taskID string) (map[string]bool, error) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		calls.Add(1)
		args, _ := json.Marshal(map[string]string{"task_id": taskID, "question": "What does the result mean?"})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": nil,
				"tool_calls": []any{map[string]any{"id": "fixture-hold", "type": "function", "function": map[string]any{"name": "hold", "arguments": string(args)}}}},
		}}})
	}))
	defer provider.Close()
	// The advisory classifier is unavailable locally; no outbound calls occur.
	classifier := conversation.NewClassifier("")
	classifier.Client = &http.Client{Transport: unavailableClassifier{provider.URL}}
	newRunner := func() *conversation.Runner {
		return conversation.New(conversation.NewModel(provider.URL, "fixture-only", "meta/muse-spark-1.3"), classifier, &conversation.Tasks{Store: store})
	}
	runner := newRunner()
	input := conversation.Input{ID: uuid.NewString(), Message: "Hold my question for the existing fixture task."}
	if _, err := runner.HandleInput(ctx, input); err != nil {
		return nil, err
	}
	if err := runner.Process(ctx); err != nil {
		return nil, err
	}
	first, err := runner.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	history, _, err := store.ConversationHistory(ctx)
	if err != nil {
		return nil, err
	}
	want := "I've saved your question to answer when the task has an outcome."
	ack := len(first.Messages) == 2 && first.Messages[1].Text == want
	native := len(history) == 1 && len(history[0].Attempt.Actions) == 1 && history[0].Attempt.Actions[0].Status == "accepted" && history[0].Attempt.Text == ""
	protocol := len(history) == 1 && len(history[0].Protocol) == 4
	if protocol {
		var message map[string]any
		_ = json.Unmarshal([]byte(history[0].Protocol[3]), &message)
		protocol = message["content"] == want
	}
	// Recreate the runner and replay the same input, using only persisted state.
	runner = newRunner()
	if _, err := runner.HandleInput(ctx, input); err != nil {
		return nil, err
	}
	if err := runner.Process(ctx); err != nil {
		return nil, err
	}
	second, err := runner.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	run, err := store.GetRun(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return map[string]bool{
		"tool-only native response admits a durable command":                         native && len(run.Holds) == 1,
		"tool-only command publishes a receipt acknowledgment":                       ack,
		"saved protocol retains native response and visible acknowledgment":          protocol,
		"tool-only command replay after restart makes no new provider call or reply": calls.Load() == 1 && len(second.Messages) == len(first.Messages) && len(second.Traces) == 1,
	}, nil
}

type unavailableClassifier struct{ base string }

func (t unavailableClassifier) RoundTrip(r *http.Request) (*http.Response, error) {
	local, err := http.NewRequestWithContext(r.Context(), http.MethodPost, t.base+"/classifier", nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultTransport.RoundTrip(local)
}
