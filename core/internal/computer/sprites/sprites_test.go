package sprites_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/computer"
	"nori/internal/computer/sprites"
)

func TestSpritesProviderEnsurePolicyDesktopAndDestroy(t *testing.T) {
	var (
		mu           sync.Mutex
		created      bool
		urlAuth      string
		networkRules []map[string]any
		destroyed    bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-sprites-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()

		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/sprites/nori-cmp-1") && !strings.Contains(path, "/checkpoints"):
			if !created || destroyed {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"not_found","message":"sprite not found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     "sp-1",
				"name":   "nori-cmp-1",
				"status": "running",
				"url":    "https://nori-cmp-1.sprites.app",
				"url_settings": map[string]any{
					"auth": urlAuth,
				},
			})
		case r.Method == http.MethodPost && path == "/v1/sprites":
			created = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     "sp-1",
				"name":   "nori-cmp-1",
				"status": "running",
				"url":    "https://nori-cmp-1.sprites.app",
			})
		case r.Method == http.MethodPut && (strings.HasSuffix(path, "/url") || path == "/v1/sprites/nori-cmp-1"):
			var body struct {
				Auth        string `json:"auth"`
				URLSettings *struct {
					Auth string `json:"auth"`
				} `json:"url_settings"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Auth != "" {
				urlAuth = body.Auth
			}
			if body.URLSettings != nil && body.URLSettings.Auth != "" {
				urlAuth = body.URLSettings.Auth
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/policy/network"):
			var body struct {
				Rules []map[string]any `json:"rules"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			networkRules = body.Rules
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/checkpoint"):
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"type":"info","data":"checkpoint created"}` + "\n"))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/checkpoints"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "v1", "comment": "baseline", "create_time": time.Now().UTC().Format(time.RFC3339)},
			})
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/v1/sprites/"):
			destroyed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Logf("unhandled mock sprites request: %s %s", r.Method, path)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	p, err := sprites.New(sprites.Config{
		Token:         "test-sprites-token",
		BaseURL:       srv.URL,
		HTTPClient:    srv.Client(),
		SkipBootstrap: true,
	})
	if err != nil {
		t.Fatalf("sprites.New: %v", err)
	}
	defer p.Close()

	ctx := context.Background()
	inst, err := p.Ensure(ctx, computer.Spec{UserID: "u1", ComputerID: "cmp_1", MaxDiskGB: 10})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if inst.ProviderID != "nori-cmp-1" {
		t.Fatalf("ProviderID = %q", inst.ProviderID)
	}
	if urlAuth != "sprite" {
		t.Fatalf("expected private URL auth=sprite, got %q", urlAuth)
	}

	// Apply SSRF network policy and verify private/metadata deny rules precede wildcard allow.
	if err := p.SetNetworkPolicy(ctx, inst.ProviderID, computer.DefaultNetworkPolicy()); err != nil {
		t.Fatalf("SetNetworkPolicy: %v", err)
	}
	if len(networkRules) < 5 {
		t.Fatalf("expected SSRF deny rules + wildcard allow, got %+v", networkRules)
	}
	last := networkRules[len(networkRules)-1]
	if last["domain"] != "*" || last["action"] != "allow" {
		t.Fatalf("expected trailing allow * rule, got %+v", last)
	}

	// DesktopEndpoint returns wss://.../websockify with internal Bearer auth header.
	target, err := p.DesktopEndpoint(ctx, inst.ProviderID)
	if err != nil {
		t.Fatalf("DesktopEndpoint: %v", err)
	}
	if target.WSURL != "wss://nori-cmp-1.sprites.app/websockify" {
		t.Fatalf("WSURL = %q", target.WSURL)
	}
	if target.Headers.Get("Authorization") != "Bearer test-sprites-token" {
		t.Fatalf("expected Authorization header on DesktopTarget")
	}

	// Destroy removes Sprite cleanly.
	if err := p.Destroy(ctx, inst.ProviderID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if !destroyed {
		t.Fatalf("expected DELETE request to be received")
	}
}
