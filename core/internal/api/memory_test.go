package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

type stubMemoryAPIStore struct {
	state.ConversationStore
	now         time.Time
	contextFrom time.Time
	memories    []state.Memory
	logs        map[string]state.MemoryLog
	summary     state.ConversationSummary
	messages    []state.Message
	searchHits  []docstore.MemorySearchHit
	receipts    map[string]state.MutationReceipt
	lastMutReq  state.MemoryMutationRequest
	lastSearch  docstore.MemorySearchQuery
}

func newStubMemoryAPIStore(now time.Time) *stubMemoryAPIStore {
	return &stubMemoryAPIStore{
		now:      now,
		logs:     map[string]state.MemoryLog{},
		receipts: map[string]state.MutationReceipt{},
	}
}

func (s *stubMemoryAPIStore) Memories(context.Context) ([]state.Memory, error) {
	return append([]state.Memory(nil), s.memories...), nil
}

func (s *stubMemoryAPIStore) MemoryLogs(context.Context, string, int) ([]state.MemoryLog, error) {
	out := make([]state.MemoryLog, 0, len(s.logs))
	for _, l := range s.logs {
		out = append(out, l)
	}
	return out, nil
}

func (s *stubMemoryAPIStore) MemoryLog(_ context.Context, date string) (state.MemoryLog, error) {
	l, ok := s.logs[date]
	if !ok {
		return state.MemoryLog{}, state.ErrNotFound
	}
	return l, nil
}

func (s *stubMemoryAPIStore) SearchMemory(_ context.Context, q docstore.MemorySearchQuery) ([]docstore.MemorySearchHit, error) {
	s.lastSearch = q
	return append([]docstore.MemorySearchHit(nil), s.searchHits...), nil
}

func (s *stubMemoryAPIStore) ContextFrom(context.Context) (time.Time, error) {
	return s.contextFrom, nil
}

func (s *stubMemoryAPIStore) ConversationSummary(context.Context) (state.ConversationSummary, error) {
	return s.summary, nil
}

func (s *stubMemoryAPIStore) ConversationMessages(_ context.Context, before string) ([]state.Message, string, error) {
	if before != "" {
		return nil, "", nil
	}
	return append([]state.Message(nil), s.messages...), "", nil
}

func (s *stubMemoryAPIStore) RecentResults(context.Context, int) ([]state.Result, error) {
	return nil, nil
}

func (s *stubMemoryAPIStore) RecentRuns(context.Context, int) ([]state.Run, error) {
	return nil, nil
}

func (s *stubMemoryAPIStore) Resources(context.Context, string, map[string]string, string, int) ([]state.Resource, string, error) {
	return nil, "", nil
}

func (s *stubMemoryAPIStore) Receipt(_ context.Context, requestID string) (state.MutationReceipt, error) {
	r, ok := s.receipts[requestID]
	if !ok {
		return state.MutationReceipt{}, state.ErrNotFound
	}
	return r, nil
}

func (s *stubMemoryAPIStore) SaveMemoryActionReceipt(_ context.Context, requestID, binding string, resource json.RawMessage) (state.MutationReceipt, error) {
	if existing, ok := s.receipts[requestID]; ok {
		if existing.Binding != binding {
			return state.MutationReceipt{}, state.ErrIdempotencyConflict
		}
		return existing, nil
	}
	r := state.MutationReceipt{
		RequestID:  requestID,
		Binding:    binding,
		Accepted:   true,
		Status:     "applied",
		Resource:   resource,
		HTTPStatus: http.StatusOK,
	}
	s.receipts[requestID] = r
	return r, nil
}

