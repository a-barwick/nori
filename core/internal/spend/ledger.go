package spend

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

const (
	FramingBytes       = 4096
	dayLimit     int64 = 2_000_000_000
	monthLimit   int64 = 20_000_000_000
)

// Ledger holds the shared development budget: one allowance record per chat scope, one pool shared by every scope,
// and one reservation per provider request. A backend implements Reserve and Settle as single atomic transactions
// and calls Admit, CheckSettlement and ApplySettlement inside them, so the budget rules stay the same on any backend.
// Collection names and field names are the saved layout and must not change.
//
// Backends return plain errors. Rule refusals are *ControlError, which callers pass through; any other error is
// reported as a generic budget failure. A record that cannot be decoded wraps ErrInvalidRecord.
type Ledger interface {
	// Scope returns the allowance record for a chat scope.
	Scope(ctx context.Context, id string) (ScopeRecord, error)
	// Pool returns the shared budget record.
	Pool(ctx context.Context) (PoolRecord, error)
	// Reserve atomically reads the scope and pool, applies Admit, and when it approves creates the reservation and
	// writes the updated scope and pool.
	Reserve(ctx context.Context, req ReserveRequest) error
	// Settle atomically replaces a reservation with the actual cost. It reports whether the cost overran the
	// allowance or the shared budget, after blocking further development spend.
	Settle(ctx context.Context, req SettleRequest) (overrun bool, err error)
	Close() error
}

// ErrInvalidRecord marks a ledger record that exists but cannot be decoded.
var ErrInvalidRecord = errors.New("spend record is invalid")

// LedgerConfig names the shared budget database a process uses.
type LedgerConfig struct {
	Project  string
	Database string
}

// LedgerOpener connects to a ledger. A backend registers its opener once at process start with RegisterLedger.
type LedgerOpener func(ctx context.Context, cfg LedgerConfig) (Ledger, error)

var openLedger LedgerOpener

// RegisterLedger selects the ledger backend for this process. internal/platform registers it, so a process that
// imports platform has the backend and one that does not fails closed when a request needs the budget.
func RegisterLedger(open LedgerOpener) { openLedger = open }

type ScopeRecord struct {
	Profile   string `firestore:"profile"`
	Allowance int64  `firestore:"allowance_nanos"`
	// Only an explicit operator approval raises this chat above the default.
	ApprovedAllowance int64     `firestore:"approved_allowance_nanos,omitempty"`
	Used              int64     `firestore:"used_nanos"`
	Closed            bool      `firestore:"closed"`
	ExpiresAt         time.Time `firestore:"expires_at"`
	CreatedAt         time.Time `firestore:"created_at"`
}

type PoolRecord struct {
	Day        string `firestore:"day"`
	Month      string `firestore:"month"`
	DayKnown   int64  `firestore:"day_known_nanos"`
	MonthKnown int64  `firestore:"month_known_nanos"`
	Pending    int64  `firestore:"pending_nanos"`
	Blocked    bool   `firestore:"blocked"`
}

type ReservationRecord struct {
	Scope    string `firestore:"scope"`
	Provider string `firestore:"provider"`
	Amount   int64  `firestore:"amount_nanos"`
	Settled  bool   `firestore:"settled"`
}

// ValidateScope checks that a scope belongs to the profile, is open, unexpired and within the profile's allowance.
func ValidateScope(profile string, s ScopeRecord, now time.Time) error {
	max := int64(1_000_000_000)
	if profile == "ci" {
		max = 100_000_000
	} else if s.ApprovedAllowance != 0 {
		if s.ApprovedAllowance < max || s.ApprovedAllowance > dayLimit {
			return Blocked("approved chat allowance is invalid")
		}
		max = s.ApprovedAllowance
	}
	if s.Profile != profile || s.Allowance <= 0 || s.Allowance > max || s.Used < 0 || s.Closed || !now.Before(s.ExpiresAt) {
		return Blocked("development allowance is invalid, closed, expired, or has a different profile")
	}
	return nil
}

