package observability

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Only explicitly selected operational fields cross the export boundary.
// This also protects Sentry when an Arize span opts into model text.
var operationalKeys = map[string]bool{
	"nori.completion.failure_count": true, "nori.completion.verified": true, "nori.completion.available": true,
	"nori.completion.score": true, "nori.completion.choice": true,
	"nori.hold.id": true, "nori.hold.count": true, "nori.hold.status": true, "nori.command.interrupt": true,
	"nori.operation.effect_class": true, "nori.operation.attempt": true, "nori.operation.stop_confirmed": true,
	"nori.history.bytes": true, "nori.history.blocks": true,
	"openinference.span.kind": true, "session.id": true,
	"gen_ai.request.reasoning_effort": true, "gen_ai.request.service_tier": true,
	"gen_ai.operation.name": true, "gen_ai.provider.name": true, "gen_ai.request.model": true,
	"gen_ai.usage.input_tokens": true, "gen_ai.usage.output_tokens": true,
	"gen_ai.usage.cache_read.input_tokens": true, "gen_ai.usage.reasoning.output_tokens": true,
	"llm.model_name": true, "llm.token_count.prompt": true, "llm.token_count.completion": true, "llm.token_count.total": true,
	"llm.cost.total": true,
	"tool.name":      true, "error.type": true,
	"nori.error.category": true, "nori.error.reason": true,
	"deployment.environment.name": true, "service.version": true, "service.name": true,
	"nori.input.kind": true, "nori.input.truncated": true, "nori.prompt.message_count": true, "nori.prompt.bytes": true, "nori.prompt.version": true,
	"http.request.method": true, "http.route": true, "http.response.status_code": true,
	"nori.input.id": true, "nori.turn.id": true, "nori.attempt.id": true, "nori.attempt.number": true,
	"nori.task.id": true, "nori.task.revision": true, "nori.task.status": true,
	"nori.operation.id": true, "nori.tool.outcome": true, "nori.tool.exit_code": true,
	"nori.tool.call_count": true, "nori.command.count": true, "nori.command.id": true,
	"nori.classification.intent": true, "nori.computer.operation": true, "nori.computer.allow_start": true, "nori.computer.reconnect_count": true,
	"nori.workflow.id": true, "nori.workflow.run_id": true, "nori.activity.id": true, "nori.activity.attempt": true,
	"nori.reply.status": true, "nori.output.present": true, "nori.conversation.input_count": true,
	"nori.reply.receipt_ack":          true,
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
	content         bool
	arizeProjectURL *url.URL
}

func (s filteredSpan) Name() string { return redactText(s.ReadOnlySpan.Name(), 120) }
func (s filteredSpan) Attributes() []attribute.KeyValue {
	attrs := s.ReadOnlySpan.Attributes()
	result := make([]attribute.KeyValue, 0, len(attrs)+3)
	for _, attr := range s.Resource().Attributes() {
		if attr.Key == "deployment.environment.name" || attr.Key == "service.version" || attr.Key == "service.name" {
			result = append(result, attr)
		}
	}
	for _, attr := range attrs {
		if attr.Key == "openinference.span.kind" && attr.Value.AsString() == "LLM" {
			result = append(result, attribute.String("nori.input.kind", "redacted_latest_user_excerpt"))
			break
		}
	}
	for _, attr := range attrs {
		if safe, ok := allowedAttribute(attr, s.content); ok {
			result = append(result, safe)
		}
	}
	// This URL is generated only from validated configuration and OTel IDs.
	// Never allow caller-provided URLs through the operational allowlist.
	if s.arizeProjectURL != nil && s.SpanContext().IsValid() {
		for _, attr := range attrs {
			if attr.Key == "openinference.span.kind" && attr.Value.AsString() == "LLM" {
				link := *s.arizeProjectURL
				query := url.Values{
					"selectedTraceId": {s.SpanContext().TraceID().String()},
					"selectedSpanId":  {s.SpanContext().SpanID().String()},
					"selectedTab":     {"spans"},
				}
				link.RawQuery = query.Encode()
				result = append(result, attribute.String("nori.arize.url", link.String()))
				break
			}
		}
	}
	return result
}

// arizeProjectURL accepts only an Arize project page with three opaque,
// base64-encoded IDs. Queries, fragments, credentials, and other hosts cannot
// become an export-side exception to URL redaction.
func arizeProjectURL(raw string) *url.URL {
	if raw == "" {
		return nil
	}
	project, err := url.Parse(raw)
	if err != nil || project.Scheme != "https" || project.Host != "app.arize.com" || project.User != nil || project.RawQuery != "" || project.ForceQuery || project.Fragment != "" {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(project.EscapedPath(), "/"), "/")
	if len(parts) != 7 || parts[0] != "" || parts[1] != "organizations" || parts[3] != "spaces" || parts[5] != "projects" {
		return nil
	}
	for _, index := range []int{2, 4, 6} {
		id, err := url.PathUnescape(parts[index])
		if err != nil {
			return nil
		}
		decoded, err := base64.StdEncoding.DecodeString(id)
		if err != nil || len(decoded) == 0 || base64.StdEncoding.EncodeToString(decoded) != id {
			return nil
		}
	}
	return project
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
	inner           sdktrace.SpanExporter
	arize           bool
	arizeProjectURL *url.URL
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
		filtered = append(filtered, filteredSpan{ReadOnlySpan: span, content: e.arize, arizeProjectURL: e.arizeProjectURL})
	}
	if len(filtered) == 0 {
		return nil
	}
	err := e.inner.ExportSpans(ctx, filtered)
	destination := "sentry.spans"
	if e.arize {
		destination = "arize.spans"
	}
	if err != nil {
		diagnostic(destination, "export_failed", len(filtered))
	}
	return err
}

func (e *filteredExporter) Shutdown(ctx context.Context) error { return e.inner.Shutdown(ctx) }
