package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"

	"github.com/google/uuid"
	"nori/internal/conversation"
	"nori/internal/state"
)

// Exercise the HTTP response -> command transaction -> visible reply boundary
// with a deterministic structured action-only response. Real-model journeys cover dispatch;
// this fixture does not depend on a model choosing to omit its content.
func conversationBoundary(ctx context.Context, store *state.Google, taskID string) (map[string]bool, error) {
	var calls atomic.Int32
	var mode atomic.Int32
	var required atomic.Bool
	var targets atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		calls.Add(1)
		var request struct {
			Model      string          `json:"model"`
			ToolChoice string          `json:"tool_choice"`
			Provider   json.RawMessage `json:"provider"`
			Effort     string          `json:"reasoning_effort"`
			Tier       string          `json:"service_tier"`
			Max        int             `json:"max_completion_tokens"`
			Store      bool            `json:"store"`
			Cache      struct {
				Mode string `json:"mode"`
			} `json:"prompt_cache_options"`
			Messages []map[string]any `json:"messages"`
			Format   struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Schema map[string]any `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		valid := request.Model == "gpt-6.1-sol" && request.Format.Type == "json_schema" && request.ToolChoice == "" && len(request.Provider) == 0 && request.Effort == "low" && request.Tier == "default" && request.Max == 4096 && !request.Store && request.Cache.Mode == "explicit"
		for _, message := range request.Messages {
			valid = valid && len(message) == 2 && message["role"] != "tool"
		}
		required.Store(valid)
		// Inspect the actual HTTP schema at the provider boundary. Existing-task
		// actions must offer saved identities, never arbitrary task-name strings.
		properties := request.Format.JSONSchema.Schema["properties"].(map[string]any)
		variants := properties["actions"].(map[string]any)["items"].(map[string]any)["anyOf"].([]any)
		bound := len(variants) == 6
		for _, variant := range variants {
			props := variant.(map[string]any)["properties"].(map[string]any)
			name := props["name"].(map[string]any)["enum"].([]any)[0].(string)
			if name == "dispatch" {
				continue
			}
			key := "run_id"
			if name == "hold" {
				key = "task_id"
			}
			ids, ok := props["arguments"].(map[string]any)["properties"].(map[string]any)[key].(map[string]any)["enum"].([]any)
			found := false
			for _, id := range ids {
				found = found || id == taskID
			}
			bound = bound && ok && found
		}
		targets.Store(bound)

		actions := []any{map[string]any{"name": "hold", "arguments": map[string]string{"task_id": taskID, "question": "What does the result mean?"}}}
		text := ""
		if mode.Load() == 1 {
			actions, text = []any{}, "4"
		}
		content, _ := json.Marshal(map[string]any{"actions": actions, "text": text})
		if mode.Load() == 2 {
			content = []byte("I started that task.")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "gpt-6.1-sol", "service_tier": "default", "usage": map[string]any{"prompt_tokens": 1500, "completion_tokens": 200, "total_tokens": 1700}, "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content), "reasoning_details": []any{map[string]string{"data": "fixture-private-provider-extension"}}}}}})

	}))
	defer provider.Close()
	// The advisory classifier is unavailable locally; no outbound calls occur.
	classifier := conversation.NewClassifier("")
	classifier.Client = &http.Client{Transport: unavailableClassifier{provider.URL}}
	newRunner := func() *conversation.Runner {
		model := conversation.NewModel("https://api.openai.com/v1", "fixture-only", "gpt-6.1-sol")
		model.Client = &http.Client{Transport: localModelTransport{provider.URL}}
		return conversation.New(model, classifier, &conversation.Tasks{Store: store})
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
	structured := len(history) == 1 && len(history[0].Attempt.Actions) == 1 && history[0].Attempt.Actions[0].Status == "accepted" && history[0].Attempt.Text == "" && history[0].Attempt.StructuredReply
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
	report := map[string]bool{
		"HTTP routing schema binds task commands to saved context identities":          targets.Load(),
		"direct OpenAI Sol low request and usage-based cost":                           required.Load() && first.Traces[0].Usage[len(first.Traces[0].Usage)-1].CostStatus == "calculated" && *first.Traces[0].Usage[len(first.Traces[0].Usage)-1].CostUSD == .005,
		"structured response admits a durable command":                                 structured && len(run.Holds) == 1,
		"action-only command publishes a receipt acknowledgment":                       ack,
		"saved protocol retains structured response and visible acknowledgment":        protocol,
		"action-only command replay after restart makes no new provider call or reply": calls.Load() == 1 && len(second.Messages) == len(first.Messages) && len(second.Traces) == 1,
	}
	mode.Store(1)
	if _, err := runner.HandleInput(ctx, conversation.Input{ID: uuid.NewString(), Message: "What is 2 + 2?"}); err != nil {
		return nil, err
	}
	if err := runner.Process(ctx); err != nil {
		return nil, err
	}
	chat, err := runner.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	report["required routing permits ordinary conversation without task commands"] = required.Load() && chat.Messages[len(chat.Messages)-1].Text == "4" && len(chat.Traces[0].Actions) == 0 && len(chat.Runs) == len(first.Runs)
	mode.Store(2)
	if _, err := runner.HandleInput(ctx, conversation.Input{ID: uuid.NewString(), Message: "Start another fixture task."}); err != nil {
		return nil, err
	}
	if err := runner.Process(ctx); err != nil {
		return nil, err
	}
	missing, err := runner.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	report["provider omission of required routing cannot publish a false start acknowledgment"] = missing.Traces[0].Status == "error" && missing.Messages[len(missing.Messages)-1].Text != "I started that task." && len(missing.Runs) == len(first.Runs) && calls.Load() == 3
	history, _, err = store.ConversationHistory(ctx)
	if err != nil {
		return nil, err
	}
	retained := false
	for _, turn := range history {
		if turn.Attempt.Status == "error" {
			retained = turn.Attempt.ProtocolInvalid && turn.Attempt.StructuredReply && turn.Attempt.Reply != ""
			for _, message := range turn.Protocol {
				if message == turn.Attempt.Reply {
					retained = false
				}
			}
		}
	}
	report["rejected response retained privately and excluded from model history"] = retained
	return report, nil
}

type unavailableClassifier struct{ base string }

func (t unavailableClassifier) RoundTrip(r *http.Request) (*http.Response, error) {
	local, err := http.NewRequestWithContext(r.Context(), http.MethodPost, t.base+"/classifier", nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultTransport.RoundTrip(local)
}

// Only the fixture transport rewrites the destination. Production still validates
// and sends the exact api.openai.com URL before reserving paid requests.
type localModelTransport struct{ base string }

func (t localModelTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	u, err := url.Parse(t.base + "/chat/completions")
	if err != nil {
		return nil, err
	}
	r.URL, r.Host = u, u.Host
	return http.DefaultTransport.RoundTrip(r)
}
