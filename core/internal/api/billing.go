package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"nori/internal/billing"
	"nori/internal/modelprofiles"
	"nori/internal/scheduler"
	"nori/internal/state"
	"nori/internal/state/records"
)

var (
	ErrRoutineLimitExceeded        = errors.New("routine limit reached for your current plan")
	ErrRoutineIntervalTooShort     = errors.New("routine interval is shorter than your current plan allows")
	ErrConcurrentTaskLimitExceeded = errors.New("concurrent task limit reached for your current plan")
)

const (
	codeStorageQuotaExceeded = "storage_quota_exceeded"
	codeRoutineLimitExceeded = "routine_limit_exceeded"
	codeRoutineIntervalShort = "routine_interval_too_short"
	codeConcurrentTaskLimit  = "concurrent_task_limit_exceeded"
)

func billingErrorDetails(err error) (status int, code, message string, ok bool) {
	if errors.Is(err, records.ErrStorageQuotaExceeded) {
		return http.StatusRequestEntityTooLarge, codeStorageQuotaExceeded,
			"Your upload storage limit has been reached. Upgrade your plan to upload more files.", true
	}
	if errors.Is(err, ErrRoutineLimitExceeded) {
		return http.StatusForbidden, codeRoutineLimitExceeded, err.Error(), true
	}
	if errors.Is(err, ErrRoutineIntervalTooShort) {
		return http.StatusForbidden, codeRoutineIntervalShort, err.Error(), true
	}
	if errors.Is(err, ErrConcurrentTaskLimitExceeded) {
		return http.StatusForbidden, codeConcurrentTaskLimit, err.Error(), true
	}
	status = billing.HTTPStatus(err)
	if status == 0 {
		return 0, "", "", false
	}
	code = billing.ErrorCode(err)
	switch {
	case errors.Is(err, billing.ErrAccountSuspended):
		message = "Your account has been suspended."
	case errors.Is(err, billing.ErrEntitlementMissing):
		message = "Account access is not configured."
	case errors.Is(err, billing.ErrCreditExhausted):
		message = "Your hosted credit balance is exhausted. Add your own API key or upgrade your plan to continue."
	case errors.Is(err, billing.ErrSubscriptionInactive):
		message = "Your subscription is inactive. Resubscribe or update billing to continue."
	case errors.Is(err, billing.ErrBYOKNotAllowed):
		message = "Bring-your-own API keys require a paid plan."
	case errors.Is(err, billing.ErrGlobalBudgetExceeded):
		message = "Nori's hosted service is temporarily at daily capacity. Try again tomorrow or use your own API key."
	case errors.Is(err, billing.ErrTrialLimitExceeded):
		message = "Daily sign-up limit reached. Try again tomorrow."
	default:
		message = err.Error()
	}
	return status, code, message, true
}

func (a *Assistant) nowUTC() time.Time {
	if a != nil && a.UsageNow != nil {
		return a.UsageNow().UTC()
	}
	return time.Now().UTC()
}

func (a *Assistant) currentEntitlement(ctx context.Context) (billing.Entitlement, bool, error) {
	if a == nil {
		return billing.Entitlement{}, false, nil
	}
	if a.GetEntitlement != nil {
		ent, err := a.GetEntitlement(ctx)
		if err != nil {
			return billing.Entitlement{}, true, err
		}
		return ent, true, nil
	}
	if a.Billing != nil && a.UserID != "" {
		ent, err := a.Billing.GetEntitlement(ctx, a.UserID)
		if err != nil {
			return billing.Entitlement{}, true, err
		}
		return ent, true, nil
	}
	return billing.Entitlement{}, false, nil
}

// checkActiveAccess enforces suspension (403) and active billing status (402 subscription_inactive)
// when billing is attached to Assistant.
func (a *Assistant) checkActiveAccess(ctx context.Context) (billing.Entitlement, bool, error) {
	ent, configured, err := a.currentEntitlement(ctx)
	if !configured {
		return billing.Entitlement{}, false, nil
	}
	if err != nil {
		return billing.Entitlement{}, true, err
	}
	if err := ent.CheckAccess(a.nowUTC()); err != nil {
		return ent, true, err
	}
	return ent, true, nil
}

