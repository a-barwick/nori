package sim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/api"
	"nori/internal/connections"
	"nori/internal/conversation"
	"nori/internal/permissions"
	"nori/internal/secret"
	"nori/internal/serviceauth"
	"nori/internal/sim/env"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

type memoryAPIUserEnv struct {
	user             users.User
	db               *records.Database
	store            state.ConversationStore
	assistant        *api.Assistant
	consolidateCalls atomic.Int64
}

type memoryAPIWorld struct {
	clock     *env.Clock
	ids       *env.IDGen
	backend   *memstore.Backend
	artifacts *memstore.Artifacts
	directory *records.Users
	auth      serviceauth.Config
	users     map[string]*memoryAPIUserEnv
	alice     *memoryAPIUserEnv
	bob       *memoryAPIUserEnv
	handler   http.Handler
}

func newMemoryAPIWorld(t *testing.T, seed int64) *memoryAPIWorld {
	t.Helper()
	clock := env.NewClock(env.DefaultBaseTime)
	ids := env.NewIDGenFromSeed(uint64(seed))
	backend := memstore.New(memstore.WithClock(clock.Now))
	artifacts := memstore.NewArtifacts()
	directory := records.NewSharedUsers(backend)
	authCfg := serviceauth.Config{
		Key:      "0123456789abcdef0123456789abcdef",
		Audience: "nori-sim-memory",
	}

	w := &memoryAPIWorld{
		clock:     clock,
		ids:       ids,
		backend:   backend,
		artifacts: artifacts,
		directory: directory,
		auth:      authCfg,
		users:     map[string]*memoryAPIUserEnv{},
	}

	alice := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "alice-mem@example.com",
		DisplayName: "Alice",
		TimeZone:    "UTC",
	}
	bob := users.User{
		ID:          "22222222-2222-4222-8222-222222222222",
		Email:       "bob-mem@example.com",
		DisplayName: "Bob",
		TimeZone:    "America/Los_Angeles",
	}

	for _, u := range []users.User{alice, bob} {
		if err := directory.Create(context.Background(), u); err != nil {
			t.Fatalf("create user %s: %v", u.ID, err)
		}
		db := records.NewShared(backend, u, 5.0)
		db.Now = clock.Now
		store := state.Compose(db, prefixedArtifacts{files: artifacts, pngs: memstore.NewArtifacts(), prefix: u.FilePrefix()})
		conn := connections.New(store, secret.Unavailable{}, "http://127.0.0.1:3211/api/v1/connections/oauth/callback")
		conn.UserTimezone = u.TimeZone
		perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow", Now: clock.Now}
		uEnv := &memoryAPIUserEnv{
			user:  u,
			db:    db,
			store: store,
		}
		assistant := &api.Assistant{
			Store:       store,
			Connections: conn,
			Permissions: perms,
			Features: map[string]bool{
				"library": true, "search": true, "work": true, "permissions": true, "interactions": true,
			},
			ConsolidateMemory: func(ctx context.Context) (state.ConversationSummary, error) {
				uEnv.consolidateCalls.Add(1)
				cur, err := store.ConversationSummary(ctx)
				if err != nil {
					return state.ConversationSummary{}, err
				}
				from, err := store.ContextFrom(ctx)
				if err != nil {
					return state.ConversationSummary{}, err
				}
				now := clock.Now().UTC()
				next := cur
				next.From = from
				next.LastDreamAt = now
				next.DreamThrough = now
				if next.Through.Before(now) {
					next.Through = now
				}
				if next.FlushedThrough.Before(next.Through) {
					next.FlushedThrough = next.Through
				}
				next.UpdatedAt = now
				return store.CommitDreamConsolidate(ctx, state.DreamConsolidateRequest{
					ExpectedSummary: cur,
					NextSummary:     next,
				})
			},
		}
		uEnv.assistant = assistant
		w.users[u.ID] = uEnv
	}
	w.alice = w.users[alice.ID]
	w.bob = w.users[bob.ID]

	open := func(_ context.Context, u users.User) (api.UserRoutes, func(), error) {
		uEnv := w.users[u.ID]
		tasks := &conversation.Tasks{Store: uEnv.store, Now: clock.Now}
		runner := conversation.New(nil, nil, nil, tasks)
		runner.Now = clock.Now
		ops := &simOps{}
		h := api.Handler(api.Deps{
			Model:     "sim-model",
			Runner:    runner,
			Tasks:     tasks,
			Ops:       ops,
			Assistant: uEnv.assistant,
		})
		return api.UserRoutes{Handler: h, Resume: ops.Resume}, func() {}, nil
	}
	userHandler := api.UserHandler(directory, open, false, "sim-model")
	w.handler = authCfg.Protect(userHandler)
	return w
}

