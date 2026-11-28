package memstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/billing"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

var (
	_ billing.Store                        = (*Backend)(nil)
	_ docstore.AtomicBillingReserver       = (*Backend)(nil)
	_ docstore.UsageFinalizationReconciler = (*Backend)(nil)
	_ docstore.TrialProvisioner            = (*transaction)(nil)
	_ users.TrialCapSetter                 = (*Backend)(nil)
)

type billingSubKey struct {
	provider billing.Provider
	env      billing.Environment
	extSubID string
}

type billingEventKey struct {
	provider billing.Provider
	eventID  string
}

type billingBudgetKey struct {
	day   string
	scope billing.BudgetScope
}

type stagedTrialProvision struct {
	userID          string
	now             time.Time
	maxTrialsPerDay int
}

type billingState struct {
	entitlements map[string]billing.Entitlement
	byAppToken   map[string]string
	subs         map[billingSubKey]billing.Subscription
	events       map[billingEventKey]bool
	grants       map[string]billing.CreditGrant
	globalBudget map[billingBudgetKey]billing.GlobalBudgetUsage
	trialMaxDay  int
	allowSandbox bool
}

func (b *Backend) SetAllowSandboxBilling(allow bool) {
	b.mu.Lock()
	b.billing.allowSandbox = allow
	b.mu.Unlock()
}

func newBillingState() billingState {
	return billingState{
		entitlements: make(map[string]billing.Entitlement),
		byAppToken:   make(map[string]string),
		subs:         make(map[billingSubKey]billing.Subscription),
		events:       make(map[billingEventKey]bool),
		grants:       make(map[string]billing.CreditGrant),
		globalBudget: make(map[billingBudgetKey]billing.GlobalBudgetUsage),
	}
}

func (s *billingState) isEmpty() bool {
	return len(s.entitlements) == 0 && len(s.subs) == 0 && len(s.events) == 0 && len(s.grants) == 0 && len(s.globalBudget) == 0
}

func (b *Backend) SetTrialMaxSignupsPerDay(maxPerDay int) {
	b.mu.Lock()
	b.billing.trialMaxDay = maxPerDay
	b.mu.Unlock()
}

func (t *transaction) ProvisionTrial(ctx context.Context, userID string, now time.Time, maxTrialsPerDay int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.readOnly {
		return ErrReadOnly
	}
	t.trialProvisions = append(t.trialProvisions, stagedTrialProvision{
		userID:          userID,
		now:             now.UTC(),
		maxTrialsPerDay: maxTrialsPerDay,
	})
	return nil
}

func (b *Backend) provisionTrialLocked(userID string, now time.Time, maxTrialsPerDay int) (billing.Entitlement, error) {
	if ent, ok := b.billing.entitlements[userID]; ok {
		return ent.Clone(), nil
	}
	if now.IsZero() {
		now = b.now()
	}
	now = now.UTC()
	if maxTrialsPerDay <= 0 {
		maxTrialsPerDay = b.billing.trialMaxDay
	}
	day := now.Format("2006-01-02")
	bk := billingBudgetKey{day: day, scope: billing.ScopeTrialsCreated}
	row := b.billing.globalBudget[bk]
	row.Day = day
	row.Scope = billing.ScopeTrialsCreated
	if maxTrialsPerDay > 0 && row.Count+1 > maxTrialsPerDay {
		return billing.Entitlement{}, billing.ErrTrialLimitExceeded
	}
	row.Count++
	b.billing.globalBudget[bk] = row

	token := uuid.NewString()
	ent := billing.Entitlement{
		UserID:            userID,
		Tier:              billing.TierTrial,
		Status:            billing.StatusActive,
		BillingProvider:   billing.ProviderManual,
		PeriodStart:       now,
		AppAccountToken:   token,
		ProviderUpdatedAt: now,
		UpdatedAt:         now,
	}
	b.billing.entitlements[userID] = ent
	b.billing.byAppToken[token] = userID

	grantID := billing.TrialGrantID(userID)
	if _, exists := b.billing.grants[grantID]; !exists {
		b.billing.grants[grantID] = billing.CreditGrant{
			ID:             grantID,
			UserID:         userID,
			Kind:           billing.GrantTrial,
			AmountMicros:   billing.MustPolicy(billing.TierTrial).HostedCreditMicros,
			ValidFrom:      now,
			SourceProvider: billing.ProviderManual,
			CreatedAt:      now,
		}
	}
	return ent.Clone(), nil
}

