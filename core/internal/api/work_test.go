package api

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

	"nori/internal/connections"
	"nori/internal/conversation"
	"nori/internal/permissions"
	"nori/internal/secret"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
	"nori/internal/wake"
)

func TestSummaryRevisionMonotonicWhenNeedsYouDrops(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	u := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "alice@example.com",
		DisplayName: "Alice",
		TimeZone:    "UTC",
	}
	store := state.Compose(records.NewShared(backend, u, 5.0), memstore.NewArtifacts())
	conn := connections.New(store, secret.Unavailable{}, "http://127.0.0.1:3211/api/v1/connections/oauth/callback")
	perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow"}

	assistant := &Assistant{
		Store:       store,
		Connections: conn,
		Permissions: perms,
		Features: map[string]bool{
			"work": true, "interactions": true,
		},
	}
	mux := http.NewServeMux()
	assistant.register(mux)

	getSummary := func() summary {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/assistant/summary", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/assistant/summary status=%d body=%s", rec.Code, rec.Body.String())
		}
		var out summary
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode summary: %v", err)
		}
		return out
	}

	// Start a task and park it on a clarifying question so needs_you == 1.
	taskID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	start := state.Command{
		ID:        taskID,
		RunID:     taskID,
		Kind:      state.CommandStart,
		Text:      "Check quarterly metrics",
		CreatedAt: time.Now().UTC(),
	}
	if _, err := store.AcceptCommandBatch(ctx, []state.Command{start}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch start: %v", err)
	}
	if err := store.Incorporate(ctx, state.IncorporateRequest{RunID: taskID, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("Incorporate start: %v", err)
	}
	qID := taskID + "-q1"
	q := state.Question{
		ID:       qID,
		Kind:     "input",
		Text:     "Which quarter?",
		CallID:   "call-1",
		Revision: 1,
	}
	if err := store.ProjectQuestion(ctx, taskID, 1, q); err != nil {
		t.Fatalf("ProjectQuestion: %v", err)
	}

	s1 := getSummary()
	if s1.NeedsYou != 1 {
		t.Fatalf("expected needs_you=1 while waiting on question, got %+v", s1)
	}
	if s1.Revision <= 0 {
		t.Fatalf("expected positive revision, got %d", s1.Revision)
	}

	// Answer the question and complete the task so needs_you drops back to 0.
	time.Sleep(2 * time.Millisecond)
	ans := state.Command{
		ID:         "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		RunID:      taskID,
		Kind:       state.CommandAnswer,
		Text:       "Q3",
		QuestionID: qID,
		CreatedAt:  time.Now().UTC(),
	}
	if _, err := store.AcceptCommandBatch(ctx, []state.Command{ans}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch answer: %v", err)
	}
	if err := store.Incorporate(ctx, state.IncorporateRequest{RunID: taskID, Revision: 1, Position: 2}); err != nil {
		t.Fatalf("Incorporate answer: %v", err)
	}
	if _, err := store.Publish(ctx, state.PublishRequest{
		RunID:         taskID,
		Revision:      1,
		Status:        state.RunCompleted,
		Text:          "Q3 revenue is up 14%.",
		BypassRouting: true,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	s2 := getSummary()
	if s2.NeedsYou != 0 || s2.Working != 0 {
		t.Fatalf("expected needs_you=0 working=0 after completion, got %+v", s2)
	}
	if s2.Revision <= s1.Revision {
		t.Fatalf("summary revision did not advance when needs_you dropped: before=%d after=%d", s1.Revision, s2.Revision)
	}
}

func TestSummaryRevisionStrictlyMonotonicAcrossRapidCallsAndClockJump(t *testing.T) {
	backend := memstore.New()
	u := users.User{
		ID:          "22222222-2222-4222-8222-222222222222",
		Email:       "bob@example.com",
		DisplayName: "Bob",
		TimeZone:    "UTC",
	}
	store := state.Compose(records.NewShared(backend, u, 5.0), memstore.NewArtifacts())
	conn := connections.New(store, secret.Unavailable{}, "http://127.0.0.1:3211/api/v1/connections/oauth/callback")
	perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow"}

	assistant := &Assistant{
		Store:       store,
		Connections: conn,
		Permissions: perms,
		Features:    map[string]bool{"work": true},
	}
	mux := http.NewServeMux()
	assistant.register(mux)

	// 1. Rapid sequential calls (no sleep) must yield strictly increasing revisions.
	var prev int64
	for i := 0; i < 64; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/assistant/summary", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d status=%d body=%s", i, rec.Code, rec.Body.String())
		}
		var out summary
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode summary %d: %v", i, err)
		}
		if out.Revision <= prev {
			t.Fatalf("call %d revision did not strictly increase: prev=%d got=%d", i, prev, out.Revision)
		}
		prev = out.Revision
	}

	// 2. Simulated backward wall-clock jump must still strictly increase.
	jumped := nextSummaryRevision(prev - 60_000)
	if jumped != prev+1 {
		t.Fatalf("backward wall-clock step revision = %d, want %d", jumped, prev+1)
	}

	// 3. Concurrent calls must all produce unique revisions without races.
	const goroutines = 8
	const perGoroutine = 25
	results := make(chan int64, goroutines*perGoroutine)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				results <- nextSummaryRevision(jumped)
			}
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[int64]bool, goroutines*perGoroutine)
	for rev := range results {
		if rev <= jumped {
			t.Fatalf("concurrent revision %d <= baseline %d", rev, jumped)
		}
		if seen[rev] {
			t.Fatalf("duplicate concurrent summary revision %d", rev)
		}
		seen[rev] = true
	}
	if len(seen) != goroutines*perGoroutine {
		t.Fatalf("unique concurrent revisions = %d, want %d", len(seen), goroutines*perGoroutine)
	}
}

