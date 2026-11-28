package sim_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/api"
	"nori/internal/app"
	"nori/internal/billing"
	"nori/internal/computer"
	"nori/internal/computer/fake"
	"nori/internal/connections"
	"nori/internal/conversation"
	"nori/internal/llmguard"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/permissions"
	"nori/internal/platform"
	"nori/internal/scheduler"
	"nori/internal/secret"
	"nori/internal/secret/tenantkey"
	"nori/internal/servicekeys"
	"nori/internal/settings"
	"nori/internal/sim/env"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
	"nori/internal/worker"
)

type simRoundTripFunc func(*http.Request) (*http.Response, error)

func (f simRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type simMemCredentialStore struct {
	mu   sync.Mutex
	vals map[string]secret.Credential
}

func newSimMemCredentialStore() *simMemCredentialStore {
	return &simMemCredentialStore{vals: map[string]secret.Credential{}}
}

func (s *simMemCredentialStore) Get(_ context.Context, id string) (secret.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.vals[id]
	if !ok {
		return secret.Credential{}, secret.ErrCredentialNotFound
	}
	return c, nil
}

func (s *simMemCredentialStore) Replace(_ context.Context, id string, expected int64, data []byte) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.vals[id]
	if expected != 0 && cur.Version != expected {
		return 0, secret.ErrCredentialConflict
	}
	next := cur.Version + 1
	s.vals[id] = secret.Credential{Version: next, Data: append([]byte(nil), data...)}
	return next, nil
}

func (s *simMemCredentialStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vals, id)
	return nil
}

// Scenario 1: Concurrent $0.01 hosted credit race.
// N concurrent reservations racing against exactly $0.01 remaining hosted credit ->
// at most 1 succeeds, remaining balance never goes negative, zero double-spend,
// and finalization below $0.01 reconciles both user credit and fleet global budget.
func TestDST_BillingConcurrentOneCentCreditRace(t *testing.T) {
	ctx := context.Background()
	for _, seed := range []uint64{101, 202, 303, 404} {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
			clock := env.NewClock(time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC))
			b := memstore.New(memstore.WithClock(clock.Now))
			t.Cleanup(func() { _ = b.Close() })

			user := users.User{
				ID:          fmt.Sprintf("11111111-1111-4111-8111-%012d", seed),
				Email:       fmt.Sprintf("race-%d@example.com", seed),
				DisplayName: "Race Tester",
				TimeZone:    "UTC",
			}
			dir := records.NewSharedUsers(b)
			dir.Now = clock.Now
			if err := dir.Create(ctx, user); err != nil {
				t.Fatalf("Create user: %v", err)
			}

			// Spend $4.99 of the $5.00 trial grant so strictly $0.01 (10_000 micros) remains.
			db := records.NewShared(b, user, 10.0)
			db.Now = clock.Now
			db.EnforceBilling = true
			db.GlobalBudget = billing.GlobalBudgetConfig{
				HostedAllMaxMicrosPerDay: 50 * billing.MicrosPerUSD,
				HostedExaMaxMicrosPerDay: 15 * billing.MicrosPerUSD,
			}
			prior := 4.99
			if err := db.RecordUsage(ctx, state.Usage{
				ID:            "prior-spend-499",
				Provider:      "gateway",
				Model:         "google/gemini-4-neon",
				Billing:       state.UsageBillingHosted,
				Category:      state.UsageCategoryLLM,
				CostUSD:       &prior,
				CostStatus:    "provider",
				RequestStatus: "received",
				CreatedAt:     clock.Now().Add(-time.Minute),
			}); err != nil {
				t.Fatalf("seed prior $4.99 spend: %v", err)
			}

			balBefore, err := b.CreditBalance(ctx, user.ID)
			if err != nil || balBefore.AvailableMicros != 10_000 {
				t.Fatalf("balance before race = %+v (err=%v), want 10_000 micros", balBefore, err)
			}

			workers := 16 + rng.IntN(17) // 16..32 concurrent racers
			meter := usage.WithBillingAdmission(db, 0, 0, clock.Now)

			var (
				wg        sync.WaitGroup
				allowed   atomic.Int64
				exhausted atomic.Int64
				otherErr  atomic.Int64
				winnerID  atomic.Value
			)
			start := make(chan struct{})
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func(idx int) {
					defer wg.Done()
					<-start
					callID := fmt.Sprintf("race-seed%d-w%02d", seed, idx)
					err := meter.RecordUsage(ctx, state.Usage{
						ID:                   callID,
						Provider:             "gateway",
						Model:                "google/gemini-4-hydrogen",
						Billing:              state.UsageBillingHosted,
						Category:             state.UsageCategoryLLM,
						EstimatedExposureUSD: 0.01,
						CostStatus:           "unknown",
						RequestStatus:        "pending",
						CreatedAt:            clock.Now(),
					})
					switch {
					case err == nil:
						allowed.Add(1)
						winnerID.Store(callID)
					case errors.Is(err, billing.ErrCreditExhausted):
						exhausted.Add(1)
					default:
						otherErr.Add(1)
						t.Errorf("worker %d unexpected err: %v", idx, err)
					}
				}(i)
			}
			close(start)
			wg.Wait()

			if allowed.Load() != 1 || exhausted.Load() != int64(workers-1) || otherErr.Load() != 0 {
				t.Fatalf("race outcome: allowed=%d exhausted=%d other=%d, want 1/%d/0",
					allowed.Load(), exhausted.Load(), otherErr.Load(), workers-1)
			}

			balAfter, err := b.CreditBalance(ctx, user.ID)
			if err != nil {
				t.Fatalf("CreditBalance after race: %v", err)
			}
			if balAfter.AvailableMicros < 0 {
				t.Fatalf("balance went negative: %+v", balAfter)
			}
			if balAfter.AvailableMicros != 0 || balAfter.PendingMicros != 10_000 {
				t.Fatalf("balance after race = %+v, want Available=0 Pending=10_000", balAfter)
			}

			// Global budget incremented by exactly $0.01 (not N * $0.01).
			gbAll, _ := b.GetGlobalBudget(ctx, clock.Now(), billing.ScopeHostedAll)
			gbLLM, _ := b.GetGlobalBudget(ctx, clock.Now(), billing.ScopeHostedLLM)
			if gbAll.SpentMicros != 5_000_000 || gbLLM.SpentMicros != 5_000_000 {
				t.Fatalf("global budget mismatch after race: all=%+v llm=%+v", gbAll, gbLLM)
			}

			// Finalize winning call at $0.004 -> reconciles both user balance ($0.006 remaining)
			// and fleet global budget down by $0.006, allowing a subsequent $0.005 call.
			actual := 0.004
			winID := winnerID.Load().(string)
			if err := meter.RecordUsage(ctx, state.Usage{
				ID:            winID,
				Provider:      "gateway",
				Model:         "google/gemini-4-hydrogen",
				Billing:       state.UsageBillingHosted,
				Category:      state.UsageCategoryLLM,
				CostUSD:       &actual,
				CostStatus:    "provider",
				RequestStatus: "received",
				CreatedAt:     clock.Now(),
			}); err != nil {
				t.Fatalf("finalize winner: %v", err)
			}
			balReconciled, _ := b.CreditBalance(ctx, user.ID)
			if balReconciled.AvailableMicros != 6_000 {
				t.Fatalf("balance after finalize = %+v, want 6_000 micros", balReconciled)
			}
			gbAllAfter, _ := b.GetGlobalBudget(ctx, clock.Now(), billing.ScopeHostedAll)
			if gbAllAfter.SpentMicros != 4_994_000 {
				t.Fatalf("global budget after finalize = %+v, want 4_994_000", gbAllAfter)
			}
			if err := meter.RecordUsage(ctx, state.Usage{
				ID:                   "followup-005",
				Provider:             "gateway",
				Model:                "google/gemini-4-neon",
				Billing:              state.UsageBillingHosted,
				Category:             state.UsageCategoryLLM,
				EstimatedExposureUSD: 0.005,
				CostStatus:           "unknown",
				RequestStatus:        "pending",
				CreatedAt:            clock.Now(),
			}); err != nil {
				t.Fatalf("followup $0.005 call should succeed after reconciliation: %v", err)
			}
		})
	}
}

