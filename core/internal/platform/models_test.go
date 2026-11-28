package platform_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"nori/internal/agent"
	"nori/internal/conversation"
	"nori/internal/llmguard"
	"nori/internal/modelprofiles"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

type stubRT func(*http.Request) (*http.Response, error)

func (f stubRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func okJSON(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestHostedModelAllowlistAndBillingTagging(t *testing.T) {
	// Rejects non-allowlisted model or missing platform gateway key fail-closed.
	called := false
	client := &http.Client{Transport: stubRT(func(r *http.Request) (*http.Response, error) {
		called = true
		return okJSON(`{}`), nil
	})}
	models := settings.Models{
		Guard:               llmguard.New(llmguard.DefaultConfig()),
		CloudDispatchClient: client,
	}
	meter := usage.Func(func(context.Context, state.Usage) error { return nil })

	unallowed := modelprofiles.Profile{Backend: modelprofiles.BackendHosted, Model: "openai/gpt-unknown"}
	if _, err := platform.ReplyModelForHosted(models, "u1", unallowed, "low", "plat-key").Respond(context.Background(), conversation.ReplyRequest{
		Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":"Hi"}`)},
	}); err == nil {
		t.Fatal("expected unallowed hosted chat model to fail closed")
	}
	if _, err := platform.TaskModelForHosted(models, meter, "u1", unallowed, "low", "plat-key").Respond(context.Background(), agent.Request{
		Messages: []transcript.Message{transcript.UserText("Hi")},
	}); err == nil {
		t.Fatal("expected unallowed hosted task model to fail closed")
	}
	allowed := modelprofiles.Profile{Backend: modelprofiles.BackendHosted, Model: "google/gemini-4-neon"}
	if _, err := platform.ReplyModelForHosted(models, "u1", allowed, "low", "").Respond(context.Background(), conversation.ReplyRequest{
		Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":"Hi"}`)},
	}); err == nil {
		t.Fatal("expected empty platform key to fail closed")
	}
	if called {
		t.Fatal("expected zero HTTP calls for rejected hosted models")
	}
}

func TestHostedGatewayTariffFallbackAndBillingTags(t *testing.T) {
	for _, tc := range []struct {
		name         string
		model        string
		chatBody     string
		taskBody     string
		wantCostMin  float64
		wantStatus   string
	}{
		{
			name:        "gemini-4-neon tariff fallback when gateway omits cost",
			model:       "google/gemini-4-neon",
			chatBody:    `{"model":"google/gemini-4-neon","usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500},"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"actions\":[],\"text\":\"Hello\"}"}}]}`,
			taskBody:    `{"status":"completed","model":"google/gemini-4-neon","usage":{"input_tokens":1000,"output_tokens":500,"total_tokens":1500},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}]}`,
			wantCostMin: 0.01,
			wantStatus:  "calculated",
		},
		{
			name:        "openai gpt-6.2-sol prefers gateway reported cost",
			model:       "openai/gpt-6.2-sol",
			chatBody:    `{"model":"openai/gpt-6.2-sol","usage":{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,"cost":0.042},"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"actions\":[],\"text\":\"Hello\"}"}}]}`,
			taskBody:    `{"status":"completed","model":"openai/gpt-6.2-sol","usage":{"input_tokens":1000,"output_tokens":500,"total_tokens":1500,"cost":0.042},"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}]}`,
			wantCostMin: 0.041,
			wantStatus:  "provider",
		},
		{
			name:        "anthropic claude-opus-5.5 tariff fallback",
			model:       "anthropic/claude-opus-5.5",
			chatBody:    `{"model":"claude-opus-5.5","stop_reason":"end_turn","usage":{"input_tokens":1000,"output_tokens":500},"content":[{"type":"text","text":"{\"actions\":[],\"text\":\"Hello\"}"}]}`,
			taskBody:    `{"model":"claude-opus-5.5","stop_reason":"end_turn","usage":{"input_tokens":1000,"output_tokens":500},"content":[{"type":"text","text":"Done"}]}`,
			wantCostMin: 0.01,
			wantStatus:  "calculated",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: stubRT(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "Bearer platform-secret" {
					return nil, fmt.Errorf("expected platform Bearer key, got %q", r.Header.Get("Authorization"))
				}
				if strings.HasSuffix(r.URL.Path, "/chat/completions") {
					return okJSON(tc.chatBody), nil
				}
				if strings.HasSuffix(r.URL.Path, "/messages") {
					var req map[string]any
					_ = json.NewDecoder(r.Body).Decode(&req)
					if _, hasTools := req["tools"]; hasTools {
						return okJSON(tc.taskBody), nil
					}
					if _, hasOutputCfg := req["output_config"]; hasOutputCfg {
						if oc, _ := req["output_config"].(map[string]any); oc["format"] != nil {
							return okJSON(tc.chatBody), nil
						}
					}
					return okJSON(tc.taskBody), nil
				}
				return okJSON(tc.taskBody), nil
			})}
			models := settings.Models{
				Guard:               llmguard.New(llmguard.DefaultConfig()),
				CloudDispatchClient: client,
			}
			profile := modelprofiles.Profile{Backend: modelprofiles.BackendHosted, Model: tc.model, Endpoint: "http://127.0.0.1:9999/v1"}

			var chatRecorded []state.Usage
			chatModel := platform.ReplyModelForHosted(models, "u-hosted", profile, "low", "platform-secret")
			reply, err := chatModel.Respond(context.Background(), conversation.ReplyRequest{
				Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":"Hi"}`)},
				RecordUsage: func(_ context.Context, u state.Usage) error {
					chatRecorded = append(chatRecorded, u)
					return nil
				},
			})
			if err != nil {
				t.Fatalf("hosted chat failed: %v", err)
			}
			if reply.Usage.Billing != state.UsageBillingHosted || reply.Usage.Category != state.UsageCategoryLLM {
				t.Fatalf("hosted chat usage tags = (%q, %q), want (hosted, llm)", reply.Usage.Billing, reply.Usage.Category)
			}
			if reply.Usage.CostUSD == nil || *reply.Usage.CostUSD < tc.wantCostMin || reply.Usage.CostStatus != tc.wantStatus {
				t.Fatalf("hosted chat cost = (%v, %q), want >= %v %q", reply.Usage.CostUSD, reply.Usage.CostStatus, tc.wantCostMin, tc.wantStatus)
			}
			if len(chatRecorded) == 0 || chatRecorded[0].Billing != state.UsageBillingHosted || chatRecorded[0].EstimatedExposureUSD <= 0 {
				t.Fatalf("hosted chat pending record missing billing/exposure: %+v", chatRecorded)
			}

			var mu sync.Mutex
			var taskRecorded []state.Usage
			meter := usage.Func(func(_ context.Context, u state.Usage) error {
				mu.Lock()
				taskRecorded = append(taskRecorded, u)
				mu.Unlock()
				return nil
			})
			taskModel := platform.TaskModelForHosted(models, meter, "u-hosted", profile, "low", "platform-secret")
			resp, err := taskModel.Respond(context.Background(), agent.Request{
				Messages:        []transcript.Message{transcript.UserText("Hi")},
				MaxOutputTokens: 1024,
			})
			if err != nil {
				t.Fatalf("hosted task failed: %v", err)
			}
			if !resp.CostKnown || resp.Cost < tc.wantCostMin {
				t.Fatalf("hosted task cost = (%v, known=%v), want >= %v", resp.Cost, resp.CostKnown, tc.wantCostMin)
			}
			if len(taskRecorded) < 2 {
				t.Fatalf("expected pending + final usage records, got %d", len(taskRecorded))
			}
			for i, rec := range taskRecorded {
				if rec.Billing != state.UsageBillingHosted || rec.Category != state.UsageCategoryLLM {
					t.Fatalf("task record %d tags = (%q, %q), want (hosted, llm)", i, rec.Billing, rec.Category)
				}
			}
		})
	}
}

