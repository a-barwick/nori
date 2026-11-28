package billing

import (
	"context"
	"strings"
	"time"
)

// Store is the persistence contract for entitlements, subscriptions, credit grants,
// and fleet-wide daily hosted budgets. Implemented by PostgresStore and MemoryStore.
type Store interface {
	GetEntitlement(ctx context.Context, userID string) (Entitlement, error)
	ProvisionTrial(ctx context.Context, userID string, maxTrialsPerDay int) (Entitlement, error)
	SetTier(ctx context.Context, userID string, tier Tier, opts SetTierOptions) (Entitlement, error)
	Suspend(ctx context.Context, userID, reason string) (Entitlement, error)
	Unsuspend(ctx context.Context, userID string) (Entitlement, error)
	AddCreditGrant(ctx context.Context, grant CreditGrant) error
	ListCreditGrants(ctx context.Context, userID string) ([]CreditGrant, error)
	CreditBalance(ctx context.Context, userID string) (CreditBalance, error)
	ApplyEvent(ctx context.Context, change Change) (Entitlement, error)
	CheckAndIncrementGlobalBudget(ctx context.Context, day time.Time, scope BudgetScope, deltaMicros, maxMicros int64, deltaCount, maxCount int) error
	ReconcileGlobalBudget(ctx context.Context, day time.Time, scope BudgetScope, deltaMicros int64) error
	GetGlobalBudget(ctx context.Context, day time.Time, scope BudgetScope) (GlobalBudgetUsage, error)
	NotifyEntitlementChanged(ctx context.Context, userID string) error
}

// Service wraps a billing Store with environment validation (production rejects sandbox events)
// and convenience helpers consumed by API, worker, computer, and CLI layers.
type Service struct {
	Store         Store
	ProductionEnv bool
	GlobalBudget  GlobalBudgetConfig
	Now           func() time.Time
}

func NewService(store Store, productionEnv bool, globalBudget GlobalBudgetConfig) *Service {
	return &Service{
		Store:         store,
		ProductionEnv: productionEnv,
		GlobalBudget:  globalBudget,
	}
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// GetEntitlement returns the user's current entitlement, failing closed with ErrEntitlementMissing
// if no entitlement row exists.
func (s *Service) GetEntitlement(ctx context.Context, userID string) (Entitlement, error) {
	if s == nil || s.Store == nil {
		return Entitlement{}, ErrEntitlementMissing
	}
	return s.Store.GetEntitlement(ctx, userID)
}

// CheckAccess verifies that the user has an entitlement row, is not suspended, and has an active billing status.
func (s *Service) CheckAccess(ctx context.Context, userID string) (Entitlement, error) {
	ent, err := s.GetEntitlement(ctx, userID)
	if err != nil {
		return Entitlement{}, err
	}
	if err := ent.CheckAccess(s.now()); err != nil {
		return ent, err
	}
	return ent, nil
}

// Summary returns the user's entitlement, tier policy, and hosted credit balance.
func (s *Service) Summary(ctx context.Context, userID string) (Summary, error) {
	ent, err := s.GetEntitlement(ctx, userID)
	if err != nil {
		return Summary{}, err
	}
	bal, err := s.Store.CreditBalance(ctx, userID)
	if err != nil {
		return Summary{}, err
	}
	return Summary{
		Entitlement: ent,
		Policy:      ent.EffectivePolicy(),
		Balance:     bal,
	}, nil
}

// SetTier updates a user's manual tier and triggers cross-instance notify eviction.
func (s *Service) SetTier(ctx context.Context, userID string, tier Tier, opts SetTierOptions) (Entitlement, error) {
	if s == nil || s.Store == nil {
		return Entitlement{}, ErrEntitlementMissing
	}
	return s.Store.SetTier(ctx, userID, tier, opts)
}

// Suspend sets suspended_at/suspended_reason without altering billing status and notifies replicas.
func (s *Service) Suspend(ctx context.Context, userID, reason string) (Entitlement, error) {
	if s == nil || s.Store == nil {
		return Entitlement{}, ErrEntitlementMissing
	}
	return s.Store.Suspend(ctx, userID, strings.TrimSpace(reason))
}

// Unsuspend clears suspended_at/suspended_reason without altering billing status and notifies replicas.
func (s *Service) Unsuspend(ctx context.Context, userID string) (Entitlement, error) {
	if s == nil || s.Store == nil {
		return Entitlement{}, ErrEntitlementMissing
	}
	return s.Store.Unsuspend(ctx, userID)
}

// ApplyEvent validates environment and applies an idempotent full-snapshot provider change
// (Stripe / Apple IAP) in a single transaction.
func (s *Service) ApplyEvent(ctx context.Context, change Change) (Entitlement, error) {
	if s == nil || s.Store == nil {
		return Entitlement{}, ErrEntitlementMissing
	}
	if err := ValidateChange(change, s.ProductionEnv); err != nil {
		return Entitlement{}, err
	}
	return s.Store.ApplyEvent(ctx, change)
}

func (s *Service) NotifyEntitlementChanged(ctx context.Context, userID string) error {
	if s == nil || s.Store == nil {
		return nil
	}
	return s.Store.NotifyEntitlementChanged(ctx, userID)
}

// ValidateChange checks required fields and environment safety on a provider Change.
func ValidateChange(change Change, productionEnv bool) error {
	if !ValidProvider(change.Provider) {
		return ErrInvalidChange
	}
	if strings.TrimSpace(change.EventID) == "" || strings.TrimSpace(change.EventType) == "" {
		return ErrInvalidChange
	}
	if change.ProviderUpdatedAt.IsZero() {
		return ErrInvalidChange
	}
	if strings.TrimSpace(change.UserID) == "" && strings.TrimSpace(change.AppAccountToken) == "" {
		return ErrInvalidChange
	}
	env := change.Environment
	if env == "" {
		env = EnvProduction
	}
	if !ValidEnvironment(env) {
		return ErrInvalidEnvironment
	}
	if productionEnv && env == EnvSandbox {
		return ErrInvalidEnvironment
	}
	if change.Subscription != nil {
		sub := change.Subscription
		if strings.TrimSpace(sub.ExternalSubscriptionID) == "" {
			return ErrInvalidChange
		}
		if !ValidPaidTier(sub.Tier) {
			return ErrInvalidTier
		}
		if !ValidStatus(sub.Status) {
			return ErrInvalidChange
		}
		if sub.PendingTier != nil && !ValidPaidTier(*sub.PendingTier) {
			return ErrInvalidTier
		}
		if sub.PeriodStart.IsZero() {
			return ErrInvalidChange
		}
	}
	if change.CreditGrantKind != "" && !ValidGrantKind(change.CreditGrantKind) {
		return ErrInvalidChange
	}
	return nil
}
