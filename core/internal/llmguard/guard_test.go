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

	for i := range 3 {
		body := strings.NewReader(`{"prompt":"` + strings.Repeat("x", 4000) + `","seq":` + string(rune('0'+i)) + `}`)
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", body)
		if _, err := rt.RoundTrip(req); !errors.Is(err, context.Canceled) {
			t.Fatalf("abort call %d: want context.Canceled, got %v", i+1, err)
		}
	}

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
		AbortBreakerThreshold: 100,
		MaxEstimatedUSD:       0.025,
	})
	rt := g.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return nil, context.Canceled
	}}, CallMeta{UserID: "u-budget", Scope: ScopeTask})

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

	req2, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"req":2}`))
	_, err := client.Do(req2)
	if !errors.Is(err, ErrConcurrencyLimited) {
		t.Fatalf("expected ErrConcurrencyLimited for 2nd concurrent compaction, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first call failed: %v", err)
	}

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
		MaxEstimatedUSD: 0.015,
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

	req2, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/fast", strings.NewReader(`{"a":2}`))
	if _, err := rt.RoundTrip(req2); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded while 1st call is in flight, got %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("1st call failed: %v", err)
	}

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

type interruptedResponse struct{}

func (interruptedResponse) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (interruptedResponse) Close() error             { return nil }

func TestSuccessfulHeadersSettleExactlyOneOutcome(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  io.ReadCloser
		known bool
		abort bool
	}{
		{"interrupted stream", interruptedResponse{}, false, true},
		{"completed response without usage", io.NopCloser(strings.NewReader("{}")), false, false},
		{"reported zero cost", io.NopCloser(strings.NewReader("{}")), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := New(DefaultConfig())
			client := guard.WrapClient(&http.Client{Transport: fakeRT{fn: func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: tc.body}, nil
			}}}, time.Second, CallMeta{Model: "gpt-6.1-sol"})
			req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1/model", strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			exposure := guard.Stats().InFlightEstimatedUSD
			if exposure <= 0 {
				t.Fatalf("missing reservation: %+v", guard.Stats())
			}
			_, err = io.ReadAll(resp.Body)
			if (err != nil) != tc.abort {
				t.Fatalf("unexpected response error: %v", err)
			}
			if tc.known {
				RecordResponseCost(resp, 0)
			}
			_ = resp.Body.Close()
			_ = resp.Body.Close()
			st := guard.Stats()
			if tc.known {
				exposure = 0
			}
			if st.InFlightEstimatedUSD != 0 || st.EstimatedUnknownUSD != exposure || (st.UnknownAborts == 1) != tc.abort {
				t.Fatalf("incorrect settlement: %+v, want estimated USD %g", st, exposure)
			}
		})
	}
}

func TestForUser_IsolatesCircuitBreakerAndSpendLimit(t *testing.T) {
	if ForUser("") != Default() {
		t.Fatal("expected ForUser(\"\") to fall back to Default()")
	}

	u1 := "test-iso-user-1-" + t.Name()
	u2 := "test-iso-user-2-" + t.Name()
	g1 := ForUser(u1)
	g2 := ForUser(u2)
	if g1 != ForUser(u1) {
		t.Fatal("expected ForUser to return the same Guard for the same userID")
	}
	if g1 == g2 {
		t.Fatal("expected distinct Guard instances for distinct users")
	}

	// 1. Trip user 1's circuit breaker via HTTP 402; user 2 must remain unaffected.
	rt1Fail := g1.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusPaymentRequired,
			Body:       io.NopCloser(strings.NewReader(`{"error":"payment required"}`)),
			Header:     http.Header{},
		}, nil
	}}, CallMeta{UserID: u1, Scope: ScopeOuter})
	req1, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat/completions", strings.NewReader(`{"u":1,"n":1}`))
	resp1, err := rt1Fail.RoundTrip(req1)
	if err != nil {
		t.Fatalf("unexpected transport error on 402: %v", err)
	}
	_ = resp1.Body.Close()

	req1Blocked, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat/completions", strings.NewReader(`{"u":1,"n":2}`))
	if _, err := rt1Fail.RoundTrip(req1Blocked); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected user 1 to hit ErrCircuitOpen, got %v", err)
	}

	rt2OK := g2.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return okResp(), nil
	}}, CallMeta{UserID: u2, Scope: ScopeOuter})
	req2, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat/completions", strings.NewReader(`{"u":2,"n":1}`))
	resp2, err := rt2OK.RoundTrip(req2)
	if err != nil {
		t.Fatalf("expected user 2 to succeed despite user 1 circuit open, got %v", err)
	}
	_ = resp2.Body.Close()

	// 2. Exhaust spend limit on a third user; user 2 must remain unaffected.
	u3 := "test-iso-user-3-" + t.Name()
	g3 := ForUser(u3)
	g3.SetMaxEstimatedUSD(0.001)
	rt3 := g3.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return okResp(), nil
	}}, CallMeta{UserID: u3, Scope: ScopeOuter})
	req3, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat/completions", strings.NewReader(`{"u":3,"n":1}`))
	if _, err := rt3.RoundTrip(req3); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected user 3 to hit ErrBudgetExceeded, got %v", err)
	}

	req2Again, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat/completions", strings.NewReader(`{"u":2,"n":2}`))
	resp2Again, err := rt2OK.RoundTrip(req2Again)
	if err != nil {
		t.Fatalf("expected user 2 to succeed despite user 3 budget exceeded, got %v", err)
	}
	_ = resp2Again.Body.Close()
}

func TestForUser_EnforcesDefaultGlobalBudgetWhileIsolatingCircuitBreaker(t *testing.T) {
	def := Default()
	t.Cleanup(func() {
		def.SetMaxEstimatedUSD(0)
	})

	uA := "test-global-user-a-" + t.Name()
	uB := "test-global-user-b-" + t.Name()
	gA := ForUser(uA)
	gB := ForUser(uB)

	// Set a tight process-wide spend ceiling on Default() relative to current accumulated stats.
	baseStats := def.Stats()
	baseSpend := baseStats.RecordedActualUSD + baseStats.EstimatedUnknownUSD
	def.SetMaxEstimatedUSD(baseSpend + 0.025)

	rtA := gA.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return okResp(), nil
	}}, CallMeta{UserID: uA, Scope: ScopeOuter})
	rtB := gB.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return okResp(), nil
	}}, CallMeta{UserID: uB, Scope: ScopeOuter})

	// User A makes a call and records actual cost that consumes most of the shared global budget.
	reqA1, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat/completions", strings.NewReader(`{"u":"a","n":1}`))
	respA1, err := rtA.RoundTrip(reqA1)
	if err != nil {
		t.Fatalf("user A first call failed: %v", err)
	}
	_ = respA1.Body.Close()
	gA.RecordActualCost(0.020)

	// User B's call would push combined global spend over Default().MaxEstimatedUSD and must be blocked.
	reqB1, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat/completions", strings.NewReader(`{"u":"b","n":1}`))
	if _, err := rtB.RoundTrip(reqB1); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected user B to hit ErrBudgetExceeded from shared Default() budget, got %v", err)
	}

	// Lift the global budget cap, then trip user A's circuit breaker; user B must remain unaffected.
	def.SetMaxEstimatedUSD(0)

	rtAFail := gA.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusPaymentRequired,
			Body:       io.NopCloser(strings.NewReader(`{"error":"payment required"}`)),
			Header:     http.Header{},
		}, nil
	}}, CallMeta{UserID: uA, Scope: ScopeOuter})
	reqA402, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat/completions", strings.NewReader(`{"u":"a","n":2}`))
	respA402, err := rtAFail.RoundTrip(reqA402)
	if err != nil {
		t.Fatalf("unexpected transport error on 402: %v", err)
	}
	_ = respA402.Body.Close()

	reqABlocked, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat/completions", strings.NewReader(`{"u":"a","n":3}`))
	if _, err := rtAFail.RoundTrip(reqABlocked); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected user A to hit ErrCircuitOpen, got %v", err)
	}

	reqB2, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat/completions", strings.NewReader(`{"u":"b","n":2}`))
	respB2, err := rtB.RoundTrip(reqB2)
	if err != nil {
		t.Fatalf("expected user B to succeed despite user A circuit open, got %v", err)
	}
	_ = respB2.Body.Close()
}

type customWrapperRT struct {
	inner http.RoundTripper
}

func (w customWrapperRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return w.inner.RoundTrip(req)
}

func TestGuard_HalfOpenSingleProbeAndCounterReset(t *testing.T) {
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	g := New(Config{
		Now:                   func() time.Time { return now },
		AbortBreakerThreshold: 2,
		BreakerBaseCooldown:   30 * time.Second,
	})

	probeStarted := make(chan struct{})
	probeRelease := make(chan struct{})
	var phase atomic.Int32 // 0=abort, 1=block-probe-then-abort, 2=abort-once

	rt := g.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		switch phase.Load() {
		case 1:
			close(probeStarted)
			<-probeRelease
			return nil, context.Canceled
		default:
			return nil, context.Canceled
		}
	}}, CallMeta{UserID: "u-halfopen", Scope: ScopeTask})

	for i := range 2 {
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"n":`+string(rune('0'+i))+`}`))
		if _, err := rt.RoundTrip(req); !errors.Is(err, context.Canceled) {
			t.Fatalf("call %d: want context.Canceled, got %v", i, err)
		}
	}
	reqOpen, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"n":2}`))
	if _, err := rt.RoundTrip(reqOpen); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}

	// Expire cooldown: only one half-open probe may enter concurrently.
	now = now.Add(31 * time.Second)
	phase.Store(1)
	probeDone := make(chan error, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"n":3}`))
		_, err := rt.RoundTrip(req)
		probeDone <- err
	}()
	<-probeStarted

	reqConcurrent, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"n":4}`))
	if _, err := rt.RoundTrip(reqConcurrent); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected concurrent call during half-open probe to fail with ErrCircuitOpen, got %v", err)
	}
	close(probeRelease)
	if err := <-probeDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("half-open probe err = %v, want context.Canceled", err)
	}

	// Because consecutiveAborts was reset on cooldown expiry, 1 failed probe
	// must NOT immediately re-open the breaker when threshold=2.
	phase.Store(2)
	reqNext, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"n":5}`))
	if _, err := rt.RoundTrip(reqNext); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected 2nd call after half-open reset to be admitted, got %v", err)
	}
	// Now 2 consecutive aborts have occurred again -> breaker re-opens.
	reqReopened, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/responses", strings.NewReader(`{"n":6}`))
	if _, err := rt.RoundTrip(reqReopened); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen after threshold reached again, got %v", err)
	}
}

