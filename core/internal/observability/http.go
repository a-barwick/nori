package observability

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"nori/internal/users"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func HTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = httpsnoop.Wrap(w, httpsnoop.Hooks{WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(code int) {
				if code == http.StatusTooManyRequests {
					w.Header().Set("X-Nori-Rejection-Source", "runtime")
				}
				next(code)
			}
		}})
		normalized := normalizeUserPath(r.URL.Path)
		route := safeRoute(normalized)
		clientPlatform := safeClientPlatform(r.Header.Get("X-Nori-Client"))
		correlation := &httpCorrelation{platform: clientPlatform}
		ctx := context.WithValue(r.Context(), httpCorrelationKey{}, correlation)
		if hasBearerAuth(r) {
			ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))
		}
		var baseAttrs []attribute.KeyValue
		if clientPlatform != "" {
			baseAttrs = append(baseAttrs, attribute.String("nori.client.platform", clientPlatform))
		}
		if route == "" {
			attrs := append([]attribute.KeyValue{attribute.String("http.route", failedPollRoute(normalized)), attribute.String("http.request.method", safeMethod(r.Method))}, baseAttrs...)
			pending := &atomic.Bool{}
			ctx = context.WithValue(ctx, requestFlushKey{}, pending)
			statusCode := 0
			defer func() {
				panicValue := recover()
				if panicValue != nil && panicValue != http.ErrAbortHandler {
					RecordErrorCategory(ctx, "http.panic", errors.New("HTTP handler panicked"), attrs...)
					pending.Store(true)
				}
				if pending.Load() && (statusCode != http.StatusUnauthorized || panicValue != nil) {
					flushRequest()
				}
				if panicValue != nil {
					panic(panicValue)
				}
			}()
			result := httpsnoop.CaptureMetrics(next, w, r.WithContext(ctx))
			statusCode = result.Code
			if reportHTTPFailure(ctx, result.Code, attrs) {
				RecordErrorCategory(ctx, "http.request", httpFailure(result.Code), append(attrs, attribute.Int("http.response.status_code", result.Code))...)
				pending.Store(true)
			}
			return
		}
		started := time.Now()
		spanAttrs := append([]attribute.KeyValue{attribute.String("http.route", route), attribute.String("http.request.method", safeMethod(r.Method))}, baseAttrs...)
		ctx, span := otel.Tracer("nori/http").Start(ctx, "HTTP "+route, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(spanAttrs...))
		statusCode := 0
		defer func() {
			panicValue := recover()
			if panicValue != nil && panicValue != http.ErrAbortHandler {
				RecordErrorCategory(ctx, "http.panic", errors.New("HTTP handler panicked"), append([]attribute.KeyValue{attribute.String("http.route", route)}, baseAttrs...)...)
			}
			span.End()
			if statusCode != http.StatusUnauthorized || panicValue != nil {
				flushRequest()
				slog.InfoContext(ctx, "nori handler completed", "duration_ms", time.Since(started).Milliseconds(), "http.route", route, "nori.user.id", correlation.userID, "nori.client.platform", clientPlatform)
			}
			if panicValue != nil {
				panic(panicValue)
			}
		}()
		result := httpsnoop.CaptureMetrics(next, w, r.WithContext(ctx))
		statusCode = result.Code
		span.SetAttributes(attribute.Int("http.response.status_code", result.Code))
		if result.Code == http.StatusUnauthorized {
			return
		}
		if reportHTTPFailure(ctx, result.Code, spanAttrs) {
			span.SetStatus(codes.Error, "request failed")
			RecordErrorCategory(ctx, "http.request", httpFailure(result.Code), append(spanAttrs, attribute.Int("http.response.status_code", result.Code))...)
		}
		Event(ctx, "nori request completed", attribute.String("http.route", route), attribute.Int("http.response.status_code", result.Code))
		Metric(ctx, "nori.http.requests", 1)
		Metric(ctx, "nori.http.duration_ms", float64(result.Duration)/float64(time.Millisecond))

	})
}

func hasBearerAuth(r *http.Request) bool {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	return ok && scheme == "Bearer" && token != ""
}

type httpCorrelationKey struct{}

type httpCorrelation struct {
	platform string
	userID   string
}

// BindHTTPUser adds the authorized directory ID to the request's telemetry.
func BindHTTPUser(ctx context.Context, id string) context.Context {
	ctx = users.WithID(ctx, id)
	if !users.ValidID(id) {
		return ctx
	}
	if correlation, ok := ctx.Value(httpCorrelationKey{}).(*httpCorrelation); ok {
		correlation.userID = id
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("nori.user.id", id))
	return ctx
}

