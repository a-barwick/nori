// Package gateway forwards the Core manifest's client routes.
package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"nori/internal/api"
	"nori/internal/clientauth"
	"nori/internal/httpjson"
	"nori/internal/serviceauth"
)

type Config struct {
	Upstream     *url.URL
	CallerKey    string
	Transport    http.RoundTripper
	ServiceToken oauth2.TokenSource
	Vertex       bool
	PerMinute    int
	Burst        int
}

type Gateway struct {
	cfg    Config
	client *http.Client
	limits *limits
}

func New(cfg Config) (*Gateway, error) {
	if cfg.Upstream == nil || cfg.Upstream.Hostname() == "" || len(cfg.CallerKey) < 32 || cfg.PerMinute < 1 || cfg.Burst < 1 {
		return nil, errors.New("gateway needs a configured upstream, caller signing key, and positive request limits")
	}
	return &Gateway{cfg: cfg, limits: newLimits(cfg.PerMinute, cfg.Burst), client: &http.Client{
		Transport:     cfg.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := httpjson.RequestID(r.Header.Get("X-Request-ID"))
	if id == "" {
		id = uuid.NewString()
	}
	w.Header().Set("X-Request-ID", id)
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet && (r.URL.Path == "/health" || r.URL.Path == "/healthz") {
		httpjson.Write(w, 200, map[string]string{"status": "ok"})
		return
	}

	path := r.URL.Path
	raw := strings.ToLower(r.URL.EscapedPath())
	if strings.Contains(raw, "%2f") || strings.Contains(raw, "%5c") {
		fail(w, 404, "not_found", "Route not found.")
		return
	}
	rule, ok := api.MatchRoute(r.Method, path, false)
	device := ok && slices.Contains(rule.Callers, api.CallerDevice)
	if !ok || (!device && rule.TokenScope == "") {
		rule, ok = api.MatchRoute(r.Method, path, true)
	}
	if !ok || (!device && rule.TokenScope == "") {
		fail(w, 404, "not_found", "Route not found.")
		return
	}
	var token string
	if !device {
		var hasBearer bool
		token, hasBearer = strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		credential, valid := clientauth.Parse(token)
		if !hasBearer || !valid {
			fail(w, 401, "invalid_client_token", "A valid Nori client token is required.")
			return
		}
		if rule.User {
			path = "/users/" + credential.Owner + path
		}
	}
	if !g.limits.allow(token, time.Now()) {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(60/float64(g.cfg.PerMinute))))))
		fail(w, 429, "rate_limited", "Client request limit reached. Try again shortly.")
		return
	}
	slog.InfoContext(r.Context(), "nori.gateway.request", "request_id", id, "route", rule.Path, "method", r.Method)
	var body []byte
	if rule.MaxBytes > 0 && r.Body != nil {
		if r.ContentLength > rule.MaxBytes {
			fail(w, 413, "request_too_large", "Request is too large.")
			return
		}
		var err error
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, rule.MaxBytes))
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				fail(w, 413, "request_too_large", "Request is too large.")
			} else {
				fail(w, 400, "validation_failed", "The request body could not be read.")
			}
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 175*time.Second)
	defer cancel()
	target := *g.cfg.Upstream
	target.Path = strings.TrimRight(target.Path, "/") + clientauth.GatewayPrefix + path
	query := r.URL.Query()
	if slices.Contains(rule.Responses, "text/event-stream") && r.Header.Get("Last-Event-ID") != "" {
		query.Set("after", r.Header.Get("Last-Event-ID"))
	}
	target.RawQuery = query.Encode()
	upstream, err := http.NewRequestWithContext(ctx, r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		fail(w, 502, "upstream_unavailable", "Core could not be reached.")
		return
	}
	for _, header := range []string{"Content-Type", "Accept", "Mcp-Protocol-Version", "Mcp-Session-Id"} {
		if value := r.Header.Get(header); value != "" {
			upstream.Header.Set(header, value)
		}
	}
	assertion, err := serviceauth.SignCaller(g.cfg.CallerKey, "gateway", "")
	if err != nil {
		fail(w, 503, "upstream_unavailable", "Gateway identity is unavailable.")
		return
	}
	upstream.Header.Set(serviceauth.CallerHeader, assertion)
	upstream.Header.Set("X-Request-ID", id)
	if g.cfg.Vertex {
		upstream.Header.Set(httpjson.VertexResponseHeader, "1")
	}
	if token != "" {
		upstream.Header.Set(clientauth.Header, token)
	}
	if g.cfg.ServiceToken != nil {
		service, err := g.cfg.ServiceToken.Token()
		if err != nil {
			fail(w, 503, "upstream_unavailable", "Gateway identity is unavailable.")
			return
		}
		service.SetAuthHeader(upstream)
	}
	response, err := g.client.Do(upstream)
	if err != nil {
		fail(w, 502, "upstream_unavailable", "Core could not be reached.")
		return
	}
	defer response.Body.Close()
	if g.cfg.Vertex {
		if response.StatusCode == http.StatusTooManyRequests {
			seconds, err := strconv.Atoi(response.Header.Get("Retry-After"))
			if err != nil || seconds <= 0 {
				seconds = 10
			}
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			fail(w, 429, "rate_limited", "Core is busy. Try again shortly.")
			return
		}
		if !httpjson.ReadVertexResponse(response) {
			slog.WarnContext(ctx, "nori.gateway.upstream_response", "request_id", id, "status", response.StatusCode)
			fail(w, 502, "upstream_unavailable", "Core returned an unsupported response.")
			return
		}
	}
	relay(w, response, rule)
}

func relay(w http.ResponseWriter, response *http.Response, rule api.RouteRule) {
	media, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	switch {
	case rule.TokenScope == "mcp" && response.StatusCode == http.StatusAccepted && media == "":
		w.WriteHeader(response.StatusCode)
		return
	case response.StatusCode == 204 || response.StatusCode == 304:
	case response.StatusCode >= 300 && response.StatusCode < 400:
		fail(w, 502, "unsupported_response", "Core returned an unsupported response.")
		return
	case !slices.Contains(rule.Responses, media):
		fail(w, 502, "unsupported_response", "Core returned an unsupported response.")
		return
	}
	httpjson.CopyGatewayHeaders(w.Header(), response.Header)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if media == "text/event-stream" {
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(response.StatusCode)
		flush := http.NewResponseController(w)
		if flush.Flush() == nil {
			_, _ = io.Copy(streamWriter{w, flush}, response.Body)
		}
		return
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

type streamWriter struct {
	io.Writer
	flush *http.ResponseController
}

func (s streamWriter) Write(data []byte) (int, error) {
	n, err := s.Writer.Write(data)
	if err == nil {
		err = s.flush.Flush()
	}
	return n, err
}

func fail(w http.ResponseWriter, status int, code, message string) {
	httpjson.Write(w, status, map[string]any{
		"error": message, "code": code, "accepted": false, "retryable": status == 429 || status >= 500,
	})
}
