package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/api"
	"nori/internal/observability"
	"nori/internal/serviceauth"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

type stubDirectory struct{}

func (stubDirectory) Get(context.Context, string) (users.User, error) {
	return users.User{}, errors.New("not found")
}
func (stubDirectory) FindEmail(context.Context, string) (users.User, error) {
	return users.User{}, errors.New("not found")
}
func (stubDirectory) Create(context.Context, users.User) error   { return nil }
func (stubDirectory) List(context.Context) ([]users.User, error) { return nil, nil }
func (stubDirectory) Paused(context.Context) (bool, error)       { return false, nil }
func (stubDirectory) Close() error                               { return nil }

func TestHealthzAndReadyzProbes(t *testing.T) {
	var pingCalls atomic.Int32
	var pingErr atomic.Pointer[error]
	var blockCh atomic.Pointer[chan struct{}]

	readyCheck := func(ctx context.Context) error {
		pingCalls.Add(1)
		if ch := blockCh.Load(); ch != nil {
			select {
			case <-*ch:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if p := pingErr.Load(); p != nil {
			return *p
		}
		return nil
	}

	handler := api.UserHandlerWithOptions(
		stubDirectory{},
		func(context.Context, users.User) (api.UserRoutes, func(), error) {
			return api.UserRoutes{}, func() {}, errors.New("unexpected open")
		},
		false,
		"test-model",
		api.UserHandlerOptions{
			ReadyCheck:   readyCheck,
			ReadyTimeout: 50 * time.Millisecond,
		},
	)

	dbDown := errors.New("dial tcp 10.0.0.5:5432: connection refused")
	pingErr.Store(&dbDown)

	for _, path := range []string{"/healthz", "/api/healthz"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s with failing DB: status = %d, want 200", path, rec.Code)
		}
	}
	if pingCalls.Load() != 0 {
		t.Fatalf("/healthz must not invoke ReadyCheck, got %d calls", pingCalls.Load())
	}

	for _, path := range []string{"/readyz", "/api/readyz"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET %s with failing DB: status = %d, want 503", path, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s invalid JSON: %v (%s)", path, err, rec.Body.String())
		}
		if body["status"] != "unavailable" || body["error"] != "database unavailable" {
			t.Fatalf("GET %s body = %+v, want status=unavailable error=database unavailable", path, body)
		}
		if strings.Contains(rec.Body.String(), "10.0.0.5") {
			t.Fatalf("GET %s leaked internal DB error: %s", path, rec.Body.String())
		}

		headRec := httptest.NewRecorder()
		headReq := httptest.NewRequest(http.MethodHead, path, nil)
		handler.ServeHTTP(headRec, headReq)
		if headRec.Code != http.StatusServiceUnavailable {
			t.Fatalf("HEAD %s with failing DB: status = %d, want 503", path, headRec.Code)
		}
		if headRec.Body.Len() != 0 {
			t.Fatalf("HEAD %s expected empty body, got %q", path, headRec.Body.String())
		}
	}

	pingErr.Store(nil)
	for _, path := range []string{"/readyz", "/api/readyz"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s healthy: status = %d, want 200", path, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s invalid JSON: %v", path, err)
		}
		if body["status"] != "ok" {
			t.Fatalf("GET %s body = %+v, want status=ok", path, body)
		}

		headRec := httptest.NewRecorder()
		headReq := httptest.NewRequest(http.MethodHead, path, nil)
		handler.ServeHTTP(headRec, headReq)
		if headRec.Code != http.StatusOK {
			t.Fatalf("HEAD %s healthy: status = %d, want 200", path, headRec.Code)
		}
		if headRec.Body.Len() != 0 {
			t.Fatalf("HEAD %s expected empty body, got %q", path, headRec.Body.String())
		}
	}

	ch := make(chan struct{})
	defer close(ch)
	blockCh.Store(&ch)
	start := time.Now()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz on hung Ping: status = %d, want 503", rec.Code)
	}
	if elapsed > time.Second {
		t.Fatalf("GET /readyz timeout took too long: %s", elapsed)
	}
	blockCh.Store(nil)

	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, httptest.NewRequest(http.MethodPost, "/readyz", nil))
	if postRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /readyz status = %d, want 405", postRec.Code)
	}
}