// checkBYOKAllowed enforces active access and policy.AllowBYOK (rejecting trial users with 402 byok_not_allowed).
func (a *Assistant) checkBYOKAllowed(ctx context.Context) error {
	ent, configured, err := a.checkActiveAccess(ctx)
	if !configured || err != nil {
		return err
	}
	if !ent.EffectivePolicy().AllowBYOK {
		return billing.ErrBYOKNotAllowed
	}
	return nil
}

// isEffectiveChatHosted reports whether chat messages submitted for this user will run on
// a platform-hosted model (as opposed to BYOK or Home AI).
func (a *Assistant) isEffectiveChatHosted(ctx context.Context, sel *state.ModelSelection, defaultProvider string) bool {
	if a == nil {
		return false
	}
	if a.Models != nil {
		effective := sel
		if effective.IsEmpty() || effective.ChatProfileVersion != state.ModelSettingsVersion {
			cur, err := a.Models.CurrentSelection(ctx)
			if err != nil {
				return false
			}
			effective = cur
		}
		if effective != nil && effective.ChatPinned != nil {
			return effective.ChatPinned.Backend == modelprofiles.BackendHosted
		}
		return a.Models.HostedEnabled && !a.AccountBYOK
	}
	if a.AccountBYOK {
		return false
	}
	return defaultProvider != "home" && defaultProvider != "local"
}

// checkChatHostedCredit rejects chat submissions immediately with 402 credit_exhausted when
// the user's effective chat model is hosted and their hosted credit balance is <= 0.
func (a *Assistant) checkChatHostedCredit(ctx context.Context, sel *state.ModelSelection, defaultProvider string) error {
	if a == nil || a.Billing == nil || a.UserID == "" {
		return nil
	}
	if !a.isEffectiveChatHosted(ctx, sel, defaultProvider) {
		return nil
	}
	sum, err := a.Billing.Summary(ctx, a.UserID)
	if err != nil {
		return err
	}
	if sum.Balance.AvailableMicros <= 0 {
		return billing.ErrCreditExhausted
	}
	return nil
}

func (a *Assistant) checkRoutineCap(ctx context.Context, excludeID string) error {
	ent, configured, err := a.checkActiveAccess(ctx)
	if !configured || err != nil {
		return err
	}
	if a.Scheduler == nil {
		return nil
	}
	policy := ent.EffectivePolicy()
	if policy.MaxRoutines <= 0 {
		return nil
	}
	items, err := a.Scheduler.List(ctx, string(scheduler.AutomationEnabled))
	if err != nil {
		return err
	}
	count := 0
	for _, item := range items {
		if item.System || (excludeID != "" && item.ID == excludeID) {
			continue
		}
		count++
	}
	if count >= policy.MaxRoutines {
		return fmt.Errorf("%w (max %d on %s plan)", ErrRoutineLimitExceeded, policy.MaxRoutines, policy.Tier)
	}
	return nil
}

func (a *Assistant) checkRoutineInterval(ctx context.Context, trig scheduler.Trigger) error {
	ent, configured, err := a.checkActiveAccess(ctx)
	if !configured || err != nil {
		return err
	}
	policy := ent.EffectivePolicy()
	minInterval := policy.MinRoutineInterval
	if minInterval <= 0 {
		return nil
	}
	minSeconds := int64(minInterval / time.Second)
	if trig.MinIntervalSeconds > 0 && trig.MinIntervalSeconds < minSeconds {
		return fmt.Errorf("%w (minimum %s on %s plan)", ErrRoutineIntervalTooShort, minInterval, policy.Tier)
	}
	if trig.Kind == "recurring" && trig.RRule != "" {
		if rule, parseErr := scheduler.ParseRule(trig.RRule); parseErr == nil {
			if gap := shortestRuleInterval(rule); gap > 0 && gap < minInterval {
				return fmt.Errorf("%w (minimum %s on %s plan)", ErrRoutineIntervalTooShort, minInterval, policy.Tier)
			}
		}
	}
	return nil
}

