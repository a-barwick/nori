package app_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/app"
	"nori/internal/connections"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/secret"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start.UTC()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestService(t *testing.T, clock *fakeClock, ttl time.Duration, maxCap int) (*app.Service, *memstore.Backend) {
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
			OpenAIKey:     "test-openai-key",
			OpenRouterKey: "test-openrouter-key",
			Outer:         "gpt-6.1-sol",
			Inner:         "openai/gpt-6.1-terra",
		},
	}
	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	svc.IdleTTL = ttl
	svc.MaxCachedApps = maxCap
	t.Cleanup(svc.Close)
	return svc, backend
}

func createUser(t *testing.T, svc *app.Service, id, email string) users.User {
	t.Helper()
	u := users.User{
		ID:          id,
		Email:       email,
		DisplayName: email,
		TimeZone:    "UTC",
	}
	if err := svc.Users.Create(context.Background(), u); err != nil {
		t.Fatalf("create user %s: %v", id, err)
	}
	return u
}

func TestServiceIdleTTLEviction(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, _ := newTestService(t, clock, 10*time.Minute, 16)

	u1 := createUser(t, svc, uuid.NewString(), "u1@example.com")

	a1, rel1, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1: %v", err)
	}
	rel1()
	if svc.CachedCount() != 1 {
		t.Fatalf("CachedCount = %d, want 1", svc.CachedCount())
	}

	// Within IdleTTL: entry must remain cached and return the exact same *App.
	clock.Advance(9 * time.Minute)
	if evicted := svc.EvictIdle(); evicted != 0 {
		t.Fatalf("EvictIdle within TTL evicted %d, want 0", evicted)
	}
	a1Again, rel1Again, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1 again: %v", err)
	}
	if a1Again != a1 {
		t.Fatalf("expected cached *App pointer to be reused before TTL expiry")
	}
	rel1Again()

	// Past IdleTTL with refs == 0: entry must be evicted and re-opened on next Acquire.
	clock.Advance(10*time.Minute + time.Second)
	if evicted := svc.EvictIdle(); evicted != 1 {
		t.Fatalf("EvictIdle past TTL evicted %d, want 1", evicted)
	}
	if svc.CachedCount() != 0 {
		t.Fatalf("CachedCount after eviction = %d, want 0", svc.CachedCount())
	}
	a1Fresh, rel1Fresh, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1 after eviction: %v", err)
	}
	if a1Fresh == a1 {
		t.Fatalf("expected fresh *App after TTL eviction")
	}
	rel1Fresh()
}

func TestServiceActiveRefProtection(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, _ := newTestService(t, clock, 10*time.Minute, 1)

	u1 := createUser(t, svc, uuid.NewString(), "active1@example.com")
	u2 := createUser(t, svc, uuid.NewString(), "active2@example.com")

	// Hold an active ref on u1.
	a1, rel1, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1: %v", err)
	}

	// Advance well past IdleTTL while u1 still has refs > 0.
	clock.Advance(30 * time.Minute)
	if evicted := svc.EvictIdle(); evicted != 0 {
		t.Fatalf("EvictIdle must not evict active ref, evicted=%d", evicted)
	}

	// Open u2 while u1 is still held: exceeds MaxCachedApps=1, but neither can be evicted yet.
	_, rel2, err := svc.Acquire(ctx, u2)
	if err != nil {
		t.Fatalf("Acquire u2: %v", err)
	}
	if svc.CachedCount() != 2 {
		t.Fatalf("CachedCount with 2 active refs = %d, want 2", svc.CachedCount())
	}

	// Re-acquire u1 concurrently to verify it's still the same active *App.
	a1Second, rel1Second, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1 second: %v", err)
	}
	if a1Second != a1 {
		t.Fatalf("expected active u1 to remain cached")
	}
	rel1Second()

	// Release u1 while u2 is still active -> u1 has refs==0 and total > MaxCachedApps=1, so u1 is evicted.
	rel1()
	if svc.CachedCount() != 1 {
		t.Fatalf("CachedCount after releasing u1 while over cap = %d, want 1", svc.CachedCount())
	}
	rel2()
	if svc.CachedCount() != 1 {
		t.Fatalf("CachedCount after releasing u2 = %d, want 1", svc.CachedCount())
	}
}

