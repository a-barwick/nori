package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/app"
	"nori/internal/conversation"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/transcript"
	"nori/internal/users"
	"nori/internal/worker"
)

type simFakeReplyModel struct {
	fn func(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error)
}

func (m *simFakeReplyModel) Name() string { return "sim-fake-reply" }
func (m *simFakeReplyModel) Respond(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error) {
	return m.fn(ctx, req)
}

type simFakeSummaryModel struct {
	fn func(ctx context.Context, req agent.Request) (agent.Response, error)
}

func (m *simFakeSummaryModel) Name() string { return "sim-fake-summary" }
func (m *simFakeSummaryModel) Respond(ctx context.Context, req agent.Request) (agent.Response, error) {
	return m.fn(ctx, req)
}

func newDrainTestEnv(t *testing.T, start time.Time, scopedUserID string) (*app.Service, *memstore.Backend, *simClockBackend, *simClock) {
	t.Helper()
	clock := newSimClock(start)
	// memstore uses wall clock for lease_until / wake_at, matching Postgres now().
	mem := memstore.New(memstore.WithClock(time.Now))
	simBackend := newSimClockBackend(mem, clock)
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(simBackend, func(users.User) state.ArtifactStore {
		return artifacts
	})
	storage.Now = clock.Now
	cfg := settings.Settings{
		Cloud:         "local",
		Role:          "worker",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Models: settings.Models{
			OpenAIKey:     "test-openai-key",
			OpenRouterKey: "test-openrouter-key",
			Outer:         "gpt-6.1-sol",
			Inner:         "openai/gpt-6.1-terra",
		},
	}
	svc := app.OpenServiceWithStorage(storage, platform.Waker(simBackend), cfg)
	svc.Now = clock.Now
	svc.IdleTTL = 365 * 24 * time.Hour
	t.Cleanup(svc.Close)

	return svc, mem, simBackend, clock
}

func configureSimAppClocks(a *app.App, clock *simClock) {
	if a.Runner != nil {
		a.Runner.Now = clock.Now
		a.Runner.Clock.Now = clock.Now
	}
	if a.Tasks != nil {
		a.Tasks.Now = clock.Now
	}
	if a.Scheduler != nil {
		a.Scheduler.Now = clock.Now
	}
	if a.WakeRepair != nil {
		a.WakeRepair.Now = clock.Now
	}
	if a.Assistant != nil {
		if a.Assistant.Permissions != nil {
			a.Assistant.Permissions.Now = clock.Now
		}
		if a.Assistant.Connections != nil {
			a.Assistant.Connections.Now = clock.Now
		}
		if a.Assistant.MCP != nil {
			a.Assistant.MCP.Now = clock.Now
		}
	}
	if a.TaskActivities != nil {
		a.TaskActivities.Now = clock.Now
	}
}

func mustSeedSimUser(t *testing.T, svc *app.Service, id, email string) users.User {
	t.Helper()
	u := users.User{
		ID:          id,
		Email:       email,
		DisplayName: email,
		TimeZone:    "UTC",
	}
	if err := svc.Users.Create(context.Background(), u); err != nil {
		t.Fatalf("Users.Create %s: %v", id, err)
	}
	return u
}

func startSimWorkerPool(t *testing.T, svc *app.Service, clock *simClock, scopedUserID string) {
	t.Helper()
	pool, err := worker.New(svc, worker.Options{
		Owner:                   "test-sim-worker-" + uuid.NewString()[:8],
		UserID:                  scopedUserID,
		TaskConcurrency:         4,
		ConversationConcurrency: 2,
		SchedulerConcurrency:    2,
		PollInterval:            50 * time.Millisecond,
		RepairInterval:          time.Hour,
		DrainTimeout:            3 * time.Second,
		Now:                     clock.Now,
	})
	if err != nil {
		t.Fatalf("worker.New: %v", err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- pool.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("pool.Run: %v", err)
		}
	})
	if mem, ok := svc.Backend().(*simClockBackend); ok {
		if raw, ok := mem.Unwrap().(*memstore.Backend); ok {
			if err := raw.WaitForWakeSubscribers(context.Background(), 1); err != nil {
				t.Fatalf("WaitForWakeSubscribers: %v", err)
			}
		}
	}
}

func simReply(text string, at time.Time) conversation.Reply {
	msg, _ := json.Marshal(map[string]any{"role": "assistant", "content": text})
	return conversation.Reply{
		Usage:   state.Usage{ID: uuid.NewString(), Provider: "sim", Model: "sim-fake-reply", CostStatus: "known", CreatedAt: at},
		Message: msg,
		Text:    text,
	}
}

