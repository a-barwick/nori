package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// UsageSample represents a minimal hosted usage record used by MemoryStore to compute CreditBalance.
type UsageSample struct {
	CreatedAt            time.Time
	Billing              string
	CostUSD              *float64
	EstimatedExposureUSD float64
	CostStatus           string
	RequestStatus        string
}

type subKey struct {
	provider Provider
	env      Environment
	extSubID string
}

type eventKey struct {
	provider Provider
	eventID  string
}

type budgetKey struct {
	day   string
	scope BudgetScope
}

// MemoryStore is a concurrency-safe, deterministic in-memory billing Store for tests and DST.
type MemoryStore struct {
	mu           sync.Mutex
	Now          func() time.Time
	AllowSandbox bool
	UsageReader  func(userID string) []UsageSample
	OnNotify     func(userID string)
	entitlements map[string]Entitlement
	byAppToken   map[string]string
	subs         map[subKey]Subscription
	events       map[eventKey]bool
	grants       map[string]CreditGrant
	globalBudget map[budgetKey]GlobalBudgetUsage
}

var _ Store = (*MemoryStore)(nil)

func NewMemoryStore(now func() time.Time) *MemoryStore {
	return &MemoryStore{
		Now:          now,
		entitlements: make(map[string]Entitlement),
		byAppToken:   make(map[string]string),
		subs:         make(map[subKey]Subscription),
		events:       make(map[eventKey]bool),
		grants:       make(map[string]CreditGrant),
		globalBudget: make(map[budgetKey]GlobalBudgetUsage),
	}
}

func (m *MemoryStore) now() time.Time {
	if m != nil && m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *MemoryStore) emitNotify(userID string) {
	if m.OnNotify != nil {
		m.OnNotify(userID)
	}
}

func (m *MemoryStore) GetEntitlement(_ context.Context, userID string) (Entitlement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ent, ok := m.entitlements[userID]
	if !ok {
		return Entitlement{}, ErrEntitlementMissing
	}
	now := m.now()
	if updated, grant, changed := RolloverManualEntitlement(ent, now); changed {
		m.entitlements[userID] = updated
		ent = updated
		if grant != nil {
			if _, exists := m.grants[grant.ID]; !exists {
				m.grants[grant.ID] = grant.Clone()
			}
		}
	}
	return ent.Clone(), nil
}

func (m *MemoryStore) ProvisionTrial(_ context.Context, userID string, maxTrialsPerDay int) (Entitlement, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ent, ok := m.entitlements[userID]; ok {
		return ent.Clone(), nil
	}
	now := m.now()
	day := now.UTC().Format("2006-01-02")
	bk := budgetKey{day: day, scope: ScopeTrialsCreated}
	row := m.globalBudget[bk]
	row.Day = day
	row.Scope = ScopeTrialsCreated
	if maxTrialsPerDay > 0 && row.Count+1 > maxTrialsPerDay {
		return Entitlement{}, ErrTrialLimitExceeded
	}
	row.Count++
	m.globalBudget[bk] = row

	token := uuid.NewString()
	ent := Entitlement{
		UserID:            userID,
		Tier:              TierTrial,
		Status:            StatusActive,
		BillingProvider:   ProviderManual,
		PeriodStart:       now,
		AppAccountToken:   token,
		ProviderUpdatedAt: now,
		UpdatedAt:         now,
	}
	m.entitlements[userID] = ent
	m.byAppToken[token] = userID

	grantID := TrialGrantID(userID)
	if _, exists := m.grants[grantID]; !exists {
		m.grants[grantID] = CreditGrant{
			ID:             grantID,
			UserID:         userID,
			Kind:           GrantTrial,
			AmountMicros:   MustPolicy(TierTrial).HostedCreditMicros,
			ValidFrom:      now,
			SourceProvider: ProviderManual,
			CreatedAt:      now,
		}
	}
	return ent.Clone(), nil
}

