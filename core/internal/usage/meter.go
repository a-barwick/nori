// Package usage defines the accounting boundary shared by model callers.
package usage

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

// Meter upserts one provider attempt by ID. A pending record precedes provider
// I/O; a terminal record replaces it even after cancellation. Nil cost means
// unknown. Implementations must tolerate duplicate records and concurrent calls.
// Task spending ceilings remain enforced by the task workflow, independently.
type Meter interface {
	RecordUsage(context.Context, state.Usage) error
}

type Func func(context.Context, state.Usage) error

func (f Func) RecordUsage(ctx context.Context, u state.Usage) error { return f(ctx, u) }

type scope struct{ input, provider string }
type scopeKey struct{}

// Session configures optional clock and ID hooks for usage attempts.
type Session struct {
	Now   func() time.Time
	NewID func() string
}

type sessionKey struct{}

func (s Session) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s Session) newID() string {
	if s.NewID == nil {
		return uuid.NewString()
	}
	return s.NewID()
}

// WithSession attaches optional clock and ID hooks to ctx for usage.Attempt.
func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// WithScope associates a model call with its task, input or compaction operation.
func WithScope(ctx context.Context, input, provider string) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope{input, provider})
}

// Attempt builds a pending usage record using s's hooks and any scope on ctx.
func (s Session) Attempt(ctx context.Context, provider, model string) state.Usage {
	sc, _ := ctx.Value(scopeKey{}).(scope)
	if sc.provider != "" {
		provider = sc.provider
	}
	return state.Usage{
		ID: s.newID(), InputID: sc.input, Provider: provider, Model: model,
		CostStatus: "unknown", RequestStatus: "pending", CreatedAt: s.now(),
	}
}

func Attempt(ctx context.Context, provider, model string) state.Usage {
	s, _ := ctx.Value(sessionKey{}).(Session)
	return s.Attempt(ctx, provider, model)
}

// RecordReportedCost retains a valid provider-reported USD charge, including zero.
func RecordReportedCost(record *state.Usage, cost *float64) bool {
	if cost == nil || *cost < 0 || math.IsNaN(*cost) || math.IsInf(*cost, 0) {
		return false
	}
	record.CostUSD, record.CostStatus = cost, "provider"
	return true
}

// Finish persists the final observation independently of caller cancellation.
func Finish(ctx context.Context, meter Meter, u state.Usage) error {
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return meter.RecordUsage(save, u)
}

// WithHost keeps Nori's durable record as the source of truth and adds a host
// accounting sink. A host failure before I/O refuses admission. After I/O, a
// delivery failure is logged without discarding the saved model result.
func WithHost(local, host Meter) Meter {
	return Func(func(ctx context.Context, u state.Usage) error {
		if err := local.RecordUsage(ctx, u); err != nil {
			return err
		}
		if err := host.RecordUsage(ctx, u); err != nil {
			if u.RequestStatus == "pending" {
				u.RequestStatus = "not_sent"
				return errors.Join(errors.New("host metering unavailable; provider request was not sent"), Finish(ctx, local, u))
			}
			slog.WarnContext(ctx, "host usage delivery failed; durable usage record retained", "usage_id", u.ID)
		}
		return nil
	})
}

// Wrap routes usage through meter while preserving the store's other methods.
func Wrap(store state.ConversationStore, meter Meter) state.ConversationStore {
	return &meteredStore{ConversationStore: store, meter: meter}
}

type meteredStore struct {
	state.ConversationStore
	meter Meter
}

func (s *meteredStore) RecordUsage(ctx context.Context, u state.Usage) error {
	return s.meter.RecordUsage(ctx, u)
}
