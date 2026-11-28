package billing_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"nori/internal/billing"
)

func TestTierPoliciesContract(t *testing.T) {
	cases := []struct {
		tier          billing.Tier
		creditMicros  int64
		allowBYOK     bool
		callsPerHour  int
		byokDayMicros int64
		storageBytes  int64
		routines      int
		minInterval   time.Duration
		tasks         int
		desktopWS     int
		checkpoints   int
	}{
		{billing.TierTrial, 5_000_000, false, 120, 0, 100 << 20, 3, 60 * time.Minute, 2, 1, 1},
		{billing.TierBYOK, 0, true, 300, 15_000_000, 250 << 20, 10, 15 * time.Minute, 2, 1, 3},
		{billing.TierStandard, 10_000_000, true, 300, 15_000_000, 500 << 20, 15, 15 * time.Minute, 4, 1, 3},
		{billing.TierPower, 25_000_000, true, 600, 30_000_000, 2 << 30, 30, 5 * time.Minute, 6, 2, 10},
	}
	for _, tc := range cases {
		p, ok := billing.PolicyFor(tc.tier)
		if !ok {
			t.Fatalf("PolicyFor(%q) not found", tc.tier)
		}
		if p.HostedCreditMicros != tc.creditMicros || p.AllowBYOK != tc.allowBYOK || p.MaxCallsPerHour != tc.callsPerHour ||
			p.MaxBYOKMicrosPerDay != tc.byokDayMicros || p.MaxStorageBytes != tc.storageBytes || p.MaxRoutines != tc.routines ||
			p.MinRoutineInterval != tc.minInterval || p.MaxConcurrentTasks != tc.tasks || p.MaxDesktopWS != tc.desktopWS ||
			p.MaxSpriteCheckpoints != tc.checkpoints {
			t.Fatalf("tier %q policy mismatch: got %+v", tc.tier, p)
		}
	}
}

func TestEntitlementStatusesAndOrthogonalSuspension(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	store := billing.NewMemoryStore(func() time.Time { return now })
	svc := billing.NewService(store, true, billing.GlobalBudgetConfig{})

	// Missing entitlement fails closed.
	if _, err := svc.GetEntitlement(ctx, "missing-user"); !errors.Is(err, billing.ErrEntitlementMissing) {
		t.Fatalf("expected ErrEntitlementMissing, got %v", err)
	}

	ent, err := store.ProvisionTrial(ctx, "u1", 25)
	if err != nil {
		t.Fatalf("ProvisionTrial: %v", err)
	}
	if ent.Tier != billing.TierTrial || ent.Status != billing.StatusActive || ent.AppAccountToken == "" {
		t.Fatalf("unexpected trial entitlement: %+v", ent)
	}

	// Suspend user and verify Stripe active invoice NEVER clears suspended_at.
	ent, err = svc.Suspend(ctx, "u1", "abuse")
	if err != nil || !ent.IsSuspended() || ent.SuspendedReason != "abuse" {
		t.Fatalf("Suspend failed: %+v, %v", ent, err)
	}
	if _, err := svc.CheckAccess(ctx, "u1"); !errors.Is(err, billing.ErrAccountSuspended) {
		t.Fatalf("expected ErrAccountSuspended, got %v", err)
	}

	pEnd := now.AddDate(0, 1, 0)
	ent, err = svc.ApplyEvent(ctx, billing.Change{
		Provider:          billing.ProviderStripe,
		Environment:       billing.EnvProduction,
		EventID:           "evt_paid_1",
		EventType:         "invoice.paid",
		ProviderUpdatedAt: now.Add(time.Minute),
		UserID:            "u1",
		Subscription: &billing.SubscriptionSnapshot{
			ExternalSubscriptionID: "sub_123",
			Tier:                   billing.TierStandard,
			Status:                 billing.StatusActive,
			PeriodStart:            now,
			PeriodEnd:              &pEnd,
		},
		CreditDeltaMicros: 10_000_000,
		Payload:           json.RawMessage(`{"ok":true}`),
	})
	if err != nil {
		t.Fatalf("ApplyEvent: %v", err)
	}
	if ent.Tier != billing.TierStandard || ent.Status != billing.StatusActive {
		t.Fatalf("expected updated tier/status, got %+v", ent)
	}
	if !ent.IsSuspended() || ent.SuspendedReason != "abuse" {
		t.Fatalf("ApplyEvent cleared suspension! ent=%+v", ent)
	}

	// Unsuspend restores active access.
	ent, err = svc.Unsuspend(ctx, "u1")
	if err != nil || ent.IsSuspended() || ent.Status != billing.StatusActive {
		t.Fatalf("Unsuspend failed: %+v, %v", ent, err)
	}
	if _, err := svc.CheckAccess(ctx, "u1"); err != nil {
		t.Fatalf("CheckAccess after unsuspend: %v", err)
	}

	// Grace period & canceled status behavior.
	grace := now.Add(24 * time.Hour)
	e := billing.Entitlement{Status: billing.StatusBillingRetry, GraceUntil: &grace}
	if err := e.CheckAccess(now); err != nil {
		t.Fatalf("billing_retry before grace should be active: %v", err)
	}
	if err := e.CheckAccess(grace.Add(time.Second)); !errors.Is(err, billing.ErrSubscriptionInactive) {
		t.Fatalf("billing_retry past grace should be inactive, got %v", err)
	}

	end := now.Add(48 * time.Hour)
	c := billing.Entitlement{Status: billing.StatusCanceled, PeriodEnd: &end}
	if err := c.CheckAccess(now); err != nil {
		t.Fatalf("canceled before period_end should be active: %v", err)
	}
	if err := c.CheckAccess(end); !errors.Is(err, billing.ErrSubscriptionInactive) {
		t.Fatalf("canceled at/after period_end should be inactive, got %v", err)
	}
}

