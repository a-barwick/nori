package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
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
