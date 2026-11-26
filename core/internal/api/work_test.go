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

	jumped := nextSummaryRevision(prev - 60_000)
	if jumped != prev+1 {
		t.Fatalf("backward wall-clock step revision = %d, want %d", jumped, prev+1)
	}

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

func TestWorkDetailWebSearchOperationsProjection(t *testing.T) {
	ctx := context.Background()
	fixedNow := time.Date(2026, 11, 25, 6, 49, 10, 0, time.UTC)
	backend := memstore.New()
	u := users.User{
		ID:          "44444444-4444-4444-8444-444444444444",
		Email:       "dana@example.com",
		DisplayName: "Dana",
		TimeZone:    "America/Los_Angeles",
	}
	store := state.Compose(records.NewShared(backend, u, 5.0), memstore.NewArtifacts())
	conn := connections.New(store, secret.Unavailable{}, "http://127.0.0.1:3211/api/v1/connections/oauth/callback")
	perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow"}

	assistant := &Assistant{
		Store:       store,
		Connections: conn,
		Permissions: perms,
		Features:    map[string]bool{"work": true, "interactions": true},
	}
	mux := http.NewServeMux()
	assistant.register(mux)

	taskID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	if _, err := store.AcceptCommandBatch(ctx, []state.Command{{
		ID: taskID, RunID: taskID, Kind: state.CommandStart, Text: "Check SF weather Thursday", CreatedAt: fixedNow,
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}

	opsToSeed := []state.OperationRecord{
		{
			ID:          taskID + "-ws-1",
			CallID:      "ws_1",
			Name:        "web_search",
			EffectClass: "read",
			Outcome:     "confirmed",
			Revision:    1,
			Attempt:     1,
			Action:      &state.Action{Digest: "d1", Text: "Searched: San Francisco weather Thursday"},
			CreatedAt:   &fixedNow,
		},
		{
			ID:          taskID + "-ws-2",
			CallID:      "ws_2",
			Name:        "web_search",
			EffectClass: "read",
			Outcome:     "failed",
			Revision:    1,
			Attempt:     1,
			Action:      &state.Action{Digest: "d2", Text: "Searched: unreachable weather station"},
			CreatedAt:   &fixedNow,
		},
		{
			ID:          taskID + "-ws-3-legacy",
			CallID:      "ws_3",
			Name:        "web_search",
			EffectClass: "read",
			Outcome:     "confirmed",
			Revision:    1,
			Attempt:     1,
		},
	}
	for _, op := range opsToSeed {
		if err := store.PutOperation(ctx, taskID, op); err != nil {
			t.Fatalf("PutOperation(%s): %v", op.ID, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/work/"+taskID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/work/%s status=%d body=%s", taskID, rec.Code, rec.Body.String())
	}

	var payload struct {
		Operations []json.RawMessage `json:"operations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode work detail: %v", err)
	}
	if len(payload.Operations) != 3 {
		t.Fatalf("len(operations) = %d, want 3", len(payload.Operations))
	}

	wantSummaries := []string{
		"Searched: San Francisco weather Thursday",
		"Searched: unreachable weather station",
		"Searched the web",
	}
	wantOutcomes := []string{"confirmed", "failed", "confirmed"}

	for i, raw := range payload.Operations {
		var op operationProjection
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&op); err != nil {
			t.Fatalf("op[%d] strict decode into operationProjection failed: %v (raw=%s)", i, err, string(raw))
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal op[%d]: %v", i, err)
		}
		if m["name"] != "web_search" {
			t.Fatalf("op[%d] name = %#v, want web_search", i, m["name"])
		}
		if m["effect_class"] != "read" {
			t.Fatalf("op[%d] effect_class = %#v, want read", i, m["effect_class"])
		}
		if m["outcome"] != wantOutcomes[i] {
			t.Fatalf("op[%d] outcome = %#v, want %q", i, m["outcome"], wantOutcomes[i])
		}
		if m["action_summary"] != wantSummaries[i] {
			t.Fatalf("op[%d] action_summary = %#v, want %q", i, m["action_summary"], wantSummaries[i])
		}
		if _, hasAuth := m["authority_source"]; hasAuth {
			t.Fatalf("op[%d] must omit authority_source for internal web_search, got %#v", i, m["authority_source"])
		}
		if _, hasAccount := m["account_summary"]; hasAccount {
			t.Fatalf("op[%d] must omit account_summary for internal web_search, got %#v", i, m["account_summary"])
		}
		if i < 2 {
			if m["created_at"] != fixedNow.Format(time.RFC3339) {
				t.Fatalf("op[%d] created_at = %#v, want %q", i, m["created_at"], fixedNow.Format(time.RFC3339))
			}
		}
	}
}