func TestManualRolloverAndPendingDowngradeAndCreditLedger(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	var samples []billing.UsageSample
	store := billing.NewMemoryStore(func() time.Time { return now })
	store.UsageReader = func(string) []billing.UsageSample { return samples }

	// Start on trial ($5), spend $5, upgrade to standard ($10/mo), spend $3 in Nov, schedule downgrade to byok at Dec 1.
	if _, err := store.ProvisionTrial(ctx, "u1", 0); err != nil {
		t.Fatal(err)
	}
	cost5 := 5.0
	samples = append(samples, billing.UsageSample{
		CreatedAt: now.Add(time.Hour), Billing: "hosted", CostUSD: &cost5, CostStatus: "provider", RequestStatus: "received",
	})
	bal, err := store.CreditBalance(ctx, "u1")
	if err != nil || bal.AvailableMicros != 0 {
		t.Fatalf("trial exhausted balance = %+v, err=%v", bal, err)
	}

	// Upgrade to standard on Nov 1 -> $10 period grant valid [Nov 1, Dec 1).
	now = time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	ent, err := store.SetTier(ctx, "u1", billing.TierStandard, billing.SetTierOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ent.PeriodEnd == nil || ent.PeriodEnd.Month() != time.December {
		t.Fatalf("unexpected standard period: %+v", ent)
	}
	cost3 := 3.0
	samples = append(samples, billing.UsageSample{
		CreatedAt: now.Add(time.Hour), Billing: "hosted", CostUSD: &cost3, CostStatus: "provider", RequestStatus: "received",
	})
	// Add stale pending (>15m, $0.25) + active pending (<15m, $0.50) + stale_aborted (exposure 0 -> fallback $0.02) + non-expiring topup $2.
	samples = append(samples,
		billing.UsageSample{CreatedAt: now.Add(30 * time.Minute), Billing: "hosted", EstimatedExposureUSD: 0.25, RequestStatus: "pending"},
		billing.UsageSample{CreatedAt: now.Add(2 * time.Hour), Billing: "hosted", EstimatedExposureUSD: 0.50, RequestStatus: "pending"},
		billing.UsageSample{CreatedAt: now.Add(2 * time.Hour), Billing: "hosted", EstimatedExposureUSD: 0, RequestStatus: "stale_aborted"},
	)
	now = now.Add(2*time.Hour + 5*time.Minute)
	if err := store.AddCreditGrant(ctx, billing.CreditGrant{
		ID: "topup:u1:1", UserID: "u1", Kind: billing.GrantTopup, AmountMicros: 2_000_000, ValidFrom: now,
	}); err != nil {
		t.Fatal(err)
	}
	// Balance: $10 (period) + $2 (topup) - $3 (spent) - $0.25 (stale pending charged as spent!) - $0.50 (active pending) - $0.02 (stale_aborted fallback) = $8.23!
	bal, err = store.CreditBalance(ctx, "u1")
	if err != nil || bal.AvailableMicros != 8_230_000 {
		t.Fatalf("mid-month balance = %+v, want 8_230_000", bal)
	}

	// Rollover into Dec 1 (still standard): unused Nov period credit expires, Dec $10 grant created, $2 topup carries over!
	// Nov $10 grant absorbed all Nov usage ($3 + $0.25 + $0.50 + $0.02 = $3.77 <= $10); Dec $10 grant + $2 topup = $12 available!
	now = time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	bal, err = store.CreditBalance(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if bal.AvailableMicros != 12_000_000 {
		t.Fatalf("after rollover balance = %+v, want 12_000_000", bal)
	}

	// Schedule pending downgrade to byok -> takes effect at Jan 1, not immediately.
	ent, err = store.SetTier(ctx, "u1", billing.TierBYOK, billing.SetTierOptions{Pending: true})
	if err != nil || ent.Tier != billing.TierStandard || ent.PendingTier == nil || *ent.PendingTier != billing.TierBYOK {
		t.Fatalf("pending downgrade unexpected: %+v, err=%v", ent, err)
	}
	now = time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	ent, err = store.GetEntitlement(ctx, "u1")
	if err != nil || ent.Tier != billing.TierBYOK || ent.PendingTier != nil {
		t.Fatalf("expected pending downgrade applied at period_end: %+v, err=%v", ent, err)
	}

	// Refund clawback can push balance negative.
	if err := store.AddCreditGrant(ctx, billing.CreditGrant{
		ID: "clawback:u1:1", UserID: "u1", Kind: billing.GrantClawback, AmountMicros: -5_000_000, ValidFrom: now,
	}); err != nil {
		t.Fatal(err)
	}
	bal, err = store.CreditBalance(ctx, "u1")
	if err != nil || bal.AvailableMicros != -3_000_000 {
		t.Fatalf("clawback balance = %+v, want -3_000_000", bal)
	}
}

func TestApplyEventIdempotencySandboxStaleAndMultiSubscription(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	store := billing.NewMemoryStore(func() time.Time { return now })
	svc := billing.NewService(store, true, billing.GlobalBudgetConfig{})

	ent, err := store.ProvisionTrial(ctx, "u1", 0)
	if err != nil {
		t.Fatal(err)
	}
	pEnd := now.AddDate(0, 1, 0)

	// Sandbox rejected in production.
	_, err = svc.ApplyEvent(ctx, billing.Change{
		Provider:          billing.ProviderAppleIAP,
		Environment:       billing.EnvSandbox,
		EventID:           "evt_sandbox",
		EventType:         "SUBSCRIBED",
		ProviderUpdatedAt: now,
		AppAccountToken:   ent.AppAccountToken,
		Subscription: &billing.SubscriptionSnapshot{
			ExternalSubscriptionID: "orig_tx_1",
			Tier:                   billing.TierPower,
			Status:                 billing.StatusActive,
			PeriodStart:            now,
			PeriodEnd:              &pEnd,
		},
	})
	if !errors.Is(err, billing.ErrInvalidEnvironment) {
		t.Fatalf("expected ErrInvalidEnvironment for sandbox in prod, got %v", err)
	}

	// Production Stripe standard + Apple IAP power (lookup by AppAccountToken) -> power wins!
	stripeChange := billing.Change{
		Provider:          billing.ProviderStripe,
		Environment:       billing.EnvProduction,
		EventID:           "evt_stripe_1",
		EventType:         "customer.subscription.created",
		ProviderUpdatedAt: now.Add(time.Minute),
		UserID:            "u1",
		Subscription: &billing.SubscriptionSnapshot{
			ExternalSubscriptionID: "sub_stripe_1",
			Tier:                   billing.TierStandard,
			Status:                 billing.StatusActive,
			PeriodStart:            now,
			PeriodEnd:              &pEnd,
		},
		CreditDeltaMicros: 10_000_000,
	}
	if _, err := svc.ApplyEvent(ctx, stripeChange); err != nil {
		t.Fatalf("stripe ApplyEvent: %v", err)
	}
	// Duplicate event is idempotent (no duplicate grant).
	if _, err := svc.ApplyEvent(ctx, stripeChange); err != nil {
		t.Fatalf("duplicate ApplyEvent: %v", err)
	}
	grants, _ := store.ListCreditGrants(ctx, "u1")
	if len(grants) != 2 { // trial + stripe
		t.Fatalf("expected 2 grants after duplicate event, got %d", len(grants))
	}

	// Stale event for same subscription rejected.
	stale := stripeChange
	stale.EventID = "evt_stripe_stale"
	stale.ProviderUpdatedAt = now
	if _, err := svc.ApplyEvent(ctx, stale); !errors.Is(err, billing.ErrStaleEvent) {
		t.Fatalf("expected ErrStaleEvent, got %v", err)
	}

	// Apple IAP active power beats Stripe active standard.
	appleEnt, err := svc.ApplyEvent(ctx, billing.Change{
		Provider:          billing.ProviderAppleIAP,
		Environment:       billing.EnvProduction,
		EventID:           "evt_apple_1",
		EventType:         "SUBSCRIBED",
		ProviderUpdatedAt: now.Add(2 * time.Minute),
		AppAccountToken:   ent.AppAccountToken,
		Subscription: &billing.SubscriptionSnapshot{
			ExternalSubscriptionID: "apple_orig_1",
			Tier:                   billing.TierPower,
			Status:                 billing.StatusActive,
			PeriodStart:            now,
			PeriodEnd:              &pEnd,
		},
		CreditDeltaMicros: 25_000_000,
	})
	if err != nil {
		t.Fatalf("apple ApplyEvent: %v", err)
	}
	if appleEnt.Tier != billing.TierPower || appleEnt.BillingProvider != billing.ProviderAppleIAP {
		t.Fatalf("expected Apple Power to win multi-subscription precedence, got %+v", appleEnt)
	}
}

func TestSetTierTwiceNoStackingAndExpiredWindowSingleAssignment(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var samples []billing.UsageSample
	store := billing.NewMemoryStore(func() time.Time { return now })
	store.UsageReader = func(string) []billing.UsageSample { return samples }

	// SetTier power on Nov 20 -> $25 live.
	if _, err := store.SetTier(ctx, "u1", billing.TierPower, billing.SetTierOptions{}); err != nil {
		t.Fatal(err)
	}
	bal, err := store.CreditBalance(ctx, "u1")
	if err != nil || bal.AvailableMicros != 25_000_000 {
		t.Fatalf("Nov 20 balance = %+v, want 25_000_000", bal)
	}

	// Re-setting SetTier power on Dec 5 closes the Nov 20 grant -> $25 live (NOT $50!).
	now = time.Date(2026, 12, 5, 12, 0, 0, 0, time.UTC)
	if _, err := store.SetTier(ctx, "u1", billing.TierPower, billing.SetTierOptions{}); err != nil {
		t.Fatal(err)
	}
	bal, err = store.CreditBalance(ctx, "u1")
	if err != nil || bal.AvailableMicros != 25_000_000 {
		t.Fatalf("Dec 5 balance after re-SetTier = %+v, want 25_000_000", bal)
	}

	// Explicit overlapping expired grants must NOT double-absorb the same usage row.
	t1 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	t4 := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	cost12 := 12.0
	grants := []billing.CreditGrant{
		{ID: "trial:u2", UserID: "u2", Kind: billing.GrantTrial, AmountMicros: 5_000_000, ValidFrom: t1},
		{ID: "exp-a", UserID: "u2", Kind: billing.GrantPeriod, AmountMicros: 10_000_000, ValidFrom: t1, ValidUntil: &t3},
		{ID: "exp-b", UserID: "u2", Kind: billing.GrantPeriod, AmountMicros: 10_000_000, ValidFrom: t2, ValidUntil: &t4},
	}
	overlapUsage := []billing.UsageSample{
		{CreatedAt: t2.Add(time.Hour), Billing: "hosted", CostUSD: &cost12, CostStatus: "provider", RequestStatus: "received"},
	}
	// $12 usage falls in BOTH [Oct 1, Nov 1) and [Oct 15, Nov 15).
	// Deterministic single-assignment gives it ONLY to exp-a (earliest valid_until = Nov 1), which absorbs $10.
	// Remaining $2 draws from the $5 trial grant -> strictly $3 available (NOT $5!).
	for i := 0; i < 20; i++ {
		got := billing.ComputeCreditBalance(billing.Entitlement{UserID: "u2"}, grants, overlapUsage, t4.Add(time.Hour))
		if got.AvailableMicros != 3_000_000 {
			t.Fatalf("iter %d: overlapping expired balance = %+v, want 3_000_000", i, got)
		}
	}
}

func TestHardeningBillingRetrySandboxHijackAndGrantValidation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	store := billing.NewMemoryStore(func() time.Time { return now })
	// Default zero Service rejects sandbox events (production-safe by default).
	svc := &billing.Service{Store: store, Now: func() time.Time { return now }}

	if _, err := store.ProvisionTrial(ctx, "u1", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ProvisionTrial(ctx, "u2", 0); err != nil {
		t.Fatal(err)
	}

	// billing_retry with nil GraceUntil and nil PeriodEnd fails closed.
	if err := (billing.Entitlement{Status: billing.StatusBillingRetry}).CheckAccess(now); !errors.Is(err, billing.ErrSubscriptionInactive) {
		t.Fatalf("expected ErrSubscriptionInactive when billing_retry has nil GraceUntil & PeriodEnd, got %v", err)
	}
	futureEnd := now.Add(time.Hour)
	if err := (billing.Entitlement{Status: billing.StatusBillingRetry, PeriodEnd: &futureEnd}).CheckAccess(now); err != nil {
		t.Fatalf("expected billing_retry with nil GraceUntil to fall back to future PeriodEnd, got %v", err)
	}

	// Direct store.ApplyEvent and default Service both reject sandbox events by default.
	pEnd := now.AddDate(0, 1, 0)
	sandboxChange := billing.Change{
		Provider:          billing.ProviderStripe,
		Environment:       billing.EnvSandbox,
		EventID:           "evt_sbx",
		EventType:         "invoice.paid",
		ProviderUpdatedAt: now,
		UserID:            "u1",
		Subscription: &billing.SubscriptionSnapshot{
			ExternalSubscriptionID: "sub_sbx",
			Tier:                   billing.TierStandard,
			Status:                 billing.StatusActive,
			PeriodStart:            now,
			PeriodEnd:              &pEnd,
		},
	}
	if _, err := svc.ApplyEvent(ctx, sandboxChange); !errors.Is(err, billing.ErrInvalidEnvironment) {
		t.Fatalf("default Service must reject sandbox, got %v", err)
	}
	if _, err := store.ApplyEvent(ctx, sandboxChange); !errors.Is(err, billing.ErrInvalidEnvironment) {
		t.Fatalf("default MemoryStore must reject sandbox, got %v", err)
	}

	// Grant kind/sign validation.
	if err := store.AddCreditGrant(ctx, billing.CreditGrant{
		ID: "bad-clawback", UserID: "u1", Kind: billing.GrantClawback, AmountMicros: 100,
	}); !errors.Is(err, billing.ErrInvalidChange) {
		t.Fatalf("positive clawback must fail, got %v", err)
	}
	if err := store.AddCreditGrant(ctx, billing.CreditGrant{
		ID: "bad-topup", UserID: "u1", Kind: billing.GrantTopup, AmountMicros: -100,
	}); !errors.Is(err, billing.ErrInvalidChange) {
		t.Fatalf("negative topup must fail, got %v", err)
	}
	if err := store.AddCreditGrant(ctx, billing.CreditGrant{
		ID: "bad-ext-trial", UserID: "u1", Kind: billing.GrantTrial, AmountMicros: 5_000_000, SourceProvider: billing.ProviderStripe,
	}); !errors.Is(err, billing.ErrInvalidChange) {
		t.Fatalf("external provider trial grant must fail, got %v", err)
	}

	// Cross-user subscription hijack rejected.
	prodSub := billing.Change{
		Provider:          billing.ProviderStripe,
		Environment:       billing.EnvProduction,
		EventID:           "evt_u1_sub",
		EventType:         "customer.subscription.created",
		ProviderUpdatedAt: now,
		UserID:            "u1",
		Subscription: &billing.SubscriptionSnapshot{
			ExternalSubscriptionID: "sub_shared_id",
			Tier:                   billing.TierStandard,
			Status:                 billing.StatusActive,
			PeriodStart:            now,
			PeriodEnd:              &pEnd,
		},
	}
	if _, err := svc.ApplyEvent(ctx, prodSub); err != nil {
		t.Fatalf("apply u1 sub: %v", err)
	}
	hijack := prodSub
	hijack.EventID = "evt_u2_hijack"
	hijack.UserID = "u2"
	hijack.ProviderUpdatedAt = now.Add(time.Minute)
	if _, err := svc.ApplyEvent(ctx, hijack); !errors.Is(err, billing.ErrSubscriptionConflict) {
		t.Fatalf("expected ErrSubscriptionConflict on cross-user hijack, got %v", err)
	}

	// Operator SetTier must not clobber provider_updated_at for subsequent non-subscription events.
	now = now.Add(10 * time.Minute)
	if _, err := store.SetTier(ctx, "u1", billing.TierPower, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier: %v", err)
	}
	// Real provider event with timestamp after prodSub (now-10m + 2m) but BEFORE SetTier (now) must succeed!
	if _, err := svc.ApplyEvent(ctx, billing.Change{
		Provider:          billing.ProviderStripe,
		Environment:       billing.EnvProduction,
		EventID:           "evt_u1_promo",
		EventType:         "credit.applied",
		ProviderUpdatedAt: now.Add(-8 * time.Minute),
		UserID:            "u1",
		CreditDeltaMicros: 1_000_000,
		CreditGrantKind:   billing.GrantPromo,
	}); err != nil {
		t.Fatalf("expected provider event before manual SetTier to succeed, got %v", err)
	}
}