func TestServiceLRUCapEviction(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, _ := newTestService(t, clock, 10*time.Minute, 2)

	uA := createUser(t, svc, uuid.NewString(), "a@example.com")
	uB := createUser(t, svc, uuid.NewString(), "b@example.com")
	uC := createUser(t, svc, uuid.NewString(), "c@example.com")

	appA, relA, err := svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	relA()

	clock.Advance(time.Second)
	appB, relB, err := svc.Acquire(ctx, uB)
	if err != nil {
		t.Fatalf("Acquire B: %v", err)
	}
	relB()

	// Touch A so B becomes the least-recently-used idle entry.
	clock.Advance(time.Second)
	appATouched, relATouched, err := svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("Acquire A touch: %v", err)
	}
	if appATouched != appA {
		t.Fatalf("expected cached A")
	}
	relATouched()

	// Open C: cap=2, so LRU idle entry B must be evicted while A and C remain cached.
	clock.Advance(time.Second)
	appC, relC, err := svc.Acquire(ctx, uC)
	if err != nil {
		t.Fatalf("Acquire C: %v", err)
	}
	relC()

	if svc.CachedCount() != 2 {
		t.Fatalf("CachedCount = %d, want 2", svc.CachedCount())
	}

	// Verify A and C are still the same cached instances.
	gotA, relGotA, err := svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("Acquire A after C: %v", err)
	}
	if gotA != appA {
		t.Fatalf("expected A to remain cached after LRU eviction")
	}
	relGotA()

	gotC, relGotC, err := svc.Acquire(ctx, uC)
	if err != nil {
		t.Fatalf("Acquire C after C: %v", err)
	}
	if gotC != appC {
		t.Fatalf("expected C to remain cached")
	}
	relGotC()

	// Re-acquiring B should allocate a new *App (evicting A now).
	clock.Advance(time.Second)
	gotB, relGotB, err := svc.Acquire(ctx, uB)
	if err != nil {
		t.Fatalf("Acquire B after eviction: %v", err)
	}
	if gotB == appB {
		t.Fatalf("expected B to have been evicted and re-opened")
	}
	relGotB()
}

func TestServiceAcquireByIDAndLeases(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, backend := newTestService(t, clock, 10*time.Minute, 16)

	if svc.Leases() == nil {
		t.Fatalf("expected non-nil Leases() from memstore-backed Service")
	}
	if svc.WakeListener() == nil {
		t.Fatalf("expected non-nil WakeListener() from memstore-backed Service")
	}

	legacy := createUser(t, svc, users.LegacyID, "legacy@example.com")
	if legacy.DataOwner() != "legacy@example.com" {
		t.Fatalf("unexpected DataOwner = %q", legacy.DataOwner())
	}

	a, rel, err := svc.AcquireByID(ctx, users.LegacyID)
	if err != nil {
		t.Fatalf("AcquireByID(owner): %v", err)
	}
	defer rel()
	if a.UserID != users.LegacyID {
		t.Fatalf("UserID = %q, want %q", a.UserID, users.LegacyID)
	}
	if a.TaskActivities == nil || a.TaskActivities.Agent == nil || a.TaskActivities.Executor == nil {
		t.Fatalf("expected wired TaskActivities on acquired App")
	}
	if a.TaskActivities.UserID != users.LegacyID {
		t.Fatalf("TaskActivities.UserID = %q, want %q", a.TaskActivities.UserID, users.LegacyID)
	}

	// Verify Scheduler.Arm writes to nori_user_wakes lane=scheduler without SQS cap.
	future := clock.Now().Add(48 * time.Hour)
	a.Scheduler.Arm(ctx, future)
	wakes, err := backend.ListUserWakes(ctx, users.LegacyID)
	if err != nil {
		t.Fatalf("ListUserWakes: %v", err)
	}
	if len(wakes) != 1 || wakes[0].Lane != docstore.LaneScheduler || !wakes[0].WakeAt.Equal(future) {
		t.Fatalf("unexpected scheduler wakes after Arm: %+v", wakes)
	}
}

