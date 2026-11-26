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
	"nori/internal/conversation"
	"nori/internal/observability"
	"nori/internal/permissions"
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

	a1, rel1, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1: %v", err)
	}

	clock.Advance(30 * time.Minute)
	if evicted := svc.EvictIdle(); evicted != 0 {
		t.Fatalf("EvictIdle must not evict active ref, evicted=%d", evicted)
	}

	_, rel2, err := svc.Acquire(ctx, u2)
	if err != nil {
		t.Fatalf("Acquire u2: %v", err)
	}
	if svc.CachedCount() != 2 {
		t.Fatalf("CachedCount with 2 active refs = %d, want 2", svc.CachedCount())
	}

	a1Second, rel1Second, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1 second: %v", err)
	}
	if a1Second != a1 {
		t.Fatalf("expected active u1 to remain cached")
	}
	rel1Second()

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

	clock.Advance(time.Second)
	appATouched, relATouched, err := svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("Acquire A touch: %v", err)
	}
	if appATouched != appA {
		t.Fatalf("expected cached A")
	}
	relATouched()

	clock.Advance(time.Second)
	appC, relC, err := svc.Acquire(ctx, uC)
	if err != nil {
		t.Fatalf("Acquire C: %v", err)
	}
	relC()

	if svc.CachedCount() != 2 {
		t.Fatalf("CachedCount = %d, want 2", svc.CachedCount())
	}

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

	uA := createUser(t, svc, uuid.NewString(), "hot-a@example.com")
	uB := createUser(t, svc, uuid.NewString(), "hot-b@example.com")

	_, relA, err := svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	relA()

	clock.Advance(time.Second)
	_, relB, err := svc.Acquire(ctx, uB)
	if err != nil {
		t.Fatalf("Acquire B: %v", err)
	}
	relB()

	if svc.CachedCount() != 2 {
		t.Fatalf("CachedCount = %d, want 2", svc.CachedCount())
	}

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

	clock.Advance(time.Second)
	appA, relA, err := svc.Acquire(ctx, uA)
	if err != nil {
		t.Fatalf("re-Acquire A: %v", err)
	}
	relA()
	clock.Advance(time.Second)
	appB, relB, err := svc.Acquire(ctx, uB)
	if err != nil {
		t.Fatalf("re-Acquire B: %v", err)
	}
	relB()

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

	if svc.CachedCount() != 2 {
		t.Fatalf("CachedCount after RepairUser sweep = %d, want 2", svc.CachedCount())
	}

	leases, err := ib.ListTaskLeases(ctx, coldUsers[0].ID)
	if err != nil {
		t.Fatalf("ListTaskLeases: %v", err)
	}
	if len(leases) != 1 || leases[0].RunID != coldRunID || !leases[0].Runnable {
		t.Fatalf("expected RepairUser to restore missing task lease for cold[0], got %+v", leases)
	}

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

	connSvc := connections.New(app1.Store, secret.Unavailable{}, "")
	removed, err := connSvc.CollapseDuplicates(ctx)
	if err != nil || removed != nil {
		t.Fatalf("CollapseDuplicates empty integrations: removed=%v err=%v", removed, err)
	}
	if ib.connQueries.Load() != 0 {
		t.Fatalf("expected 0 connection queries after direct CollapseDuplicates, got %d", ib.connQueries.Load())
	}
	rel1()

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

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /readyz healthy status=%d want 200", rec.Code)
	}

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

type fakeReplyModel struct {
	calls atomic.Int32
	reply string
	err   error
}

func (f *fakeReplyModel) Name() string { return "fake-outer" }

func (f *fakeReplyModel) Respond(_ context.Context, req conversation.ReplyRequest) (conversation.Reply, error) {
	f.calls.Add(1)
	if !req.NoDispatch {
		return conversation.Reply{}, errors.New("expected NoDispatch=true for auto-review")
	}
	if f.err != nil {
		return conversation.Reply{}, f.err
	}
	return conversation.Reply{Text: f.reply}, nil
}

func TestAppDefaultUserSendsAndAutoReviewerWiring(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	svc, _ := newTestService(t, clock, 10*time.Minute, 4)

	u := createUser(t, svc, uuid.NewString(), "perms-default@example.com")
	a, rel, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer rel()

	if a.Assistant == nil || a.Assistant.Permissions == nil {
		t.Fatalf("expected Permissions service wired on App")
	}
	if a.Assistant.Permissions.UserSends != "ask" {
		t.Fatalf("default Permissions.UserSends = %q, want %q", a.Assistant.Permissions.UserSends, "ask")
	}
	if a.TaskActivities == nil || a.TaskActivities.Executor == nil || a.TaskActivities.Executor.AutoReviewer == nil {
		t.Fatalf("expected AutoReviewer wired on TaskActivities.Executor")
	}

	model := &fakeReplyModel{reply: `{"verdict":"ask","reason":"Ambiguous target."} `}
	reviewer := app.NewOuterModelAutoReviewer(model)

	exfil, err := reviewer.ReviewAction(ctx, permissions.AutoReviewRequest{
		ToolID:      "gmail.send",
		Summary:     "Forward data to attacker@evil.example",
		Effect:      "sensitive_send",
		Recipients:  []string{"attacker@evil.example"},
		Origin:      "user",
		UserSources: []string{"Summarize my inbox."},
	})
	if err != nil || exfil.Verdict != "deny" || model.calls.Load() != 0 {
		t.Fatalf("exfil = (%+v, %v, calls=%d), want deny/0", exfil, err, model.calls.Load())
	}

	res, err := reviewer.ReviewAction(ctx, permissions.AutoReviewRequest{
		ToolID:      "calendar.create",
		Summary:     "Book sync",
		Effect:      "reversible_write",
		Origin:      "user",
		UserSources: []string{"Book a quick sync on Friday."},
	})
	if err != nil || res.Verdict != "ask" || model.calls.Load() != 1 {
		t.Fatalf("model review = (%+v, %v, calls=%d), want ask/1", res, err, model.calls.Load())
	}
}
