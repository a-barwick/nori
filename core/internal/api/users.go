package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"nori/internal/users"
)

// UserHandler runs behind the runtime's service-account authentication. The web
// server resolves a verified sign-in email here and forwards only that row's ID.
// Browser headers and JSON never select a scope. Internal scheduler/resume calls
// without a user deliberately fan out; ordinary routes require an explicit user.
func UserHandler(directory users.Directory, open func(context.Context, users.User) (Deps, func(), error), local bool, model string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local && (r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "") {
			writeJSON(w, 403, map[string]string{"error": "Use the web server to access the runtime."})
			return
		}
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/healthz" {
			writeJSON(w, 200, map[string]string{"status": "ok", "model": model})
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/users/resolve" {
			var body struct {
				Email string `json:"email"`
			}
			if !decode(w, r, &body) {
				return
			}
			row, err := directory.FindEmail(r.Context(), body.Email)
			if err != nil {
				writeJSON(w, 403, map[string]string{"error": "No user for this sign-in."})
				return
			}
			writeJSON(w, 200, map[string]string{"id": row.ID})
			return
		}
		id := ""
		if strings.HasPrefix(r.URL.Path, "/users/") {
			var path string
			id, path, _ = strings.Cut(strings.TrimPrefix(r.URL.Path, "/users/"), "/")
			copyURL := *r.URL
			copyURL.Path = "/" + path
			r = r.Clone(r.Context())
			r.URL = &copyURL
		}
		if id == "" && r.Method == "POST" && (r.URL.Path == "/api/proactive" || r.URL.Path == "/api/resume") {
			rows, err := directory.List(r.Context())
			if err != nil {
				writeError(w, err)
				return
			}
			// Each call retains its own row and durable check identity. Other users
			// still run when one fails; a scheduler retry is safe for finished checks.
			failed := false
			for _, row := range rows {
				deps, release, err := open(r.Context(), row)
				if err != nil {
					failed = true
					continue
				}
				if r.URL.Path == "/api/resume" {
					err = deps.Ops.Resume(r.Context())
				} else {
					check, cancel := context.WithTimeout(r.Context(), 80*time.Second)
					_, err = deps.Proactive.Check(check, false)
					cancel()
					if wakeErr := deps.Waker.WakeConversation(r.Context()); wakeErr != nil {
						err = wakeErr
					}
				}
				release()
				if err != nil {
					failed = true
				}
			}
			if failed {
				writeJSON(w, 503, map[string]string{"error": "Some users need a retry."})
				return
			}
			writeJSON(w, 200, map[string]string{"status": "ok"})
			return
		}
		// Retained Cloud Tasks wakes predate user IDs and belong only to the
		// original row. New wakes always carry /users/<id>/api/process.
		if id == "" && r.Method == "POST" && r.URL.Path == "/api/process" {
			id = users.LegacyID
		}
		row, err := directory.Get(r.Context(), id)
		if err != nil {
			writeJSON(w, 403, map[string]string{"error": "A known user is required."})
			return
		}
		deps, release, err := open(r.Context(), row)
		if err != nil {
			writeError(w, err)
			return
		}
		defer release()
		Handler(deps, local).ServeHTTP(w, r.WithContext(users.WithID(r.Context(), row.ID)))
	})
}
