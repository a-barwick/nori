package task

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/observability"
	"nori/internal/state"
)

var taskTracer = otel.Tracer("nori/task")

func startActivity(ctx context.Context, name, kind string, run state.Run, revision int64) (context.Context, trace.Span) {
	ctx = observability.ExtractTrace(ctx, run.TraceContext)
	attrs := []attribute.KeyValue{
		attribute.String("openinference.span.kind", kind), attribute.String("session.id", observability.SessionID()),
		attribute.String("nori.task.id", run.ID), attribute.Int64("nori.task.revision", revision),
	}
	if o := run.Origin; o != nil {
		for key, value := range map[string]string{"nori.automation.id": o.AutomationID, "nori.occurrence.id": o.OccurrenceID,
			"nori.goal.id": o.GoalID, "nori.task.continues": o.ContinuesTaskID} {
			if value != "" {
				attrs = append(attrs, attribute.String(key, value))
			}
		}
	}
	opts := []trace.SpanStartOption{trace.WithAttributes(attrs...)}
	origin := trace.SpanContextFromContext(observability.ExtractTrace(context.Background(), run.OriginTraceContext))
	if origin.IsValid() && origin.TraceID() != trace.SpanContextFromContext(ctx).TraceID() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: origin}))
	}
	return taskTracer.Start(ctx, name, opts...)
}

func expectedBoundary(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, state.ErrSuperseded) ||
		errors.Is(err, state.ErrCancelled) || errors.Is(err, state.ErrPaused) ||
		errors.Is(err, state.ErrRoutingPending) || errors.Is(err, state.ErrBusy) ||
		errors.Is(err, state.ErrTaskBusy)
}

func (a *Activities) activityRun(_ context.Context, id string) state.Run { return state.Run{ID: id} }

func recordActivityFailure(ctx context.Context, err error, operation string) {
	if !expectedBoundary(err) && !errors.Is(ctx.Err(), context.Canceled) {
		observability.RecordErrorCategory(ctx, operation, err)
	}
}
