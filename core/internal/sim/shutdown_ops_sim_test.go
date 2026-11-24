package sim_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/api"
	"nori/internal/app"
	"nori/internal/httpserver"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/localfs"
	"nori/internal/task"
	"nori/internal/users"
	"nori/internal/wake"
	"nori/internal/worker"
)

type memPipeAddr string

func (a memPipeAddr) Network() string { return "mempipe" }
func (a memPipeAddr) String() string  { return string(a) }

type memPipeListener struct {
	addr   memPipeAddr
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newMemPipeListener(addr string) *memPipeListener {
	return &memPipeListener{
		addr:   memPipeAddr(addr),
		conns:  make(chan net.Conn, 16),
		closed: make(chan struct{}),
	}
}

func (l *memPipeListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case c := <-l.conns:
		return c, nil
	}
}

func (l *memPipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *memPipeListener) Addr() net.Addr { return l.addr }

func (l *memPipeListener) dialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case <-l.closed:
		_ = client.Close()
		_ = server.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	case l.conns <- server:
		return client, nil
	}
}

func TestHTTPServerGracefulShutdownDrainsInFlight(t *testing.T) {
	ln := newMemPipeListener("127.0.0.1:18080")
	addr := ln.Addr().String()

	started := make(chan struct{})
	release := make(chan struct{})
	var ctxErrDuringWait atomic.Value
	var ctxCanceledEarly atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
			ctxCanceledEarly.Store(true)
			ctxErrDuringWait.Store(r.Context().Err().Error())
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case <-release:
		}
		if err := r.Context().Err(); err != nil {
			ctxCanceledEarly.Store(true)
			ctxErrDuringWait.Store(err.Error())
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("drained"))
	})

	serverCtx, cancelServer := context.WithCancel(context.Background())
	defer cancelServer()

	srv := httpserver.New(serverCtx, addr, mux)
	if srv.WriteTimeout < 13*time.Minute {
		t.Fatalf("WriteTimeout = %s, want >= 13m", srv.WriteTimeout)
	}
	if srv.BaseContext != nil {
		t.Fatalf("expected BaseContext to be nil so parent cancellation does not abort in-flight requests")
	}

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- httpserver.Serve(serverCtx, srv, ln, 5*time.Second)
	}()

	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DialContext: ln.dialContext},
	}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}

	type slowResult struct {
		status int
		body   string
		err    error
	}
	slowRes := make(chan slowResult, 1)
	go func() {
		resp, err := client.Get("http://" + addr + "/slow")
		if err != nil {
			slowRes <- slowResult{err: err}
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		slowRes <- slowResult{status: resp.StatusCode, body: string(raw)}
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatalf("slow handler never started")
	}

	cancelServer()
	time.Sleep(100 * time.Millisecond)
	if ctxCanceledEarly.Load() {
		t.Fatalf("in-flight request context canceled prematurely on shutdown: %v", ctxErrDuringWait.Load())
	}

	close(release)

	select {
	case res := <-slowRes:
		if res.err != nil {
			t.Fatalf("in-flight request failed: %v", res.err)
		}
		if res.status != http.StatusOK || res.body != "drained" {
			t.Fatalf("in-flight response = %d %q, want 200 drained", res.status, res.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for in-flight response")
	}

	if ctxCanceledEarly.Load() {
		t.Fatalf("in-flight request observed canceled context: %v", ctxErrDuringWait.Load())
	}

	select {
	case err := <-listenErr:
		if err != nil {
			t.Fatalf("httpserver.Serve returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("httpserver.Serve did not exit after drain")
	}

	apiHandler := api.Handler(api.Deps{Model: "sim-model"})
	oversized := `{"pad":"` + strings.Repeat("a", 70<<10) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/seen", strings.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	apiHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized JSON status = %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
}

func TestLocalFSArtifactErrorParityAndNestedKeys(t *testing.T) {
	local, err := localfs.Open(t.TempDir())
	if err != nil {
		t.Fatalf("localfs.Open: %v", err)
	}
	defer func() { _ = local.Close() }()

	mem := memstore.NewArtifacts()
	stores := []struct {
		name  string
		store state.ArtifactStore
	}{
		{name: "localfs", store: local},
		{name: "memstore", store: mem},
	}

	ctx := context.Background()
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	for _, tc := range stores {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.store
			runID := uuid.NewString()
			artID := uuid.NewString()

			if _, err := s.GetPNG(ctx, runID, artID); !errors.Is(err, state.ErrNotFound) {
				t.Fatalf("missing GetPNG = %v, want state.ErrNotFound", err)
			}
			png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3}
			if err := s.PutPNG(ctx, runID, artID, png); err != nil {
				t.Fatalf("PutPNG: %v", err)
			}
			if err := s.PutPNG(ctx, runID, artID, []byte("dup")); !errors.Is(err, state.ErrFileExists) {
				t.Fatalf("duplicate PutPNG = %v, want state.ErrFileExists", err)
			}
			gotPNG, err := s.GetPNG(ctx, runID, artID)
			if err != nil || !bytes.Equal(gotPNG, png) {
				t.Fatalf("GetPNG = %v err=%v, want %v", gotPNG, err, png)
			}

			nestedKey := "nested/deep/dir/" + uuid.NewString() + ".txt"
			if _, err := s.GetFile(ctx, nestedKey); !errors.Is(err, state.ErrNotFound) {
				t.Fatalf("missing GetFile = %v, want state.ErrNotFound", err)
			}
			payload := []byte("nested-artifact-body")
			if err := s.PutFile(ctx, nestedKey, "text/plain", payload); err != nil {
				t.Fatalf("PutFile nested: %v", err)
			}
			if err := s.PutFile(ctx, nestedKey, "text/plain", []byte("dup")); !errors.Is(err, state.ErrFileExists) {
				t.Fatalf("duplicate PutFile = %v, want state.ErrFileExists", err)
			}
			gotFile, err := s.GetFile(ctx, nestedKey)
			if err != nil || !bytes.Equal(gotFile, payload) {
				t.Fatalf("GetFile nested = %q err=%v", gotFile, err)
			}

			flatKey := "flat-" + uuid.NewString() + ".bin"
			if err := s.PutFile(ctx, flatKey, "application/octet-stream", []byte("flat")); err != nil {
				t.Fatalf("PutFile flat: %v", err)
			}
			gotFlat, err := s.GetFile(ctx, flatKey)
			if err != nil || string(gotFlat) != "flat" {
				t.Fatalf("GetFile flat = %q err=%v", gotFlat, err)
			}

			if err := s.DeleteFile(ctx, nestedKey); err != nil {
				t.Fatalf("DeleteFile existing: %v", err)
			}
			if err := s.DeleteFile(ctx, nestedKey); err != nil {
				t.Fatalf("DeleteFile missing: %v", err)
			}
			if _, err := s.GetFile(ctx, nestedKey); !errors.Is(err, state.ErrNotFound) {
				t.Fatalf("GetFile after delete = %v, want state.ErrNotFound", err)
			}

			if err := s.PutPNG(canceled, runID, uuid.NewString(), png); !errors.Is(err, context.Canceled) {
				t.Fatalf("PutPNG canceled = %v, want context.Canceled", err)
			}
			if _, err := s.GetPNG(canceled, runID, artID); !errors.Is(err, context.Canceled) {
				t.Fatalf("GetPNG canceled = %v, want context.Canceled", err)
			}
			if err := s.PutFile(canceled, "c/file.txt", "text/plain", payload); !errors.Is(err, context.Canceled) {
				t.Fatalf("PutFile canceled = %v, want context.Canceled", err)
			}
			if _, err := s.GetFile(canceled, flatKey); !errors.Is(err, context.Canceled) {
				t.Fatalf("GetFile canceled = %v, want context.Canceled", err)
			}
			if err := s.DeleteFile(canceled, flatKey); !errors.Is(err, context.Canceled) {
				t.Fatalf("DeleteFile canceled = %v, want context.Canceled", err)
			}
		})
	}
}

type simClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *simClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *simClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func openTestService(t *testing.T, clock *simClock, maxCap int) (*app.Service, *memstore.Backend) {
	t.Helper()
	backend := memstore.New(memstore.WithClock(clock.Now))
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore {
		return artifacts
	})
	cfg := settings.Settings{
		Cloud:         "local",
		Role:          "all",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Models: settings.Models{
			OpenAIKey:     "sim-openai-key",
			OpenRouterKey: "sim-openrouter-key",
			Outer:         "gpt-6.1-sol",
			Inner:         "openai/gpt-6.1-terra",
		},
	}
	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	svc.IdleTTL = 10 * time.Minute
	svc.MaxCachedApps = maxCap
	t.Cleanup(svc.Close)
	return svc, backend
}