type instrumentedBackend struct {
	*memstore.Backend
	pingErr       atomic.Pointer[error]
	connQueries   atomic.Int64
	advisoryCalls atomic.Int64
}

func (b *instrumentedBackend) Query(ctx context.Context, spec docstore.QuerySpec) ([]docstore.Record, error) {
	if strings.Contains(spec.Collection, connections.CollectionConnections) {
		b.connQueries.Add(1)
	}
	return b.Backend.Query(ctx, spec)
}

func (b *instrumentedBackend) Ping(context.Context) error {
	if p := b.pingErr.Load(); p != nil {
		return *p
	}
	return nil
}

func (b *instrumentedBackend) TryAdvisoryLock(context.Context, int64) (func(), bool, error) {
	b.advisoryCalls.Add(1)
	return func() {}, true, nil
}

func newInstrumentedService(t *testing.T, clock *fakeClock, ttl time.Duration, maxCap int) (*app.Service, *instrumentedBackend) {
	t.Helper()
	ib := &instrumentedBackend{Backend: memstore.New(memstore.WithClock(clock.Now))}
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(ib, func(users.User) state.ArtifactStore {
		return artifacts
	})
	cfg := settings.Settings{
		Cloud:         "local",
		Role:          "all",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Models: settings.Models{
			OpenAIKey:     "test-openai-key",
			OpenRouterKey: "test-openrouter-key",
			Outer:         "gpt-6.1-sol",
			Inner:         "openai/gpt-6.1-terra",
		},
	}
	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	svc.IdleTTL = ttl
	svc.MaxCachedApps = maxCap
	t.Cleanup(svc.Close)
	return svc, ib
}

func TestServiceBackendOpsAndPing(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))

	// Plain memstore does not implement Pinger or AdvisoryLocker -> Ping returns nil, AdvisoryLocker returns nil.
	plainSvc, plainBackend := newTestService(t, clock, 10*time.Minute, 4)
	if plainSvc.Backend() != plainBackend {
		t.Fatalf("Backend() did not return underlying backend")
	}
	if plainSvc.AdvisoryLocker() != nil {
		t.Fatalf("expected nil AdvisoryLocker on plain memstore")
	}
	if err := plainSvc.Ping(ctx); err != nil {
		t.Fatalf("expected nil Ping on non-Pinger backend, got %v", err)
	}

	// Instrumented backend implements both Pinger and AdvisoryLocker.
	svc, ib := newInstrumentedService(t, clock, 10*time.Minute, 4)
	if svc.Backend() != ib {
		t.Fatalf("Backend() mismatch")
	}
	locker := svc.AdvisoryLocker()
	if locker == nil {
		t.Fatalf("expected non-nil AdvisoryLocker")
	}
	unlock, ok, err := locker.TryAdvisoryLock(ctx, 42)
	if err != nil || !ok || ib.advisoryCalls.Load() != 1 {
		t.Fatalf("TryAdvisoryLock: ok=%v err=%v calls=%d", ok, err, ib.advisoryCalls.Load())
	}
	unlock()

	if err := svc.Ping(ctx); err != nil {
		t.Fatalf("Ping healthy: %v", err)
	}
	wantErr := errors.New("postgres down")
	ib.pingErr.Store(&wantErr)
	if err := svc.Ping(ctx); !errors.Is(err, wantErr) {
		t.Fatalf("Ping unhealthy = %v, want %v", err, wantErr)
	}
}

