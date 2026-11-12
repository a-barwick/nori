package api

import (
	"log/slog"
	"mime"
	"net"
	"net/http"
	"slices"
	"strings"

	"github.com/felixge/httpsnoop"
	"nori/internal/clientauth"
	"nori/internal/httpjson"
	"nori/internal/serviceauth"
)

type AccessConfig struct {
	Local   bool
	Callers serviceauth.Callers
}

type caller struct {
	kind, subject, service string
	token                  string
}

func (c AccessConfig) mode() string {
	if c.Callers.Mode != "" {
		return c.Callers.Mode
	}
	if c.Local {
		return "enforce"
	}
	return "off"
}

func (c AccessConfig) identify(r *http.Request) caller {
	role, subject := c.Callers.Verify(r.Header.Get(serviceauth.CallerHeader))
	local := false
	if c.Local {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		local = err == nil && net.ParseIP(host).IsLoopback()
	}
	token := clientToken(r)
	if r.Header.Get(clientauth.Header) != "" || strings.HasPrefix(token, "nori_") || strings.HasPrefix(token, "nori2_") {
		if local || role == "gateway" {
			return caller{kind: CallerClient, service: role, token: token}
		}
		return caller{kind: CallerClient, token: token}
	}
	if role == "" && r.Method == "POST" && c.Callers.QueueValid(r.URL.Path, r.Header.Get(serviceauth.QueueHeader)) {
		return caller{kind: CallerInternal}
	}
	if role != "" {
		return caller{kind: role, subject: subject, service: role}
	}
	if local {
		return caller{kind: CallerLocal}
	}
	return caller{kind: "unknown"}
}

type callerKey struct{}
type verifiedClientKey struct{}

func requestCaller(r *http.Request) caller {
	value, _ := r.Context().Value(callerKey{}).(caller)
	return value
}

func routeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiError{Error: message, Code: code})
}

func (c AccessConfig) refuse(w http.ResponseWriter, r *http.Request, rule RouteRule, identity caller, status int, code, message string, strict bool) bool {
	mode := c.mode()
	if !strict && mode == "off" {
		return false
	}
	observe := !strict && mode == "observe"
	label := "nori.access.refused"
	if observe {
		label = "nori.access.would_refuse"
	}
	slog.WarnContext(r.Context(), label, "route", rule.Path, "method", rule.Method, "caller", identity.kind, "code", code)
	if !observe {
		routeError(w, status, code, message)
	}
	return !observe
}

func (c AccessConfig) authorize(w http.ResponseWriter, r *http.Request, rule RouteRule, owner string) bool {
	identity := requestCaller(r)
	if slices.Contains(rule.Callers, CallerHealth) {
		return true
	}
	if identity.kind == CallerClient {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		local := c.Local && net.ParseIP(host).IsLoopback()
		if !local && identity.service != "gateway" {
			return !c.refuse(w, r, rule, identity, 401, "unidentified_caller", "A verified gateway is required.", true)
		}
		if rule.TokenScope == "" {
			return !c.refuse(w, r, rule, identity, 403, "client_route_forbidden", "This route does not accept client tokens.", true)
		}
		credential, ok := clientauth.Parse(identity.token)
		if !ok {
			return !c.refuse(w, r, rule, identity, 401, "invalid_client_token", "A valid Nori client token is required.", true)
		}
		if owner != "" && owner != credential.Owner {
			return !c.refuse(w, r, rule, identity, 403, "wrong_owner", "The client token belongs to a different user.", true)
		}
		return true
	}
	if slices.Contains(rule.Callers, CallerDevice) {
		if identity.kind == CallerLocal || identity.kind == "gateway" {
			return true
		}
		return !c.refuse(w, r, rule, identity, 403, "device_gateway_required", "Client authorization requires the gateway.", true)
	}
	if identity.kind == "gateway" {
		return !c.refuse(w, r, rule, identity, 401, "invalid_client_token", "A valid Nori client token is required.", true)
	}
	if identity.kind == CallerBrowser && identity.subject != owner {
		return !c.refuse(w, r, rule, identity, 403, "wrong_owner", "The browser session belongs to a different user.", false)
	}
	if slices.Contains(rule.Callers, identity.kind) {
		return true
	}
	if slices.Contains(rule.Callers, CallerInternal) {
		return !c.refuse(w, r, rule, identity, 403, "internal_only", "An internal service is required.", false)
	}
	if rule.TokenScope == "mcp" {
		return !c.refuse(w, r, rule, identity, 401, "invalid_client_token", "A valid Nori client token is required.", true)
	}
	return !c.refuse(w, r, rule, identity, 401, "unidentified_caller", "This caller is not authorized for this route.", false)
}

func (c AccessConfig) limit(w http.ResponseWriter, r *http.Request, rule RouteRule) bool {
	identity := requestCaller(r)
	strict := identity.kind == CallerClient || c.mode() == "enforce"
	if rule.MaxBytes == 0 {
		return true
	}
	if r.ContentLength > rule.MaxBytes && c.refuse(w, r, rule, identity, 413, "request_too_large", "Request is too large.", strict) {
		return false
	}
	if strict {
		r.Body = http.MaxBytesReader(w, r.Body, rule.MaxBytes)
	}
	return true
}

// ServeMediaType validates the selected response before the handler's first
// write. httpsnoop preserves Flush and the response controller used by SSE.
func (c AccessConfig) serve(w http.ResponseWriter, r *http.Request, rule RouteRule, handler http.Handler) {
	if requestCaller(r).service == "gateway" && rule.Path != "/mcp" {
		if id := httpjson.RequestID(r.Header.Get("X-Request-ID")); id != "" {
			w.Header().Set("X-Request-ID", id)
			slog.InfoContext(r.Context(), "nori.core.gateway_request", "request_id", id, "route", rule.Path, "method", r.Method)
		}
	}
	if !c.limit(w, r, rule) {
		return
	}
	if c.mode() == "off" && requestCaller(r).kind != CallerClient {
		handler.ServeHTTP(w, r)
		return
	}
	blocked, started := false, false
	check := func(status int) {
		if started || status < 200 {
			return
		}
		started = true
		if status == http.StatusNoContent || status == http.StatusNotModified {
			return
		}
		media, _, _ := mime.ParseMediaType(w.Header().Get("Content-Type"))
		if slices.Contains(rule.Responses, media) || rule.TokenScope == "mcp" && status == http.StatusAccepted && media == "" {
			return
		}
		if c.mode() != "observe" || requestCaller(r).kind == CallerClient {
			w.Header().Del("Content-Disposition")
			w.Header().Del("Content-Length")
			w.Header().Del("Location")
		}
		blocked = c.refuse(w, r, rule, requestCaller(r), 502, "unsupported_response", "The runtime returned an unsupported response.", c.mode() == "enforce" || requestCaller(r).kind == CallerClient)
	}
	wrapped := httpsnoop.Wrap(w, httpsnoop.Hooks{
		WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(status int) {
				check(status)
				if !blocked {
					next(status)
				}
			}
		},
		Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(body []byte) (int, error) {
				check(http.StatusOK)
				if blocked {
					return len(body), nil
				}
				return next(body)
			}
		},
	})
	handler.ServeHTTP(wrapped, r)
}