func TestWorkerPoolGracefulDrainPauseAndEviction(t *testing.T) {
	ctx := context.Background()

	t.Run("GracefulDrainReleasesLeaseWithoutAbortingBeforeTimeout", func(t *testing.T) {
		clock := &simClock{now: time.Now().UTC().Add(time.Second)}
		svc, backend := openTestService(t, clock, 16)
		u := users.User{ID: uuid.NewString(), Email: "drain-ops@example.com", TimeZone: "UTC"}
		if err := svc.Users.Create(ctx, u); err != nil {
			t.Fatalf("Users.Create: %v", err)
		}
		uApp, rel, err := svc.Acquire(ctx, u)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		defer rel()

		started := make(chan struct{})
		drained := make(chan struct{})
		var workCanceledEarly atomic.Bool

		pool, err := worker.New(svc, worker.Options{
			Owner:          "ops-drain",
			PollInterval:   20 * time.Millisecond,
			RepairInterval: time.Hour,
			DrainTimeout:   2 * time.Second,
			CancelGrace:    time.Second,
			Now:            clock.Now,
			RunTask: func(workCtx context.Context, userApp *app.App, claim docstore.TaskLease, drain <-chan struct{}) (task.RunOutcome, error) {
				close(started)
				<-drain
				if workCtx.Err() != nil {
					workCanceledEarly.Store(true)
				}
				if err := backend.ReleaseTask(workCtx, claim.UserID, claim.RunID, "ops-drain", 0, time.Time{}); err != nil {
					return task.OutcomeBackoff, err
				}
				close(drained)
				return task.OutcomeReleased, nil
			},
		})
		if err != nil {
			t.Fatalf("worker.New: %v", err)
		}

		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- pool.Run(runCtx) }()

		runID := uuid.NewString()
		if _, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: runID, Kind: state.CommandStart, Text: "graceful drain task",
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}

		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("in-flight task never started")
		}
		cancel()
		select {
		case <-drained:
		case <-time.After(2 * time.Second):
			t.Fatalf("in-flight task never saw drain signal")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("pool.Run: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("pool.Run did not finish after drain")
		}
		if workCanceledEarly.Load() {
			t.Fatalf("workCtx canceled prematurely during DrainTimeout window")
		}
		leases, err := backend.ListTaskLeases(ctx, u.ID)
		if err != nil || len(leases) != 1 || leases[0].Owner != "" || !leases[0].Runnable {
			t.Fatalf("expected released runnable lease for immediate takeover, got %+v (err=%v)", leases, err)
		}
	})

	t.Run("PausedStopCompletesWithoutPaidWorkerAndDeletesLease", func(t *testing.T) {
		clock := &simClock{now: time.Now().UTC().Add(time.Second)}
		svc, backend := openTestService(t, clock, 16)
		u := users.User{ID: uuid.NewString(), Email: "pause-ops@example.com", TimeZone: "UTC"}
		if err := svc.Users.Create(ctx, u); err != nil {
			t.Fatalf("Users.Create: %v", err)
		}
		uApp, rel, err := svc.Acquire(ctx, u)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		defer rel()

		runID := uuid.NewString()
		if _, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: runID, Kind: state.CommandStart, Text: "start before pause",
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch start: %v", err)
		}
		if err := backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "system/control",
			Fields: map[string]any{"paused": true},
		}); err != nil {
			t.Fatalf("set paused=true: %v", err)
		}
		if _, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: uuid.NewString(), RunID: runID, Kind: state.CommandStop,
		}}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch stop: %v", err)
		}

		// Resume / RepairOnce resolves paused Stop via StopPaused and removes the task lease.
		if err := uApp.Resume(ctx); err != nil {
			t.Fatalf("App.Resume while paused: %v", err)
		}
		run, err := uApp.Store.GetRun(ctx, runID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if run.Status != state.RunCancelled || run.CleanupPending {
			t.Fatalf("expected run cancelled & clean, got status=%s cleanup=%v", run.Status, run.CleanupPending)
		}
		leases, err := backend.ListTaskLeases(ctx, u.ID)
		if err != nil || len(leases) != 0 {
			t.Fatalf("expected task lease deleted after StopPaused, got %+v (err=%v)", leases, err)
		}
	})

	t.Run("AppServiceEvictsIdleAndRespectsActiveRefs", func(t *testing.T) {
		clock := &simClock{now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
		svc, _ := openTestService(t, clock, 2)

		u1 := users.User{ID: uuid.NewString(), Email: "evict1@example.com", TimeZone: "UTC"}
		u2 := users.User{ID: uuid.NewString(), Email: "evict2@example.com", TimeZone: "UTC"}
		u3 := users.User{ID: uuid.NewString(), Email: "evict3@example.com", TimeZone: "UTC"}
		for _, u := range []users.User{u1, u2, u3} {
			if err := svc.Users.Create(ctx, u); err != nil {
				t.Fatalf("Users.Create %s: %v", u.Email, err)
			}
		}

		_, rel1, err := svc.Acquire(ctx, u1)
		if err != nil {
			t.Fatalf("Acquire u1: %v", err)
		}
		clock.Advance(time.Second)
		_, rel2, err := svc.Acquire(ctx, u2)
		if err != nil {
			t.Fatalf("Acquire u2: %v", err)
		}
		rel2()

		// u1 is active (refs==1), u2 is idle (refs==0); acquiring u3 must evict u2, keeping u1 and u3.
		clock.Advance(time.Second)
		_, rel3, err := svc.Acquire(ctx, u3)
		if err != nil {
			t.Fatalf("Acquire u3: %v", err)
		}
		rel3()
		if svc.CachedCount() != 2 {
			t.Fatalf("CachedCount = %d, want 2", svc.CachedCount())
		}

		// Advance past 10m IdleTTL: u3 is evicted, u1 (still active) stays protected until released.
		clock.Advance(11 * time.Minute)
		if evicted := svc.EvictIdle(); evicted != 1 {
			t.Fatalf("EvictIdle = %d, want 1", evicted)
		}
		if svc.CachedCount() != 1 {
			t.Fatalf("CachedCount with active u1 = %d, want 1", svc.CachedCount())
		}
		rel1()
		clock.Advance(11 * time.Minute)
		if evicted := svc.EvictIdle(); evicted != 1 {
			t.Fatalf("EvictIdle after u1 release = %d, want 1", evicted)
		}
		if svc.CachedCount() != 0 {
			t.Fatalf("CachedCount = %d, want 0", svc.CachedCount())
		}
	})
}