// Scenario 2: trial cutoff & BYOK/Home AI block.
// trial user spends up to $5.00 hosted credit -> next hosted call rejected with 402 credit_exhausted;
// BYOK and Home AI are rejected with ErrBYOKNotAllowed on trial both before AND after exhaustion,
// and trial never auto-renews even after advancing months.
func TestDST_BillingTrialCutoffAndBYOKHomeAIBlock(t *testing.T) {
	ctx := context.Background()
	clock := env.NewClock(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	backend := memstore.New(memstore.WithClock(clock.Now))
	t.Cleanup(func() { _ = backend.Close() })

	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore { return memstore.NewArtifacts() })
	storage.Now = clock.Now
	cfg := settings.Settings{
		Cloud:                 "local",
		EnvProfile:            "test",
		TimeZone:              "UTC",
		TaskBudgetUSD:         5.0,
		TrialMaxSignupsPerDay: 25,
		Features:              settings.Features{DisableJev: true},
		Connections: settings.Connections{
			CredentialStore: "database",
			CredentialKey:   hex.EncodeToString(bytes.Repeat([]byte{0x21}, 32)),
		},
		Models: settings.Models{BYOKRequired: true},
	}
	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	t.Cleanup(svc.Close)

	trialUser := users.User{
		ID:          "22222222-2222-4222-8222-222222222201",
		Email:       "trial-cutoff@example.com",
		DisplayName: "Trial User",
		TimeZone:    "UTC",
	}
	if err := svc.Users.Create(ctx, trialUser); err != nil {
		t.Fatalf("Create trialUser: %v", err)
	}

	db := records.NewShared(backend, trialUser, 10.0)
	db.Now = clock.Now
	db.EnforceBilling = true
	db.GlobalBudget = billing.GlobalBudgetConfig{
		HostedAllMaxMicrosPerDay: 50 * billing.MicrosPerUSD,
		HostedExaMaxMicrosPerDay: 15 * billing.MicrosPerUSD,
	}
	meter := usage.WithBillingAdmission(db, 0, 0, clock.Now)

	store := state.Compose(db, memstore.NewArtifacts())
	tasks := &conversation.Tasks{Store: store, Now: clock.Now}
	runner := conversation.New(nil, nil, nil, tasks)
	runner.Now = clock.Now
	conn := connections.New(store, secret.Unavailable{}, "http://127.0.0.1:3211/cb")
	perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow", Now: clock.Now}
	httpOK := &http.Client{Transport: simRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":[],"results":[]}`)),
		}, nil
	})}
	svcKeys := &servicekeys.Service{Store: newSimMemCredentialStore(), Now: clock.Now, HTTPClient: httpOK}
	modelKeys := &modelkeys.Service{Store: newSimMemCredentialStore(), Now: clock.Now, HTTPClient: httpOK, HostedAvailable: true}
	account := &modelprofiles.Account{
		Keys:          modelKeys,
		Store:         db,
		HostedEnabled: true,
		AllowBYOK: func(ctx context.Context) (bool, error) {
			ent, err := svc.GetEntitlement(ctx, trialUser.ID)
			if err != nil {
				return false, err
			}
			return ent.EffectivePolicy().AllowBYOK, nil
		},
		AllowHomeAI: func(ctx context.Context) (bool, error) {
			ent, err := svc.GetEntitlement(ctx, trialUser.ID)
			if err != nil {
				return false, err
			}
			return ent.EffectivePolicy().AllowHomeAI, nil
		},
	}

	assistant := &api.Assistant{
		UserID:      trialUser.ID,
		Store:       store,
		Connections: conn,
		Permissions: perms,
		ModelKeys:   modelKeys,
		ServiceKeys: svcKeys,
		Billing:     svc.Billing(),
		UsageNow:    clock.Now,
	}
	apiHandler := api.Handler(api.Deps{Runner: runner, Tasks: tasks, Assistant: assistant})

	assertBYOKAndHomeBlocked := func(phase string) {
		t.Helper()
		// 1. Usage admission blocks both BYOK and Home AI ("local").
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "byok-" + phase, Provider: "openai", Model: "gpt-6.2-sol",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: clock.Now(),
		}); !errors.Is(err, billing.ErrBYOKNotAllowed) {
			t.Fatalf("[%s] BYOK admission err = %v, want ErrBYOKNotAllowed", phase, err)
		}
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: "home-" + phase, Provider: "home", Model: "inkling-mini-2",
			Billing: state.UsageBillingLocal, Category: state.UsageCategoryLLM,
			CostStatus: "local", RequestStatus: "pending", CreatedAt: clock.Now(),
		}); !errors.Is(err, billing.ErrBYOKNotAllowed) {
			t.Fatalf("[%s] Home AI admission err = %v, want ErrBYOKNotAllowed", phase, err)
		}
		// 2. app.Service.CheckHomeAIAccess rejects trial.
		if err := svc.CheckHomeAIAccess(ctx, trialUser.ID); !errors.Is(err, billing.ErrBYOKNotAllowed) {
			t.Fatalf("[%s] CheckHomeAIAccess err = %v, want ErrBYOKNotAllowed", phase, err)
		}
		// 3. modelprofiles blocks BYOK & Home AI Save/Resolve.
		view, err := account.View(ctx)
		if err != nil {
			t.Fatalf("[%s] account.View: %v", phase, err)
		}
		if _, err := account.Save(ctx, state.ModelSelection{ChatProfileID: "openai-sol", ChatReasoning: "low"}, view.Revision, nil); !errors.Is(err, billing.ErrBYOKNotAllowed) {
			t.Fatalf("[%s] Save BYOK profile err = %v, want ErrBYOKNotAllowed", phase, err)
		}
		if _, err := account.Save(ctx, state.ModelSelection{ChatProfileID: "home-device1"}, view.Revision, nil); !errors.Is(err, billing.ErrBYOKNotAllowed) {
			t.Fatalf("[%s] Save Home AI profile err = %v, want ErrBYOKNotAllowed", phase, err)
		}
		// 4. HTTP API rejects PUT /v1/settings/service-keys/exa with 402 byok_not_allowed.
		req := httptest.NewRequest(http.MethodPut, "/v1/settings/service-keys/exa", strings.NewReader(`{"api_key":"exa_trial_blocked"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		apiHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), billing.CodeBYOKNotAllowed) {
			t.Fatalf("[%s] PUT service-keys/exa = %d %s, want 402 byok_not_allowed", phase, rec.Code, rec.Body.String())
		}
	}

	// Phase A: Before exhaustion, BYOK & Home AI must already be blocked on trial.
	assertBYOKAndHomeBlocked("before_exhaustion")

	// Phase B: Spend exact $5.00 across 5 hosted calls of $1.00 each.
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("trial-hosted-%d", i)
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: id, Provider: "gateway", Model: "google/gemini-4-neon",
			Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 1.00, CostStatus: "unknown", RequestStatus: "pending",
			CreatedAt: clock.Now(),
		}); err != nil {
			t.Fatalf("hosted call #%d reserve failed: %v", i, err)
		}
		actual := 1.00
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: id, Provider: "gateway", Model: "google/gemini-4-neon",
			Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
			CostUSD: &actual, CostStatus: "provider", RequestStatus: "received",
			CreatedAt: clock.Now(),
		}); err != nil {
			t.Fatalf("hosted call #%d finalize failed: %v", i, err)
		}
		clock.Advance(time.Minute)
	}

	bal, err := backend.CreditBalance(ctx, trialUser.ID)
	if err != nil || bal.AvailableMicros != 0 {
		t.Fatalf("balance after $5.00 spend = %+v (err=%v), want 0", bal, err)
	}

	// Phase C: Next hosted call is rejected with ErrCreditExhausted -> HTTP 402 credit_exhausted.
	err = meter.RecordUsage(ctx, state.Usage{
		ID: "trial-over-limit", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.01, CostStatus: "unknown", RequestStatus: "pending",
		CreatedAt: clock.Now(),
	})
	if !errors.Is(err, billing.ErrCreditExhausted) {
		t.Fatalf("expected ErrCreditExhausted after $5.00 trial spend, got %v", err)
	}
	if billing.HTTPStatus(err) != http.StatusPaymentRequired || billing.ErrorCode(err) != billing.CodeCreditExhausted {
		t.Fatalf("HTTP mapping = (%d, %q), want (402, credit_exhausted)", billing.HTTPStatus(err), billing.ErrorCode(err))
	}

	// Computer Ensure on exhausted trial also rejects with ErrCreditExhausted.
	compMgr := computer.NewManager(fake.New(), computer.NewMemoryStore(), computer.Config{RatePerHourUSD: 0.15})
	compMgr.SetClock(clock.Now)
	compMgr.SetBilling(svc.Billing())
	if _, err := compMgr.Ensure(ctx, trialUser.ID); !errors.Is(err, billing.ErrCreditExhausted) {
		t.Fatalf("computer.Ensure on exhausted trial = %v, want ErrCreditExhausted", err)
	}

	// GET /v1/costs remains readable (200) and reports $0 remaining.
	costReq := httptest.NewRequest(http.MethodGet, "/v1/costs?days=7", nil)
	costRec := httptest.NewRecorder()
	apiHandler.ServeHTTP(costRec, costReq)
	if costRec.Code != http.StatusOK {
		t.Fatalf("GET /v1/costs on exhausted trial = %d %s", costRec.Code, costRec.Body.String())
	}

	// Phase D: After exhaustion, BYOK & Home AI remain blocked.
	assertBYOKAndHomeBlocked("after_exhaustion")

	// Phase E: Advancing 60 days NEVER renews a one-time trial grant.
	clock.Advance(60 * 24 * time.Hour)
	balLater, err := backend.CreditBalance(ctx, trialUser.ID)
	if err != nil || balLater.AvailableMicros != 0 {
		t.Fatalf("trial renewed unexpectedly after 60d: %+v (err=%v)", balLater, err)
	}
	if err := meter.RecordUsage(ctx, state.Usage{
		ID: "trial-after-60d", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: clock.Now(),
	}); !errors.Is(err, billing.ErrCreditExhausted) {
		t.Fatalf("expected ErrCreditExhausted after 60d, got %v", err)
	}
}

// Scenario 3: manual monthly rollover.
// standard/power user with billing_provider='manual' advances past period_end (including
// multi-month inactivity & pending_tier downgrade) -> rolls forward to current period,
// inserts single deterministic manual:<uid>:<YYYY-MM> grant (no back-credit stacking),
// applies pending_tier, and auto-pauses excess routines newest-first while preserving system routines.
func TestDST_BillingManualMonthlyRolloverAndPendingDowngrade(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	clock := env.NewClock(start)
	backend := memstore.New(memstore.WithClock(clock.Now))
	t.Cleanup(func() { _ = backend.Close() })

	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore { return memstore.NewArtifacts() })
	storage.Now = clock.Now
	cfg := settings.Settings{
		Cloud:                 "local",
		EnvProfile:            "test",
		TimeZone:              "UTC",
		TaskBudgetUSD:         5.0,
		TrialMaxSignupsPerDay: 25,
		Features:              settings.Features{DisableJev: true},
		Connections: settings.Connections{
			CredentialStore: "database",
			CredentialKey:   hex.EncodeToString(bytes.Repeat([]byte{0x33}, 32)),
		},
		Models: settings.Models{BYOKRequired: true},
	}
	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	svc.ConfigureApp = func(_ users.User, a *app.App) {
		if a.Scheduler != nil {
			a.Scheduler.Now = clock.Now
		}
	}
	t.Cleanup(svc.Close)

	u := users.User{
		ID:          "33333333-3333-4333-8333-333333333301",
		Email:       "manual-rollover@example.com",
		DisplayName: "Manual Power",
		TimeZone:    "UTC",
	}
	if err := svc.Users.Create(ctx, u); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// Promote to manual power ($25/mo, MaxRoutines=30).
	ent, err := svc.Billing().SetTier(ctx, u.ID, billing.TierPower, billing.SetTierOptions{})
	if err != nil {
		t.Fatalf("SetTier power: %v", err)
	}
	if ent.PeriodEnd == nil || !ent.PeriodEnd.Equal(time.Date(2026, 12, 10, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("initial power period_end = %v, want 2026-12-10T12:00:00Z", ent.PeriodEnd)
	}

	// Create 18 enabled non-system routines + 1 system routine spaced 1 minute apart.
	userApp, release, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	for i := 1; i <= 18; i++ {
		clock.Advance(time.Minute)
		title := fmt.Sprintf("routine-%02d", i)
		rcpt, err := userApp.Scheduler.Save(ctx, "", uuid.NewString(), 0, scheduler.Editable{
			Title:                  title,
			Instruction:            "Run " + title,
			Trigger:                scheduler.Trigger{Kind: "recurring", LocalStart: "2026-11-11T08:00", Timezone: "UTC", RRule: "FREQ=DAILY"},
			ConnectionIDs:          []string{},
			BudgetUSD:              0.01,
			MaxActiveRuns:          1,
			MissedRun:              "skip",
			ExecutionWindowSeconds: 300,
			DestinationIDs:         []string{scheduler.InAppDestination},
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		if rcpt.Status != "applied" {
			t.Fatalf("create %s receipt status = %q (want applied)", title, rcpt.Status)
		}
	}
	// Seed 1 system routine directly in store so we verify system routines are excluded from cap pausing.
	sysNow := clock.Now()
	sysAuto := scheduler.Automation{
		ID:                     "sys-morning-brief",
		Title:                  "Morning Brief",
		Instruction:            "System routine",
		System:                 true,
		Status:                 scheduler.AutomationEnabled,
		Trigger:                scheduler.Trigger{Kind: "recurring", LocalStart: "2026-11-11T08:00", Timezone: "UTC", RRule: "FREQ=DAILY"},
		BudgetUSD:              0.1,
		MaxActiveRuns:          1,
		MissedRun:              "skip",
		ExecutionWindowSeconds: 300,
		DestinationIDs:         []string{scheduler.InAppDestination},
		Revision:               1,
		CreatedAt:              &sysNow,
		UpdatedAt:              &sysNow,
	}
	sysRes, err := state.NewResource(scheduler.CollectionAutomations, sysAuto.ID, 1, sysAuto, map[string]string{"status": string(scheduler.AutomationEnabled)}, sysNow)
	if err != nil {
		t.Fatalf("NewResource system automation: %v", err)
	}
	if _, err := userApp.Store.PutResource(ctx, sysRes, 0); err != nil {
		t.Fatalf("PutResource system automation: %v", err)
	}
	release()

	// Schedule pending_tier downgrade to standard ($10/mo, MaxRoutines=15).
	entPending, err := svc.Billing().SetTier(ctx, u.ID, billing.TierStandard, billing.SetTierOptions{Pending: true})
	if err != nil {
		t.Fatalf("SetTier pending standard: %v", err)
	}
	if entPending.Tier != billing.TierPower || entPending.PendingTier == nil || *entPending.PendingTier != billing.TierStandard {
		t.Fatalf("pending tier not preserved mid-cycle: %+v", entPending)
	}

	// Spend $20 during Nov period (leaves $5 trial + $5 unused Nov power; Nov power will expire).
	db := records.NewShared(backend, u, 10.0)
	db.Now = clock.Now
	db.EnforceBilling = true
	spent20 := 20.0
	if err := db.RecordUsage(ctx, state.Usage{
		ID: "nov-spend-20", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		CostUSD: &spent20, CostStatus: "provider", RequestStatus: "received",
		CreatedAt: clock.Now(),
	}); err != nil {
		t.Fatalf("record Nov spend: %v", err)
	}

	// Jump 4+ months ahead (multi-month inactivity: 2027-03-15, skipping Dec, Jan, Feb!).
	clock.Set(time.Date(2027, 3, 15, 8, 0, 0, 0, time.UTC))
	svc.EvictEntitlementCache(u.ID)

	// Trigger rollover via ReserveUsage admission.
	meter := usage.WithBillingAdmission(db, 0, 0, clock.Now)
	if err := meter.RecordUsage(ctx, state.Usage{
		ID: "mar-call-1", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.50, CostStatus: "unknown", RequestStatus: "pending",
		CreatedAt: clock.Now(),
	}); err != nil {
		t.Fatalf("March call after rollover failed: %v", err)
	}

	rolled, err := svc.Billing().GetEntitlement(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetEntitlement after rollover: %v", err)
	}
	wantStart := time.Date(2027, 3, 10, 12, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2027, 4, 10, 12, 0, 0, 0, time.UTC)
	if rolled.Tier != billing.TierStandard || rolled.PendingTier != nil {
		t.Fatalf("rolled tier/pending = (%s, %v), want (standard, nil)", rolled.Tier, rolled.PendingTier)
	}
	if !rolled.PeriodStart.Equal(wantStart) || rolled.PeriodEnd == nil || !rolled.PeriodEnd.Equal(wantEnd) {
		t.Fatalf("rolled period = [%v, %v), want [%v, %v)", rolled.PeriodStart, rolled.PeriodEnd, wantStart, wantEnd)
	}

	// Verify ONLY ONE rollover grant (manual:<uid>:2027-03 for $10.00) was inserted — NO Dec/Jan/Feb stacking!
	grants, err := backend.ListCreditGrants(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListCreditGrants: %v", err)
	}
	grantMap := map[string]billing.CreditGrant{}
	for _, g := range grants {
		grantMap[g.ID] = g
	}
	wantGrantID := billing.ManualPeriodGrantID(u.ID, wantStart) // manual:<uid>:2027-03
	gMar, ok := grantMap[wantGrantID]
	if !ok || gMar.AmountMicros != 10*billing.MicrosPerUSD {
		t.Fatalf("expected single %s grant of $10, got %+v (all=%+v)", wantGrantID, gMar, grants)
	}
	for _, skipped := range []string{"2026-12", "2027-01", "2027-02"} {
		badID := fmt.Sprintf("manual:%s:%s", u.ID, skipped)
		if _, stacked := grantMap[badID]; stacked {
			t.Fatalf("back-credit stacked forbidden grant %s!", badID)
		}
	}
	// Total grants must be 3: trial + Nov 2026 power + Mar 2027 standard.
	if len(grants) != 3 {
		t.Fatalf("grant count = %d, want 3 (trial + 2026-11 + 2027-03): %+v", len(grants), grants)
	}

	// Idempotency: re-reading entitlement in the same period must NOT create another grant.
	if _, err := svc.Billing().GetEntitlement(ctx, u.ID); err != nil {
		t.Fatalf("second GetEntitlement: %v", err)
	}
	grantsAgain, _ := backend.ListCreditGrants(ctx, u.ID)
	if len(grantsAgain) != 3 {
		t.Fatalf("idempotency violated: grant count = %d, want 3", len(grantsAgain))
	}

	// Auto-pause excess routines on downgrade (power 18 -> standard max 15: pauses newest 3: 16, 17, 18).
	paused, err := svc.EnforceRoutineCap(ctx, u.ID)
	if err != nil {
		t.Fatalf("EnforceRoutineCap: %v", err)
	}
	if paused != 3 {
		t.Fatalf("paused = %d, want 3", paused)
	}
	userApp, release, err = svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire #2: %v", err)
	}
	enabled, err := userApp.Scheduler.List(ctx, string(scheduler.AutomationEnabled))
	release()
	if err != nil {
		t.Fatalf("List enabled: %v", err)
	}
	nonSystemEnabled := map[string]bool{}
	systemEnabled := false
	for _, a := range enabled {
		if a.System {
			systemEnabled = true
			continue
		}
		nonSystemEnabled[a.Title] = true
	}
	if !systemEnabled {
		t.Fatal("system routine must NOT be paused by user routine cap")
	}
	if len(nonSystemEnabled) != 15 {
		t.Fatalf("enabled non-system routines = %d, want 15", len(nonSystemEnabled))
	}
	for _, dropped := range []string{"routine-16", "routine-17", "routine-18"} {
		if nonSystemEnabled[dropped] {
			t.Fatalf("newest excess routine %s should have been auto-paused", dropped)
		}
	}

	// Pending downgrade to BYOK ($0 hosted grant, MaxRoutines=10) at next period_end (2027-04-10).
	if _, err := svc.Billing().SetTier(ctx, u.ID, billing.TierBYOK, billing.SetTierOptions{Pending: true}); err != nil {
		t.Fatalf("SetTier pending byok: %v", err)
	}
	clock.Set(time.Date(2027, 4, 11, 0, 0, 0, 0, time.UTC))
	svc.EvictEntitlementCache(u.ID)
	entBYOK, err := svc.Billing().GetEntitlement(ctx, u.ID)
	if err != nil || entBYOK.Tier != billing.TierBYOK || entBYOK.PendingTier != nil {
		t.Fatalf("rolled to BYOK = %+v (err=%v)", entBYOK, err)
	}
	grantsBYOK, _ := backend.ListCreditGrants(ctx, u.ID)
	if len(grantsBYOK) != 3 {
		t.Fatalf("BYOK rollover must not insert a hosted credit grant, got %d grants", len(grantsBYOK))
	}
	pausedBYOK, err := svc.EnforceRoutineCap(ctx, u.ID)
	if err != nil || pausedBYOK != 5 {
		t.Fatalf("EnforceRoutineCap on BYOK downgrade = (%d, %v), want (5, nil)", pausedBYOK, err)
	}
}

// Scenario 4: Mid-task suspension + cross-instance LISTEN nori_wake 'entitlement:<uid>' eviction.
// Active user/task suspended mid-flight on Instance A -> LISTEN nori_wake delivers 'entitlement:<uid>'
// to peer Instance B -> evicts entitlement/app/DEK caches, disconnects sockets, sleeps Sprite,
// cancels active task & parks wake, blocks subsequent requests with 403 account_suspended,
// AND a subsequent Stripe ApplyEvent setting status='active' NEVER unsuspends the account!
func TestDST_BillingMidTaskSuspensionCrossInstanceNotifyEviction(t *testing.T) {
	ctx := context.Background()
	clock := env.NewClock(time.Date(2026, 11, 27, 16, 0, 0, 0, time.UTC))
	sharedBackend := memstore.New(memstore.WithClock(clock.Now))
	t.Cleanup(func() { _ = sharedBackend.Close() })

	credKeyHex := hex.EncodeToString(bytes.Repeat([]byte{0x5a}, 32))
	baseCfg := settings.Settings{
		Cloud:                 "local",
		EnvProfile:            "test",
		TimeZone:              "UTC",
		TaskBudgetUSD:         5.0,
		TrialMaxSignupsPerDay: 25,
		Features:              settings.Features{DisableJev: true},
		Connections: settings.Connections{
			CredentialStore: "database",
			CredentialKey:   credKeyHex,
		},
		Models: settings.Models{BYOKRequired: true},
	}

	// Peer Instance A (operator / API replica emitting Suspend).
	storageA := platform.NewStorage(sharedBackend, func(users.User) state.ArtifactStore { return memstore.NewArtifacts() })
	storageA.Now = clock.Now
	instanceA := app.OpenServiceWithStorage(storageA, nil, baseCfg)
	instanceA.Now = clock.Now
	t.Cleanup(instanceA.Close)

	// Peer Instance B (serving active userApp, hot DEK cache, active worker.Pool task, awake Sprite, Desktop WS, HTTP API).
	rawArtifactsB := memstore.NewArtifacts()
	storageB := platform.NewStorage(sharedBackend, func(users.User) state.ArtifactStore { return rawArtifactsB })
	storageB.Now = clock.Now
	instanceB := app.OpenServiceWithStorage(storageB, nil, baseCfg)
	instanceB.Now = clock.Now
	t.Cleanup(instanceB.Close)

	// Attach Sprite computer manager to Instance B (wires desktop disconnect & force-sleep hooks).
	fakeProvider := fake.New()
	fakeProvider.Now = clock.Now
	compStore := computer.NewMemoryStore()
	compMgrB := computer.NewManager(fakeProvider, compStore, computer.Config{RatePerHourUSD: 0.15})
	compMgrB.SetClock(clock.Now)
	instanceB.SetComputerManager(compMgrB)

	alice := users.User{
		ID:          "44444444-4444-4444-8444-444444444401",
		Email:       "midtask-suspend@example.com",
		DisplayName: "Alice Active",
		TimeZone:    "UTC",
	}
	if err := instanceA.Users.Create(ctx, alice); err != nil {
		t.Fatalf("Create alice: %v", err)
	}
	if _, err := instanceA.Billing().SetTier(ctx, alice.ID, billing.TierStandard, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier standard: %v", err)
	}

	// Prime Instance B caches:
	// 1) Entitlement cache (15s TTL)
	entB, err := instanceB.GetEntitlement(ctx, alice.ID)
	if err != nil || entB.IsSuspended() {
		t.Fatalf("prime Instance B entitlement: %+v err=%v", entB, err)
	}
	// 2) userApp cache + encrypted artifact DEK (tenantkey cache) + credential rowstore DEK cache.
	appB, releaseB, err := instanceB.Acquire(ctx, alice)
	if err != nil {
		t.Fatalf("Instance B Acquire: %v", err)
	}
	secretFileKey := state.ResultKey(uuid.NewString())
	if err := appB.Store.PutFile(ctx, secretFileKey, "text/plain", []byte("top-secret-payload")); err != nil {
		t.Fatalf("PutFile (prime tenantkey DEK cache): %v", err)
	}
	if appB.Assistant != nil && appB.Assistant.ServiceKeys != nil && appB.Assistant.ServiceKeys.Store != nil {
		if _, err := appB.Assistant.ServiceKeys.Store.Replace(ctx, "conn-test", 0, []byte(`{"token":"tok-123"}`)); err != nil {
			t.Fatalf("rowstore Replace (prime rowstore DEK cache): %v", err)
		}
		if _, err := appB.Assistant.ServiceKeys.Store.Get(ctx, "conn-test"); err != nil {
			t.Fatalf("rowstore Get (populate rowstore DEK cache): %v", err)
		}
	}
	// Mark the stored tenant DEK row shredded while the unwrapped DEK is still cached in Instance B's 10m TTL cache.
	// Pre-eviction GetFile succeeds from the hot DEK cache; post-eviction GetFile MUST fail with ErrKeyNotFound!
	existingDEK, err := appB.Store.Resource(ctx, tenantkey.Collection, tenantkey.PrimaryKeyID)
	if err != nil {
		t.Fatalf("read tenant_keys/primary: %v", err)
	}
	shredRes, err := state.NewResource(tenantkey.Collection, tenantkey.PrimaryKeyID, existingDEK.Revision+1, map[string]any{
		"owner":    alice.ID,
		"shredded": true,
	}, nil, clock.Now())
	if err != nil {
		t.Fatalf("NewResource shredded DEK: %v", err)
	}
	if _, err := appB.Store.PutResource(ctx, shredRes, existingDEK.Revision); err != nil {
		t.Fatalf("PutResource shredded DEK: %v", err)
	}
	if gotPre, err := appB.Store.GetFile(ctx, secretFileKey); err != nil || string(gotPre) != "top-secret-payload" {
		t.Fatalf("expected hot tenantkey DEK cache hit before notify eviction, got (%q, %v)", string(gotPre), err)
	}
	storeBRef := appB.Store
	releaseB()
	if instanceB.CachedCount() != 1 {
		t.Fatalf("Instance B CachedCount = %d, want 1", instanceB.CachedCount())
	}

	// 3) Wake Alice's Sprite & register an active Desktop VNC WebSocket session on Instance B.
	if _, err := compMgrB.Ensure(ctx, alice.ID); err != nil {
		t.Fatalf("compMgrB.Ensure: %v", err)
	}
	var desktopDisconnected atomic.Int32
	compMgrB.RegisterDesktopSession(alice.ID, func() { desktopDisconnected.Add(1) })

	// 4) Start Instance B worker.Pool with an active in-flight task/conversation for Alice.
	if err := sharedBackend.WakeUser(ctx, alice.ID, docstore.LaneConversation, time.Time{}); err != nil {
		t.Fatalf("WakeUser: %v", err)
	}
	workStarted := make(chan struct{}, 1)
	workCanceled := make(chan struct{}, 1)
	evictDone := make(chan string, 2)
	instanceB.SetOnEntitlementChanged(func(_ context.Context, uid string) {
		select {
		case evictDone <- uid:
		default:
		}
	})

	poolB, err := worker.New(instanceB, worker.Options{
		Owner:          "peer-instance-b",
		Now:            clock.Now,
		PollInterval:   5 * time.Millisecond,
		RepairInterval: time.Hour,
		RunConversation: func(runCtx context.Context, _ *app.App) error {
			select {
			case workStarted <- struct{}{}:
			default:
			}
			<-runCtx.Done()
			select {
			case workCanceled <- struct{}{}:
			default:
			}
			return runCtx.Err()
		},
	})
	if err != nil {
		t.Fatalf("worker.New(instanceB): %v", err)
	}
	poolCtx, poolCancel := context.WithCancel(ctx)
	poolDone := make(chan error, 1)
	go func() { poolDone <- poolB.Run(poolCtx) }()
	t.Cleanup(func() {
		poolCancel()
		<-poolDone
	})

	select {
	case <-workStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Alice's mid-flight task on Instance B to start")
	}

	// Build Instance B's HTTP handler using instanceB.CheckNotSuspended (same as production API server).
	handlerB := api.UserHandlerWithOptions(instanceB.Users, func(_ context.Context, _ users.User) (api.UserRoutes, func(), error) {
		return api.UserRoutes{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
			}),
		}, func() {}, nil
	}, false, "sim-model", api.UserHandlerOptions{
		DisableRateLimit: true,
		CheckEntitlement: instanceB.CheckNotSuspended,
	})

	// Sanity check: before suspension, Instance B serves 200 OK.
	preReq := httptest.NewRequest(http.MethodGet, "/users/"+alice.ID+"/v1/capabilities", nil)
	preRec := httptest.NewRecorder()
	handlerB.ServeHTTP(preRec, preReq)
	if preRec.Code != http.StatusOK {
		t.Fatalf("pre-suspend GET capabilities = %d %s", preRec.Code, preRec.Body.String())
	}

	// --- TRIGGER MID-TASK SUSPENSION FROM PEER INSTANCE A ---
	// Shred the stored tenant DEK row directly right before suspend to verify Instance B's DEK cache
	// is truly evicted on notify (if still cached, Open would wrongly return plaintext!).
	if _, err := instanceA.Billing().Suspend(ctx, alice.ID, "fraud_chargeback"); err != nil {
		t.Fatalf("instanceA Suspend: %v", err)
	}

	// Wait for Instance B to receive 'entitlement:<uid>' via LISTEN nori_wake and cancel active work.
	select {
	case <-workCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Instance B to cancel Alice's mid-flight work on entitlement notify")
	}
	select {
	case uid := <-evictDone:
		if uid != alice.ID {
			t.Fatalf("OnEntitlementChanged uid = %q, want %q", uid, alice.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Instance B HandleEntitlementChanged completion")
	}

	// 1. Entitlement cache, userApp cache, and DEK cache on Instance B immediately evicted (without waiting TTL!).
	if !instanceB.IsUserSuspended(ctx, alice.ID) {
		t.Fatal("Instance B did not immediately observe suspension after notify eviction")
	}
	if instanceB.CachedCount() != 0 {
		t.Fatalf("Instance B CachedCount after notify = %d, want 0", instanceB.CachedCount())
	}
	if _, err := storeBRef.GetFile(ctx, secretFileKey); !errors.Is(err, tenantkey.ErrKeyNotFound) {
		t.Fatalf("expected DEK cache evicted (ErrKeyNotFound), got err=%v", err)
	}

	// 2. Desktop VNC socket disconnected & Sprite put to sleep on Instance B.
	if desktopDisconnected.Load() != 1 {
		t.Fatalf("desktopDisconnected = %d, want 1", desktopDisconnected.Load())
	}
	compRec, err := compStore.GetComputer(ctx, alice.ID)
	if err != nil || compRec.Status != computer.StatusSleeping {
		t.Fatalf("Sprite status after suspension notify = (%+v, %v), want sleeping", compRec, err)
	}

	// 3. Active wake parked so worker.Pool never re-dispatches suspended user.
	deadline := time.Now().Add(2 * time.Second)
	for {
		wakes, err := sharedBackend.ListUserWakes(ctx, alice.ID)
		if err != nil {
			t.Fatalf("ListUserWakes: %v", err)
		}
		if len(wakes) == 1 && wakes[0].Owner == "" && !wakes[0].Runnable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected suspended user wake parked, got %+v", wakes)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 4. Subsequent HTTP requests on Instance B immediately return 403 account_suspended.
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/api/users/resolve", `{"email":"midtask-suspend@example.com"}`},
		{http.MethodGet, "/users/" + alice.ID + "/v1/capabilities", ""},
		{http.MethodPost, "/users/" + alice.ID + "/v1/conversations/assistant/messages", `{"id":"m1","text":"hi"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		handlerB.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), billing.CodeAccountSuspended) {
			t.Fatalf("post-suspend %s %s = %d %s, want 403 account_suspended", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}

	// 5. Usage admission blocks subsequent steps with ErrAccountSuspended.
	dbB := records.NewShared(sharedBackend, alice, 10.0)
	dbB.Now = clock.Now
	dbB.EnforceBilling = true
	meterB := usage.WithBillingAdmission(dbB, 0, 0, clock.Now)
	if err := meterB.RecordUsage(ctx, state.Usage{
		ID: "post-suspend-step", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: clock.Now(),
	}); !errors.Is(err, billing.ErrAccountSuspended) {
		t.Fatalf("post-suspend ReserveUsage = %v, want ErrAccountSuspended", err)
	}

	// 6. CRITICAL INVARIANT: Stripe ApplyEvent setting status='active' NEVER unsuspends the account!
	pEnd := clock.Now().AddDate(0, 1, 0)
	afterStripe, err := instanceA.Billing().ApplyEvent(ctx, billing.Change{
		Provider:          billing.ProviderStripe,
		Environment:       billing.EnvProduction,
		EventID:           "evt_stripe_paid_after_ban",
		EventType:         "invoice.paid",
		ProviderUpdatedAt: clock.Now().Add(time.Minute),
		UserID:            alice.ID,
		Subscription: &billing.SubscriptionSnapshot{
			ExternalSubscriptionID: "sub_alice_power",
			Tier:                   billing.TierPower,
			Status:                 billing.StatusActive,
			PeriodStart:            clock.Now(),
			PeriodEnd:              &pEnd,
		},
		CreditDeltaMicros: 25 * billing.MicrosPerUSD,
		Payload:           json.RawMessage(`{"invoice":"paid"}`),
	})
	if err != nil {
		t.Fatalf("Stripe ApplyEvent: %v", err)
	}
	if afterStripe.Tier != billing.TierPower || afterStripe.Status != billing.StatusActive {
		t.Fatalf("Stripe ApplyEvent did not update billing fields: %+v", afterStripe)
	}
	if !afterStripe.IsSuspended() || afterStripe.SuspendedReason != "fraud_chargeback" {
		t.Fatalf("SECURITY VIOLATION: Stripe status='active' cleared suspension! %+v", afterStripe)
	}

	// Wait for Instance B to handle the second entitlement notify, then verify STILL 403 account_suspended!
	select {
	case <-evictDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second entitlement notify after Stripe ApplyEvent")
	}
	stillRec := httptest.NewRecorder()
	handlerB.ServeHTTP(stillRec, httptest.NewRequest(http.MethodGet, "/users/"+alice.ID+"/v1/capabilities", nil))
	if stillRec.Code != http.StatusForbidden || !strings.Contains(stillRec.Body.String(), billing.CodeAccountSuspended) {
		t.Fatalf("after Stripe active event, GET capabilities = %d %s, want 403 account_suspended", stillRec.Code, stillRec.Body.String())
	}
	if err := meterB.RecordUsage(ctx, state.Usage{
		ID: "post-stripe-still-suspended", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 0.01, RequestStatus: "pending", CreatedAt: clock.Now(),
	}); !errors.Is(err, billing.ErrAccountSuspended) {
		t.Fatalf("after Stripe active event, ReserveUsage = %v, want ErrAccountSuspended", err)
	}
}

// Scenario 5: BYOK bypass.
// standard/power user using BYOK LLM and BYOK Exa draws $0 from hosted credit balance and does
// not trip platform hosted llmguard spend cap, while still respecting the 24h BYOK safety rail.
func TestDST_BillingBYOKBypassAndSafetyRail(t *testing.T) {
	ctx := context.Background()
	clock := env.NewClock(time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	backend := memstore.New(memstore.WithClock(clock.Now))
	t.Cleanup(func() { _ = backend.Close() })

	u := users.User{
		ID:          "55555555-5555-4555-8555-555555555501",
		Email:       "byok-bypass@example.com",
		DisplayName: "BYOK User",
		TimeZone:    "UTC",
	}
	dir := records.NewSharedUsers(backend)
	dir.Now = clock.Now
	if err := dir.Create(ctx, u); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	// Promote to standard: $10.00 period grant + $5.00 lifetime trial = $15.00 hosted balance; $15.00 24h BYOK rail.
	if _, err := backend.SetTier(ctx, u.ID, billing.TierStandard, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier standard: %v", err)
	}
	initialBal, err := backend.CreditBalance(ctx, u.ID)
	if err != nil || initialBal.AvailableMicros <= 0 {
		t.Fatalf("initial balance = %+v (err=%v)", initialBal, err)
	}

	db := records.NewShared(backend, u, 10.0)
	db.Now = clock.Now
	db.EnforceBilling = true
	db.GlobalBudget = billing.GlobalBudgetConfig{
		HostedAllMaxMicrosPerDay: 50 * billing.MicrosPerUSD,
		HostedExaMaxMicrosPerDay: 15 * billing.MicrosPerUSD,
	}
	meter := usage.WithBillingAdmission(db, 0, 0, clock.Now)

	// Tight platform hosted llmguard spend cap ($0.25 / 24h) — BYOK calls MUST NOT trip it!
	guardCfg := llmguard.DefaultConfig()
	guardCfg.Now = clock.Now
	guardCfg.MaxEstimatedUSD = 0.25
	guardCfg.SpendWindow = 24 * time.Hour
	guard := llmguard.New(guardCfg)

	// 1. Run 14 BYOK LLM calls of $1.00 each ($14.00 total — 56x higher than the $0.25 hosted llmguard cap!).
	byokClient := guard.WrapClient(&http.Client{
		Transport: simRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		}),
	}, 10*time.Second, llmguard.CallMeta{
		UserID:   u.ID,
		Scope:    llmguard.ScopeOuter,
		Provider: "openai",
		Model:    "gpt-6.2-sol",
		Billing:  state.UsageBillingBYOK,
		BYOK:     true,
	})

	for i := 1; i <= 14; i++ {
		callID := fmt.Sprintf("byok-llm-%02d", i)
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: callID, Provider: "openai", Model: "gpt-6.2-sol",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
			EstimatedExposureUSD: 1.00, CostStatus: "unknown", RequestStatus: "pending",
			CreatedAt: clock.Now(),
		}); err != nil {
			t.Fatalf("BYOK LLM reserve #%d failed: %v", i, err)
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", strings.NewReader(fmt.Sprintf(`{"input":"msg-%d"}`, i)))
		req.Header.Set("Authorization", "Bearer sk-user-byok-key")
		res, err := byokClient.Do(req)
		if err != nil {
			t.Fatalf("BYOK LLM llmguard #%d unexpectedly failed: %v", i, err)
		}
		llmguard.RecordResponseCost(res, 1.00)
		_ = res.Body.Close()

		actual := 1.00
		if err := meter.RecordUsage(ctx, state.Usage{
			ID: callID, Provider: "openai", Model: "gpt-6.2-sol",
			Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
			CostUSD: &actual, CostStatus: "calculated", RequestStatus: "received",
			CreatedAt: clock.Now(),
		}); err != nil {
			t.Fatalf("BYOK LLM finalize #%d failed: %v", i, err)
		}
		clock.Advance(time.Minute)
	}

	// 2. Run 5 BYOK Exa searches via agent.Service.CallRuntimeTool("web_search").
	var exaKeysSeen []string
	agentSvc := agent.New(state.Compose(db, memstore.NewArtifacts()), nil)
	agentSvc.Now = clock.Now
	agentSvc.Capabilities.ExaSearchKey = "platform-exa-should-not-be-used"
	agentSvc.HTTP = &http.Client{
		Transport: simRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			exaKeysSeen = append(exaKeysSeen, r.Header.Get("x-api-key"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"results":[{"title":"BYOK Hit","url":"https://example.com","summary":"ok"}],"costDollars":{"total":0.013}}`)),
			}, nil
		}),
	}
	agent.ConfigureSearch(agentSvc, agent.SearchOptions{
		Meter:  meter,
		Guard:  guard,
		UserID: u.ID,
		AllowBYOK: func(ctx context.Context) (bool, error) {
			ent, err := backend.GetEntitlement(ctx, u.ID)
			if err != nil {
				return false, err
			}
			return ent.EffectivePolicy().AllowBYOK, nil
		},
		ResolveBYOKKey: func(context.Context) (string, bool, error) {
			return "exa-user-byok-key", true, nil
		},
	})
	searchCtx := usage.WithSession(ctx, usage.Session{Now: clock.Now})
	for i := 1; i <= 5; i++ {
		res, err := agentSvc.CallRuntimeTool(searchCtx, "run-byok", "web_search", map[string]any{
			"query": fmt.Sprintf("byok search query %d", i),
		})
		if err != nil {
			t.Fatalf("BYOK Exa search #%d failed: %v", i, err)
		}
		if res["cost_usd"] != 0.013 {
			t.Fatalf("BYOK Exa search #%d cost_usd = %v, want 0.013", i, res["cost_usd"])
		}
		clock.Advance(time.Minute)
	}
	if len(exaKeysSeen) != 5 {
		t.Fatalf("expected 5 Exa calls, got %d", len(exaKeysSeen))
	}
	for i, k := range exaKeysSeen {
		if k != "exa-user-byok-key" {
			t.Fatalf("Exa call #%d used key %q, want user BYOK key", i+1, k)
		}
	}

	// 3. Hosted credit balance & fleet global budgets must be 100% untouched ($0 drawn)!
	balAfterBYOK, err := backend.CreditBalance(ctx, u.ID)
	if err != nil {
		t.Fatalf("CreditBalance after BYOK: %v", err)
	}
	if balAfterBYOK.AvailableMicros != initialBal.AvailableMicros || balAfterBYOK.SpentMicros != 0 {
		t.Fatalf("BYOK drew from hosted credit! before=%+v after=%+v", initialBal, balAfterBYOK)
	}
	for _, scope := range []billing.BudgetScope{billing.ScopeHostedAll, billing.ScopeHostedLLM, billing.ScopeHostedExa} {
		gb, _ := backend.GetGlobalBudget(ctx, clock.Now(), scope)
		if gb.SpentMicros != 0 {
			t.Fatalf("BYOK incremented hosted global budget %s: %+v", scope, gb)
		}
	}

	// 4. Platform hosted llmguard spend cap ($0.25) was NOT tripped by $14.065 of BYOK traffic!
	snap := guard.Stats()
	if snap.RecordedActualUSD != 0 || snap.EstimatedUnknownUSD != 0 {
		t.Fatalf("BYOK polluted hosted llmguard spend: %+v", snap)
	}
	hostedClient := guard.WrapClient(&http.Client{
		Transport: simRoundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
			}, nil
		}),
	}, 10*time.Second, llmguard.CallMeta{
		UserID:   u.ID,
		Scope:    llmguard.ScopeOuter,
		Provider: "gateway",
		Model:    "google/gemini-4-neon",
		Billing:  state.UsageBillingHosted,
		BYOK:     false,
	})
	hReq1, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://ai-gateway.vercel.sh/v1/chat/completions", strings.NewReader(`{"input":"hosted-1"}`))
	hRes1, err := hostedClient.Do(hReq1)
	if err != nil {
		t.Fatalf("first hosted call should pass untripped llmguard: %v", err)
	}
	llmguard.RecordResponseCost(hRes1, 0.25)
	_ = hRes1.Body.Close()
	// Now hosted spend reached $0.25 -> next hosted call MUST trip ErrBudgetExceeded, proving the cap is active.
	hReq2, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://ai-gateway.vercel.sh/v1/chat/completions", strings.NewReader(`{"input":"hosted-2"}`))
	if _, err := hostedClient.Do(hReq2); !errors.Is(err, llmguard.ErrBudgetExceeded) {
		t.Fatalf("expected hosted llmguard ErrBudgetExceeded after $0.25 hosted spend, got %v", err)
	}

	// 5. Rolling 24h BYOK safety rail ($15.00 on standard): $14.065 spent so far -> $1.00 call rejected!
	if err := meter.RecordUsage(ctx, state.Usage{
		ID: "byok-over-rail", Provider: "openai", Model: "gpt-6.2-sol",
		Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 1.00, CostStatus: "unknown", RequestStatus: "pending",
		CreatedAt: clock.Now(),
	}); !errors.Is(err, usage.ErrUserQuotaExceeded) {
		t.Fatalf("expected ErrUserQuotaExceeded at 24h BYOK safety rail ($15), got %v", err)
	}

	// 6. Verify cost report attributes BYOK and search accurately.
	report, err := usage.ReadCostReport(ctx, db.ReadUsageCosts, 1, time.UTC, clock.Now().Add(time.Second))
	if err != nil {
		t.Fatalf("ReadCostReport: %v", err)
	}
	if report.ByBilling.Hosted.PricedCalls != 0 || report.ByBilling.BYOK.PricedCalls != 19 || report.ByCategory.Search.PricedCalls != 5 {
		t.Fatalf("unexpected cost report attribution: by_billing=%+v by_category=%+v", report.ByBilling, report.ByCategory)
	}
	if math.Abs(report.ByBilling.BYOK.CostUSD-14.065) > 1e-6 {
		t.Fatalf("BYOK cost_usd = %v, want 14.065", report.ByBilling.BYOK.CostUSD)
	}

	// 7. Advance virtual clock past 24h -> prior BYOK spend rolls off, new BYOK call admitted!
	clock.Advance(24*time.Hour + time.Minute)
	if err := meter.RecordUsage(ctx, state.Usage{
		ID: "byok-after-24h-rolloff", Provider: "openai", Model: "gpt-6.2-sol",
		Billing: state.UsageBillingBYOK, Category: state.UsageCategoryLLM,
		EstimatedExposureUSD: 1.00, CostStatus: "unknown", RequestStatus: "pending",
		CreatedAt: clock.Now(),
	}); err != nil {
		t.Fatalf("BYOK call after 24h rolling window expiry should succeed: %v", err)
	}
}

// Scenario 6: Sprite 15m idle auto-sleep + exhausted/suspended reaper + double-bill prevention.
// Exercises concurrent janitor + explicit Sleep single-tx zero double-bill, 15m idle auto-sleep
// with activity touch, credit-exhausted & suspended reapers closing Desktop WS, and Fly
// already-sleeping provider status reconciliation preventing phantom billing.
func TestDST_SpritesIdleAutoSleepReaperAndZeroDoubleBill(t *testing.T) {
	ctx := context.Background()
	clock := env.NewClock(time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC))

	fp := fake.New()
	fp.Now = clock.Now
	compStore := computer.NewMemoryStore()
	bs := billing.NewMemoryStore(clock.Now)
	bs.UsageReader = compStore.UsageSamplesForBilling
	bsvc := billing.NewService(bs, false, billing.GlobalBudgetConfig{})
	bsvc.Now = clock.Now

	mgr := computer.NewManager(fp, compStore, computer.Config{
		RatePerHourUSD: 0.15, // $0.0025/min = 2500 micros/min
		IdleAutoSleep:  15 * time.Minute,
	})
	mgr.SetClock(clock.Now)
	mgr.SetBilling(bsvc)

	const (
		uIdle    = "66666666-6666-4666-8666-666666666601"
		uRace    = "66666666-6666-4666-8666-666666666602"
		uExhaust = "66666666-6666-4666-8666-666666666603"
		uSusp    = "66666666-6666-4666-8666-666666666604"
		uFlyStop = "66666666-6666-4666-8666-666666666605"
	)
	for _, uid := range []string{uIdle, uRace, uSusp, uFlyStop} {
		if _, err := bs.SetTier(ctx, uid, billing.TierStandard, billing.SetTierOptions{}); err != nil {
			t.Fatalf("SetTier %s: %v", uid, err)
		}
	}
	oneCent := int64(10_000) // $0.01 -> exhausted after >4m awake @ $0.15/hr
	if _, err := bs.SetTier(ctx, uExhaust, billing.TierStandard, billing.SetTierOptions{CreditOverrideMicros: &oneCent}); err != nil {
		t.Fatalf("SetTier uExhaust: %v", err)
	}

	sumBilledMicros := func(uid string) int64 {
		var total int64
		for _, r := range compStore.MeteredUsage(uid) {
			if r.CostUSD != nil {
				total += billing.USDToMicros(*r.CostUSD)
			}
		}
		return total
	}

	// --- Part A: Concurrent janitor + explicit Sleep race (zero double-bill) ---
	if _, err := mgr.Ensure(ctx, uRace); err != nil {
		t.Fatalf("Ensure uRace: %v", err)
	}
	clock.Advance(12 * time.Minute) // 12m @ $0.15/hr = $0.03 = 30_000 micros
	var wg sync.WaitGroup
	startCh := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-startCh
			_ = mgr.RunJanitorPass(ctx)
		}()
		go func() {
			defer wg.Done()
			<-startCh
			_ = mgr.Sleep(ctx, uRace)
		}()
	}
	close(startCh)
	wg.Wait()

	if got := sumBilledMicros(uRace); got != 30_000 {
		t.Fatalf("uRace billed micros = %d, want EXACTLY 30_000 (12m, zero double-bill)", got)
	}
	gbAll, _ := bs.GetGlobalBudget(ctx, clock.Now(), billing.ScopeHostedAll)
	gbSpr, _ := bs.GetGlobalBudget(ctx, clock.Now(), billing.ScopeHostedSprites)
	if gbAll.SpentMicros != 30_000 || gbSpr.SpentMicros != 30_000 {
		t.Fatalf("uRace global budget mismatch: all=%d sprites=%d, want 30_000", gbAll.SpentMicros, gbSpr.SpentMicros)
	}
	// Sleep duration (2 hours) must bill $0.
	clock.Advance(2 * time.Hour)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("janitor while uRace asleep: %v", err)
	}
	if got := sumBilledMicros(uRace); got != 30_000 {
		t.Fatalf("uRace billed during sleep: %d, want 30_000", got)
	}

	// --- Part B: 15m idle auto-sleep + activity touch ---
	if _, err := mgr.Ensure(ctx, uIdle); err != nil {
		t.Fatalf("Ensure uIdle: %v", err)
	}
	clock.Advance(10 * time.Minute)
	if _, err := mgr.Exec(ctx, uIdle, computer.ExecRequest{Command: "echo keepalive"}); err != nil {
		t.Fatalf("Exec uIdle: %v", err)
	}
	// 10m since last activity (20m total awake) -> stays awake, bills 20m = 50_000 micros.
	clock.Advance(10 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("janitor uIdle 10m idle: %v", err)
	}
	recIdle, _ := compStore.GetComputer(ctx, uIdle)
	if recIdle.Status != computer.StatusReady {
		t.Fatalf("uIdle at 10m idle = %q, want ready", recIdle.Status)
	}
	// +5m -> 15m since last activity -> auto-sleeps, total billed = 25m = 62_500 micros.
	clock.Advance(5 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("janitor uIdle 15m idle: %v", err)
	}
	recIdle, _ = compStore.GetComputer(ctx, uIdle)
	if recIdle.Status != computer.StatusSleeping || recIdle.AwakeSince != nil || recIdle.SleepingSince == nil {
		t.Fatalf("uIdle after 15m idle = %+v, want sleeping", recIdle)
	}
	if got := sumBilledMicros(uIdle); got != 62_500 {
		t.Fatalf("uIdle billed micros = %d, want 62_500 (25m)", got)
	}

	// --- Part C: Credit-exhausted & suspended reaper ---
	if _, err := mgr.Ensure(ctx, uExhaust); err != nil {
		t.Fatalf("Ensure uExhaust: %v", err)
	}
	if _, err := mgr.Ensure(ctx, uSusp); err != nil {
		t.Fatalf("Ensure uSusp: %v", err)
	}
	var suspWSClosed atomic.Int32
	mgr.RegisterDesktopSession(uSusp, func() { suspWSClosed.Add(1) })
	if _, err := bsvc.Suspend(ctx, uSusp, "abuse"); err != nil {
		t.Fatalf("Suspend uSusp: %v", err)
	}
	// Advance 6m: uExhaust bills $0.015 > $0.01 credit -> exhausted; uSusp is suspended.
	clock.Advance(6 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("janitor reaper pass: %v", err)
	}
	if suspWSClosed.Load() != 1 {
		t.Fatalf("suspended Desktop WS closed = %d, want 1", suspWSClosed.Load())
	}
	for _, uid := range []string{uExhaust, uSusp} {
		rec, _ := compStore.GetComputer(ctx, uid)
		if rec.Status != computer.StatusSleeping {
			t.Fatalf("reaped user %s status = %q, want sleeping", uid, rec.Status)
		}
	}
	if _, err := mgr.Ensure(ctx, uExhaust); !errors.Is(err, billing.ErrCreditExhausted) {
		t.Fatalf("Ensure exhausted user = %v, want ErrCreditExhausted", err)
	}
	if _, err := mgr.Ensure(ctx, uSusp); !errors.Is(err, billing.ErrAccountSuspended) {
		t.Fatalf("Ensure suspended user = %v, want ErrAccountSuspended", err)
	}

	// --- Part D: Fly already-sleeping status reconciliation (zero phantom billing) ---
	recFly, err := mgr.Ensure(ctx, uFlyStop)
	if err != nil {
		t.Fatalf("Ensure uFlyStop: %v", err)
	}
	clock.Advance(4 * time.Minute)
	if _, err := mgr.Exec(ctx, uFlyStop, computer.ExecRequest{Command: "echo work"}); err != nil {
		t.Fatalf("Exec uFlyStop: %v", err)
	}
	// Provider stops VM externally right after t+4m.
	fp.SetStatus(recFly.ProviderID, computer.StatusSleeping)

	// Janitor runs 20 minutes later -> must reconcile and bill ONLY 4m (10_000 micros), NOT 24m!
	clock.Advance(20 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("janitor Fly reconcile pass: %v", err)
	}
	afterFly, _ := compStore.GetComputer(ctx, uFlyStop)
	if afterFly.Status != computer.StatusSleeping || afterFly.AwakeSince != nil {
		t.Fatalf("uFlyStop after reconcile = %+v, want sleeping", afterFly)
	}
	if got := sumBilledMicros(uFlyStop); got != 10_000 {
		t.Fatalf("uFlyStop reconciled micros = %d, want 10_000 (4m only, zero phantom billing)", got)
	}
	// Further janitor passes bill nothing.
	clock.Advance(45 * time.Minute)
	if err := mgr.RunJanitorPass(ctx); err != nil {
		t.Fatalf("janitor post-reconcile: %v", err)
	}
	if got := sumBilledMicros(uFlyStop); got != 10_000 {
		t.Fatalf("uFlyStop billed after reconcile = %d, want 10_000", got)
	}
}
