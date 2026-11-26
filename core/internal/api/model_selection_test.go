package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nori/internal/settings"
)

func TestTurnModelSelectionRejectedWithoutAccountBYOK(t *testing.T) {
	handler := Handler(Deps{Model: "anthropic/claude-opus-5.5", ModelProvider: "gateway"})
	payload := []byte(`{"id":"11111111-1111-4111-8111-111111111111","text":"hi","model":{"chat_profile_id":"local-inkling-2-mini"}}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/conversations/assistant/messages", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed per-turn model selection, got %d (%s)", rec.Code, rec.Body.String())
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
