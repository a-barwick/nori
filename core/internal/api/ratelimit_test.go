package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/api"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/users"
)

type resolveDirectory struct {
	mapDirectory
}

func (d *resolveDirectory) FindEmail(_ context.Context, email string) (users.User, error) {
	for _, u := range d.users {
		if strings.EqualFold(u.Email, email) {
			return u, nil
		}
	}
	return users.User{}, state.ErrNotFound
}

func testUser() users.User {
	return users.User{
		ID:          users.LegacyID,
		Email:       "alice@example.com",
		DisplayName: "Alice",
		TimeZone:    "UTC",
	}
}

func TestRateLimiterTokenBucketAndClockRefill(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cfg := settings.DefaultRateLimit()
	cfg.WritePerMinute = 60 // 1 req/sec
	cfg.WriteBurst = 2
	rl := api.NewRateLimiterWithClock(cfg, func() time.Time { return now })

	if ok, _ := rl.Allow(api.RateLimitWrite, "u1"); !ok {
		t.Fatal("1st request should be allowed")
	}
	if ok, _ := rl.Allow(api.RateLimitWrite, "u1"); !ok {
		t.Fatal("2nd request should be allowed")
	}
	ok, retryAfter := rl.Allow(api.RateLimitWrite, "u1")
	if ok || retryAfter < 1 {
		t.Fatalf("3rd request = (%v, %d), want (false, >=1)", ok, retryAfter)
	}
	if rl.Stats().WriteRejected != 1 {
		t.Fatalf("WriteRejected = %d, want 1", rl.Stats().WriteRejected)
	}

	// Another user is isolated.
	if ok, _ := rl.Allow(api.RateLimitWrite, "u2"); !ok {
		t.Fatal("different user should not be affected")
	}

	// Advance clock by 1 second -> 1 token refilled.
	now = now.Add(time.Second)
	if ok, _ := rl.Allow(api.RateLimitWrite, "u1"); !ok {
		t.Fatal("request after 1s refill should be allowed")
	}
}

func TestRateLimiterBoundedEviction(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cfg := settings.DefaultRateLimit()
	cfg.MaxEntries = 32 // 1 entry per shard
	cfg.ReadPerMinute = 60
	cfg.ReadBurst = 1
	cfg.IdleTTL = 10 * time.Minute
	rl := api.NewRateLimiterWithClock(cfg, func() time.Time { return now })

	// Insert many distinct keys to force shard eviction without panicking or growing unbounded.
	for i := 0; i < 200; i++ {
		now = now.Add(time.Millisecond)
		key := "user-" + string(rune('a'+i%26)) + "-" + string(rune('0'+i/26))
		if ok, _ := rl.Allow(api.RateLimitRead, key); !ok {
			t.Fatalf("fresh key %q unexpectedly rejected", key)
		}
	}
}

