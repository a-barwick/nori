package app_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/agent/anthropic"
	"nori/internal/agent/responses"
	"nori/internal/app"
	"nori/internal/conversation"
	"nori/internal/modelkeys"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/users"
)

func TestBYOKRequired_BlocksWithoutUserKeyAndNeverFallsBackToDeploymentKey(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC))

	backend := memstore.New(memstore.WithClock(clock.Now))
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore { return artifacts })

	masterKey := hex.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	cfg := settings.Settings{
		Cloud:         "local",
		EnvProfile:    "dev",
		Role:          "all",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Connections: settings.Connections{
			CredentialStore: "database",
			CredentialKey:   masterKey,
		},
		Models: settings.Models{
			Provider:     "gateway",
			GatewayURL:   "https://127.0.0.1:18443/v1",
			GatewayKey:   "vck_deployment_key_must_never_be_used",
			BYOKRequired: true,
			Outer:        "anthropic/claude-haiku-4.5",
			Inner:        "anthropic/claude-haiku-4.5",
		},
	}

	var mu sync.Mutex
	var authHeaders []string
	mockTransport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		mu.Unlock()

		if strings.HasSuffix(r.URL.Path, "/models") {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
			}, nil
		}
		resp, _ := json.Marshal(map[string]any{
			"id": "chatcmpl_byok_1",
			"choices": []map[string]any{{
				"finish_reason": "stop",
				"message":       map[string]any{"role": "assistant", "content": `{"text":"BYOK ok","actions":[]}`},
			}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(resp)),
		}, nil
	})

	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	svc.ConfigureApp = func(u users.User, a *app.App) {
		a.Runner.Now = clock.Now
		a.Runner.SyncWork = true
		if a.Assistant != nil && a.Assistant.ModelKeys != nil {
			a.Assistant.ModelKeys.HTTPClient = &http.Client{Transport: mockTransport}
		}
		origTurn := a.Runner.ModelForTurn
		a.Runner.ModelForTurn = func(sel *state.ModelSelection) conversation.ReplyModel {
			m := origTurn(sel)
			switch typed := m.(type) {
			case *conversation.AnthropicModel:
				typed.Client = &http.Client{Transport: mockTransport}
			case *conversation.OpenAIModel:
				typed.Client = &http.Client{Transport: mockTransport}
			}
			return m
		}
		origTask := a.TaskActivities.ModelForRun
		a.TaskActivities.ModelForRun = func(sel *state.ModelSelection) agent.Model {
			m := origTask(sel)
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

	user := createUser(t, svc, "11111111-1111-4111-8111-111111111111", "byok@example.com")
	uApp, release, err := svc.Acquire(ctx, user)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	// 1. Without a user key, both chat and task fail with missing_api_key and make zero outbound calls.
	mChat := uApp.Runner.ModelForTurn(nil)
	if mChat == nil {
		t.Fatal("expected non-nil ReplyModel from ModelForTurn when gateway outer is enabled")
	}
	_, err = mChat.Respond(ctx, conversation.ReplyRequest{})
	if err == nil || !strings.Contains(err.Error(), "missing_api_key") {
		t.Fatalf("expected missing_api_key error for chat without BYOK key, got %v", err)
	}

	_, err = uApp.TaskActivities.Agent.Model.Respond(ctx, agent.Request{})
	if err == nil || !strings.Contains(err.Error(), "missing_api_key") {
		t.Fatalf("expected missing_api_key error for task without BYOK key, got %v", err)
	}

	mu.Lock()
	if len(authHeaders) != 0 {
		t.Fatalf("expected zero outbound calls without BYOK key, got %v", authHeaders)
	}
	mu.Unlock()

	// 2. Configure the user's BYOK key; chat now uses the user's key, never the deployment key.
	userKey := "vck_user_byok_secret_key_9999"
	if _, err := uApp.Assistant.ModelKeys.Set(ctx, modelkeys.ProviderVercelAIGateway, userKey); err != nil {
		t.Fatalf("Set BYOK key: %v", err)
	}
	mChatAfter := uApp.Runner.ModelForTurn(nil)
	if mChatAfter == nil {
		t.Fatal("expected non-nil ReplyModel after setting BYOK key")
	}
	msg, _ := json.Marshal(map[string]string{"role": "user", "content": "hi"})
	if _, err := mChatAfter.Respond(ctx, conversation.ReplyRequest{Messages: []json.RawMessage{msg}}); err != nil {
		t.Fatalf("chat Respond with BYOK key failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(authHeaders) < 2 {
		t.Fatalf("expected validation + chat calls, got %v", authHeaders)
	}
	for _, h := range authHeaders {
		if h == "Bearer vck_deployment_key_must_never_be_used" {
			t.Fatalf("deployment fallback key leaked into outbound request: %v", authHeaders)
		}
		if h != "Bearer "+userKey {
			t.Fatalf("unexpected Authorization header %q, want Bearer %s", h, userKey)
		}
	}
}

func TestBYOKRequired_FailsClosedWhenModelKeysServiceIsNil(t *testing.T) {
	ctx := context.Background()
	clock := newFakeClock(time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC))

	backend := memstore.New(memstore.WithClock(clock.Now))
	artifacts := memstore.NewArtifacts()
	storage := platform.NewStorage(backend, func(users.User) state.ArtifactStore { return artifacts })

	// Intentionally leave Connections.CredentialStore empty so services.ModelKeys is nil,
	// while BYOKRequired=true and a deployment GatewayKey is present.
	cfg := settings.Settings{
		Cloud:         "local",
		EnvProfile:    "dev",
		Role:          "all",
		TaskBudgetUSD: 1.0,
		Features:      settings.Features{DisableJev: true},
		Models: settings.Models{
			Provider:     "gateway",
			GatewayURL:   "https://127.0.0.1:18443/v1",
			GatewayKey:   "vck_deployment_key_must_never_be_used",
			BYOKRequired: true,
			Outer:        "anthropic/claude-haiku-4.5",
			Inner:        "anthropic/claude-haiku-4.5",
		},
	}

	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	t.Cleanup(svc.Close)

	user := createUser(t, svc, "22222222-2222-4222-8222-222222222222", "byok-nil@example.com")
	uApp, release, err := svc.Acquire(ctx, user)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer release()

	if uApp.Assistant.ModelKeys != nil {
		t.Fatal("expected ModelKeys service to be nil when CredentialStore is unconfigured")
	}

	// Default outer model, ModelForTurn(nil), and default task model must all fail closed with ErrMissingAPIKey.
	mChat := uApp.Runner.ModelForTurn(nil)
	if mChat == nil {
		t.Fatal("expected non-nil unavailable ReplyModel from ModelForTurn(nil)")
	}
	if _, err := mChat.Respond(ctx, conversation.ReplyRequest{}); err == nil || !strings.Contains(err.Error(), "missing_api_key") {
		t.Fatalf("expected missing_api_key from ModelForTurn(nil), got %v", err)
	}
	if _, err := uApp.TaskActivities.Agent.Model.Respond(ctx, agent.Request{}); err == nil || !strings.Contains(err.Error(), "missing_api_key") {
		t.Fatalf("expected missing_api_key from default task model, got %v", err)
	}
}
