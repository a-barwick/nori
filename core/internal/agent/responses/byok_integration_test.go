package responses_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nori/internal/agent"
	"nori/internal/conversation"
	"nori/internal/llmguard"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

func TestBYOKChatAndWorkProtocols(t *testing.T) {
	for _, tc := range []struct{ provider, model string }{
		{"openai", "gpt-6.2-sol"}, {"openai", "gpt-6.1-terra"}, {"openai", "gpt-6-luna"},
		{"anthropic", "claude-opus-5-5"}, {"anthropic", "claude-sonnet-5-5"}, {"anthropic", "claude-haiku-5-5"},
		{"google", "gemini-4-carbon"}, {"google", "gemini-4-neon"}, {"google", "gemini-4-hydrogen"},
		{"vercel_ai_gateway", "anthropic/claude-opus-5.5"}, {"vercel_ai_gateway", "openai/gpt-6.2-sol"},
		{"vercel_ai_gateway", "google/gemini-4-neon"}, {"openrouter", "anthropic/claude-opus-5.5"},
		{"openrouter", "openai/gpt-6.2-sol"}, {"openrouter", "google/gemini-4-neon"},
	} {
		t.Run(tc.provider+"/"+tc.model, func(t *testing.T) {
			var workRequests int
			anthropicProtocol := tc.provider == "anthropic" || (tc.provider == "vercel_ai_gateway" && strings.HasPrefix(tc.model, "anthropic/"))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.provider == "anthropic" {
					if r.Header.Get("x-api-key") != "user-secret" || r.Header.Get("anthropic-version") == "" {
						t.Error("incorrect Anthropic authentication")
					}
				} else if r.Header.Get("Authorization") != "Bearer user-secret" {
					t.Errorf("incorrect %s authentication", tc.provider)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["model"] != tc.model {
					t.Errorf("wrong model: %v", body["model"])
				}
				if anthropicProtocol {
					config, _ := body["output_config"].(map[string]any)
					if config["effort"] != "high" {
						t.Errorf("missing Anthropic effort: %v", config)
					}
				} else if strings.HasSuffix(r.URL.Path, "/responses") {
					config, _ := body["reasoning"].(map[string]any)
					if config["effort"] != "high" {
						t.Errorf("missing Responses effort: %v", config)
					}
					options, _ := body["provider"].(map[string]any)
					if _, limited := options["max_price"]; limited {
						t.Error("unexpected OpenRouter price cap")
					}
				} else if body["reasoning_effort"] != "high" {
					t.Errorf("missing Chat effort: %v", body["reasoning_effort"])
				}
				work := body["tools"] != nil || strings.HasSuffix(r.URL.Path, "/responses")
				if !work {
					if anthropicProtocol {
						if _, ok := body["output_config"].(map[string]any)["format"]; !ok {
							t.Error("missing Chat structured output format")
						}
						fmt.Fprint(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":"{\"actions\":[],\"text\":\"Hello\"}"}]}`)
					} else {
						if _, ok := body["response_format"]; !ok {
							t.Error("missing Chat structured output format")
						}
						fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{\"actions\":[],\"text\":\"Hello\"}"}}]}`)
					}
					return
				}
				workRequests++
				if workRequests == 2 && (anthropicProtocol || tc.provider == "google") {
					encoded, _ := json.Marshal(body["messages"])
					if !strings.Contains(string(encoded), `"signed-thought"`) {
						t.Error("provider thought state was not replayed")
					}
				}
				if anthropicProtocol {
					if workRequests == 1 {
						fmt.Fprint(w, `{"stop_reason":"tool_use","content":[{"type":"thinking","thinking":"reasoning","signature":"signed-thought"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"q":"hello"}}]}`)
					} else {
						fmt.Fprint(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":"Done"}]}`)
					}
				} else if tc.provider == "google" {
					if workRequests == 1 {
						fmt.Fprint(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"hello\"}"},"extra_content":{"google":{"thought_signature":"signed-thought"}}}]}}]}`)
					} else {
						fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"Done"}}]}`)
					}
				} else if workRequests == 1 {
					fmt.Fprint(w, `{"status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"hello\"}"}]}`)
				} else {
					fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done"}]}]}`)
				}
			}))
			defer server.Close()
			models := settings.Models{TestURL: server.URL, Guard: llmguard.New(llmguard.DefaultConfig())}
			profile := modelprofiles.Profile{Backend: tc.provider, Model: tc.model}
			if tc.provider == "google" || tc.provider == "vercel_ai_gateway" {
				profile.Endpoint = server.URL + "/v1"
			}
			meter := usage.Func(func(context.Context, state.Usage) error { return nil })
			chat := platform.ReplyModelForBYOK(models, "user", profile, "high", "user-secret")
			reply, err := chat.Respond(context.Background(), conversation.ReplyRequest{Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":"Hello"}`)}})
			if err != nil || reply.Text != "Hello" {
				t.Fatalf("Chat failed: %+v, %v", reply, err)
			}
			work := platform.TaskModelForBYOK(models, meter, "user", profile, "high", "user-secret")
			request := agent.Request{Instructions: "Help", Messages: []transcript.Message{transcript.UserText("Hello")}, Tools: []transcript.Tool{{Name: "lookup", Parameters: &transcript.Schema{Type: transcript.TypeObject}}}, MaxOutputTokens: 1024}
			first, err := work.Respond(context.Background(), request)
			if err != nil || len(first.Calls) != 1 || first.Message == nil {
				t.Fatalf("Work tool call failed: %+v, %v", first, err)
			}
			request.Messages = append(request.Messages, *first.Message, transcript.ToolResultMessage(first.Calls[0].ID, first.Calls[0].Name, map[string]any{"ok": true}))
			second, err := work.Respond(context.Background(), request)
			if err != nil || second.Answer != "Done" {
				t.Fatalf("Work continuation failed: %+v, %v", second, err)
			}
			if workRequests != 2 {
				t.Fatalf("sent %d Work requests", workRequests)
			}
		})
	}
}

func TestGoogleBYOKQuotaFailureStopsAndExplainsRepair(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"insufficient_quota"}}`)
	}))
	defer server.Close()
	profile := modelprofiles.Profile{Backend: "google", Model: "gemini-4-neon", Endpoint: server.URL}
	meter := usage.Func(func(context.Context, state.Usage) error { return nil })
	model := platform.TaskModelForBYOK(settings.Models{Guard: llmguard.New(llmguard.DefaultConfig())}, meter, "user", profile, "low", "user-secret")
	_, err := model.Respond(context.Background(), agent.Request{Messages: []transcript.Message{transcript.UserText("Hi")}})
	if !errors.Is(err, modelkeys.ErrModelAccess) || calls != 1 || !strings.Contains(err.Error(), "More → Models") {
		t.Fatalf("quota error = %v after %d calls", err, calls)
	}
}
