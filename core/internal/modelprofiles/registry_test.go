package modelprofiles

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"nori/internal/settings"
	"nori/internal/state"
)

func TestRegistryLocalDevDualBackendAndAllowlist(t *testing.T) {
	cfg := settings.Settings{
		Cloud:      "local",
		EnvProfile: "dev",
		Models: settings.Models{
			Provider:   "gateway",
			GatewayURL: "https://ai-gateway.vercel.sh/v1",
			GatewayKey: "vck_dev_test_token",
			LocalURL:   "http://127.0.0.1:11435/v1",
			LocalModel: "inkling-2-mini:64k",
			Outer:      "inkling-2-mini:64k",
			Inner:      "inkling-2-mini:64k",
		},
	}
	reg, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if !reg.CanSelect("anyone", "anyone@example.com") {
		t.Fatalf("expected local mode to allow model selection for any user")
	}

	cat := reg.Snapshot()
	if len(cat.Profiles) != 2 {
		t.Fatalf("expected 2 profiles (local Inkling + dev Haiku), got %d", len(cat.Profiles))
	}
	local, ok := cat.Get("local-inkling-2-mini")
	if !ok || local.Paid || local.Backend != BackendLocal || local.Endpoint != "http://127.0.0.1:11435/v1" {
		t.Fatalf("unexpected local profile: %+v", local)
	}
	cloud, ok := cat.Get("cloud-claude-haiku-5-5")
	if !ok || !cloud.Paid || cloud.Backend != BackendGateway || cloud.ChatAdapter != AdapterAnthropicMessages {
		t.Fatalf("unexpected cloud profile: %+v", cloud)
	}

	resolved, err := reg.ValidateAndResolve(&state.ModelSelection{
		ChatProfileID: "cloud-claude-haiku-5-5",
		ChatReasoning: "medium",
		TaskProfileID: "local-inkling-2-mini",
	})
	if err != nil {
		t.Fatalf("ValidateAndResolve valid: %v", err)
	}
	if resolved.ChatProfileVersion != "v1" || resolved.TaskProfileVersion != "v1" {
		t.Fatalf("expected v1 versions, got %+v", resolved)
	}
	if _, err := reg.ValidateAndResolve(&state.ModelSelection{
		ChatProfileID: "local-inkling-2-mini",
		ChatReasoning: "high",
	}); err == nil {
		t.Fatalf("expected error for unsupported reasoning on local-inkling-2-mini")
	}
}

func TestRegistryProdAllowlistAndCatalog(t *testing.T) {
	cfg := settings.Settings{
		Cloud:      "aws",
		EnvProfile: "prod",
		Models: settings.Models{
			Provider:    "gateway",
			GatewayURL:  "https://ai-gateway.vercel.sh/v1",
			GatewayKey:  "vck_prod_test_token",
			Outer:       "anthropic/claude-opus-5.5",
			Inner:       "anthropic/claude-haiku-5.5",
			PickerUsers: []string{"austin@example.com", "user-42"},
		},
	}
	reg, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if !reg.CanSelect("user-42", "") {
		t.Fatalf("expected allowlisted userID to select")
	}
	if !reg.CanSelect("other", "AUSTIN@example.com") {
		t.Fatalf("expected allowlisted email (case-insensitive) to select")
	}
	if reg.CanSelect("user-99", "stranger@example.com") {
		t.Fatalf("expected non-allowlisted user to be denied in prod")
	}
	cat := reg.Snapshot()
	if len(cat.Profiles) != 4 {
		t.Fatalf("expected 4 prod profiles (Opus, Haiku, Neon, Hydrogen), got %d", len(cat.Profiles))
	}
	if cat.DefaultChatProfileID != "cloud-claude-opus-5-5" || cat.DefaultTaskProfileID != "cloud-claude-haiku-5-5" {
		t.Fatalf("unexpected prod defaults: chat=%q task=%q", cat.DefaultChatProfileID, cat.DefaultTaskProfileID)
	}
	pub := cat.Public()
	if len(pub.Profiles) != 4 {
		t.Fatalf("expected 4 public profiles, got %d", len(pub.Profiles))
	}
}

func TestRegistryHotReloadFileAndFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	initial := `{
		"profiles": [
			{
				"id": "custom-haiku",
				"version": "v1",
				"label": "Custom Haiku",
				"backend": "gateway",
				"model": "anthropic/claude-haiku-5.5",
				"chat_adapter": "anthropic_messages",
				"task_adapter": "anthropic_messages",
				"lanes": ["chat", "task"],
				"supported_reasoning": ["low", "high"],
				"default_reasoning": "low",
				"paid": true
			}
		]
	}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := settings.Settings{
		Cloud:      "local",
		EnvProfile: "dev",
		Models: settings.Models{
			GatewayURL:   "https://ai-gateway.vercel.sh/v1",
			GatewayKey:   "vck_dev_test",
			ProfilesFile: path,
		},
	}
	reg, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if p, ok := reg.Snapshot().Get("custom-haiku"); !ok || p.Version != "v1" {
		t.Fatalf("expected initial custom-haiku v1, got %+v (ok=%v)", p, ok)
	}

	updated := `{
		"profiles": [
			{
				"id": "custom-haiku",
				"version": "v2",
				"label": "Custom Haiku v2",
				"backend": "gateway",
				"model": "anthropic/claude-haiku-5.5",
				"chat_adapter": "anthropic_messages",
				"task_adapter": "anthropic_messages",
				"lanes": ["chat", "task"],
				"supported_reasoning": ["low", "medium", "high"],
				"default_reasoning": "medium",
				"paid": true
			}
		]
	}`
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, future, future)

	if p, ok := reg.Snapshot().Get("custom-haiku"); !ok || p.Version != "v2" || p.DefaultReasoning != "medium" {
		t.Fatalf("expected hot-reloaded v2, got %+v (ok=%v)", p, ok)
	}

	if err := os.WriteFile(path, []byte(`{invalid json`), 0o600); err != nil {
		t.Fatal(err)
	}
	future2 := future.Add(2 * time.Second)
	_ = os.Chtimes(path, future2, future2)
	if p, ok := reg.Snapshot().Get("custom-haiku"); !ok || p.Version != "v2" {
		t.Fatalf("expected fallback to last-good v2 on bad JSON, got %+v (ok=%v)", p, ok)
	}
}

func TestRegistryBYOKRequiredAllowsEmptyDeploymentGatewayKey(t *testing.T) {
	cfg := settings.Settings{
		Cloud:      "aws",
		EnvProfile: "prod",
		Models: settings.Models{
			Provider:     "gateway",
			GatewayURL:   "https://ai-gateway.vercel.sh/v1",
			GatewayKey:   "",
			BYOKRequired: true,
			Outer:        "anthropic/claude-opus-5.5",
			Inner:        "anthropic/claude-haiku-5.5",
		},
	}
	reg, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	cat := reg.Snapshot()
	if len(cat.Profiles) != 4 {
		t.Fatalf("expected 4 cloud gateway profiles in BYOK mode without deployment key, got %d", len(cat.Profiles))
	}

	sel, err := reg.ValidateAndResolve(&state.ModelSelection{
		ChatProfileID: "cloud-claude-opus-5-5",
		ChatReasoning: "medium",
		TaskProfileID: "cloud-claude-haiku-5-5",
	})
	if err != nil {
		t.Fatalf("ValidateAndResolve: %v", err)
	}
	if sel.ChatPinned == nil || sel.TaskPinned == nil {
		t.Fatalf("expected pinned profiles, got %+v", sel)
	}

	pChat, reasoning, explicit, err := reg.ResolveChat(sel)
	if err != nil || !explicit || pChat.Backend != BackendGateway || pChat.Endpoint != cfg.Models.GatewayURL || reasoning != "medium" {
		t.Fatalf("ResolveChat pinned BYOK profile = (%+v, %q, %v, %v)", pChat, reasoning, explicit, err)
	}

	pTask, _, explicitTask, err := reg.ResolveTask(sel)
	if err != nil || !explicitTask || pTask.Backend != BackendGateway || pTask.Endpoint != cfg.Models.GatewayURL {
		t.Fatalf("ResolveTask pinned BYOK profile = (%+v, %v, %v)", pTask, explicitTask, err)
	}
}
