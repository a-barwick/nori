package memstore_test

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
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/app"
	"nori/internal/billing"
	"nori/internal/computer"
	"nori/internal/computer/fake"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
)

// runBillingTrackAConformanceSuite verifies all 6 Track A billing/cost-safety scenarios
// identically across memstore (sim-deep / make check) and real PostgreSQL (make sim-pg).
func runBillingTrackAConformanceSuite(t *testing.T, backend docstore.Backend, prefix string) {
	ctx := context.Background()

	var clockMu sync.Mutex
	curNow := time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC)
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curNow
	}
	setNow := func(ts time.Time) {
		clockMu.Lock()
		curNow = ts.UTC()
		clockMu.Unlock()
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		curNow = curNow.Add(d)
		clockMu.Unlock()
	}

	var (
		bStore billing.Store
		pgPool *pgxpool.Pool
	)
	if mb, ok := backend.(*memstore.Backend); ok {
		bStore = mb
		// Use a dedicated memstore with controllable clock for time-advancing subtests below,
		// while still testing the passed backend directly where wall-clock isn't shifted.
	} else if pp, ok := backend.(interface{ Pool() *pgxpool.Pool }); ok && pp.Pool() != nil {
		pgPool = pp.Pool()
		ps := billing.NewPostgresStore(pgPool)
		ps.Now = nowFn
		bStore = ps
	}
	if bStore == nil {
		t.Fatal("backend exposes neither billing.Store nor pgxpool.Pool")
	}

	// For memstore, use a clock-controlled backend so rollover and 24h BYOK window shifts are deterministic.
	workBackend := backend
	if _, isMem := backend.(*memstore.Backend); isMem {
		mb := memstore.New(memstore.WithClock(nowFn))
		t.Cleanup(func() { _ = mb.Close() })
		workBackend = mb
		bStore = mb
	}

	bSvc := billing.NewService(bStore, true, billing.GlobalBudgetConfig{
		HostedAllMaxMicrosPerDay: 50 * billing.MicrosPerUSD,
		HostedExaMaxMicrosPerDay: 15 * billing.MicrosPerUSD,
		TrialMaxSignupsPerDay:    50,
	})
	bSvc.Now = nowFn

	dir := records.NewSharedUsers(workBackend)
	dir.Now = nowFn
	dir.SetTrialMaxSignupsPerDay(50)

	newUser := func(tag string) (users.User, *records.Database) {
		t.Helper()
		u := users.User{
			ID:          uuid.NewString(),
			Email:       fmt.Sprintf("%s-%s-%s@example.com", prefix, tag, uuid.NewString()[:8]),
			DisplayName: tag,
			TimeZone:    "UTC",
		}
		if err := dir.Create(ctx, u); err != nil {
			t.Fatalf("dir.Create(%s): %v", tag, err)
		}
		db := records.NewShared(workBackend, u, 10.0)
		db.Now = nowFn
		db.EnforceBilling = true
		db.GlobalBudget = bSvc.GlobalBudget
		return u, db
	}

	// 1. Concurrent $0.01 hosted credit race (FOR UPDATE serialization).
	t.Run("Billing_ConcurrentOneCentCreditRace", func(t *testing.T) {
		setNow(time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC))
		u, db := newUser("race")

		spent499 := 4.99
		if err := db.RecordUsage(ctx, state.Usage{
			ID: "prior-499", Provider: "gateway", Model: "google/gemini-4-neon",
			Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
			CostUSD: &spent499, CostStatus: "provider", RequestStatus: "received",
			CreatedAt: nowFn().Add(-time.Minute),
		}); err != nil {
			t.Fatalf("seed $4.99: %v", err)
		}
		bal, err := bStore.CreditBalance(ctx, u.ID)
		if err != nil || bal.AvailableMicros != 10_000 {
			t.Fatalf("pre-race balance = %+v (err=%v), want 10_000", bal, err)
		}

		const workers = 20
		meter := usage.WithBillingAdmission(db, 0, 0, nowFn)
		var (
			wg        sync.WaitGroup
			allowed   atomic.Int64
			exhausted atomic.Int64
			otherErr  atomic.Int64
		)
		start := make(chan struct{})
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				err := meter.RecordUsage(ctx, state.Usage{
					ID: fmt.Sprintf("conf-race-%02d", idx), Provider: "gateway", Model: "google/gemini-4-hydrogen",
					Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
					EstimatedExposureUSD: 0.01, CostStatus: "unknown", RequestStatus: "pending",
					CreatedAt: nowFn(),
				})
				switch {
				case err == nil:
					allowed.Add(1)
				case errors.Is(err, billing.ErrCreditExhausted):
					exhausted.Add(1)
				default:
					otherErr.Add(1)
					t.Errorf("worker %d err: %v", idx, err)
				}
			}(i)
		}
		close(start)
		wg.Wait()

		if allowed.Load() != 1 || exhausted.Load() != workers-1 || otherErr.Load() != 0 {
			t.Fatalf("race result allowed=%d exhausted=%d other=%d, want 1/%d/0",
				allowed.Load(), exhausted.Load(), otherErr.Load(), workers-1)
		}
		balAfter, err := bStore.CreditBalance(ctx, u.ID)
		if err != nil || balAfter.AvailableMicros != 0 {
			t.Fatalf("post-race balance = %+v (err=%v), want 0", balAfter, err)
		}
	})

	// 2. Trial $5.00 cutoff & BYOK/Home AI block (before & after exhaustion + non-renewal).
	t.Run("Billing_TrialCutoffAndBYOKHomeAIBlock", func(t *testing.T) {
		setNow(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
		u, db := newUser("trial")
		meter := usage.WithBillingAdmission(db, 0, 0, nowFn)

		// Before exhaustion: BYOK & Home AI blocked.
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "trial-byok-pre", Provider: "openai", Model: "gpt-6.2-sol",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: nowFn(),
		}); !errors.Is(err, billing.ErrBYOKNotAllowed) {
			t.Fatalf("pre-exhaustion BYOK = %v, want ErrBYOKNotAllowed", err)
		}
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "trial-local-pre", Provider: "home", Model: "inkling-2-mini",
			Billing: state.UsageBillingLocal, Category: state.UsageCategoryLLM,
			CostStatus: "local", RequestStatus: "pending", CreatedAt: nowFn(),
		}); !errors.Is(err, billing.ErrBYOKNotAllowed) {
			t.Fatalf("pre-exhaustion Home AI = %v, want ErrBYOKNotAllowed", err)
		}

		// Exhaust exact $5.00 hosted credit.
		spent5 := 5.0
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "trial-spend-5", Provider: "gateway", Model: "google/gemini-4-neon",
			Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 5.0, RequestStatus: "pending", CreatedAt: nowFn(),
		}); err != nil {
			t.Fatalf("reserve $5.00 trial: %v", err)
		}
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "trial-spend-5", Provider: "gateway", Model: "google/gemini-4-neon",
			Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
			CostUSD: &spent5, CostStatus: "provider", RequestStatus: "received", CreatedAt: nowFn(),
		}); err != nil {
			t.Fatalf("finalize $5.00 trial: %v", err)
		}

		// Next hosted call rejected with ErrCreditExhausted (402 credit_exhausted).
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "trial-over", Provider: "gateway", Model: "google/gemini-4-neon",
			Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: nowFn(),
		}); !errors.Is(err, billing.ErrCreditExhausted) {
			t.Fatalf("post-exhaustion hosted = %v, want ErrCreditExhausted", err)
		}

		// After exhaustion: BYOK & Home AI still blocked.
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "trial-byok-post", Provider: "openai", Model: "gpt-6.2-sol",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: nowFn(),
		}); !errors.Is(err, billing.ErrBYOKNotAllowed) {
			t.Fatalf("post-exhaustion BYOK = %v, want ErrBYOKNotAllowed", err)
		}
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "trial-local-post", Provider: "home", Model: "inkling-2-mini",
			Billing: state.UsageBillingLocal, Category: state.UsageCategoryLLM,
			CostStatus: "local", RequestStatus: "pending", CreatedAt: nowFn(),
		}); !errors.Is(err, billing.ErrBYOKNotAllowed) {
			t.Fatalf("post-exhaustion Home AI = %v, want ErrBYOKNotAllowed", err)
		}

		// Trial never renews after 60 days.
		advance(60 * 24 * time.Hour)
		if bal, err := bStore.CreditBalance(ctx, u.ID); err != nil || bal.AvailableMicros != 0 {
			t.Fatalf("trial balance after 60d = %+v (err=%v), want 0", bal, err)
		}
	})

	// 3. Manual multi-month rollover, single deterministic grant, pending_tier downgrade & routine auto-pause.
	t.Run("Billing_ManualMonthlyRolloverAndDowngrade", func(t *testing.T) {
		setNow(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
		u, db := newUser("manual")

		if _, err := bSvc.SetTier(ctx, u.ID, billing.TierPower, billing.SetTierOptions{}); err != nil {
			t.Fatalf("SetTier power: %v", err)
		}
		if _, err := bSvc.SetTier(ctx, u.ID, billing.TierStandard, billing.SetTierOptions{Pending: true}); err != nil {
			t.Fatalf("SetTier pending standard: %v", err)
		}

		// Seed 17 enabled routines (power allows 30, standard allows 15).
		store := state.Compose(db, memstore.NewArtifacts())
		perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow", Now: nowFn}
		sched := &scheduler.Service{
			Store: store, Tasks: store, Permissions: perms,
			Timezone: func(context.Context) string { return "UTC" },
			Now:      nowFn,
		}
		for i := 1; i <= 17; i++ {
			advance(time.Minute)
			rcpt, err := sched.Save(ctx, "", uuid.NewString(), 0, scheduler.Editable{
				Title:                  fmt.Sprintf("r-%02d", i),
				Instruction:            "Run routine",
				Trigger:                scheduler.Trigger{Kind: "recurring", LocalStart: "2026-11-11T08:00", Timezone: "UTC", RRule: "FREQ=DAILY"},
				ConnectionIDs:          []string{},
				BudgetUSD:              0.01,
				MaxActiveRuns:          1,
				MissedRun:              "skip",
				ExecutionWindowSeconds: 300,
				DestinationIDs:         []string{scheduler.InAppDestination},
			})
			if err != nil || rcpt.Status != "applied" {
				t.Fatalf("Save routine %d: status=%q err=%v", i, rcpt.Status, err)
			}
		}

		// Skip 4 months (Dec/Jan/Feb inactive) to 2027-03-15.
		setNow(time.Date(2027, 3, 15, 8, 0, 0, 0, time.UTC))
		meter := usage.WithBillingAdmission(db, 0, 0, nowFn)
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "rollover-mar-call", Provider: "gateway", Model: "google/gemini-4-neon",
			Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 0.25, RequestStatus: "pending", CreatedAt: nowFn(),
		}); err != nil {
			t.Fatalf("post-rollover call failed: %v", err)
		}

		ent, err := bSvc.GetEntitlement(ctx, u.ID)
		if err != nil {
			t.Fatalf("GetEntitlement: %v", err)
		}
		wantStart := time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)
		wantEnd := time.Date(2027, 4, 10, 12, 0, 0, 0, time.UTC)
		if ent.Tier != billing.TierStandard || ent.PendingTier != nil || !ent.PeriodStart.Equal(wantStart) || ent.PeriodEnd == nil || !ent.PeriodEnd.Equal(wantEnd) {
			t.Fatalf("unexpected rolled entitlement: %+v", ent)
		}

		grants, err := bStore.ListCreditGrants(ctx, u.ID)
		if err != nil {
			t.Fatalf("ListCreditGrants: %v", err)
		}
		if len(grants) != 3 {
			t.Fatalf("grant count = %d, want 3 (trial + 2026-11 + 2027-03; no back-credit stacking): %+v", len(grants), grants)
		}
		foundMar := false
		for _, g := range grants {
			if g.ID == billing.ManualPeriodGrantID(u.ID, wantStart) && g.AmountMicros == 10*billing.MicrosPerUSD {
				foundMar = true
			}
		}
		if !foundMar {
			t.Fatalf("missing deterministic %s $10 grant in %+v", billing.ManualPeriodGrantID(u.ID, wantStart), grants)
		}

		// Auto-pause excess routines on downgrade (17 -> 15: pauses newest 2).
		paused, err := app.PauseExcessRoutines(ctx, sched, ent.EffectivePolicy().MaxRoutines)
		if err != nil || paused != 2 {
			t.Fatalf("PauseExcessRoutines = (%d, %v), want (2, nil)", paused, err)
		}
	})

	// 4. Suspension + LISTEN nori_wake 'entitlement:<uid>' + Stripe active NEVER unsuspends.
	t.Run("Billing_SuspendNotifyAndStripeActiveNeverUnsuspends", func(t *testing.T) {
		setNow(time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC))
		u, db := newUser("suspend")

		listener, ok := workBackend.(docstore.WakeListener)
		if !ok {
			t.Fatal("backend does not implement docstore.WakeListener")
		}
		wakeCh := make(chan docstore.WakeEvent, 8)
		listenCtx, cancelListen := context.WithCancel(ctx)
		defer cancelListen()
		go func() {
			_ = listener.ListenWakes(listenCtx, func(ev docstore.WakeEvent) {
				parsed := docstore.ParseWakePayload(ev.Payload)
				if parsed.Kind == docstore.WakeKindEntitlement && parsed.UserID == u.ID {
					wakeCh <- ev
				}
			})
		}()
		time.Sleep(50 * time.Millisecond)

		entSusp, err := bSvc.Suspend(ctx, u.ID, "chargeback")
		if err != nil || !entSusp.IsSuspended() {
			t.Fatalf("Suspend: %+v err=%v", entSusp, err)
		}
		select {
		case ev := <-wakeCh:
			parsed := docstore.ParseWakePayload(ev.Payload)
			if parsed.Kind != docstore.WakeKindEntitlement || parsed.UserID != u.ID {
				t.Fatalf("unexpected wake event: %+v", ev)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for LISTEN nori_wake 'entitlement:<uid>'")
		}

		// Stripe invoice.paid setting status='active' NEVER clears suspended_at.
		pEnd := nowFn().AddDate(0, 1, 0)
		afterStripe, err := bSvc.ApplyEvent(ctx, billing.Change{
			Provider:          billing.ProviderStripe,
			Environment:       billing.EnvProduction,
			EventID:           "evt_conf_" + uuid.NewString()[:8],
			EventType:         "invoice.paid",
			ProviderUpdatedAt: nowFn().Add(time.Minute),
			UserID:            u.ID,
			Subscription: &billing.SubscriptionSnapshot{
				ExternalSubscriptionID: "sub_conf_" + uuid.NewString()[:8],
				Tier:                   billing.TierPower,
				Status:                 billing.StatusActive,
				PeriodStart:            nowFn(),
				PeriodEnd:              &pEnd,
			},
			CreditDeltaMicros: 25 * billing.MicrosPerUSD,
			Payload:           json.RawMessage(`{"conformance":true}`),
		})
		if err != nil {
			t.Fatalf("ApplyEvent: %v", err)
		}
		if afterStripe.Tier != billing.TierPower || afterStripe.Status != billing.StatusActive || !afterStripe.IsSuspended() {
			t.Fatalf("Stripe ApplyEvent must preserve suspension while updating billing: %+v", afterStripe)
		}

		meter := usage.WithBillingAdmission(db, 0, 0, nowFn)
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "susp-blocked", Provider: "gateway", Model: "google/gemini-4-neon",
			Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: nowFn(),
		}); !errors.Is(err, billing.ErrAccountSuspended) {
			t.Fatalf("suspended user ReserveUsage after Stripe active = %v, want ErrAccountSuspended", err)
		}
	})

	// 5. BYOK bypass ($0 hosted credit draw, $0 global hosted budget draw, 24h BYOK safety rail).
	t.Run("Billing_BYOKBypassAnd24hSafetyRail", func(t *testing.T) {
		setNow(time.Date(2026, 12, 1, 10, 0, 0, 0, time.UTC))
		u, db := newUser("byok")
		if _, err := bSvc.SetTier(ctx, u.ID, billing.TierStandard, billing.SetTierOptions{}); err != nil {
			t.Fatalf("SetTier standard: %v", err)
		}
		balBefore, err := bStore.CreditBalance(ctx, u.ID)
		if err != nil {
			t.Fatalf("CreditBalance before: %v", err)
		}
		gbBeforeAll, _ := bStore.GetGlobalBudget(ctx, nowFn(), billing.ScopeHostedAll)
		gbBeforeExa, _ := bStore.GetGlobalBudget(ctx, nowFn(), billing.ScopeHostedExa)

		meter := usage.WithBillingAdmission(db, 0, 0, nowFn)
		// Spend $14.50 on BYOK LLM + $0.013 on BYOK Exa.
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "byok-llm-1450", Provider: "openai", Model: "gpt-6.2-sol",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 14.50, RequestStatus: "pending", CreatedAt: nowFn(),
		}); err != nil {
			t.Fatalf("BYOK LLM reserve: %v", err)
		}
		actualLLM := 14.50
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "byok-llm-1450", Provider: "openai", Model: "gpt-6.2-sol",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
			CostUSD: &actualLLM, CostStatus: "calculated", RequestStatus: "received", CreatedAt: nowFn(),
		}); err != nil {
			t.Fatalf("BYOK LLM finalize: %v", err)
		}
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "byok-exa-1", Provider: "exa", Model: "exa-search",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategorySearch,
			EstimatedExposureUSD: 0.013, RequestStatus: "pending", CreatedAt: nowFn(),
		}); err != nil {
			t.Fatalf("BYOK Exa reserve: %v", err)
		}
		actualExa := 0.013
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "byok-exa-1", Provider: "exa", Model: "exa-search",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategorySearch,
			CostUSD: &actualExa, CostStatus: "provider", RequestStatus: "received", CreatedAt: nowFn(),
		}); err != nil {
			t.Fatalf("BYOK Exa finalize: %v", err)
		}

		// Hosted balance and global hosted budgets unchanged.
		balAfter, _ := bStore.CreditBalance(ctx, u.ID)
		if balAfter.AvailableMicros != balBefore.AvailableMicros || balAfter.SpentMicros != balBefore.SpentMicros {
			t.Fatalf("BYOK touched hosted balance: before=%+v after=%+v", balBefore, balAfter)
		}
		gbAfterAll, _ := bStore.GetGlobalBudget(ctx, nowFn(), billing.ScopeHostedAll)
		gbAfterExa, _ := bStore.GetGlobalBudget(ctx, nowFn(), billing.ScopeHostedExa)
		if gbAfterAll.SpentMicros != gbBeforeAll.SpentMicros || gbAfterExa.SpentMicros != gbBeforeExa.SpentMicros {
			t.Fatalf("BYOK touched hosted global budget: all %d->%d exa %d->%d",
				gbBeforeAll.SpentMicros, gbAfterAll.SpentMicros, gbBeforeExa.SpentMicros, gbAfterExa.SpentMicros)
		}

		// Standard 24h BYOK safety rail is $15.00 -> next $0.60 BYOK call rejected!
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "byok-over-rail", Provider: "openai", Model: "gpt-6.2-sol",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 0.60, RequestStatus: "pending", CreatedAt: nowFn(),
		}); !errors.Is(err, usage.ErrUserQuotaExceeded) {
			t.Fatalf("expected ErrUserQuotaExceeded at $15 BYOK rail, got %v", err)
		}

		// After 24h + 1m, rolling window clears -> BYOK admitted again.
		advance(24*time.Hour + time.Minute)
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "byok-after-24h", Provider: "openai", Model: "gpt-6.2-sol",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 0.60, RequestStatus: "pending", CreatedAt: nowFn(),
		}); err != nil {
			t.Fatalf("BYOK after 24h rolloff failed: %v", err)
		}
	})

	// 6. Sprite 15m idle auto-sleep + exhausted/suspended reaper + concurrent zero double-bill + Fly reconciliation.
	t.Run("Billing_SpritesIdleAutoSleepReaperAndZeroDoubleBill", func(t *testing.T) {
		setNow(time.Date(2026, 12, 2, 12, 0, 0, 0, time.UTC))

		var (
			compStore computer.Store
			sprBS     billing.Store
			sprSvc    *billing.Service
		)
		if pgPool != nil {
			compStore = computer.NewPostgresStore(pgPool)
			sprBS = bStore
			sprSvc = bSvc
		} else {
			ms := computer.NewMemoryStore()
			compStore = ms
			mbs := billing.NewMemoryStore(nowFn)
			mbs.UsageReader = ms.UsageSamplesForBilling
			sprBS = mbs
			sprSvc = billing.NewService(mbs, false, billing.GlobalBudgetConfig{})
			sprSvc.Now = nowFn
		}

		fp := fake.New()
		fp.Now = nowFn
		mgr := computer.NewManager(fp, compStore, computer.Config{
			RatePerHourUSD: 0.15, // $0.0025/min = 2500 micros/min
			IdleAutoSleep:  15 * time.Minute,
		})
		mgr.SetClock(nowFn)
		mgr.SetBilling(sprSvc)

		uRace := "spr-race-" + uuid.NewString()[:8]
		uIdle := "spr-idle-" + uuid.NewString()[:8]
		uExhaust := "spr-exh-" + uuid.NewString()[:8]
		uFly := "spr-fly-" + uuid.NewString()[:8]

		for _, uid := range []string{uRace, uIdle, uFly} {
			if _, err := sprBS.SetTier(ctx, uid, billing.TierStandard, billing.SetTierOptions{}); err != nil {
				t.Fatalf("SetTier %s: %v", uid, err)
			}
		}
		oneCent := int64(10_000)
		if _, err := sprBS.SetTier(ctx, uExhaust, billing.TierStandard, billing.SetTierOptions{CreditOverrideMicros: &oneCent}); err != nil {
			t.Fatalf("SetTier uExhaust: %v", err)
		}

		// A. Concurrent janitor + Sleep on uRace after 12m -> exact 30_000 micros (zero double-bill).
		if _, err := mgr.Ensure(ctx, uRace); err != nil {
			t.Fatalf("Ensure uRace: %v", err)
		}
		advance(12 * time.Minute)
		var wg sync.WaitGroup
		goStart := make(chan struct{})
		for i := 0; i < 4; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-goStart
				_ = mgr.RunJanitorPass(ctx)
			}()
			go func() {
				defer wg.Done()
				<-goStart
				_ = mgr.Sleep(ctx, uRace)
			}()
		}
		close(goStart)
		wg.Wait()

		balRace, err := sprBS.CreditBalance(ctx, uRace)
		if err != nil || balRace.SpentMicros != 30_000 {
			t.Fatalf("uRace spent micros = %+v (err=%v), want 30_000 (12m, zero double-bill)", balRace, err)
		}

		// B. 15m idle auto-sleep on uIdle.
		if _, err := mgr.Ensure(ctx, uIdle); err != nil {
			t.Fatalf("Ensure uIdle: %v", err)
		}
		advance(15 * time.Minute)
		if err := mgr.RunJanitorPass(ctx); err != nil {
			t.Fatalf("janitor 15m idle: %v", err)
		}
		recIdle, err := compStore.GetComputer(ctx, uIdle)
		if err != nil || recIdle.Status != computer.StatusSleeping {
			t.Fatalf("uIdle after 15m idle = (%+v, %v), want sleeping", recIdle, err)
		}
		balIdle, _ := sprBS.CreditBalance(ctx, uIdle)
		if balIdle.SpentMicros != 37_500 {
			t.Fatalf("uIdle spent micros = %d, want 37_500 (15m)", balIdle.SpentMicros)
		}

		// C. Credit-exhausted reaper on uExhaust ($0.01 credit -> 6m awake = $0.015 -> reaped).
		if _, err := mgr.Ensure(ctx, uExhaust); err != nil {
			t.Fatalf("Ensure uExhaust: %v", err)
		}
		advance(6 * time.Minute)
		if err := mgr.RunJanitorPass(ctx); err != nil {
			t.Fatalf("janitor exhaust reap: %v", err)
		}
		recExh, _ := compStore.GetComputer(ctx, uExhaust)
		if recExh.Status != computer.StatusSleeping {
			t.Fatalf("uExhaust status = %q, want sleeping", recExh.Status)
		}
		if _, err := mgr.Ensure(ctx, uExhaust); !errors.Is(err, billing.ErrCreditExhausted) {
			t.Fatalf("Ensure exhausted = %v, want ErrCreditExhausted", err)
		}

		// D. Fly already-sleeping reconciliation prevents phantom billing.
		recFly, err := mgr.Ensure(ctx, uFly)
		if err != nil {
			t.Fatalf("Ensure uFly: %v", err)
		}
		advance(4 * time.Minute)
		if _, err := mgr.Exec(ctx, uFly, computer.ExecRequest{Command: "echo hi"}); err != nil {
			t.Fatalf("Exec uFly: %v", err)
		}
		fp.SetStatus(recFly.ProviderID, computer.StatusSleeping)
		advance(20 * time.Minute)
		if err := mgr.RunJanitorPass(ctx); err != nil {
			t.Fatalf("janitor Fly reconcile: %v", err)
		}
		balFly, _ := sprBS.CreditBalance(ctx, uFly)
		if balFly.SpentMicros != 10_000 {
			t.Fatalf("uFly reconciled spent micros = %d, want 10_000 (4m only, zero phantom billing)", balFly.SpentMicros)
		}
	})
}
