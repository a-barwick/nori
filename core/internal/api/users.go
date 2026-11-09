package api

import (
	"context"
	"net/http"
	"strings"

	"nori/internal/users"
)

type UserRoutes struct {
	Handler http.Handler
	Resume  func(context.Context) error
}

// The authenticated web server supplies the resolved user ID. Only internal resume can fan out.
func UserHandler(directory users.Directory, open func(context.Context, users.User) (UserRoutes, func(), error), local bool, model string) http.Handler {
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
		// External MCP clients reach /mcp with a client token that names its user; the token itself is verified
		// against that user's records before anything is served.
		if r.URL.Path == "/mcp" {
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			owner, ok := ClientTokenUser(token)
			if !ok {
				writeJSON(w, 401, map[string]string{"error": "A Nori client token is required."})
				return
			}
			id = owner
			copyURL := *r.URL
			copyURL.Path = "/v1/nori-mcp"
			r = r.Clone(r.Context())
			r.URL = &copyURL
		}
		if strings.HasPrefix(r.URL.Path, "/users/") {
			var path string
			id, path, _ = strings.Cut(strings.TrimPrefix(r.URL.Path, "/users/"), "/")
			copyURL := *r.URL
			copyURL.Path = "/" + path
			r = r.Clone(r.Context())
			r.URL = &copyURL
		}
		if id == "" && r.Method == "POST" && r.URL.Path == "/api/resume" {
			rows, err := directory.List(r.Context())
			if err != nil {
				writeError(w, err)
				return
			}
			failed := false
			for _, row := range rows {
				routes, release, err := open(r.Context(), row)
				if err != nil {
					failed = true
					continue
				}
				err = routes.Resume(r.Context())
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
		routes, release, err := open(r.Context(), row)
		if err != nil {
			writeError(w, err)
			return
		}
		defer release()
		routes.Handler.ServeHTTP(w, r.WithContext(users.WithID(r.Context(), row.ID)))
	})
}
