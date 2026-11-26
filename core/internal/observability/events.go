package observability

import (
	"context"
	"log/slog"
	"math"
	"strings"

	"github.com/getsentry/sentry-go"
	sentryattr "github.com/getsentry/sentry-go/attribute"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var errorCategories = map[string]bool{
	"task.history.sync": true, "task.history.model": true, "task.history.tool": true,
	"task.history.position": true,
	"task.history.question": true, "task.history.append_result": true, "task.history.publish": true,
	"task.completion.check": true, "task.completion.jev": true,
	"task.evidence": true, "task.history": true, "task.completion": true,
	"startup": true, "operation": true, "http.request": true, "http.panic": true, "http.listen": true, "wake.conversation": true, "wake.worker": true,
	"worker.lifecycle": true, "conversation.accept": true, "conversation.snapshot": true,
	"conversation.process": true, "conversation.attempt": true, "conversation.command": true,
	"model.outer": true, "model.classifier": true, "model.task": true,
	"task.delivery": true, "task.model": true, "task.tool": true, "task.question": true,
	"task.publish": true, "task.failed": true,
}

var eventNames = map[string]bool{
	"model.call.denied":                   true,
	"conversation.denied":                 true,
	"conversation.command_acknowledgment": true,
	"task.incomplete":                     true,
	"conversation.hold.accepted":          true, "conversation.hold.answered": true, "conversation.correction.accepted": true,
	"task.operation.recorded": true, "task.operation.interrupted": true, "task.operation.retry": true,
	"nori request completed": true, "operation.failed": true,
	"worker.slice.started": true, "worker.slice.completed": true, "worker.slice.failed": true,
	"conversation.accepted": true, "conversation.completed": true, "conversation.failed": true,
	"model.call.completed": true, "model.call.failed": true, "task.completed": true,
	"task.failed": true, "task.tool.completed": true, "task.tool.failed": true,
}

var metricNames = map[string]bool{
	"nori.http.requests": true, "nori.http.duration_ms": true,
	"nori.model.input_tokens": true, "nori.model.output_tokens": true,
	"nori.model.duration_ms": true, "nori.task.duration_ms": true, "nori.conversation.reply_latency_ms": true,
}

func Event(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	if !eventNames[name] {
		return
	}
	local := make([]any, 0, len(attrs)*2)
	var entry sentry.LogEntry
	if sentryEnabled.Load() {
		if strings.HasSuffix(name, ".failed") {
			entry = sentry.NewLogger(ctx).Error()
		} else {
			entry = sentry.NewLogger(ctx).Info()
		}
	}
	for _, attr := range attrs {
		safe, ok := allowedAttribute(attr, false)
		if !ok {
			continue
		}
		key := string(safe.Key)
		local = append(local, key, safe.Value.AsInterface())
		if entry == nil {
			continue
		}
		switch safe.Value.Type() {
		case attribute.STRING:
			entry = entry.String(key, safe.Value.AsString())
		case attribute.BOOL:
			entry = entry.Bool(key, safe.Value.AsBool())
		case attribute.INT64:
			entry = entry.Int64(key, safe.Value.AsInt64())
		case attribute.FLOAT64:
			entry = entry.Float64(key, safe.Value.AsFloat64())
		}
	}
	if strings.HasSuffix(name, ".failed") {
		slog.ErrorContext(ctx, name, local...)
	} else {
		slog.InfoContext(ctx, name, local...)
	}
	if entry != nil {
		entry.Emit(name)
	}
}

var metricDimensions = map[string]bool{
	"gen_ai.provider.name": true, "gen_ai.request.model": true, "http.route": true,
	"http.response.status_code": true, "nori.task.status": true, "nori.reply.status": true,
	"nori.metric.sampled": true,
}

func Metric(ctx context.Context, name string, value float64) {
	if !sentryEnabled.Load() || !metricNames[name] || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || !trace.SpanContextFromContext(ctx).IsSampled() {
		return
	}
	attrs := []sentryattr.Builder{sentryattr.Bool("nori.metric.sampled", true)}
	if span, ok := trace.SpanFromContext(ctx).(sdktrace.ReadOnlySpan); ok {
		for _, attr := range span.Attributes() {
			if !metricDimensions[string(attr.Key)] {
				continue
			}
			switch attr.Value.Type() {
			case attribute.STRING:
				attrs = append(attrs, sentryattr.String(string(attr.Key), redactText(attr.Value.AsString(), 120)))
			case attribute.INT64:
				attrs = append(attrs, sentryattr.Int64(string(attr.Key), attr.Value.AsInt64()))
			}
		}
	}
	opts := []sentry.MeterOption{sentry.WithAttributes(attrs...)}
	if strings.HasSuffix(name, "_ms") {
		opts = append(opts, sentry.WithUnit("millisecond"))
	}
	if name == "nori.http.requests" {
		sentry.NewMeter(ctx).Count(name, int64(value), opts...)
		return
	}
	sentry.NewMeter(ctx).Distribution(name, value, opts...)
}