func correlationAttributes(ctx context.Context) []attribute.KeyValue {
	id := users.ID(ctx)
	var attrs []attribute.KeyValue
	if correlation, ok := ctx.Value(httpCorrelationKey{}).(*httpCorrelation); ok {
		if id == "" {
			id = correlation.userID
		}
		if correlation.platform != "" {
			attrs = append(attrs, attribute.String("nori.client.platform", correlation.platform))
		}
	}
	if users.ValidID(id) {
		attrs = append(attrs, attribute.String("nori.user.id", id))
	}
	return attrs
}

func safeClientPlatform(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ios":
		return "ios"
	case "web":
		return "web"
	default:
		return ""
	}
}

func normalizeUserPath(path string) string {
	if prefix, ok := strings.CutPrefix(path, "/users/"); ok {
		if id, rest, found := strings.Cut(prefix, "/"); found && id != "" {
			return "/" + rest
		}
	}
	return path
}

var lastHTTPRejection atomic.Int64

func reportHTTPFailure(ctx context.Context, status int, attrs []attribute.KeyValue) bool {
	if status != http.StatusTooManyRequests {
		return status >= 500
	}
	now := time.Now().Unix()
	last := lastHTTPRejection.Load()
	if now-last < 60 || !lastHTTPRejection.CompareAndSwap(last, now) {
		return false
	}
	Event(ctx, "nori request completed", append(attrs, attribute.Int("http.response.status_code", status))...)
	return true
}

type requestFlushKey struct{}

type httpFailure int

func (httpFailure) Error() string         { return "HTTP request failed" }
func (e httpFailure) HTTPStatusCode() int { return int(e) }

func failedPollRoute(path string) string {
	switch path {
	case "/healthz", "/api/healthz", "/readyz", "/api/readyz", "/metrics",
		"/v1/events", "/v1/conversations/assistant/events",
		"/api/trace", "/api/artifact", "/operation", "/operation/cancel", "/activity":
		return path
	}
	if rest, ok := strings.CutPrefix(path, "/v1/computer/sessions/"); ok {
		if id, suffix, found := strings.Cut(rest, "/"); found && id != "" && suffix == "ws" {
			return "/v1/computer/sessions/{id}/ws"
		}
	}
	return "unknown"
}

func flushRequest() {
	if tracerProvider.Load() != nil || sentryEnabled.Load() {
		flush, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		Flush(flush)
	}
}

func safeRoute(path string) string {
	switch path {
	case "/api/turn", "/api/process", "/api/resume", "/api/retry", "/api/command",
		"/api/delivered", "/api/seen",
		"/api/users/resolve", "/api/users/redeem", "/api/oauth/google-workspace/callback",
		"/run", "/cancel", "/browser/close", "/exec", "/navigate", "/screenshot":
		return path
	}
	return matchV1Route(path)
}