func (w *memoryAPIWorld) token(t *testing.T) string {
	t.Helper()
	tok, err := w.auth.Token()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return tok
}

func (w *memoryAPIWorld) doJSON(t *testing.T, userID, method, suffix string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body []byte
	var ct string
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		ct = "application/json"
	}
	req := httptest.NewRequest(method, "/users/"+userID+suffix, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+w.token(t))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, req)
	return rec
}

type sseCaptureWriter struct {
	mu           sync.Mutex
	header       http.Header
	status       int
	buf          bytes.Buffer
	targetSubstr string
	cancel       context.CancelFunc
	matched      bool
}

func newSSECaptureWriter(targetSubstr string, cancel context.CancelFunc) *sseCaptureWriter {
	return &sseCaptureWriter{
		header:       http.Header{},
		targetSubstr: targetSubstr,
		cancel:       cancel,
	}
}

func (w *sseCaptureWriter) Header() http.Header { return w.header }

func (w *sseCaptureWriter) WriteHeader(code int) {
	w.mu.Lock()
	if w.status == 0 {
		w.status = code
	}
	w.mu.Unlock()
}

func (w *sseCaptureWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.buf.Write(p)
	if !w.matched && w.targetSubstr != "" && strings.Contains(w.buf.String(), w.targetSubstr) {
		w.matched = true
		if w.cancel != nil {
			w.cancel()
		}
	}
	w.mu.Unlock()
	return n, err
}

func (w *sseCaptureWriter) Flush() {}