func ValidatePool(p PoolRecord) error {
	if p.Blocked || p.DayKnown < 0 || p.MonthKnown < 0 || p.Pending < 0 {
		return Blocked("shared development budget is blocked or invalid")
	}
	return nil
}

// Roll starts a new UTC day or month in the pool's known-cost counters.
func Roll(p *PoolRecord, now time.Time) {
	day, month := now.UTC().Format("2006-01-02"), now.UTC().Format("2006-01")
	if p.Day != day {
		p.Day = day
		p.DayKnown = 0
	}
	if p.Month != month {
		p.Month = month
		p.MonthKnown = 0
	}
}

// OutputCap is the most output tokens one request may reserve for the provider under the profile. Zero means the
// allowance does not cover the provider.
func OutputCap(profile, provider string) int {
	switch provider {
	case "openrouter", "openai":
		if profile == "ci" {
			return 512
		}
		return 4096
	case "vertex":
		if profile == "ci" {
			return 1024
		}
		return 8192
	}
	return 0
}

// ReserveRequest is one provider request to reserve, already priced at its worst case.
type ReserveRequest struct {
	ID              string
	Scope           string
	Profile         string
	Provider        string
	InputBytes      int
	MaxOutputTokens int
	// Amount is the reserved cost in nano-USD: every input byte priced as a token, framing, and the full output.
	Amount int64
}

// NewReserveRequest prices a request. Reserve charges one byte as one potential input token, including complete JSON
// history, tool schemas and framing. Conservative fixed prices never come from the environment.
func NewReserveRequest(profile, scope, provider, model string, inputBytes, maxOutputTokens int, requestID string) (ReserveRequest, error) {
	inRate, outRate := int64(0), int64(0)
	switch {
	case provider == "openrouter" && model == "anthropic/claude-opus-5.5":
		// Matches provider.max_price in the request, including regional pricing.
		inRate, outRate = 4400, 22000
	case provider == "openrouter" && model == "meta/muse-spark-1.3":
		inRate, outRate = 1250, 4250
	case provider == "openai" && model == "gpt-6.1-sol":
		// Stay below the long-context pricing threshold even at one token per byte.
		if inputBytes+FramingBytes > 272_000 {
			return ReserveRequest{}, Blocked("OpenAI request exceeds standard-price context bounds")
		}
		inRate, outRate = 2000, 10000
	case provider == "openrouter" && model == "openai/gpt-6.1-terra":
		inRate, outRate = 1000, 5000
	case provider == "vertex" && model == "gemini-3.8-flash":
		inRate, outRate = 1500, 7500
	default:
		return ReserveRequest{}, Blocked("model is not covered by this development allowance")
	}
	id, err := uuid.Parse(requestID)
	if err != nil || id.String() != requestID || inputBytes < 0 || inputBytes > 1_000_000 || maxOutputTokens <= 0 ||
		maxOutputTokens > OutputCap(profile, provider) {
		return ReserveRequest{}, Blocked("model request exceeds the supported development bounds")
	}
	return ReserveRequest{
		ID: requestID, Scope: scope, Profile: profile, Provider: provider, InputBytes: inputBytes, MaxOutputTokens: maxOutputTokens,
		Amount: (int64(inputBytes)+FramingBytes)*inRate + int64(maxOutputTokens)*outRate,
	}, nil
}

// Admit applies a reservation to the scope and pool records, or refuses it. Subtraction comparisons avoid
// overflowing counters supplied by bad records. It returns the records to write and the reservation to create.
func Admit(req ReserveRequest, scope ScopeRecord, pool PoolRecord, now time.Time) (ScopeRecord, PoolRecord, ReservationRecord, error) {
	reservation := ReservationRecord{Scope: req.Scope, Provider: req.Provider, Amount: req.Amount}
	if err := ValidateScope(req.Profile, scope, now); err != nil {
		return scope, pool, reservation, err
	}
	if err := ValidatePool(pool); err != nil {
		return scope, pool, reservation, err
	}
	Roll(&pool, now)
	amount := req.Amount
	if scope.Used > scope.Allowance-amount || pool.Pending > dayLimit-amount || pool.DayKnown > dayLimit-pool.Pending-amount ||
		pool.Pending > monthLimit-amount || pool.MonthKnown > monthLimit-pool.Pending-amount {
		return scope, pool, reservation, &ControlError{admissionDenied: true, message: fmt.Sprintf(
			"request reserve $%.6f (%d input bytes + %d framing, %d output tokens) exceeds remaining allowance $%.6f or shared budget",
			float64(amount)/1e9, req.InputBytes, FramingBytes, req.MaxOutputTokens, float64(scope.Allowance-scope.Used)/1e9)}
	}
	scope.Used += amount
	pool.Pending += amount
	return scope, pool, reservation, nil
}

