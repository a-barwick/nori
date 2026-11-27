package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/google/uuid"

	"nori/internal/computer"
	"nori/internal/computer/fake"
	"nori/internal/connections"
	"nori/internal/connectors/mcp"
	"nori/internal/conversation"
	"nori/internal/extensions"
	"nori/internal/homeai"
	"nori/internal/homeaiwire"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/secret"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func openAPIPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		candidate := filepath.Join(dir, "api", name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("api/%s not found from working directory", name)
		}
		dir = parent
	}
}

func loadOpenAPIDoc(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(openAPIPath(t, "openapi.yaml"))
	if err != nil {
		t.Fatalf("load api/openapi.yaml: %v", err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("validate api/openapi.yaml: %v", err)
	}
	return doc
}

func TestOpenAPISpecValidAndInSyncWithJSON(t *testing.T) {
	doc := loadOpenAPIDoc(t)

	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi version = %q, want 3.1.0", doc.OpenAPI)
	}

	// Ensure backend-only internal schemas are not leaked into the public HTTP spec.
	for _, forbidden := range []string{"Invocation", "PreparedAction", "AdapterResult"} {
		if _, exists := doc.Components.Schemas[forbidden]; exists {
			t.Fatalf("private backend schema %q must not appear in api/openapi.yaml", forbidden)
		}
	}

	// Ensure api/openapi.json stays in sync with api/openapi.yaml for zero-dependency Swift tests.
	yamlDocBytes, err := doc.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal yaml doc to json: %v", err)
	}
	jsonLoader := openapi3.NewLoader()
	jsonDoc, err := jsonLoader.LoadFromFile(openAPIPath(t, "openapi.json"))
	if err != nil {
		t.Fatalf("load api/openapi.json: %v", err)
	}
	if err := jsonDoc.Validate(context.Background()); err != nil {
		t.Fatalf("validate api/openapi.json: %v", err)
	}
	jsonDocBytes, err := jsonDoc.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal json doc: %v", err)
	}
	var yamlObj, jsonObj any
	if err := json.Unmarshal(yamlDocBytes, &yamlObj); err != nil {
		t.Fatalf("unmarshal yaml json: %v", err)
	}
	if err := json.Unmarshal(jsonDocBytes, &jsonObj); err != nil {
		t.Fatalf("unmarshal json mirror: %v", err)
	}
	if !reflect.DeepEqual(yamlObj, jsonObj) {
		t.Fatalf("api/openapi.json is out of sync with api/openapi.yaml; run `make api-types`")
	}
}

