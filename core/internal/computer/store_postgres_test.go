package computer_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/billing"
	"nori/internal/computer"
	"nori/internal/computer/fake"
	"nori/internal/state/postgres"
)

func TestPostgresStoreAndAccountDeleteIntegration(t *testing.T) {
	dsn := os.Getenv("NORI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NORI_TEST_POSTGRES_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Opening the postgres docstore backend runs all embedded migrations including 014_user_computers.sql.
	rawBackend, err := postgres.OpenBackend(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenBackend: %v", err)
	}
	t.Cleanup(func() { _ = rawBackend.Close() })

	store, err := computer.OpenPostgresStore(ctx, dsn, 4)
	if err != nil {
		t.Fatalf("OpenPostgresStore: %v", err)
	}
	t.Cleanup(store.Close)

	fp := fake.New()
	mgr := computer.NewManager(fp, store, computer.Config{MemoryLimitMB: 3072, MaxDiskGB: 10})
	userID := uuid.NewString()

	rec, err := mgr.Ensure(ctx, userID)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if rec.ProviderID == "" || rec.BaselineCheckpointID == "" {
		t.Fatalf("unexpected computer record: %+v", rec)
	}

	// Create session, toggle mode, validate, and end.
	created, err := mgr.CreateSession(ctx, userID, computer.SessionModeView, "task-pg-1", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	validated, err := mgr.ValidateSession(ctx, userID, created.Session.ID, created.RawToken)
	if err != nil || validated.Mode != computer.SessionModeView {
		t.Fatalf("ValidateSession = (%+v, %v)", validated, err)
	}
	if _, err := mgr.SetSessionMode(ctx, userID, created.Session.ID, computer.SessionModeControl); err != nil {
		t.Fatalf("SetSessionMode(control): %v", err)
	}
	if _, err := mgr.SetSessionMode(ctx, userID, created.Session.ID, computer.SessionModeView); err != nil {
		t.Fatalf("SetSessionMode(view): %v", err)
	}

	// Link workspace file to a Library result and entity tag, then purge by result ID.
	if err := mgr.WriteFile(ctx, userID, "out/report.pdf", []byte("%PDF-1.4"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := mgr.LinkFile(ctx, userID, "out/report.pdf", "res-pg-1", []string{"Project Alpha"}); err != nil {
		t.Fatalf("LinkFile: %v", err)
	}
	if err := mgr.PurgeLinkedResultFiles(ctx, userID, "res-pg-1"); err != nil {
		t.Fatalf("PurgeLinkedResultFiles: %v", err)
	}
	if _, err := mgr.ReadFile(ctx, userID, "out/report.pdf", 1024); !errors.Is(err, computer.ErrNotFound) {
		t.Fatalf("expected file purged from workspace, got %v", err)
	}

	// Simulate account delete teardown + reaper recovery.
	fp.Faults.DestroyErr = errors.New("temporary network blip")
	if err := mgr.DestroyForAccountDelete(ctx, userID); err == nil {
		t.Fatalf("expected DestroyForAccountDelete error under fault")
	}
	// Session must already be revoked when marked deleting.
	if _, err := mgr.ValidateSession(ctx, userID, created.Session.ID, created.RawToken); !errors.Is(err, computer.ErrSessionRevoked) {
		t.Fatalf("expected ErrSessionRevoked after MarkComputerDeleting, got %v", err)
	}
	// Clear fault and reap.
	fp.Faults.DestroyErr = nil
	if err := mgr.ReapDeletingComputers(ctx); err != nil {
		t.Fatalf("ReapDeletingComputers: %v", err)
	}
	after, err := store.GetComputer(ctx, userID)
	if err != nil || after.Status != computer.StatusDeleted {
		t.Fatalf("after reap = (%+v, %v), want StatusDeleted", after, err)
	}
}

func TestPostgresSingleTxAwakeMeteringAndSingleUseToken(t *testing.T) {
	dsn := os.Getenv("NORI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("NORI_TEST_POSTGRES_URL is unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rawBackend, err := postgres.OpenBackend(ctx, dsn)
	if err != nil {
		t.Fatalf("OpenBackend: %v", err)
	}
	t.Cleanup(func() { _ = rawBackend.Close() })

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	store := computer.NewPostgresStore(pool)
	bs := billing.NewPostgresStore(pool)
	bsvc := billing.NewService(bs, false, billing.GlobalBudgetConfig{})

	var clockMu sync.Mutex
	now := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}
	bs.Now = nowFn
	bsvc.Now = nowFn

	fp := fake.New()
	fp.Now = nowFn
	mgr := computer.NewManager(fp, store, computer.Config{
		RatePerHourUSD: 0.15,
		IdleAutoSleep:  15 * time.Minute,
	})
	mgr.SetClock(nowFn)
	mgr.SetBilling(bsvc)

	userID := "pg-sprite-" + uuid.NewString()[:8]
	if _, err := bs.SetTier(ctx, userID, billing.TierStandard, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier: %v", err)
	}

	if _, err := mgr.Ensure(ctx, userID); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Single-use session token atomicity in Postgres.
	created, err := mgr.CreateSession(ctx, userID, computer.SessionModeView, "", "")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := mgr.ConsumeSession(ctx, userID, created.Session.ID, created.RawToken); err != nil {
		t.Fatalf("ConsumeSession #1: %v", err)
	}
	if _, err := mgr.ConsumeSession(ctx, userID, created.Session.ID, created.RawToken); !errors.Is(err, computer.ErrSessionTokenUsed) {
		t.Fatalf("ConsumeSession replay = %v, want ErrSessionTokenUsed", err)
	}

	// Advance 12m and race 6 concurrent MeterAwakeTime / Sleep / Janitor calls against Postgres FOR UPDATE.
	// Exact cost for 12m @ $0.15/hr = $0.03 = 30,000 micros.
	advance(12 * time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = store.MeterAwakeTime(ctx, computer.MeterAwakeRequest{
				UserID:         userID,
				Now:            nowFn(),
				RatePerHourUSD: 0.15,
			})
		}()
		go func() {
			defer wg.Done()
			_ = mgr.Sleep(ctx, userID)
		}()
	}
	wg.Wait()

	bal, err := bs.CreditBalance(ctx, userID)
	if err != nil {
		t.Fatalf("CreditBalance: %v", err)
	}
	if bal.SpentMicros != 30_000 {
		t.Fatalf("Postgres spent micros after concurrent metering+Sleep = %d, want exactly 30000", bal.SpentMicros)
	}
	gbSprites, err := bs.GetGlobalBudget(ctx, nowFn(), billing.ScopeHostedSprites)
	if err != nil {
		t.Fatalf("GetGlobalBudget: %v", err)
	}
	if gbSprites.SpentMicros < 30_000 {
		t.Fatalf("nori_global_budget_daily hosted_sprites = %d, want >= 30000", gbSprites.SpentMicros)
	}
}
