package task

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
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
	if activity.IsActivity(ctx) {
		info := activity.GetInfo(ctx)
		attrs = append(attrs,
			attribute.String("nori.workflow.id", info.WorkflowExecution.ID),
			attribute.String("nori.workflow.run_id", info.WorkflowExecution.RunID),
			attribute.String("nori.activity.id", info.ActivityID),
			attribute.Int64("nori.activity.attempt", int64(info.Attempt)),
		)
	}
	opts := []trace.SpanStartOption{trace.WithAttributes(attrs...)}
	origin := trace.SpanContextFromContext(observability.ExtractTrace(context.Background(), run.OriginTraceContext))
	if origin.IsValid() && origin.TraceID() != trace.SpanContextFromContext(ctx).TraceID() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: origin}))
	}
	return taskTracer.Start(ctx, name, opts...)
}

func expectedBoundary(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, state.ErrSuperseded) ||
		errors.Is(err, state.ErrCancelled) || errors.Is(err, state.ErrPaused) ||
		errors.Is(err, state.ErrRoutingPending) || errors.Is(err, state.ErrBusy) {
		return true
	}
	var app *temporal.ApplicationError
	if errors.As(err, &app) {
		switch app.Type() {
		case "superseded", "cancelled", "paused", "routing_pending":
			return true
		}
	}
	return false
}

// Optional activities correlate by task/workflow without a telemetry-only DB read.
func (a *Activities) activityRun(_ context.Context, id string) state.Run { return state.Run{ID: id} }

func recordActivityFailure(ctx context.Context, err error, operation string) {
	if !expectedBoundary(err) && !errors.Is(ctx.Err(), context.Canceled) {
		observability.RecordErrorCategory(ctx, operation, err)
	}
}

func safeToolName(name string) string {
	switch name {
	case "shell", "browser_navigate", "browser_screenshot", "request_input", "request_work", "complete", "job_start", "job_status", "job_wait", "job_stop", "web_search", "screenshot_inspect", "calendar_read":
		return name
	default:
		return "unknown"
	}
}
