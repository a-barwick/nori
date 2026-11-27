package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"nori/internal/connections"
	"nori/internal/connectors/mcp"
	"nori/internal/conversation"
	"nori/internal/extensions"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/secret/rowstore"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

// countingBackend wraps a docstore.Backend and counts every DB operation
// (both direct calls and calls made inside RunTransaction).
type countingBackend struct {
	inner       docstore.Backend
	gets        atomic.Int64
	queries     atomic.Int64
	applies     atomic.Int64
	deleteBatch atomic.Int64
}

var (
	_ docstore.Backend          = (*countingBackend)(nil)
	_ docstore.BackendUnwrapper = (*countingBackend)(nil)
)

type dbOpCounts struct {
	Gets        int64
	Queries     int64
	Applies     int64
	DeleteBatch int64
}

func (c dbOpCounts) Total() int64 {
	return c.Gets + c.Queries + c.Applies + c.DeleteBatch
}

func (b *countingBackend) Unwrap() docstore.Backend { return b.inner }

func (b *countingBackend) Reset() {
	b.gets.Store(0)
	b.queries.Store(0)
	b.applies.Store(0)
	b.deleteBatch.Store(0)
}

func (b *countingBackend) Snapshot() dbOpCounts {
	return dbOpCounts{
		Gets:        b.gets.Load(),
		Queries:     b.queries.Load(),
		Applies:     b.applies.Load(),
		DeleteBatch: b.deleteBatch.Load(),
	}
}

func (b *countingBackend) Get(ctx context.Context, path string) (docstore.Record, error) {
	b.gets.Add(1)
	return b.inner.Get(ctx, path)
}

func (b *countingBackend) Query(ctx context.Context, spec docstore.QuerySpec) ([]docstore.Record, error) {
	b.queries.Add(1)
	return b.inner.Query(ctx, spec)
}

func (b *countingBackend) Apply(ctx context.Context, mut docstore.Mutation) error {
	b.applies.Add(1)
	return b.inner.Apply(ctx, mut)
}

func (b *countingBackend) DeleteBatch(ctx context.Context, paths []string) error {
	b.deleteBatch.Add(1)
	return b.inner.DeleteBatch(ctx, paths)
}

func (b *countingBackend) Watch(ctx context.Context, spec docstore.QuerySpec, fn func([]docstore.Record) error) error {
	return b.inner.Watch(ctx, spec, fn)
}

func (b *countingBackend) Close() error {
	return b.inner.Close()
}

func (b *countingBackend) RunTransaction(ctx context.Context, readOnly bool, attempts int, fn func(docstore.TransactionBackend) error) error {
	return b.inner.RunTransaction(ctx, readOnly, attempts, func(tx docstore.TransactionBackend) error {
		return fn(&countingTxBackend{inner: tx, parent: b})
	})
}

func (b *countingBackend) CurrentSequence(ctx context.Context, path string) (int64, error) {
	if sr, ok := b.inner.(docstore.SequenceReader); ok {
		return sr.CurrentSequence(ctx, path)
	}
	return 0, nil
}

type countingTxBackend struct {
	inner  docstore.TransactionBackend
	parent *countingBackend
}

func (t *countingTxBackend) Get(ctx context.Context, path string) (docstore.Record, error) {
	t.parent.gets.Add(1)
	return t.inner.Get(ctx, path)
}

func (t *countingTxBackend) Query(ctx context.Context, spec docstore.QuerySpec) ([]docstore.Record, error) {
	t.parent.queries.Add(1)
	return t.inner.Query(ctx, spec)
}

func (t *countingTxBackend) Apply(ctx context.Context, mut docstore.Mutation) error {
	t.parent.applies.Add(1)
	return t.inner.Apply(ctx, mut)
}

// countingKeyWrapper wraps a rowstore.AADKeyWrapper and counts KMS unwrap calls.
type countingKeyWrapper struct {
	inner   rowstore.AADKeyWrapper
	unwraps atomic.Int64
}

var _ rowstore.AADKeyWrapper = (*countingKeyWrapper)(nil)

func (w *countingKeyWrapper) Reset()       { w.unwraps.Store(0) }
func (w *countingKeyWrapper) Count() int64 { return w.unwraps.Load() }

func (w *countingKeyWrapper) Wrap(ctx context.Context, dataKey []byte) ([]byte, string, error) {
	return w.inner.Wrap(ctx, dataKey)
}