func TestGuard_ErrDispatchBlockedRollsBackQuotas(t *testing.T) {
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	parent := New(Config{
		Now:                func() time.Time { return now },
		MaxTotalCalls:      1,
		GlobalMaxPerMinute: 1,
	})
	child := New(Config{
		Now:                func() time.Time { return now },
		MaxTotalCalls:      1,
		GlobalMaxPerMinute: 1,
		Scopes: map[string]ScopePolicy{
			ScopeOuter: {MaxPerMinute: 1, MaxPerHour: 1, MaxInFlight: 1},
		},
	})
	child.parent = parent

	block := true
	rt := child.Transport(dispatchTransport{
		base: fakeRT{fn: func(*http.Request) (*http.Response, error) { return okResp(), nil }},
		check: func(context.Context) error {
			if block {
				return errors.New("not allowed")
			}
			return nil
		},
	}, CallMeta{UserID: "u-roll", Scope: ScopeOuter})

	req1, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat", strings.NewReader(`{"a":1}`))
	if _, err := rt.RoundTrip(req1); !errors.Is(err, ErrDispatchBlocked) {
		t.Fatalf("expected ErrDispatchBlocked, got %v", err)
	}
	if st := child.Stats(); st.TotalAdmitted != 0 {
		t.Fatalf("child TotalAdmitted = %d, want 0 after rollback", st.TotalAdmitted)
	}
	if st := parent.Stats(); st.TotalAdmitted != 0 {
		t.Fatalf("parent TotalAdmitted = %d, want 0 after rollback", st.TotalAdmitted)
	}

	// The subsequent allowed call must succeed within the 1-call quota.
	block = false
	req2, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat", strings.NewReader(`{"a":1}`))
	resp, err := rt.RoundTrip(req2)
	if err != nil {
		t.Fatalf("expected call after dispatch rollback to succeed, got %v", err)
	}
	_ = resp.Body.Close()
}