type stubRepairWaker struct {
	convWakes   atomic.Int32
	blockConv   chan struct{}
	enteredConv chan struct{}
}

func (w *stubRepairWaker) WakeConversation(ctx context.Context) error {
	w.convWakes.Add(1)
	if w.enteredConv != nil {
		select {
		case w.enteredConv <- struct{}{}:
		default:
		}
	}
	if w.blockConv != nil {
		select {
		case <-w.blockConv:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (w *stubRepairWaker) WakeWorker(context.Context, string, ...string) error { return nil }
func (w *stubRepairWaker) WorkersNeedWake() bool                               { return false }

func TestWakeRepairDoesNotHoldLockAcrossWake(t *testing.T) {
	ctx := context.Background()
	gate := make(chan struct{})
	entered := make(chan struct{}, 2)
	waker := &stubRepairWaker{blockConv: gate, enteredConv: entered}
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	r := &wake.Repairer{
		Waker:   waker,
		Pending: func(context.Context) (string, error) { return "v1", nil },
		Now:     func() time.Time { return now },
	}

	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)
	go func() { errCh1 <- r.Repair(ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("first Repair never called WakeConversation")
	}
	go func() { errCh2 <- r.Repair(ctx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("second Repair blocked on Repairer mutex while WakeConversation was in-flight")
	}
	close(gate)
	if err := <-errCh1; err != nil {
		t.Fatalf("first Repair error: %v", err)
	}
	if err := <-errCh2; err != nil {
		t.Fatalf("second Repair error: %v", err)
	}

	beforeThrottle := waker.convWakes.Load()
	if err := r.Repair(ctx); err != nil {
		t.Fatalf("throttled Repair error: %v", err)
	}
	if waker.convWakes.Load() != beforeThrottle {
		t.Fatalf("expected same-version Repair within 1m to be throttled")
	}
}
