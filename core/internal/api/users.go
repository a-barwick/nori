package api

import (
	"context"
	"net/http"
	"strings"

	"nori/internal/clientauth"
	"nori/internal/state"
	"nori/internal/users"
)

type UserRoutes struct {
	Handler     http.Handler
	Resume      func(context.Context) error
	ClientStore state.ResourceStore
}

type userServer struct {
	directory users.Directory
	open      func(context.Context, users.User) (UserRoutes, func(), error)
	model     string
	devices   clientauth.DeviceStore
}

// The authenticated web server supplies the resolved user ID. Only internal resume can fan out.
func UserHandler(directory users.Directory, open func(context.Context, users.User) (UserRoutes, func(), error), local bool, model string, devices clientauth.DeviceStore) http.Handler {
	s := &userServer{directory: directory, open: open, model: model, devices: devices}
	root := newRouteMux(false, false)
	s.register(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local && (r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "") {
			writeJSON(w, 403, map[string]string{"error": "Use the web server to access the runtime."})
			return
		}
		if handler, pattern := root.mux.Handler(r); pattern != "" && pattern == r.Method+" "+r.URL.Path || pattern == r.URL.Path {
			handler.ServeHTTP(w, r)
			return
		}
		id := ""
		if strings.HasPrefix(r.URL.Path, "/users/") {
			var path string
			id, path, _ = strings.Cut(strings.TrimPrefix(r.URL.Path, "/users/"), "/")
			r = withPath(r, "/"+path)
		}
		s.serveUser(w, r, id)
	})
}

func (s *userServer) register(mux *routeMux) {
	mux.handle("/healthz", healthRule, s.health)
	mux.handle("/api/healthz", healthRule, s.health)
	mux.handle("POST /api/users/resolve", browserJSON, s.resolve)
	mux.handle("/mcp", mcpRule, s.mcp)
	if s.devices != nil || mux.manifest {
		device := route(CallerDevice).body("json", jsonLimit)
		mux.handle("POST "+clientauth.DeviceStartPath, device, jsonBody(s.startDevice))
		mux.handle("POST "+clientauth.DeviceStatusPath, device, jsonBody(s.pollDevice))
	}
	// Retained Cloud Tasks wakes predate user IDs and belong only to the original row.
	mux.handle("POST /api/process", internal.body("json", jsonLimit), func(w http.ResponseWriter, r *http.Request) {
		s.serveUser(w, r, users.LegacyID)
	})
	mux.handle("POST /api/resume", internal.body("json", jsonLimit), s.resume)
}

func (s *userServer) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"status": "ok", "model": s.model})
}

func (s *userServer) resolve(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if !decode(w, r, &body) {
		return
	}
	row, err := s.directory.FindEmail(r.Context(), body.Email)
	if err != nil {
		writeJSON(w, 403, map[string]string{"error": "No user for this sign-in."})
		return
	}
	writeJSON(w, 200, map[string]string{"id": row.ID})
}

// The token names its user and is verified against that user's records.
func (s *userServer) mcp(w http.ResponseWriter, r *http.Request) {
	credential, ok := clientauth.Parse(clientToken(r))
	if !ok {
		clientUnauthorized(w)
		return
	}
	s.serveUser(w, withPath(r, "/v1/nori-mcp"), credential.Owner)
}

func (s *userServer) resume(w http.ResponseWriter, r *http.Request) {
	rows, err := s.directory.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	failed := false
	for _, row := range rows {
		routes, release, err := s.open(r.Context(), row)
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
}

func (s *userServer) serveUser(w http.ResponseWriter, r *http.Request, id string) {
	row, err := s.directory.Get(r.Context(), id)
	if err != nil {
		writeJSON(w, 403, map[string]string{"error": "A known user is required."})
		return
	}
	routes, release, err := s.open(r.Context(), row)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	routes.Handler.ServeHTTP(w, r.WithContext(users.WithID(r.Context(), row.ID)))
}

func withPath(r *http.Request, path string) *http.Request {
	r = r.Clone(r.Context())
	r.URL.Path, r.URL.RawPath = path, ""
	return r
}
