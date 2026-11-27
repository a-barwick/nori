package agent

import (
	"context"
	"errors"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/observability"
	"nori/internal/prompts"
)

const inspectOutputTokens = 2048

// InspectImage runs Nori's isolated no-tools vision inspector on image bytes
// (used by upload_inspect and computer_browser screenshot inspection so image
// text can never invoke tools or accumulate raw screenshots in task history).
func (s *Service) InspectImage(ctx context.Context, idKey, id, instruction, question string, data []byte, mediaType string) (map[string]any, error) {
	return s.inspect(ctx, idKey, id, instruction, question, data, mediaType)
}

func (s *Service) inspect(ctx context.Context, idKey, id, instruction, question string, data []byte, mediaType string) (result map[string]any, err error) {
	provider, model := s.Model.Provider(), s.Model.Name()
	ctx, span := otel.Tracer("nori/agent").Start(ctx, "inspect_image "+model, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("openinference.span.kind", "LLM"), attribute.String("session.id", observability.SessionID()),
		attribute.String("gen_ai.operation.name", "generate_content"), attribute.String("gen_ai.provider.name", provider),
		attribute.String("gen_ai.request.model", model), attribute.String("llm.model_name", model),
	))
	defer func() {
		if cost, ok := result["cost_usd"].(float64); ok {
			span.SetAttributes(attribute.Float64("nori.cost.usd", cost), attribute.Float64("llm.cost.total", cost), attribute.String("nori.cost.status", "reported"))
		} else {
			span.SetAttributes(attribute.String("nori.cost.status", "unknown"))
		}
		if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "model.inspect", err)
		}
		span.End()
	}()
	inspection, err := s.Model.Inspect(ctx, InspectRequest{
		Instructions: instruction, Question: question, Image: data, MediaType: mediaType, MaxOutputTokens: inspectOutputTokens,
	})
	switch {
	case inspection.CostKnown:
		result = map[string]any{idKey: id, "cost_usd": inspection.Cost}
	case inspection.Sent:
		result = map[string]any{idKey: id, "cost_unknown": true}
	}
	if err != nil {
		return result, err
	}
	if result == nil {
		result = map[string]any{idKey: id}
	}
	observation := strings.TrimSpace(inspection.Observation)
	if observation == "" {
		return result, errors.New(prompts.Runtime("tool-inspection-model-returned-no-answer", nil))
	}
	result["question"], result["observation"] = question, observation
	return result, nil
}