func (m *MemoryStore) SetTier(_ context.Context, userID string, tier Tier, opts SetTierOptions) (Entitlement, error) {
	if !ValidTier(tier) {
		return Entitlement{}, ErrInvalidTier
	}
	if opts.Pending && !ValidPaidTier(tier) {
		return Entitlement{}, ErrInvalidTier
	}
	m.mu.Lock()
	now := m.now()
	ent, exists := m.entitlements[userID]
	if !exists {
		token := uuid.NewString()
		ent = Entitlement{
			UserID:            userID,
			Tier:              tier,
			Status:            StatusActive,
			BillingProvider:   ProviderManual,
			PeriodStart:       now,
			AppAccountToken:   token,
			ProviderUpdatedAt: now,
			UpdatedAt:         now,
		}
		m.byAppToken[token] = userID
	}

	if opts.Pending {
		pt := tier
		ent.PendingTier = &pt
		ent.UpdatedAt = now
		m.entitlements[userID] = ent
		m.mu.Unlock()
		m.emitNotify(userID)
		return ent.Clone(), nil
	}

	ent.Tier = tier
	ent.PendingTier = nil
	ent.BillingProvider = ProviderManual
	if opts.Status != "" {
		if !ValidStatus(opts.Status) {
			m.mu.Unlock()
			return Entitlement{}, ErrInvalidChange
		}
		ent.Status = opts.Status
	} else {
		ent.Status = StatusActive
	}
	pStart := now
	if opts.PeriodStart != nil {
		pStart = opts.PeriodStart.UTC()
	}
	ent.PeriodStart = pStart
	if opts.PeriodEnd != nil {
		pe := opts.PeriodEnd.UTC()
		ent.PeriodEnd = &pe
	} else if tier == TierTrial {
		ent.PeriodEnd = nil
	} else {
		pe := pStart.AddDate(0, 1, 0)
		ent.PeriodEnd = &pe
	}
	if opts.ClearCreditOverride {
		ent.CreditOverrideMicros = nil
	} else if opts.CreditOverrideMicros != nil {
		v := *opts.CreditOverrideMicros
		ent.CreditOverrideMicros = &v
	}
	// Do not clobber provider_updated_at on existing rows so external provider events are not
	// falsely rejected as stale after an operator manual tier change.
	if !exists {
		ent.ProviderUpdatedAt = time.Time{}
	}
	ent.UpdatedAt = now
	m.entitlements[userID] = ent

	// Close any prior active period grants at pStart so re-setting or upgrading a paid tier
	// never stacks overlapping active period credits.
	for id, g := range m.grants {
		if g.UserID == userID && g.Kind == GrantPeriod && (g.ValidUntil == nil || g.ValidUntil.UTC().After(pStart)) {
			closed := pStart
			if g.ValidFrom.UTC().After(closed) {
				closed = g.ValidFrom.UTC()
			}
			g.ValidUntil = &closed
			m.grants[id] = g
		}
	}

	if !opts.SkipGrant {
		if tier == TierTrial {
			grantID := TrialGrantID(userID)
			if _, ok := m.grants[grantID]; !ok {
				if amt := ent.EffectiveHostedCreditGrantMicros(); amt > 0 {
					m.grants[grantID] = CreditGrant{
						ID:             grantID,
						UserID:         userID,
						Kind:           GrantTrial,
						AmountMicros:   amt,
						ValidFrom:      pStart,
						SourceProvider: ProviderManual,
						CreatedAt:      now,
					}
				}
			}
		} else if tier == TierStandard || tier == TierPower {
			if amt := ent.EffectiveHostedCreditGrantMicros(); amt > 0 {
				grantID := ManualPeriodGrantID(userID, pStart)
				if _, exists := m.grants[grantID]; exists {
					grantID = ManualSetTierGrantID(userID, tier, pStart)
				}
				if _, ok := m.grants[grantID]; !ok {
					var vu *time.Time
					if ent.PeriodEnd != nil {
						t := ent.PeriodEnd.UTC()
						vu = &t
					}
					m.grants[grantID] = CreditGrant{
						ID:             grantID,
						UserID:         userID,
						Kind:           GrantPeriod,
						AmountMicros:   amt,
						ValidFrom:      pStart,
						ValidUntil:     vu,
						SourceProvider: ProviderManual,
						CreatedAt:      now,
					}
				}
			}
		}
	}
	m.mu.Unlock()
	m.emitNotify(userID)
	return ent.Clone(), nil
}

func (m *MemoryStore) Suspend(_ context.Context, userID, reason string) (Entitlement, error) {
	m.mu.Lock()
	ent, ok := m.entitlements[userID]
	if !ok {
		m.mu.Unlock()
		return Entitlement{}, ErrEntitlementMissing
	}
	now := m.now()
	ent.SuspendedAt = &now
	ent.SuspendedReason = strings.TrimSpace(reason)
	ent.UpdatedAt = now
	m.entitlements[userID] = ent
	m.mu.Unlock()
	m.emitNotify(userID)
	return ent.Clone(), nil
}