// SettleRequest replaces a reservation with the cost actually incurred.
type SettleRequest struct {
	ID       string
	Scope    string
	Profile  string
	Provider string
	// Actual is the incurred cost in nano-USD.
	Actual int64
}

// ActualCost converts a provider's reported cost or usage into nano-USD. ok is false when the provider did not
// report one, so the whole reservation stays held: an unknown call is never free.
func ActualCost(provider string, costUSD *float64, usage *TokenUsage) (nanos int64, ok bool, err error) {
	switch provider {
	case "openrouter", "openai":
		if costUSD == nil {
			return 0, false, nil
		}
		value := math.Ceil(*costUSD * 1e9)
		if math.IsNaN(value) || math.IsInf(value, 0) || *costUSD < 0 || value >= float64(math.MaxInt64) {
			return 0, false, Blocked("invalid provider cost; reservation retained")
		}
		return int64(value), true, nil
	case "vertex":
		if usage == nil {
			return 0, false, nil
		}
		if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.InputTokens > 10_000_000 || usage.OutputTokens > 10_000_000 {
			return 0, false, Blocked("invalid provider usage; reservation retained")
		}
		return usage.InputTokens*1500 + usage.OutputTokens*7500, true, nil
	default:
		return 0, false, Blocked("unsupported reservation provider")
	}
}

// CheckSettlement validates the stored reservation against the settlement. settled is true when it was already
// settled, which needs no further work. A backend checks this before it reads the scope and pool.
func CheckSettlement(req SettleRequest, reservation ReservationRecord) (settled bool, err error) {
	if reservation.Scope != req.Scope || reservation.Provider != req.Provider || reservation.Amount <= 0 {
		return false, Blocked("reservation is invalid; allowance retained")
	}
	return reservation.Settled, nil
}

// ApplySettlement moves a reservation from pending to known cost. Unknown calls remain pending across all UTC
// periods. Settling a known call is permitted after the scope closes, so spend already incurred is still recorded.
// overrun reports cost above the reservation, the allowance or the shared budget; the pool is then blocked.
func ApplySettlement(
	req SettleRequest, reservation ReservationRecord, scope ScopeRecord, pool PoolRecord, now time.Time,
) (ScopeRecord, PoolRecord, bool, error) {
	actual := req.Actual
	if scope.Profile != req.Profile || scope.Used < reservation.Amount || pool.Pending < reservation.Amount ||
		pool.DayKnown < 0 || pool.MonthKnown < 0 {
		return scope, pool, false, Blocked("budget records are invalid; reservation retained")
	}
	Roll(&pool, now)
	if scope.Used-reservation.Amount > math.MaxInt64-actual || pool.DayKnown > math.MaxInt64-actual ||
		pool.MonthKnown > math.MaxInt64-actual {
		return scope, pool, false, Blocked("budget counters are invalid; reservation retained")
	}
	pool.Pending -= reservation.Amount
	scope.Used += actual - reservation.Amount
	pool.DayKnown += actual
	pool.MonthKnown += actual
	overrun := actual > reservation.Amount || scope.Used > scope.Allowance || pool.DayKnown > dayLimit-pool.Pending ||
		pool.MonthKnown > monthLimit-pool.Pending
	pool.Blocked = pool.Blocked || overrun
	return scope, pool, overrun, nil
}
