package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"nori/internal/llmguard"
	"nori/internal/state"
	"nori/internal/usage"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestWebSearchUsesExa(t *testing.T) {
	var gotURL, gotMethod, gotKey, gotQuery string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotMethod = r.Method
		gotKey = r.Header.Get("x-api-key")
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		gotQuery, _ = req["query"].(string)
		return jsonResponse(http.StatusOK, `{
			"results": [
				{
					"title": "<b>Exa Result</b>",
					"url": "https://example.com/exa",
					"publishedDate": "2026-11-20",
					"highlights": ["A <em>highlighted</em> snippet."]
				}
			],
			"costDollars": {"total": 0.007}
		}`), nil
	})}

	svc := &Service{
		HTTP: client,
		Capabilities: Capabilities{
			ExaSearchKey: "exa-test-key",
		},
	}
	res, err := svc.webSearch(context.Background(), "nori assistant")
	if err != nil {
		t.Fatalf("webSearch failed: %v", err)
	}
	if gotURL != exaSearchURL || gotMethod != http.MethodPost {
		t.Fatalf("expected POST %s, got %s %s", exaSearchURL, gotMethod, gotURL)
	}
	if gotKey != "exa-test-key" {
		t.Fatalf("expected x-api-key exa-test-key, got %q", gotKey)
	}
	if gotQuery != "nori assistant" {
		t.Fatalf("expected query %q, got %q", "nori assistant", gotQuery)
	}
	if res["cost_usd"] != 0.007 {
		t.Fatalf("expected cost_usd 0.007, got %v", res["cost_usd"])
	}
	items, _ := res["results"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 result, got %d", len(items))
	}
	if items[0]["title"] != "Exa Result" || items[0]["snippet"] != "A highlighted snippet." || items[0]["published"] != "2026-11-20" {
		t.Fatalf("unexpected result item: %#v", items[0])
	}
}

