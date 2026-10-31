package observability

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"golang.org/x/time/rate"
)

// Only explicitly selected operational fields cross the export boundary.
// This also protects Sentry when an Arize span opts into model text.
var operationalKeys = map[string]bool{
	"openinference.span.kind": true, "session.id": true,
	"gen_ai.operation.name": true, "gen_ai.provider.name": true, "gen_ai.request.model": true,
	"gen_ai.usage.input_tokens": true, "gen_ai.usage.output_tokens": true,
	"gen_ai.usage.cache_read.input_tokens": true, "gen_ai.usage.reasoning.output_tokens": true,
	"llm.model_name": true, "llm.token_count.prompt": true, "llm.token_count.completion": true, "llm.token_count.total": true,
	"llm.cost.total": true,
	"tool.name":      true, "error.type": true,
	"nori.error.category": true,
	"http.request.method": true, "http.route": true, "http.response.status_code": true,
	"nori.input.id": true, "nori.turn.id": true, "nori.attempt.id": true, "nori.attempt.number": true,
	"nori.task.id": true, "nori.task.revision": true, "nori.task.status": true,
	"nori.operation.id": true, "nori.tool.outcome": true, "nori.tool.exit_code": true,
	"nori.tool.call_count": true, "nori.command.count": true, "nori.command.id": true,
	"nori.classification.intent": true, "nori.computer.operation": true, "nori.computer.allow_start": true, "nori.computer.reconnect_count": true,
	"nori.workflow.id": true, "nori.workflow.run_id": true, "nori.activity.id": true, "nori.activity.attempt": true,
	"nori.reply.status": true, "nori.output.present": true, "nori.conversation.input_count": true,
	"nori.conversation.restart_count": true, "nori.cost.usd": true, "nori.cost.status": true,
}

func allowedAttribute(kv attribute.KeyValue, content bool) (attribute.KeyValue, bool) {
	key := string(kv.Key)
	if content && (key == "input.value" || key == "output.value") && kv.Value.Type() == attribute.STRING {
		return ContentAttribute(key, kv.Value.AsString())
	}
	if !operationalKeys[key] && !(strings.HasPrefix(key, "nori.phase.") && strings.HasSuffix(key, "_ms")) {
		return attribute.KeyValue{}, false
	}
	switch kv.Value.Type() {
	case attribute.STRING:
		return attribute.String(key, redactText(kv.Value.AsString(), 256)), true
	case attribute.BOOL, attribute.INT64, attribute.FLOAT64:
		return kv, true
	default:
		return attribute.KeyValue{}, false
	}
}

type filteredSpan struct {
	sdktrace.ReadOnlySpan
	content bool
}

func (s filteredSpan) Name() string { return redactText(s.ReadOnlySpan.Name(), 120) }
func (s filteredSpan) Attributes() []attribute.KeyValue {
	attrs := s.ReadOnlySpan.Attributes()
	result := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		if safe, ok := allowedAttribute(attr, s.content); ok {
			result = append(result, safe)
		}
	}
	return result
}
func (s filteredSpan) Events() []sdktrace.Event { return nil }
func (s filteredSpan) Links() []sdktrace.Link {
	links := append([]sdktrace.Link(nil), s.ReadOnlySpan.Links()...)
	for i := range links {
		links[i].Attributes = nil
	}
	return links
}
func (s filteredSpan) Status() sdktrace.Status {
	status := s.ReadOnlySpan.Status()
	status.Description = ""
	return status
}

type filteredExporter struct {
	inner   sdktrace.SpanExporter
	arize   bool
	limiter *rate.Limiter
}

func (e *filteredExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	filtered := make([]sdktrace.ReadOnlySpan, 0, len(spans))
	for _, span := range spans {
		if e.arize {
			ai := false
			for _, attr := range span.Attributes() {
				if attr.Key == "openinference.span.kind" && attr.Value.AsString() != "" {
					ai = true
					break
				}
			}
			if !ai {
				continue
			}
		}
		if !e.limiter.Allow() {
			continue
		}
		filtered = append(filtered, filteredSpan{ReadOnlySpan: span, content: e.arize})
	}
	if len(filtered) == 0 {
		return nil
	}
	return e.inner.ExportSpans(ctx, filtered)
}

func (e *filteredExporter) Shutdown(ctx context.Context) error { return e.inner.Shutdown(ctx) }
