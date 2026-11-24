package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"nori/internal/httpjson"
	"nori/internal/users"
)

const defaultReadyCheckTimeout = 2 * time.Second

type UserRoutes struct {
	Handler http.Handler
	Resume  func(context.Context) error
}

// UserHandlerOptions configures optional readiness and operational endpoints on UserHandlerWithOptions.
type UserHandlerOptions struct {
	// ReadyCheck verifies live backend connectivity for /readyz and /api/readyz.
	// Nil treats the process as ready without probing external dependencies.
	ReadyCheck func(context.Context) error
	// ReadyTimeout bounds ReadyCheck duration (defaults to 2s).
	ReadyTimeout time.Duration
	// Metrics handles GET/HEAD /metrics when non-nil.
	Metrics http.Handler
}

// The authenticated web server supplies the resolved user ID. Only internal resume can fan out.
func UserHandler(directory users.Directory, open func(context.Context, users.User) (UserRoutes, func(), error), local bool, model string) http.Handler {
	return UserHandlerWithOptions(directory, open, local, model, UserHandlerOptions{})
}

// UserHandlerWithOptions is like UserHandler with optional readiness probe and metrics handlers.
func UserHandlerWithOptions(directory users.Directory, open func(context.Context, users.User) (UserRoutes, func(), error), local bool, model string, opts UserHandlerOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local && (r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "") {
			writeJSON(w, 403, map[string]string{"error": "Use the web server to access the runtime."})
			return
		}
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/healthz" {
			writeJSON(w, 200, map[string]string{"status": "ok", "model": model})
			return
		}
		if r.URL.Path == "/readyz" || r.URL.Path == "/api/readyz" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
				return
			}
			serveReadyz(w, r, opts.ReadyCheck, opts.ReadyTimeout)
			return
		}
		if r.URL.Path == "/metrics" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
				return
			}
			if opts.Metrics == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
				return
			}
			opts.Metrics.ServeHTTP(w, r)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/users/resolve" {
			var body struct {
				Email string `json:"email"`
			}
			if err := httpjson.Decode(w, r, &body, 64<<10); err != nil {
				if errors.Is(err, httpjson.ErrTooLarge) {
					writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "Request body is too large."})
					return
				}
				message := "Send a valid JSON object."
				if errors.Is(err, httpjson.ErrMultiple) {
					message = "Send one JSON object."
				}
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": message})
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
			if id == "" {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
				return
			}
			copyURL := *r.URL
			copyURL.Path = "/" + path
			copyURL.RawPath = ""
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
		if id == "" {
			writeJSON(w, 403, map[string]string{"error": "A known user is required."})
			return
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

// OpsHandler serves only process operational endpoints (/healthz, /api/healthz,
// /readyz, /api/readyz, and /metrics) without user-scoped API routes. Used by
// dedicated worker processes exposing an optional operational listener.
func OpsHandler(local bool, model string, opts UserHandlerOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local && (r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "") {
			writeJSON(w, 403, map[string]string{"error": "Use the web server to access the runtime."})
			return
		}
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/healthz" {
			writeJSON(w, 200, map[string]string{"status": "ok", "model": model})
			return
		}
		if r.URL.Path == "/readyz" || r.URL.Path == "/api/readyz" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
				return
			}
			serveReadyz(w, r, opts.ReadyCheck, opts.ReadyTimeout)
			return
		}
		if r.URL.Path == "/metrics" {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "Method not allowed."})
				return
			}
			if opts.Metrics == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
				return
			}
			opts.Metrics.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not found."})
	})
}

func serveReadyz(w http.ResponseWriter, r *http.Request, readyCheck func(context.Context) error, timeout time.Duration) {
	if timeout <= 0 {
		timeout = defaultReadyCheckTimeout
	}
	if readyCheck != nil {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := readyCheck(ctx); err != nil {
			if r.Method == http.MethodHead {
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "unavailable",
				"error":  "database unavailable",
			})
			return
		}
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