func (m *MemoryStore) Unsuspend(_ context.Context, userID string) (Entitlement, error) {
	m.mu.Lock()
	ent, ok := m.entitlements[userID]
	if !ok {
		m.mu.Unlock()
		return Entitlement{}, ErrEntitlementMissing
	}
	now := m.now()
	ent.SuspendedAt = nil
	ent.SuspendedReason = ""
	ent.UpdatedAt = now
	m.entitlements[userID] = ent
	m.mu.Unlock()
	m.emitNotify(userID)
	return ent.Clone(), nil
}

func (m *MemoryStore) AddCreditGrant(_ context.Context, grant CreditGrant) error {
	isExternal := grant.SourceProvider == ProviderStripe || grant.SourceProvider == ProviderAppleIAP
	if err := ValidateCreditGrant(grant, isExternal); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.grants[grant.ID]; exists {
		return nil
	}
	cp := grant.Clone()
	if cp.SourceProvider == "" {
		cp.SourceProvider = ProviderManual
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = m.now()
	}
	if cp.ValidFrom.IsZero() {
		cp.ValidFrom = cp.CreatedAt
	}
	m.grants[cp.ID] = cp
	return nil
}

func (m *MemoryStore) ListCreditGrants(_ context.Context, userID string) ([]CreditGrant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]CreditGrant, 0)
	for _, g := range m.grants {
		if g.UserID == userID {
			out = append(out, g.Clone())
		}
	}
	slices.SortFunc(out, func(a, b CreditGrant) int {
		if c := a.ValidFrom.Compare(b.ValidFrom); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func (m *MemoryStore) CreditBalance(ctx context.Context, userID string) (CreditBalance, error) {
	ent, err := m.GetEntitlement(ctx, userID)
	if err != nil {
		return CreditBalance{}, err
	}
	grants, err := m.ListCreditGrants(ctx, userID)
	if err != nil {
		return CreditBalance{}, err
	}
	var samples []UsageSample
	if m.UsageReader != nil {
		samples = m.UsageReader(userID)
	}
	return ComputeCreditBalance(ent, grants, samples, m.now()), nil
}

// ComputeCreditBalance calculates exact micro-USD hosted credit balance from grants and usage samples.
// Only request_status == "not_sent" is free; stale pending (>15m) and stale_aborted rows are charged
// at CEIL(estimated_exposure_usd) (falling back to DefaultUnknownExposureMicros when <= 0).
// Each usage row is deterministically assigned to at most ONE expired positive grant ordered by
// (valid_until ASC, id ASC) so overlapping expired grants never double-absorb usage.
func ComputeCreditBalance(ent Entitlement, grants []CreditGrant, usage []UsageSample, now time.Time) CreditBalance {
	now = now.UTC()
	staleCutoff := now.Add(-StalePendingTTL)

	type expiredGrant struct {
		id           string
		from         time.Time
		until        time.Time
		amountMicros int64
	}
	var expired []expiredGrant
	var activeGranted int64
	for _, g := range grants {
		if g.IsActiveAt(now) {
			activeGranted += g.AmountMicros
			continue
		}
		if g.ValidUntil != nil && !now.Before(g.ValidUntil.UTC()) && g.AmountMicros > 0 && g.ValidUntil.UTC().After(g.ValidFrom.UTC()) {
			expired = append(expired, expiredGrant{
				id:           g.ID,
				from:         g.ValidFrom.UTC(),
				until:        g.ValidUntil.UTC(),
				amountMicros: g.AmountMicros,
			})
		}
	}
	slices.SortFunc(expired, func(a, b expiredGrant) int {
		if c := a.until.Compare(b.until); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})

	var totalSpent, totalPending int64
	usedByGrant := make([]int64, len(expired))

	exposureMicros := func(est float64) int64 {
		if est > 0 {
			if m := USDToMicrosCeil(est); m > 0 {
				return m
			}
		}
		return DefaultUnknownExposureMicros
	}

	for _, u := range usage {
		if u.RequestStatus == "not_sent" {
			continue
		}
		effectiveBilling := u.Billing
		if effectiveBilling == "" {
			if u.CostStatus == "local" {
				effectiveBilling = "local"
			} else {
				effectiveBilling = "hosted"
			}
		}
		if effectiveBilling != "hosted" {
			continue
		}

		var spentMicros, pendingMicros int64
		switch {
		case u.CostUSD != nil && *u.CostUSD > 0:
			spentMicros = USDToMicros(*u.CostUSD)
		case u.RequestStatus == "pending":
			exp := exposureMicros(u.EstimatedExposureUSD)
			if !u.CreatedAt.UTC().Before(staleCutoff) {
				pendingMicros = exp
			} else {
				// Stale pending (>15m) is treated as spent at estimated exposure (not $0).
				spentMicros = exp
			}
		case u.RequestStatus == "stale_aborted":
			spentMicros = exposureMicros(u.EstimatedExposureUSD)
		case u.CostStatus != "provider" && u.CostStatus != "calculated" && u.CostStatus != "local":
			spentMicros = exposureMicros(u.EstimatedExposureUSD)
		}

		totalSpent += spentMicros
		totalPending += pendingMicros

		if len(expired) > 0 && (spentMicros+pendingMicros) > 0 {
			ts := u.CreatedAt.UTC()
			for idx, eg := range expired {
				if !ts.Before(eg.from) && ts.Before(eg.until) {
					usedByGrant[idx] += spentMicros + pendingMicros
					break
				}
			}
		}
	}

	var absorbedByExpired int64
	for idx, eg := range expired {
		used := usedByGrant[idx]
		if used > eg.amountMicros {
			absorbedByExpired += eg.amountMicros
		} else {
			absorbedByExpired += used
		}
	}

	netConsumed := (totalSpent + totalPending) - absorbedByExpired
	if netConsumed < 0 {
		netConsumed = 0
	}
	netSpent := totalSpent - absorbedByExpired
	if netSpent < 0 {
		netSpent = 0
	}

	return CreditBalance{
		GrantedMicros:   activeGranted,
		SpentMicros:     netSpent,
		PendingMicros:   totalPending,
		AvailableMicros: activeGranted - netConsumed,
		PeriodStart:     ent.PeriodStart,
		PeriodEnd:       ent.PeriodEnd,
	}
}

func (m *MemoryStore) ApplyEvent(_ context.Context, change Change) (Entitlement, error) {
	if err := ValidateChange(change, !m.AllowSandbox); err != nil {
		return Entitlement{}, err
	}
	env := change.Environment
	if env == "" {
		env = EnvProduction
	}

	m.mu.Lock()
	userID := strings.TrimSpace(change.UserID)
	if userID == "" && change.AppAccountToken != "" {
		userID = m.byAppToken[strings.TrimSpace(change.AppAccountToken)]
	}
	if userID == "" {
		m.mu.Unlock()
		return Entitlement{}, ErrEntitlementMissing
	}
	ent, ok := m.entitlements[userID]
	if !ok {
		m.mu.Unlock()
		return Entitlement{}, ErrEntitlementMissing
	}

	ek := eventKey{provider: change.Provider, eventID: change.EventID}
	if m.events[ek] {
		m.mu.Unlock()
		return ent.Clone(), nil
	}

	now := m.now()
	if change.Subscription != nil {
		sk := subKey{provider: change.Provider, env: env, extSubID: change.Subscription.ExternalSubscriptionID}
		if existing, found := m.subs[sk]; found {
			if existing.UserID != userID {
				m.mu.Unlock()
				return Entitlement{}, ErrSubscriptionConflict
			}
			if change.ProviderUpdatedAt.UTC().Before(existing.ProviderUpdatedAt.UTC()) {
				m.mu.Unlock()
				return Entitlement{}, ErrStaleEvent
			}
		}
		sub := Subscription{
			ID:                     uuid.NewString(),
			UserID:                 userID,
			Provider:               change.Provider,
			Environment:            env,
			ExternalCustomerID:     change.Subscription.ExternalCustomerID,
			ExternalSubscriptionID: change.Subscription.ExternalSubscriptionID,
			ExternalProductID:      change.Subscription.ExternalProductID,
			Tier:                   change.Subscription.Tier,
			Status:                 change.Subscription.Status,
			PeriodStart:            change.Subscription.PeriodStart.UTC(),
			ProviderUpdatedAt:      change.ProviderUpdatedAt.UTC(),
			UpdatedAt:              now,
		}
		if change.Subscription.PeriodEnd != nil {
			t := change.Subscription.PeriodEnd.UTC()
			sub.PeriodEnd = &t
		}
		if change.Subscription.GraceUntil != nil {
			t := change.Subscription.GraceUntil.UTC()
			sub.GraceUntil = &t
		}
		m.subs[sk] = sub

		// Recompute user entitlement from all subscriptions for userID.
		// IMPORTANT: never clear suspended_at / suspended_reason!
		var userSubs []Subscription
		for _, s := range m.subs {
			if s.UserID == userID {
				userSubs = append(userSubs, s)
			}
		}
		if winner, hasWinner := SelectWinningSubscription(userSubs, now); hasWinner {
			ent.Tier = winner.Tier
			ent.Status = winner.Status
			ent.BillingProvider = winner.Provider
			ent.PeriodStart = winner.PeriodStart
			ent.PeriodEnd = winner.PeriodEnd
			ent.GraceUntil = winner.GraceUntil
			if winner.ExternalSubscriptionID == sub.ExternalSubscriptionID && winner.Provider == sub.Provider {
				ent.PendingTier = change.Subscription.PendingTier
			}
			if winner.ProviderUpdatedAt.After(ent.ProviderUpdatedAt) {
				ent.ProviderUpdatedAt = winner.ProviderUpdatedAt
			}
		}
	} else {
		if change.ProviderUpdatedAt.UTC().Before(ent.ProviderUpdatedAt.UTC()) {
			m.mu.Unlock()
			return Entitlement{}, ErrStaleEvent
		}
		ent.ProviderUpdatedAt = change.ProviderUpdatedAt.UTC()
	}

	ent.UpdatedAt = now
	m.entitlements[userID] = ent

	if change.CreditDeltaMicros != 0 {
		kind := change.CreditGrantKind
		if kind == "" {
			if change.CreditDeltaMicros < 0 {
				kind = GrantClawback
			} else {
				kind = GrantPeriod
			}
		}
		grantID := EventGrantID(change.Provider, change.EventID, change.CreditDeltaMicros)
		if _, exists := m.grants[grantID]; !exists {
			validFrom := ent.PeriodStart
			if validFrom.IsZero() {
				validFrom = now
			}
			validUntil := change.CreditValidUntil
			if validUntil == nil && kind == GrantPeriod {
				validUntil = ent.PeriodEnd
			}
			m.grants[grantID] = CreditGrant{
				ID:             grantID,
				UserID:         userID,
				Kind:           kind,
				AmountMicros:   change.CreditDeltaMicros,
				ValidFrom:      validFrom,
				ValidUntil:     validUntil,
				SourceProvider: change.Provider,
				SourceEventID:  change.EventID,
				CreatedAt:      now,
			}
		}
	}

	_ = json.Valid(change.Payload)
	m.events[ek] = true
	m.mu.Unlock()
	m.emitNotify(userID)
	return ent.Clone(), nil
}

func (m *MemoryStore) CheckAndIncrementGlobalBudget(_ context.Context, day time.Time, scope BudgetScope, deltaMicros, maxMicros int64, deltaCount, maxCount int) error {
	if !ValidBudgetScope(scope) {
		return fmt.Errorf("%w: invalid scope %q", ErrInvalidChange, scope)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dayStr := day.UTC().Format("2006-01-02")
	bk := budgetKey{day: dayStr, scope: scope}
	row := m.globalBudget[bk]
	row.Day = dayStr
	row.Scope = scope
	if maxMicros > 0 && row.SpentMicros+deltaMicros > maxMicros {
		return ErrGlobalBudgetExceeded
	}
	if maxCount > 0 && row.Count+deltaCount > maxCount {
		if scope == ScopeTrialsCreated {
			return ErrTrialLimitExceeded
		}
		return ErrGlobalBudgetExceeded
	}
	row.SpentMicros += deltaMicros
	row.Count += deltaCount
	m.globalBudget[bk] = row
	return nil
}

func (m *MemoryStore) ReconcileGlobalBudget(_ context.Context, day time.Time, scope BudgetScope, deltaMicros int64) error {
	if !ValidBudgetScope(scope) || deltaMicros == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dayStr := day.UTC().Format("2006-01-02")
	bk := budgetKey{day: dayStr, scope: scope}
	row := m.globalBudget[bk]
	row.Day = dayStr
	row.Scope = scope
	row.SpentMicros += deltaMicros
	if row.SpentMicros < 0 {
		row.SpentMicros = 0
	}
	m.globalBudget[bk] = row
	return nil
}

func (m *MemoryStore) GetGlobalBudget(_ context.Context, day time.Time, scope BudgetScope) (GlobalBudgetUsage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dayStr := day.UTC().Format("2006-01-02")
	bk := budgetKey{day: dayStr, scope: scope}
	row, ok := m.globalBudget[bk]
	if !ok {
		return GlobalBudgetUsage{Day: dayStr, Scope: scope}, nil
	}
	return row, nil
}

func (m *MemoryStore) NotifyEntitlementChanged(_ context.Context, userID string) error {
	m.emitNotify(userID)
	return nil
}
