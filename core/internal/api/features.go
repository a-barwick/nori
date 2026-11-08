package api

import "net/http"

func (s *server) assistantRoute(handler http.HandlerFunc) http.HandlerFunc {
	if s.Features.DisableBackgroundTasks {
		return assistantUnavailable
	}
	return handler
}

func (s *server) conversationRoute(handler http.HandlerFunc) http.HandlerFunc {
	if !s.Features.DisableBackgroundTasks {
		return handler
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("conversation") == "assistant" {
			assistantUnavailable(w, r)
			return
		}
		handler(w, r)
	}
}

func assistantUnavailable(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "assistant is not enabled.", "code": "not_enabled", "feature": "assistant",
	})
}