func TestGuard_UnwrapsArbitraryRoundTripperForTestGuard(t *testing.T) {
	if !isRealNetworkTransport(customWrapperRT{inner: http.DefaultTransport}) {
		t.Fatal("expected custom wrapper around http.DefaultTransport to be detected as real network transport")
	}
	if isRealNetworkTransport(customWrapperRT{inner: fakeRT{fn: func(*http.Request) (*http.Response, error) { return okResp(), nil }}}) {
		t.Fatal("expected custom wrapper around fakeRT not to be treated as real network transport")
	}
}

func TestGuard_MeteredBodyConcurrentRecordCostAndClose(t *testing.T) {
	g := New(DefaultConfig())
	client := g.WrapClient(&http.Client{Transport: fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return okResp(), nil
	}}}, time.Second, CallMeta{Model: "gpt-6.1-sol"})
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/model", strings.NewReader("{}"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		RecordResponseCost(resp, 0.005)
		close(done)
	}()
	_ = resp.Body.Close()
	<-done
}

func TestGuard_Rolling24hWindowResetsHostedSpend(t *testing.T) {
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	g := New(Config{
		Now:             func() time.Time { return now },
		MaxEstimatedUSD: 0.025,
	})
	rt := g.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return okResp(), nil
	}}, CallMeta{UserID: "u-roll24", Scope: ScopeSearch, Provider: "exa", Billing: "hosted"})

	// First Exa call ($0.013) succeeds.
	req1, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/search", strings.NewReader(`{"q":"1"}`))
	resp1, err := rt.RoundTrip(req1)
	if err != nil {
		t.Fatalf("first hosted search failed: %v", err)
	}
	RecordResponseCost(resp1, 0.013)
	_ = resp1.Body.Close()

	// Second Exa call ($0.013 + $0.013 = $0.026 > $0.025) is blocked.
	req2, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/search", strings.NewReader(`{"q":"2"}`))
	if _, err := rt.RoundTrip(req2); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded within 24h window, got %v", err)
	}

	// Advancing 23h59m still blocks.
	now = now.Add(23*time.Hour + 59*time.Minute)
	req3, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/search", strings.NewReader(`{"q":"3"}`))
	if _, err := rt.RoundTrip(req3); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected ErrBudgetExceeded at 23h59m, got %v", err)
	}

	// Advancing past 24h expires the old entry and admits a new call.
	now = now.Add(2 * time.Minute)
	if st := g.Stats(); st.RecordedActualUSD != 0 {
		t.Fatalf("expected rolling 24h spend to decay to 0, got %+v", st)
	}
	req4, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/search", strings.NewReader(`{"q":"4"}`))
	resp4, err := rt.RoundTrip(req4)
	if err != nil {
		t.Fatalf("expected call after 24h window to succeed, got %v", err)
	}
	_ = resp4.Body.Close()
}

