// Package spend reserves development model allowances atomically in the existing
// Firestore database. Uncontrolled production requests retain their normal path.
package spend

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/google/uuid"
)

const (
	FramingBytes       = 4096
	dayLimit     int64 = 2_000_000_000
	monthLimit   int64 = 20_000_000_000
)

type ControlError struct {
	message         string
	admissionDenied bool
}

func (e *ControlError) Error() string { return "spend control: " + e.message }
func blocked(message string) error    { return &ControlError{message: message} }
func IsControlError(err error) bool   { var e *ControlError; return errors.As(err, &e) }

// IsAdmissionDenied identifies a valid budget refusing a new request. Invalid
// configuration, unavailable storage and settlement errors remain failures.
func IsAdmissionDenied(err error) bool {
	var e *ControlError
	return errors.As(err, &e) && e.admissionDenied
}

type Client struct {
	project, database, scope, profile string
	store                             *firestore.Client
}

type scopeRecord struct {
	Profile   string `firestore:"profile"`
	Allowance int64  `firestore:"allowance_nanos"`
	// Only an explicit operator approval raises this chat above the default.
	ApprovedAllowance int64     `firestore:"approved_allowance_nanos,omitempty"`
	Used              int64     `firestore:"used_nanos"`
	Closed            bool      `firestore:"closed"`
	ExpiresAt         time.Time `firestore:"expires_at"`
	CreatedAt         time.Time `firestore:"created_at"`
}
type poolRecord struct {
	Day        string `firestore:"day"`
	Month      string `firestore:"month"`
	DayKnown   int64  `firestore:"day_known_nanos"`
	MonthKnown int64  `firestore:"month_known_nanos"`
	Pending    int64  `firestore:"pending_nanos"`
	Blocked    bool   `firestore:"blocked"`
}
type reservationRecord struct {
	Scope    string `firestore:"scope"`
	Provider string `firestore:"provider"`
	Amount   int64  `firestore:"amount_nanos"`
	Settled  bool   `firestore:"settled"`
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func FromEnv() (*Client, error) {
	scope, profile := os.Getenv("NORI_SPEND_SCOPE"), os.Getenv("NORI_SPEND_PROFILE")
	if scope == "" && profile == "" {
		if os.Getenv("NORI_ENVIRONMENT") == "sandbox" || os.Getenv("FIRESTORE_DATABASE_ID") == "nori-sandbox" {
			return nil, blocked("sandbox requires NORI_SPEND_SCOPE and NORI_SPEND_PROFILE")
		}
		return nil, nil
	}
	raw, err := hex.DecodeString(scope)
	if err != nil || len(raw) != 32 || scope != strings.ToLower(scope) || (profile != "ci" && profile != "feature") {
		return nil, blocked("NORI_SPEND_SCOPE must be a SHA-256 hex ID and NORI_SPEND_PROFILE must be ci or feature")
	}
	project := os.Getenv("NORI_PROJECT_ID")
	if project == "" {
		return nil, blocked("NORI_PROJECT_ID must be configured")
	}
	return &Client{project: project, database: env("NORI_SPEND_DATABASE", "nori-sandbox"), scope: scope, profile: profile}, nil
}

func (c *Client) Close() {
	if c != nil && c.store != nil {
		c.store.Close()
	}
}
func (c *Client) open(ctx context.Context) error {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") != "" {
		return blocked("shared budget cannot use an emulator; use NORI_STATE_EMULATOR_HOST for app data")
	}
	store, err := firestore.NewClientWithDatabase(ctx, c.project, c.database)
	if err != nil {
		return blocked("budget database is unavailable")
	}
	c.store = store
	return nil
}
func (c *Client) scopeRef() *firestore.DocumentRef {
	return c.store.Collection("development_spend_scopes").Doc(c.scope)
}
func (c *Client) poolRef() *firestore.DocumentRef {
	return c.store.Collection("development_spend_pools").Doc("global")
}

