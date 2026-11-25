package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nori/internal/modelprofiles"
	"nori/internal/settings"
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

	// Allowed caller sees model_profiles.
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

	// Disallowed caller does not see model_profiles and gets 403 on explicit selection.
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

	// Allowed caller sending an unknown profile gets 400.
	badPayload := []byte(`{"id":"11111111-1111-4111-8111-111111111111","text":"hi","model":{"chat_profile_id":"nonexistent"}}`)
	req4 := httptest.NewRequest(http.MethodPost, "/v1/conversations/assistant/messages", bytes.NewReader(badPayload))
	req4.Header.Set("Content-Type", "application/json")
	rec4 := httptest.NewRecorder()
	allowed.ServeHTTP(rec4, req4)
	if rec4.Code != 400 {
		t.Fatalf("expected 400 for unknown profile, got %d (%s)", rec4.Code, rec4.Body.String())
	}
}
