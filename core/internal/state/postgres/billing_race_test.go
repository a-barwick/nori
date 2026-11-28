package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/billing"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
)

// TestPostgresBillingConcurrentOneCentRace verifies that N goroutines racing against
// $0.01 remaining hosted credit in real Postgres serialize cleanly via SELECT ... FOR UPDATE
// on nori_user_entitlements, admitting strictly 1 reservation with zero double-spend.
func TestPostgresBillingConcurrentOneCentRace(t *testing.T) {
	b := openIsolatedTestBackend(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	user := users.User{
		ID:          uuid.NewString(),
		Email:       fmt.Sprintf("race-%s@example.com", uuid.NewString()[:8]),
		DisplayName: "Race Tester",
		TimeZone:    "UTC",
	}
	dir := records.NewSharedUsers(b)
	dir.Now = func() time.Time { return now }
	if err := dir.Create(ctx, user); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	bStore := billing.NewPostgresStore(b.Pool())
	bStore.Now = func() time.Time { return now }

	// Verify atomic user creation created the trial entitlement + $5.00 grant.
	ent, err := bStore.GetEntitlement(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetEntitlement after Create: %v", err)
	}
	if ent.Tier != billing.TierTrial || ent.Status != billing.StatusActive {
		t.Fatalf("expected active trial entitlement, got %+v", ent)
	}

	// Spend $4.99 of the $5.00 trial credit so strictly $0.01 (10_000 micros) remains!
	db := records.NewShared(b, user, 10.0)
	db.Now = func() time.Time { return now }
	db.EnforceBilling = true
	db.GlobalBudget = billing.GlobalBudgetConfig{
		HostedAllMaxMicrosPerDay: 50 * billing.MicrosPerUSD,
		HostedExaMaxMicrosPerDay: 15 * billing.MicrosPerUSD,
	}
	spent499 := 4.99
	if err := db.RecordUsage(ctx, state.Usage{
		ID:            "prior-499",
		Provider:      "gateway",
		Model:         "google/gemini-4-neon",
		Billing:       state.UsageBillingHosted,
		Category:      state.UsageCategoryLLM,
		CostUSD:       &spent499,
		CostStatus:    "provider",
		RequestStatus: "received",
		CreatedAt:     now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed $4.99 spend: %v", err)
	}

	bal, err := bStore.CreditBalance(ctx, user.ID)
	if err != nil || bal.AvailableMicros != 10_000 {
		t.Fatalf("expected 10_000 micros ($0.01) remaining before race, got %+v (err=%v)", bal, err)
	}

	const workers = 20
	var (
		wg        sync.WaitGroup
		allowed   atomic.Int64
		exhausted atomic.Int64
		otherErr  atomic.Int64
		firstErr  error
		firstMu   sync.Mutex
	)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			ok, err := db.ReserveUsage(ctx, state.Usage{
				ID:                   fmt.Sprintf("race-call-%02d", idx),
				Provider:             "gateway",
				Model:                "google/gemini-4-hydrogen",
				Billing:              state.UsageBillingHosted,
				Category:             state.UsageCategoryLLM,
				EstimatedExposureUSD: 0.01,
				CostStatus:           "unknown",
				RequestStatus:        "pending",
				CreatedAt:            now,
			}, now.Add(-time.Hour), now.Add(-24*time.Hour), 300, 15.0, 0.01)
			switch {
			case err == nil && ok:
				allowed.Add(1)
			case errors.Is(err, billing.ErrCreditExhausted):
				exhausted.Add(1)
			default:
				otherErr.Add(1)
				firstMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("ok=%v err=%v", ok, err)
				}
				firstMu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if allowed.Load() != 1 || exhausted.Load() != workers-1 || otherErr.Load() != 0 {
		t.Fatalf("concurrent $0.01 race FAILED: allowed=%d exhausted=%d other=%d (firstErr=%v), want 1 / %d / 0",
			allowed.Load(), exhausted.Load(), otherErr.Load(), firstErr, workers-1)
	}

	balAfter, err := bStore.CreditBalance(ctx, user.ID)
	if err != nil {
		t.Fatalf("CreditBalance after race: %v", err)
	}
	if balAfter.AvailableMicros != 0 {
		t.Fatalf("expected 0 micros remaining after race, got %+v", balAfter)
	}
	t.Logf("PASS Postgres concurrent $0.01 race: workers=%d allowed=%d exhausted=%d remaining_micros=%d",
		workers, allowed.Load(), exhausted.Load(), balAfter.AvailableMicros)
}

func TestPostgresBillingUserProvisioningCapsAndLifecycle(t *testing.T) {
	b := openIsolatedTestBackend(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	bStore := billing.NewPostgresStore(b.Pool())
	bStore.Now = func() time.Time { return now }
	svc := billing.NewService(bStore, true, billing.GlobalBudgetConfig{})

	dir := records.NewSharedUsers(b)
	dir.SetTrialMaxSignupsPerDay(3)

	// 1. Create
	u1 := users.User{ID: uuid.NewString(), Email: "p1@example.com", TimeZone: "UTC"}
	if err := dir.Create(ctx, u1); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// 2. ResolveIdentity (open mode)
	u2, err := dir.ResolveIdentity(ctx, users.Identity{
		Issuer: "https://accounts.google.com", Subject: "sub-p2", EmailAuthoritative: true,
	}, "p2@example.com", "P2", "open", "")
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	// 3. RedeemInviteAndCreateUser
	inv, err := dir.CreateInvite(ctx, users.CreateInviteOptions{MaxUses: 2})
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	u3, _, err := dir.RedeemInviteAndCreateUser(ctx, inv.Code, "p3@example.com", "P3")
	if err != nil {
		t.Fatalf("RedeemInviteAndCreateUser: %v", err)
	}
	for _, uid := range []string{u1.ID, u2.ID, u3.ID} {
		ent, err := bStore.GetEntitlement(ctx, uid)
		if err != nil || ent.Tier != billing.TierTrial || ent.Status != billing.StatusActive {
			t.Fatalf("user %s missing trial entitlement: %+v, %v", uid, ent, err)
		}
		grants, err := bStore.ListCreditGrants(ctx, uid)
		if err != nil || len(grants) != 1 || grants[0].ID != billing.TrialGrantID(uid) || grants[0].AmountMicros != 5_000_000 {
			t.Fatalf("user %s unexpected trial grants: %+v, %v", uid, grants, err)
		}
	}

	// 4th trial signup must be rejected AND roll back user creation atomically.
	u4 := users.User{ID: uuid.NewString(), Email: "p4@example.com", TimeZone: "UTC"}
	if err := dir.Create(ctx, u4); !errors.Is(err, billing.ErrTrialLimitExceeded) {
		t.Fatalf("expected ErrTrialLimitExceeded on 4th signup, got %v", err)
	}
	if _, err := dir.Get(ctx, u4.ID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("expected u4 rolled back on trial cap, got %v", err)
	}

	// Missing entitlement fails closed when EnforceBilling=true.
	ghost := users.User{ID: uuid.NewString(), Email: "ghost@example.com", TimeZone: "UTC"}
	ghostDB := records.NewShared(b, ghost, 10)
	ghostDB.EnforceBilling = true
	if _, err := ghostDB.ReserveUsage(ctx, state.Usage{
		ID: "ghost-1", Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: now,
	}, now.Add(-time.Hour), now.Add(-24*time.Hour), 300, 15, 0.01); !errors.Is(err, billing.ErrEntitlementMissing) {
		t.Fatalf("expected ErrEntitlementMissing for ghost user, got %v", err)
	}

	// Suspend -> Stripe active event NEVER clears suspended_at; pg_notify delivers entitlement:<uid>.
	conn, err := b.Pool().Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire listener conn: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN nori_wake"); err != nil {
		t.Fatalf("LISTEN nori_wake: %v", err)
	}

	ent1, err := svc.Suspend(ctx, u1.ID, "chargeback")
	if err != nil || !ent1.IsSuspended() {
		t.Fatalf("Suspend u1: %+v, %v", ent1, err)
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 2*time.Second)
	defer waitCancel()
	notif, err := conn.Conn().WaitForNotification(waitCtx)
	if err != nil {
		t.Fatalf("WaitForNotification(entitlement): %v", err)
	}
	parsedWake := docstore.ParseWakePayload(notif.Payload)
	if parsedWake.Kind != docstore.WakeKindEntitlement || parsedWake.UserID != u1.ID {
		t.Fatalf("unexpected pg_notify payload %q -> %+v", notif.Payload, parsedWake)
	}
	pEnd := now.AddDate(0, 1, 0)
	ent1, err = svc.ApplyEvent(ctx, billing.Change{
		Provider:          billing.ProviderStripe,
		Environment:       billing.EnvProduction,
		EventID:           "evt_pg_1",
		EventType:         "invoice.paid",
		ProviderUpdatedAt: now.Add(time.Minute),
		UserID:            u1.ID,
		Subscription: &billing.SubscriptionSnapshot{
			ExternalSubscriptionID: "sub_pg_1",
			Tier:                   billing.TierPower,
			Status:                 billing.StatusActive,
			PeriodStart:            now,
			PeriodEnd:              &pEnd,
		},
		CreditDeltaMicros: 25_000_000,
		Payload:           json.RawMessage(`{"source":"test"}`),
	})
	if err != nil {
		t.Fatalf("ApplyEvent: %v", err)
	}
	if ent1.Tier != billing.TierPower || !ent1.IsSuspended() || ent1.SuspendedReason != "chargeback" {
		t.Fatalf("ApplyEvent altered suspension or failed tier update: %+v", ent1)
	}

	// Suspended user blocked on ReserveUsage.
	db1 := records.NewShared(b, u1, 10)
	db1.EnforceBilling = true
	db1.GlobalBudget = billing.GlobalBudgetConfig{
		HostedAllMaxMicrosPerDay: 50_000_000,
		HostedExaMaxMicrosPerDay: 15_000, // $0.015 Exa daily cap
	}
	meter1 := usage.WithBillingAdmission(db1, 0, 0, func() time.Time { return now })
	if err := meter1.RecordUsage(ctx, state.Usage{
		ID: "susp-1", Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: now,
	}); !errors.Is(err, billing.ErrAccountSuspended) {
		t.Fatalf("expected ErrAccountSuspended, got %v", err)
	}

	// Unsuspend -> global Exa cap + finalization reconciliation works in Postgres.
	if _, err := svc.Unsuspend(ctx, u1.ID); err != nil {
		t.Fatalf("Unsuspend: %v", err)
	}
	if err := meter1.RecordUsage(ctx, state.Usage{
		ID: "pg-exa-1", Provider: "exa", Model: "exa-search",
		Billing: state.UsageBillingHosted, Category: state.UsageCategorySearch,
		EstimatedExposureUSD: 0.013, RequestStatus: "pending", CreatedAt: now,
	}); err != nil {
		t.Fatalf("first hosted exa: %v", err)
	}
	if err := meter1.RecordUsage(ctx, state.Usage{
		ID: "pg-exa-2", Provider: "exa", Model: "exa-search",
		Billing: state.UsageBillingHosted, Category: state.UsageCategorySearch,
		EstimatedExposureUSD: 0.013, RequestStatus: "pending", CreatedAt: now,
	}); !errors.Is(err, billing.ErrGlobalBudgetExceeded) {
		t.Fatalf("expected ErrGlobalBudgetExceeded on 2nd exa, got %v", err)
	}
	// Finalize pg-exa-1 as not_sent -> reconciles Exa budget back to 0 so pg-exa-2 succeeds!
	if err := meter1.RecordUsage(ctx, state.Usage{
		ID: "pg-exa-1", Provider: "exa", Model: "exa-search",
		Billing: state.UsageBillingHosted, Category: state.UsageCategorySearch,
		RequestStatus: "not_sent", CreatedAt: now,
	}); err != nil {
		t.Fatalf("finalize not_sent: %v", err)
	}
	if err := meter1.RecordUsage(ctx, state.Usage{
		ID: "pg-exa-2", Provider: "exa", Model: "exa-search",
		Billing: state.UsageBillingHosted, Category: state.UsageCategorySearch,
		EstimatedExposureUSD: 0.013, RequestStatus: "pending", CreatedAt: now,
	}); err != nil {
		t.Fatalf("expected pg-exa-2 to pass after not_sent reconciliation, got %v", err)
	}

	// Verify ReadUsageCosts returns billing & category from Postgres.
	actual := 0.013
	if err := meter1.RecordUsage(ctx, state.Usage{
		ID: "pg-exa-2", Provider: "exa", Model: "exa-search",
		Billing: state.UsageBillingHosted, Category: state.UsageCategorySearch,
		CostUSD: &actual, CostStatus: "provider", RequestStatus: "received", CreatedAt: now,
	}); err != nil {
		t.Fatalf("finalize pg-exa-2: %v", err)
	}
	report, err := usage.ReadCostReport(ctx, db1.ReadUsageCosts, 1, time.UTC, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ReadCostReport: %v", err)
	}
	if report.ByBilling.Hosted.PricedCalls != 1 || report.ByCategory.Search.PricedCalls != 1 {
		t.Fatalf("unexpected Postgres cost report breakdowns: hosted=%+v search=%+v", report.ByBilling.Hosted, report.ByCategory.Search)
	}

	// Direct-final hosted Sprites metering (no prior pending row) must update nori_global_budget_daily.
	spritesCost := 0.05
	if err := db1.RecordUsage(ctx, state.Usage{
		ID: "pg-sprite-direct-1", Provider: "sprites", Model: "sprites-vm",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryComputer,
		CostUSD: &spritesCost, CostStatus: "calculated", RequestStatus: "received", CreatedAt: now,
	}); err != nil {
		t.Fatalf("direct-final sprites RecordUsage: %v", err)
	}
	sprBudget, err := bStore.GetGlobalBudget(ctx, now, billing.ScopeHostedSprites)
	if err != nil || sprBudget.SpentMicros != 50_000 || sprBudget.Count != 1 {
		t.Fatalf("expected direct-final hosted sprites in global budget, got %+v (err=%v)", sprBudget, err)
	}

	// Stale/aborted call retains reserved exposure (no $0 leak and no global budget refund).
	allBefore, _ := bStore.GetGlobalBudget(ctx, now, billing.ScopeHostedAll)
	balBefore, _ := bStore.CreditBalance(ctx, u1.ID)
	if err := meter1.RecordUsage(ctx, state.Usage{
		ID: "pg-aborted-1", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.04, RequestStatus: "pending", CreatedAt: now,
	}); err != nil {
		t.Fatalf("reserve pg-aborted-1: %v", err)
	}
	if err := meter1.RecordUsage(ctx, state.Usage{
		ID: "pg-aborted-1", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.04, RequestStatus: "stale_aborted", CreatedAt: now,
	}); err != nil {
		t.Fatalf("finalize pg-aborted-1 stale_aborted: %v", err)
	}
	allAfter, _ := bStore.GetGlobalBudget(ctx, now, billing.ScopeHostedAll)
	balAfter, _ := bStore.CreditBalance(ctx, u1.ID)
	if allAfter.SpentMicros != allBefore.SpentMicros+40_000 {
		t.Fatalf("stale_aborted refunded Postgres global budget: before=%d after=%d", allBefore.SpentMicros, allAfter.SpentMicros)
	}
	if balAfter.AvailableMicros != balBefore.AvailableMicros-40_000 {
		t.Fatalf("stale_aborted refunded Postgres user credit: before=%d after=%d", balBefore.AvailableMicros, balAfter.AvailableMicros)
	}

	// SetTier twice in overlapping months must NOT stack free credit, and overlapping expired windows
	// must NOT double-absorb usage in Postgres SQL.
	uOverlap := users.User{ID: uuid.NewString(), Email: "overlap@example.com", TimeZone: "UTC"}
	dir.SetTrialMaxSignupsPerDay(10)
	if err := dir.Create(ctx, uOverlap); err != nil {
		t.Fatalf("create uOverlap: %v", err)
	}
	tNov20 := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	bStore.Now = func() time.Time { return tNov20 }
	if _, err := bStore.SetTier(ctx, uOverlap.ID, billing.TierPower, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier Nov 20: %v", err)
	}
	tDec5 := time.Date(2026, 12, 5, 12, 0, 0, 0, time.UTC)
	bStore.Now = func() time.Time { return tDec5 }
	if _, err := bStore.SetTier(ctx, uOverlap.ID, billing.TierPower, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier Dec 5: %v", err)
	}
	// Active grants at Dec 5: $5 trial (valid_from=now <= Dec 5 only if created <= Dec 5) + $25 Dec 5 power (Nov 20 closed!).
	ovBal, err := bStore.CreditBalance(ctx, uOverlap.ID)
	if err != nil {
		t.Fatalf("CreditBalance uOverlap: %v", err)
	}
	// Must be at most $30 ($5 trial + $25 single active power grant), NEVER $55 ($5 + $25 + $25)!
	if ovBal.GrantedMicros > 30_000_000 {
		t.Fatalf("SetTier twice stacked overlapping active grants: %+v", ovBal)
	}

	// Explicit overlapping expired grants in Postgres: single-assignment lateral join prevents double-absorption.
	uExp := users.User{ID: uuid.NewString(), Email: "exp-single@example.com", TimeZone: "UTC"}
	t1 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	t4 := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	dir.Now = func() time.Time { return t1 }
	if err := dir.Create(ctx, uExp); err != nil {
		t.Fatalf("create uExp: %v", err)
	}
	if err := bStore.AddCreditGrant(ctx, billing.CreditGrant{
		ID: "pg-exp-a", UserID: uExp.ID, Kind: billing.GrantPeriod, AmountMicros: 10_000_000, ValidFrom: t1, ValidUntil: &t3,
	}); err != nil {
		t.Fatal(err)
	}
	if err := bStore.AddCreditGrant(ctx, billing.CreditGrant{
		ID: "pg-exp-b", UserID: uExp.ID, Kind: billing.GrantPeriod, AmountMicros: 10_000_000, ValidFrom: t2, ValidUntil: &t4,
	}); err != nil {
		t.Fatal(err)
	}
	expDB := records.NewShared(b, uExp, 10)
	cost12 := 12.0
	if err := expDB.RecordUsage(ctx, state.Usage{
		ID: "pg-overlap-spend", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		CostUSD: &cost12, CostStatus: "provider", RequestStatus: "received", CreatedAt: t2.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	bStore.Now = func() time.Time { return t4.Add(time.Hour) }
	expBal, err := bStore.CreditBalance(ctx, uExp.ID)
	if err != nil || expBal.AvailableMicros != 3_000_000 {
		t.Fatalf("Postgres overlapping expired balance = %+v (err=%v), want 3_000_000", expBal, err)
	}

	// Cross-user subscription hijack rejected in Postgres.
	if _, err := svc.ApplyEvent(ctx, billing.Change{
		Provider:          billing.ProviderStripe,
		Environment:       billing.EnvProduction,
		EventID:           "evt_pg_hijack",
		EventType:         "customer.subscription.updated",
		ProviderUpdatedAt: now.Add(5 * time.Minute),
		UserID:            u2.ID,
		Subscription: &billing.SubscriptionSnapshot{
			ExternalSubscriptionID: "sub_pg_1", // owned by u1!
			Tier:                   billing.TierPower,
			Status:                 billing.StatusActive,
			PeriodStart:            now,
			PeriodEnd:              &pEnd,
		},
	}); !errors.Is(err, billing.ErrSubscriptionConflict) {
		t.Fatalf("expected ErrSubscriptionConflict on Postgres cross-user hijack, got %v", err)
	}
}
