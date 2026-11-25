package llmguard

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRT struct {
	fn func(*http.Request) (*http.Response, error)
}

func (f fakeRT) RoundTrip(r *http.Request) (*http.Response, error) { return f.fn(r) }

func okResp() *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: http.Header{}}
}

func TestGuard_BlocksNonLoopbackInGoTest(t *testing.T) {
	g := New(DefaultConfig())
	client := g.Client(time.Second, CallMeta{Scope: ScopeOuter})

	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", strings.NewReader(`{"a":1}`))
	_, err := client.Do(req)
	if !errors.Is(err, ErrLiveCallBlockedInTest) {
		t.Fatalf("expected ErrLiveCallBlockedInTest, got %v", err)
	}
	if st := g.Stats(); st.BlockedLiveTest != 1 || st.TotalAdmitted != 0 {
		t.Fatalf("expected BlockedLiveTest=1 TotalAdmitted=0, got %+v", st)
	}
}

func TestGuard_CompactionRateLimitAndDuplicateStorm(t *testing.T) {
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	g := New(Config{
		Now: func() time.Time { return now },
	})
	var calls atomic.Int32
	rt := g.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return okResp(), nil
	}}, CallMeta{UserID: "u1", Scope: ScopeCompaction})

	// 1. Duplicate storm detector: identical payload rejected on 5th attempt (allows 1 initial + 3 transient retries).
	for i := range 4 {
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"same":"payload"}`))
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("identical call %d failed unexpectedly: %v", i+1, err)
		}
		_ = resp.Body.Close()
	}
	reqDup, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"same":"payload"}`))
	if _, err := rt.RoundTrip(reqDup); !errors.Is(err, ErrDuplicateStorm) {
		t.Fatalf("expected ErrDuplicateStorm on 5th identical call, got %v", err)
	}

	// 2. Scope per-minute rate limit (compaction default = 6/min): 2 more distinct payloads succeed, 7th is rate-limited.
	for i := range 2 {
		body := bytes.NewBufferString(`{"distinct":` + string(rune('a'+i)) + `}`)
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", body)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("distinct call %d failed: %v", i, err)
		}
		_ = resp.Body.Close()
	}
	reqRate, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"distinct":"z"}`))
	if _, err := rt.RoundTrip(reqRate); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited on 7th compaction call in 1m, got %v", err)
	}
	if calls.Load() != 6 {
		t.Fatalf("expected exactly 6 admitted upstream calls, got %d", calls.Load())
	}

	// Advancing > 1 minute restores per-minute rate budget.
	now = now.Add(61 * time.Second)
	reqAfter, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"distinct":"after"}`))
	respAfter, err := rt.RoundTrip(reqAfter)
	if err != nil {
		t.Fatalf("expected call after 1m window to succeed, got %v", err)
	}
	_ = respAfter.Body.Close()
}

func TestGuard_ConsecutiveAbortCircuitBreakerAndUnknownCost(t *testing.T) {
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	g := New(Config{
		Now:                   func() time.Time { return now },
		AbortBreakerThreshold: 3,
		BreakerBaseCooldown:   30 * time.Second,
	})
	var calls atomic.Int32
	rt := g.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, context.Canceled
	}}, CallMeta{UserID: "u-abort", Scope: ScopeCompaction})

	// 3 consecutive aborted calls trip the circuit breaker (mirrors the 500ms cancel storm).
	for i := range 3 {
		body := strings.NewReader(`{"prompt":"` + strings.Repeat("x", 4000) + `","seq":` + string(rune('0'+i)) + `}`)
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", body)
		if _, err := rt.RoundTrip(req); !errors.Is(err, context.Canceled) {
			t.Fatalf("abort call %d: want context.Canceled, got %v", i+1, err)
		}
	}

	// 4th call is blocked BEFORE reaching the upstream transport!
	req4, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"seq":4}`))
	if _, err := rt.RoundTrip(req4); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen after 3 consecutive aborts, got %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 upstream calls before breaker opened, got %d", calls.Load())
	}
	st := g.Stats()
	if st.UnknownAborts != 3 || st.BlockedCircuit != 1 || st.EstimatedUnknownUSD <= 0 {
		t.Fatalf("expected UnknownAborts=3, BlockedCircuit=1, EstimatedUnknownUSD>0; got %+v", st)
	}

	// After cooldown elapses, a healthy response resets the breaker.
	now = now.Add(31 * time.Second)
	okRT := g.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return okResp(), nil
	}}, CallMeta{UserID: "u-abort", Scope: ScopeCompaction})
	req5, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"seq":5}`))
	if _, err := okRT.RoundTrip(req5); err != nil {
		t.Fatalf("expected call after cooldown to succeed, got %v", err)
	}
}