func TestGoogleBYOKCalculatesGemini4TariffAndTagsBYOK(t *testing.T) {
	client := &http.Client{Transport: stubRT(func(r *http.Request) (*http.Response, error) {
		return okJSON(`{"model":"gemini-4-carbon","usage":{"prompt_tokens":10000,"completion_tokens":2000,"total_tokens":12000,"prompt_tokens_details":{"cached_tokens":2000}},"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Done"}}]}`), nil
	})}
	models := settings.Models{
		Guard:               llmguard.New(llmguard.DefaultConfig()),
		CloudDispatchClient: client,
	}
	var records []state.Usage
	meter := usage.Func(func(_ context.Context, u state.Usage) error {
		records = append(records, u)
		return nil
	})
	profile := modelprofiles.Profile{Backend: "google", Model: "gemini-4-carbon", Endpoint: "http://127.0.0.1:9999/v1"}
	taskModel := platform.TaskModelForBYOK(models, meter, "u-byok", profile, "low", "user-google-key")
	resp, err := taskModel.Respond(context.Background(), agent.Request{
		Messages:        []transcript.Message{transcript.UserText("Hi")},
		MaxOutputTokens: 1024,
	})
	if err != nil {
		t.Fatalf("Google BYOK task failed: %v", err)
	}
	wantCost, _ := usage.GoogleCost("gemini-4-carbon", 10000, 2000, 2000)
	if !resp.CostKnown || resp.Cost != wantCost {
		t.Fatalf("Google BYOK cost = (%v, known=%v), want %v", resp.Cost, resp.CostKnown, wantCost)
	}
	if len(records) < 2 {
		t.Fatalf("expected 2 usage records, got %d", len(records))
	}
	for _, r := range records {
		if r.Billing != state.UsageBillingBYOK || r.Category != state.UsageCategoryLLM {
			t.Fatalf("expected billing=byok category=llm, got (%q, %q)", r.Billing, r.Category)
		}
	}
}