func (b *Backend) getEntitlementLocked(userID string, now time.Time) (billing.Entitlement, error) {
	ent, ok := b.billing.entitlements[userID]
	if !ok {
		return billing.Entitlement{}, billing.ErrEntitlementMissing
	}
	if updated, grant, changed := billing.RolloverManualEntitlement(ent, now); changed {
		b.billing.entitlements[userID] = updated
		ent = updated
		if grant != nil {
			if _, exists := b.billing.grants[grant.ID]; !exists {
				b.billing.grants[grant.ID] = grant.Clone()
			}
		}
	}
	return ent.Clone(), nil
}

func (b *Backend) usageSamplesLocked(dataOwner string) []billing.UsageSample {
	collection := "owners/" + dataOwner + "/model_usage"
	colDocs := b.docs[collection]
	if len(colDocs) == 0 {
		return nil
	}
	out := make([]billing.UsageSample, 0, len(colDocs))
	for _, raw := range colDocs {
		var u struct {
			CreatedAt            time.Time `json:"created_at"`
			Billing              string    `json:"billing"`
			CostUSD              *float64  `json:"cost_usd"`
			EstimatedExposureUSD float64   `json:"estimated_exposure_usd"`
			CostStatus           string    `json:"cost_status"`
			RequestStatus        string    `json:"request_status"`
		}
		if json.Unmarshal(raw, &u) != nil {
			continue
		}
		out = append(out, billing.UsageSample{
			CreatedAt:            u.CreatedAt,
			Billing:              u.Billing,
			CostUSD:              u.CostUSD,
			EstimatedExposureUSD: u.EstimatedExposureUSD,
			CostStatus:           u.CostStatus,
			RequestStatus:        u.RequestStatus,
		})
	}
	return out
}