func (s *stubMemoryAPIStore) MutateMemory(_ context.Context, req state.MemoryMutationRequest) (state.MutationReceipt, error) {
	s.lastMutReq = req
	binding, _ := state.MutationBinding(state.Mutation{
		Method:           req.Kind,
		Target:           req.Date + "/" + req.TargetID,
		ExpectedRevision: req.ExpectedRevision,
		Payload:          map[string]any{"text": req.Text, "about": req.About, "section": req.Section, "pinned": req.Pinned},
	})
	if existing, ok := s.receipts[req.RequestID]; ok {
		if existing.Binding != binding {
			return state.MutationReceipt{}, state.ErrIdempotencyConflict
		}
		return existing, nil
	}
	var res state.MemoryMutationResult
	status := http.StatusOK
	switch req.Kind {
	case state.MemoryMutateCreateCore:
		status = http.StatusCreated
		pinned := req.Pinned != nil && *req.Pinned
		m := state.Memory{
			ID:            "mem_" + req.RequestID[:8],
			Text:          req.Text,
			About:         req.About,
			Section:       req.Section,
			Pinned:        pinned,
			Provenance:    state.ProvenanceUserExplicit,
			SourceInputID: "ui",
			CreatedAt:     s.now,
			UpdatedAt:     s.now,
		}
		s.memories = append(s.memories, m)
		res.Memory = &m
	case state.MemoryMutateEditCore:
		idx := -1
		for i, m := range s.memories {
			if m.ID == req.TargetID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return state.MutationReceipt{}, state.ErrNotFound
		}
		if s.memories[idx].UpdatedAt.UnixMilli() != req.ExpectedRevision {
			return state.MutationReceipt{}, state.ErrRevisionConflict
		}
		s.now = s.now.Add(time.Second)
		m := s.memories[idx]
		m.Text = req.Text
		m.About = req.About
		m.Section = req.Section
		if req.Pinned != nil {
			m.Pinned = *req.Pinned
		}
		m.UpdatedAt = s.now
		s.memories[idx] = m
		res.Memory = &m
	case state.MemoryMutatePinCore, state.MemoryMutateUnpinCore:
		idx := -1
		for i, m := range s.memories {
			if m.ID == req.TargetID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return state.MutationReceipt{}, state.ErrNotFound
		}
		if s.memories[idx].UpdatedAt.UnixMilli() != req.ExpectedRevision {
			return state.MutationReceipt{}, state.ErrRevisionConflict
		}
		s.now = s.now.Add(time.Second)
		m := s.memories[idx]
		m.Pinned = req.Kind == state.MemoryMutatePinCore
		m.UpdatedAt = s.now
		s.memories[idx] = m
		res.Memory = &m
	case state.MemoryMutateDemoteCore:
		idx := -1
		for i, m := range s.memories {
			if m.ID == req.TargetID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return state.MutationReceipt{}, state.ErrNotFound
		}
		m := s.memories[idx]
		s.memories = append(s.memories[:idx], s.memories[idx+1:]...)
		entry := state.MemoryLogEntry{
			ID:         "demote_" + m.ID,
			Text:       m.Text,
			About:      m.About,
			Section:    m.Section,
			Provenance: m.EffectiveProvenance(),
			CreatedAt:  s.now,
			UpdatedAt:  s.now,
		}
		res.Memory = &m
		res.LogEntry = &entry
	case state.MemoryMutateForgetCore:
		idx := -1
		for i, m := range s.memories {
			if m.ID == req.TargetID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return state.MutationReceipt{}, state.ErrNotFound
		}
		m := s.memories[idx]
		s.memories = append(s.memories[:idx], s.memories[idx+1:]...)
		res.Memory = &m
	case state.MemoryMutatePromoteLog, state.MemoryMutatePromoteWorking:
		m := state.Memory{
			ID:            "mem_promoted",
			Text:          req.Text,
			About:         req.About,
			Section:       req.Section,
			Provenance:    state.ProvenanceUserExplicit,
			SourceInputID: "ui",
			CreatedAt:     s.now,
			UpdatedAt:     s.now,
		}
		if m.Text == "" {
			m.Text = "Promoted fact"
		}
		s.memories = append(s.memories, m)
		res.Memory = &m
	case state.MemoryMutateForgetLog:
		entry := state.MemoryLogEntry{ID: req.TargetID, Text: "Removed entry", Provenance: state.ProvenancePreCompactFlush, CreatedAt: s.now}
		res.LogEntry = &entry
	case state.MemoryMutateDismissWorking:
		s.summary.Items = nil
		s.summary.UpdatedAt = s.now.Add(time.Second)
		sum := s.summary
		res.Summary = &sum
		res.CoreBytes = state.CoreMemorySize(s.memories)
		res.ContextFrom = s.contextFrom
	}
	raw, err := req.FormatResource(res)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	r := state.MutationReceipt{
		RequestID:  req.RequestID,
		Binding:    binding,
		Accepted:   true,
		Status:     "applied",
		Resource:   raw,
		HTTPStatus: status,
	}
	s.receipts[req.RequestID] = r
	return r, nil
}

func newTestMemoryMux(a *Assistant) *http.ServeMux {
	mux := http.NewServeMux()
	a.register(mux)
	return mux
}

