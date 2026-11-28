package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var (
	ErrCreditExhausted      = errors.New("billing: hosted credit balance exhausted")
	ErrAccountSuspended     = errors.New("billing: account is suspended")
	ErrSubscriptionInactive = errors.New("billing: subscription is inactive")
	ErrEntitlementMissing   = errors.New("billing: user entitlement missing")
	ErrBYOKNotAllowed       = errors.New("billing: BYOK is not allowed on this tier")
	ErrGlobalBudgetExceeded = errors.New("billing: platform daily hosted budget exceeded")
	ErrTrialLimitExceeded   = errors.New("billing: daily trial signup limit exceeded")
	ErrStaleEvent           = errors.New("billing: stale entitlement event")
	ErrInvalidEnvironment   = errors.New("billing: invalid billing environment")
	ErrInvalidTier          = errors.New("billing: invalid tier")
	ErrInvalidChange        = errors.New("billing: invalid entitlement change")
	ErrSubscriptionConflict = errors.New("billing: external subscription belongs to another user")
)

const (
	CodeCreditExhausted      = "credit_exhausted"
	CodeAccountSuspended     = "account_suspended"
	CodeSubscriptionInactive = "subscription_inactive"
	CodeEntitlementMissing   = "entitlement_missing"
	CodeBYOKNotAllowed       = "byok_not_allowed"
	CodeGlobalBudgetExceeded = "global_budget_exceeded"
	CodeTrialLimitExceeded   = "trial_limit_exceeded"
)

const EntitlementNotifyChannel = "nori_wake"

// EntitlementNotifyPayload returns the Postgres LISTEN/NOTIFY payload on nori_wake
// used to evict cached entitlements, DEKs, and active sessions across all Core instances.
func EntitlementNotifyPayload(userID string) string {
	return "entitlement:" + userID
}

// ErrorCode maps billing sentinel errors to stable API error codes.
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrAccountSuspended):
		return CodeAccountSuspended
	case errors.Is(err, ErrCreditExhausted):
		return CodeCreditExhausted
	case errors.Is(err, ErrSubscriptionInactive):
		return CodeSubscriptionInactive
	case errors.Is(err, ErrEntitlementMissing):
		return CodeEntitlementMissing
	case errors.Is(err, ErrBYOKNotAllowed):
		return CodeBYOKNotAllowed
	case errors.Is(err, ErrGlobalBudgetExceeded):
		return CodeGlobalBudgetExceeded
	case errors.Is(err, ErrTrialLimitExceeded):
		return CodeTrialLimitExceeded
	default:
		return ""
	}
}

// HTTPStatus maps billing sentinel errors to the HTTP status codes specified by the contract.
func HTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrAccountSuspended), errors.Is(err, ErrEntitlementMissing):
		return http.StatusForbidden
	case errors.Is(err, ErrCreditExhausted), errors.Is(err, ErrSubscriptionInactive), errors.Is(err, ErrBYOKNotAllowed):
		return http.StatusPaymentRequired
	case errors.Is(err, ErrGlobalBudgetExceeded), errors.Is(err, ErrTrialLimitExceeded):
		return http.StatusTooManyRequests
	default:
		return 0
	}
}