func simFlushSummaryResponse() agent.Response {
	call := transcript.ToolCall{
		ID:   uuid.NewString(),
		Name: "flush_and_compact",
		Args: map[string]any{
			"core_upserts":      []any{},
			"daily_log_entries": []any{},
			"summary_items":     []any{},
		},
	}
	return agent.Response{
		Message:   &transcript.Message{Role: transcript.RoleModel, Parts: []transcript.Part{{ToolCall: &call}}},
		Calls:     []transcript.ToolCall{call},
		CostKnown: true,
	}
}

func TestDrainUserToQuiescenceReleasesDelayedWakes(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2027, 1, 10, 9, 0, 0, 0, time.UTC)
	svc, _, simBackend, clock := newDrainTestEnv(t, start, "")

	var replyCalls, extractCalls atomic.Int32
	svc.ConfigureApp = func(_ users.User, a *app.App) {
		tasks := &conversation.Tasks{Store: a.Store, Now: clock.Now}
		replyModel := &simFakeReplyModel{
			fn: func(_ context.Context, _ conversation.ReplyRequest) (conversation.Reply, error) {
				replyCalls.Add(1)
				return simReply("Got it.", clock.Now()), nil
			},
		}
		summaryModel := &simFakeSummaryModel{
			fn: func(_ context.Context, _ agent.Request) (agent.Response, error) {
				extractCalls.Add(1)
				return simFlushSummaryResponse(), nil
			},
		}
		a.Runner = conversation.New(replyModel, nil, summaryModel, tasks)
		a.Runner.Clock = conversation.Clock{Location: time.UTC, Now: clock.Now}
		configureSimAppClocks(a, clock)
	}

	u := mustSeedSimUser(t, svc, uuid.NewString(), "delayed@example.com")
	uApp, rel, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer rel()

	startSimWorkerPool(t, svc, clock, u.ID)

	// 1. Admit a conversation message and also schedule a 2s delayed task wake.
	if err := uApp.Store.AcceptConversationInput(ctx, uuid.NewString(), "hello nori", nil); err != nil {
		t.Fatalf("AcceptConversationInput: %v", err)
	}
	// Schedule a 2s task wake (<= 2m shortTaskWakeThreshold -> wall clock) and a 2s scheduler wake (<= 30s shortUserWakeThreshold).
	if err := simBackend.WakeUser(ctx, u.ID, docstore.LaneScheduler, clock.Now().Add(2*time.Second)); err != nil {
		t.Fatalf("WakeUser scheduler +2s: %v", err)
	}

	drainCtx, cancelDrain := context.WithTimeout(ctx, 10*time.Second)
	defer cancelDrain()
	res, err := drainUserToQuiescence(drainCtx, svc, simBackend, clock, u.ID)
	if err != nil {
		t.Fatalf("drainUserToQuiescence #1: %v (res=%+v)", err, res)
	}
	if !res.Quiescent || replyCalls.Load() != 1 {
		t.Fatalf("expected quiescent with 1 reply, got res=%+v replies=%d", res, replyCalls.Load())
	}
	// Crucial H-9 invariant: 2m IdleExtractDelay must NOT have triggered yet because simClock is 0x frozen!
	if extractCalls.Load() != 0 {
		t.Fatalf("2m IdleExtractDelay triggered prematurely before clock advance (extractCalls=%d)", extractCalls.Load())
	}

	// 2. Now advance clock by 2m + 1s (crossing IdleExtractDelay) and drain -> extraction MUST run cleanly.
	clock.Advance(state.IdleExtractDelay + time.Second)
	res2, err := drainUserToQuiescence(drainCtx, svc, simBackend, clock, u.ID)
	if err != nil {
		t.Fatalf("drainUserToQuiescence #2 after +2m: %v (res=%+v)", err, res2)
	}
	if extractCalls.Load() != 1 {
		t.Fatalf("expected 1 idle extraction after advancing past 2m IdleExtractDelay, got %d", extractCalls.Load())
	}
	summary, err := uApp.Store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary: %v", err)
	}
	if summary.EffectiveExtractedThrough().IsZero() {
		t.Fatalf("expected ExtractedThrough updated after 2m idle extraction, got %+v", summary)
	}
}