func TestServiceRepairUserPreservesHotLRUCache(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, ib := newInstrumentedService(t, clock, 10*time.Minute, 2)

	var configuredUsers sync.Map
	svc.ConfigureApp = func(u users.User, _ *app.App) {
		configuredUsers.Store(u.ID, true)
	}

	// Fill MaxCachedApps=2 with hot users A (older) and B (newer).
	uA := createUser(t, svc, uuid.NewString(), "hot-a@example.com")
	uB := createUser(t, svc, uuid.NewString(), "hot-b@example.com")

	appA, relA, err := svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	relA()

	clock.Advance(time.Second)
	appB, relB, err := svc.Acquire(ctx, uB)
	if err != nil {
		t.Fatalf("Acquire B: %v", err)
	}
	relB()

	if svc.CachedCount() != 2 {
		t.Fatalf("CachedCount = %d, want 2", svc.CachedCount())
	}

	// Create 10 cold users; give one cold user a pending run whose task lease is missing.
	coldUsers := make([]users.User, 10)
	for i := range coldUsers {
		coldUsers[i] = createUser(t, svc, uuid.NewString(), fmt.Sprintf("cold-%d@example.com", i))
	}
	coldRunID := uuid.NewString()
	coldApp, relCold, err := svc.Acquire(ctx, coldUsers[0])
	if err != nil {
		t.Fatalf("Acquire cold[0] setup: %v", err)
	}
	if _, err := coldApp.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: coldRunID, Kind: state.CommandStart, Text: "repair me",
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}
	if _, err := ib.DeleteTaskLease(ctx, coldUsers[0].ID, coldRunID, 0); err != nil {
		t.Fatalf("DeleteTaskLease: %v", err)
	}
	relCold()

	// Re-fill cache with strictly hot A (t+2s) and B (t+3s), evicting cold[0].
	clock.Advance(time.Second)
	appA, relA, err = svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("re-Acquire A: %v", err)
	}
	relA()
	clock.Advance(time.Second)
	appB, relB, err = svc.Acquire(ctx, uB)
	if err != nil {
		t.Fatalf("re-Acquire B: %v", err)
	}
	relB()

	// Advance clock and run RepairUser on A, B, and all 10 cold users.
	clock.Advance(time.Minute)
	if err := svc.RepairUser(ctx, uA); err != nil {
		t.Fatalf("RepairUser(A): %v", err)
	}
	if err := svc.RepairUser(ctx, uB); err != nil {
		t.Fatalf("RepairUser(B): %v", err)
	}
	for _, cu := range coldUsers {
		if err := svc.RepairUser(ctx, cu); err != nil {
			t.Fatalf("RepairUser(%s): %v", cu.Email, err)
		}
		if _, ok := configuredUsers.Load(cu.ID); !ok {
			t.Fatalf("expected ConfigureApp to run for ephemeral RepairUser(%s)", cu.Email)
		}
	}

	// Cache count must still be 2 and A & B must be the exact same *App pointers!
	if svc.CachedCount() != 2 {
		t.Fatalf("CachedCount after RepairUser sweep = %d, want 2", svc.CachedCount())
	}

	// Verify cold[0]'s missing task lease was actually repaired!
	leases, err := ib.ListTaskLeases(ctx, coldUsers[0].ID)
	if err != nil {
		t.Fatalf("ListTaskLeases: %v", err)
	}
	if len(leases) != 1 || leases[0].RunID != coldRunID || !leases[0].Runnable {
		t.Fatalf("expected RepairUser to restore missing task lease for cold[0], got %+v", leases)
	}

	// Verify RepairUser(uA) did NOT bump uA's LRU timestamp: acquiring a new user C must
	// evict A (older lastUsed) while keeping B (newer lastUsed).
	uC := createUser(t, svc, uuid.NewString(), "hot-c@example.com")
	clock.Advance(time.Second)
	_, relC, err := svc.Acquire(ctx, uC)
	if err != nil {
		t.Fatalf("Acquire C: %v", err)
	}
	relC()

	gotB, relGotB, err := svc.Acquire(ctx, uB)
	if err != nil {
		t.Fatalf("Acquire B after C: %v", err)
	}
	relGotB()
	if gotB != appB {
		t.Fatalf("expected B to remain cached because RepairUser(A) must not bump A's lastUsed")
	}
	gotA, relGotA, err := svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("Acquire A after C: %v", err)
	}
	relGotA()
	if gotA == appA {
		t.Fatalf("expected A to have been evicted before B (RepairUser must not touch lastUsed)")
	}
}