func TestWebSearchUsesFallbackCostWhenExaOmitsTotal(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"results":[{"title":"Result","url":"https://example.com/r","summary":"Summary"}]}`), nil
	})}

	svc := &Service{
		HTTP:         client,
		Capabilities: Capabilities{ExaSearchKey: "exa-test-key"},
	}
	res, err := svc.webSearch(context.Background(), "hello")
	if err != nil {
		t.Fatalf("webSearch failed: %v", err)
	}
	if res["cost_usd"] != ExaSearchCostUSD {
		t.Fatalf("expected fallback cost_usd=%v, got %#v", ExaSearchCostUSD, res["cost_usd"])
	}
}

func TestWebSearchUnavailableWithoutKeys(t *testing.T) {
	svc := &Service{}
	res, err := svc.webSearch(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res["available"] != false {
		t.Fatalf("expected available=false, got %#v", res)
	}
}

func TestWebSearchMetersHostedAndBYOKExa(t *testing.T) {
	t.Run("hosted Exa metering reserves $0.013 and finalizes exact costDollars.total", func(t *testing.T) {
		var gotKey string
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotKey = r.Header.Get("x-api-key")
			return jsonResponse(http.StatusOK, `{"results":[{"title":"R","url":"https://example.com"}],"costDollars":{"total":0.009}}`), nil
		})}
		var records []state.Usage
		svc := &Service{
			HTTP:         client,
			Capabilities: Capabilities{ExaSearchKey: "platform-exa-key"},
		}
		ConfigureSearch(svc, SearchOptions{
			Meter:  usage.Func(func(_ context.Context, u state.Usage) error { records = append(records, u); return nil }),
			Guard:  llmguard.New(llmguard.DefaultConfig()),
			UserID: "u-hosted-exa",
		})
		// Verify shallow copy (like task.Activities.agentForRun) preserves SearchOptions.
		copied := *svc
		res, err := copied.webSearch(context.Background(), "nori pricing")
		if err != nil {
			t.Fatalf("webSearch failed: %v", err)
		}
		if gotKey != "platform-exa-key" {
			t.Fatalf("got key %q, want platform-exa-key", gotKey)
		}
		if res["cost_usd"] != 0.009 {
			t.Fatalf("cost_usd = %v, want 0.009", res["cost_usd"])
		}
		if len(records) != 2 {
			t.Fatalf("expected pending + finalized records, got %d", len(records))
		}
		pending, final := records[0], records[1]
		if pending.Billing != state.UsageBillingHosted || pending.Category != state.UsageCategorySearch || pending.EstimatedExposureUSD != 0.013 || pending.RequestStatus != "pending" {
			t.Fatalf("unexpected pending Exa record: %+v", pending)
		}
		if final.Billing != state.UsageBillingHosted || final.Category != state.UsageCategorySearch || final.CostUSD == nil || *final.CostUSD != 0.009 || final.CostStatus != "provider" {
			t.Fatalf("unexpected finalized Exa record: %+v", final)
		}
	})

	t.Run("BYOK Exa used when allowed and skips hosted llmguard spend cap", func(t *testing.T) {
		var gotKey string
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotKey = r.Header.Get("x-api-key")
			return jsonResponse(http.StatusOK, `{"results":[{"title":"BYOK","url":"https://example.com"}]}`), nil
		})}
		g := llmguard.New(llmguard.Config{MaxEstimatedUSD: 0.001}) // tighter than $0.013
		g.RecordActualCost(0.001)                                  // hosted cap exhausted
		var records []state.Usage
		svc := &Service{
			HTTP:         client,
			Capabilities: Capabilities{ExaSearchKey: "platform-exa-key"},
		}
		ConfigureSearch(svc, SearchOptions{
			Meter:          usage.Func(func(_ context.Context, u state.Usage) error { records = append(records, u); return nil }),
			Guard:          g,
			UserID:         "u-byok-exa",
			AllowBYOK:      func(context.Context) (bool, error) { return true, nil },
			ResolveBYOKKey: func(context.Context) (string, bool, error) { return "user-exa-key", true, nil },
		})
		res, err := svc.webSearch(context.Background(), "byok search")
		if err != nil {
			t.Fatalf("expected BYOK Exa to bypass hosted guard cap, got %v", err)
		}
		if gotKey != "user-exa-key" {
			t.Fatalf("expected BYOK key, got %q", gotKey)
		}
		if res["cost_usd"] != ExaSearchCostUSD {
			t.Fatalf("expected fallback cost %v, got %v", ExaSearchCostUSD, res["cost_usd"])
		}
		if len(records) != 2 || records[1].Billing != state.UsageBillingBYOK || records[1].Category != state.UsageCategorySearch || records[1].CostUSD == nil || *records[1].CostUSD != 0.013 {
			t.Fatalf("unexpected BYOK Exa usage records: %+v", records)
		}
	})

	t.Run("trial tier ignores BYOK Exa and enforces hosted ScopeSearch guard", func(t *testing.T) {
		var gotKey string
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotKey = r.Header.Get("x-api-key")
			return jsonResponse(http.StatusOK, `{"results":[]}`), nil
		})}
		g := llmguard.New(llmguard.Config{MaxEstimatedUSD: 0.005}) // tighter than $0.013
		var records []state.Usage
		svc := &Service{
			HTTP:         client,
			Capabilities: Capabilities{ExaSearchKey: "platform-exa-key"},
		}
		ConfigureSearch(svc, SearchOptions{
			Meter:          usage.Func(func(_ context.Context, u state.Usage) error { records = append(records, u); return nil }),
			Guard:          g,
			UserID:         "u-trial-exa",
			AllowBYOK:      func(context.Context) (bool, error) { return false, nil },
			ResolveBYOKKey: func(context.Context) (string, bool, error) { return "user-exa-key", true, nil },
		})
		_, err := svc.webSearch(context.Background(), "trial search")
		if !errors.Is(err, llmguard.ErrBudgetExceeded) {
			t.Fatalf("expected hosted ScopeSearch guard ErrBudgetExceeded, got %v", err)
		}
		if gotKey != "" {
			t.Fatalf("expected blocked search not to send HTTP request, got key %q", gotKey)
		}
		if len(records) != 2 || records[1].RequestStatus != "not_sent" || records[1].Billing != state.UsageBillingHosted {
			t.Fatalf("expected blocked hosted Exa record marked not_sent, got %+v", records)
		}
	})
}