func doJSONReq(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMemoryAPILifecycleAndConsolidate(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	store := newStubMemoryAPIStore(now)
	store.contextFrom = cutoff
	store.summary = state.ConversationSummary{
		From:           cutoff,
		FlushedThrough: now.Add(-time.Hour),
		Through:        now.Add(-time.Hour),
		DreamThrough:   now.Add(-2 * time.Hour),
		LastDreamAt:    now.Add(-2 * time.Hour),
		Gist:           "Working on memory migration.",
		Turns:          12,
		Model:          "gpt-5",
		Items:          []state.SummaryItem{{Kind: "decision", Quote: "Keep 24 KiB core budget.", InputID: "in_1"}},
		UpdatedAt:      now.Add(-time.Hour),
	}
	store.logs["2026-11-23"] = state.MemoryLog{
		Date: "2026-11-23",
		Entries: []state.MemoryLogEntry{
			{ID: "pre_entry", Text: "Before rewind", Provenance: state.ProvenancePreCompactFlush, CreatedAt: cutoff.Add(-time.Minute)},
			{ID: "post_entry", Text: "After rewind", Provenance: state.ProvenancePreCompactFlush, CreatedAt: now},
		},
		UpdatedAt: now,
	}
	store.logs["2026-11-19"] = state.MemoryLog{
		Date: "2026-11-19",
		Entries: []state.MemoryLogEntry{
			{ID: "all_old", Text: "Old day", Provenance: state.ProvenancePreCompactFlush, CreatedAt: cutoff.Add(-24 * time.Hour)},
		},
		UpdatedAt: cutoff.Add(-24 * time.Hour),
	}
	store.messages = []state.Message{
		{ID: "msg-old", Role: "user", Text: "Old zephyr message before rewind", CreatedAt: cutoff.Add(-time.Minute)},
		{ID: "msg-new", Role: "user", Text: "New zephyr message after rewind", CreatedAt: now},
	}

	consolidateCalls := 0
	assistant := &Assistant{Store: store}
	mux := newTestMemoryMux(assistant)

	createID := uuid.NewString()
	createBody := map[string]any{
		"request_id":        createID,
		"expected_revision": 0,
		"value":             map[string]any{"text": "Always use UTC for zephyr reports.", "section": "Preferences", "about": "the user"},
	}
	rec := doJSONReq(t, mux, "POST", "/v1/memory", createBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /v1/memory status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var createReceipt state.MutationReceipt
	if err := json.Unmarshal(rec.Body.Bytes(), &createReceipt); err != nil {
		t.Fatalf("decode create receipt: %v", err)
	}
	var createdFact memoryFact
	if err := json.Unmarshal(createReceipt.Resource, &createdFact); err != nil {
		t.Fatalf("decode created fact: %v", err)
	}
	if createdFact.ID == "" || createdFact.Section != "Preferences" {
		t.Fatalf("unexpected created fact: %+v", createdFact)
	}

	rec = doJSONReq(t, mux, "GET", "/v1/mutations/"+createID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/mutations/%s status = %d: %s", createID, rec.Code, rec.Body.String())
	}

	conflictBody := map[string]any{
		"request_id":        createID,
		"expected_revision": 0,
		"value":             map[string]any{"text": "Conflicting payload"},
	}
	rec = doJSONReq(t, mux, "POST", "/v1/memory", conflictBody)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"idempotency_conflict"`) || strings.Contains(rec.Body.String(), `"current"`) {
		t.Fatalf("expected 409 idempotency_conflict without current, got %d: %s", rec.Code, rec.Body.String())
	}

	editID := uuid.NewString()
	rec = doJSONReq(t, mux, "PUT", "/v1/memory/"+createdFact.ID, map[string]any{
		"request_id":        editID,
		"expected_revision": createdFact.Revision,
		"value":             map[string]any{"text": "Always use ISO-8601 UTC for zephyr reports.", "section": "Preferences", "about": "the user"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /v1/memory/%s status = %d: %s", createdFact.ID, rec.Code, rec.Body.String())
	}
	var editReceipt state.MutationReceipt
	_ = json.Unmarshal(rec.Body.Bytes(), &editReceipt)
	var editedFact memoryFact
	_ = json.Unmarshal(editReceipt.Resource, &editedFact)

	rec = doJSONReq(t, mux, "PUT", "/v1/memory/"+createdFact.ID, map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": createdFact.Revision,
		"value":             map[string]any{"text": "Stale write"},
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"revision_conflict"`) || !strings.Contains(rec.Body.String(), `"current"`) {
		t.Fatalf("expected 409 revision_conflict with current, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSONReq(t, mux, "POST", "/v1/memory/"+createdFact.ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": editedFact.Revision,
		"value":             map[string]any{"action": "pin"},
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pinned":true`) {
		t.Fatalf("pin failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONReq(t, mux, "POST", "/v1/memory/"+createdFact.ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": editedFact.Revision,
		"value":             map[string]any{"action": "consolidate"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for consolidate on item route, got %d", rec.Code)
	}

	consReqID := uuid.NewString()
	consBody := map[string]any{
		"request_id":        consReqID,
		"expected_revision": 0,
		"value":             map[string]any{"action": "consolidate"},
	}
	rec = doJSONReq(t, mux, "POST", "/v1/memory/actions", consBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when ConsolidateMemory is nil, got %d: %s", rec.Code, rec.Body.String())
	}
	assistant.ConsolidateMemory = func(context.Context) (state.ConversationSummary, error) {
		consolidateCalls++
		return store.summary, nil
	}
	rec = doJSONReq(t, mux, "POST", "/v1/memory/actions", consBody)
	if rec.Code != http.StatusOK || consolidateCalls != 1 {
		t.Fatalf("consolidate failed: status=%d calls=%d body=%s", rec.Code, consolidateCalls, rec.Body.String())
	}
	firstConsolidateBody := rec.Body.String()
	rec = doJSONReq(t, mux, "POST", "/v1/memory/actions", consBody)
	if rec.Code != http.StatusOK || consolidateCalls != 1 || rec.Body.String() != firstConsolidateBody {
		t.Fatalf("consolidate retry failed: status=%d calls=%d", rec.Code, consolidateCalls)
	}

	rec = doJSONReq(t, mux, "GET", "/v1/memory/logs", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/memory/logs status = %d: %s", rec.Code, rec.Body.String())
	}
	var logsResp list[memoryLogView]
	_ = json.Unmarshal(rec.Body.Bytes(), &logsResp)
	if len(logsResp.Items) != 1 || logsResp.Items[0].Date != "2026-11-23" || len(logsResp.Items[0].Entries) != 1 || logsResp.Items[0].Entries[0].ID != "post_entry" {
		t.Fatalf("expected only post-ContextFrom log entries, got %+v", logsResp.Items)
	}
	rec = doJSONReq(t, mux, "GET", "/v1/memory/logs/2026-11-19", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for pre-ContextFrom log date, got %d", rec.Code)
	}
	rec = doJSONReq(t, mux, "GET", "/v1/memory/logs/bad-date", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid log date, got %d", rec.Code)
	}

	rec = doJSONReq(t, mux, "POST", "/v1/memory/logs/2026-11-23/post_entry/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": now.UnixMilli(),
		"value":             map[string]any{"action": "promote", "text": "Promoted note"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("promote log entry failed: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONReq(t, mux, "POST", "/v1/memory/logs/2026-11-23/post_entry/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": now.UnixMilli(),
		"value":             map[string]any{"action": "forget", "text": "invalid extra"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for forget with extra text field, got %d", rec.Code)
	}

	rec = doJSONReq(t, mux, "GET", "/v1/memory/working", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/memory/working failed: %d %s", rec.Code, rec.Body.String())
	}
	var working workingMemoryView
	_ = json.Unmarshal(rec.Body.Bytes(), &working)
	if working.CoreBudgetBytes != state.CoreMemoryBytes || len(working.Items) != 1 || working.Items[0].ID == "" {
		t.Fatalf("unexpected working memory view: %+v", working)
	}
	rec = doJSONReq(t, mux, "POST", "/v1/memory/working/"+working.Items[0].ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": working.Revision,
		"value":             map[string]any{"action": "dismiss"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("dismiss working item failed: %d %s", rec.Code, rec.Body.String())
	}

	store.searchHits = []docstore.MemorySearchHit{
		{
			Chunk: docstore.MemoryChunk{
				ChunkID:    "core:" + createdFact.ID,
				Layer:      docstore.MemoryLayerCore,
				Section:    "Preferences",
				Body:       "Always use ISO-8601 UTC for zephyr reports.",
				Provenance: string(state.ProvenanceUserExplicit),
				CreatedAt:  now,
				UpdatedAt:  now,
			},
			Snippet: "Always use ISO-8601 UTC for zephyr reports.",
			Score:   1.2,
		},
		{
			Chunk: docstore.MemoryChunk{
				ChunkID:    "log:2026-11-19:all_old",
				Layer:      docstore.MemoryLayerLog,
				LogDate:    "2026-11-19",
				Body:       "Old zephyr entry",
				Provenance: string(state.ProvenancePreCompactFlush),
				CreatedAt:  cutoff.Add(-time.Hour),
			},
			Snippet: "Old zephyr entry",
			Score:   1.5,
		},
	}
	rec = doJSONReq(t, mux, "GET", "/v1/memory/search?q=zephyr&layer=core&limit=5", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/memory/search failed: %d %s", rec.Code, rec.Body.String())
	}
	var memSearchResp memorySearchResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &memSearchResp)
	if len(memSearchResp.Items) != 1 || memSearchResp.Items[0].MemoryID != createdFact.ID {
		t.Fatalf("unexpected memory search response: %+v", memSearchResp)
	}
	if !store.lastSearch.CreatedAfter.Equal(cutoff) || store.lastSearch.Layer != "core" || store.lastSearch.Limit != 5 {
		t.Fatalf("unexpected SearchMemory query params: %+v", store.lastSearch)
	}

	rec = doJSONReq(t, mux, "GET", "/v1/search?q=zephyr", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v1/search failed: %d %s", rec.Code, rec.Body.String())
	}
	var globalResp searchResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &globalResp)
	sawMemory, sawNewMsg, sawOldMsg := false, false, false
	for _, h := range globalResp.Items {
		switch h.Kind {
		case "memory":
			if h.Jump != nil && h.Jump.Kind == "memory" && h.Jump.Layer == "core" && h.Jump.MemoryID == createdFact.ID {
				sawMemory = true
			}
		case "message":
			if h.ID == "msg-new" {
				sawNewMsg = true
			}
			if h.ID == "msg-old" {
				sawOldMsg = true
			}
		}
	}
	if !sawMemory || !sawNewMsg || sawOldMsg {
		t.Fatalf("global search ContextFrom/memory mismatch: sawMemory=%v sawNewMsg=%v sawOldMsg=%v items=%+v", sawMemory, sawNewMsg, sawOldMsg, globalResp.Items)
	}
}

func TestWorkingMemoryExtractedThroughView(t *testing.T) {
	now := time.Date(2026, 11, 23, 14, 0, 0, 0, time.UTC)
	cutoff := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

	t.Run("session-idle extraction before compaction exposes extracted_through with omitted through", func(t *testing.T) {
		extractedAt := now.Add(-25 * time.Minute)
		updatedAt := now.Add(-5 * time.Minute)
		store := newStubMemoryAPIStore(now)
		store.contextFrom = cutoff
		store.summary = state.ConversationSummary{
			From:             cutoff,
			ExtractedThrough: extractedAt,
			UpdatedAt:        updatedAt,
		}
		mux := newTestMemoryMux(&Assistant{Store: store})

		rec := doJSONReq(t, mux, "GET", "/v1/memory/working", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/memory/working status = %d: %s", rec.Code, rec.Body.String())
		}
		var raw map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("decode raw json: %v", err)
		}
		if _, hasThrough := raw["through"]; hasThrough {
			t.Fatalf("expected through to be omitted before compaction, got %s", rec.Body.String())
		}
		if _, hasFlushed := raw["flushed_through"]; hasFlushed {
			t.Fatalf("expected flushed_through to be omitted before compaction, got %s", rec.Body.String())
		}
		var view workingMemoryView
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode workingMemoryView: %v", err)
		}
		if view.ExtractedThrough == nil || !view.ExtractedThrough.Equal(extractedAt) {
			t.Fatalf("extracted_through = %v, want %v", view.ExtractedThrough, extractedAt)
		}
		if view.From == nil || !view.From.Equal(cutoff) {
			t.Fatalf("from = %v, want %v", view.From, cutoff)
		}
		if view.Revision != updatedAt.UnixMilli() {
			t.Fatalf("revision = %d, want %d", view.Revision, updatedAt.UnixMilli())
		}
		if view.UpdatedAt == nil || !view.UpdatedAt.Equal(updatedAt) {
			t.Fatalf("updated_at = %v, want %v", view.UpdatedAt, updatedAt)
		}
		if view.Through != nil || view.FlushedThrough != nil {
			t.Fatalf("expected nil through/flushed_through, got through=%v flushed=%v", view.Through, view.FlushedThrough)
		}
	})

	t.Run("session-idle extraction in zero contextFrom epoch and rewind isolation", func(t *testing.T) {
		extractedAt := now.Add(-20 * time.Minute)
		dreamAt := now.Add(-10 * time.Minute)
		store := newStubMemoryAPIStore(now)
		store.summary = state.ConversationSummary{
			ExtractedThrough: extractedAt,
			DreamThrough:     extractedAt,
			LastDreamAt:      dreamAt,
			UpdatedAt:        dreamAt,
		}
		mux := newTestMemoryMux(&Assistant{Store: store})

		rec := doJSONReq(t, mux, "GET", "/v1/memory/working", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/memory/working status = %d: %s", rec.Code, rec.Body.String())
		}
		var view workingMemoryView
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode workingMemoryView: %v", err)
		}
		if view.ExtractedThrough == nil || !view.ExtractedThrough.Equal(extractedAt) {
			t.Fatalf("extracted_through = %v, want %v", view.ExtractedThrough, extractedAt)
		}
		if view.LastDreamAt == nil || !view.LastDreamAt.Equal(dreamAt) {
			t.Fatalf("last_dream_at = %v, want %v", view.LastDreamAt, dreamAt)
		}

		store.contextFrom = now
		rec = doJSONReq(t, mux, "GET", "/v1/memory/working", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/memory/working after rewind status = %d: %s", rec.Code, rec.Body.String())
		}
		var rewound workingMemoryView
		if err := json.Unmarshal(rec.Body.Bytes(), &rewound); err != nil {
			t.Fatalf("decode rewound workingMemoryView: %v", err)
		}
		if rewound.ExtractedThrough != nil || rewound.LastDreamAt != nil || rewound.Revision != 0 {
			t.Fatalf("expected empty view after rewind, got %+v", rewound)
		}
	})

	t.Run("coupled compaction sets extracted_through >= flushed_through", func(t *testing.T) {
		flushedAt := now.Add(-2 * time.Hour)
		extractedAhead := now.Add(-20 * time.Minute)
		store := newStubMemoryAPIStore(now)
		store.contextFrom = cutoff
		store.summary = state.ConversationSummary{
			From:           cutoff,
			FlushedThrough: flushedAt,
			Through:        flushedAt,
			Gist:           "Compacted summary.",
			Turns:          6,
			Model:          "gpt-5",
			UpdatedAt:      flushedAt,
		}
		mux := newTestMemoryMux(&Assistant{Store: store})

		rec := doJSONReq(t, mux, "GET", "/v1/memory/working", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/memory/working status = %d: %s", rec.Code, rec.Body.String())
		}
		var view workingMemoryView
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode workingMemoryView: %v", err)
		}
		if view.FlushedThrough == nil || !view.FlushedThrough.Equal(flushedAt) {
			t.Fatalf("flushed_through = %v, want %v", view.FlushedThrough, flushedAt)
		}
		if view.ExtractedThrough == nil || !view.ExtractedThrough.Equal(flushedAt) {
			t.Fatalf("extracted_through = %v, want >= flushed_through (%v)", view.ExtractedThrough, flushedAt)
		}

		store.summary.ExtractedThrough = extractedAhead
		store.summary.UpdatedAt = now
		rec = doJSONReq(t, mux, "GET", "/v1/memory/working", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /v1/memory/working ahead status = %d: %s", rec.Code, rec.Body.String())
		}
		var ahead workingMemoryView
		if err := json.Unmarshal(rec.Body.Bytes(), &ahead); err != nil {
			t.Fatalf("decode ahead workingMemoryView: %v", err)
		}
		if ahead.ExtractedThrough == nil || !ahead.ExtractedThrough.Equal(extractedAhead) {
			t.Fatalf("extracted_through = %v, want %v", ahead.ExtractedThrough, extractedAhead)
		}
		if ahead.FlushedThrough == nil || !ahead.FlushedThrough.Equal(flushedAt) || !ahead.ExtractedThrough.After(*ahead.FlushedThrough) {
			t.Fatalf("expected extracted_through (%v) > flushed_through (%v)", ahead.ExtractedThrough, ahead.FlushedThrough)
		}
	})
}

func TestSendEventMemory(t *testing.T) {
	rec := httptest.NewRecorder()
	stream := prepareEventStream(rec, "no-cache")
	s := &server{}
	if err := s.sendEvent(context.Background(), stream, state.LogEvent{Seq: 42, Kind: state.EventMemory, Ref: "core:mem_1"}); err != nil {
		t.Fatalf("sendEvent EventMemory: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: memory\n") || !strings.Contains(body, "id: 42\n") || !strings.Contains(body, `"ref":"core:mem_1"`) {
		t.Fatalf("unexpected SSE output for EventMemory: %q", body)
	}
}
