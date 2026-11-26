package observability

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func InjectTrace(ctx context.Context) map[string]string {
	if !TracingEnabled() {
		return nil
	}
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	if parent := carrier.Get("traceparent"); parent != "" {
		return map[string]string{"traceparent": parent}
	}
	return nil
}

func ExtractTrace(ctx context.Context, headers map[string]string) context.Context {
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": headers["traceparent"]})
}

type sentryTraceContext struct{}

func (sentryTraceContext) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	if !TracingEnabled() {
		return
	}
	for key, value := range InjectTrace(ctx) {
		carrier.Set(key, value)
	}
	sc := trace.SpanContextFromContext(ctx)
	if sc.IsValid() {
		sampled := "0"
		if sc.IsSampled() {
			sampled = "1"
		}
		carrier.Set("sentry-trace", sc.TraceID().String()+"-"+sc.SpanID().String()+"-"+sampled)
	}
}

func (sentryTraceContext) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	w3c := ExtractTrace(context.Background(), map[string]string{"traceparent": carrier.Get("traceparent")})
	if sc := trace.SpanContextFromContext(w3c); sc.IsValid() {
		return trace.ContextWithRemoteSpanContext(ctx, sc)
	}
	parts := strings.Split(carrier.Get("sentry-trace"), "-")
	if len(parts) != 3 || (parts[2] != "0" && parts[2] != "1") {
		return ctx
	}
	id, err := trace.TraceIDFromHex(parts[0])
	if err != nil {
		return ctx
	}
	spanID, err := trace.SpanIDFromHex(parts[1])
	if err != nil {
		return ctx
	}
	flags := trace.TraceFlags(0)
	if parts[2] == "1" {
		flags = trace.FlagsSampled
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: id, SpanID: spanID, TraceFlags: flags, Remote: true})
	if !sc.IsValid() {
		return ctx
	}
	return trace.ContextWithRemoteSpanContext(ctx, sc)
}

func (sentryTraceContext) Fields() []string { return []string{"traceparent", "sentry-trace"} }
