package observability

import (
	"context"
	"log/slog"
	"math"
	"strings"

	"github.com/getsentry/sentry-go"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var errorCategories = map[string]bool{
	"operation": true, "http.request": true, "http.panic": true, "http.listen": true, "wake.conversation": true, "wake.worker": true,
	"worker.lifecycle": true, "conversation.accept": true, "conversation.snapshot": true,
	"conversation.process": true, "conversation.attempt": true, "conversation.command": true,
	"model.outer": true, "model.classifier": true, "model.task": true,
	"task.delivery": true, "task.model": true, "task.tool": true, "task.question": true,
	"task.publish": true, "task.failed": true, "task.stop_computer": true, "task.browser_cleanup": true,
	"computer.connect": true, "computer.rpc": true,
}

var eventNames = map[string]bool{
	"nori request completed": true, "operation.failed": true,
	"worker.slice.started": true, "worker.slice.completed": true, "worker.slice.failed": true,
	"conversation.accepted": true, "conversation.completed": true, "conversation.failed": true,
	"model.call.completed": true, "model.call.failed": true, "task.completed": true,
	"task.failed": true, "task.tool.completed": true, "task.tool.failed": true,
	"computer.rpc.completed": true, "computer.rpc.failed": true,
}

var metricNames = map[string]bool{
	"nori.http.requests": true, "nori.http.duration_ms": true,
	"nori.model.input_tokens": true, "nori.model.output_tokens": true,
	"nori.model.duration_ms": true, "nori.task.duration_ms": true, "nori.conversation.reply_latency_ms": true,
}

// Event logs only predefined operational names and allowlisted attributes.
// It never forwards the application's unstructured log.Printf output.
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

// Metric records sampled, bounded distributions; these are not exact totals.
func Metric(ctx context.Context, name string, value float64) {
	if !sentryEnabled.Load() || !metricNames[name] || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || !trace.SpanContextFromContext(ctx).IsSampled() {
		return
	}
	sentry.NewMeter(ctx).Distribution(name, value)
}