func TestGuard_SpendCapScopedStrictlyToHosted(t *testing.T) {
	now := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	g := New(Config{
		Now:             func() time.Time { return now },
		MaxEstimatedUSD: 0.02,
	})

	// Exhaust hosted budget completely.
	g.RecordActualCost(0.02)

	// Hosted call is blocked.
	rtHosted := g.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return okResp(), nil
	}}, CallMeta{UserID: "u-scope", Scope: ScopeOuter, Billing: "hosted"})
	reqH, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat", strings.NewReader(`{"h":1}`))
	if _, err := rtHosted.RoundTrip(reqH); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("expected hosted call blocked by spend cap, got %v", err)
	}

	// BYOK call is NOT blocked and does NOT inflate hosted spend stats.
	rtBYOK := g.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return okResp(), nil
	}}, CallMeta{UserID: "u-scope", Scope: ScopeOuter, Billing: "byok", BYOK: true})
	reqB, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat", strings.NewReader(`{"b":1}`))
	respB, err := rtBYOK.RoundTrip(reqB)
	if err != nil {
		t.Fatalf("expected BYOK call to bypass hosted spend cap, got %v", err)
	}
	RecordResponseCost(respB, 5.00)
	_ = respB.Body.Close()

	// Local call is NOT blocked either.
	rtLocal := g.Transport(fakeRT{fn: func(*http.Request) (*http.Response, error) {
		return okResp(), nil
	}}, CallMeta{UserID: "u-scope", Scope: ScopeOuter, Provider: ProviderHome, Billing: "local"})
	reqL, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:9999/v1/chat", strings.NewReader(`{"l":1}`))
	respL, err := rtLocal.RoundTrip(reqL)
	if err != nil {
		t.Fatalf("expected local call to bypass hosted spend cap, got %v", err)
	}
	_ = respL.Body.Close()

	if st := g.Stats(); st.RecordedActualUSD != 0.02 || st.EstimatedUnknownUSD != 0 {
		t.Fatalf("expected BYOK/local spend not to pollute hosted spend stats, got %+v", st)
	}
}
