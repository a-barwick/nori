package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"nori/internal/billing"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
)

func jevOKResponse() *http.Response {
	body := `{"answers":{
		"converse":{"choice":"present","confidence":0.95},
		"ask":{"choice":"absent","confidence":0.9},
		"request_work":{"choice":"absent","confidence":0.9},
		"steer":{"choice":"absent","confidence":0.9},
		"provide_input":{"choice":"absent","confidence":0.9},
		"decide":{"choice":"absent","confidence":0.9},
		"inspect":{"choice":"absent","confidence":0.9},
		"control":{"choice":"absent","confidence":0.9},
		"update_memory":{"choice":"absent","confidence":0.9}
	}}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestJevClassifierSystemBillingBYOKAndTrial(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }
	backend := memstore.New(memstore.WithClock(nowFn))
	t.Cleanup(func() { _ = backend.Close() })

	dir := records.NewSharedUsers(backend)
	dir.Now = nowFn

	byokUser := users.User{
		ID:          "11111111-1111-4111-8111-111111111101",
		Email:       "byok-jev@example.com",
		DisplayName: "BYOK User",
		TimeZone:    "UTC",
	}
	trialUser := users.User{
		ID:          "11111111-1111-4111-8111-111111111102",
		Email:       "trial-jev@example.com",
		DisplayName: "Trial User",
		TimeZone:    "UTC",
	}
	for _, u := range []users.User{byokUser, trialUser} {
		if err := dir.Create(ctx, u); err != nil {
			t.Fatalf("Create %s: %v", u.Email, err)
		}
	}
	// Move byokUser to BYOK tier with $0 hosted grant (SkipGrant + zero trial grant doesn't apply; override to 0).
	// Specifically create a clean BYOK user with 0 hosted credits by consuming or setting a fresh BYOK user.
	byokZeroUser := users.User{
		ID:          "11111111-1111-4111-8111-111111111103",
		Email:       "byok-zero@example.com",
		DisplayName: "BYOK Zero",
		TimeZone:    "UTC",
	}
	if _, err := backend.SetTier(ctx, byokZeroUser.ID, billing.TierBYOK, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier byokZeroUser: %v", err)
	}
	if bal, err := backend.CreditBalance(ctx, byokZeroUser.ID); err != nil || bal.AvailableMicros != 0 {
		t.Fatalf("byokZeroUser initial balance = (%+v, %v), want 0", bal, err)
	}

	newJev := func(uid string) *JevClassifier {
		c := NewJevClassifier("jev-test-key")
		c.UserID = uid
		c.Now = nowFn
		c.Client = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return jevOKResponse(), nil
		})}
		return c
	}

	runClassify := func(t *testing.T, u users.User) Classification {
		t.Helper()
		db := records.NewShared(backend, u, 5.0)
		db.Now = nowFn
		db.EnforceBilling = true
		meter := usage.WithBillingAdmission(db, 0, 0, nowFn)
		c := newJev(u.ID)
		res, err := c.Classify(ctx, ClassifyRequest{
			Inputs:      []state.ConversationInput{{ID: "in-1", Text: "hello"}},
			RecordUsage: meter.RecordUsage,
		})
		if err != nil {
			t.Fatalf("Classify for %s failed: %v", u.Email, err)
		}
		if err := usage.Finish(ctx, meter, res.Usage); err != nil {
			t.Fatalf("Finish usage for %s failed: %v", u.Email, err)
		}
		return res
	}

	// 1. BYOK user with $0 hosted credit must NOT fail with ErrCreditExhausted and must tag system/system.
	resBYOK := runClassify(t, byokZeroUser)
	if resBYOK.Usage.Billing != state.UsageBillingSystem || resBYOK.Usage.Category != state.UsageCategorySystem {
		t.Fatalf("Jev usage meta = (%q, %q), want (system, system)", resBYOK.Usage.Billing, resBYOK.Usage.Category)
	}
	if bal, err := backend.CreditBalance(ctx, byokZeroUser.ID); err != nil || bal.AvailableMicros != 0 || bal.SpentMicros != 0 {
		t.Fatalf("BYOK balance after Jev = (%+v, %v), want 0 available / 0 spent", bal, err)
	}

	// 2. Trial user ($5.00 hosted credit) must NOT drain $0.02/turn from Jev!
	beforeTrial, err := backend.CreditBalance(ctx, trialUser.ID)
	if err != nil {
		t.Fatalf("trial balance before: %v", err)
	}
	for range 5 {
		_ = runClassify(t, trialUser)
	}
	afterTrial, err := backend.CreditBalance(ctx, trialUser.ID)
	if err != nil {
		t.Fatalf("trial balance after: %v", err)
	}
	if afterTrial.AvailableMicros != beforeTrial.AvailableMicros || afterTrial.SpentMicros != 0 {
		t.Fatalf("trial balance drained by Jev! before=%+v after=%+v", beforeTrial, afterTrial)
	}

	// 3. Suspended user is still rejected with billing.ErrAccountSuspended even for system calls.
	if _, err := backend.Suspend(ctx, byokZeroUser.ID, "abuse"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	dbSusp := records.NewShared(backend, byokZeroUser, 5.0)
	dbSusp.Now = nowFn
	dbSusp.EnforceBilling = true
	meterSusp := usage.WithBillingAdmission(dbSusp, 0, 0, nowFn)
	if _, err := newJev(byokZeroUser.ID).Classify(ctx, ClassifyRequest{
		Inputs:      []state.ConversationInput{{ID: "in-susp", Text: "hello"}},
		RecordUsage: meterSusp.RecordUsage,
	}); !errors.Is(err, billing.ErrAccountSuspended) {
		t.Fatalf("suspended Jev Classify err = %v, want ErrAccountSuspended", err)
	}
}

func TestUsageRecordErrorAndMidTurnCreditExhaustedMessageError(t *testing.T) {
	// 1. usageRecordError must preserve all billing sentinel errors and mask opaque store failures.
	for _, want := range []error{
		billing.ErrCreditExhausted,
		billing.ErrSubscriptionInactive,
		billing.ErrAccountSuspended,
		billing.ErrBYOKNotAllowed,
		billing.ErrGlobalBudgetExceeded,
		billing.ErrEntitlementMissing,
	} {
		if got := usageRecordError(want); !errors.Is(got, want) {
			t.Fatalf("usageRecordError(%v) = %v, want preserved", want, got)
		}
	}
	if got := usageRecordError(errors.New("pg connection reset")); !strings.Contains(got.Error(), "usage recording unavailable") {
		t.Fatalf("usageRecordError(opaque) = %v, want masked error", got)
	}

	// 2. OpenAIModel and AnthropicModel preserve billing.ErrCreditExhausted returned by RecordUsage.
	ctx := context.Background()
	oai := NewOpenAIModel("sk-test", "gpt-6.1-sol")
	_, oaiErr := oai.Respond(ctx, ReplyRequest{
		Messages:   []json.RawMessage{modelMessage(map[string]string{"role": "user", "content": "hi"})},
		NoDispatch: true,
		RecordUsage: func(context.Context, state.Usage) error {
			return billing.ErrCreditExhausted
		},
	})
	if !errors.Is(oaiErr, billing.ErrCreditExhausted) {
		t.Fatalf("OpenAIModel.Respond err = %v, want ErrCreditExhausted", oaiErr)
	}

	anth := NewAnthropicModel("sk-ant-test", "claude-sonnet-4-5")
	_, anthErr := anth.Respond(ctx, ReplyRequest{
		Messages:   []json.RawMessage{modelMessage(map[string]string{"role": "user", "content": "hi"})},
		NoDispatch: true,
		RecordUsage: func(context.Context, state.Usage) error {
			return billing.ErrCreditExhausted
		},
	})
	if !errors.Is(anthErr, billing.ErrCreditExhausted) {
		t.Fatalf("AnthropicModel.Respond err = %v, want ErrCreditExhausted", anthErr)
	}

	// 3. classifyModelFailure maps billing errors to non-retryable MessageError with billing.ErrorCode.
	me := classifyModelFailure(billing.ErrCreditExhausted, true)
	if me.Code != billing.CodeCreditExhausted || me.Retryable {
		t.Fatalf("classifyModelFailure(ErrCreditExhausted) = %+v, want code=credit_exhausted retryable=false", me)
	}

	// 4. End-to-end mid-turn exhaustion through Runner records MessageError.Code == "credit_exhausted".
	now := time.Date(2026, 11, 27, 13, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }
	backend := memstore.New(memstore.WithClock(nowFn))
	t.Cleanup(func() { _ = backend.Close() })
	u := users.User{ID: "22222222-2222-4222-8222-222222222201", Email: "exhaust@example.com", TimeZone: "UTC"}
	dir := records.NewSharedUsers(backend)
	dir.Now = nowFn
	if err := dir.Create(ctx, u); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	db := records.NewShared(backend, u, 5.0)
	db.Now = nowFn
	db.EnforceBilling = true
	// Exhaust the user's $5.00 trial grant completely.
	spent := 5.0
	if err := db.RecordUsage(ctx, state.Usage{
		ID: "prior-spend", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		CostUSD: &spent, CostStatus: "provider", RequestStatus: "received", CreatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("RecordUsage prior spend: %v", err)
	}
	meter := usage.WithBillingAdmission(db, 0, 0, nowFn)
	store := usage.Wrap(state.Compose(db, memstore.NewArtifacts()), meter)
	tasks := &Tasks{Store: store, Now: nowFn}
	// Model calls RecordUsage with hosted billing before provider HTTP; transport fails test if hit.
	hostedModel := NewOpenAIModel("sk-test", "gpt-6.1-sol")
	hostedModel.BillingMode = state.UsageBillingHosted
	hostedModel.Now = nowFn
	hostedModel.Client = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	})}
	runner := New(hostedModel, nil, nil, tasks)
	runner.Now = nowFn

	if _, err := runner.HandleInput(ctx, Input{ID: "33333333-3333-4333-8333-333333333301", Message: "Hello"}); err != nil {
		t.Fatalf("HandleInput: %v", err)
	}
	if err := runner.Process(ctx); err != nil {
		t.Fatalf("Process: %v", err)
	}
	snap, err := runner.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var found *state.MessageError
	for _, m := range snap.Messages {
		if m.Role == "assistant" && m.Error != nil {
			found = m.Error
		}
	}
	if found == nil || found.Code != billing.CodeCreditExhausted || found.Retryable {
		t.Fatalf("expected failed assistant message with code=credit_exhausted retryable=false, got %+v (messages=%+v)", found, snap.Messages)
	}
}
