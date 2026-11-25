package responses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"nori/internal/agent"
	"nori/internal/llmguard"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

type stubRoundTrip func(*http.Request) (*http.Response, error)

func (f stubRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDirectTerraEnablesAutomaticPromptCachingAndRecordsCacheTokens(t *testing.T) {
	var seenBody map[string]any
	var recorded []state.Usage
	meter := usage.Func(func(_ context.Context, u state.Usage) error {
		recorded = append(recorded, u)
		return nil
	})

	client := New("sk-test-openai", settings.DefaultInnerModel, meter)
	client.Guard = llmguard.New(llmguard.DefaultConfig())
	client.HTTP = &http.Client{Transport: stubRoundTrip(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &seenBody)
		resp := `{
			"status": "completed",
			"model": "gpt-6.1-terra",
			"service_tier": "default",
			"output": [{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "done"}]}],
			"usage": {
				"input_tokens": 10000,
				"output_tokens": 200,
				"total_tokens": 10200,
				"input_tokens_details": {"cached_tokens": 6000, "cache_write_tokens": 2000}
			}
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(resp)),
		}, nil
	})}

	res, err := client.Respond(context.Background(), agent.Request{
		Instructions: "System instructions.",
		Messages: []transcript.Message{{
			Role:  transcript.RoleUser,
			Parts: []transcript.Part{{Text: "hello"}},
		}},
		MaxOutputTokens: 512,
	})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if _, hasExplicit := seenBody["prompt_cache_options"]; hasExplicit {
		t.Fatalf("direct gpt-6.1-terra must not send prompt_cache_options without explicit breakpoints, got %#v", seenBody["prompt_cache_options"])
	}
	if !res.CostKnown || res.Cost <= 0 {
		t.Fatalf("expected known Terra cost, got %+v", res)
	}
	last := recorded[len(recorded)-1]
	if last.CachedTokens == nil || *last.CachedTokens != 6000 {
		t.Fatalf("expected CachedTokens=6000, got %+v", last.CachedTokens)
	}
	if last.CacheWriteTokens == nil || *last.CacheWriteTokens != 2000 {
		t.Fatalf("expected CacheWriteTokens=2000, got %+v", last.CacheWriteTokens)
	}
}
