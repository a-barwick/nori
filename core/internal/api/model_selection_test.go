package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"nori/internal/conversation"
	"nori/internal/modelprofiles"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestCapabilitiesModelProfilesGating(t *testing.T) {
	reg, err := modelprofiles.NewRegistry(settings.Settings{
		Cloud:      "local",
		EnvProfile: "dev",
		Models: settings.Models{
			Provider:   "gateway",
			GatewayURL: "https://ai-gateway.vercel.sh/v1",
			GatewayKey: "vck_dev_key",
			LocalURL:   "http://127.0.0.1:11435/v1",
			LocalModel: "inkling-2-mini:64k",
			Outer:      "inkling-2-mini:64k",
			Inner:      "inkling-2-mini:64k",
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	allowed := Handler(Deps{Model: "inkling-2-mini:64k", ModelProvider: "gateway", ModelProfiles: reg, CanPickModels: true})
	rec := httptest.NewRecorder()
	allowed.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var capView capabilitiesView
	if err := json.Unmarshal(rec.Body.Bytes(), &capView); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if capView.ModelProfiles == nil || len(capView.ModelProfiles.Profiles) != 2 {
		t.Fatalf("expected 2 model profiles, got %+v", capView.ModelProfiles)
	}
	if capView.Provider != "gateway" {
		t.Fatalf("expected provider=gateway, got %q", capView.Provider)
	}

	denied := Handler(Deps{Model: "anthropic/claude-opus-5.5", ModelProvider: "gateway", ModelProfiles: reg, CanPickModels: false})
	rec2 := httptest.NewRecorder()
	denied.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil))
	var capDenied capabilitiesView
	if err := json.Unmarshal(rec2.Body.Bytes(), &capDenied); err != nil {
		t.Fatalf("decode denied capabilities: %v", err)
	}
	if capDenied.ModelProfiles != nil {
		t.Fatalf("expected nil model_profiles for disallowed account, got %+v", capDenied.ModelProfiles)
	}

	payload := []byte(`{"id":"11111111-1111-4111-8111-111111111111","text":"hi","model":{"chat_profile_id":"local-inkling-2-mini"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/assistant/messages", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	denied.ServeHTTP(rec3, req)
	if rec3.Code != 403 {
		t.Fatalf("expected 403 for disallowed model selection, got %d (%s)", rec3.Code, rec3.Body.String())
	}

	badPayload := []byte(`{"id":"11111111-1111-4111-8111-111111111111","text":"hi","model":{"chat_profile_id":"nonexistent"}}`)
	req4 := httptest.NewRequest(http.MethodPost, "/v1/conversations/assistant/messages", bytes.NewReader(badPayload))
	req4.Header.Set("Content-Type", "application/json")
	rec4 := httptest.NewRecorder()
	allowed.ServeHTTP(rec4, req4)
	if rec4.Code != 400 {
		t.Fatalf("expected 400 for unknown profile, got %d (%s)", rec4.Code, rec4.Body.String())
	}
}

func TestAllowedSelectionThroughBothTurnEndpoints(t *testing.T) {
	const model = "fixture-profile-local"
	requests := make(chan string, 2)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		requests <- request.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"actions\":[],\"text\":\"profile reply\"}"},"finish_reason":"stop"}]}`))
	}))
	defer provider.Close()

	cfg := settings.Settings{Cloud: "local", EnvProfile: "dev", Models: settings.Models{
		LocalURL: provider.URL + "/v1", LocalModel: model, Outer: model, Inner: model,
	}}
	reg, err := modelprofiles.NewRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var profileID string
	for _, p := range reg.Snapshot().Public().Profiles {
		if p.Model == model {
			profileID = p.ID
		}
	}
	if profileID == "" {
		t.Fatal("local model profile unavailable")
	}
	for _, endpoint := range []struct{ path, textKey string }{
		{"/api/turn", "message"},
		{"/v1/conversations/assistant/messages", "text"},
	} {
		t.Run(endpoint.path, func(t *testing.T) {
			ctx := context.Background()
			u := users.User{ID: uuid.NewString(), Email: "selection@example.test", TimeZone: "UTC"}
			store := state.Compose(records.NewShared(memstore.New(), u, 1), memstore.NewArtifacts())
			tasks := &conversation.Tasks{Store: store}
			runner := conversation.New(nil, nil, nil, tasks)
			runner.ModelForTurn = func(sel *state.ModelSelection) conversation.ReplyModel {
				p, effort, ok, err := reg.ResolveChat(sel)
				if !ok || err != nil {
					t.Fatalf("chat pin not resolved: %+v: %v", sel, err)
				}
				return platform.ReplyModelForProfile(cfg.Models, u.ID, p, effort)
			}
			h := Handler(Deps{Runner: runner, Tasks: tasks, ModelProfiles: reg, CanPickModels: true})
			body, _ := json.Marshal(map[string]any{"id": uuid.NewString(), endpoint.textKey: "hello", "model": map[string]string{
				"chat_profile_id": profileID, "task_profile_id": profileID,
			}})
			req := httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("message rejected: %d %s", rec.Code, rec.Body.String())
			}
			if err := runner.Process(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-requests:
				if got != model {
					t.Fatalf("provider got %q, want %q", got, model)
				}
			default:
				t.Fatal("selected model endpoint was never called")
			}
		})
	}
}

func TestDisabledAssistantRejectsTurnRoutes(t *testing.T) {
	handler := Handler(Deps{Features: settings.Features{DisableBackgroundTasks: true}})
	capabilities := httptest.NewRecorder()
	handler.ServeHTTP(capabilities, httptest.NewRequest("GET", "/v1/capabilities", nil))
	var view capabilitiesView
	if err := json.Unmarshal(capabilities.Body.Bytes(), &view); err != nil || view.Features["backgroundTasks"] || len(view.ConversationModes) != 0 {
		t.Fatalf("disabled assistant capabilities = %+v, %v", view, err)
	}
	for _, route := range []string{"/api/turn", "/v1/conversations/assistant/messages"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("POST", route, strings.NewReader("{}")))
		if response.Code != http.StatusNotImplemented {
			t.Fatalf("%s returned %d, want 501", route, response.Code)
		}
	}
}
