package llmguard

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"nori/internal/modelkeys"
)

func TestBYOKCircuitKeepsSharedLimitsWhileAllowingKeyRepair(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Scopes[ScopeOuter] = ScopePolicy{MaxPerMinute: 3, MaxPerHour: 10, MaxInFlight: 2}
	guard := New(cfg)
	transport := guard.Transport(fakeRT{fn: func(req *http.Request) (*http.Response, error) {
		status := http.StatusOK
		if req.Header.Get("Authorization") == "Bearer failed-secret" {
			status = http.StatusPaymentRequired
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	}}, CallMeta{UserID: "user", Scope: ScopeOuter, BYOK: true})
	request := func(host, key string) error {
		req, _ := http.NewRequest(http.MethodPost, "https://"+host+"/v1/chat", strings.NewReader(`{"input":"test"}`))
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := transport.RoundTrip(req)
		if res != nil {
			_ = res.Body.Close()
		}
		return err
	}
	if err := request("first.example", "failed-secret"); err != nil {
		t.Fatal(err)
	}
	if err := request("first.example", "failed-secret"); !errors.Is(err, ErrCircuitOpen) || !errors.Is(err, modelkeys.ErrModelAccess) || !strings.Contains(err.Error(), "More → Models") {
		t.Fatalf("failed key circuit = %v", err)
	}
	if err := request("second.example", "other-secret"); err != nil {
		t.Fatalf("different provider blocked: %v", err)
	}
	if err := request("first.example", "replaced-secret"); err != nil {
		t.Fatalf("replacement key blocked: %v", err)
	}
	if err := request("first.example", "failed-secret"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("old key circuit was cleared: %v", err)
	}
	if err := request("third.example", "third-secret"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("shared rate limit = %v", err)
	}
}

func TestBYOKCircuitPreservesAccessFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		access bool
	}{
		{"invalid key", http.StatusUnauthorized, `{}`, true},
		{"forbidden", http.StatusForbidden, `{}`, true},
		{"quota", http.StatusTooManyRequests, `{"error":{"code":"insufficient_quota"}}`, true},
		{"temporary rate limit", http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded"}}`, false},
		{"provider outage", http.StatusServiceUnavailable, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := New(DefaultConfig())
			transport := guard.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			}}, CallMeta{UserID: "user", Scope: ScopeTask, BYOK: true})
			for i := 0; i < 5; i++ {
				req, _ := http.NewRequest(http.MethodPost, "https://example.com/v1/model", nil)
				req.Header.Set("Authorization", "Bearer test-key")
				res, err := transport.RoundTrip(req)
				if err != nil {
					t.Fatalf("attempt %d: %v", i, err)
				}
				_ = res.Body.Close()
			}
			req, _ := http.NewRequest(http.MethodPost, "https://example.com/v1/model", nil)
			req.Header.Set("Authorization", "Bearer test-key")
			_, err := transport.RoundTrip(req)
			if !errors.Is(err, ErrCircuitOpen) || errors.Is(err, modelkeys.ErrModelAccess) != tc.access {
				t.Fatalf("circuit error = %v", err)
			}
			if tc.access && !strings.HasPrefix(err.Error(), modelkeys.ErrModelAccess.Error()) {
				t.Fatalf("access message does not lead the circuit error: %v", err)
			}
		})
	}
}
