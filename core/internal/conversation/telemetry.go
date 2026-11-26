package conversation

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/observability"
	"nori/internal/state"
)

var conversationTracer = otel.Tracer("nori/conversation")

func startTurn(ctx context.Context, t state.ConversationTurn) (context.Context, trace.Span) {
	parent := ctx
	var primary trace.SpanContext
	var links []trace.Link
	if wake := trace.SpanContextFromContext(ctx); wake.IsValid() {
		links = append(links, trace.Link{SpanContext: wake})
	}
	for _, in := range t.Inputs {
		inputCtx := observability.ExtractTrace(context.Background(), in.TraceContext)
		sc := trace.SpanContextFromContext(inputCtx)
		if !sc.IsValid() {
			continue
		}
		if !primary.IsValid() {
			parent = observability.ExtractTrace(ctx, in.TraceContext)
			primary = sc
		} else if !primary.IsSampled() && sc.IsSampled() {
			if sc.TraceID() != primary.TraceID() || sc.SpanID() != primary.SpanID() {
				links = append(links, trace.Link{SpanContext: primary})
			}
			parent = observability.ExtractTrace(ctx, in.TraceContext)
			primary = sc
		} else if sc.TraceID() != primary.TraceID() || sc.SpanID() != primary.SpanID() {
			links = append(links, trace.Link{SpanContext: sc})
		}
	}
	return conversationTracer.Start(parent, "conversation.turn", trace.WithLinks(links...), trace.WithAttributes(
		attribute.String("openinference.span.kind", "CHAIN"),
		attribute.String("session.id", observability.SessionID()),
		attribute.String("nori.turn.id", t.ID),
		attribute.Int("nori.conversation.input_count", len(t.Inputs)),
		attribute.Int("nori.conversation.restart_count", t.Restarts),
	))
}

func recordReplies(ctx context.Context, t state.ConversationTurn, text string, now time.Time) {
	turnSpan := trace.SpanContextFromContext(ctx)
	for _, in := range t.Inputs {
		if in.Event != nil {
			for _, hold := range in.Event.Holds {
				status := "answered"
				if t.Attempt.Status == "error" || text != t.Attempt.Text {
					status = "failed"
				}
				_, span := conversationTracer.Start(ctx, "conversation.hold.reply", trace.WithAttributes(
					attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID()),
					attribute.String("nori.hold.id", hold.ID), attribute.String("nori.task.id", hold.TaskID),
					attribute.String("nori.hold.status", status),
				))
				if status == "failed" {
					span.SetStatus(codes.Error, "held reply failed")
				}
				span.End()
				if status == "answered" {
					observability.Event(ctx, "conversation.hold.answered", attribute.String("nori.hold.id", hold.ID), attribute.String("nori.task.id", hold.TaskID))
				}
			}
		}
		if !in.IsUser() {
			continue
		}
		parent := observability.ExtractTrace(ctx, in.TraceContext)
		opts := []trace.SpanStartOption{trace.WithAttributes(
			attribute.String("openinference.span.kind", "CHAIN"),
			attribute.String("session.id", observability.SessionID()),
			attribute.String("nori.input.id", in.ID),
			attribute.String("nori.turn.id", t.ID),
			attribute.String("nori.attempt.id", t.Attempt.ID),
			attribute.Int("nori.attempt.number", t.Attempt.Number),
			attribute.String("nori.reply.status", t.Attempt.Status),
			attribute.Bool("nori.output.present", text != ""),
		)}
		if !in.CreatedAt.IsZero() && !in.CreatedAt.After(now) {
			opts = append(opts, trace.WithTimestamp(in.CreatedAt))
			observability.Metric(ctx, "nori.conversation.reply_latency_ms", float64(now.Sub(in.CreatedAt).Milliseconds()))
		}
		parentSpan := trace.SpanContextFromContext(parent)
		if turnSpan.IsValid() && (parentSpan.TraceID() != turnSpan.TraceID() || parentSpan.SpanID() != turnSpan.SpanID()) {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: turnSpan}))
		}
		_, span := conversationTracer.Start(parent, "conversation.acceptance_to_reply", opts...)
		if a, ok := observability.ContentAttribute("input.value", in.Text); ok {
			span.SetAttributes(a)
		}
		if t.Attempt.Status == "completed" && t.Attempt.Reply != "" && text == t.Attempt.Text {
			if a, ok := observability.ContentAttribute("output.value", text); ok {
				span.SetAttributes(a)
			}
		}
		if t.Attempt.Status == "error" {
			span.SetStatus(codes.Error, "conversation reply failed")
		}
		span.End(trace.WithTimestamp(now))
	}
}
