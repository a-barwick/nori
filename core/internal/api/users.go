package api

import (
	"context"
	"net/http"
	"slices"
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
	access    AccessConfig
}

// The authenticated web server supplies the resolved user ID. Only internal resume can fan out.
func UserHandler(directory users.Directory, open func(context.Context, users.User) (UserRoutes, func(), error), access AccessConfig, model string, devices clientauth.DeviceStore) http.Handler {
	s := &userServer{directory: directory, open: open, model: model, devices: devices, access: access}
	root := newRouteMux(false, false)
	s.register(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if access.Local && (r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "") {
			routeError(w, 403, "browser_proxy_required", "Use the web server to access the runtime.")
			return
		}
		identity := access.identify(r)
		// Older Core versions reject this namespace without reaching user routes.
		if path, gateway := strings.CutPrefix(r.URL.Path, clientauth.GatewayPrefix+"/"); gateway {
			if identity.service != "gateway" {
				routeError(w, 401, "unidentified_caller", "A verified gateway is required.")
				return
			}
			r = withPath(r, "/"+path)
		}
		r = r.WithContext(context.WithValue(r.Context(), callerKey{}, identity))
		if handler, pattern := root.mux.Handler(r); pattern != "" && (pattern == r.Method+" "+r.URL.Path || pattern == r.URL.Path) {
			rule, _ := MatchRoute(r.Method, r.URL.Path, false)
			if access.authorize(w, r, rule, "") {
				access.serve(w, r, rule, handler)
			}
			return
		}
		if raw, ok := strings.CutPrefix(r.URL.Path, "/users/"); ok {
			id, path, found := strings.Cut(raw, "/")
			if found && users.ValidID(id) {
				s.serveUser(w, withPath(r, "/"+path), id)
				return
			}
		}
		routeError(w, 404, "not_found", "Route not found.")
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
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		routeError(w, 405, "method_not_allowed", "Use POST for this MCP endpoint.")
		return
	}
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
	rule, ok := MatchRoute(r.Method, r.URL.Path, true)
	if !ok {
		routeError(w, 404, "not_found", "Route not found.")
		return
	}
	if !s.access.authorize(w, r, rule, id) {
		return
	}
	identity := requestCaller(r)
	row, err := s.directory.Get(r.Context(), id)
	if err != nil {
		if identity.kind == CallerClient {
			clientUnauthorized(w)
		} else {
			routeError(w, 403, "unknown_user", "A known user is required.")
		}
		return
	}
	routes, release, err := s.open(r.Context(), row)
	if err != nil {
		writeError(w, err)
		return
	}
	defer release()
	ctx := users.WithID(r.Context(), row.ID)
	if identity.kind == CallerClient {
		credential, _ := clientauth.Parse(identity.token)
		client, err := lookupClient(ctx, routes.ClientStore, credential)
		if err != nil {
			s.access.refuse(w, r, rule, identity, 401, "invalid_client_token", "A valid Nori client token is required.", true)
			return
		}
		if rule.TokenScope != "mcp" && rule.TokenScope != "authenticated" && !slices.Contains(client.Scopes, rule.TokenScope) {
			s.access.refuse(w, r, rule, identity, 403, "insufficient_scope", "This command requires the "+rule.TokenScope+" scope.", true)
			return
		}
		recordClientUse(ctx, routes.ClientStore, client.ID)
		ctx = context.WithValue(ctx, verifiedClientKey{}, client)
	}
	s.access.serve(w, r.WithContext(ctx), rule, routes.Handler)
}

func withPath(r *http.Request, path string) *http.Request {
	r = r.Clone(r.Context())
	r.URL.Path, r.URL.RawPath = path, ""
	return r
}
