package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/billing"
	"nori/internal/connections"
	"nori/internal/conversation"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/secret"
	"nori/internal/servicekeys"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestUserResolveAndDispatchEnforceSuspensionAndMissingEntitlement(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	backend := memstore.New(memstore.WithClock(func() time.Time { return now }))
	t.Cleanup(func() { _ = backend.Close() })

	dir := records.NewSharedUsers(backend)
	alice := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", DisplayName: "Alice", TimeZone: "UTC"}
	if err := dir.Create(ctx, alice); err != nil {
		t.Fatalf("Create alice: %v", err)
	}
	ghost := users.User{ID: "22222222-2222-4222-8222-222222222222", Email: "ghost@example.com", DisplayName: "Ghost", TimeZone: "UTC"}
	// Insert ghost directly without entitlement row.
	if _, err := records.NewSharedUsers(backend).Get(ctx, ghost.ID); err == nil {
		t.Fatalf("expected ghost missing")
	}
	memDir := users.NewMemoryDirectory()
	_ = memDir.Create(ctx, alice)
	_ = memDir.Create(ctx, ghost)

	billingSvc := billing.NewService(backend, false, billing.GlobalBudgetConfig{})
	handler := UserHandlerWithOptions(memDir, func(context.Context, users.User) (UserRoutes, func(), error) {
		return UserRoutes{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]string{"ok": "true"})
			}),
		}, func() {}, nil
	}, false, "test", UserHandlerOptions{
		DisableRateLimit: true,
		CheckEntitlement: func(ctx context.Context, userID string) (billing.Entitlement, error) {
			ent, err := billingSvc.GetEntitlement(ctx, userID)
			if err != nil {
				return billing.Entitlement{}, err
			}
			if ent.IsSuspended() {
				return ent, billing.ErrAccountSuspended
			}
			return ent, nil
		},
	})

	// 1. Active user resolves and accesses /users/{id}/...
	req := httptest.NewRequest(http.MethodPost, "/api/users/resolve", strings.NewReader(`{"email":"alice@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("active resolve status = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/users/"+alice.ID+"/v1/capabilities", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("active dispatch status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 2. Ghost user missing entitlement row fails closed with 403 entitlement_missing.
	req = httptest.NewRequest(http.MethodPost, "/api/users/resolve", strings.NewReader(`{"email":"ghost@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), billing.CodeEntitlementMissing) {
		t.Fatalf("missing entitlement resolve = %d %s, want 403 entitlement_missing", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/users/"+ghost.ID+"/v1/capabilities", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), billing.CodeEntitlementMissing) {
		t.Fatalf("missing entitlement dispatch = %d %s, want 403 entitlement_missing", rec.Code, rec.Body.String())
	}

	// 3. Suspended user fails with 403 account_suspended on BOTH resolve AND /users/{id}/*!
	if _, err := billingSvc.Suspend(ctx, alice.ID, "policy violation"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/users/resolve", strings.NewReader(`{"email":"alice@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), billing.CodeAccountSuspended) {
		t.Fatalf("suspended resolve = %d %s, want 403 account_suspended", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/users/"+alice.ID+"/v1/capabilities", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), billing.CodeAccountSuspended) {
		t.Fatalf("suspended dispatch = %d %s, want 403 account_suspended", rec.Code, rec.Body.String())
	}
}

func TestCostsServiceKeysCapabilitiesTierCapsAndLibraryPurge(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	backend := memstore.New(memstore.WithClock(func() time.Time { return now }))
	t.Cleanup(func() { _ = backend.Close() })

	dir := records.NewSharedUsers(backend)
	u := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "alice@example.com", DisplayName: "Alice", TimeZone: "UTC"}
	if err := dir.Create(ctx, u); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	db := records.NewShared(backend, u, 5.0)
	db.Now = func() time.Time { return now }
	db.EnforceBilling = true
	artifacts := memstore.NewArtifacts()
	store := state.Compose(db, artifacts)
	tasks := &conversation.Tasks{Store: store, Now: func() time.Time { return now }}
	replyModel := &stubReplyModel{
		fn: func(context.Context, conversation.ReplyRequest) (conversation.Reply, error) {
			return conversation.Reply{Text: "ok"}, nil
		},
	}
	runner := conversation.New(replyModel, nil, nil, tasks)
	runner.Now = func() time.Time { return now }
	conn := connections.New(store, secret.Unavailable{}, "http://127.0.0.1:3211/api/v1/connections/oauth/callback")
	perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow", Now: func() time.Time { return now }}
	sched := &scheduler.Service{
		Store: store, Tasks: store, Permissions: perms,
		Timezone: func(context.Context) string { return "UTC" },
		Now:      func() time.Time { return now },
	}
	var mutateNotify atomic.Int32
	svcKeys := &servicekeys.Service{
		Store: newMemModelKeyStore(),
		Now:   func() time.Time { return now },
		HTTPClient: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"results":[]}`)),
			}, nil
		})},
		OnMutate: func(context.Context, string) { mutateNotify.Add(1) },
	}
	modelKeys := &modelkeys.Service{
		Store: newMemModelKeyStore(),
		Now:   func() time.Time { return now },
		HTTPClient: &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"data":[]}`)),
			}, nil
		})},
	}

	billingSvc := billing.NewService(backend, false, billing.GlobalBudgetConfig{})
	assistant := &Assistant{
		UserID:               u.ID,
		Store:                store,
		Connections:          conn,
		Permissions:          perms,
		Scheduler:            sched,
		ModelKeys:            modelKeys,
		ServiceKeys:          svcKeys,
		Billing:              billingSvc,
		PlatformExaAvailable: true,
		UsageNow:             func() time.Time { return now },
		GetStorageUsed: func(ctx context.Context) (int64, error) {
			return records.GetUserStorageBytes(ctx, backend, u)
		},
		ReleaseStorageBytes: func(ctx context.Context, delta int64) error {
			return records.ReleaseUserStorageBytes(ctx, backend, u, delta)
		},
		Features: map[string]bool{"automations": true, "library": true},
	}
	h := Handler(Deps{Runner: runner, Tasks: tasks, Assistant: assistant})

	// 1. Trial user is rejected on PUT /v1/settings/service-keys/exa with 402 byok_not_allowed.
	req := httptest.NewRequest(http.MethodPut, "/v1/settings/service-keys/exa", strings.NewReader(`{"api_key":"exa_secret_1234"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), billing.CodeBYOKNotAllowed) {
		t.Fatalf("trial PUT service-key = %d %s, want 402 byok_not_allowed", rec.Code, rec.Body.String())
	}

	// 2. Upgrade to standard: PUT/GET/DELETE /v1/settings/service-keys/exa succeeds and never leaks raw key.
	if _, err := billingSvc.SetTier(ctx, u.ID, billing.TierStandard, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier standard: %v", err)
	}
	req = httptest.NewRequest(http.MethodPut, "/v1/settings/service-keys/exa", strings.NewReader(`{"api_key":"exa_secret_9876"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "exa_secret_9876") || !strings.Contains(rec.Body.String(), "9876") {
		t.Fatalf("standard PUT service-key = %d %s", rec.Code, rec.Body.String())
	}
	if mutateNotify.Load() != 1 {
		t.Fatalf("expected OnMutate fired once, got %d", mutateNotify.Load())
	}

	// 3. /v1/capabilities includes entitlement + service_keys.
	req = httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/capabilities = %d %s", rec.Code, rec.Body.String())
	}
	var capBody struct {
		Entitlement *capabilitiesEntitlement `json:"entitlement"`
		ServiceKeys *capabilitiesServiceKeys `json:"service_keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &capBody); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if capBody.Entitlement == nil || capBody.Entitlement.Tier != "standard" || capBody.Entitlement.MaxRoutines != 15 || capBody.Entitlement.MaxConcurrentTasks != 4 {
		t.Fatalf("unexpected capabilities entitlement: %+v", capBody.Entitlement)
	}
	if capBody.ServiceKeys == nil || !capBody.ServiceKeys.ExaConfigured || !capBody.ServiceKeys.PlatformExaAvailable {
		t.Fatalf("unexpected capabilities service_keys: %+v", capBody.ServiceKeys)
	}

	// 4. DELETE /v1/settings/service-keys/exa clears key and fires OnMutate.
	req = httptest.NewRequest(http.MethodDelete, "/v1/settings/service-keys/exa", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || mutateNotify.Load() != 2 {
		t.Fatalf("DELETE service-key = %d notify=%d body=%s", rec.Code, mutateNotify.Load(), rec.Body.String())
	}

	// 5. GET /v1/costs returns by_billing, by_category, and entitlement summary.
	cost := 0.5
	_ = db.RecordUsage(ctx, state.Usage{
		ID: "cost-1", Provider: "gateway", Model: "openai/gpt-5.4",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		CostUSD: &cost, CostStatus: "provider", RequestStatus: "completed", CreatedAt: now.Add(-time.Hour),
	})
	req = httptest.NewRequest(http.MethodGet, "/v1/costs?days=7", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/costs = %d %s", rec.Code, rec.Body.String())
	}
	var costBody map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &costBody); err != nil {
		t.Fatalf("decode costs: %v", err)
	}
	if costBody["by_billing"] == nil || costBody["by_category"] == nil || costBody["entitlement"] == nil {
		t.Fatalf("missing cost breakdowns or entitlement: %s", rec.Body.String())
	}

	// 6. Routine interval cap: standard min interval is 15m -> 60s min_interval_seconds rejected.
	autoReq := map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"title":                    "Too fast watch",
			"instruction":              "Check something",
			"trigger":                  map[string]any{"kind": "recurring", "local_start": "2026-11-28T08:00", "timezone": "UTC", "rrule": "FREQ=DAILY;BYMINUTE=0,5,10"},
			"connection_ids":           []string{},
			"budget_usd":               0.5,
			"max_active_runs":          1,
			"missed_run":               "skip",
			"execution_window_seconds": 300,
			"destination_ids":          []string{"dest_in_app"},
		},
	}
	rawAuto, _ := json.Marshal(autoReq)
	req = httptest.NewRequest(http.MethodPost, "/v1/automations", bytes.NewReader(rawAuto))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), codeRoutineIntervalShort) {
		t.Fatalf("short interval routine = %d %s, want 403 %s", rec.Code, rec.Body.String(), codeRoutineIntervalShort)
	}

	// 7. Library delete purges underlying blob without decrementing uploads-only nori_user_storage.
	runID := uuid.NewString()
	if _, err := tasks.AcceptBatch(ctx, []state.Command{{ID: runID, RunID: runID, Kind: state.CommandStart, Text: "make report"}}, nil); err != nil {
		t.Fatalf("AcceptBatch start run: %v", err)
	}
	resID := uuid.NewString()
	fileBytes := []byte("generated-report-blob-contents")
	if err := store.PutFile(ctx, state.ResultKey(resID), "text/plain", fileBytes); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	const priorUploadBytes = int64(1234)
	if err := records.SetUserStorageBytes(ctx, backend, u, priorUploadBytes); err != nil {
		t.Fatalf("SetUserStorageBytes: %v", err)
	}
	if _, err := db.SaveResult(ctx, state.Result{
		ID: resID, RunID: runID, Kind: state.ResultFile, Title: "Report",
		Name: "report.txt", MediaType: "text/plain", Size: int64(len(fileBytes)), CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveResult: %v", err)
	}
	delReq := map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value":             map[string]any{"action": "delete", "scope": "version"},
	}
	rawDel, _ := json.Marshal(delReq)
	req = httptest.NewRequest(http.MethodPost, "/v1/library/"+resID+"/actions", bytes.NewReader(rawDel))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("library delete = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetFile(ctx, state.ResultKey(resID)); err == nil {
		t.Fatalf("expected library file blob to be purged on delete")
	}
	afterBytes, err := records.GetUserStorageBytes(ctx, backend, u)
	if err != nil || afterBytes != priorUploadBytes {
		t.Fatalf("storage bytes after library delete = %d (err=%v), want unchanged %d (uploads-only quota)", afterBytes, err, priorUploadBytes)
	}

	// 8. Expired subscription allows GET /v1/costs (read-only) but blocks POST message with 402 subscription_inactive.
	if _, err := billingSvc.SetTier(ctx, u.ID, billing.TierStandard, billing.SetTierOptions{Status: billing.StatusExpired}); err != nil {
		t.Fatalf("SetTier expired: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/costs?days=7", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expired GET /v1/costs = %d, want 200 read-only", rec.Code)
	}
	msgReq := map[string]any{"id": uuid.NewString(), "text": "hello"}
	rawMsg, _ := json.Marshal(msgReq)
	req = httptest.NewRequest(http.MethodPost, "/v1/conversations/assistant/messages", bytes.NewReader(rawMsg))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), billing.CodeSubscriptionInactive) {
		t.Fatalf("expired POST message = %d %s, want 402 subscription_inactive", rec.Code, rec.Body.String())
	}
}

func TestChatSendReturns402CreditExhaustedForHostedAndAllowsBYOK(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)
	nowFn := func() time.Time { return now }
	backend := memstore.New(memstore.WithClock(nowFn))
	t.Cleanup(func() { _ = backend.Close() })

	dir := records.NewSharedUsers(backend)
	dir.Now = nowFn
	u := users.User{ID: "33333333-3333-4333-8333-333333333311", Email: "chat-credit@example.com", TimeZone: "UTC"}
	if err := dir.Create(ctx, u); err != nil {
		t.Fatalf("Create user: %v", err)
	}

	db := records.NewShared(backend, u, 5.0)
	db.Now = nowFn
	db.EnforceBilling = true
	// Exhaust trial $5.00 hosted credit completely.
	spent := 5.0
	if err := db.RecordUsage(ctx, state.Usage{
		ID: "spent-all", Provider: "gateway", Model: "google/gemini-4-neon",
		Billing: state.UsageBillingHosted, Category: state.UsageCategoryLLM,
		CostUSD: &spent, CostStatus: "provider", RequestStatus: "received", CreatedAt: now.Add(-time.Minute),
	}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	store := state.Compose(db, memstore.NewArtifacts())
	tasks := &conversation.Tasks{Store: store, Now: nowFn}
	runner := conversation.New(&stubReplyModel{
		fn: func(context.Context, conversation.ReplyRequest) (conversation.Reply, error) {
			return conversation.Reply{Text: "ok"}, nil
		},
	}, nil, nil, tasks)
	runner.Now = nowFn

	httpOK := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
		}, nil
	})}
	modelKeys := &modelkeys.Service{Store: newMemModelKeyStore(), Now: nowFn, HTTPClient: httpOK, HostedAvailable: true}
	account := &modelprofiles.Account{Keys: modelKeys, Store: db, HostedEnabled: true}
	billingSvc := billing.NewService(backend, false, billing.GlobalBudgetConfig{})
	assistant := &Assistant{
		UserID:      u.ID,
		Store:       store,
		ModelKeys:   modelKeys,
		Models:      account,
		AccountBYOK: true,
		Billing:     billingSvc,
		UsageNow:    nowFn,
	}
	h := Handler(Deps{Runner: runner, Tasks: tasks, Assistant: assistant})

	// 1. Default hosted chat model with $0 balance -> immediate 402 credit_exhausted on send!
	rawMsg, _ := json.Marshal(map[string]any{"id": uuid.NewString(), "text": "hello hosted"})
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/assistant/messages", bytes.NewReader(rawMsg))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired || !strings.Contains(rec.Body.String(), billing.CodeCreditExhausted) {
		t.Fatalf("hosted exhausted POST message = %d %s, want 402 credit_exhausted", rec.Code, rec.Body.String())
	}

	// 2. Switch user to BYOK tier ($0 hosted credit) and pin a BYOK chat model -> POST message succeeds (202)!
	if _, err := billingSvc.SetTier(ctx, u.ID, billing.TierBYOK, billing.SetTierOptions{}); err != nil {
		t.Fatalf("SetTier byok: %v", err)
	}
	if _, err := modelKeys.Set(ctx, "openai", "sk-test-byok-key-1234567890"); err != nil {
		t.Fatalf("set openai key: %v", err)
	}
	view, err := account.View(ctx)
	if err != nil {
		t.Fatalf("account.View: %v", err)
	}
	if _, err := account.Save(ctx, state.ModelSelection{
		ChatProfileID: "openai-sol",
		ChatReasoning: "low",
		TaskProfileID: "openai-sol",
		TaskReasoning: "low",
	}, view.Revision, nil); err != nil {
		t.Fatalf("account.Save BYOK profile: %v", err)
	}

	rawBYOK, _ := json.Marshal(map[string]any{"id": uuid.NewString(), "text": "hello byok"})
	reqBYOK := httptest.NewRequest(http.MethodPost, "/v1/conversations/assistant/messages", bytes.NewReader(rawBYOK))
	reqBYOK.Header.Set("Content-Type", "application/json")
	recBYOK := httptest.NewRecorder()
	h.ServeHTTP(recBYOK, reqBYOK)
	if recBYOK.Code != http.StatusAccepted {
		t.Fatalf("BYOK POST message with $0 hosted credit = %d %s, want 202", recBYOK.Code, recBYOK.Body.String())
	}
}
