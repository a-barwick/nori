package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/billing"
	"nori/internal/permissions"
	"nori/internal/platform"
	"nori/internal/scheduler"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestEntitlementCacheTTLFallbackAndNotifyEviction(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	backend := memstore.New(memstore.WithClock(clock))
	t.Cleanup(func() { _ = backend.Close() })

	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore { return memstore.NewArtifacts() })
	storage.Now = clock
	cfg := settings.Settings{
		Cloud:                 "local",
		EnvProfile:            "test",
		TimeZone:              "UTC",
		TaskBudgetUSD:         2.0,
		TrialMaxSignupsPerDay: 25,
		Features:              settings.Features{DisableJev: true},
		Connections: settings.Connections{
			CredentialStore: "database",
			CredentialKey:   hex.EncodeToString(bytes.Repeat([]byte{0x42}, 32)),
		},
		Models: settings.Models{BYOKRequired: true},
	}
	svc := OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock
	t.Cleanup(svc.Close)

	u := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", DisplayName: "Alice", TimeZone: "UTC"}
	if err := svc.Users.Create(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Acquire userApp so it sits in cache.
	_, release, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()
	if svc.CachedCount() != 1 {
		t.Fatalf("CachedCount = %d, want 1", svc.CachedCount())
	}

	// Prime entitlement cache.
	ent, err := svc.GetEntitlement(ctx, u.ID)
	if err != nil || ent.Tier != billing.TierTrial || ent.IsSuspended() {
		t.Fatalf("initial entitlement = %+v err=%v", ent, err)
	}

	// Suspend directly in backing store without calling HandleEntitlementChanged yet.
	if _, err := svc.Billing().Suspend(ctx, u.ID, "chargeback"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}

	// Within 15s fallback TTL, cache still serves previous non-suspended snapshot until evicted.
	now = now.Add(5 * time.Second)
	entCached, err := svc.GetEntitlement(ctx, u.ID)
	if err != nil || entCached.IsSuspended() {
		t.Fatalf("expected cached entitlement before 15s TTL, got %+v err=%v", entCached, err)
	}

	// Advance past 15s fallback TTL: fresh read picks up suspension even if notify dropped!
	now = now.Add(11 * time.Second)
	entFresh, err := svc.GetEntitlement(ctx, u.ID)
	if err != nil || !entFresh.IsSuspended() {
		t.Fatalf("expected suspended entitlement after 15s TTL fallback, got %+v err=%v", entFresh, err)
	}

	// Unsuspend then suspend again and invoke HandleEntitlementChanged -> immediate eviction + hook dispatch.
	if _, err := svc.Billing().Unsuspend(ctx, u.ID); err != nil {
		t.Fatalf("Unsuspend: %v", err)
	}
	svc.HandleEntitlementChanged(ctx, u.ID)
	// Re-acquire userApp and re-prime cache as unsuspended.
	_, release, err = svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire #2: %v", err)
	}
	release()

	var desktopClosed, workCanceled, computerSlept atomic.Int32
	svc.SetDisconnectDesktop(func(uid string) {
		if uid == u.ID {
			desktopClosed.Add(1)
		}
	})
	svc.SetCancelUserWork(func(uid string) {
		if uid == u.ID {
			workCanceled.Add(1)
		}
	})
	svc.SetSleepUserComputer(func(_ context.Context, uid string) error {
		if uid == u.ID {
			computerSlept.Add(1)
		}
		return nil
	})

	if _, err := svc.Billing().Suspend(ctx, u.ID, "abuse"); err != nil {
		t.Fatalf("Suspend #2: %v", err)
	}
	svc.HandleEntitlementChanged(ctx, u.ID)

	if svc.CachedCount() != 0 {
		t.Fatalf("CachedCount after HandleEntitlementChanged = %d, want 0", svc.CachedCount())
	}
	if desktopClosed.Load() != 1 || workCanceled.Load() != 1 || computerSlept.Load() != 1 {
		t.Fatalf("hooks called desktop=%d work=%d sleep=%d, want 1/1/1", desktopClosed.Load(), workCanceled.Load(), computerSlept.Load())
	}
	if !svc.IsUserSuspended(ctx, u.ID) {
		t.Fatalf("expected IsUserSuspended=true immediately after HandleEntitlementChanged")
	}
}

func TestPauseExcessRoutinesNewestFirst(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	backend := memstore.New(memstore.WithClock(func() time.Time { return now }))
	t.Cleanup(func() { _ = backend.Close() })

	u := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", TimeZone: "UTC"}
	db := records.NewShared(backend, u, 5.0)
	db.Now = func() time.Time { return now }
	store := state.Compose(db, memstore.NewArtifacts())
	perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow", Now: func() time.Time { return now }}
	sched := &scheduler.Service{
		Store: store, Tasks: store, Permissions: perms,
		Timezone: func(context.Context) string { return "UTC" },
		Now:      func() time.Time { return now },
	}

	// Create 4 routines spaced 1 minute apart.
	titles := []string{"oldest-1", "second-2", "third-3", "newest-4"}
	for _, title := range titles {
		now = now.Add(time.Minute)
		receipt, err := sched.Save(ctx, "", uuid.NewString(), 0, scheduler.Editable{
			Title:                  title,
			Instruction:            "Do " + title,
			Trigger:                scheduler.Trigger{Kind: "recurring", LocalStart: "2026-11-28T08:00", Timezone: "UTC", RRule: "FREQ=DAILY"},
			ConnectionIDs:          []string{},
			BudgetUSD:              0.1,
			MaxActiveRuns:          1,
			MissedRun:              "skip",
			ExecutionWindowSeconds: 300,
			DestinationIDs:         []string{scheduler.InAppDestination},
		})
		if err != nil {
			t.Fatalf("Save %s: %v", title, err)
		}
		if receipt.Status != "applied" {
			t.Fatalf("Save %s receipt status = %q", title, receipt.Status)
		}
	}

	paused, err := PauseExcessRoutines(ctx, sched, 2)
	if err != nil {
		t.Fatalf("PauseExcessRoutines: %v", err)
	}
	if paused != 2 {
		t.Fatalf("paused = %d, want 2", paused)
	}
	enabled, err := sched.List(ctx, string(scheduler.AutomationEnabled))
	if err != nil {
		t.Fatalf("List enabled: %v", err)
	}
	if len(enabled) != 2 {
		t.Fatalf("enabled count = %d, want 2", len(enabled))
	}
	gotTitles := map[string]bool{}
	for _, a := range enabled {
		gotTitles[a.Title] = true
	}
	if !gotTitles["oldest-1"] || !gotTitles["second-2"] {
		t.Fatalf("expected oldest two routines to remain enabled, got %v", gotTitles)
	}
}
