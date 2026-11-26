package app_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nori/internal/agent"
	"nori/internal/app"
	"nori/internal/conversation"
	"nori/internal/modelkeys"
	"nori/internal/modelprofiles"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/users"
)

func TestBYOKAccountSettingsRouteChatAndWorkWithoutFallback(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore { return artifacts })
	svc := app.OpenServiceWithStorage(storage, nil, settings.Settings{
		Cloud: "local", Role: "all", TaskBudgetUSD: 1, Features: settings.Features{DisableJev: true},
		Connections: settings.Connections{
			CredentialStore: "database", CredentialKey: hex.EncodeToString(bytes.Repeat([]byte{0x42}, 32)),
		},
		Models: settings.Models{
			Provider: "gateway", BYOKRequired: true, GatewayURL: "https://unused.example/v1", GatewayKey: "deployment_key",
			OpenAIKey: "system_openai_key", AnthropicKey: "system_anthropic_key", OpenRouterKey: "system_openrouter_key",
		},
	})
	t.Cleanup(svc.Close)
	svc.ConfigureApp = func(_ users.User, a *app.App) {
		a.Assistant.ModelKeys.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected inference during key validation: %s", r.URL)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})}
	}
	u := createUser(t, svc, "11111111-1111-4111-8111-111111111111", "byok@example.com")
	other := createUser(t, svc, "22222222-2222-4222-8222-222222222222", "another-byok@example.com")
	uApp, release, err := svc.Acquire(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	handler := svc.Handler(true)
	request := func(owner users.User, method, path string, body any, status int) []byte {
		t.Helper()
		var b []byte
		if body != nil {
			b, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, "/users/"+owner.ID+"/v1/"+path, bytes.NewReader(b))
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body.Bytes())
		}
		if strings.Contains(w.Body.String(), "test_user_api_key") {
			t.Fatal("a model settings response exposed a raw API key")
		}
		return w.Body.Bytes()
	}
	view := func(owner users.User) modelprofiles.AccountView {
		t.Helper()
		var out modelprofiles.AccountView
		if err := json.Unmarshal(request(owner, "GET", "settings/models", nil, 200), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := view(u); got.Revision != 0 || len(got.ModelProfiles.Profiles) != 0 {
		t.Fatalf("new account unexpectedly has a model configured: %+v", got)
	}
	if _, err := uApp.Runner.AccountModel(ctx, nil).Respond(ctx, conversation.ReplyRequest{}); !errors.Is(err, modelkeys.ErrMissingAPIKey) {
		t.Fatalf("Chat without a key: %v", err)
	}
	if _, err := uApp.TaskActivities.AccountModel(ctx, nil).Respond(ctx, agent.Request{}); !errors.Is(err, modelkeys.ErrMissingAPIKey) {
		t.Fatalf("Work without a key: %v", err)
	}

	// Fetching the empty conversation before any API key is configured must not enqueue
	// a doomed introduction turn that leaves a stale model_key_required error card in Chat.
	initialSnapshot := request(u, "GET", "conversations/assistant", nil, 200)
	if err := uApp.Runner.Process(ctx); err != nil {
		t.Fatal(err)
	}
	afterInitialProcess := request(u, "GET", "conversations/assistant", nil, 200)
	if bytes.Contains(initialSnapshot, []byte("model_key_required")) || bytes.Contains(afterInitialProcess, []byte("model_key_required")) {
		t.Fatalf("empty conversation snapshot enqueued a failing introduction before key setup: %s", afterInitialProcess)
	}

	request(u, "POST", "conversations/assistant/messages", map[string]string{
		"id": "f64d9cb5-0727-4f99-98d0-8f653c67da6f", "text": "Hello",
	}, 202)
	if err := uApp.Runner.Process(ctx); err != nil {
		t.Fatal(err)
	}
	messages := request(u, "GET", "conversations/assistant", nil, 200)
	if !bytes.Contains(messages, []byte("model_key_required")) || !bytes.Contains(messages, []byte("More → Models")) {
		t.Fatalf("Chat did not explain how to add a key: %s", messages)
	}

	request(u, "PUT", "settings/model-keys/anthropic", map[string]string{"api_key": "test_user_api_key_anthropic"}, 200)
	first := view(u)
	if first.Selection.ChatProfileID != "anthropic-opus" || first.Selection.TaskProfileID != "anthropic-haiku" ||
		first.Selection.ChatReasoning != "low" || first.Selection.TaskReasoning != "low" || first.Revision != 1 ||
		len(first.ModelProfiles.Profiles) != 3 {
		t.Fatalf("unexpected first key defaults: %+v", first)
	}
	if name := uApp.Runner.AccountModel(ctx, nil).Name(); name != "claude-opus-5-5" {
		t.Fatalf("Chat model = %q", name)
	}
	if name := uApp.TaskActivities.AccountModel(ctx, nil).Name(); name != "claude-haiku-5-5" {
		t.Fatalf("Work model = %q", name)
	}

	request(u, "PUT", "settings/model-keys/openai", map[string]string{"api_key": "test_user_api_key_openai"}, 200)
	added := view(u)
	if !state.ModelSelectionEqual(&first.Selection, &added.Selection) || len(added.ModelProfiles.Profiles) != 6 {
		t.Fatalf("adding a second provider changed the account's choices: %+v", added)
	}
	if got := view(other); got.Revision != 0 || len(got.ModelProfiles.Profiles) != 0 {
		t.Fatalf("another user's keys or selection are visible: %+v", got)
	}
	command := state.Command{
		ID: "584bcd39-2ff5-4e49-841f-43d8d87c4ad4", RunID: "584bcd39-2ff5-4e49-841f-43d8d87c4ad4",
		Kind: state.CommandStart, Text: "Scheduled test task",
	}
	originalRun, err := uApp.Scheduler.Accept(ctx, command)
	if err != nil || originalRun.ModelSelection.IsEmpty() || originalRun.ModelSelection.TaskProfileID != "anthropic-haiku" ||
		originalRun.ModelSelection.TaskPinned == nil || originalRun.ModelSelection.TaskPinned.Model != "claude-haiku-5-5" {
		t.Fatalf("scheduled task did not pin the account work model: %v, %v", originalRun.ModelSelection, err)
	}

	historical := originalRun.ModelSelection.Clone()
	historical.TaskPinned.Model = "claude-haiku-4-5-20251001"
	if name := uApp.TaskActivities.AccountModel(ctx, historical).Name(); name != historical.TaskPinned.Model {
		t.Fatalf("an accepted run changed its pinned model to the current catalog entry: %q", name)
	}

	next := added.Selection
	next.TaskProfileID, next.TaskReasoning = "openai-luna", "xhigh"
	input := func(sel state.ModelSelection, rev int64) map[string]any {
		return map[string]any{"selection": sel, "expected_revision": rev}
	}
	request(u, "PUT", "settings/models", input(next, added.Revision-1), 409)
	invalid := next
	invalid.TaskProfileID, invalid.TaskReasoning = "openai-sol", "none"
	request(u, "PUT", "settings/models", input(invalid, added.Revision), 400)
	request(u, "PUT", "settings/models", input(next, added.Revision), 200)
	synced := view(u)
	if !state.ModelSelectionEqual(&next, &synced.Selection) || synced.Revision != added.Revision+1 {
		t.Fatalf("saved model settings were not visible from a separate request: %+v", synced)
	}

	acceptedAgain, err := uApp.Scheduler.Accept(ctx, command)
	if err != nil || !state.ModelSelectionEqual(originalRun.ModelSelection, acceptedAgain.ModelSelection) ||
		uApp.TaskActivities.AccountModel(ctx, acceptedAgain.ModelSelection).Name() != "claude-haiku-5-5" {
		t.Fatalf("resubmitting a scheduled occurrence changed its pinned model: %v", err)
	}
	command.ID = "d0d51c9a-2d1b-4374-8764-9706de7270f7"
	command.RunID = command.ID
	nextRun, err := uApp.Scheduler.Accept(ctx, command)
	if err != nil || nextRun.ModelSelection.IsEmpty() || nextRun.ModelSelection.TaskProfileID != "openai-luna" {
		t.Fatalf("another scheduled task did not use the current account model: %v, %v", nextRun.ModelSelection, err)
	}

	request(u, "DELETE", "settings/model-keys/anthropic", nil, 200)
	deleted := view(u)
	if !state.ModelSelectionEqual(&next, &deleted.Selection) || len(deleted.ModelProfiles.Profiles) != 3 {
		t.Fatalf("removing the selected key silently changed it: %+v", deleted)
	}
	if _, err := uApp.Runner.AccountModel(ctx, nil).Respond(ctx, conversation.ReplyRequest{}); !errors.Is(err, modelkeys.ErrModelAccess) {
		t.Fatalf("Chat bypassed the missing provider key: %v", err)
	}
	if _, err := uApp.TaskActivities.AccountModel(ctx, originalRun.ModelSelection).Respond(ctx, agent.Request{}); !errors.Is(err, modelkeys.ErrModelAccess) {
		t.Fatalf("scheduled Work bypassed the missing key for its pinned provider: %v", err)
	}
	if name := uApp.TaskActivities.AccountModel(ctx, nextRun.ModelSelection).Name(); name != "gpt-6-luna" {
		t.Fatalf("removing Chat's key affected the Work lane: %q", name)
	}
}
func TestBYOKApplicationRequiresCredentialStore(t *testing.T) {
	_, err := app.OpenService(context.Background(), nil, settings.Settings{
		Features: settings.Features{DisableJev: true},
		Models:   settings.Models{BYOKRequired: true},
	})
	if err == nil || !strings.Contains(err.Error(), "NORI_CREDENTIAL_STORE=database") {
		t.Fatalf("expected missing credential store error, got %v", err)
	}
}
