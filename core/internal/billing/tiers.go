// Package billing defines Nori's subscription tiers, micro-USD credit grant ledger,
// account suspension semantics, fleet-wide daily hosted budgets, and Stripe/Apple IAP ports.
package billing

import (
	"math"
	"strings"
	"time"
)

// MicrosPerUSD is the integer monetary unit (1 USD = 1,000,000 micro-USD) used to avoid float drift.
const MicrosPerUSD int64 = 1_000_000

// StalePendingTTL is the duration after which an unfinalized pending usage reservation
// is treated as stale/aborted spent exposure rather than active pending exposure.
const StalePendingTTL = 15 * time.Minute

// DefaultUnknownExposureUSD / Micros is the conservative fallback exposure charged when
// an unpriced/aborted hosted or BYOK call has estimated_exposure_usd <= 0.
const (
	DefaultUnknownExposureUSD    float64 = 0.02
	DefaultUnknownExposureMicros int64   = 20_000
)

type Tier string

const (
	TierTrial    Tier = "trial"
	TierBYOK     Tier = "byok"
	TierStandard Tier = "standard"
	TierPower    Tier = "power"
)

type Status string

const (
	StatusActive       Status = "active"
	StatusBillingRetry Status = "billing_retry"
	StatusPastDue      Status = "past_due"
	StatusCanceled     Status = "canceled"
	StatusExpired      Status = "expired"
)

type Provider string

const (
	ProviderManual   Provider = "manual"
	ProviderStripe   Provider = "stripe"
	ProviderAppleIAP Provider = "apple_iap"
)

type Environment string

const (
	EnvProduction Environment = "production"
	EnvSandbox    Environment = "sandbox"
)

type GrantKind string

const (
	GrantTrial    GrantKind = "trial"
	GrantPeriod   GrantKind = "period"
	GrantTopup    GrantKind = "topup"
	GrantPromo    GrantKind = "promo"
	GrantClawback GrantKind = "clawback"
)

type BudgetScope string

const (
	ScopeHostedAll     BudgetScope = "hosted_all"
	ScopeHostedLLM     BudgetScope = "hosted_llm"
	ScopeHostedExa     BudgetScope = "hosted_exa"
	ScopeHostedSprites BudgetScope = "hosted_sprites"
	ScopeTrialsCreated BudgetScope = "trials_created"
)

// Policy encodes the exact economic and operational caps for a billing tier.
type Policy struct {
	Tier                 Tier          `json:"tier"`
	HostedCreditMicros   int64         `json:"hosted_credit_micros"`
	HostedCreditOneTime  bool          `json:"hosted_credit_one_time"`
	AllowBYOK            bool          `json:"allow_byok"`
	AllowHomeAI          bool          `json:"allow_home_ai"`
	MaxCallsPerHour      int           `json:"max_calls_per_hour"`
	MaxBYOKMicrosPerDay  int64         `json:"max_byok_micros_per_day"`
	MaxStorageBytes      int64         `json:"max_storage_bytes"`
	MaxRoutines          int           `json:"max_routines"`
	MinRoutineInterval   time.Duration `json:"min_routine_interval"`
	MaxConcurrentTasks   int           `json:"max_concurrent_tasks"`
	MaxDesktopWS         int           `json:"max_desktop_ws"`
	MaxSpriteCheckpoints int           `json:"max_sprite_checkpoints"`
}

// MaxBYOKUSDPerDay returns the 24h BYOK safety rail in USD.
func (p Policy) MaxBYOKUSDPerDay() float64 {
	return MicrosToUSD(p.MaxBYOKMicrosPerDay)
}