func (b *Backend) userGrantsLocked(userID string) []billing.CreditGrant {
	out := make([]billing.CreditGrant, 0)
	for _, g := range b.billing.grants {
		if g.UserID == userID {
			out = append(out, g.Clone())
		}
	}
	slices.SortFunc(out, func(a, b billing.CreditGrant) int {
		if c := a.ValidFrom.Compare(b.ValidFrom); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

func (b *Backend) GetEntitlement(ctx context.Context, userID string) (billing.Entitlement, error) {
	if err := ctx.Err(); err != nil {
		return billing.Entitlement{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.getEntitlementLocked(userID, b.now())
}

func (b *Backend) ProvisionTrial(ctx context.Context, userID string, maxTrialsPerDay int) (billing.Entitlement, error) {
	if err := ctx.Err(); err != nil {
		return billing.Entitlement{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.provisionTrialLocked(userID, b.now(), maxTrialsPerDay)
}

func (b *Backend) SetTier(ctx context.Context, userID string, tier billing.Tier, opts billing.SetTierOptions) (billing.Entitlement, error) {
	if err := ctx.Err(); err != nil {
		return billing.Entitlement{}, err
	}
	if !billing.ValidTier(tier) {
		return billing.Entitlement{}, billing.ErrInvalidTier
	}
	if opts.Pending && !billing.ValidPaidTier(tier) {
		return billing.Entitlement{}, billing.ErrInvalidTier
	}
	b.mu.Lock()
	now := b.now()
	ent, exists := b.billing.entitlements[userID]
	if !exists {
		if opts.Pending {
			b.mu.Unlock()
			return billing.Entitlement{}, billing.ErrEntitlementMissing
		}
		token := uuid.NewString()
		ent = billing.Entitlement{
			UserID:            userID,
			Tier:              tier,
			Status:            billing.StatusActive,
			BillingProvider:   billing.ProviderManual,
			PeriodStart:       now,
			AppAccountToken:   token,
			ProviderUpdatedAt: now,
			UpdatedAt:         now,
		}
		b.billing.byAppToken[token] = userID
	}
	if opts.Pending {
		pt := tier
		ent.PendingTier = &pt
		ent.UpdatedAt = now
		b.billing.entitlements[userID] = ent
		b.mu.Unlock()
		b.emitEntitlementWake(userID)
		return ent.Clone(), nil
	}
	ent.Tier = tier
	ent.PendingTier = nil
	ent.BillingProvider = billing.ProviderManual
	if opts.Status != "" {
		if !billing.ValidStatus(opts.Status) {
			b.mu.Unlock()
			return billing.Entitlement{}, billing.ErrInvalidChange
		}
		ent.Status = opts.Status
	} else {
		ent.Status = billing.StatusActive
	}
	pStart := now
	if opts.PeriodStart != nil {
		pStart = opts.PeriodStart.UTC()
	}
	ent.PeriodStart = pStart
	if opts.PeriodEnd != nil {
		pe := opts.PeriodEnd.UTC()
		ent.PeriodEnd = &pe
	} else if tier == billing.TierTrial {
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
	if !exists {
		ent.ProviderUpdatedAt = time.Time{}
	}
	ent.UpdatedAt = now
	b.billing.entitlements[userID] = ent

	// Close any prior active period grants at pStart so overlapping active grants never stack free credit.
	for id, g := range b.billing.grants {
		if g.UserID == userID && g.Kind == billing.GrantPeriod && (g.ValidUntil == nil || g.ValidUntil.UTC().After(pStart)) {
			closed := pStart
			if g.ValidFrom.UTC().After(closed) {
				closed = g.ValidFrom.UTC()
			}
			g.ValidUntil = &closed
			b.billing.grants[id] = g
		}
	}

	if !opts.SkipGrant {
		if tier == billing.TierTrial {
			grantID := billing.TrialGrantID(userID)
			if _, ok := b.billing.grants[grantID]; !ok {
				if amt := ent.EffectiveHostedCreditGrantMicros(); amt > 0 {
					b.billing.grants[grantID] = billing.CreditGrant{
						ID:             grantID,
						UserID:         userID,
						Kind:           billing.GrantTrial,
						AmountMicros:   amt,
						ValidFrom:      pStart,
						SourceProvider: billing.ProviderManual,
						CreatedAt:      now,
					}
				}
			}
		} else if billing.ValidPaidTier(tier) {
			if amt := ent.EffectiveHostedCreditGrantMicros(); amt > 0 {
				grantID := billing.ManualPeriodGrantID(userID, pStart)
				if _, exists := b.billing.grants[grantID]; exists {
					grantID = billing.ManualSetTierGrantID(userID, tier, pStart)
				}
				if _, ok := b.billing.grants[grantID]; !ok {
					var vu *time.Time
					if ent.PeriodEnd != nil {
						t := ent.PeriodEnd.UTC()
						vu = &t
					}
					b.billing.grants[grantID] = billing.CreditGrant{
						ID:             grantID,
						UserID:         userID,
						Kind:           billing.GrantPeriod,
						AmountMicros:   amt,
						ValidFrom:      pStart,
						ValidUntil:     vu,
						SourceProvider: billing.ProviderManual,
						CreatedAt:      now,
					}
				}
			}
		}
	}
	b.mu.Unlock()
	b.emitEntitlementWake(userID)
	return ent.Clone(), nil
}

func (b *Backend) Suspend(ctx context.Context, userID, reason string) (billing.Entitlement, error) {
	if err := ctx.Err(); err != nil {
		return billing.Entitlement{}, err
	}
	b.mu.Lock()
	ent, ok := b.billing.entitlements[userID]
	if !ok {
		b.mu.Unlock()
		return billing.Entitlement{}, billing.ErrEntitlementMissing
	}
	now := b.now()
	ent.SuspendedAt = &now
	ent.SuspendedReason = strings.TrimSpace(reason)
	ent.UpdatedAt = now
	b.billing.entitlements[userID] = ent
	b.mu.Unlock()
	b.emitEntitlementWake(userID)
	return ent.Clone(), nil
}

func (b *Backend) Unsuspend(ctx context.Context, userID string) (billing.Entitlement, error) {
	if err := ctx.Err(); err != nil {
		return billing.Entitlement{}, err
	}
	b.mu.Lock()
	ent, ok := b.billing.entitlements[userID]
	if !ok {
		b.mu.Unlock()
		return billing.Entitlement{}, billing.ErrEntitlementMissing
	}
	now := b.now()
	ent.SuspendedAt = nil
	ent.SuspendedReason = ""
	ent.UpdatedAt = now
	b.billing.entitlements[userID] = ent
	b.mu.Unlock()
	b.emitEntitlementWake(userID)
	return ent.Clone(), nil
}

func (b *Backend) AddCreditGrant(ctx context.Context, grant billing.CreditGrant) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	isExternal := grant.SourceProvider == billing.ProviderStripe || grant.SourceProvider == billing.ProviderAppleIAP
	if err := billing.ValidateCreditGrant(grant, isExternal); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.billing.grants[grant.ID]; exists {
		return nil
	}
	cp := grant.Clone()
	if cp.SourceProvider == "" {
		cp.SourceProvider = billing.ProviderManual
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = b.now()
	}
	if cp.ValidFrom.IsZero() {
		cp.ValidFrom = cp.CreatedAt
	}
	b.billing.grants[cp.ID] = cp
	return nil
}

func (b *Backend) ListCreditGrants(ctx context.Context, userID string) ([]billing.CreditGrant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.userGrantsLocked(userID), nil
}

func (b *Backend) CreditBalance(ctx context.Context, userID string) (billing.CreditBalance, error) {
	if err := ctx.Err(); err != nil {
		return billing.CreditBalance{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	ent, err := b.getEntitlementLocked(userID, now)
	if err != nil {
		return billing.CreditBalance{}, err
	}
	return billing.ComputeCreditBalance(ent, b.userGrantsLocked(userID), b.usageSamplesLocked(userID), now), nil
}

func (b *Backend) ApplyEvent(ctx context.Context, change billing.Change) (billing.Entitlement, error) {
	if err := ctx.Err(); err != nil {
		return billing.Entitlement{}, err
	}
	b.mu.Lock()
	allowSandbox := b.billing.allowSandbox
	if err := billing.ValidateChange(change, !allowSandbox); err != nil {
		b.mu.Unlock()
		return billing.Entitlement{}, err
	}
	env := change.Environment
	if env == "" {
		env = billing.EnvProduction
	}

	userID := strings.TrimSpace(change.UserID)
	if userID == "" && change.AppAccountToken != "" {
		userID = b.billing.byAppToken[strings.TrimSpace(change.AppAccountToken)]
	}
	if userID == "" {
		b.mu.Unlock()
		return billing.Entitlement{}, billing.ErrEntitlementMissing
	}
	ent, ok := b.billing.entitlements[userID]
	if !ok {
		b.mu.Unlock()
		return billing.Entitlement{}, billing.ErrEntitlementMissing
	}

	ek := billingEventKey{provider: change.Provider, eventID: change.EventID}
	if b.billing.events[ek] {
		b.mu.Unlock()
		return ent.Clone(), nil
	}

	now := b.now()
	if change.Subscription != nil {
		sk := billingSubKey{provider: change.Provider, env: env, extSubID: change.Subscription.ExternalSubscriptionID}
		if existing, found := b.billing.subs[sk]; found {
			if existing.UserID != userID {
				b.mu.Unlock()
				return billing.Entitlement{}, billing.ErrSubscriptionConflict
			}
			if change.ProviderUpdatedAt.UTC().Before(existing.ProviderUpdatedAt.UTC()) {
				b.mu.Unlock()
				return billing.Entitlement{}, billing.ErrStaleEvent
			}
		}
		sub := billing.Subscription{
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
		b.billing.subs[sk] = sub

		var userSubs []billing.Subscription
		for _, s := range b.billing.subs {
			if s.UserID == userID {
				userSubs = append(userSubs, s)
			}
		}
		if winner, hasWinner := billing.SelectWinningSubscription(userSubs, now); hasWinner {
			ent.Tier = winner.Tier
			ent.Status = winner.Status
			ent.BillingProvider = winner.Provider
			ent.PeriodStart = winner.PeriodStart
			ent.PeriodEnd = winner.PeriodEnd
			ent.GraceUntil = winner.GraceUntil
			if winner.Provider == sub.Provider && winner.ExternalSubscriptionID == sub.ExternalSubscriptionID {
				ent.PendingTier = change.Subscription.PendingTier
			}
			if winner.ProviderUpdatedAt.After(ent.ProviderUpdatedAt) {
				ent.ProviderUpdatedAt = winner.ProviderUpdatedAt
			}
		}
	} else {
		if change.ProviderUpdatedAt.UTC().Before(ent.ProviderUpdatedAt.UTC()) {
			b.mu.Unlock()
			return billing.Entitlement{}, billing.ErrStaleEvent
		}
		ent.ProviderUpdatedAt = change.ProviderUpdatedAt.UTC()
	}

	ent.UpdatedAt = now
	b.billing.entitlements[userID] = ent

	if change.CreditDeltaMicros != 0 {
		kind := change.CreditGrantKind
		if kind == "" {
			if change.CreditDeltaMicros < 0 {
				kind = billing.GrantClawback
			} else {
				kind = billing.GrantPeriod
			}
		}
		grantID := billing.EventGrantID(change.Provider, change.EventID, change.CreditDeltaMicros)
		if _, exists := b.billing.grants[grantID]; !exists {
			validFrom := ent.PeriodStart
			if validFrom.IsZero() {
				validFrom = now
			}
			validUntil := change.CreditValidUntil
			if validUntil == nil && kind == billing.GrantPeriod {
				validUntil = ent.PeriodEnd
			}
			b.billing.grants[grantID] = billing.CreditGrant{
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

	b.billing.events[ek] = true
	b.mu.Unlock()
	b.emitEntitlementWake(userID)
	return ent.Clone(), nil
}

func (b *Backend) checkAndIncrementGlobalBudgetLocked(day time.Time, scope billing.BudgetScope, deltaMicros, maxMicros int64, deltaCount, maxCount int) error {
	if !billing.ValidBudgetScope(scope) {
		return fmt.Errorf("%w: invalid scope %q", billing.ErrInvalidChange, scope)
	}
	dayStr := day.UTC().Format("2006-01-02")
	bk := billingBudgetKey{day: dayStr, scope: scope}
	row := b.billing.globalBudget[bk]
	row.Day = dayStr
	row.Scope = scope
	if maxMicros > 0 && row.SpentMicros+deltaMicros > maxMicros {
		return billing.ErrGlobalBudgetExceeded
	}
	if maxCount > 0 && row.Count+deltaCount > maxCount {
		if scope == billing.ScopeTrialsCreated {
			return billing.ErrTrialLimitExceeded
		}
		return billing.ErrGlobalBudgetExceeded
	}
	row.SpentMicros += deltaMicros
	row.Count += deltaCount
	b.billing.globalBudget[bk] = row
	return nil
}

func (b *Backend) CheckAndIncrementGlobalBudget(ctx context.Context, day time.Time, scope billing.BudgetScope, deltaMicros, maxMicros int64, deltaCount, maxCount int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checkAndIncrementGlobalBudgetLocked(day, scope, deltaMicros, maxMicros, deltaCount, maxCount)
}

func (b *Backend) reconcileGlobalBudgetLocked(day time.Time, scope billing.BudgetScope, deltaMicros int64) {
	if !billing.ValidBudgetScope(scope) || deltaMicros == 0 {
		return
	}
	dayStr := day.UTC().Format("2006-01-02")
	bk := billingBudgetKey{day: dayStr, scope: scope}
	row := b.billing.globalBudget[bk]
	row.Day = dayStr
	row.Scope = scope
	row.SpentMicros += deltaMicros
	if row.SpentMicros < 0 {
		row.SpentMicros = 0
	}
	b.billing.globalBudget[bk] = row
}

func (b *Backend) ReconcileGlobalBudget(ctx context.Context, day time.Time, scope billing.BudgetScope, deltaMicros int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reconcileGlobalBudgetLocked(day, scope, deltaMicros)
	return nil
}

func (b *Backend) GetGlobalBudget(ctx context.Context, day time.Time, scope billing.BudgetScope) (billing.GlobalBudgetUsage, error) {
	if err := ctx.Err(); err != nil {
		return billing.GlobalBudgetUsage{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	dayStr := day.UTC().Format("2006-01-02")
	bk := billingBudgetKey{day: dayStr, scope: scope}
	row, ok := b.billing.globalBudget[bk]
	if !ok {
		return billing.GlobalBudgetUsage{Day: dayStr, Scope: scope}, nil
	}
	return row, nil
}

func (b *Backend) NotifyEntitlementChanged(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.emitEntitlementWake(userID)
	return nil
}

func (b *Backend) emitEntitlementWake(userID string) {
	if strings.TrimSpace(userID) == "" {
		return
	}
	b.emitWakeEvents([]docstore.WakeEvent{{
		Channel: docstore.WakeChannel,
		Payload: docstore.EntitlementWakePayload(userID),
	}})
}

func memEffectiveBilling(rawBilling, costStatus string) string {
	switch rawBilling {
	case "hosted", "byok", "local", "system":
		return rawBilling
	default:
		if costStatus == "local" {
			return "local"
		}
		return "hosted"
	}
}

func memEffectiveCategory(rawCategory string) string {
	switch rawCategory {
	case "llm", "search", "computer", "system":
		return rawCategory
	default:
		return "llm"
	}
}

func memBudgetScopeForCategory(cat string) billing.BudgetScope {
	switch cat {
	case "search":
		return billing.ScopeHostedExa
	case "computer":
		return billing.ScopeHostedSprites
	default:
		return billing.ScopeHostedLLM
	}
}

// ReserveUsageWithBilling faithfully mirrors Postgres's single-transaction FOR UPDATE
// entitlement reservation, credit ledger check, fleet daily budget check, BYOK rail,
// and LLM-only hourly call limit inside b.mu for deterministic simulation testing.
func (b *Backend) ReserveUsageWithBilling(ctx context.Context, req docstore.UsageReserveRequest) (docstore.UsageReserveResult, error) {
	if err := ctx.Err(); err != nil {
		return docstore.UsageReserveResult{}, err
	}
	now := req.Now.UTC()
	if now.IsZero() {
		now = b.now()
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	ent, err := b.getEntitlementLocked(req.UserID, now)
	if errors.Is(err, billing.ErrEntitlementMissing) {
		if !req.EnforceEntitlement {
			return docstore.UsageReserveResult{UsedEntitlement: false}, nil
		}
		return docstore.UsageReserveResult{UsedEntitlement: true}, billing.ErrEntitlementMissing
	}
	if err != nil {
		return docstore.UsageReserveResult{}, err
	}

	if err := ent.CheckAccess(now); err != nil {
		return docstore.UsageReserveResult{UsedEntitlement: true}, err
	}

	if colMap, ok := b.docs[req.Collection]; ok {
		if _, exists := colMap[req.UsageID]; exists {
			return docstore.UsageReserveResult{
				Allowed:             true,
				UsedEntitlement:     true,
				ReservedExposureUSD: req.EstimatedExposureUSD,
				EffectiveBilling:    memEffectiveBilling(req.Billing, req.CostStatus),
				EffectiveCategory:   memEffectiveCategory(req.Category),
			}, nil
		}
	}

	effBilling := memEffectiveBilling(req.Billing, req.CostStatus)
	effCategory := memEffectiveCategory(req.Category)
	policy := ent.EffectivePolicy()

	incomingExposure := req.EstimatedExposureUSD
	if effBilling == "local" || req.CostStatus == "local" {
		incomingExposure = 0
	} else if effBilling == "system" {
		if incomingExposure < 0 {
			incomingExposure = 0
		}
	} else if incomingExposure <= 0 {
		incomingExposure = req.UnknownEstimateUSD
	}

	// Rolling 1h LLM-only call cap (excludes search, computer, and system).
	if effCategory == "llm" && effBilling != "system" && policy.MaxCallsPerHour > 0 {
		calls := 0
		for _, raw := range b.docs[req.Collection] {
			var u struct {
				CreatedAt     time.Time `json:"created_at"`
				Category      string    `json:"category"`
				RequestStatus string    `json:"request_status"`
			}
			if json.Unmarshal(raw, &u) != nil {
				continue
			}
			if u.RequestStatus == "not_sent" {
				continue
			}
			if u.Category != "" && u.Category != "llm" {
				continue
			}
			if !u.CreatedAt.UTC().Before(req.HourCutoff.UTC()) {
				calls++
			}
		}
		if calls+1 > policy.MaxCallsPerHour {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, nil
		}
	}

	switch effBilling {
	case "hosted":
		bal := billing.ComputeCreditBalance(ent, b.userGrantsLocked(req.UserID), b.usageSamplesLocked(req.DataOwner), now)
		incomingMicros := billing.USDToMicrosCeil(incomingExposure)
		if bal.AvailableMicros <= 0 || bal.AvailableMicros < incomingMicros {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, billing.ErrCreditExhausted
		}
		// Check both scopes before mutating either so rejection leaves global budget untouched.
		dayStr := now.Format("2006-01-02")
		allKey := billingBudgetKey{day: dayStr, scope: billing.ScopeHostedAll}
		if req.HostedAllMaxMicrosPerDay > 0 && b.billing.globalBudget[allKey].SpentMicros+incomingMicros > req.HostedAllMaxMicrosPerDay {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, billing.ErrGlobalBudgetExceeded
		}
		catScope := memBudgetScopeForCategory(effCategory)
		catKey := billingBudgetKey{day: dayStr, scope: catScope}
		if catScope == billing.ScopeHostedExa && req.HostedExaMaxMicrosPerDay > 0 && b.billing.globalBudget[catKey].SpentMicros+incomingMicros > req.HostedExaMaxMicrosPerDay {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, billing.ErrGlobalBudgetExceeded
		}
		_ = b.checkAndIncrementGlobalBudgetLocked(now, billing.ScopeHostedAll, incomingMicros, req.HostedAllMaxMicrosPerDay, 1, 0)
		var catCap int64
		if catScope == billing.ScopeHostedExa {
			catCap = req.HostedExaMaxMicrosPerDay
		}
		_ = b.checkAndIncrementGlobalBudgetLocked(now, catScope, incomingMicros, catCap, 1, 0)

	case "byok":
		if !policy.AllowBYOK {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, billing.ErrBYOKNotAllowed
		}
		if policy.MaxBYOKMicrosPerDay > 0 {
			fallbackUSD := req.UnknownEstimateUSD
			if fallbackUSD <= 0 {
				fallbackUSD = billing.DefaultUnknownExposureUSD
			}
			var usedMicros int64
			for _, raw := range b.docs[req.Collection] {
				var u struct {
					CreatedAt            time.Time `json:"created_at"`
					Billing              string    `json:"billing"`
					CostUSD              *float64  `json:"cost_usd"`
					EstimatedExposureUSD float64   `json:"estimated_exposure_usd"`
					CostStatus           string    `json:"cost_status"`
					RequestStatus        string    `json:"request_status"`
				}
				if json.Unmarshal(raw, &u) != nil {
					continue
				}
				if u.RequestStatus == "not_sent" || u.Billing != "byok" {
					continue
				}
				if u.CreatedAt.UTC().Before(req.DayCutoff.UTC()) {
					continue
				}
				switch {
				case u.CostUSD != nil && *u.CostUSD > 0:
					usedMicros += billing.USDToMicros(*u.CostUSD)
				case u.RequestStatus == "pending" || u.RequestStatus == "stale_aborted":
					exp := u.EstimatedExposureUSD
					if exp <= 0 {
						exp = fallbackUSD
					}
					usedMicros += billing.USDToMicrosCeil(exp)
				case u.CostStatus != "provider" && u.CostStatus != "calculated" && u.CostStatus != "local":
					exp := u.EstimatedExposureUSD
					if exp <= 0 {
						exp = fallbackUSD
					}
					usedMicros += billing.USDToMicrosCeil(exp)
				}
			}
			incomingMicros := billing.USDToMicrosCeil(incomingExposure)
			if usedMicros+incomingMicros > policy.MaxBYOKMicrosPerDay {
				return docstore.UsageReserveResult{
					Allowed:           false,
					UsedEntitlement:   true,
					EffectiveBilling:  effBilling,
					EffectiveCategory: effCategory,
				}, nil
			}
		}

	case "local":
		if !policy.AllowHomeAI {
			return docstore.UsageReserveResult{
				Allowed:           false,
				UsedEntitlement:   true,
				EffectiveBilling:  effBilling,
				EffectiveCategory: effCategory,
			}, billing.ErrBYOKNotAllowed
		}

	case "system":
		// Platform internal infrastructure calls (e.g. Jev classifier): suspension/status is already
		// checked above, but system calls never drain user hosted credits, never block BYOK/trial
		// users, and never count against user hourly LLM or BYOK 24h caps. Track global hosted_all
		// only when explicitly priced (incomingExposure > 0).
		if incomingExposure > 0 {
			incomingMicros := billing.USDToMicrosCeil(incomingExposure)
			dayStr := now.Format("2006-01-02")
			allKey := billingBudgetKey{day: dayStr, scope: billing.ScopeHostedAll}
			if req.HostedAllMaxMicrosPerDay > 0 && b.billing.globalBudget[allKey].SpentMicros+incomingMicros > req.HostedAllMaxMicrosPerDay {
				return docstore.UsageReserveResult{
					Allowed:           false,
					UsedEntitlement:   true,
					EffectiveBilling:  effBilling,
					EffectiveCategory: effCategory,
				}, billing.ErrGlobalBudgetExceeded
			}
			_ = b.checkAndIncrementGlobalBudgetLocked(now, billing.ScopeHostedAll, incomingMicros, req.HostedAllMaxMicrosPerDay, 1, 0)
		}
	}

	fields := make(map[string]any, len(req.UsageFields)+3)
	for k, v := range req.UsageFields {
		fields[k] = v
	}
	fields["billing"] = effBilling
	fields["category"] = effCategory
	fields["estimated_exposure_usd"] = incomingExposure
	raw, err := encodeDoc(fields)
	if err != nil {
		return docstore.UsageReserveResult{}, err
	}
	colMap, ok := b.docs[req.Collection]
	if !ok {
		colMap = map[string][]byte{}
		b.docs[req.Collection] = colMap
	}
	colMap[req.UsageID] = raw
	b.notifyLocked()

	return docstore.UsageReserveResult{
		Allowed:             true,
		UsedEntitlement:     true,
		ReservedExposureUSD: incomingExposure,
		EffectiveBilling:    effBilling,
		EffectiveCategory:   effCategory,
	}, nil
}

func (b *Backend) RecordAndReconcileUsage(ctx context.Context, req docstore.UsageReconcileRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := encodeDoc(req.UsageFields)
	if err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	var prevRaw []byte
	var hasPrev bool
	if colMap, ok := b.docs[req.Collection]; ok {
		prevRaw, hasPrev = colMap[req.UsageID]
	} else {
		b.docs[req.Collection] = map[string][]byte{}
	}
	b.docs[req.Collection][req.UsageID] = raw
	b.notifyLocked()

	if hasPrev && req.RequestStatus != "pending" {
		var prev struct {
			Billing              string    `json:"billing"`
			Category             string    `json:"category"`
			CostStatus           string    `json:"cost_status"`
			RequestStatus        string    `json:"request_status"`
			EstimatedExposureUSD float64   `json:"estimated_exposure_usd"`
			CreatedAt            time.Time `json:"created_at"`
		}
		if json.Unmarshal(prevRaw, &prev) == nil && prev.RequestStatus == "pending" {
			effBilling := memEffectiveBilling(prev.Billing, prev.CostStatus)
			if effBilling == "hosted" {
				prevMicros := billing.USDToMicrosCeil(prev.EstimatedExposureUSD)
				var newMicros int64
				switch {
				case req.RequestStatus == "not_sent":
					newMicros = 0
				case req.RequestStatus == "stale_aborted":
					newMicros = prevMicros
				case req.CostUSD != nil && *req.CostUSD >= 0:
					newMicros = billing.USDToMicros(*req.CostUSD)
				default:
					newMicros = prevMicros
				}
				if delta := newMicros - prevMicros; delta != 0 {
					day := prev.CreatedAt
					if day.IsZero() {
						day = req.CreatedAt
					}
					if day.IsZero() {
						day = b.now()
					}
					cat := memEffectiveCategory(prev.Category)
					b.reconcileGlobalBudgetLocked(day, billing.ScopeHostedAll, delta)
					b.reconcileGlobalBudgetLocked(day, memBudgetScopeForCategory(cat), delta)
				}
			} else if effBilling == "system" {
				prevMicros := billing.USDToMicrosCeil(prev.EstimatedExposureUSD)
				var newMicros int64
				switch {
				case req.RequestStatus == "not_sent":
					newMicros = 0
				case req.CostUSD != nil && *req.CostUSD >= 0:
					newMicros = billing.USDToMicros(*req.CostUSD)
				default:
					newMicros = prevMicros
				}
				if delta := newMicros - prevMicros; delta != 0 {
					day := prev.CreatedAt
					if day.IsZero() {
						day = req.CreatedAt
					}
					if day.IsZero() {
						day = b.now()
					}
					b.reconcileGlobalBudgetLocked(day, billing.ScopeHostedAll, delta)
				}
			}
		}
	} else if !hasPrev && req.RequestStatus != "pending" && req.RequestStatus != "not_sent" {
		effBilling := memEffectiveBilling(req.Billing, req.CostStatus)
		if effBilling == "hosted" {
			var actualMicros int64
			switch {
			case req.CostUSD != nil && *req.CostUSD > 0:
				actualMicros = billing.USDToMicros(*req.CostUSD)
			case req.CostStatus != "provider" && req.CostStatus != "calculated" && req.CostStatus != "local":
				if req.EstimatedExposureUSD > 0 {
					actualMicros = billing.USDToMicrosCeil(req.EstimatedExposureUSD)
				} else {
					actualMicros = billing.DefaultUnknownExposureMicros
				}
			}
			if actualMicros > 0 {
				day := req.CreatedAt
				if day.IsZero() {
					day = b.now()
				}
				cat := memEffectiveCategory(req.Category)
				_ = b.checkAndIncrementGlobalBudgetLocked(day, billing.ScopeHostedAll, actualMicros, 0, 1, 0)
				_ = b.checkAndIncrementGlobalBudgetLocked(day, memBudgetScopeForCategory(cat), actualMicros, 0, 1, 0)
			}
		} else if effBilling == "system" && req.CostUSD != nil && *req.CostUSD > 0 {
			actualMicros := billing.USDToMicros(*req.CostUSD)
			if actualMicros > 0 {
				day := req.CreatedAt
				if day.IsZero() {
					day = b.now()
				}
				_ = b.checkAndIncrementGlobalBudgetLocked(day, billing.ScopeHostedAll, actualMicros, 0, 1, 0)
			}
		}
	}
	return nil
}
