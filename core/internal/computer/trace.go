package computer

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/observability"
)

func Traced(p Provider) Provider { return traced{p} }

type traced struct{ p Provider }

func (t traced) Open(ctx context.Context, run string) (*Client, error) {
	return connectSpan(ctx, true, func(ctx context.Context) (*Client, error) { return t.p.Open(ctx, run) })
}

func (t traced) Attach(ctx context.Context, run string) (*Client, error) {
	return connectSpan(ctx, false, func(ctx context.Context) (*Client, error) { return t.p.Attach(ctx, run) })
}

func connectSpan(ctx context.Context, allowStart bool, connect func(context.Context) (*Client, error)) (*Client, error) {
	ctx, span := otel.Tracer("nori/computer").Start(ctx, "computer.connect", trace.WithAttributes(
		attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID()),
		attribute.Bool("nori.computer.allow_start", allowStart)))
	defer span.End()
	client, err := connect(ctx)
	if err != nil && !errors.Is(err, ErrStopped) && !errors.Is(ctx.Err(), context.Canceled) {
		observability.RecordErrorCategory(ctx, "computer.connect", err)
	}
	return client, err
}