func TestGuard_EstimatedBudgetCountsUnknownAborts(t *testing.T) {
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	g := New(Config{
		Now:                   func() time.Time { return now },
		AbortBreakerThreshold: 100, // isolate budget cap
		MaxEstimatedUSD:       0.025,
	})
	rt := g.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return nil, context.Canceled
	}}, CallMeta{UserID: "u-budget", Scope: ScopeTask})

	// Each aborted call reserves ~$0.01+ conservative exposure; budget must trip quickly.
	var budgetHit bool
	for i := range 10 {
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"n":`+string(rune('0'+i))+`}`))
		_, err := rt.RoundTrip(req)
		if errors.Is(err, ErrBudgetExceeded) {
			budgetHit = true
			break
		}
	}
	if !budgetHit {
		t.Fatalf("expected ErrBudgetExceeded from accumulated unknown/aborted exposure; stats=%+v", g.Stats())
	}
}

func TestGuard_ConcurrencyLimitAndRedirectRefusal(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	base := fakeRT{fn: func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/redirect" {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"http://127.0.0.1:9999/other"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}
		close(started)
		<-release
		return okResp(), nil
	}}

	g := New(DefaultConfig())
	client := g.WrapClient(&http.Client{Transport: base}, 10*time.Second, CallMeta{UserID: "u-conc", Scope: ScopeCompaction})

	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"req":1}`))
		resp, err := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("first call exited before reaching transport: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first call to start")
	}

	// Second concurrent compaction for same user must be rejected (MaxInFlight=1).
	req2, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"req":2}`))
	_, err := client.Do(req2)
	if !errors.Is(err, ErrConcurrencyLimited) {
		t.Fatalf("expected ErrConcurrencyLimited for 2nd concurrent compaction, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	// Redirects must not be followed.
	reqRedir, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:9999/redirect", nil)
	resp, err := client.Do(reqRedir)
	if err != nil {
		t.Fatalf("redirect request error: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 without following redirect, got %d", resp.StatusCode)
	}
}

func TestGuard_MaxTotalCallsAndInFlightEstimatedUSD(t *testing.T) {
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	started := make(chan struct{})
	release := make(chan struct{})
	g := New(Config{
		Now:             func() time.Time { return now },
		MaxEstimatedUSD: 0.015, // enough for 1 in-flight (~$0.0108), blocks 2nd concurrent call
		MaxTotalCalls:   2,
	})
	rt := g.Transport(fakeRT{fn: func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "block") {
			close(started)
			<-release
		}
		return okResp(), nil
	}}, CallMeta{UserID: "u-cap", Scope: ScopeTask})

	done := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/block", strings.NewReader(`{"a":1}`))
		resp, err := rt.RoundTrip(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	<-started

	// Concurrent 2nd call sees in-flight worst-case USD reservation and is blocked immediately.
	req2, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/fast", strings.NewReader(`{"a":2}`))
	if _, err := rt.RoundTrip(req2); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded while 1st call is in flight, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("1st call failed: %v", err)
	}

	// Raise USD cap via SetMaxEstimatedUSD; 2nd admitted call succeeds, 3rd hits MaxTotalCalls=2.
	g.SetMaxEstimatedUSD(5.0)
	req3, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/fast", strings.NewReader(`{"a":3}`))
	resp3, err := rt.RoundTrip(req3)
	if err != nil {
		t.Fatalf("2nd admitted call failed: %v", err)
	}
	_ = resp3.Body.Close()
	req4, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/fast", strings.NewReader(`{"a":4}`))
	if _, err := rt.RoundTrip(req4); !errors.Is(err, ErrCallLimitExceeded) {
		t.Fatalf("expected ErrCallLimitExceeded on 3rd call, got %v", err)
	}
}
