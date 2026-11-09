package agent

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/extensions"
	"nori/internal/observability"
	"nori/internal/prompts"
	"nori/internal/transcript"
)

// The output cap includes reasoning, so it leaves room for both reasoning and a
// detailed answer without changing the model's default thinking settings.
const modelOutputTokenLimit = 8192

// Decide performs exactly one model request. Its caller owns retries and budgets.
func (s *Service) Decide(ctx context.Context, messages []transcript.Message, wait, work bool) (resp Response, err error) {
	started := time.Now()
	provider, model := s.Model.Provider(), s.Model.Name()
	ctx, span := otel.Tracer("nori/agent").Start(ctx, "generate_content "+model, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("openinference.span.kind", "LLM"), attribute.String("session.id", observability.SessionID()),
		attribute.String("gen_ai.operation.name", "generate_content"), attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model), attribute.String("llm.model_name", model),
	))
	span.SetAttributes(attribute.Int("nori.prompt.message_count", len(messages)))
	// Export only an unambiguous latest user text part. The historical context
	// block also has role=user; never scan past a tool response or export it.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != transcript.RoleUser {
			continue
		}
		if len(messages[i].Parts) == 1 {
			part := messages[i].Parts[0]
			background := strings.HasPrefix(part.Text, prompts.Runtime("task-history-marker", nil))
			if part.Text != "" && !background && part.ToolCall == nil && part.ToolResult == nil && part.Provider == nil {
				span.SetAttributes(attribute.Bool("nori.input.truncated", len(part.Text) > 2048))
				if a, ok := observability.ContentAttribute("input.value", part.Text); ok {
					span.SetAttributes(a)
				}
			}
		}
		break
	}
	defer func() {
		span.SetAttributes(attribute.Bool("nori.output.present", resp.Answer != ""), attribute.Int("nori.tool.call_count", len(resp.Calls)))
		if u := resp.Usage; u != nil {
			input := int64(u.InputTokens)
			output := int64(u.OutputTokens) + int64(u.ReasoningTokens)
			span.SetAttributes(
				attribute.Int64("gen_ai.usage.input_tokens", input), attribute.Int64("gen_ai.usage.output_tokens", output),
				attribute.Int64("gen_ai.usage.cache_read.input_tokens", int64(u.CachedTokens)),
				attribute.Int64("gen_ai.usage.reasoning.output_tokens", int64(u.ReasoningTokens)),
				attribute.Int64("llm.token_count.prompt", input), attribute.Int64("llm.token_count.completion", output),
				attribute.Int64("llm.token_count.total", int64(u.TotalTokens)),
			)
			observability.Metric(ctx, "nori.model.input_tokens", float64(input))
			observability.Metric(ctx, "nori.model.output_tokens", float64(output))
		}
		if resp.CostKnown {
			span.SetAttributes(
				attribute.Float64("nori.cost.usd", resp.Cost), attribute.Float64("llm.cost.total", resp.Cost),
				attribute.String("nori.cost.status", "reported"),
			)
		} else if resp.NotSent {
			span.SetAttributes(attribute.String("nori.cost.status", "not_sent"))
		} else {
			span.SetAttributes(attribute.String("nori.cost.status", "unknown"))
		}
		if a, ok := observability.ContentAttribute("output.value", resp.Answer); ok {
			span.SetAttributes(a)
		}
		identity := []attribute.KeyValue{attribute.String("gen_ai.provider.name", provider), attribute.String("gen_ai.request.model", model)}
		if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "model.task", err)
			observability.Event(ctx, "model.call.failed", identity...)
		} else if err == nil {
			observability.Event(ctx, "model.call.completed", identity...)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			observability.Metric(ctx, "nori.model.duration_ms", float64(time.Since(started).Milliseconds()))
		}
		span.End()
	}()
	snapshot, dynamic := s.Tools(ctx)
	catalog := s.catalog(dynamic, snapshot)
	resp, err = s.Model.Respond(ctx, Request{
		Instructions:    prompts.TaskWorker + "\nUser details (data): " + s.UserFacts + "\n" + s.catalogInstruction(catalog),
		Messages:        messages,
		Tools:           offeredTools(catalog, snapshot, messages, wait, work, s.Computer != nil),
		MaxOutputTokens: modelOutputTokenLimit,
	})
	resp.Tools = snapshot
	return resp, err
}

// Leaving a schema out shortens the request. Execution still checks every call.
// Without a computer, tools that run on it are left out: the catalog still shows the capability as unavailable.
func offeredTools(catalog []Capability, snapshot *extensions.Snapshot, messages []transcript.Message, wait, work, computer bool) []transcript.Tool {
	results := []map[string]any{}
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.ToolResult != nil && part.ToolResult.Name == "capability_load" {
				results = append(results, part.ToolResult.Output)
			}
		}
	}
	loaded := loadedCapabilities(results)
	for _, capability := range catalog {
		if capability.Default {
			loaded[capability.Name] = true
		}
	}
	access := availableTools(wait, work)
	selected := make([]transcript.Tool, 0, len(taskToolDefinitions))
	for _, definition := range taskToolDefinitions {
		if !computer && !definition.policy.Runtime && definition.capability != "" {
			continue
		}
		if definition.access <= access && (definition.capability == "" || loaded[definition.capability]) {
			selected = append(selected, definition.Tool)
		}
	}
	for _, d := range snapshot.Definitions() {
		if d.Builtin() || (d.Exposure != extensions.ExposureDirect && !(d.Exposure == extensions.ExposureDiscoverable && loaded[d.CapabilityTitle])) {
			continue
		}
		if tool, ok := modelTool(d); ok {
			selected = append(selected, tool)
		}
	}
	return selected
}