func matchV1Route(path string) string {
	rest, ok := strings.CutPrefix(path, "/v1/")
	if !ok || rest == "" {
		return ""
	}
	segs := strings.Split(rest, "/")
	for _, s := range segs {
		if s == "" {
			return ""
		}
	}
	switch segs[0] {
	case "capabilities", "snapshot", "operations", "search", "feedback", "preferences":
		if len(segs) == 1 {
			return path
		}
	case "assistant":
		if len(segs) == 2 && segs[1] == "summary" {
			return "/v1/assistant/summary"
		}
	case "conversations":
		if len(segs) >= 2 && segs[1] == "assistant" {
			switch len(segs) {
			case 2:
				return "/v1/conversations/assistant"
			case 3:
				switch segs[2] {
				case "messages", "operations", "cancel", "retry", "uploads":
					return "/v1/conversations/assistant/" + segs[2]
				}
			case 4:
				if segs[2] == "uploads" {
					return "/v1/conversations/assistant/uploads/{id}"
				}
			}
		}
	case "uploads":
		switch len(segs) {
		case 1:
			return "/v1/uploads"
		case 2:
			return "/v1/uploads/{id}"
		}
	case "results":
		switch len(segs) {
		case 2:
			return "/v1/results/{id}"
		case 3:
			if segs[2] == "file" {
				return "/v1/results/{id}/file"
			}
		}
	case "integrations":
		switch len(segs) {
		case 1:
			return "/v1/integrations"
		case 2:
			return "/v1/integrations/{id}"
		}
	case "connections":
		switch len(segs) {
		case 1:
			return "/v1/connections"
		case 2:
			return "/v1/connections/{id}"
		case 3:
			if segs[1] == "oauth" && segs[2] == "callback" {
				return "/v1/connections/oauth/callback"
			}
			if segs[2] == "actions" || segs[2] == "resources" {
				return "/v1/connections/{id}/" + segs[2]
			}
		case 4:
			if segs[2] == "google-drive" && segs[3] == "picker" {
				return "/v1/connections/{id}/google-drive/picker"
			}
		}
	case "permissions":
		switch len(segs) {
		case 1:
			return "/v1/permissions"
		case 2:
			return "/v1/permissions/{id}"
		case 3:
			if segs[2] == "actions" {
				return "/v1/permissions/{id}/actions"
			}
		}
	case "interactions":
		switch len(segs) {
		case 1:
			return "/v1/interactions"
		case 2:
			return "/v1/interactions/{id}"
		case 3:
			if segs[2] == "responses" {
				return "/v1/interactions/{id}/responses"
			}
		}
	case "mutations":
		if len(segs) == 2 {
			return "/v1/mutations/{id}"
		}
	case "tools":
		if len(segs) == 1 {
			return "/v1/tools"
		}
		return "/v1/tools/{id}"
	case "work":
		switch len(segs) {
		case 1:
			return "/v1/work"
		case 2:
			return "/v1/work/{id}"
		case 3:
			if segs[2] == "operations" || segs[2] == "actions" {
				return "/v1/work/{id}/" + segs[2]
			}
		}
	case "library":
		switch len(segs) {
		case 1:
			return "/v1/library"
		case 2:
			return "/v1/library/{id}"
		case 3:
			switch segs[2] {
			case "versions", "file", "actions":
				return "/v1/library/{id}/" + segs[2]
			}
		}
	case "memory":
		switch len(segs) {
		case 1:
			return "/v1/memory"
		case 2:
			switch segs[1] {
			case "actions", "search", "logs", "working":
				return "/v1/memory/" + segs[1]
			default:
				return "/v1/memory/{id}"
			}
		case 3:
			if segs[1] == "logs" {
				return "/v1/memory/logs/{date}"
			}
			if segs[2] == "actions" && segs[1] != "actions" && segs[1] != "search" && segs[1] != "working" {
				return "/v1/memory/{id}/actions"
			}
		case 4:
			if segs[1] == "working" && segs[3] == "actions" {
				return "/v1/memory/working/{id}/actions"
			}
		case 5:
			if segs[1] == "logs" && segs[4] == "actions" {
				return "/v1/memory/logs/{date}/{id}/actions"
			}
		}
	case "diagnostics":
		switch len(segs) {
		case 1:
			return "/v1/diagnostics"
		case 2:
			if segs[1] == "actions" {
				return "/v1/diagnostics/actions"
			}
		}
	case "automations":
		switch len(segs) {
		case 1:
			return "/v1/automations"
		case 2:
			if segs[1] == "preview" {
				return "/v1/automations/preview"
			}
			return "/v1/automations/{id}"
		case 3:
			if segs[2] == "actions" || segs[2] == "occurrences" {
				return "/v1/automations/{id}/" + segs[2]
			}
		}
	case "inbox":
		switch len(segs) {
		case 1:
			return "/v1/inbox"
		case 3:
			if segs[2] == "actions" {
				return "/v1/inbox/{id}/actions"
			}
		}
	case "mcp":
		switch len(segs) {
		case 1:
			return "/v1/mcp"
		case 2:
			return "/v1/mcp/{id}"
		case 3:
			switch segs[2] {
			case "actions", "tools", "credential":
				return "/v1/mcp/{id}/" + segs[2]
			}
		}
	case "computer":
		switch len(segs) {
		case 1:
			return "/v1/computer"
		case 2:
			switch segs[1] {
			case "wake", "sleep", "reset", "checkpoints", "restore", "sessions":
				return "/v1/computer/" + segs[1]
			}
		case 3:
			if segs[1] == "sessions" {
				return "/v1/computer/sessions/{id}"
			}
		}
	case "settings":
		switch len(segs) {
		case 2:
			switch segs[1] {
			case "reset", "models", "model-keys", "api-costs", "home-connectors":
				return "/v1/settings/" + segs[1]
			}
		case 3:
			switch segs[1] {
			case "model-keys":
				return "/v1/settings/model-keys/{id}"
			case "home-connectors":
				return "/v1/settings/home-connectors/{id}"
			case "account":
				if segs[2] == "delete" {
					return "/v1/settings/account/delete"
				}
			}
		case 4:
			if segs[1] == "home-connectors" {
				if segs[2] == "pairing" && (segs[3] == "lookup" || segs[3] == "approve") {
					return "/v1/settings/home-connectors/pairing/" + segs[3]
				}
				if segs[3] == "test" {
					return "/v1/settings/home-connectors/{id}/test"
				}
			}
		}
	}
	return ""
}

func safeMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return method
	default:
		return "OTHER"
	}
}

type traceLogHandler struct{ slog.Handler }

func (h traceLogHandler) Handle(ctx context.Context, record slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		record.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, record)
}
func (h traceLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceLogHandler{Handler: h.Handler.WithAttrs(attrs)}
}
func (h traceLogHandler) WithGroup(name string) slog.Handler {
	return traceLogHandler{Handler: h.Handler.WithGroup(name)}
}