func TestUserHandlerRateLimitsResolveWritesUploadsReadsAndHealthBypass(t *testing.T) {
	u := testUser()
	dir := &resolveDirectory{
		mapDirectory: mapDirectory{
			users: map[string]users.User{u.ID: u},
		},
	}

	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cfg := settings.RateLimit{
		Enabled:          true,
		ResolvePerMinute: 30,
		ResolveBurst:     2,
		WritePerMinute:   60,
		WriteBurst:       2,
		UploadPerMinute:  20,
		UploadBurst:      1,
		ReadPerMinute:    300,
		ReadBurst:        3,
		MaxConcurrentSSE: 2,
		MaxEntries:       1000,
		IdleTTL:          15 * time.Minute,
	}
	rl := api.NewRateLimiterWithClock(cfg, func() time.Time { return now })

	handler := api.UserHandlerWithOptions(
		dir,
		func(_ context.Context, _ users.User) (api.UserRoutes, func(), error) {
			return api.UserRoutes{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
			}, func() {}, nil
		},
		false,
		"test-model",
		api.UserHandlerOptions{RateLimiter: rl},
	)

	// 1. POST /api/users/resolve rate-limited by IP.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/users/resolve", strings.NewReader(`{"email":"alice@example.com"}`))
		req.Header.Set("X-Forwarded-For", "203.0.113.1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("resolve #%d status = %d, want 200", i+1, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/users/resolve", strings.NewReader(`{"email":"alice@example.com"}`))
	req.Header.Set("X-Forwarded-For", "203.0.113.1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assertRateLimited429(t, rec, "rate_limited")

	// 2. Different IP targeting the same email is still rate-limited by email hash!
	req = httptest.NewRequest(http.MethodPost, "/api/users/resolve", strings.NewReader(`{"email":"ALICE@example.com"}`))
	req.Header.Set("X-Forwarded-For", "198.51.100.99")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assertRateLimited429(t, rec, "rate_limited")

	// 3. Writes (POST /v1/conversations/assistant/messages) capped at burst=2.
	for i := 0; i < 2; i++ {
		wReq := httptest.NewRequest(http.MethodPost, "/users/"+u.ID+"/v1/conversations/assistant/messages", strings.NewReader(`{}`))
		wRec := httptest.NewRecorder()
		handler.ServeHTTP(wRec, wReq)
		if wRec.Code != http.StatusOK {
			t.Fatalf("write #%d status = %d, want 200", i+1, wRec.Code)
		}
	}
	wReq := httptest.NewRequest(http.MethodPost, "/users/"+u.ID+"/v1/conversations/assistant/messages", strings.NewReader(`{}`))
	wRec := httptest.NewRecorder()
	handler.ServeHTTP(wRec, wReq)
	assertRateLimited429(t, wRec, "rate_limited")

	// 4. Uploads (POST /v1/conversations/assistant/uploads) have their own bucket (burst=1).
	uReq := httptest.NewRequest(http.MethodPost, "/users/"+u.ID+"/v1/conversations/assistant/uploads", strings.NewReader(`data`))
	uRec := httptest.NewRecorder()
	handler.ServeHTTP(uRec, uReq)
	if uRec.Code != http.StatusOK {
		t.Fatalf("upload #1 status = %d, want 200", uRec.Code)
	}
	uReq2 := httptest.NewRequest(http.MethodPost, "/users/"+u.ID+"/v1/conversations/assistant/uploads", strings.NewReader(`data`))
	uRec2 := httptest.NewRecorder()
	handler.ServeHTTP(uRec2, uReq2)
	assertRateLimited429(t, uRec2, "rate_limited")

	// 5. Reads (GET /v1/snapshot) have their own bucket (burst=3).
	for i := 0; i < 3; i++ {
		rReq := httptest.NewRequest(http.MethodGet, "/users/"+u.ID+"/v1/snapshot", nil)
		rRec := httptest.NewRecorder()
		handler.ServeHTTP(rRec, rReq)
		if rRec.Code != http.StatusOK {
			t.Fatalf("read #%d status = %d, want 200", i+1, rRec.Code)
		}
	}
	rReq := httptest.NewRequest(http.MethodGet, "/users/"+u.ID+"/v1/snapshot", nil)
	rRec := httptest.NewRecorder()
	handler.ServeHTTP(rRec, rReq)
	assertRateLimited429(t, rRec, "rate_limited")

	// 6. Health & readiness endpoints are never rate-limited.
	for i := 0; i < 10; i++ {
		hRec := httptest.NewRecorder()
		handler.ServeHTTP(hRec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if hRec.Code != http.StatusOK {
			t.Fatalf("/healthz #%d status = %d, want 200", i+1, hRec.Code)
		}
		rdRec := httptest.NewRecorder()
		handler.ServeHTTP(rdRec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rdRec.Code != http.StatusOK {
			t.Fatalf("/readyz #%d status = %d, want 200", i+1, rdRec.Code)
		}
	}
}

func TestUserHandlerConcurrentSSECap(t *testing.T) {
	u := testUser()
	dir := &resolveDirectory{
		mapDirectory: mapDirectory{
			users: map[string]users.User{u.ID: u},
		},
	}

	cfg := settings.DefaultRateLimit()
	cfg.MaxConcurrentSSE = 2
	rl := api.NewRateLimiter(cfg)

	started := make(chan struct{}, 2)
	releaseStreams := make(chan struct{})

	handler := api.UserHandlerWithOptions(
		dir,
		func(_ context.Context, _ users.User) (api.UserRoutes, func(), error) {
			return api.UserRoutes{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					started <- struct{}{}
					<-releaseStreams
					w.WriteHeader(http.StatusOK)
				}),
			}, func() {}, nil
		},
		false,
		"test-model",
		api.UserHandlerOptions{RateLimiter: rl},
	)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/users/"+u.ID+"/v1/conversations/assistant/events", nil)
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("active SSE stream status = %d, want 200", rec.Code)
			}
		}()
	}

	// Wait for both SSE streams to be active.
	<-started
	<-started
	if got := rl.Stats().ActiveSSE; got != 2 {
		t.Fatalf("ActiveSSE = %d, want 2", got)
	}

	// 3rd concurrent SSE stream must be rejected with 429.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/users/"+u.ID+"/v1/conversations/assistant/events", nil)
	handler.ServeHTTP(rec3, req3)
	assertRateLimited429(t, rec3, "sse_limit_exceeded")
	if got := rl.Stats().SSERejected; got != 1 {
		t.Fatalf("SSERejected = %d, want 1", got)
	}

	// Closing active streams frees slots immediately.
	close(releaseStreams)
	wg.Wait()
	if got := rl.Stats().ActiveSSE; got != 0 {
		t.Fatalf("ActiveSSE after close = %d, want 0", got)
	}
}

