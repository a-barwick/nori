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

	"github.com/coder/websocket"
	"nori/internal/computer"
	"nori/internal/computer/sprites"
	"nori/internal/computer/vmscripts"
)

func TestSpritesProviderEnsurePolicyDesktopAndDestroy(t *testing.T) {
	var (
		mu           sync.Mutex
		created      bool
		urlAuth      string
		networkRules []map[string]any
		destroyed    bool
		checkpointed bool
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
		case strings.HasSuffix(path, "/control"):
			t.Error("provider opened a pooled Sprite control socket")
			http.NotFound(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/fs/read"):
			_, _ = w.Write([]byte(strings.Repeat("A", 43)))
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
			checkpointed = true
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"type":"info","data":"checkpoint created"}` + "\n"))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/checkpoints"):
			w.Header().Set("Content-Type", "application/json")
			now := time.Now().UTC()
			checkpoints := []map[string]any{
				{"id": "Current", "create_time": now.Format(time.RFC3339)},
			}
			if checkpointed {
				checkpoints = append(checkpoints, map[string]any{"id": "v1", "comment": "baseline", "create_time": now.Add(-time.Minute).Format(time.RFC3339)})
			}
			_ = json.NewEncoder(w).Encode(checkpoints)
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
	if inst.BaselineCheckpointID != "v1" {
		t.Fatalf("BaselineCheckpointID = %q", inst.BaselineCheckpointID)
	}
	checkpoints, err := p.ListCheckpoints(ctx, inst.ProviderID)
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].ID != "v1" || checkpoints[0].Label != "baseline" {
		t.Fatalf("checkpoints = %+v, want the restorable baseline", checkpoints)
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
	foundFlycastDeny := false
	for _, rule := range networkRules {
		if rule["domain"] == "*.flycast" && rule["action"] == "deny" {
			foundFlycastDeny = true
		}
	}
	if !foundFlycastDeny {
		t.Fatal("missing deny rule for private Flycast hosts")
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
	if target.Headers.Get("X-Nori-Desktop-Token") != strings.Repeat("A", 43) {
		t.Fatalf("expected desktop token header on DesktopTarget")
	}

	// Destroy removes Sprite cleanly.
	if err := p.Destroy(ctx, inst.ProviderID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if !destroyed {
		t.Fatalf("expected DELETE request to be received")
	}
}

func TestSpritesProviderStatusOnlyReadsManagementMetadata(t *testing.T) {
	for _, tt := range []struct {
		state   string
		want    computer.Status
		standby bool
	}{
		{state: "running", want: computer.StatusReady, standby: false},
		{state: "warm", want: computer.StatusReady, standby: true},
		{state: "cold", want: computer.StatusSleeping, standby: false},
	} {
		t.Run(tt.state, func(t *testing.T) {
			now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.Method + " " + r.URL.Path {
				case "GET /v1/sprites/nori-test":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"name":            "nori-test",
						"status":          tt.state,
						"last_running_at": now,
						"config":          map[string]int{"storage_gb": 10},
					})
				default:
					t.Errorf("status made an unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			p, err := sprites.New(sprites.Config{Token: "test-token", BaseURL: srv.URL, HTTPClient: srv.Client()})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()

			got, err := p.Status(t.Context(), "nori-test")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.want || got.Standby != tt.standby || got.DiskUsedBytes != -1 || got.DiskMaxBytes != 10<<30 {
				t.Fatalf("unexpected status: %+v", got)
			}
			if got.LastActiveAt == nil || !got.LastActiveAt.Equal(now) {
				t.Errorf("last activity = %v, want %v", got.LastActiveAt, now)
			}
			if len(got.Checkpoints) != 0 {
				t.Errorf("status included checkpoints: %+v", got.Checkpoints)
			}
		})
	}
}

func TestSpritesProviderExecAndBootstrapCanonicalHomeAndNPMPrefix(t *testing.T) {
	var (
		mu       sync.Mutex
		requests []string
		execCmds [][]string
		execDirs []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		path := r.URL.Path
		requests = append(requests, r.Method+" "+path)

		switch {
		case r.Method == http.MethodGet && path == "/v1/sprites/nori-cmp-2":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		case r.Method == http.MethodPost && path == "/v1/sprites":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     "sp-2",
				"name":   "nori-cmp-2",
				"status": "running",
				"url":    "https://nori-cmp-2.sprites.app",
			})
		case r.Method == http.MethodPut && (strings.HasSuffix(path, "/url") || path == "/v1/sprites/nori-cmp-2"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/policy/network"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(path, "/exec"):
			q := r.URL.Query()
			execCmds = append(execCmds, append([]string(nil), q["cmd"]...))
			execDirs = append(execDirs, q.Get("dir"))
			mu.Unlock()
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				mu.Lock()
				return
			}
			// Drain the SDK's initial stdin-EOF frame before closing so sprites-go's
			// wsAdapter.writeLoop finishes writing before readLoop closes the conn.
			readCtx, readCancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
			_, _, _ = conn.Read(readCtx)
			readCancel()
			_ = conn.Write(r.Context(), websocket.MessageBinary, []byte{3, 0})
			_ = conn.Close(websocket.StatusNormalClosure, "")
			mu.Lock()
		case strings.Contains(path, "/fs/"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/checkpoint"):
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"type":"info","data":"ok"}` + "\n"))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/checkpoints"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "v1", "comment": "baseline", "create_time": time.Now().UTC().Format(time.RFC3339)},
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	p, err := sprites.New(sprites.Config{
		Token:      "test-token",
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	inst, err := p.Ensure(t.Context(), computer.Spec{UserID: "u1", ComputerID: "cmp_2"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if inst.BootstrapVersion != vmscripts.Version {
		t.Fatalf("BootstrapVersion = %q, want %q", inst.BootstrapVersion, vmscripts.Version)
	}

	if len(execCmds) < 2 {
		t.Fatalf("expected canonical-home exec and bootstrap exec, got %d execs: %+v", len(execCmds), execCmds)
	}
	// First exec must link /home/nori -> /home/sprite from "/" before bootstrap writes scripts.
	firstCmd := execCmds[0]
	if len(firstCmd) < 3 || firstCmd[0] != "bash" || firstCmd[1] != "-lc" {
		t.Fatalf("first exec args = %q, want bash -lc ...", firstCmd)
	}
	if execDirs[0] != "/" {
		t.Fatalf("canonical-home exec dir = %q, want /", execDirs[0])
	}
	for _, want := range []string{
		`HOME="/home/nori"`,
		`NPM_CONFIG_PREFIX="/home/nori/.local/npm"`,
		`PATH="/home/nori/.nori/bin:/home/nori/.local/npm/bin:/home/nori/.local/bin:/home/nori/.local/venv/bin:`,
		`ln -s /home/sprite /home/nori`,
	} {
		if !strings.Contains(firstCmd[2], want) {
			t.Fatalf("first exec missing %q in:\n%s", want, firstCmd[2])
		}
	}

	// Verify canonical-home exec happens before any /fs/write call.
	firstExecIdx, firstWriteIdx := -1, -1
	for i, req := range requests {
		if firstExecIdx == -1 && strings.HasSuffix(req, "/exec") {
			firstExecIdx = i
		}
		if firstWriteIdx == -1 && strings.Contains(req, "/fs/write") {
			firstWriteIdx = i
		}
	}
	if firstExecIdx == -1 || firstWriteIdx == -1 || firstExecIdx >= firstWriteIdx {
		t.Fatalf("expected canonical-home /exec before /fs/write, requests = %v", requests)
	}

	// Verify vmscripts configure .npmrc and PATH consistently.
	if !strings.Contains(vmscripts.BootstrapSH, "prefix=/home/nori/.local/npm") {
		t.Fatal("bootstrap.sh missing .npmrc prefix=/home/nori/.local/npm")
	}
	if !strings.Contains(vmscripts.DesktopStartSH, `NPM_CONFIG_PREFIX="/home/nori/.local/npm"`) {
		t.Fatal("nori-desktop-start missing NPM_CONFIG_PREFIX")
	}
}