var tierPolicies = map[Tier]Policy{
	TierTrial: {
		Tier:                 TierTrial,
		HostedCreditMicros:   5 * MicrosPerUSD,
		HostedCreditOneTime:  true,
		AllowBYOK:            false,
		AllowHomeAI:          false,
		MaxCallsPerHour:      120,
		MaxBYOKMicrosPerDay:  0,
		MaxStorageBytes:      100 << 20, // 100 MiB
		MaxRoutines:          3,
		MinRoutineInterval:   60 * time.Minute,
		MaxConcurrentTasks:   2,
		MaxDesktopWS:         1,
		MaxSpriteCheckpoints: 1,
	},
	TierBYOK: {
		Tier:                 TierBYOK,
		HostedCreditMicros:   0,
		HostedCreditOneTime:  false,
		AllowBYOK:            true,
		AllowHomeAI:          true,
		MaxCallsPerHour:      300,
		MaxBYOKMicrosPerDay:  15 * MicrosPerUSD,
		MaxStorageBytes:      250 << 20, // 250 MiB
		MaxRoutines:          10,
		MinRoutineInterval:   15 * time.Minute,
		MaxConcurrentTasks:   2,
		MaxDesktopWS:         1,
		MaxSpriteCheckpoints: 3,
	},
	TierStandard: {
		Tier:                 TierStandard,
		HostedCreditMicros:   10 * MicrosPerUSD,
		HostedCreditOneTime:  false,
		AllowBYOK:            true,
		AllowHomeAI:          true,
		MaxCallsPerHour:      300,
		MaxBYOKMicrosPerDay:  15 * MicrosPerUSD,
		MaxStorageBytes:      500 << 20, // 500 MiB
		MaxRoutines:          15,
		MinRoutineInterval:   15 * time.Minute,
		MaxConcurrentTasks:   4,
		MaxDesktopWS:         1,
		MaxSpriteCheckpoints: 3,
	},
	TierPower: {
		Tier:                 TierPower,
		HostedCreditMicros:   25 * MicrosPerUSD,
		HostedCreditOneTime:  false,
		AllowBYOK:            true,
		AllowHomeAI:          true,
		MaxCallsPerHour:      600,
		MaxBYOKMicrosPerDay:  30 * MicrosPerUSD,
		MaxStorageBytes:      2 << 30, // 2 GiB
		MaxRoutines:          30,
		MinRoutineInterval:   5 * time.Minute,
		MaxConcurrentTasks:   6,
		MaxDesktopWS:         2,
		MaxSpriteCheckpoints: 10,
	},
}

// ParseTier normalizes a tier string and validates it.
func ParseTier(raw string) (Tier, bool) {
	t := Tier(strings.ToLower(strings.TrimSpace(raw)))
	return t, ValidTier(t)
}

func ValidTier(t Tier) bool {
	_, ok := tierPolicies[t]
	return ok
}

func ValidPaidTier(t Tier) bool {
	return t == TierBYOK || t == TierStandard || t == TierPower
}

func ValidStatus(s Status) bool {
	switch s {
	case StatusActive, StatusBillingRetry, StatusPastDue, StatusCanceled, StatusExpired:
		return true
	default:
		return false
	}
}

func ValidProvider(p Provider) bool {
	switch p {
	case ProviderManual, ProviderStripe, ProviderAppleIAP:
		return true
	default:
		return false
	}
}

func ValidEnvironment(e Environment) bool {
	return e == EnvProduction || e == EnvSandbox
}

func ValidGrantKind(k GrantKind) bool {
	switch k {
	case GrantTrial, GrantPeriod, GrantTopup, GrantPromo, GrantClawback:
		return true
	default:
		return false
	}
}

func ValidBudgetScope(s BudgetScope) bool {
	switch s {
	case ScopeHostedAll, ScopeHostedLLM, ScopeHostedExa, ScopeHostedSprites, ScopeTrialsCreated:
		return true
	default:
		return false
	}
}

// PolicyFor returns the Policy for tier, or ok=false if unknown.
func PolicyFor(tier Tier) (Policy, bool) {
	p, ok := tierPolicies[tier]
	return p, ok
}

// MustPolicy returns the Policy for tier, panicking if unknown.
func MustPolicy(tier Tier) Policy {
	p, ok := tierPolicies[tier]
	if !ok {
		panic("billing: unknown tier " + string(tier))
	}
	return p
}

// TierRank orders tiers for multi-subscription precedence (power > standard > byok > trial).
func TierRank(t Tier) int {
	switch t {
	case TierPower:
		return 3
	case TierStandard:
		return 2
	case TierBYOK:
		return 1
	case TierTrial:
		return 0
	default:
		return -1
	}
}

// USDToMicros converts a USD dollar amount to rounded integer micro-USD.
// NaN, Inf, and negative values clamp to 0.
func USDToMicros(usd float64) int64 {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd <= 0 {
		return 0
	}
	return int64(math.Round(usd * float64(MicrosPerUSD)))
}

// USDToMicrosCeil conservatively rounds a positive USD amount up to integer micro-USD
// so sub-micro exposure never rounds down to zero.
func USDToMicrosCeil(usd float64) int64 {
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd <= 0 {
		return 0
	}
	return int64(math.Ceil(usd * float64(MicrosPerUSD)))
}

// MicrosToUSD converts integer micro-USD to USD float64.
func MicrosToUSD(micros int64) float64 {
	return float64(micros) / float64(MicrosPerUSD)
}