func (w *sseCaptureWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestDST_MemoryAPIIdempotencyReceiptsAndSearch(t *testing.T) {
	runScenario := func(seed int64) string {
		ctx := context.Background()
		w := newMemoryAPIWorld(t, seed)
		aliceID := w.alice.user.ID
		bobID := w.bob.user.ID

		decodeJSON := func(rec *httptest.ResponseRecorder, dst any) {
			t.Helper()
			if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
				t.Fatalf("decode response (%d %s): %v", rec.Code, rec.Body.String(), err)
			}
		}

		posBeforeCreate, err := w.alice.store.EventPosition(ctx)
		if err != nil {
			t.Fatalf("EventPosition before create: %v", err)
		}

		createReqID := w.ids.Next()
		createBody := map[string]any{
			"request_id":        createReqID,
			"expected_revision": 0,
			"value": map[string]any{
				"text":    "Prefers single-origin pour-over coffee in the morning (keyword: cobalt_roast)",
				"about":   "the user",
				"section": "Preferences",
			},
		}
		rec := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory", createBody)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST /v1/memory: status=%d body=%s", rec.Code, rec.Body.String())
		}
		var createReceipt state.MutationReceipt
		decodeJSON(rec, &createReceipt)
		if createReceipt.RequestID != createReqID || len(createReceipt.Resource) == 0 {
			t.Fatalf("unexpected create receipt: %+v", createReceipt)
		}
		var createdMem struct {
			ID         string `json:"id"`
			Revision   int64  `json:"revision"`
			Text       string `json:"text"`
			Section    string `json:"section"`
			Pinned     bool   `json:"pinned"`
			Provenance string `json:"provenance"`
		}
		if err := json.Unmarshal(createReceipt.Resource, &createdMem); err != nil || createdMem.ID == "" {
			t.Fatalf("unmarshal created memory: %v resource=%s", err, string(createReceipt.Resource))
		}
		if createdMem.Provenance != string(state.ProvenanceUserExplicit) {
			t.Fatalf("expected user_explicit provenance, got %q", createdMem.Provenance)
		}

		recLookup := w.doJSON(t, aliceID, http.MethodGet, "/v1/mutations/"+createReqID, nil)
		if recLookup.Code != http.StatusOK {
			t.Fatalf("GET /v1/mutations/%s: status=%d body=%s", createReqID, recLookup.Code, recLookup.Body.String())
		}
		var storedReceipt state.MutationReceipt
		decodeJSON(recLookup, &storedReceipt)
		if storedReceipt.RequestID != createReceipt.RequestID || string(storedReceipt.Resource) != string(createReceipt.Resource) {
			t.Fatalf("stored receipt mismatch: got=%+v want=%+v", storedReceipt, createReceipt)
		}

		w.clock.Advance(2 * time.Second)
		recRetry := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory", createBody)
		if recRetry.Code != http.StatusCreated {
			t.Fatalf("idempotent retry POST /v1/memory: status=%d body=%s", recRetry.Code, recRetry.Body.String())
		}
		var retryReceipt state.MutationReceipt
		decodeJSON(recRetry, &retryReceipt)
		if string(retryReceipt.Resource) != string(createReceipt.Resource) || retryReceipt.RequestID != createReceipt.RequestID {
			t.Fatalf("retry receipt diverged: got=%+v want=%+v", retryReceipt, createReceipt)
		}

		conflictBody := map[string]any{
			"request_id":        createReqID,
			"expected_revision": 0,
			"value": map[string]any{
				"text":    "Different payload for same request_id",
				"about":   "the user",
				"section": "Preferences",
			},
		}
		recConflict := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory", conflictBody)
		if recConflict.Code != http.StatusConflict {
			t.Fatalf("expected 409 idempotency_conflict, got %d body=%s", recConflict.Code, recConflict.Body.String())
		}
		var errResp struct {
			Code    string          `json:"code"`
			Current json.RawMessage `json:"current"`
		}
		decodeJSON(recConflict, &errResp)
		if errResp.Code != "idempotency_conflict" {
			t.Fatalf("expected code=idempotency_conflict, got %q", errResp.Code)
		}

		sseCtx, sseCancel := context.WithCancel(ctx)
		sseWriter := newSSECaptureWriter("event: memory\n", sseCancel)
		sseReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/users/%s/v1/events?after=%d", aliceID, posBeforeCreate), nil).WithContext(sseCtx)
		sseReq.Header.Set("Authorization", "Bearer "+w.token(t))
		w.handler.ServeHTTP(sseWriter, sseReq)
		if !strings.Contains(sseWriter.String(), "event: memory\n") {
			t.Fatalf("expected SSE stream to emit 'event: memory', got:\n%s", sseWriter.String())
		}

		w.clock.Advance(3 * time.Second)
		oversizedRec := w.doJSON(t, aliceID, http.MethodPut, "/v1/memory/"+createdMem.ID, map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": createdMem.Revision,
			"value": map[string]any{
				"text": strings.Repeat("a", state.MaxMemoryText+1),
			},
		})
		if oversizedRec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for >2000B edit, got %d body=%s", oversizedRec.Code, oversizedRec.Body.String())
		}

		editReqID := w.ids.Next()
		editText := "Prefers washed Ethiopian pour-over coffee (keyword: cobalt_roast) " + strings.Repeat("note ", 300)
		editRec := w.doJSON(t, aliceID, http.MethodPut, "/v1/memory/"+createdMem.ID, map[string]any{
			"request_id":        editReqID,
			"expected_revision": createdMem.Revision,
			"value": map[string]any{
				"text":    editText,
				"about":   "the user",
				"section": "Preferences",
			},
		})
		if editRec.Code != http.StatusOK {
			t.Fatalf("PUT /v1/memory/%s: status=%d body=%s", createdMem.ID, editRec.Code, editRec.Body.String())
		}
		var editReceipt state.MutationReceipt
		decodeJSON(editRec, &editReceipt)
		var editedMem struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
			Text     string `json:"text"`
		}
		if err := json.Unmarshal(editReceipt.Resource, &editedMem); err != nil {
			t.Fatalf("unmarshal edited memory: %v", err)
		}
		if editedMem.Text != editText || editedMem.Revision <= createdMem.Revision {
			t.Fatalf("unexpected edited memory: %+v", editedMem)
		}

		staleRec := w.doJSON(t, aliceID, http.MethodPut, "/v1/memory/"+createdMem.ID, map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": createdMem.Revision,
			"value": map[string]any{
				"text": "Stale overwrite attempt",
			},
		})
		if staleRec.Code != http.StatusConflict {
			t.Fatalf("expected 409 revision_conflict, got %d body=%s", staleRec.Code, staleRec.Body.String())
		}
		errResp = struct {
			Code    string          `json:"code"`
			Current json.RawMessage `json:"current"`
		}{}
		decodeJSON(staleRec, &errResp)
		if errResp.Code != "revision_conflict" || len(errResp.Current) == 0 {
			t.Fatalf("expected revision_conflict with current, got code=%q current=%s", errResp.Code, string(errResp.Current))
		}
		var currentView struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		if err := json.Unmarshal(errResp.Current, &currentView); err != nil || currentView.ID != createdMem.ID || currentView.Revision != editedMem.Revision {
			t.Fatalf("unexpected current in 409 revision_conflict: %s", string(errResp.Current))
		}

		runMemAction := func(memID string, rev int64, action string) state.MutationReceipt {
			t.Helper()
			w.clock.Advance(2 * time.Second)
			reqID := w.ids.Next()
			body := map[string]any{
				"request_id":        reqID,
				"expected_revision": rev,
				"value":             map[string]any{"action": action},
			}
			r := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory/"+memID+"/actions", body)
			if r.Code != http.StatusOK {
				t.Fatalf("POST /v1/memory/%s/actions (%s): status=%d body=%s", memID, action, r.Code, r.Body.String())
			}
			var receipt state.MutationReceipt
			decodeJSON(r, &receipt)
			lookup := w.doJSON(t, aliceID, http.MethodGet, "/v1/mutations/"+reqID, nil)
			if lookup.Code != http.StatusOK {
				t.Fatalf("GET /v1/mutations/%s (%s): status=%d", reqID, action, lookup.Code)
			}
			retry := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory/"+memID+"/actions", body)
			if retry.Code != http.StatusOK || retry.Body.String() != r.Body.String() {
				t.Fatalf("retry POST /v1/memory/%s/actions (%s) diverged: status=%d", memID, action, retry.Code)
			}
			return receipt
		}

		pinReceipt := runMemAction(createdMem.ID, editedMem.Revision, "pin")
		var pinnedView struct {
			Revision int64 `json:"revision"`
			Pinned   bool  `json:"pinned"`
		}
		if err := json.Unmarshal(pinReceipt.Resource, &pinnedView); err != nil || !pinnedView.Pinned {
			t.Fatalf("expected pinned=true after pin action, got %s", string(pinReceipt.Resource))
		}

		unpinReceipt := runMemAction(createdMem.ID, pinnedView.Revision, "unpin")
		var unpinnedView struct {
			Revision int64 `json:"revision"`
			Pinned   bool  `json:"pinned"`
		}
		if err := json.Unmarshal(unpinReceipt.Resource, &unpinnedView); err != nil || unpinnedView.Pinned {
			t.Fatalf("expected pinned=false after unpin action, got %s", string(unpinReceipt.Resource))
		}

		demoteReceipt := runMemAction(createdMem.ID, unpinnedView.Revision, "demote")
		var demotedPayload struct {
			Demoted  map[string]any `json:"demoted"`
			LogEntry struct {
				ID string `json:"id"`
			} `json:"log_entry"`
		}
		if err := json.Unmarshal(demoteReceipt.Resource, &demotedPayload); err != nil || demotedPayload.LogEntry.ID == "" {
			t.Fatalf("expected demoted log_entry after demote action, got %s", string(demoteReceipt.Resource))
		}

		w.clock.Advance(2 * time.Second)
		rec2 := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory", map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": 0,
			"value":             map[string]any{"text": "Temporary note to forget", "about": "the user"},
		})
		if rec2.Code != http.StatusCreated {
			t.Fatalf("create second memory: %d %s", rec2.Code, rec2.Body.String())
		}
		var receipt2 state.MutationReceipt
		decodeJSON(rec2, &receipt2)
		var mem2 struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		_ = json.Unmarshal(receipt2.Resource, &mem2)
		_ = runMemAction(mem2.ID, mem2.Revision, "forget")

		tPre := w.clock.Now().UTC().Truncate(time.Microsecond)
		todayDate := tPre.Format("2006-01-02")
		preInputID := w.ids.Next()
		if err := w.alice.store.AcceptConversationInput(ctx, preInputID, "cobalt_roast pre_cutoff_secret_token hidden message", nil); err != nil {
			t.Fatalf("seed pre-cutoff input: %v", err)
		}
		curSum, _ := w.alice.store.ConversationSummary(ctx)
		if _, err := w.alice.store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			ExpectedSummary: curSum,
			NextSummary: state.ConversationSummary{
				From:           time.Time{},
				FlushedThrough: tPre,
				Through:        tPre,
				Gist:           "Pre-cutoff summary",
				UpdatedAt:      tPre,
			},
			DailyLogDate: todayDate,
			DailyLogEntries: []state.MemoryLogEntry{{
				ID:            "pre-cutoff-log",
				Section:       "Context",
				Text:          "cobalt_roast pre_cutoff_secret_token hidden daily log",
				About:         "the user",
				Provenance:    state.ProvenancePreCompactFlush,
				SourceInputID: preInputID,
				CreatedAt:     tPre,
				UpdatedAt:     tPre,
			}},
		}); err != nil {
			t.Fatalf("seed pre-cutoff daily log: %v", err)
		}

		w.clock.Advance(1 * time.Hour)
		cutoff := w.clock.Now().UTC().Truncate(time.Microsecond)
		aliceControlPath := "owners/" + w.alice.user.DataOwner() + "/conversation/control"
		convCtrl, _, _ := w.alice.store.ConversationStatus(ctx)
		if err := w.backend.Apply(ctx, docstore.Mutation{
			Kind: "set",
			Path: aliceControlPath,
			Fields: map[string]any{
				"context_from":  cutoff,
				"received":      convCtrl.Received,
				"consumed":      convCtrl.Received,
				"user_received": convCtrl.UserReceived,
				"user_routed":   convCtrl.UserReceived,
			},
		}); err != nil {
			t.Fatalf("set alice context_from: %v", err)
		}

		w.clock.Advance(5 * time.Minute)
		tPost := w.clock.Now().UTC().Truncate(time.Microsecond)
		postInputID := w.ids.Next()
		if err := w.alice.store.AcceptConversationInput(ctx, postInputID, "cobalt_roast post_cutoff_visible_token schedule tasting", nil); err != nil {
			t.Fatalf("seed post-cutoff input: %v", err)
		}
		curSum, _ = w.alice.store.ConversationSummary(ctx)
		workingItem1 := state.SummaryItem{
			Kind:    "preference",
			Quote:   "cobalt_roast post_cutoff_visible_token schedule tasting",
			InputID: postInputID,
			Note:    "Promote this working item",
		}
		workingItem2 := state.SummaryItem{
			Kind:    "constraint",
			Quote:   "schedule tasting",
			InputID: postInputID,
			Note:    "Dismiss this working item",
		}
		if _, err := w.alice.store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			ExpectedSummary: curSum,
			NextSummary: state.ConversationSummary{
				From:           cutoff,
				FlushedThrough: tPost,
				Through:        tPost,
				Gist:           "Planning cobalt_roast tasting session",
				Items:          []state.SummaryItem{workingItem1, workingItem2},
				UpdatedAt:      tPost,
			},
			DailyLogDate: todayDate,
			DailyLogEntries: []state.MemoryLogEntry{
				{
					ID:            "post-user-log",
					Section:       "Decisions",
					Text:          "Post-cutoff decision about cobalt_roast post_cutoff_visible_token",
					About:         "the user",
					Provenance:    state.ProvenancePreCompactFlush,
					SourceInputID: postInputID,
					CreatedAt:     tPost,
					UpdatedAt:     tPost,
				},
				{
					ID:         "post-task-log",
					Section:    "Completed Work",
					Text:       "Task completed benchmark for cobalt_roast latency",
					About:      "the user",
					Provenance: state.ProvenanceTaskOutcome,
					RunID:      "run-bench-1",
					CreatedAt:  tPost,
					UpdatedAt:  tPost,
				},
				{
					ID:            "post-forget-log",
					Section:       "Context",
					Text:          "Transient note to forget from daily log",
					About:         "the user",
					Provenance:    state.ProvenancePreCompactFlush,
					SourceInputID: postInputID,
					CreatedAt:     tPost,
					UpdatedAt:     tPost,
				},
			},
		}); err != nil {
			t.Fatalf("seed post-cutoff daily log & working memory: %v", err)
		}

		logsRec := w.doJSON(t, aliceID, http.MethodGet, "/v1/memory/logs", nil)
		if logsRec.Code != http.StatusOK {
			t.Fatalf("GET /v1/memory/logs: %d %s", logsRec.Code, logsRec.Body.String())
		}
		if strings.Contains(logsRec.Body.String(), "pre_cutoff_secret_token") || strings.Contains(logsRec.Body.String(), "pre-cutoff-log") {
			t.Fatalf("GET /v1/memory/logs leaked pre-ContextFrom entry: %s", logsRec.Body.String())
		}

		dayRec := w.doJSON(t, aliceID, http.MethodGet, "/v1/memory/logs/"+todayDate, nil)
		if dayRec.Code != http.StatusOK {
			t.Fatalf("GET /v1/memory/logs/%s: %d %s", todayDate, dayRec.Code, dayRec.Body.String())
		}
		if strings.Contains(dayRec.Body.String(), "pre_cutoff_secret_token") {
			t.Fatalf("GET /v1/memory/logs/%s leaked pre-ContextFrom entry", todayDate)
		}
		var dayLog struct {
			Date     string `json:"date"`
			Revision int64  `json:"revision"`
			Entries  []struct {
				ID         string `json:"id"`
				Provenance string `json:"provenance"`
			} `json:"entries"`
		}
		decodeJSON(dayRec, &dayLog)
		if len(dayLog.Entries) != 3 {
			t.Fatalf("expected 3 post-cutoff log entries, got %d (%s)", len(dayLog.Entries), dayRec.Body.String())
		}

		w.clock.Advance(2 * time.Second)
		blockedPromote := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory/logs/"+todayDate+"/post-task-log/actions", map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": dayLog.Revision,
			"value":             map[string]any{"action": "promote"},
		})
		if blockedPromote.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 blocking raw task_outcome promotion, got %d body=%s", blockedPromote.Code, blockedPromote.Body.String())
		}

		w.clock.Advance(2 * time.Second)
		reauthoredPromote := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory/logs/"+todayDate+"/post-task-log/actions", map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": dayLog.Revision,
			"value": map[string]any{
				"action":  "promote",
				"text":    "User-confirmed benchmark baseline for cobalt_roast",
				"section": "Projects",
			},
		})
		if reauthoredPromote.Code != http.StatusOK {
			t.Fatalf("expected re-authored task_outcome promote to succeed, got %d body=%s", reauthoredPromote.Code, reauthoredPromote.Body.String())
		}
		var reauthReceipt state.MutationReceipt
		decodeJSON(reauthoredPromote, &reauthReceipt)
		var reauthMem struct {
			ID         string `json:"id"`
			Provenance string `json:"provenance"`
		}
		_ = json.Unmarshal(reauthReceipt.Resource, &reauthMem)
		if reauthMem.Provenance != string(state.ProvenanceUserExplicit) {
			t.Fatalf("expected promoted re-authored memory to have user_explicit provenance, got %q", reauthMem.Provenance)
		}

		w.clock.Advance(2 * time.Second)
		userPromote := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory/logs/"+todayDate+"/post-user-log/actions", map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": dayLog.Revision,
			"value":             map[string]any{"action": "promote"},
		})
		if userPromote.Code != http.StatusOK {
			t.Fatalf("expected user-sourced log promote to succeed, got %d body=%s", userPromote.Code, userPromote.Body.String())
		}

		w.clock.Advance(2 * time.Second)
		forgetLogReqID := w.ids.Next()
		forgetLogRec := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory/logs/"+todayDate+"/post-forget-log/actions", map[string]any{
			"request_id":        forgetLogReqID,
			"expected_revision": dayLog.Revision,
			"value":             map[string]any{"action": "forget"},
		})
		if forgetLogRec.Code != http.StatusOK {
			t.Fatalf("forget daily log entry: %d %s", forgetLogRec.Code, forgetLogRec.Body.String())
		}
		if r := w.doJSON(t, aliceID, http.MethodGet, "/v1/mutations/"+forgetLogReqID, nil); r.Code != http.StatusOK {
			t.Fatalf("GET /v1/mutations/%s (forget log): %d", forgetLogReqID, r.Code)
		}

		workingRec := w.doJSON(t, aliceID, http.MethodGet, "/v1/memory/working", nil)
		if workingRec.Code != http.StatusOK {
			t.Fatalf("GET /v1/memory/working: %d %s", workingRec.Code, workingRec.Body.String())
		}
		var workingView struct {
			Revision        int64 `json:"revision"`
			CoreBudgetBytes int   `json:"core_budget_bytes"`
			Items           []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		decodeJSON(workingRec, &workingView)
		if workingView.CoreBudgetBytes != state.CoreMemoryBytes || len(workingView.Items) != 2 {
			t.Fatalf("unexpected working memory view: %s", workingRec.Body.String())
		}

		w.clock.Advance(2 * time.Second)
		promoteWorkingRec := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory/working/"+workingView.Items[0].ID+"/actions", map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": workingView.Revision,
			"value":             map[string]any{"action": "promote"},
		})
		if promoteWorkingRec.Code != http.StatusOK {
			t.Fatalf("promote working item: %d %s", promoteWorkingRec.Code, promoteWorkingRec.Body.String())
		}

		w.clock.Advance(2 * time.Second)
		dismissWorkingRec := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory/working/"+workingView.Items[1].ID+"/actions", map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": workingView.Revision,
			"value":             map[string]any{"action": "dismiss"},
		})
		if dismissWorkingRec.Code != http.StatusOK {
			t.Fatalf("dismiss working item: %d %s", dismissWorkingRec.Code, dismissWorkingRec.Body.String())
		}

		w.clock.Advance(2 * time.Second)
		consolidateReqID := w.ids.Next()
		consolidateBody := map[string]any{
			"request_id":        consolidateReqID,
			"expected_revision": 0,
			"value":             map[string]any{"action": "consolidate"},
		}
		consRec1 := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory/actions", consolidateBody)
		if consRec1.Code != http.StatusOK {
			t.Fatalf("POST /v1/memory/actions consolidate: %d %s", consRec1.Code, consRec1.Body.String())
		}
		if got := w.alice.consolidateCalls.Load(); got != 1 {
			t.Fatalf("expected 1 ConsolidateMemory call, got %d", got)
		}
		consRetry := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory/actions", consolidateBody)
		if consRetry.Code != http.StatusOK || consRetry.Body.String() != consRec1.Body.String() {
			t.Fatalf("consolidate retry diverged: %d %s", consRetry.Code, consRetry.Body.String())
		}
		if got := w.alice.consolidateCalls.Load(); got != 1 {
			t.Fatalf("idempotent consolidate retry re-ran ConsolidateMemory: calls=%d", got)
		}
		if lookup := w.doJSON(t, aliceID, http.MethodGet, "/v1/mutations/"+consolidateReqID, nil); lookup.Code != http.StatusOK {
			t.Fatalf("GET /v1/mutations/%s (consolidate): %d", consolidateReqID, lookup.Code)
		}

		w.clock.Advance(2 * time.Second)
		bobCreate := w.doJSON(t, bobID, http.MethodPost, "/v1/memory", map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": 0,
			"value": map[string]any{
				"text":    "Bob private cobalt_roast bob_only_secret_data",
				"about":   "the user",
				"section": "Private",
			},
		})
		if bobCreate.Code != http.StatusCreated {
			t.Fatalf("create bob memory: %d %s", bobCreate.Code, bobCreate.Body.String())
		}

		bobCrossEdit := w.doJSON(t, bobID, http.MethodPut, "/v1/memory/"+reauthMem.ID, map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": 0,
			"value":             map[string]any{"text": "cross tenant tamper"},
		})
		if bobCrossEdit.Code != http.StatusNotFound {
			t.Fatalf("expected 404 on cross-owner memory edit, got %d body=%s", bobCrossEdit.Code, bobCrossEdit.Body.String())
		}

		memSearchRec := w.doJSON(t, aliceID, http.MethodGet, "/v1/memory/search?q=cobalt_roast&limit=20", nil)
		if memSearchRec.Code != http.StatusOK {
			t.Fatalf("GET /v1/memory/search: %d %s", memSearchRec.Code, memSearchRec.Body.String())
		}
		if strings.Contains(memSearchRec.Body.String(), "pre_cutoff_secret_token") {
			t.Fatalf("GET /v1/memory/search leaked pre-ContextFrom entry: %s", memSearchRec.Body.String())
		}
		if strings.Contains(memSearchRec.Body.String(), "bob_only_secret_data") {
			t.Fatalf("GET /v1/memory/search leaked Bob's memory: %s", memSearchRec.Body.String())
		}
		var memSearchResp struct {
			Items []struct {
				ChunkID string `json:"chunk_id"`
				Layer   string `json:"layer"`
			} `json:"items"`
		}
		decodeJSON(memSearchRec, &memSearchResp)
		if len(memSearchResp.Items) == 0 {
			t.Fatalf("expected non-empty memory search hits for cobalt_roast")
		}

		globalSearchRec := w.doJSON(t, aliceID, http.MethodGet, "/v1/search?q=cobalt_roast", nil)
		if globalSearchRec.Code != http.StatusOK {
			t.Fatalf("GET /v1/search: %d %s", globalSearchRec.Code, globalSearchRec.Body.String())
		}
		if strings.Contains(globalSearchRec.Body.String(), "pre_cutoff_secret_token") {
			t.Fatalf("GET /v1/search leaked pre-ContextFrom data: %s", globalSearchRec.Body.String())
		}
		if strings.Contains(globalSearchRec.Body.String(), "bob_only_secret_data") {
			t.Fatalf("GET /v1/search leaked Bob's data: %s", globalSearchRec.Body.String())
		}
		var globalSearchResp struct {
			Items []struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
				Jump *struct {
					Kind string `json:"kind"`
				} `json:"jump"`
			} `json:"items"`
		}
		decodeJSON(globalSearchRec, &globalSearchResp)
		foundMemJump := false
		for _, item := range globalSearchResp.Items {
			if item.Kind == "memory" && item.Jump != nil && item.Jump.Kind == "memory" {
				foundMemJump = true
				break
			}
		}
		if !foundMemJump {
			t.Fatalf("expected GET /v1/search to return at least one memory hit with jump.kind==memory, got %s", globalSearchRec.Body.String())
		}

		w.clock.Advance(2 * time.Second)
		pinnedTrue := true
		pinnedCreate := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory", map[string]any{
			"request_id":        w.ids.Next(),
			"expected_revision": 0,
			"value": map[string]any{
				"text":    "PINNED_SENTINEL: " + strings.Repeat("important_pinned_anchor ", 70),
				"about":   "the user",
				"section": "Core",
				"pinned":  pinnedTrue,
			},
		})
		if pinnedCreate.Code != http.StatusCreated {
			t.Fatalf("create pinned sentinel: %d %s", pinnedCreate.Code, pinnedCreate.Body.String())
		}
		var pinnedReceipt state.MutationReceipt
		decodeJSON(pinnedCreate, &pinnedReceipt)
		var pinnedSentinel struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(pinnedReceipt.Resource, &pinnedSentinel)

		for i := range 16 {
			w.clock.Advance(time.Second)
			text := fmt.Sprintf("overflow_unpinned_%02d: ", i) + strings.Repeat("bulk_memory_content ", 90)
			if len(text) > 1900 {
				text = text[:1900]
			}
			r := w.doJSON(t, aliceID, http.MethodPost, "/v1/memory", map[string]any{
				"request_id":        w.ids.Next(),
				"expected_revision": 0,
				"value": map[string]any{
					"text":    text,
					"about":   "the user",
					"section": "Bulk",
				},
			})
			if r.Code != http.StatusCreated {
				t.Fatalf("overflow create %d failed: %d %s", i, r.Code, r.Body.String())
			}
		}

		listRec := w.doJSON(t, aliceID, http.MethodGet, "/v1/memory", nil)
		if listRec.Code != http.StatusOK {
			t.Fatalf("GET /v1/memory after overflow: %d %s", listRec.Code, listRec.Body.String())
		}
		var coreList struct {
			Items []struct {
				ID     string `json:"id"`
				Text   string `json:"text"`
				Pinned bool   `json:"pinned"`
			} `json:"items"`
		}
		decodeJSON(listRec, &coreList)
		totalBytes := 0
		pinnedSurvived := false
		for _, item := range coreList.Items {
			totalBytes += len(item.Text)
			if item.ID == pinnedSentinel.ID && item.Pinned {
				pinnedSurvived = true
			}
		}
		if totalBytes > state.CoreMemoryBytes {
			t.Fatalf("INV-MEM-03 violation: Core Memory bytes=%d exceeds %d", totalBytes, state.CoreMemoryBytes)
		}
		if !pinnedSurvived {
			t.Fatalf("INV-MEM-03 violation: pinned sentinel %s was evicted during Core overflow!", pinnedSentinel.ID)
		}

		demotedSearch := w.doJSON(t, aliceID, http.MethodGet, "/v1/memory/search?q=overflow_unpinned_00&layer=log", nil)
		if demotedSearch.Code != http.StatusOK {
			t.Fatalf("search demoted item: %d %s", demotedSearch.Code, demotedSearch.Body.String())
		}
		if !strings.Contains(demotedSearch.Body.String(), "overflow_unpinned_00") {
			t.Fatalf("expected auto-demoted overflow_unpinned_00 to be searchable in Daily Log, got %s", demotedSearch.Body.String())
		}

		bobList := w.doJSON(t, bobID, http.MethodGet, "/v1/memory", nil)
		if bobList.Code != http.StatusOK || !strings.Contains(bobList.Body.String(), "bob_only_secret_data") {
			t.Fatalf("Bob's isolated memory check failed: %d %s", bobList.Code, bobList.Body.String())
		}

		return w.backend.Digest()
	}

	d1 := runScenario(808)
	d2 := runScenario(808)
	if d1 != d2 {
		t.Fatalf("non-deterministic backend digest across 2 runs: %s vs %s", d1, d2)
	}
}

var _ io.Writer = (*sseCaptureWriter)(nil)
