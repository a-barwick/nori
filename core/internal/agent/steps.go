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

const modelOutputTokenLimit = 8192

func (s *Service) Decide(ctx context.Context, messages []transcript.Message, wait, work bool) (resp Response, err error) {
	started := s.currentTime()
	provider, model := s.Model.Provider(), s.Model.Name()
	ctx, span := otel.Tracer("nori/agent").Start(ctx, "generate_content "+model, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("openinference.span.kind", "LLM"), attribute.String("session.id", observability.SessionID()),
		attribute.String("gen_ai.operation.name", "generate_content"), attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model), attribute.String("llm.model_name", model),
	))
	span.SetAttributes(attribute.Int("nori.prompt.message_count", len(messages)))
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
			observability.Metric(ctx, "nori.model.duration_ms", float64(s.currentTime().Sub(started).Milliseconds()))
		}
		span.End()
	}()
	snapshot, dynamic := s.Tools(ctx)
	catalog := s.catalog(dynamic, snapshot)
	facts, location := s.UserFacts, (*time.Location)(nil)
	if s.Profile != nil {
		facts, location = s.Profile(ctx)
	}
	if location == nil {
		location = s.location()
	}
	now := s.currentTime().In(location)
	clock := transcript.Message{Role: transcript.RoleUser, Parts: []transcript.Part{{Text: prompts.Runtime("task-clock", map[string]any{
		"Now":   now.Format("Monday, 2 January 2006 15:04 MST") + " (" + location.String() + ")",
		"Today": now.Format("Monday, 2 January 2006"), "Tomorrow": now.AddDate(0, 0, 1).Format("Monday, 2 January 2006")})}}}
	nativeSearch := s.usesNativeWebSearch()
	resp, err = s.Model.Respond(ctx, Request{
		Instructions:    prompts.TaskWorker + "\nUser details (data): " + facts + "\n" + s.catalogInstruction(catalog, location),
		Messages:        append(messages[:len(messages):len(messages)], clock),
		Tools:           offeredTools(catalog, snapshot, messages, wait, work, nativeSearch),
		MaxOutputTokens: modelOutputTokenLimit,
		WebSearch:       nativeSearch,
	})
	resp.Tools = snapshot
	return resp, err
}

func offeredTools(catalog []Capability, snapshot *extensions.Snapshot, messages []transcript.Message, wait, work, nativeSearch bool) []transcript.Tool {
	results := []map[string]any{}
	hasAttachments := false
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.ToolResult != nil && part.ToolResult.Name == "capability_load" {
				results = append(results, part.ToolResult.Output)
			}
			if part.Text != "" && strings.Contains(part.Text, `"upload_id"`) {
				hasAttachments = true
			}
		}
	}
	loaded := loadedCapabilities(results)
	for _, capability := range catalog {
		if capability.Default {
			loaded[capability.Name] = true
		}
	}
	if hasAttachments {
		loaded[attachmentCapability] = true
		loaded["Attached text files"] = true
	}
	const (
		preloadMaxTools = 12
		preloadMaxBytes = 6 << 10
	)
	type capSize struct {
		count int
		bytes int
	}
	discoverableSizes := map[string]capSize{}
	if snapshot != nil {
		for _, d := range snapshot.Definitions() {
			if d.Builtin() || d.Exposure != extensions.ExposureDiscoverable || d.CapabilityTitle == "Attached text files" {
				continue
			}
			if d.Binding.Adapter != extensions.AdapterNative {
				continue
			}
			sz := discoverableSizes[d.CapabilityTitle]
			sz.count++
			sz.bytes += len(d.InputSchema) + len(d.Description) + len(d.ModelName)
			discoverableSizes[d.CapabilityTitle] = sz
		}
	}
	for title, sz := range discoverableSizes {
		if sz.count > 0 && sz.count <= preloadMaxTools && sz.bytes <= preloadMaxBytes {
			loaded[title] = true
		}
	}
	access := availableTools(wait, work)
	selected := make([]transcript.Tool, 0, len(taskToolDefinitions))
	for _, definition := range taskToolDefinitions {
		if nativeSearch && definition.Name == "web_search" {
			continue
		}
		if definition.access <= access && (definition.capability == "" || loaded[definition.capability]) {
			selected = append(selected, definition.Tool)
		}
	}
	if snapshot != nil {
		for _, d := range snapshot.Definitions() {
			if d.Builtin() || (d.Exposure != extensions.ExposureDirect && !(d.Exposure == extensions.ExposureDiscoverable && loaded[d.CapabilityTitle])) {
				continue
			}
			if tool, ok := modelTool(d); ok {
				selected = append(selected, tool)
			}
		}
	}
	return selected
}