func (c *Client) validateScope(s scopeRecord, now time.Time) error {
	max := int64(1_000_000_000)
	if c.profile == "ci" {
		max = 100_000_000
	} else if s.ApprovedAllowance != 0 {
		if s.ApprovedAllowance < max || s.ApprovedAllowance > dayLimit {
			return blocked("approved chat allowance is invalid")
		}
		max = s.ApprovedAllowance
	}
	if s.Profile != c.profile || s.Allowance <= 0 || s.Allowance > max || s.Used < 0 || s.Closed || !now.Before(s.ExpiresAt) {
		return blocked("development allowance is invalid, closed, expired, or has a different profile")
	}
	return nil
}
func validatePool(p poolRecord) error {
	if p.Blocked || p.DayKnown < 0 || p.MonthKnown < 0 || p.Pending < 0 {
		return blocked("shared development budget is blocked or invalid")
	}
	return nil
}
func roll(p *poolRecord, now time.Time) {
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

func (c *Client) writePool(tx *firestore.Transaction, pool poolRecord) error {
	return tx.Update(c.poolRef(), []firestore.Update{
		{Path: "day", Value: pool.Day}, {Path: "month", Value: pool.Month},
		{Path: "day_known_nanos", Value: pool.DayKnown}, {Path: "month_known_nanos", Value: pool.MonthKnown},
		{Path: "pending_nanos", Value: pool.Pending}, {Path: "blocked", Value: pool.Blocked},
	})
}
func (c *Client) validate(ctx context.Context) error {
	snap, err := c.scopeRef().Get(ctx)
	if err != nil {
		return blocked("development allowance is unavailable")
	}
	var scope scopeRecord
	if snap.DataTo(&scope) != nil {
		return blocked("development allowance is invalid")
	}
	if err = c.validateScope(scope, time.Now()); err != nil {
		return err
	}
	snap, err = c.poolRef().Get(ctx)
	if err != nil {
		return blocked("shared development budget is unavailable")
	}
	var pool poolRecord
	if snap.DataTo(&pool) != nil {
		return blocked("shared development budget is invalid")
	}
	return validatePool(pool)
}

// ValidateConfiguration keeps health and cleanup endpoints available after a
// lease closes. Prepare and Reserve still enforce live budgets before inference.
func ValidateConfiguration() error {
	c, err := FromEnv()
	if c != nil {
		c.Close()
	}
	return err
}

func Validate(ctx context.Context) error {
	c, err := FromEnv()
	if err != nil || c == nil {
		return err
	}
	defer c.Close()
	if err = c.open(ctx); err != nil {
		return err
	}
	return c.validate(ctx)
}

// CIContextStart isolates a synthetic run's context without deleting retained
// conversations. Retrying a scope keeps the same starting point and allowance.
func CIContextStart(ctx context.Context) (time.Time, error) {
	if os.Getenv("NORI_SPEND_PROFILE") != "ci" {
		return time.Time{}, nil
	}
	c, err := FromEnv()
	if err != nil {
		return time.Time{}, err
	}
	defer c.Close()
	if err = c.open(ctx); err != nil {
		return time.Time{}, err
	}
	snapshot, err := c.scopeRef().Get(ctx)
	if err != nil {
		return time.Time{}, blocked("CI context boundary is unavailable")
	}
	var scope scopeRecord
	if snapshot.DataTo(&scope) != nil {
		return time.Time{}, blocked("CI context boundary is invalid")
	}
	if err = c.validateScope(scope, time.Now()); err != nil {
		return time.Time{}, err
	}
	if scope.CreatedAt.IsZero() {
		// Scopes issued before created_at was recorded used a fixed 24h lease.
		scope.CreatedAt = scope.ExpiresAt.Add(-24 * time.Hour)
	}
	return scope.CreatedAt, nil
}

func (c *Client) outputCap(provider string) int {
	switch provider {
	case "openrouter", "openai":
		if c.profile == "ci" {
			return 512
		}
		return 4096
	case "vertex":
		if c.profile == "ci" {
			return 1024
		}
		return 8192
	}
	return 0
}
func Prepare(ctx context.Context, provider string, normalLimit int) (*Client, int, error) {
	c, err := FromEnv()
	if err != nil || c == nil {
		return c, normalLimit, err
	}
	if err = c.open(ctx); err != nil {
		return nil, 0, err
	}
	if err = c.validate(ctx); err != nil {
		c.Close()
		return nil, 0, err
	}
	cap := c.outputCap(provider)
	if cap == 0 {
		c.Close()
		return nil, 0, blocked("provider is not covered by this allowance")
	}
	return c, min(normalLimit, cap), nil
}

type Reservation struct {
	client       *Client
	id, provider string
}
type TokenUsage struct{ InputTokens, OutputTokens int64 }

// Reserve charges one byte as one potential input token, including complete JSON
// history/tool schemas and framing. Conservative fixed prices never come from env.
func (c *Client) Reserve(ctx context.Context, provider, model string, inputBytes, maxOutputTokens int, requestID string) (*Reservation, error) {
	if c == nil {
		return nil, nil
	}
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
			return nil, blocked("OpenAI request exceeds standard-price context bounds")
		}
		inRate, outRate = 2000, 10000
	case provider == "openrouter" && model == "openai/gpt-6.1-terra":
		inRate, outRate = 1000, 5000
	case provider == "vertex" && model == "gemini-3.8-flash":
		inRate, outRate = 1500, 7500
	default:
		return nil, blocked("model is not covered by this development allowance")
	}
	id, err := uuid.Parse(requestID)
	if err != nil || id.String() != requestID || inputBytes < 0 || inputBytes > 1_000_000 || maxOutputTokens <= 0 || maxOutputTokens > c.outputCap(provider) {
		return nil, blocked("model request exceeds the supported development bounds")
	}
	amount := (int64(inputBytes)+FramingBytes)*inRate + int64(maxOutputTokens)*outRate
	ref := c.store.Collection("development_spend_reservations").Doc(requestID)
	err = c.store.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snapshot, e := tx.Get(c.scopeRef())
		if e != nil {
			return e
		}
		var scope scopeRecord
		if e = snapshot.DataTo(&scope); e != nil {
			return e
		}
		snapshot, e = tx.Get(c.poolRef())
		if e != nil {
			return e
		}
		var pool poolRecord
		if e = snapshot.DataTo(&pool); e != nil {
			return e
		}
		if e = c.validateScope(scope, time.Now()); e != nil {
			return e
		}
		if e = validatePool(pool); e != nil {
			return e
		}
		roll(&pool, time.Now())
		// Subtraction comparisons avoid overflowing counters supplied by bad records.
		if scope.Used > scope.Allowance-amount || pool.Pending > dayLimit-amount || pool.DayKnown > dayLimit-pool.Pending-amount || pool.Pending > monthLimit-amount || pool.MonthKnown > monthLimit-pool.Pending-amount {
			return &ControlError{admissionDenied: true, message: fmt.Sprintf("request reserve $%.6f (%d input bytes + %d framing, %d output tokens) exceeds remaining allowance $%.6f or shared budget", float64(amount)/1e9, inputBytes, FramingBytes, maxOutputTokens, float64(scope.Allowance-scope.Used)/1e9)}
		}
		scope.Used += amount
		pool.Pending += amount
		if e = tx.Create(ref, reservationRecord{Scope: c.scope, Provider: provider, Amount: amount}); e != nil {
			return e
		}
		if e = tx.Update(c.scopeRef(), []firestore.Update{{Path: "used_nanos", Value: scope.Used}}); e != nil {
			return e
		}
		return c.writePool(tx, pool)
	})
	if err != nil {
		if IsControlError(err) {
			return nil, err
		}
		return nil, blocked("budget reservation failed; provider request was not sent")
	}
	return &Reservation{client: c, id: requestID, provider: provider}, nil
}