func TestOpenAPIHandlersMatchSpec(t *testing.T) {
	ctx := context.Background()
	doc := loadOpenAPIDoc(t)
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		t.Fatalf("create OpenAPI router: %v", err)
	}

	backend := memstore.New()
	fixedNow := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	u := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "alice@example.com",
		DisplayName: "Alice",
		TimeZone:    "America/Los_Angeles",
	}
	db := records.NewShared(backend, u, 5.0)
	db.Now = func() time.Time { return fixedNow }
	store := state.Compose(db, memstore.NewArtifacts())
	tasks := &conversation.Tasks{Store: store}
	replyModel := &stubReplyModel{
		fn: func(context.Context, conversation.ReplyRequest) (conversation.Reply, error) {
			return conversation.Reply{Text: "All set."}, nil
		},
	}
	runner := conversation.New(replyModel, nil, nil, tasks)
	runner.Now = func() time.Time { return fixedNow }

	conn := connections.New(store, secret.Unavailable{}, "http://127.0.0.1:3211/api/v1/connections/oauth/callback")
	perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow", Now: func() time.Time { return fixedNow }}
	sched := &scheduler.Service{
		Store:       store,
		Tasks:       store,
		Permissions: perms,
		Timezone:    func(context.Context) string { return "America/Los_Angeles" },
		Now:         func() time.Time { return fixedNow },
	}
	mcpSvc := &mcp.Service{Store: store}
	keyStore := newMemModelKeyStore()
	modelKeysSvc := &modelkeys.Service{
		Store: keyStore,
		Now:   func() time.Time { return fixedNow },
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewBufferString(`{"data":[]}`)),
			}, nil
		})},
	}
	modelsSvc := &modelprofiles.Account{
		Keys:  modelKeysSvc,
		Store: store,
		HomeProfiles: func(context.Context) ([]modelprofiles.Profile, error) {
			return []modelprofiles.Profile{{
				ID:           "home-mac-llama",
				Version:      "v1",
				Label:        "Llama 4 on Mac Studio",
				Backend:      "home",
				Model:        "llama4:latest",
				Lanes:        []string{modelprofiles.LaneChat, modelprofiles.LaneTask},
				Paid:         false,
				HomeDeviceID: "dev_mac_studio",
				Availability: "online",
			}}, nil
		},
	}

	assistant := &Assistant{
		Store:       store,
		Connections: conn,
		Permissions: perms,
		Scheduler:   sched,
		MCP:         mcpSvc,
		ModelKeys:   modelKeysSvc,
		Models:      modelsSvc,
		AccountBYOK: true,
		UsageNow:    func() time.Time { return fixedNow },
		Features: map[string]bool{
			"integrations": true, "connections": true, "mcp": true,
			"interactions": true, "work": true, "automations": true,
			"library": true, "search": true,
			"permissions": true, "preferences": true, "memory": true,
			"inbox": true, "operations": true,
		},
		Tools: func(context.Context) (*extensions.Snapshot, []extensions.CapabilityState) {
			return &extensions.Snapshot{}, nil
		},
		Answer: func(context.Context, string, string, string, string) error { return nil },
	}
	handler := Handler(Deps{
		Model:         "gpt-6.1-terra",
		ModelProvider: "openai",
		Runner:        runner,
		Tasks:         tasks,
		Assistant:     assistant,
	})

	// Seed a task with an operation, a saved result, and a pending approval interaction.
	taskID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := store.AcceptCommandBatch(ctx, []state.Command{{
		ID:        taskID,
		RunID:     taskID,
		Kind:      state.CommandStart,
		Text:      "Prepare weekly summary",
		CreatedAt: fixedNow,
	}}, nil); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if err := store.Incorporate(ctx, state.IncorporateRequest{RunID: taskID, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("incorporate task: %v", err)
	}
	opTime := fixedNow
	if err := store.PutOperation(ctx, taskID, state.OperationRecord{
		ID:          "op-1",
		CallID:      "call-1",
		Name:        "web_search",
		EffectClass: "read",
		Outcome:     "confirmed",
		Revision:    1,
		Attempt:     1,
		Action:      &state.Action{Digest: "sha256:abc", Text: "Searched: weekly summary"},
		CreatedAt:   &opTime,
	}); err != nil {
		t.Fatalf("seed operation: %v", err)
	}
	if _, err := store.Publish(ctx, state.PublishRequest{
		RunID:         taskID,
		Revision:      1,
		Status:        state.RunCompleted,
		Text:          "Weekly summary complete.",
		Answer:        &state.ResultDraft{Checked: true},
		BypassRouting: true,
	}); err != nil {
		t.Fatalf("publish task result: %v", err)
	}
	results, err := store.RecentResults(ctx, 10)
	if err != nil || len(results) == 0 {
		t.Fatalf("expected seeded result, got len=%d err=%v", len(results), err)
	}
	resultID := results[0].ID

	approvalExpiry := fixedNow.Add(30 * time.Minute)
	openedInteraction, err := perms.Open(ctx, permissions.Interaction{
		ID:           "int_openapi_1",
		TaskID:       taskID,
		Kind:         "approval",
		Status:       "pending",
		Prompt:       "Send summary email?",
		ActionDigest: "sha256:send1",
		Payload: json.RawMessage(`{
			"summary": "Send email to team@example.com",
			"effect": "sensitive_send",
			"account": {"connection_id": "conn_1", "label": "alice@example.com"},
			"resources": [{"connection_id": "conn_1", "kind": "mail", "provider_id": "inbox"}],
			"recipients": ["team@example.com"]
		}`),
		ExpiresAt: &approvalExpiry,
	}, "")
	if err != nil {
		t.Fatalf("seed interaction: %v", err)
	}

	validateCall := func(method, target string, body any, wantStatus int) []byte {
		t.Helper()
		var reqBody []byte
		if body != nil {
			var err error
			reqBody, err = json.Marshal(body)
			if err != nil {
				t.Fatalf("marshal %s %s request: %v", method, target, err)
			}
		}
		req := httptest.NewRequest(method, target, bytes.NewReader(reqBody))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		route, pathParams, err := router.FindRoute(req)
		if err != nil {
			t.Fatalf("OpenAPI route not found for %s %s: %v", method, target, err)
		}
		reqVal := &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: pathParams,
			Route:      route,
			Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		}
		if err := openapi3filter.ValidateRequest(ctx, reqVal); err != nil {
			t.Fatalf("OpenAPI request validation failed for %s %s: %v", method, target, err)
		}
		// Reset request body after validator consumed it.
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("%s %s status = %d, want %d (body=%s)", method, target, rec.Code, wantStatus, rec.Body.String())
		}
		respVal := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: reqVal,
			Status:                 rec.Code,
			Header:                 rec.Header(),
			Body:                   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
			Options: &openapi3filter.Options{
				IncludeResponseStatus: true,
			},
		}
		if err := openapi3filter.ValidateResponse(ctx, respVal); err != nil {
			t.Fatalf("OpenAPI response validation failed for %s %s (%d): %v\nbody=%s", method, target, rec.Code, err, rec.Body.String())
		}
		return rec.Body.Bytes()
	}

	// 1. Capabilities, snapshot, canonical conversation, summary
	validateCall(http.MethodGet, "/v1/capabilities", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/snapshot", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/conversations/assistant", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/conversations/assistant/messages", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/assistant/summary", nil, http.StatusOK)

	// 2. Preferences read, update, and 409 conflict with current
	prefsRaw := validateCall(http.MethodGet, "/v1/preferences", nil, http.StatusOK)
	var prefs connections.Preferences
	if err := json.Unmarshal(prefsRaw, &prefs); err != nil {
		t.Fatalf("unmarshal preferences: %v", err)
	}
	prefReqID := uuid.NewString()
	validateCall(http.MethodPut, "/v1/preferences", map[string]any{
		"request_id":        prefReqID,
		"expected_revision": prefs.Revision,
		"value": map[string]any{
			"timezone":                "America/Los_Angeles",
			"working_hours":           []any{},
			"default_destination_ids": []any{},
			"account_defaults":        map[string]any{},
			"budgets":                 map[string]any{"task_usd": 5.0},
		},
	}, http.StatusOK)
	validateCall(http.MethodGet, "/v1/mutations/"+prefReqID, nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/mutations/"+uuid.NewString(), nil, http.StatusNotFound)
	validateCall(http.MethodPut, "/v1/preferences", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"timezone":                "UTC",
			"working_hours":           []any{},
			"default_destination_ids": []any{},
			"account_defaults":        map[string]any{},
			"budgets":                 map[string]any{"task_usd": 5.0},
		},
	}, http.StatusConflict)

	// 3. Work & operations
	validateCall(http.MethodGet, "/v1/work", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/work/"+taskID, nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/work/"+taskID+"/operations", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/operations", nil, http.StatusOK)

	// 4. Interactions
	validateCall(http.MethodGet, "/v1/interactions", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/interactions/"+openedInteraction.ID, nil, http.StatusOK)
	validateCall(http.MethodPost, "/v1/interactions/"+openedInteraction.ID+"/responses", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": openedInteraction.Revision,
		"value": map[string]any{
			"kind":          "approve",
			"action_digest": "sha256:send1",
		},
	}, http.StatusOK)

	// 5. Permissions
	validateCall(http.MethodGet, "/v1/permissions", nil, http.StatusOK)
	ruleReceipt := validateCall(http.MethodPost, "/v1/permissions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"effect":         "allow",
			"tool_ids":       []string{"nori.google/calendar/events.list"},
			"connection_ids": []string{"conn_1"},
			"resource_ids":   []string{"primary"},
			"action_classes": []string{"read"},
		},
	}, http.StatusCreated)
	var createdRule struct {
		Resource permissions.Rule `json:"resource"`
	}
	if err := json.Unmarshal(ruleReceipt, &createdRule); err != nil {
		t.Fatalf("unmarshal created rule: %v", err)
	}
	validateCall(http.MethodGet, "/v1/permissions/"+createdRule.Resource.ID, nil, http.StatusOK)
	validateCall(http.MethodPost, "/v1/permissions/"+createdRule.Resource.ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": createdRule.Resource.Revision,
		"value":             map[string]any{"action": "revoke"},
	}, http.StatusOK)

	// 6. Automations & inbox
	autoValue := map[string]any{
		"title":                    "Morning briefing",
		"instruction":              "Summarize today's agenda.",
		"trigger":                  map[string]any{"kind": "recurring", "local_start": "2026-11-21T08:00", "timezone": "America/Los_Angeles", "rrule": "FREQ=DAILY"},
		"connection_ids":           []string{},
		"budget_usd":               0.5,
		"max_active_runs":          1,
		"missed_run":               "skip",
		"execution_window_seconds": 3600,
		"destination_ids":          []string{scheduler.InAppDestination},
	}
	validateCall(http.MethodPost, "/v1/automations/preview", autoValue, http.StatusOK)
	autoReceipt := validateCall(http.MethodPost, "/v1/automations", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value":             autoValue,
	}, http.StatusCreated)
	var createdAuto struct {
		Resource scheduler.Automation `json:"resource"`
	}
	if err := json.Unmarshal(autoReceipt, &createdAuto); err != nil {
		t.Fatalf("unmarshal created automation: %v", err)
	}
	validateCall(http.MethodGet, "/v1/automations", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/automations/"+createdAuto.Resource.ID, nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/automations/"+createdAuto.Resource.ID+"/occurrences", nil, http.StatusOK)
	validateCall(http.MethodPost, "/v1/automations/"+createdAuto.Resource.ID+"/actions", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": createdAuto.Resource.Revision,
		"value":             map[string]any{"action": "pause"},
	}, http.StatusOK)
	validateCall(http.MethodGet, "/v1/inbox", nil, http.StatusOK)

	// 7. Memory (core, working, logs, search)
	memReceipt := validateCall(http.MethodPost, "/v1/memory", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"text":    "Prefers oat milk in coffee",
			"about":   "Coffee",
			"section": "Preferences",
			"pinned":  true,
		},
	}, http.StatusCreated)
	var createdMem struct {
		Resource memoryFact `json:"resource"`
	}
	if err := json.Unmarshal(memReceipt, &createdMem); err != nil {
		t.Fatalf("unmarshal created memory: %v", err)
	}
	validateCall(http.MethodGet, "/v1/memory", nil, http.StatusOK)
	validateCall(http.MethodPut, "/v1/memory/"+createdMem.Resource.ID, map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": createdMem.Resource.Revision,
		"value": map[string]any{
			"text":    "Prefers oat milk in morning coffee",
			"about":   "Coffee",
			"section": "Preferences",
		},
	}, http.StatusOK)
	validateCall(http.MethodGet, "/v1/memory/working", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/memory/logs", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/memory/search?q=coffee", nil, http.StatusOK)

	// 8. Library, results, search
	validateCall(http.MethodGet, "/v1/results/"+resultID, nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/library", nil, http.StatusOK)
	libRaw := validateCall(http.MethodGet, "/v1/library/"+resultID, nil, http.StatusOK)
	var libItem libraryItem
	if err := json.Unmarshal(libRaw, &libItem); err != nil {
		t.Fatalf("unmarshal library item: %v", err)
	}
	validateCall(http.MethodGet, "/v1/library/"+resultID+"/versions", nil, http.StatusOK)
	validateCall(http.MethodPut, "/v1/library/"+resultID, map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": libItem.Revision,
		"value":             map[string]any{"title": "Renamed weekly summary"},
	}, http.StatusOK)
	validateCall(http.MethodGet, "/v1/search?q=summary", nil, http.StatusOK)

	// 9. Integrations, connections, MCP, tools
	validateCall(http.MethodGet, "/v1/integrations", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/connections", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/mcp", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/tools", nil, http.StatusOK)

	// 10. Settings: BYOK model keys, models (including home backend!), API costs
	validateCall(http.MethodGet, "/v1/settings/model-keys", nil, http.StatusOK)
	validateCall(http.MethodPut, "/v1/settings/model-keys/openai", map[string]any{"api_key": "sk-test-1234567890"}, http.StatusOK)
	validateCall(http.MethodPut, "/v1/settings/model-keys/unknown_provider", map[string]any{"api_key": "sk-test-1234567890"}, http.StatusBadRequest)
	modelsRaw := validateCall(http.MethodGet, "/v1/settings/models", nil, http.StatusOK)
	var modelView modelprofiles.AccountView
	if err := json.Unmarshal(modelsRaw, &modelView); err != nil {
		t.Fatalf("unmarshal model settings: %v", err)
	}
	validateCall(http.MethodPut, "/v1/settings/models", map[string]any{
		"selection": map[string]any{
			"chat_profile_id": "home-mac-llama",
			"task_profile_id": "home-mac-llama",
			"home_only":       true,
		},
		"expected_revision": modelView.Revision,
	}, http.StatusOK)
	validateCall(http.MethodDelete, "/v1/settings/model-keys/openai", nil, http.StatusOK)

	validateCall(http.MethodGet, "/v1/settings/api-costs", nil, http.StatusOK)
	validateCall(http.MethodGet, "/v1/settings/api-costs?days=7", nil, http.StatusOK)
	{
		// Invalid query enum should fail OpenAPI request validation AND return a schema-valid 400 APIError from Go.
		req := httptest.NewRequest(http.MethodGet, "/v1/settings/api-costs?days=99", nil)
		route, pathParams, err := router.FindRoute(req)
		if err != nil {
			t.Fatalf("find api-costs route: %v", err)
		}
		reqVal := &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: pathParams,
			Route:      route,
			Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		}
		if err := openapi3filter.ValidateRequest(ctx, reqVal); err == nil {
			t.Fatalf("expected OpenAPI request validation to reject days=99")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET /v1/settings/api-costs?days=99 status = %d, want 400", rec.Code)
		}
		respVal := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: reqVal,
			Status:                 rec.Code,
			Header:                 rec.Header(),
			Body:                   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
			Options:                &openapi3filter.Options{IncludeResponseStatus: true},
		}
		if err := openapi3filter.ValidateResponse(ctx, respVal); err != nil {
			t.Fatalf("400 api-costs response failed OpenAPI validation: %v", err)
		}
	}

	// 11. Diagnostics & feedback
	validateCall(http.MethodGet, "/v1/diagnostics", nil, http.StatusOK)
	validateCall(http.MethodPost, "/v1/feedback", map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value": map[string]any{
			"kind":    "wrong_result",
			"task_id": taskID,
			"text":    "Summary missed Q3 figures",
		},
	}, http.StatusCreated)

	// 12. Validate HomeConnectors response shape directly against schema (HomeAISettings.Service requires Postgres)
	validateHomeConnectorsWireShape(t, ctx, router, fixedNow)

	// 13. Validate Personal Computer endpoints against OpenAPI schema
	validateComputerOpenAPI(t, ctx, router)
}

