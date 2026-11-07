package api

import (
	"net/http"
	"strings"
)

func (s *server) featureUnavailable(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	feature := ""
	if s.Features.DisableBackgroundTasks && (strings.HasPrefix(path, "/v1/conversations/assistant") || (strings.HasPrefix(path, "/api/") && path != "/api/process" && path != "/api/resume" && path != "/api/healthz") || path == "/v1/snapshot" || path == "/v1/events" || strings.HasPrefix(path, "/v1/uploads") || strings.HasPrefix(path, "/v1/results")) {
		feature = "assistant"
	}
	if s.Features.DisableOffers && (path == "/api/proactive" || path == "/api/offer") {
		feature = "calendar offers"
	}
	if s.Features.DisableSchedules && path == "/api/schedule" {
		feature = "schedules"
	}
	if feature == "" {
		return false
	}
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": feature + " is not enabled.", "code": "not_enabled", "feature": feature})
	return true
}
