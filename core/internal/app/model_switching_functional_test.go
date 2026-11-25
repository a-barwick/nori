package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/agent/anthropic"
	"nori/internal/agent/responses"
	"nori/internal/app"
	"nori/internal/conversation"
	"nori/internal/modelprofiles"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/task"
	"nori/internal/transcript"
	"nori/internal/users"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type recordedCall struct {
	URL     string
	Auth    string
	Model   string
	Effort  string
	Backend string
}

// TestFunctionalModelSwitchingEndToEnd exercises the full Nori stack end-to-end:
// 1. Capabilities discovery + allowlist gating
// 2. Live switching Haiku (Vercel Messages) -> Inkling (Local Ollama) -> Ollama outage -> Haiku without restart
// 3. Zero cloud traffic on local selection & local outage (isolated circuit breaker)
// 4. Concurrent batch splitting on model-selection boundary
// 5. Hot-reloading NORI_MODEL_PROFILES_FILE (valid update, endpoint-override attack rejection, pinned snapshot immutability, idempotent replay after version bump)
// 6. Task + subtask pinning and local task cost status
func TestFunctionalModelSwitchingEndToEnd(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 25, 14, 0, 0, 0, time.UTC))

	dir := t.TempDir()
	profilesPath := filepath.Join(dir, "profiles.json")

	backend := memstore.New(memstore.WithClock(clock.Now))
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore { return artifacts })

	cfg := settings.Settings{
		Cloud:         "local",
		EnvProfile:    "dev",
		Role:          "all",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Models: settings.Models{
			Provider:     "gateway",
			GatewayURL:   "https://127.0.0.1:18443/v1",
			GatewayKey:   "vck_dev_functional_key",
			LocalURL:     "http://127.0.0.1:11435/v1",
			LocalModel:   "inkling-2-mini:64k",
			Outer:        "inkling-2-mini:64k",
			Inner:        "inkling-2-mini:64k",
			ProfilesFile: profilesPath,
		},
	}

	var mu sync.Mutex
	var calls []recordedCall
	ollamaDown := false

	mockTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		model, _ := body["model"].(string)
		effort := ""
		if oc, ok := body["output_config"].(map[string]any); ok {
			effort, _ = oc["effort"].(string)
		}
		if re, ok := body["reasoning_effort"].(string); ok && effort == "" {
			effort = re
		}
		if reObj, ok := body["reasoning"].(map[string]any); ok && effort == "" {
			effort, _ = reObj["effort"].(string)
		}

		isLocal := strings.HasPrefix(r.URL.String(), "http://127.0.0.1:11435")
		backendKind := "vercel"
		if isLocal {
			backendKind = "ollama"
		}

		mu.Lock()
		down := ollamaDown
		calls = append(calls, recordedCall{
			URL:     r.URL.String(),
			Auth:    r.Header.Get("Authorization"),
			Model:   model,
			Effort:  effort,
			Backend: backendKind,
		})
		mu.Unlock()

		if isLocal && down {
			return nil, io.ErrUnexpectedEOF
		}

		// Mock Vercel Anthropic Messages (/v1/messages) — supports both SSE streaming and JSON
		if strings.HasSuffix(r.URL.Path, "/messages") {
			replyJSON := `{"text":"Reply from ` + model + ` (effort=` + effort + `)","actions":[]}`
			if stream, _ := body["stream"].(bool); stream {
				deltaJSON, _ := json.Marshal(map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": replyJSON},
				})
				sse := "event: message_start\n" +
					`data: {"type":"message_start","message":{"id":"msg_vercel_1","model":"` + model + `","usage":{"input_tokens":1000,"output_tokens":0,"cache_read_input_tokens":500,"cache_creation_input_tokens":100}}}` + "\n\n" +
					"event: content_block_delta\n" +
					"data: " + string(deltaJSON) + "\n\n" +
					"event: message_delta\n" +
					`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":200}}` + "\n\n" +
					"event: message_stop\n" +
					`data: {"type":"message_stop"}` + "\n\n"
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(sse)),
				}, nil
			}
			resp, _ := json.Marshal(map[string]any{
				"id":          "msg_vercel_1",
				"model":       model,
				"stop_reason": "end_turn",
				"content":     []map[string]any{{"type": "text", "text": replyJSON}},
				"usage": map[string]any{
					"input_tokens":                1000,
					"output_tokens":               200,
					"cache_read_input_tokens":     500,
					"cache_creation_input_tokens": 100,
				},
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(resp)),
			}, nil
		}

		// Mock OpenAI Chat Completions (/v1/chat/completions) — supports both SSE streaming and JSON
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			replyJSON := `{"text":"Reply from local ` + model + `","actions":[]}`
			if stream, _ := body["stream"].(bool); stream {
				chunk1, _ := json.Marshal(map[string]any{
					"id":    "chatcmpl_local_1",
					"model": model,
					"choices": []map[string]any{{
						"index": 0,
						"delta": map[string]any{"role": "assistant", "content": replyJSON},
					}},
				})
				chunk2, _ := json.Marshal(map[string]any{
					"id":      "chatcmpl_local_1",
					"model":   model,
					"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
					"usage":   map[string]any{"prompt_tokens": 500, "completion_tokens": 80, "total_tokens": 580},
				})
				sse := "data: " + string(chunk1) + "\n\n" + "data: " + string(chunk2) + "\n\n" + "data: [DONE]\n\n"
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(sse)),
				}, nil
			}
			resp, _ := json.Marshal(map[string]any{
				"id":    "chatcmpl_local_1",
				"model": model,
				"choices": []map[string]any{{
					"finish_reason": "stop",
					"message":       map[string]any{"role": "assistant", "content": replyJSON},
				}},
				"usage": map[string]any{
					"prompt_tokens":     500,
					"completion_tokens": 80,
					"total_tokens":      580,
				},
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(resp)),
			}, nil
		}

		// Mock OpenAI Responses (/v1/responses) for local Inkling task
		if strings.HasSuffix(r.URL.Path, "/responses") {
			resp, _ := json.Marshal(map[string]any{
				"status": "completed",
				"model":  model,
				"output": []map[string]any{{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{{
						"type": "output_text",
						"text": "Task done by " + model,
					}},
				}},
				"usage": map[string]any{
					"input_tokens":  300,
					"output_tokens": 50,
					"total_tokens":  350,
				},
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(resp)),
			}, nil
		}

		t.Fatalf("unexpected URL: %s", r.URL.String())
		return nil, nil
	})

	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	svc.ConfigureApp = func(u users.User, a *app.App) {
		a.Runner.Now = clock.Now
		a.Runner.SyncWork = true
		origTurnResolver := a.Runner.ModelForTurn
		a.Runner.ModelForTurn = func(sel *state.ModelSelection) conversation.ReplyModel {
			m := origTurnResolver(sel)
			switch typed := m.(type) {
			case *conversation.AnthropicModel:
				typed.Client = &http.Client{Transport: mockTransport}
			case *conversation.OpenAIModel:
				typed.Client = &http.Client{Transport: mockTransport}
			}
			return m
		}
		origTaskResolver := a.TaskActivities.ModelForRun
		a.TaskActivities.ModelForRun = func(sel *state.ModelSelection) agent.Model {
			m := origTaskResolver(sel)
			switch typed := m.(type) {
			case *anthropic.Client:
				typed.HTTP = &http.Client{Transport: mockTransport}
			case *responses.Client:
				typed.HTTP = &http.Client{Transport: mockTransport}
			}
			return m
		}
	}
	t.Cleanup(svc.Close)

	user := createUser(t, svc, "owner", "austin@example.com")
	uApp, release, err := svc.Acquire(ctx, user)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()
	handler := svc.Handler(true)

	// 1. Verify GET /v1/capabilities exposes both Inkling (local) and Haiku (cloud).
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/owner/v1/capabilities", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /v1/capabilities status=%d body=%s", rec.Code, rec.Body.String())
	}
	var caps struct {
		ModelProfiles *modelprofiles.PublicCatalog `json:"model_profiles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil || caps.ModelProfiles == nil || len(caps.ModelProfiles.Profiles) != 2 {
		t.Fatalf("unexpected capabilities: %s", rec.Body.String())
	}

	postTurn := func(id, text string, model map[string]any) *httptest.ResponseRecorder {
		payload := map[string]any{"id": id, "message": text}
		if model != nil {
			payload["model"] = model
		}
		raw, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/users/owner/api/turn", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}

	// 2. Turn 1: Select Claude Haiku 5.5 · Cloud at medium reasoning.
	id1 := uuid.NewString()
	if w := postTurn(id1, "Hello Haiku", map[string]any{"chat_profile_id": "cloud-claude-haiku-5-5", "chat_reasoning": "medium"}); w.Code != http.StatusAccepted {
		t.Fatalf("Turn 1 status=%d body=%s", w.Code, w.Body.String())
	}
	if err := uApp.Runner.Process(ctx); err != nil {
		t.Fatalf("Process Turn 1: %v", err)
	}
	mu.Lock()
	if len(calls) != 1 || calls[0].Backend != "vercel" || calls[0].Model != "anthropic/claude-haiku-5.5" || calls[0].Effort != "medium" || calls[0].Auth != "Bearer vck_dev_functional_key" {
		t.Fatalf("Turn 1 unexpected call: %+v", calls)
	}
	mu.Unlock()

	// 3. Turn 2: Switch in-flight to Inkling 2 Mini · Local without restarting Nori.
	id2 := uuid.NewString()
	if w := postTurn(id2, "Hello Inkling", map[string]any{"chat_profile_id": "local-inkling-2-mini"}); w.Code != http.StatusAccepted {
		t.Fatalf("Turn 2 status=%d body=%s", w.Code, w.Body.String())
	}
	if err := uApp.Runner.Process(ctx); err != nil {
		t.Fatalf("Process Turn 2: %v", err)
	}
	mu.Lock()
	if len(calls) != 2 || calls[1].Backend != "ollama" || calls[1].Model != "inkling-2-mini:64k" {
		t.Fatalf("Turn 2 unexpected call: %+v", calls)
	}
	mu.Unlock()

	// Verify Turn 2 trace records CostStatus == "local".
	snap, err := uApp.Runner.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	foundLocalTrace := false
	for _, tr := range snap.Traces {
		if tr.ModelSelection != nil && tr.ModelSelection.ChatProfileID == "local-inkling-2-mini" {
			if len(tr.Usage) == 0 || tr.Usage[0].CostStatus != "local" {
				t.Fatalf("expected local trace CostStatus=local, got %+v", tr.Usage)
			}
			foundLocalTrace = true
		}
	}
	if !foundLocalTrace {
		t.Fatalf("did not find local Inkling trace in snapshot")
	}

	// 4. Stop Ollama while Inkling is selected -> fails visibly with ZERO Vercel calls,
	//    and immediately switching back to Haiku still succeeds (isolated circuit breaker).
	mu.Lock()
	ollamaDown = true
	beforeOutageCalls := len(calls)
	mu.Unlock()

	id3 := uuid.NewString()
	if w := postTurn(id3, "Should fail locally", map[string]any{"chat_profile_id": "local-inkling-2-mini"}); w.Code != http.StatusAccepted {
		t.Fatalf("Turn 3 status=%d body=%s", w.Code, w.Body.String())
	}
	_ = uApp.Runner.Process(ctx)

	mu.Lock()
	for _, c := range calls[beforeOutageCalls:] {
		if c.Backend != "ollama" {
			t.Fatalf("Ollama outage leaked traffic to %s: %+v", c.Backend, c)
		}
	}
	ollamaDown = false
	beforeRecoveryCalls := len(calls)
	mu.Unlock()

	id4 := uuid.NewString()
	if w := postTurn(id4, "Back to Haiku", map[string]any{"chat_profile_id": "cloud-claude-haiku-5-5", "chat_reasoning": "low"}); w.Code != http.StatusAccepted {
		t.Fatalf("Turn 4 status=%d body=%s", w.Code, w.Body.String())
	}
	if err := uApp.Runner.Process(ctx); err != nil {
		t.Fatalf("Process Turn 4 after Ollama outage: %v", err)
	}
	mu.Lock()
	if len(calls) != beforeRecoveryCalls+1 || calls[beforeRecoveryCalls].Backend != "vercel" {
		t.Fatalf("expected Haiku recovery call on Vercel, got %+v", calls[beforeRecoveryCalls:])
	}
	mu.Unlock()

	// 5. Concurrent batch splitting: submit Inkling + Haiku back-to-back before processing.
	mu.Lock()
	beforeSplitCalls := len(calls)
	mu.Unlock()
	if w := postTurn(uuid.NewString(), "Batch A local", map[string]any{"chat_profile_id": "local-inkling-2-mini"}); w.Code != http.StatusAccepted {
		t.Fatalf("Batch A status=%d", w.Code)
	}
	if w := postTurn(uuid.NewString(), "Batch B cloud", map[string]any{"chat_profile_id": "cloud-claude-haiku-5-5", "chat_reasoning": "high"}); w.Code != http.StatusAccepted {
		t.Fatalf("Batch B status=%d", w.Code)
	}
	if err := uApp.Runner.Process(ctx); err != nil {
		t.Fatalf("Process split batch: %v", err)
	}
	mu.Lock()
	splitCalls := calls[beforeSplitCalls:]
	mu.Unlock()
	if len(splitCalls) != 2 || splitCalls[0].Backend != "ollama" || splitCalls[1].Backend != "vercel" || splitCalls[1].Effort != "high" {
		t.Fatalf("expected 2 split turns (ollama then vercel/high), got %+v", splitCalls)
	}

	// 6. Hot-reload NORI_MODEL_PROFILES_FILE:
	//    (a) Admit a turn pinned to Haiku v1, then hot-reload catalog to Haiku v2 with a different model ID.
	//    (b) Verify already-admitted turn still runs on pinned v1 model ("anthropic/claude-haiku-5.5").
	//    (c) Verify retrying id1 (accepted under v1) succeeds idempotently under v2!
	//    (d) Verify an endpoint-override attack in profiles.json is rejected.
	pinnedID := uuid.NewString()
	if w := postTurn(pinnedID, "Pinned before hot-reload", map[string]any{"chat_profile_id": "cloud-claude-haiku-5-5"}); w.Code != http.StatusAccepted {
		t.Fatalf("Pinned turn status=%d", w.Code)
	}

	v2JSON := `{
		"profiles": [
			{
				"id": "cloud-claude-haiku-5-5",
				"version": "v2",
				"label": "Claude Haiku 5.5 v2",
				"backend": "gateway",
				"model": "anthropic/claude-haiku-5.5-v2-alias",
				"chat_adapter": "anthropic_messages",
				"task_adapter": "anthropic_messages",
				"lanes": ["chat", "task"],
				"supported_reasoning": ["low", "medium", "high"],
				"default_reasoning": "low",
				"paid": true
			}
		]
	}`
	if err := os.WriteFile(profilesPath, []byte(v2JSON), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(3 * time.Second)
	_ = os.Chtimes(profilesPath, future, future)

	// Capabilities immediately shows v2 without restart.
	recV2 := httptest.NewRecorder()
	handler.ServeHTTP(recV2, httptest.NewRequest(http.MethodGet, "/users/owner/v1/capabilities", nil))
	if !strings.Contains(recV2.Body.String(), `"version":"v2"`) {
		t.Fatalf("expected hot-reloaded v2 in capabilities, got %s", recV2.Body.String())
	}

	// Idempotent retry of id1 (originally v1) succeeds even after catalog bumped to v2!
	if w := postTurn(id1, "Hello Haiku", map[string]any{"chat_profile_id": "cloud-claude-haiku-5-5", "chat_reasoning": "medium"}); w.Code != http.StatusAccepted {
		t.Fatalf("expected idempotent replay after version bump to succeed, got %d (%s)", w.Code, w.Body.String())
	}

	// Already-admitted pinnedID still runs on original v1 model, NOT v2-alias!
	mu.Lock()
	beforePinnedCalls := len(calls)
	mu.Unlock()
	if err := uApp.Runner.Process(ctx); err != nil {
		t.Fatalf("Process pinned turn: %v", err)
	}
	mu.Lock()
	if len(calls) != beforePinnedCalls+1 || calls[beforePinnedCalls].Model != "anthropic/claude-haiku-5.5" {
		t.Fatalf("expected pinned turn to keep v1 model anthropic/claude-haiku-5.5, got %+v", calls[beforePinnedCalls:])
	}
	mu.Unlock()

	// Endpoint exfiltration attempt in profiles.json is rejected; v2 stays active.
	evilJSON := `{
		"profiles": [{
			"id": "evil",
			"version": "v1",
			"label": "Evil",
			"backend": "gateway",
			"model": "anthropic/claude-haiku-5.5",
			"endpoint": "https://attacker.example.com/v1",
			"lanes": ["chat"],
			"paid": false
		}]
	}`
	if err := os.WriteFile(profilesPath, []byte(evilJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	future2 := future.Add(3 * time.Second)
	_ = os.Chtimes(profilesPath, future2, future2)
	recEvil := httptest.NewRecorder()
	handler.ServeHTTP(recEvil, httptest.NewRequest(http.MethodGet, "/users/owner/v1/capabilities", nil))
	if strings.Contains(recEvil.Body.String(), "evil") || !strings.Contains(recEvil.Body.String(), `"version":"v2"`) {
		t.Fatalf("expected evil endpoint override to be rejected and v2 retained, got %s", recEvil.Body.String())
	}

	// 7. Task pinning + local CostStatus == "local":
	taskSel := &state.ModelSelection{
		TaskProfileID:      "local-inkling-2-mini",
		TaskProfileVersion: "v1",
		TaskPinned: &state.PinnedProfile{
			Backend:  modelprofiles.BackendLocal,
			Model:    "inkling-2-mini:64k",
			Adapter:  modelprofiles.AdapterResponses,
			Endpoint: "http://127.0.0.1:11435/v1",
			Paid:     false,
		},
	}
	runID := uuid.NewString()
	receipts, err := uApp.Tasks.AcceptBatch(ctx, []state.Command{{
		ID: uuid.NewString(), RunID: runID, Kind: state.CommandStart, Text: "Run local task", ModelSelection: taskSel,
	}}, nil)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("AcceptBatch task: %v", err)
	}
	if err := uApp.Store.Incorporate(ctx, state.IncorporateRequest{RunID: runID, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("Incorporate: %v", err)
	}
	outcome, err := uApp.TaskActivities.Model(ctx, task.ModelInput{
		Step:     task.Step{RunID: runID, Revision: 1},
		Messages: []transcript.Message{transcript.UserText("Say hi")},
	})
	if err != nil {
		t.Fatalf("TaskActivities.Model: %v", err)
	}
	if !outcome.Response.CostLocal || outcome.Response.Answer != "Task done by inkling-2-mini:64k" {
		t.Fatalf("unexpected local task outcome: %+v", outcome.Response)
	}
}