func validateComputerOpenAPI(t *testing.T, ctx context.Context, router routers.Router) {
	t.Helper()
	mgr := computer.NewManager(fake.New(), computer.NewMemoryStore(), computer.Config{})
	comp := &ComputerAPI{Manager: mgr, UserID: "openapi-user"}
	mux := http.NewServeMux()
	comp.register(mux)

	check := func(method, target string, body any, wantStatus int) []byte {
		t.Helper()
		var reqBody []byte
		if body != nil {
			reqBody, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, target, bytes.NewReader(reqBody))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		route, pathParams, err := router.FindRoute(req)
		if err != nil {
			t.Fatalf("find route %s %s: %v", method, target, err)
		}
		reqVal := &openapi3filter.RequestValidationInput{
			Request: req, PathParams: pathParams, Route: route,
			Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		}
		if err := openapi3filter.ValidateRequest(ctx, reqVal); err != nil {
			t.Fatalf("request validation failed %s %s: %v", method, target, err)
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != wantStatus {
			t.Fatalf("%s %s status=%d want=%d body=%s", method, target, rec.Code, wantStatus, rec.Body.String())
		}
		respVal := &openapi3filter.ResponseValidationInput{
			RequestValidationInput: reqVal, Status: rec.Code, Header: rec.Header(),
			Body:    io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
			Options: &openapi3filter.Options{IncludeResponseStatus: true},
		}
		if err := openapi3filter.ValidateResponse(ctx, respVal); err != nil {
			t.Fatalf("response validation failed %s %s: %v\nbody=%s", method, target, err, rec.Body.String())
		}
		return rec.Body.Bytes()
	}

	check(http.MethodGet, "/v1/computer", nil, http.StatusOK)
	check(http.MethodPost, "/v1/computer/wake", nil, http.StatusOK)
	sessRaw := check(http.MethodPost, "/v1/computer/sessions", map[string]any{"mode": "view", "task_id": "task-1"}, http.StatusCreated)
	var sess createComputerSessionResponse
	_ = json.Unmarshal(sessRaw, &sess)
	check(http.MethodPatch, "/v1/computer/sessions/"+sess.ID, map[string]any{"mode": "control"}, http.StatusOK)
	check(http.MethodDelete, "/v1/computer/sessions/"+sess.ID, nil, http.StatusNoContent)
	ckptRaw := check(http.MethodPost, "/v1/computer/checkpoints", map[string]any{"label": "pre-upgrade"}, http.StatusCreated)
	var ckpt computer.CheckpointInfo
	_ = json.Unmarshal(ckptRaw, &ckpt)
	check(http.MethodPost, "/v1/computer/restore", map[string]any{"checkpoint_id": ckpt.ID}, http.StatusOK)
	check(http.MethodPost, "/v1/computer/reset", nil, http.StatusOK)
	check(http.MethodPost, "/v1/computer/sleep", nil, http.StatusNoContent)
}

func validateHomeConnectorsWireShape(t *testing.T, ctx context.Context, router routers.Router, now time.Time) {
	t.Helper()
	payload := struct {
		Enabled   bool                     `json:"enabled"`
		Devices   []homeai.Device          `json:"devices"`
		Downloads settings.HomeAIDownloads `json:"downloads,omitempty"`
	}{
		Enabled: true,
		Devices: []homeai.Device{{
			ID:       "dev_mac_studio",
			Name:     "Mac Studio",
			Status:   "online",
			LastSeen: &now,
			Version:  "1.2.0",
			Models:   []homeaiwire.Model{{Name: "llama4:latest", Digest: "sha256:123", Capabilities: []string{"tools"}}},
		}},
		Downloads: settings.HomeAIDownloads{MacOS: "https://example.com/nori.dmg"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal home connectors: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/settings/home-connectors", nil)
	route, pathParams, err := router.FindRoute(req)
	if err != nil {
		t.Fatalf("find /v1/settings/home-connectors route: %v", err)
	}
	reqVal := &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	respVal := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: reqVal,
		Status:                 http.StatusOK,
		Header:                 http.Header{"Content-Type": []string{"application/json"}},
		Body:                   io.NopCloser(bytes.NewReader(raw)),
		Options:                &openapi3filter.Options{IncludeResponseStatus: true},
	}
	if err := openapi3filter.ValidateResponse(ctx, respVal); err != nil {
		t.Fatalf("HomeConnectors response validation failed: %v", err)
	}
}