func shortestRuleInterval(rule scheduler.Rule) time.Duration {
	if len(rule.ByMinute) > 1 {
		mins := append([]int(nil), rule.ByMinute...)
		sort.Ints(mins)
		best := 60
		for i := 1; i < len(mins); i++ {
			if d := mins[i] - mins[i-1]; d > 0 && d < best {
				best = d
			}
		}
		if wrap := 60 - mins[len(mins)-1] + mins[0]; wrap > 0 && wrap < best {
			best = wrap
		}
		return time.Duration(best) * time.Minute
	}
	if len(rule.ByHour) > 1 {
		hours := append([]int(nil), rule.ByHour...)
		sort.Ints(hours)
		best := 24
		for i := 1; i < len(hours); i++ {
			if d := hours[i] - hours[i-1]; d > 0 && d < best {
				best = d
			}
		}
		if wrap := 24 - hours[len(hours)-1] + hours[0]; wrap > 0 && wrap < best {
			best = wrap
		}
		return time.Duration(best) * time.Hour
	}
	return 0
}

func (a *Assistant) checkConcurrentTaskCap(ctx context.Context) error {
	ent, configured, err := a.checkActiveAccess(ctx)
	if !configured || err != nil {
		return err
	}
	if a.Store == nil {
		return nil
	}
	policy := ent.EffectivePolicy()
	if policy.MaxConcurrentTasks <= 0 {
		return nil
	}
	open, err := a.Store.OpenTasks(ctx)
	if err != nil {
		return err
	}
	active := 0
	for _, run := range open {
		if run.Status == state.RunRunning {
			active++
		}
	}
	if active >= policy.MaxConcurrentTasks {
		return fmt.Errorf("%w (max %d on %s plan)", ErrConcurrentTaskLimitExceeded, policy.MaxConcurrentTasks, policy.Tier)
	}
	return nil
}

type entitlementCostSummary struct {
	Tier               string     `json:"tier"`
	PendingTier        *string    `json:"pending_tier,omitempty"`
	Status             string     `json:"status"`
	BillingProvider    string     `json:"billing_provider"`
	PeriodStart        time.Time  `json:"period_start"`
	PeriodEnd          *time.Time `json:"period_end,omitempty"`
	GraceUntil         *time.Time `json:"grace_until,omitempty"`
	Suspended          bool       `json:"suspended"`
	SuspendedReason    string     `json:"suspended_reason,omitempty"`
	AllowBYOK          bool       `json:"allow_byok"`
	CreditGrantUSD     float64    `json:"credit_grant_usd"`
	CreditUsedUSD      float64    `json:"credit_used_usd"`
	CreditPendingUSD   float64    `json:"credit_pending_usd"`
	CreditRemainingUSD float64    `json:"credit_remaining_usd"`
	BYOKMaxUSDPerDay   float64    `json:"byok_max_usd_per_day"`
	StorageUsedBytes   int64      `json:"storage_used_bytes"`
	StorageLimitBytes  int64      `json:"storage_limit_bytes"`
}

func (a *Assistant) buildEntitlementCostSummary(ctx context.Context) (*entitlementCostSummary, error) {
	if a == nil || a.Billing == nil || a.UserID == "" {
		return nil, nil
	}
	sum, err := a.Billing.Summary(ctx, a.UserID)
	if err != nil {
		return nil, err
	}
	var storageUsed int64
	if a.GetStorageUsed != nil {
		if used, sErr := a.GetStorageUsed(ctx); sErr == nil {
			storageUsed = used
		}
	}
	var pendingTier *string
	if sum.Entitlement.PendingTier != nil {
		v := string(*sum.Entitlement.PendingTier)
		pendingTier = &v
	}
	remaining := sum.Balance.AvailableMicros
	if remaining < 0 {
		remaining = 0
	}
	return &entitlementCostSummary{
		Tier:               string(sum.Entitlement.Tier),
		PendingTier:        pendingTier,
		Status:             string(sum.Entitlement.Status),
		BillingProvider:    string(sum.Entitlement.BillingProvider),
		PeriodStart:        sum.Entitlement.PeriodStart,
		PeriodEnd:          sum.Entitlement.PeriodEnd,
		GraceUntil:         sum.Entitlement.GraceUntil,
		Suspended:          sum.Entitlement.IsSuspended(),
		SuspendedReason:    sum.Entitlement.SuspendedReason,
		AllowBYOK:          sum.Policy.AllowBYOK,
		CreditGrantUSD:     billing.MicrosToUSD(sum.Balance.GrantedMicros),
		CreditUsedUSD:      billing.MicrosToUSD(sum.Balance.SpentMicros),
		CreditPendingUSD:   billing.MicrosToUSD(sum.Balance.PendingMicros),
		CreditRemainingUSD: billing.MicrosToUSD(remaining),
		BYOKMaxUSDPerDay:   sum.Policy.MaxBYOKUSDPerDay(),
		StorageUsedBytes:   storageUsed,
		StorageLimitBytes:  sum.Policy.MaxStorageBytes,
	}, nil
}