func TestDrainUserToQuiescenceBurstCoalescingRestartQuiet(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2027, 1, 10, 10, 0, 0, 0, time.UTC)
	svc, _, simBackend, clock := newDrainTestEnv(t, start, "")

	firstCallStarted := make(chan struct{})
	releaseFirstCall := make(chan struct{})
	var replyCalls, extractCalls atomic.Int32
	var mu sync.Mutex
	var replyTexts []string

	svc.ConfigureApp = func(_ users.User, a *app.App) {
		tasks := &conversation.Tasks{Store: a.Store, Now: clock.Now}
		replyModel := &simFakeReplyModel{
			fn: func(mCtx context.Context, req conversation.ReplyRequest) (conversation.Reply, error) {
				n := replyCalls.Add(1)
				if n == 1 {
					close(firstCallStarted)
					select {
					case <-releaseFirstCall:
					case <-mCtx.Done():
						return conversation.Reply{}, mCtx.Err()
					}
					// Runner checks StaleFor after Respond and restarts after RestartQuiet.
					return simReply("first-attempt-should-be-superseded", clock.Now()), nil
				}
				mu.Lock()
				replyTexts = append(replyTexts, req.TelemetryInput)
				mu.Unlock()
				return simReply("coalesced reply", clock.Now()), nil
			},
		}
		summaryModel := &simFakeSummaryModel{
			fn: func(_ context.Context, _ agent.Request) (agent.Response, error) {
				extractCalls.Add(1)
				return simFlushSummaryResponse(), nil
			},
		}
		a.Runner = conversation.New(replyModel, nil, summaryModel, tasks)
		a.Runner.Clock = conversation.Clock{Location: time.UTC, Now: clock.Now}
		configureSimAppClocks(a, clock)
	}

	u := mustSeedSimUser(t, svc, uuid.NewString(), "burst@example.com")
	uApp, rel, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer rel()

	startSimWorkerPool(t, svc, clock, u.ID)

	// Send message #1, wait until Runner is inside ReplyModel call #1, then admit message #2
	// while clock is still frozen at `start` (triggering RestartWait = 1s).
	if err := uApp.Store.AcceptConversationInput(ctx, uuid.NewString(), "burst msg 1", nil); err != nil {
		t.Fatalf("AcceptConversationInput 1: %v", err)
	}
	select {
	case <-firstCallStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for first ReplyModel call")
	}
	if err := uApp.Store.AcceptConversationInput(ctx, uuid.NewString(), "burst msg 2", nil); err != nil {
		t.Fatalf("AcceptConversationInput 2: %v", err)
	}
	close(releaseFirstCall)

	drainCtx, cancelDrain := context.WithTimeout(ctx, 10*time.Second)
	defer cancelDrain()
	res, err := drainUserToQuiescence(drainCtx, svc, simBackend, clock, u.ID)
	if err != nil {
		t.Fatalf("drainUserToQuiescence burst: %v (res=%+v)", err, res)
	}
	if !res.Quiescent {
		t.Fatalf("expected quiescent, got %+v", res)
	}
	if replyCalls.Load() != 2 {
		t.Fatalf("expected 2 ReplyModel calls (1 superseded + 1 coalesced), got %d", replyCalls.Load())
	}
	// Clock should have advanced by ~1.001s (RestartQuiet + 1ms), well short of 2m IdleExtractDelay and 30s ConversationLease.
	elapsed := clock.Now().Sub(start)
	if elapsed < state.RestartQuiet || elapsed > 5*time.Second {
		t.Fatalf("expected clock advance ~1.001s for RestartQuiet, got %s", elapsed)
	}
	if extractCalls.Load() != 0 {
		t.Fatalf("expected 0 idle extractions during burst coalesce, got %d", extractCalls.Load())
	}
	// Verify both messages were coalesced into a single assistant reply.
	msgs, _, err := uApp.Store.ConversationMessages(ctx, "")
	if err != nil {
		t.Fatalf("ConversationMessages: %v", err)
	}
	assistantCount := 0
	for _, m := range msgs {
		if m.Role == "assistant" {
			assistantCount++
			if m.Text != "coalesced reply" {
				t.Fatalf("unexpected assistant reply text: %q", m.Text)
			}
		}
	}
	if assistantCount != 1 {
		t.Fatalf("expected 1 coalesced assistant reply, got %d (msgs=%+v)", assistantCount, msgs)
	}
}