func (w *countingKeyWrapper) WrapWithAAD(ctx context.Context, dataKey, aad []byte) ([]byte, string, error) {
	return w.inner.WrapWithAAD(ctx, dataKey, aad)
}

func (w *countingKeyWrapper) Unwrap(ctx context.Context, wrapped []byte, keyID string) ([]byte, error) {
	w.unwraps.Add(1)
	return w.inner.Unwrap(ctx, wrapped, keyID)
}

func (w *countingKeyWrapper) UnwrapWithAAD(ctx context.Context, wrapped []byte, keyID string, aad []byte) ([]byte, error) {
	w.unwraps.Add(1)
	return w.inner.UnwrapWithAAD(ctx, wrapped, keyID, aad)
}

func deterministicUUID(label string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("perf-baseline:"+label)).String()
}

type perfMeasurement struct {
	Endpoint   string
	DBTotal    int64
	DBGets     int64
	DBQueries  int64
	DBApplies  int64
	KMSUnwraps int64
	MedianMS   float64
	P95MS      float64
}

func percentileDuration(sorted []time.Duration, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return float64(sorted[idx].Microseconds()) / 1000.0
}

func TestPerfBaseline(t *testing.T) {
	ctx := context.Background()
	baseTime := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	cb := &countingBackend{inner: memstore.New()}
	masterKey, err := rowstore.ParseMasterKey("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	ckw := &countingKeyWrapper{inner: masterKey}

	u := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "perf@example.com",
		DisplayName: "Perf User",
		TimeZone:    "UTC",
	}

	db := records.NewShared(cb, u, 5.0)
	curTime := baseTime
	db.Now = func() time.Time { return curTime }
	store := state.Compose(db, memstore.NewArtifacts())

	credStore := &rowstore.Store{
		Resources: store,
		Keys:      ckw,
		Owner:     u.DataOwner(),
		Now:       func() time.Time { return curTime },
	}
	conn := connections.New(store, credStore, "http://127.0.0.1:3211/api/v1/connections/oauth/callback")
	perms := &permissions.Service{
		Store:            store,
		UnattendedWrites: "confirm_writes",
		UserSends:        "allow",
	}
	sched := &scheduler.Service{
		Store:       store,
		Tasks:       store,
		Permissions: perms,
		Now:         func() time.Time { return curTime },
	}
	mcpSvc := &mcp.Service{
		Store:       store,
		Credentials: credStore,
		Now:         func() time.Time { return curTime },
	}
	modelKeysSvc := &modelkeys.Service{
		Store: credStore,
		Now:   func() time.Time { return curTime },
		HTTPClient: &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
					Header:     http.Header{},
				}, nil
			}),
		},
	}
	modelsSvc := &modelprofiles.Account{
		Keys:  modelKeysSvc,
		Store: store,
	}

	tasks := &conversation.Tasks{Store: store}
	runner := conversation.New(nil, nil, nil, tasks)

	// 1. Seed 65 conversation turns (130 messages) so history pagination with ?before=... returns a full page.
	for i := 0; i < 65; i++ {
		curTime = baseTime.Add(time.Duration(i) * time.Minute)
		inputID := deterministicUUID(fmt.Sprintf("conv-input-%03d", i))
		if err := store.AcceptConversationInput(ctx, inputID, fmt.Sprintf("User message %d", i), nil); err != nil {
			t.Fatalf("AcceptConversationInput %d: %v", i, err)
		}
		turnID := deterministicUUID(fmt.Sprintf("conv-turn-%03d", i))
		ctrl, turn, err := store.AcquireConversation(ctx, "perf-worker", turnID)
		if err != nil {
			t.Fatalf("AcquireConversation %d: %v", i, err)
		}
		if turn == nil {
			t.Fatalf("AcquireConversation %d returned nil turn", i)
		}
		fence := state.ConversationFence{
			Owner:       "perf-worker",
			Fence:       ctrl.Fence,
			TurnID:      turn.ID,
			Through:     turn.Through,
			UserThrough: turn.UserThrough,
			Frozen:      turn.Frozen,
		}
		curTime = curTime.Add(5 * time.Second)
		turn.Attempt.ID = deterministicUUID(fmt.Sprintf("conv-attempt-%03d", i))
		turn.Attempt.TurnID = turn.ID
		turn.Attempt.Number = 1
		turn.Attempt.Status = state.AttemptCompleted
		turn.Attempt.StartedAt = curTime
		if err := store.PublishConversation(ctx, fence, *turn, fmt.Sprintf("Assistant reply %d", i)); err != nil {
			t.Fatalf("PublishConversation %d: %v", i, err)
		}
	}

	// 2. Seed 50 finished runs + 3 running runs, each with 3 operations.
	finishedRunIDs := make([]string, 0, 50)
	for i := 0; i < 53; i++ {
		curTime = baseTime.Add(time.Duration(200+i) * time.Minute)
		runID := deterministicUUID(fmt.Sprintf("run-%03d", i))
		startCmd := state.Command{
			ID:        runID,
			RunID:     runID,
			Kind:      state.CommandStart,
			Text:      fmt.Sprintf("Analyze quarterly report #%d", i+1),
			CreatedAt: curTime,
		}
		if _, err := store.AcceptCommandBatch(ctx, []state.Command{startCmd}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch run %d: %v", i, err)
		}
		if err := store.Incorporate(ctx, state.IncorporateRequest{RunID: runID, Revision: 1, Position: 1}); err != nil {
			t.Fatalf("Incorporate run %d: %v", i, err)
		}
		for opIdx := 0; opIdx < 3; opIdx++ {
			opTime := curTime.Add(time.Duration(opIdx+1) * time.Second)
			op := state.OperationRecord{
				ID:          fmt.Sprintf("op-%03d-%d", i, opIdx),
				CallID:      fmt.Sprintf("call-%03d-%d", i, opIdx),
				Name:        "web_search",
				EffectClass: "read",
				Outcome:     "confirmed",
				Revision:    1,
				Attempt:     1,
				CreatedAt:   &opTime,
				Action: &state.Action{
					Digest: fmt.Sprintf("digest-%03d-%d", i, opIdx),
					Text:   fmt.Sprintf("Searched docs for item %d step %d", i+1, opIdx+1),
					Spec:   map[string]string{"query": fmt.Sprintf("q-%d-%d", i, opIdx)},
				},
			}
			if err := store.PutOperation(ctx, runID, op); err != nil {
				t.Fatalf("PutOperation run %d op %d: %v", i, opIdx, err)
			}
		}
		if i < 50 {
			curTime = curTime.Add(30 * time.Second)
			if _, err := store.Publish(ctx, state.PublishRequest{
				RunID:         runID,
				Revision:      1,
				Status:        state.RunCompleted,
				Text:          fmt.Sprintf("Completed analysis #%d.", i+1),
				BypassRouting: true,
			}); err != nil {
				t.Fatalf("Publish run %d: %v", i, err)
			}
			finishedRunIDs = append(finishedRunIDs, runID)
		} else if i == 50 {
			// Make 1 of the 3 running runs wait on a pending question.
			q := state.Question{
				ID:       runID + "-q1",
				Kind:     "input",
				Text:     "Should I include Q4 projections?",
				CallID:   "call-q1",
				Revision: 1,
			}
			if err := store.ProjectQuestion(ctx, runID, 1, q); err != nil {
				t.Fatalf("ProjectQuestion run %d: %v", i, err)
			}
		}
	}

	// 3. Seed 60 results across the finished runs.
	for i := 0; i < 60; i++ {
		curTime = baseTime.Add(time.Duration(300+i) * time.Minute)
		runID := finishedRunIDs[i%len(finishedRunIDs)]
		resID := deterministicUUID(fmt.Sprintf("result-%03d", i))
		res := state.Result{
			ID:        resID,
			RootID:    resID,
			Version:   1,
			RunID:     runID,
			Revision:  1,
			Kind:      state.ResultAnswer,
			Title:     fmt.Sprintf("Report Summary #%d", i+1),
			Text:      fmt.Sprintf("# Report Summary #%d\n\nKey metrics and findings for item %d.", i+1, i+1),
			CreatedAt: curTime,
		}
		if _, err := store.SaveResult(ctx, res); err != nil {
			t.Fatalf("SaveResult %d: %v", i, err)
		}
	}

	// 4. Seed 5 automations, each with 5 occurrences (and 1 delivery per occurrence).
	var firstAutomationID string
	for aIdx := 0; aIdx < 5; aIdx++ {
		curTime = baseTime.Add(time.Duration(400+aIdx) * time.Minute)
		autoID := fmt.Sprintf("auto_perf_%02d", aIdx+1)
		if firstAutomationID == "" {
			firstAutomationID = autoID
		}
		var lastOccID string
		for oIdx := 0; oIdx < 5; oIdx++ {
			occID := fmt.Sprintf("occ_perf_%02d_%02d", aIdx+1, oIdx+1)
			delID := fmt.Sprintf("del_perf_%02d_%02d", aIdx+1, oIdx+1)
			lastOccID = occID
			schedAt := baseTime.Add(time.Duration(aIdx*24+oIdx) * time.Hour)
			finAt := schedAt.Add(2 * time.Minute)

			del := scheduler.Delivery{
				ID:            delID,
				OccurrenceID:  occID,
				DestinationID: scheduler.InAppDestination,
				State:         "delivered",
				CreatedAt:     &schedAt,
				UpdatedAt:     &finAt,
			}
			delRes, err := state.NewResource(scheduler.CollectionDeliveries, delID, 1, del, map[string]string{
				"occurrence_id": occID,
				"state":         "delivered",
			}, finAt)
			if err != nil {
				t.Fatalf("NewResource delivery %s: %v", delID, err)
			}
			if _, err := store.PutResource(ctx, delRes, 0); err != nil {
				t.Fatalf("PutResource delivery %s: %v", delID, err)
			}

			occPayload := map[string]any{
				"id":                  occID,
				"automation_id":       autoID,
				"automation_revision": 1,
				"generation":          1,
				"scheduled_at":        schedAt,
				"status":              scheduler.OccurrenceCompleted,
				"task_ids":            []string{finishedRunIDs[(aIdx*5+oIdx)%len(finishedRunIDs)]},
				"delivery_ids":        []string{delID},
				"started_at":          schedAt,
				"finished_at":         finAt,
				"summary":             fmt.Sprintf("Completed automation %d occurrence %d", aIdx+1, oIdx+1),
				"instruction":         fmt.Sprintf("Run daily check #%d", aIdx+1),
				"budget_usd":          1.0,
			}
			occRes, err := state.NewResource(scheduler.CollectionOccurrences, occID, 1, occPayload, map[string]string{
				"automation_id": autoID,
				"status":        string(scheduler.OccurrenceCompleted),
			}, finAt)
			if err != nil {
				t.Fatalf("NewResource occurrence %s: %v", occID, err)
			}
			if _, err := store.PutResource(ctx, occRes, 0); err != nil {
				t.Fatalf("PutResource occurrence %s: %v", occID, err)
			}
		}

		nextAt := baseTime.Add(time.Duration(24*(aIdx+1)) * time.Hour)
		autoPayload := map[string]any{
			"id":                       autoID,
			"revision":                 1,
			"title":                    fmt.Sprintf("Daily Briefing #%d", aIdx+1),
			"instruction":              fmt.Sprintf("Compile briefing #%d", aIdx+1),
			"status":                   scheduler.AutomationEnabled,
			"trigger":                  scheduler.Trigger{Kind: "recurring", LocalStart: "09:00", Timezone: "UTC", RRule: "FREQ=DAILY"},
			"connection_ids":           []string{},
			"authority_rule_ids":       []string{},
			"budget_usd":               1.0,
			"max_active_runs":          1,
			"missed_run":               "skip",
			"execution_window_seconds": 3600,
			"destination_ids":          []string{scheduler.InAppDestination},
			"next_at":                  nextAt,
			"last_occurrence_id":       lastOccID,
			"generation":               1,
		}
		autoRes, err := state.NewResource(scheduler.CollectionAutomations, autoID, 1, autoPayload, map[string]string{
			"status": string(scheduler.AutomationEnabled),
		}, curTime)
		if err != nil {
			t.Fatalf("NewResource automation %s: %v", autoID, err)
		}
		if _, err := store.PutResource(ctx, autoRes, 0); err != nil {
			t.Fatalf("PutResource automation %s: %v", autoID, err)
		}
	}

	// 5. Seed 3 MCP servers (auth=configured with real rowstore credentials), each with 5 tools.
	var firstMCPServerID string
	for sIdx := 0; sIdx < 3; sIdx++ {
		curTime = baseTime.Add(time.Duration(500+sIdx) * time.Minute)
		reqID := deterministicUUID(fmt.Sprintf("mcp-create-%02d", sIdx))
		receipt, err := mcpSvc.Save(ctx, "", reqID, 0, mcp.Editable{
			Name:      fmt.Sprintf("Perf MCP Server %d", sIdx+1),
			Transport: "http",
			Endpoint:  fmt.Sprintf("https://mcp-%d.example.com/sse", sIdx+1),
			Auth:      "configured",
			Enabled:   true,
			Exposure:  mcp.Exposure{Default: "direct"},
		})
		if err != nil {
			t.Fatalf("mcpSvc.Save %d: %v", sIdx, err)
		}
		var saved mcp.Server
		if err := json.Unmarshal(receipt.Resource, &saved); err != nil {
			t.Fatalf("unmarshal mcp server %d: %v", sIdx, err)
		}
		if firstMCPServerID == "" {
			firstMCPServerID = saved.ID
		}
		if err := mcpSvc.SetCredential(ctx, saved.ID, fmt.Sprintf("mcp_tok_secret_%d_1234567890", sIdx+1)); err != nil {
			t.Fatalf("mcpSvc.SetCredential %d: %v", sIdx, err)
		}

		// Attach 5 discovered tools to the server resource and write their definition docs.
		rawRes, err := store.Resource(ctx, mcp.CollectionServers, saved.ID)
		if err != nil {
			t.Fatalf("Resource mcp server %s: %v", saved.ID, err)
		}
		var serverDoc map[string]any
		if err := rawRes.Decode(&serverDoc); err != nil {
			t.Fatalf("Decode mcp server %s: %v", saved.ID, err)
		}
		toolRefs := map[string]string{}
		for tIdx := 0; tIdx < 5; tIdx++ {
			toolName := fmt.Sprintf("tool_%d_%d", sIdx+1, tIdx+1)
			defKey := fmt.Sprintf("%s__%s", saved.ID, toolName)
			toolRefs[toolName] = defKey
			defPayload := map[string]any{
				"server_id":      saved.ID,
				"tool_name":      toolName,
				"read_only_hint": true,
				"definition": extensions.Definition{
					Ref: extensions.DefinitionRef{
						ID:      "mcp:" + defKey,
						Version: mcp.Version,
						Digest:  fmt.Sprintf("sha256:perf-%d-%d", sIdx+1, tIdx+1),
					},
					Title:       fmt.Sprintf("Tool %d.%d", sIdx+1, tIdx+1),
					Description: fmt.Sprintf("Synthetic MCP tool %d on server %d", tIdx+1, sIdx+1),
					Exposure:    extensions.ExposureDirect,
				},
			}
			defRes, err := state.NewResource(mcp.CollectionDefinitions, defKey, 1, defPayload, map[string]string{
				"server_id": saved.ID,
			}, curTime)
			if err != nil {
				t.Fatalf("NewResource mcp def %s: %v", defKey, err)
			}
			if _, err := store.PutResource(ctx, defRes, 0); err != nil {
				t.Fatalf("PutResource mcp def %s: %v", defKey, err)
			}
		}
		serverDoc["tools"] = toolRefs
		if obs, ok := serverDoc["observed"].(map[string]any); ok {
			obs["status"] = "connected"
			obs["tool_count"] = 5
		}
		updatedRes, err := state.NewResource(mcp.CollectionServers, saved.ID, rawRes.Revision+1, serverDoc, rawRes.Index, curTime)
		if err != nil {
			t.Fatalf("NewResource updated mcp server %s: %v", saved.ID, err)
		}
		if _, err := store.PutResource(ctx, updatedRes, rawRes.Revision); err != nil {
			t.Fatalf("PutResource updated mcp server %s: %v", saved.ID, err)
		}
	}

	// 6. Seed 3 configured model keys + initialize model selection.
	for _, kv := range [][2]string{
		{modelkeys.ProviderOpenAI, "sk-perf-openai-key-1234567890"},
		{modelkeys.ProviderAnthropic, "sk-ant-perf-key-1234567890"},
		{modelkeys.ProviderGoogle, "AIzaPerfGoogleKey1234567890"},
	} {
		if _, err := modelKeysSvc.Set(ctx, kv[0], kv[1]); err != nil {
			t.Fatalf("modelKeysSvc.Set %s: %v", kv[0], err)
		}
	}
	if err := modelsSvc.EnsureSelection(ctx); err != nil {
		t.Fatalf("modelsSvc.EnsureSelection: %v", err)
	}

	handler := Handler(Deps{
		Runner: runner,
		Tasks:  tasks,
		Assistant: &Assistant{
			Store:            store,
			Connections:      conn,
			Permissions:      perms,
			Scheduler:        sched,
			MCP:              mcpSvc,
			ModelKeys:        modelKeysSvc,
			Models:           modelsSvc,
			AccountBYOK:      true,
			UnattendedWrites: "confirm_writes",
			NextScheduled:    sched.NextScheduled,
			InboxUnread:      sched.UnreadCount,
			Features: map[string]bool{
				"work":         true,
				"interactions": true,
				"automations":  true,
				"mcp":          true,
			},
		},
	})

	// Fetch the initial conversation snapshot once to obtain a real history cursor.
	snapRec := httptest.NewRecorder()
	handler.ServeHTTP(snapRec, httptest.NewRequest(http.MethodGet, "/v1/conversations/assistant", nil))
	if snapRec.Code != http.StatusOK {
		t.Fatalf("GET /v1/conversations/assistant status=%d body=%s", snapRec.Code, snapRec.Body.String())
	}
	var snapView struct {
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal(snapRec.Body.Bytes(), &snapView); err != nil {
		t.Fatalf("decode conversation snapshot: %v", err)
	}
	if snapView.NextCursor == "" {
		t.Fatalf("expected non-empty nextCursor after seeding 130 messages")
	}

	type endpointCase struct {
		label string
		path  string
	}
	cases := []endpointCase{
		{label: "GET /v1/assistant/summary", path: "/v1/assistant/summary"},
		{label: "GET /v1/work", path: "/v1/work"},
		{label: "GET /v1/operations", path: "/v1/operations"},
		{label: "GET /v1/library", path: "/v1/library"},
		{label: "GET /v1/conversations/assistant/messages?before=...", path: "/v1/conversations/assistant/messages?before=" + snapView.NextCursor},
		{label: "GET /v1/automations", path: "/v1/automations"},
		{label: "GET /v1/automations/{id}/occurrences", path: "/v1/automations/" + firstAutomationID + "/occurrences"},
		{label: "GET /v1/settings/model-keys", path: "/v1/settings/model-keys"},
		{label: "GET /v1/settings/models", path: "/v1/settings/models"},
		{label: "GET /v1/mcp", path: "/v1/mcp"},
		{label: "GET /v1/mcp/{id}/tools", path: "/v1/mcp/" + firstMCPServerID + "/tools"},
	}

	const iterations = 25
	results := make([]perfMeasurement, 0, len(cases))

	for _, tc := range cases {
		// Warmup request + verify HTTP 200.
		warmRec := httptest.NewRecorder()
		handler.ServeHTTP(warmRec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if warmRec.Code != http.StatusOK {
			t.Fatalf("%s warmup status=%d body=%s", tc.label, warmRec.Code, warmRec.Body.String())
		}

		// Measure exact DB ops and KMS unwraps on a single isolated request.
		cb.Reset()
		ckw.Reset()
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", tc.label, rec.Code, rec.Body.String())
		}
		counts := cb.Snapshot()
		unwraps := ckw.Count()

		// Measure latency distribution across N iterations.
		durations := make([]time.Duration, 0, iterations)
		for i := 0; i < iterations; i++ {
			start := time.Now()
			r := httptest.NewRecorder()
			handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, tc.path, nil))
			elapsed := time.Since(start)
			if r.Code != http.StatusOK {
				t.Fatalf("%s iter %d status=%d", tc.label, i, r.Code)
			}
			durations = append(durations, elapsed)
		}
		slices.Sort(durations)

		results = append(results, perfMeasurement{
			Endpoint:   tc.label,
			DBTotal:    counts.Total(),
			DBGets:     counts.Gets,
			DBQueries:  counts.Queries,
			DBApplies:  counts.Applies,
			KMSUnwraps: unwraps,
			MedianMS:   percentileDuration(durations, 0.50),
			P95MS:      percentileDuration(durations, 0.95),
		})
	}

	var sb strings.Builder
	sb.WriteString("\n=== CORE PERF BASELINE (BEFORE) ===\n")
	sb.WriteString(fmt.Sprintf("%-54s | %7s | %5s | %7s | %7s | %11s | %10s | %10s\n",
		"Endpoint", "DB Ops", "Gets", "Queries", "Applies", "KMS Unwraps", "Median ms", "p95 ms"))
	sb.WriteString(strings.Repeat("-", 128) + "\n")
	for _, m := range results {
		sb.WriteString(fmt.Sprintf("%-54s | %7d | %5d | %7d | %7d | %11d | %10.3f | %10.3f\n",
			m.Endpoint, m.DBTotal, m.DBGets, m.DBQueries, m.DBApplies, m.KMSUnwraps, m.MedianMS, m.P95MS))
	}
	t.Log(sb.String())
}