func TestUserHandlerDisableRateLimit(t *testing.T) {
	u := testUser()
	dir := &resolveDirectory{
		mapDirectory: mapDirectory{
			users: map[string]users.User{u.ID: u},
		},
	}
	handler := api.UserHandlerWithOptions(
		dir,
		func(_ context.Context, _ users.User) (api.UserRoutes, func(), error) {
			return api.UserRoutes{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
				}),
			}, func() {}, nil
		},
		false,
		"test-model",
		api.UserHandlerOptions{DisableRateLimit: true},
	)

	for i := 0; i < 100; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/users/"+u.ID+"/v1/conversations/assistant/messages", strings.NewReader(`{}`))
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request #%d with DisableRateLimit status = %d, want 200", i+1, rec.Code)
		}
	}
}

func assertRateLimited429(t *testing.T, rec *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (body=%s)", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header on 429 response")
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON body %q: %v", rec.Body.String(), err)
	}
	if body["code"] != wantCode {
		t.Fatalf("error code = %q, want %q (body=%s)", body["code"], wantCode, rec.Body.String())
	}
	if body["error"] == "" {
		t.Fatalf("missing error message in body=%s", rec.Body.String())
	}
}

func TestRateLimiterAcquireDesktopWS(t *testing.T) {
	rl := api.NewRateLimiter(settings.DefaultRateLimit())

	// max <= 0 fails closed.
	if rel, ok := rl.AcquireDesktopWS("u1", 0); ok || rel != nil {
		t.Fatal("expected max=0 to reject")
	}
	if rl.Stats().DesktopWSRejected != 1 {
		t.Fatalf("DesktopWSRejected = %d, want 1", rl.Stats().DesktopWSRejected)
	}

	// Tier with max=1 allows 1 concurrent WS, blocks 2nd, isolates other users.
	rel1, ok := rl.AcquireDesktopWS("u1", 1)
	if !ok || rel1 == nil {
		t.Fatal("expected first desktop WS slot to succeed")
	}
	if _, ok := rl.AcquireDesktopWS("u1", 1); ok {
		t.Fatal("expected second desktop WS slot for u1 (max=1) to be rejected")
	}
	rel2, ok := rl.AcquireDesktopWS("u2", 2)
	if !ok || rel2 == nil {
		t.Fatal("expected u2 first slot to succeed")
	}
	rel2b, ok := rl.AcquireDesktopWS("u2", 2)
	if !ok || rel2b == nil {
		t.Fatal("expected u2 second slot (max=2) to succeed")
	}
	if _, ok := rl.AcquireDesktopWS("u2", 2); ok {
		t.Fatal("expected u2 third slot (max=2) to be rejected")
	}
	if st := rl.Stats(); st.ActiveDesktopWS != 3 || st.DesktopWSRejected != 3 {
		t.Fatalf("unexpected stats: %+v", st)
	}

	// Release is idempotent.
	rel1()
	rel1()
	if st := rl.Stats(); st.ActiveDesktopWS != 2 {
		t.Fatalf("ActiveDesktopWS after double release = %d, want 2", st.ActiveDesktopWS)
	}
	rel1Again, ok := rl.AcquireDesktopWS("u1", 1)
	if !ok {
		t.Fatal("expected u1 slot to be available after release")
	}
	rel1Again()
	rel2()
	rel2b()
	if st := rl.Stats(); st.ActiveDesktopWS != 0 {
		t.Fatalf("ActiveDesktopWS after releasing all = %d, want 0", st.ActiveDesktopWS)
	}
}
