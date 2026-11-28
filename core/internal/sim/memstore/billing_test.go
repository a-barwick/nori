package memstore_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/billing"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
)

func TestMemstoreBillingConcurrentOneCentRace(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC)
	b := memstore.New(memstore.WithClock(func() time.Time { return now }))

	user := users.User{ID: uuid.NewString(), Email: "race@example.com", TimeZone: "UTC"}
	dir := records.NewSharedUsers(b)
	if err := dir.Create(ctx, user); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	// Replace credit with exact $0.01 (10_000 micros) remaining.
	zero := int64(10_000)
	if _, err := b.SetTier(ctx, user.ID, billing.TierStandard, billing.SetTierOptions{
		CreditOverrideMicros: &zero,
	}); err != nil {
		t.Fatalf("SetTier: %v", err)
	}
	// Consume the $5.00 trial grant so only the $0.01 standard grant remains.
	cost5 := 5.0
	db := records.NewShared(b, user, 10)
	db.Now = func() time.Time { return now }
	db.EnforceBilling = true
	if err := db.RecordUsage(ctx, state.Usage{
		ID: "prior-trial-spend", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		CostUSD: &cost5, CostStatus: "provider", RequestStatus: "received", CreatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed prior spend: %v", err)
	}

	bal, err := b.CreditBalance(ctx, user.ID)
	if err != nil || bal.AvailableMicros != 10_000 {
		t.Fatalf("expected 10_000 micros remaining before race, got %+v (err=%v)", bal, err)
	}

	const workers = 20
	var wg sync.WaitGroup
	var allowed, exhausted, otherErr atomic.Int64
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			ok, err := db.ReserveUsage(ctx, state.Usage{
				ID:                   fmt.Sprintf("call-%d", idx),
				Provider:             "gateway",
				Model:                "google/gemini-4-hydrogen",
				Billing:              state.UsageBillingHosted,
				Category:             state.UsageCategoryLLM,
				EstimatedExposureUSD: 0.01,
				CostStatus:           "unknown",
				RequestStatus:        "pending",
				CreatedAt:            now,
			}, now.Add(-time.Hour), now.Add(-24*time.Hour), 300, 15, 0.01)
			if err == nil && ok {
				allowed.Add(1)
			} else if errors.Is(err, billing.ErrCreditExhausted) {
				exhausted.Add(1)
			} else {
				otherErr.Add(1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if allowed.Load() != 1 || exhausted.Load() != workers-1 || otherErr.Load() != 0 {
		t.Fatalf("race result: allowed=%d exhausted=%d other=%d, want 1 / %d / 0",
			allowed.Load(), exhausted.Load(), otherErr.Load(), workers-1)
	}
	bal, err = b.CreditBalance(ctx, user.ID)
	if err != nil || bal.AvailableMicros != 0 {
		t.Fatalf("balance after race = %+v, want 0", bal)
	}
}

func TestMemstoreBillingTrialCapAndSuspensionAndWakes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC)
	b := memstore.New(memstore.WithClock(func() time.Time { return now }))

	dir := records.NewSharedUsers(b)
	dir.Now = func() time.Time { return now }
	dir.SetTrialMaxSignupsPerDay(2)

	u1 := users.User{ID: uuid.NewString(), Email: "t1@example.com", TimeZone: "UTC"}
	u2 := users.User{ID: uuid.NewString(), Email: "t2@example.com", TimeZone: "UTC"}
	u3 := users.User{ID: uuid.NewString(), Email: "t3@example.com", TimeZone: "UTC"}
	if err := dir.Create(ctx, u1); err != nil {
		t.Fatalf("create u1: %v", err)
	}
	if err := dir.Create(ctx, u2); err != nil {
		t.Fatalf("create u2: %v", err)
	}
	if err := dir.Create(ctx, u3); !errors.Is(err, billing.ErrTrialLimitExceeded) {
		t.Fatalf("expected ErrTrialLimitExceeded on 3rd trial, got %v", err)
	}
	// Failed trial cap must roll back user creation atomically.
	if _, err := dir.Get(ctx, u3.ID); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("expected u3 rolled back, got err=%v", err)
	}

	// Trial blocks BYOK.
	db1 := records.NewShared(b, u1, 10)
	db1.Now = func() time.Time { return now }
	db1.EnforceBilling = true
	db1.GlobalBudget = billing.GlobalBudgetConfig{
		HostedAllMaxMicrosPerDay: 50_000_000,
		HostedExaMaxMicrosPerDay: 20_000, // $0.02 Exa daily cap
	}
	meter := usage.WithBillingAdmission(db1, 0, 0, func() time.Time { return now })
	err := meter.RecordUsage(ctx, state.Usage{
		ID: "byok-on-trial", Provider: "openai", Model: "gpt-6.2-sol",
		Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: now,
	})
	if !errors.Is(err, billing.ErrBYOKNotAllowed) {
		t.Fatalf("expected ErrBYOKNotAllowed on trial, got %v", err)
	}

	// Hosted Exa hits global Exa cap ($0.02) on 2nd $0.013 search and reconciles cleanly on finalize.
	err = meter.RecordUsage(ctx, state.Usage{
		ID: "exa-1", Provider: "exa", Model: "exa-search",
		Billing: state.UsageBillingHosted, Category: state.UsageCategorySearch,
		EstimatedExposureUSD: 0.013, RequestStatus: "pending", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("first exa search failed: %v", err)
	}
	err = meter.RecordUsage(ctx, state.Usage{
		ID: "exa-2", Provider: "exa", Model: "exa-search",
		Billing: state.UsageBillingHosted, Category: state.UsageCategorySearch,
		EstimatedExposureUSD: 0.013, RequestStatus: "pending", CreatedAt: now,
	})
	if !errors.Is(err, billing.ErrGlobalBudgetExceeded) {
		t.Fatalf("expected ErrGlobalBudgetExceeded on 2nd exa search, got %v", err)
	}
	// Finalize exa-1 at $0.005 -> global budget reconciles down from 13_000 to 5_000 micros so exa-2 now fits!
	actual := 0.005
	if err := meter.RecordUsage(ctx, state.Usage{
		ID: "exa-1", Provider: "exa", Model: "exa-search",
		Billing: state.UsageBillingHosted, Category: state.UsageCategorySearch,
		CostUSD: &actual, CostStatus: "provider", RequestStatus: "received", CreatedAt: now,
	}); err != nil {
		t.Fatalf("finalize exa-1: %v", err)
	}
	if err := meter.RecordUsage(ctx, state.Usage{
		ID: "exa-2", Provider: "exa", Model: "exa-search",
		Billing: state.UsageBillingHosted, Category: state.UsageCategorySearch,
		EstimatedExposureUSD: 0.013, RequestStatus: "pending", CreatedAt: now,
	}); err != nil {
		t.Fatalf("expected exa-2 to succeed after global budget reconciliation, got %v", err)
	}

	// Wake listener receives entitlement:<uid> on Suspend.
	wakeCh := make(chan docstore.WakeEvent, 4)
	listenCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_ = b.ListenWakes(listenCtx, func(ev docstore.WakeEvent) {
			wakeCh <- ev
		})
	}()
	time.Sleep(10 * time.Millisecond)

	if _, err := b.Suspend(ctx, u1.ID, "policy violation"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	select {
	case ev := <-wakeCh:
		parsed := docstore.ParseWakePayload(ev.Payload)
		if parsed.Kind != docstore.WakeKindEntitlement || parsed.UserID != u1.ID {
			t.Fatalf("unexpected wake event: %+v (parsed=%+v)", ev, parsed)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for entitlement wake event")
	}

	err = meter.RecordUsage(ctx, state.Usage{
		ID: "after-suspend", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: now,
	})
	if !errors.Is(err, billing.ErrAccountSuspended) {
		t.Fatalf("expected ErrAccountSuspended, got %v", err)
	}

	// Snapshot / Restore preserves billing state.
	snap := b.Snapshot()
	b2 := memstore.New(memstore.WithClock(func() time.Time { return now }))
	b2.Restore(snap)
	restoredEnt, err := b2.GetEntitlement(ctx, u1.ID)
	if err != nil || !restoredEnt.IsSuspended() || restoredEnt.SuspendedReason != "policy violation" {
		t.Fatalf("restored entitlement mismatch: %+v, %v", restoredEnt, err)
	}
}
