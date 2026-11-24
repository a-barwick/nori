package api

import "net/http"

func (s *server) assistantRoute(handler http.HandlerFunc) http.HandlerFunc {
	if s.Features.DisableBackgroundTasks {
		return assistantUnavailable
	}
	return handler
}

func assistantUnavailable(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "assistant is not enabled.", "code": "not_enabled", "feature": "assistant",
	})
}