type capabilitiesEntitlement struct {
	Tier               string  `json:"tier"`
	Status             string  `json:"status"`
	AllowBYOK          bool    `json:"allow_byok"`
	Suspended          bool    `json:"suspended"`
	CreditRemainingUSD float64 `json:"credit_remaining_usd"`
	CreditGrantUSD     float64 `json:"credit_grant_usd"`
	StorageUsedBytes   int64   `json:"storage_used_bytes"`
	StorageLimitBytes  int64   `json:"storage_limit_bytes"`
	MaxRoutines        int     `json:"max_routines"`
	MaxConcurrentTasks int     `json:"max_concurrent_tasks"`
	MaxDesktopWS       int     `json:"max_desktop_ws"`
	MaxCheckpoints     int     `json:"max_checkpoints"`
}

type capabilitiesServiceKeys struct {
	ExaConfigured        bool `json:"exa_configured"`
	PlatformExaAvailable bool `json:"platform_exa_available"`
}

func (a *Assistant) buildCapabilitiesEntitlement(ctx context.Context) *capabilitiesEntitlement {
	if a == nil || a.Billing == nil || a.UserID == "" {
		return nil
	}
	sum, err := a.Billing.Summary(ctx, a.UserID)
	if err != nil {
		return nil
	}
	var storageUsed int64
	if a.GetStorageUsed != nil {
		if used, sErr := a.GetStorageUsed(ctx); sErr == nil {
			storageUsed = used
		}
	}
	remaining := sum.Balance.AvailableMicros
	if remaining < 0 {
		remaining = 0
	}
	return &capabilitiesEntitlement{
		Tier:               string(sum.Entitlement.Tier),
		Status:             string(sum.Entitlement.Status),
		AllowBYOK:          sum.Policy.AllowBYOK,
		Suspended:          sum.Entitlement.IsSuspended(),
		CreditRemainingUSD: billing.MicrosToUSD(remaining),
		CreditGrantUSD:     billing.MicrosToUSD(sum.Balance.GrantedMicros),
		StorageUsedBytes:   storageUsed,
		StorageLimitBytes:  sum.Policy.MaxStorageBytes,
		MaxRoutines:        sum.Policy.MaxRoutines,
		MaxConcurrentTasks: sum.Policy.MaxConcurrentTasks,
		MaxDesktopWS:       sum.Policy.MaxDesktopWS,
		MaxCheckpoints:     sum.Policy.MaxSpriteCheckpoints,
	}
}

func (a *Assistant) buildCapabilitiesServiceKeys(ctx context.Context) *capabilitiesServiceKeys {
	if a == nil || (a.ServiceKeys == nil && !a.PlatformExaAvailable) {
		return nil
	}
	exaConfigured := false
	if a.ServiceKeys != nil {
		if st, err := a.ServiceKeys.Status(ctx, "exa"); err == nil && st.Configured {
			exaConfigured = true
		}
	}
	return &capabilitiesServiceKeys{
		ExaConfigured:        exaConfigured,
		PlatformExaAvailable: a.PlatformExaAvailable,
	}
}