// Unknown calls remain pending across all UTC periods. Settling a known call is
// permitted after scope closure so already-incurred spend can still be recorded.
func (r *Reservation) Settle(ctx context.Context, costUSD *float64, _ string, usage *TokenUsage) error {
	if r == nil {
		return nil
	}
	actual := int64(0)
	switch r.provider {
	case "openrouter", "openai":
		if costUSD == nil {
			return nil
		}
		nanos := math.Ceil(*costUSD * 1e9)
		if math.IsNaN(nanos) || math.IsInf(nanos, 0) || *costUSD < 0 || nanos >= float64(math.MaxInt64) {
			return blocked("invalid provider cost; reservation retained")
		}
		actual = int64(nanos)
	case "vertex":
		if usage == nil {
			return nil
		}
		if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.InputTokens > 10_000_000 || usage.OutputTokens > 10_000_000 {
			return blocked("invalid provider usage; reservation retained")
		}
		actual = usage.InputTokens*1500 + usage.OutputTokens*7500
	default:
		return blocked("unsupported reservation provider")
	}
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	c := r.client
	ref := c.store.Collection("development_spend_reservations").Doc(r.id)
	overrun := false
	err := c.store.RunTransaction(settle, func(ctx context.Context, tx *firestore.Transaction) error {
		overrun = false
		snapshot, e := tx.Get(ref)
		if e != nil {
			return e
		}
		var reservation reservationRecord
		if e = snapshot.DataTo(&reservation); e != nil {
			return e
		}
		if reservation.Scope != c.scope || reservation.Provider != r.provider || reservation.Amount <= 0 {
			return blocked("reservation is invalid; allowance retained")
		}
		if reservation.Settled {
			return nil
		}
		snapshot, e = tx.Get(c.scopeRef())
		if e != nil {
			return e
		}
		var scope scopeRecord
		if e = snapshot.DataTo(&scope); e != nil {
			return e
		}
		snapshot, e = tx.Get(c.poolRef())
		if e != nil {
			return e
		}
		var pool poolRecord
		if e = snapshot.DataTo(&pool); e != nil {
			return e
		}
		if scope.Profile != c.profile || scope.Used < reservation.Amount || pool.Pending < reservation.Amount || pool.DayKnown < 0 || pool.MonthKnown < 0 {
			return blocked("budget records are invalid; reservation retained")
		}
		roll(&pool, time.Now())
		if scope.Used-reservation.Amount > math.MaxInt64-actual || pool.DayKnown > math.MaxInt64-actual || pool.MonthKnown > math.MaxInt64-actual {
			return blocked("budget counters are invalid; reservation retained")
		}
		pool.Pending -= reservation.Amount
		scope.Used += actual - reservation.Amount
		pool.DayKnown += actual
		pool.MonthKnown += actual
		reservation.Settled = true
		overrun = actual > reservation.Amount || scope.Used > scope.Allowance || pool.DayKnown > dayLimit-pool.Pending || pool.MonthKnown > monthLimit-pool.Pending
		pool.Blocked = pool.Blocked || overrun
		if e = tx.Update(ref, []firestore.Update{{Path: "settled", Value: true}}); e != nil {
			return e
		}
		if e = tx.Update(c.scopeRef(), []firestore.Update{{Path: "used_nanos", Value: scope.Used}}); e != nil {
			return e
		}
		return c.writePool(tx, pool)
	})
	if err != nil {
		if IsControlError(err) {
			return err
		}
		return blocked("spend settlement failed; reservation retained")
	}
	if overrun {
		return blocked("provider cost exceeded its allowance; shared development spend is blocked")
	}
	return nil
}