func TestDrainUserToQuiescenceFarHorizonNudge(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2027, 1, 10, 11, 0, 0, 0, time.UTC)
	svc, mem, simBackend, clock := newDrainTestEnv(t, start, "")

	var convRuns atomic.Int32
	svc.ConfigureApp = func(_ users.User, a *app.App) {
		tasks := &conversation.Tasks{Store: a.Store, Now: clock.Now}
		replyModel := &simFakeReplyModel{
			fn: func(_ context.Context, _ conversation.ReplyRequest) (conversation.Reply, error) {
				convRuns.Add(1)
				return simReply("nudged reply", clock.Now()), nil
			},
		}
		summaryModel := &simFakeSummaryModel{
			fn: func(_ context.Context, _ agent.Request) (agent.Response, error) {
				return simFlushSummaryResponse(), nil
			},
		}
		a.Runner = conversation.New(replyModel, nil, summaryModel, tasks)
		a.Runner.Clock = conversation.Clock{Location: time.UTC, Now: clock.Now}
		configureSimAppClocks(a, clock)
	}

	u := mustSeedSimUser(t, svc, uuid.NewString(), "farhorizon@example.com")
	uApp, rel, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer rel()

	// Seed a pending conversation input, then overwrite its memstore user wake row to
	// runnable=true, owner="", wake_at=now+365d (farHorizon) while clearing simBackend.userWakes
	// to simulate a restart/missed in-memory wake entry.
	if err := uApp.Store.AcceptConversationInput(ctx, uuid.NewString(), "needs nudge", nil); err != nil {
		t.Fatalf("AcceptConversationInput: %v", err)
	}
	if err := mem.WakeUser(ctx, u.ID, docstore.LaneConversation, time.Now().UTC().Add(365*24*time.Hour)); err != nil {
		t.Fatalf("mem.WakeUser farHorizon: %v", err)
	}
	simBackend.mu.Lock()
	delete(simBackend.userWakes, userWakeKey{userID: u.ID, lane: docstore.LaneConversation})
	simBackend.mu.Unlock()

	startSimWorkerPool(t, svc, clock, u.ID)

	drainCtx, cancelDrain := context.WithTimeout(ctx, 10*time.Second)
	defer cancelDrain()
	res, err := drainUserToQuiescence(drainCtx, svc, simBackend, clock, u.ID)
	if err != nil {
		t.Fatalf("drainUserToQuiescence farHorizon: %v (res=%+v)", err, res)
	}
	if !res.Quiescent || convRuns.Load() != 1 {
		t.Fatalf("expected farHorizon row nudged and drained; res=%+v convRuns=%d", res, convRuns.Load())
	}
}

