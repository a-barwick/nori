package observability

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HTTPHandler records only known route names and status codes, not URLs,
// headers, query strings, or bodies. Health and polling routes are excluded.
func HTTPHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := safeRoute(r.URL.Path)
		if route == "" {
			ctx := context.WithValue(r.Context(), excludedRequestKey{}, true)
			pending := &atomic.Bool{}
			ctx = context.WithValue(ctx, requestFlushKey{}, pending)
			defer func() {
				panicValue := recover()
				if panicValue != nil && panicValue != http.ErrAbortHandler {
					RecordErrorCategory(ctx, "http.panic", errors.New("HTTP handler panicked"))
					pending.Store(true)
				}
				if pending.Load() {
					flushRequest()
				}
				if panicValue != nil {
					panic(panicValue)
				}
			}()
			result := httpsnoop.CaptureMetrics(next, w, r.WithContext(ctx))
			if result.Code >= 500 {
				RecordErrorCategory(ctx, "http.request", errors.New("HTTP request failed"), attribute.Int("http.response.status_code", result.Code))
				pending.Store(true)
			}
			return
		}
		started := time.Now()
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("nori/http").Start(ctx, "HTTP "+route, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(attribute.String("http.route", route), attribute.String("http.request.method", safeMethod(r.Method))))
		defer func() {
			panicValue := recover()
			if panicValue != nil && panicValue != http.ErrAbortHandler {
				// Capture a caller stack and fixed category without inspecting or
				// exporting the panic value. Preserve net/http's panic behavior.
				RecordErrorCategory(ctx, "http.panic", errors.New("HTTP handler panicked"), attribute.String("http.route", route))
			}
			span.End()
			flushRequest()
			// Include export overhead in locally observed handler time.
			slog.InfoContext(ctx, "nori handler completed", "duration_ms", time.Since(started).Milliseconds(), "http.route", route)
			if panicValue != nil {
				panic(panicValue)
			}
		}()
		result := httpsnoop.CaptureMetrics(next, w, r.WithContext(ctx))
		span.SetAttributes(attribute.Int("http.response.status_code", result.Code))
		if result.Code >= 500 {
			span.SetStatus(codes.Error, "request failed")
			RecordErrorCategory(ctx, "http.request", errors.New("HTTP request failed"), attribute.String("http.route", route), attribute.Int("http.response.status_code", result.Code))
		}
		Event(ctx, "nori request completed", attribute.String("http.route", route), attribute.Int("http.response.status_code", result.Code))
		Metric(ctx, "nori.http.requests", 1)
		Metric(ctx, "nori.http.duration_ms", float64(result.Duration)/float64(time.Millisecond))

	})
}

type requestFlushKey struct{}

// RequireRequestFlush marks real work inside an otherwise excluded request.
// Idle polls stay quiet; the HTTP handler flushes marked requests on return.
func RequireRequestFlush(ctx context.Context) {
	if pending, ok := ctx.Value(requestFlushKey{}).(*atomic.Bool); ok {
		pending.Store(true)
	}
}

func flushRequest() {
	if tracerProvider != nil || sentryEnabled.Load() {
		// Cloud Run can suspend CPU after this handler returns. A short,
		// bounded flush is best effort and never changes the response.
		flush, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
		defer cancel()
		Flush(flush)
	}
}

func safeRoute(path string) string {
	switch path {
	case "/api/turn", "/api/process", "/api/resume", "/api/input", "/api/command", "/api/cancel", "/run", "/cancel", "/browser/close", "/exec", "/navigate", "/screenshot":
		return path
	default:
		return ""
	}
}

func safeMethod(method string) string {
	if method == http.MethodPost {
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
