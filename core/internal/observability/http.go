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
	// Bound repeated polling diagnostics to one per minute per process. The
	// response marker is still present on every application-generated rejection.
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

// These routes stay untraced on success. Unknown paths never leave the process.
func failedPollRoute(path string) string {
	switch path {
	case "/healthz", "/api/healthz", "/readyz", "/api/readyz", "/metrics", "/v1/snapshot", "/v1/events", "/api/trace", "/api/artifact", "/operation", "/operation/cancel", "/activity":
		return path
	default:
		return "unknown"
	}
}

// RequireRequestFlush marks real work inside an otherwise excluded request.
// Idle polls stay quiet; the HTTP handler flushes marked requests on return.
func RequireRequestFlush(ctx context.Context) {
	if pending, ok := ctx.Value(requestFlushKey{}).(*atomic.Bool); ok {
		pending.Store(true)
	}
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
	case "/api/turn", "/api/process", "/api/resume", "/api/command", "/run", "/cancel", "/browser/close", "/exec", "/navigate", "/screenshot":
		return path
	default:
		return ""
	}
}

func safeMethod(method string) string {
	if method == http.MethodPost || method == http.MethodGet || method == http.MethodHead {
		return method
	}
	return "OTHER"
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
