// Package usage defines the accounting boundary shared by model callers.
package usage

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"
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
	if u.CostUSD == nil && u.EstimatedExposureUSD <= 0 && u.RequestStatus != "not_sent" {
		u.EstimatedExposureUSD = EstimateExposureUSD(u.Model)
	}
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return meter.RecordUsage(save, u)
}

// ErrUserQuotaExceeded is returned before provider I/O when a user's durable
// Postgres usage history exceeds the configured per-hour call or per-day spend cap.
var ErrUserQuotaExceeded = errors.New("usage: per-user rate or spend limit exceeded")

const defaultUnknownCallEstimateUSD = 0.02

// EstimateExposureUSD returns a conservative pre-I/O USD reservation for a model call,
// kept separate from recorded actual spend (CostUSD).
func EstimateExposureUSD(model string) float64 {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "opus") || strings.Contains(m, "gpt-6.2-sol") || strings.Contains(m, "gpt-6.1-sol"):
		return 0.05
	case strings.Contains(m, "haiku") || strings.Contains(m, "mini") || strings.Contains(m, "nano") || strings.Contains(m, "terra"):
		return 0.01
	default:
		return defaultUnknownCallEstimateUSD
	}
}

// WindowReader summarizes a user's durable usage records (backed by Postgres)
// across hourly and daily cutoffs in a single scan so multi-replica Cloud Run
// instances enforce shared per-user rate and spend admission limits.
type WindowReader interface {
	UsageWindowSummary(ctx context.Context, hourCutoff, dayCutoff time.Time) (hourCalls int, daySpendUSD float64, dayUnknownCalls int, err error)
}

// AtomicReserver checks per-user quotas and writes the pending usage reservation
// inside a single storage transaction so concurrent requests cannot race past a cap.
type AtomicReserver interface {
	ReserveUsage(ctx context.Context, u state.Usage, hourCutoff, dayCutoff time.Time, maxCallsPerHour int, maxUSDPerDay float64, unknownEstimateUSD float64) (bool, error)
}

// WithAdmission enforces Postgres-backed pre-I/O per-user rate and spend caps
// before persisting a "pending" usage attempt. Non-pending terminal updates
// always pass through so completed calls are never dropped.
func WithAdmission(local Meter, reader WindowReader, maxCallsPerHour int, maxUSDPerDay float64, now func() time.Time) Meter {
	reserver, hasReserver := local.(AtomicReserver)
	if !hasReserver {
		reserver, hasReserver = reader.(AtomicReserver)
	}
	if (!hasReserver && reader == nil) || (maxCallsPerHour <= 0 && maxUSDPerDay <= 0) {
		return local
	}
	return Func(func(ctx context.Context, u state.Usage) error {
		if u.RequestStatus == "pending" {
			var t time.Time
			switch {
			case now != nil:
				t = now().UTC()
			case !u.CreatedAt.IsZero():
				t = u.CreatedAt.UTC()
			default:
				t = time.Now().UTC()
			}
			hourCutoff := t.Add(-time.Hour)
			dayCutoff := hourCutoff
			if maxUSDPerDay > 0 {
				dayCutoff = t.Add(-24 * time.Hour)
			}
			if u.EstimatedExposureUSD <= 0 {
				u.EstimatedExposureUSD = EstimateExposureUSD(u.Model)
			}
			if hasReserver {
				allowed, err := reserver.ReserveUsage(ctx, u, hourCutoff, dayCutoff, maxCallsPerHour, maxUSDPerDay, defaultUnknownCallEstimateUSD)
				if err == nil {
					if !allowed {
						slog.WarnContext(ctx, "per-user atomic usage reservation exceeded limit before provider I/O",
							"model", u.Model, "incoming_exposure_usd", u.EstimatedExposureUSD, "max_calls_per_hour", maxCallsPerHour, "max_usd_per_day", maxUSDPerDay)
						return ErrUserQuotaExceeded
					}
					return nil
				}
				if !errors.Is(err, state.ErrReserveUnsupported) {
					return err
				}
				if reader == nil {
					return local.RecordUsage(ctx, u)
				}
			}
			hourCalls, daySpendUSD, dayUnknownCalls, err := reader.UsageWindowSummary(ctx, hourCutoff, dayCutoff)
			if err != nil {
				return err
			}
			if maxCallsPerHour > 0 && hourCalls >= maxCallsPerHour {
				slog.WarnContext(ctx, "per-user hourly call limit exceeded before provider I/O",
					"calls_in_hour", hourCalls, "limit", maxCallsPerHour)
				return ErrUserQuotaExceeded
			}
			if maxUSDPerDay > 0 {
				exposure := daySpendUSD + float64(dayUnknownCalls)*defaultUnknownCallEstimateUSD + u.EstimatedExposureUSD
				if exposure > maxUSDPerDay {
					slog.WarnContext(ctx, "per-user daily spend limit exceeded before provider I/O",
						"spend_usd", daySpendUSD, "unknown_calls", dayUnknownCalls, "exposure_usd", exposure, "limit_usd", maxUSDPerDay)
					return ErrUserQuotaExceeded
				}
			}
		} else if u.CostUSD != nil || u.RequestStatus == "not_sent" {
			// Terminal record with known actual spend or not_sent clears estimated exposure.
			u.EstimatedExposureUSD = 0
		}
		return local.RecordUsage(ctx, u)
	})
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