// Entitlement is the authoritative billing and access state for one user.
// Suspension (SuspendedAt / SuspendedReason) is strictly orthogonal to billing Status.
type Entitlement struct {
	UserID               string     `json:"user_id"`
	Tier                 Tier       `json:"tier"`
	PendingTier          *Tier      `json:"pending_tier,omitempty"`
	Status               Status     `json:"status"`
	BillingProvider      Provider   `json:"billing_provider"`
	PeriodStart          time.Time  `json:"period_start"`
	PeriodEnd            *time.Time `json:"period_end,omitempty"`
	GraceUntil           *time.Time `json:"grace_until,omitempty"`
	CreditOverrideMicros *int64     `json:"credit_override_micros,omitempty"`
	AppAccountToken      string     `json:"app_account_token"`
	SuspendedAt          *time.Time `json:"suspended_at,omitempty"`
	SuspendedReason      string     `json:"suspended_reason,omitempty"`
	ProviderUpdatedAt    time.Time  `json:"provider_updated_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

func (e Entitlement) IsSuspended() bool {
	return e.SuspendedAt != nil
}

// IsActiveAt reports whether the billing status permits active access at now
// (independent of suspension).
func (e Entitlement) IsActiveAt(now time.Time) bool {
	now = now.UTC()
	switch e.Status {
	case StatusActive:
		return true
	case StatusBillingRetry:
		if e.GraceUntil != nil {
			return now.Before(e.GraceUntil.UTC())
		}
		if e.PeriodEnd != nil {
			return now.Before(e.PeriodEnd.UTC())
		}
		return false
	case StatusCanceled:
		return e.PeriodEnd != nil && now.Before(e.PeriodEnd.UTC())
	case StatusPastDue, StatusExpired:
		return false
	default:
		return false
	}
}

// CheckAccess verifies suspension first (403) and billing status second (402).
func (e Entitlement) CheckAccess(now time.Time) error {
	if e.IsSuspended() {
		return ErrAccountSuspended
	}
	if !e.IsActiveAt(now) {
		return ErrSubscriptionInactive
	}
	return nil
}

// EffectivePolicy returns the Policy for the entitlement's current tier.
func (e Entitlement) EffectivePolicy() Policy {
	p, ok := PolicyFor(e.Tier)
	if !ok {
		return MustPolicy(TierBYOK)
	}
	return p
}

// EffectiveHostedCreditGrantMicros returns the per-period hosted credit grant for this user,
// honoring explicit CreditOverrideMicros (including an explicit 0).
func (e Entitlement) EffectiveHostedCreditGrantMicros() int64 {
	if e.CreditOverrideMicros != nil {
		return *e.CreditOverrideMicros
	}
	return e.EffectivePolicy().HostedCreditMicros
}

// Clone returns a deep copy of Entitlement with copied pointer fields.
func (e Entitlement) Clone() Entitlement {
	cp := e
	if e.PendingTier != nil {
		pt := *e.PendingTier
		cp.PendingTier = &pt
	}
	if e.PeriodEnd != nil {
		t := e.PeriodEnd.UTC()
		cp.PeriodEnd = &t
	}
	if e.GraceUntil != nil {
		t := e.GraceUntil.UTC()
		cp.GraceUntil = &t
	}
	if e.CreditOverrideMicros != nil {
		v := *e.CreditOverrideMicros
		cp.CreditOverrideMicros = &v
	}
	if e.SuspendedAt != nil {
		t := e.SuspendedAt.UTC()
		cp.SuspendedAt = &t
	}
	return cp
}

// Subscription represents one external/manual subscription row in nori_subscriptions.
type Subscription struct {
	ID                     string      `json:"id"`
	UserID                 string      `json:"user_id"`
	Provider               Provider    `json:"provider"`
	Environment            Environment `json:"environment"`
	ExternalCustomerID     string      `json:"external_customer_id"`
	ExternalSubscriptionID string      `json:"external_subscription_id"`
	ExternalProductID      string      `json:"external_product_id"`
	Tier                   Tier        `json:"tier"`
	Status                 Status      `json:"status"`
	PeriodStart            time.Time   `json:"period_start"`
	PeriodEnd              *time.Time  `json:"period_end,omitempty"`
	GraceUntil             *time.Time  `json:"grace_until,omitempty"`
	ProviderUpdatedAt      time.Time   `json:"provider_updated_at"`
	UpdatedAt              time.Time   `json:"updated_at"`
}

func (s Subscription) Clone() Subscription {
	cp := s
	if s.PeriodEnd != nil {
		t := s.PeriodEnd.UTC()
		cp.PeriodEnd = &t
	}
	if s.GraceUntil != nil {
		t := s.GraceUntil.UTC()
		cp.GraceUntil = &t
	}
	return cp
}

// CreditGrant represents a row in nori_credit_grants.
type CreditGrant struct {
	ID             string     `json:"id"`
	UserID         string     `json:"user_id"`
	Kind           GrantKind  `json:"kind"`
	AmountMicros   int64      `json:"amount_micros"`
	ValidFrom      time.Time  `json:"valid_from"`
	ValidUntil     *time.Time `json:"valid_until,omitempty"`
	SourceProvider Provider   `json:"source_provider"`
	SourceEventID  string     `json:"source_event_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

func (g CreditGrant) Clone() CreditGrant {
	cp := g
	if g.ValidUntil != nil {
		t := g.ValidUntil.UTC()
		cp.ValidUntil = &t
	}
	return cp
}

func (g CreditGrant) IsActiveAt(now time.Time) bool {
	now = now.UTC()
	if now.Before(g.ValidFrom.UTC()) {
		return false
	}
	return g.ValidUntil == nil || now.Before(g.ValidUntil.UTC())
}

// CreditBalance summarizes a user's active hosted credit grants and consumption.
type CreditBalance struct {
	GrantedMicros   int64      `json:"granted_micros"`
	SpentMicros     int64      `json:"spent_micros"`
	PendingMicros   int64      `json:"pending_micros"`
	AvailableMicros int64      `json:"available_micros"`
	PeriodStart     time.Time  `json:"period_start"`
	PeriodEnd       *time.Time `json:"period_end,omitempty"`
}

// Summary combines entitlement, tier policy, and hosted credit balance for API & CLI responses.
type Summary struct {
	Entitlement Entitlement   `json:"entitlement"`
	Policy      Policy        `json:"policy"`
	Balance     CreditBalance `json:"balance"`
}

// GlobalBudgetConfig holds fleet-wide daily hosted caps.
type GlobalBudgetConfig struct {
	HostedAllMaxMicrosPerDay int64
	HostedExaMaxMicrosPerDay int64
	TrialMaxSignupsPerDay    int
}

// GlobalBudgetUsage is one daily row from nori_global_budget_daily.
type GlobalBudgetUsage struct {
	Day         string      `json:"day"`
	Scope       BudgetScope `json:"scope"`
	SpentMicros int64       `json:"spent_micros"`
	Count       int         `json:"count"`
}

// SetTierOptions controls operator-driven manual tier changes.
type SetTierOptions struct {
	Pending              bool       // If true, sets pending_tier for next period_end rather than switching immediately.
	PeriodStart          *time.Time // Optional override; defaults to now.
	PeriodEnd            *time.Time // Optional override; defaults to +1 month for paid tiers, nil for trial.
	CreditOverrideMicros *int64     // Optional explicit credit override (including explicit 0).
	ClearCreditOverride  bool       // If true, resets credit_override_micros to NULL.
	Status               Status     // Defaults to StatusActive.
	SkipGrant            bool       // If true, does not insert a period/trial credit grant.
}

// SubscriptionSnapshot is the full-snapshot provider state carried in Change for Stripe/Apple IAP.
type SubscriptionSnapshot struct {
	ExternalCustomerID     string
	ExternalSubscriptionID string
	ExternalProductID      string
	Tier                   Tier
	Status                 Status
	PendingTier            *Tier
	PeriodStart            time.Time
	PeriodEnd              *time.Time
	GraceUntil             *time.Time
}

// Change is the idempotent full-snapshot input to ApplyEvent (Stripe / Apple IAP port).
type Change struct {
	Provider          Provider
	Environment       Environment
	EventID           string
	EventType         string
	ProviderUpdatedAt time.Time
	UserID            string
	AppAccountToken   string
	Subscription      *SubscriptionSnapshot
	CreditDeltaMicros int64
	CreditGrantKind   GrantKind
	CreditValidUntil  *time.Time
	Payload           json.RawMessage
}

// TrialGrantID returns the deterministic one-time trial credit grant ID for userID.
func TrialGrantID(userID string) string {
	return "trial:" + userID
}

// ManualPeriodGrantID returns the deterministic monthly grant ID for manual standard/power rollovers.
func ManualPeriodGrantID(userID string, periodStart time.Time) string {
	return fmt.Sprintf("manual:%s:%s", userID, periodStart.UTC().Format("2006-01"))
}

// ManualSetTierGrantID returns a deterministic grant ID for an operator SetTier grant when
// the base monthly ID was already used/closed in the same calendar month.
func ManualSetTierGrantID(userID string, tier Tier, periodStart time.Time) string {
	return fmt.Sprintf("manual:%s:%s:%s:%d", userID, periodStart.UTC().Format("2006-01"), tier, periodStart.UTC().Unix())
}

// ValidateCreditGrant checks grant kind/sign consistency and prevents external providers from creating trial grants.
func ValidateCreditGrant(grant CreditGrant, fromExternalProvider bool) error {
	if grant.ID == "" || grant.UserID == "" || !ValidGrantKind(grant.Kind) {
		return ErrInvalidChange
	}
	if fromExternalProvider && grant.Kind == GrantTrial {
		return ErrInvalidChange
	}
	switch grant.Kind {
	case GrantClawback:
		if grant.AmountMicros >= 0 {
			return ErrInvalidChange
		}
	default:
		if grant.AmountMicros <= 0 {
			return ErrInvalidChange
		}
	}
	return nil
}

// EventGrantID returns the deterministic grant ID for a provider event credit delta or clawback.
func EventGrantID(provider Provider, eventID string, deltaMicros int64) string {
	if deltaMicros < 0 {
		return fmt.Sprintf("clawback:%s:%s", provider, eventID)
	}
	return fmt.Sprintf("%s:%s", provider, eventID)
}

// RolloverManualEntitlement advances a manual entitlement when now >= period_end,
// applying pending_tier or transitioning canceled -> expired, and returning any
// deterministic period grant that should be inserted idempotently.
func RolloverManualEntitlement(ent Entitlement, now time.Time) (updated Entitlement, grant *CreditGrant, changed bool) {
	now = now.UTC()
	updated = ent.Clone()
	if updated.BillingProvider != ProviderManual || updated.PeriodEnd == nil {
		return updated, nil, false
	}
	if now.Before(updated.PeriodEnd.UTC()) {
		return updated, nil, false
	}
	if updated.Status == StatusCanceled {
		updated.Status = StatusExpired
		updated.PendingTier = nil
		updated.UpdatedAt = now
		return updated, nil, true
	}
	if updated.Status != StatusActive {
		return updated, nil, false
	}
	if updated.PendingTier != nil && ValidPaidTier(*updated.PendingTier) {
		updated.Tier = *updated.PendingTier
		updated.PendingTier = nil
	}
	pStart := updated.PeriodStart.UTC()
	pEnd := updated.PeriodEnd.UTC()
	for !now.Before(pEnd) {
		pStart = pEnd
		pEnd = pStart.AddDate(0, 1, 0)
	}
	updated.PeriodStart = pStart
	updated.PeriodEnd = &pEnd
	updated.UpdatedAt = now

	if ValidPaidTier(updated.Tier) {
		if amount := updated.EffectiveHostedCreditGrantMicros(); amount > 0 {
			endCopy := pEnd
			grant = &CreditGrant{
				ID:             ManualPeriodGrantID(updated.UserID, pStart),
				UserID:         updated.UserID,
				Kind:           GrantPeriod,
				AmountMicros:   amount,
				ValidFrom:      pStart,
				ValidUntil:     &endCopy,
				SourceProvider: ProviderManual,
				CreatedAt:      now,
			}
		}
	}
	return updated, grant, true
}

// SelectWinningSubscription chooses the highest-precedence subscription when a user
// holds multiple subscriptions (e.g., Stripe + Apple IAP).
func SelectWinningSubscription(subs []Subscription, now time.Time) (Subscription, bool) {
	if len(subs) == 0 {
		return Subscription{}, false
	}
	now = now.UTC()
	statusScore := func(s Subscription) int {
		switch s.Status {
		case StatusActive:
			return 4
		case StatusBillingRetry:
			if s.GraceUntil != nil {
				if now.Before(s.GraceUntil.UTC()) {
					return 3
				}
				return 1
			}
			if s.PeriodEnd != nil && now.Before(s.PeriodEnd.UTC()) {
				return 3
			}
			return 1
		case StatusCanceled:
			if s.PeriodEnd != nil && now.Before(s.PeriodEnd.UTC()) {
				return 2
			}
			return 0
		case StatusPastDue:
			return 1
		default:
			return 0
		}
	}
	best := subs[0]
	bestScore := statusScore(best)
	for _, s := range subs[1:] {
		sc := statusScore(s)
		if sc != bestScore {
			if sc > bestScore {
				best, bestScore = s, sc
			}
			continue
		}
		if r1, r2 := TierRank(s.Tier), TierRank(best.Tier); r1 != r2 {
			if r1 > r2 {
				best, bestScore = s, sc
			}
			continue
		}
		if s.PeriodEnd != nil && best.PeriodEnd != nil && !s.PeriodEnd.Equal(*best.PeriodEnd) {
			if s.PeriodEnd.After(*best.PeriodEnd) {
				best, bestScore = s, sc
			}
			continue
		}
		if s.ProviderUpdatedAt.After(best.ProviderUpdatedAt) {
			best, bestScore = s, sc
		}
	}
	return best, true
}