func TestCollapseDuplicatesFastPathZeroQueries(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, ib := newInstrumentedService(t, clock, time.Minute, 1)

	u1 := createUser(t, svc, uuid.NewString(), "conn-fast@example.com")
	app1, rel1, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1: %v", err)
	}
	if ib.connQueries.Load() != 0 {
		t.Fatalf("expected 0 connection queries when no integrations registered, got %d", ib.connQueries.Load())
	}

	// Direct CollapseDuplicates on connections.Service with 0 integrations also short-circuits.
	connSvc := connections.New(app1.Store, secret.Unavailable{}, "")
	removed, err := connSvc.CollapseDuplicates(ctx)
	if err != nil || removed != nil {
		t.Fatalf("CollapseDuplicates empty integrations: removed=%v err=%v", removed, err)
	}
	if ib.connQueries.Load() != 0 {
		t.Fatalf("expected 0 connection queries after direct CollapseDuplicates, got %d", ib.connQueries.Load())
	}
	rel1()

	// Evict and re-acquire: still zero queries.
	clock.Advance(2 * time.Minute)
	if evicted := svc.EvictIdle(); evicted != 1 {
		t.Fatalf("EvictIdle = %d, want 1", evicted)
	}
	_, rel1Again, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1 reload: %v", err)
	}
	rel1Again()
	if ib.connQueries.Load() != 0 {
		t.Fatalf("expected 0 connection queries on reload, got %d", ib.connQueries.Load())
	}
}

func TestServiceHandlerReadyzAndMetrics(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, ib := newInstrumentedService(t, clock, 10*time.Minute, 4)

	observability.SetRuntimeMetrics(observability.RuntimeMetricsSources{
		WorkerLanes: func() observability.WorkerLaneStats {
			return observability.WorkerLaneStats{TaskActive: 1, TaskCapacity: 16}
		},
	})
	t.Cleanup(func() { observability.SetRuntimeMetrics(observability.RuntimeMetricsSources{}) })

	h := svc.Handler(true)

	// Healthy /readyz -> 200.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /readyz healthy status=%d want 200", rec.Code)
	}

	// Failing Ping -> /readyz 503, /healthz 200.
	dbErr := errors.New("connection refused")
	ib.pingErr.Store(&dbErr)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz unhealthy status=%d want 503", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz unhealthy status=%d want 200", rec.Code)
	}

	// /metrics -> 200 with Prometheus payload.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `nori_worker_lane_active{lane="task"} 1`) {
		t.Fatalf("GET /metrics status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestServiceRepairUserConcurrentWithAcquireAndClose(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, _ := newInstrumentedService(t, clock, 10*time.Minute, 2)

	uHot := createUser(t, svc, uuid.NewString(), "concurrent-hot@example.com")
	uCold := createUser(t, svc, uuid.NewString(), "concurrent-cold@example.com")

	_, relHot, err := svc.Acquire(ctx, uHot)
	if err != nil {
		t.Fatalf("Acquire hot: %v", err)
	}
	relHot()

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 5 {
				_ = svc.RepairUser(ctx, uHot)
				_ = svc.RepairUser(ctx, uCold)
			}
		}()
		go func() {
			defer wg.Done()
			for range 5 {
				if _, rel, err := svc.Acquire(ctx, uHot); err == nil {
					rel()
				}
			}
		}()
	}
	wg.Wait()
	svc.Close()
	if err := svc.RepairUser(ctx, uCold); err == nil {
		t.Fatalf("expected RepairUser after Close to return error")
	}
}