func TestSimContextFromAndClockEndpoints(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2027, 1, 10, 12, 0, 0, 0, time.UTC)
	svc, mem, simBackend, clock := newDrainTestEnv(t, start, "")

	u1 := mustSeedSimUser(t, svc, uuid.NewString(), "ctx1@example.com")
	u2 := mustSeedSimUser(t, svc, uuid.NewString(), "ctx2@example.com")

	app1, rel1, err := svc.Acquire(ctx, u1)
	if err != nil {
		t.Fatalf("Acquire u1: %v", err)
	}
	defer rel1()
	if err := app1.Store.AcceptConversationInput(ctx, uuid.NewString(), "prior msg", nil); err != nil {
		t.Fatalf("AcceptConversationInput: %v", err)
	}
	beforeCtrl, _, err := app1.Store.ConversationStatus(ctx)
	if err != nil || beforeCtrl.Received != 1 {
		t.Fatalf("before ConversationStatus = (%+v, %v), want Received=1", beforeCtrl, err)
	}

	handler := buildSimHandler(svc, simBackend, clock, u1.ID)

	// 1. Unknown user -> 404.
	req404 := httptest.NewRequest(http.MethodPost, "/_sim/users/"+uuid.NewString()+"/context_from", bytes.NewReader([]byte(`{}`)))
	rec404 := httptest.NewRecorder()
	handler.ServeHTTP(rec404, req404)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("unknown user status = %d, want 404 (body=%s)", rec404.Code, rec404.Body.String())
	}

	// 2. Bad timestamp / malformed JSON -> 400.
	reqBadTime := httptest.NewRequest(http.MethodPost, "/_sim/users/"+u1.ID+"/context_from", bytes.NewReader([]byte(`{"context_from":"not-a-timestamp"}`)))
	recBadTime := httptest.NewRecorder()
	handler.ServeHTTP(recBadTime, reqBadTime)
	if recBadTime.Code != http.StatusBadRequest {
		t.Fatalf("bad timestamp status = %d, want 400 (body=%s)", recBadTime.Code, recBadTime.Body.String())
	}

	reqBadJSON := httptest.NewRequest(http.MethodPost, "/_sim/users/"+u1.ID+"/context_from", bytes.NewReader([]byte(`{bad`)))
	recBadJSON := httptest.NewRecorder()
	handler.ServeHTTP(recBadJSON, reqBadJSON)
	if recBadJSON.Code != http.StatusBadRequest {
		t.Fatalf("bad JSON status = %d, want 400 (body=%s)", recBadJSON.Code, recBadJSON.Body.String())
	}

	// 3. Empty object {} defaults to clock.Now().UTC().Truncate(time.Microsecond) and preserves Received/Consumed.
	clock.Advance(5*time.Minute + 123*time.Nanosecond)
	expectedDefault := clock.Now().UTC().Truncate(time.Microsecond)
	reqDefault := httptest.NewRequest(http.MethodPost, "/_sim/users/"+u1.ID+"/context_from", bytes.NewReader([]byte(`{}`)))
	recDefault := httptest.NewRecorder()
	handler.ServeHTTP(recDefault, reqDefault)
	if recDefault.Code != http.StatusOK {
		t.Fatalf("default context_from status = %d, body=%s", recDefault.Code, recDefault.Body.String())
	}
	var respDefault struct {
		ContextFrom string `json:"context_from"`
	}
	if err := json.Unmarshal(recDefault.Body.Bytes(), &respDefault); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if parsed, err := time.Parse(time.RFC3339Nano, respDefault.ContextFrom); err != nil || !parsed.Equal(expectedDefault) {
		t.Fatalf("respDefault.ContextFrom = %q (err=%v), want %s", respDefault.ContextFrom, err, expectedDefault)
	}
	afterCtrl, _, err := app1.Store.ConversationStatus(ctx)
	if err != nil || !afterCtrl.ContextFrom.Equal(expectedDefault) || afterCtrl.Received != beforeCtrl.Received {
		t.Fatalf("after default ConversationStatus = (%+v, %v), want ContextFrom=%s Received=%d",
			afterCtrl, err, expectedDefault, beforeCtrl.Received)
	}

	// 4. Explicit RFC3339 timestamp updates ContextFrom accurately.
	explicitTime := start.Add(2 * time.Hour).UTC()
	reqExplicit := httptest.NewRequest(http.MethodPost, "/_sim/users/"+u1.ID+"/context_from",
		bytes.NewReader([]byte(`{"context_from":"`+explicitTime.Format(time.RFC3339)+`"}`)))
	recExplicit := httptest.NewRecorder()
	handler.ServeHTTP(recExplicit, reqExplicit)
	if recExplicit.Code != http.StatusOK {
		t.Fatalf("explicit context_from status = %d, body=%s", recExplicit.Code, recExplicit.Body.String())
	}
	afterExplicit, _, err := app1.Store.ConversationStatus(ctx)
	if err != nil || !afterExplicit.ContextFrom.Equal(explicitTime) {
		t.Fatalf("after explicit ConversationStatus = (%+v, %v), want ContextFrom=%s", afterExplicit, err, explicitTime)
	}

	// 5. Scoped wakeDueForAllUsers via POST /_sim/clock/advance only wakes u1, not u2.
	dueAt := clock.Now().Add(2 * time.Minute)
	if err := simBackend.WakeUser(ctx, u1.ID, docstore.LaneConversation, dueAt); err != nil {
		t.Fatalf("WakeUser u1: %v", err)
	}
	if err := simBackend.WakeUser(ctx, u2.ID, docstore.LaneConversation, dueAt); err != nil {
		t.Fatalf("WakeUser u2: %v", err)
	}
	reqAdv := httptest.NewRequest(http.MethodPost, "/_sim/clock/advance", bytes.NewReader([]byte(`{"seconds":180}`)))
	recAdv := httptest.NewRecorder()
	handler.ServeHTTP(recAdv, reqAdv)
	if recAdv.Code != http.StatusOK {
		t.Fatalf("clock/advance status = %d, body=%s", recAdv.Code, recAdv.Body.String())
	}
	if w1, err := mem.ListUserWakes(ctx, u1.ID); err != nil || len(w1) != 1 || !w1[0].Runnable || w1[0].WakeAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("expected scoped u1 wake released to now, got %+v (err=%v)", w1, err)
	}
	if w2, err := mem.ListUserWakes(ctx, u2.ID); err != nil || len(w2) != 1 || !w2[0].WakeAt.After(time.Now().UTC().Add(30*24*time.Hour)) {
		t.Fatalf("expected unscoped u2 wake still parked at farHorizon after scoped clock/advance, got %+v (err=%v)", w2, err)
	}
}