type countingWaker struct {
	convWakes atomic.Int32
}

func (w *countingWaker) WakeConversation(context.Context) error {
	w.convWakes.Add(1)
	return nil
}
func (w *countingWaker) WakeWorker(context.Context, string, ...string) error { return nil }
func (w *countingWaker) WorkersNeedWake() bool                               { return false }

type countingOps struct {
	repairCalls atomic.Int32
	repairer    *wake.Repairer
}

func (o *countingOps) Process(context.Context) error { return nil }
func (o *countingOps) Resume(context.Context) error  { return nil }
func (o *countingOps) RepairWake(ctx context.Context) error {
	o.repairCalls.Add(1)
	if o.repairer != nil {
		return o.repairer.Repair(ctx)
	}
	return nil
}

func TestConversationEndpointsAvoidRedundantWakesAndRepairTaskOnlyWork(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	u := users.User{
		ID:          "33333333-3333-4333-8333-333333333333",
		Email:       "carol@example.com",
		DisplayName: "Carol",
		TimeZone:    "UTC",
	}
	store := state.Compose(records.NewShared(backend, u, 5.0), memstore.NewArtifacts())
	tasks := &conversation.Tasks{Store: store}
	runner := conversation.New(nil, nil, nil, tasks)
	waker := &countingWaker{}
	repairer := &wake.Repairer{
		Waker: waker,
		Pending: func(ctx context.Context) (string, error) {
			return store.WorkVersion(ctx, true)
		},
	}
	ops := &countingOps{repairer: repairer}
	h := Handler(Deps{
		Model:  "test-model",
		Runner: runner,
		Tasks:  tasks,
		Waker:  waker,
		Ops:    ops,
	})

	// 1. POST /v1/conversations/assistant/messages writes LaneConversation once in-tx
	// without calling s.Waker.WakeConversation redundantly.
	msgID := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	body := bytes.NewBufferString(`{"id":"` + msgID + `","text":"hello nori"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/assistant/messages", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /v1/conversations/assistant/messages status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := waker.convWakes.Load(); got != 0 {
		t.Fatalf("external WakeConversation calls after message accept = %d, want 0 (already woken in-tx)", got)
	}
	wakes, err := backend.ListUserWakes(ctx, u.ID)
	if err != nil || len(wakes) != 1 || wakes[0].Lane != docstore.LaneConversation || wakes[0].WakeSeq != 1 {
		t.Fatalf("user wakes after message accept = %+v (err=%v), want 1 LaneConversation wake with WakeSeq=1", wakes, err)
	}

	// 2. POST /v1/conversations/assistant/cancel does NOT spuriously call s.Waker.WakeConversation.
	cancelBody := bytes.NewBufferString(`{"turnId":"` + msgID + `"}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/conversations/assistant/cancel", cancelBody)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/conversations/assistant/cancel status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := waker.convWakes.Load(); got != 0 {
		t.Fatalf("external WakeConversation calls after cancel = %d, want 0", got)
	}

	// 3. Seed an undelivered task command while conversation inbox is empty:
	// GET /v1/conversations/assistant skips RepairWake on idle inbox, AND calling
	// Repairer.Repair directly skips task-only WorkVersion ("command:...") without
	// waking LaneConversation!
	taskID := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	if _, err := store.AcceptCommandBatch(ctx, []state.Command{{
		ID: taskID, RunID: taskID, Kind: state.CommandStart, Text: "background work", CreatedAt: time.Now().UTC(),
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}
	wv, err := store.WorkVersion(ctx, true)
	if err != nil || wv != "command:"+taskID {
		t.Fatalf("WorkVersion = (%q, %v), want command:%s", wv, err, taskID)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/conversations/assistant", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/conversations/assistant status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := ops.repairCalls.Load(); got != 0 {
		t.Fatalf("RepairWake called %d times on idle conversation snapshot, want 0", got)
	}
	if err := repairer.Repair(ctx); err != nil {
		t.Fatalf("Repairer.Repair(command:...): %v", err)
	}
	if got := waker.convWakes.Load(); got != 0 {
		t.Fatalf("Repairer woke LaneConversation %d times for task-only WorkVersion %q, want 0", got, wv)
	}

	// 4. POST /api/turn writes LaneConversation once in-tx (WakeSeq advances by 1, not 2).
	turnID := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	turnBody := bytes.NewBufferString(`{"id":"` + turnID + `","message":"second message"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/turn", turnBody)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/turn status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := waker.convWakes.Load(); got != 0 {
		t.Fatalf("external WakeConversation calls after /api/turn = %d, want 0", got)
	}
	wakes, err = backend.ListUserWakes(ctx, u.ID)
	if err != nil || len(wakes) != 1 || wakes[0].WakeSeq != 2 {
		t.Fatalf("user wakes after /api/turn = %+v (err=%v), want WakeSeq=2", wakes, err)
	}

	// 5. GET /v1/conversations/assistant while conversation IS pending & not processing
	// triggers RepairWake -> Repairer wakes LaneConversation for "assistant:...".
	req = httptest.NewRequest(http.MethodGet, "/v1/conversations/assistant", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/conversations/assistant pending status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := ops.repairCalls.Load(); got != 1 {
		t.Fatalf("RepairWake calls on pending snapshot = %d, want 1", got)
	}
	if got := waker.convWakes.Load(); got != 1 {
		t.Fatalf("Repairer WakeConversation calls on pending assistant work = %d, want 1", got)
	}
}