func TestReadyzOnAPIHandlerAndOpsHandler(t *testing.T) {
	fail := false
	check := func(context.Context) error {
		if fail {
			return errors.New("down")
		}
		return nil
	}

	h := api.Handler(api.Deps{Model: "test-model", ReadyCheck: check})
	for _, path := range []string{"/readyz", "/api/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("api.Handler GET %s status = %d, want 200", path, rec.Code)
		}
		headRec := httptest.NewRecorder()
		h.ServeHTTP(headRec, httptest.NewRequest(http.MethodHead, path, nil))
		if headRec.Code != http.StatusOK {
			t.Fatalf("api.Handler HEAD %s status = %d, want 200", path, headRec.Code)
		}
	}
	fail = true
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("api.Handler GET /readyz failing status = %d, want 503", rec.Code)
	}

	collector := observability.NewMetricsCollector(observability.RuntimeMetricsSources{
		WorkerLanes: func() observability.WorkerLaneStats {
			return observability.WorkerLaneStats{TaskCapacity: 64}
		},
	})
	ops := api.OpsHandler(false, "test-model", api.UserHandlerOptions{
		ReadyCheck: check,
		Metrics:    collector.Handler(),
	})
	opsRec := httptest.NewRecorder()
	ops.ServeHTTP(opsRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if opsRec.Code != http.StatusOK || !strings.Contains(opsRec.Body.String(), `nori_worker_lane_capacity{lane="task"} 64`) {
		t.Fatalf("OpsHandler GET /metrics status=%d body=%s", opsRec.Code, opsRec.Body.String())
	}
	notFoundRec := httptest.NewRecorder()
	ops.ServeHTTP(notFoundRec, httptest.NewRequest(http.MethodGet, "/users/abc/v1/snapshot", nil))
	if notFoundRec.Code != http.StatusNotFound {
		t.Fatalf("OpsHandler unknown path status = %d, want 404", notFoundRec.Code)
	}
}

type readyzPoolStats struct{}

func (readyzPoolStats) PoolStats() docstore.PoolStats {
	return docstore.PoolStats{AcquiredConns: 1, IdleConns: 4, TotalConns: 5, MaxConns: 25}
}

func TestServiceAuthProtectReadyzHealthzAndMetrics(t *testing.T) {
	authCfg := serviceauth.Config{
		Key:      "0123456789abcdef0123456789abcdef",
		Audience: "nori",
	}
	collector := observability.NewMetricsCollector(observability.RuntimeMetricsSources{
		Pool: readyzPoolStats{},
	})
	inner := api.UserHandlerWithOptions(
		stubDirectory{},
		func(context.Context, users.User) (api.UserRoutes, func(), error) {
			return api.UserRoutes{}, func() {}, errors.New("unexpected open")
		},
		false,
		"test-model",
		api.UserHandlerOptions{
			ReadyCheck: func(context.Context) error { return nil },
			Metrics:    collector.Handler(),
		},
	)
	protected := authCfg.Protect(inner)

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/healthz"},
		{http.MethodHead, "/healthz"},
		{http.MethodGet, "/readyz"},
		{http.MethodHead, "/readyz"},
	} {
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("unauthenticated %s %s: status = %d, want 200", tc.method, tc.path, rec.Code)
		}
	}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/healthz"},
		{http.MethodGet, "/api/readyz"},
		{http.MethodPost, "/api/users/resolve"},
		{http.MethodPost, "/api/resume"},
		{http.MethodGet, "/users/11111111-1111-4111-8111-111111111111/v1/snapshot"},
		{http.MethodGet, "/metrics"},
	} {
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s: status = %d, want 401", tc.method, tc.path, rec.Code)
		}
	}

	token, err := authCfg.Token()
	if err != nil {
		t.Fatalf("authCfg.Token: %v", err)
	}
	for _, tc := range []struct {
		path    string
		contain string
	}{
		{"/api/readyz", `"status":"ok"`},
		{"/metrics", "nori_postgres_pool_max_conns 25"},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		protected.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("authenticated GET %s: status = %d, want 200 (body=%s)", tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.contain) {
			t.Fatalf("authenticated GET %s: body missing %q: %s", tc.path, tc.contain, rec.Body.String())
		}
	}
}
