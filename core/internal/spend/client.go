// Package spend reserves development model allowances atomically in a shared budget ledger. Uncontrolled production
// requests retain their normal path.
//
// The budget rules are pure functions in this package. A Ledger backend (internal/spend/firestore) provides the
// atomic reads and writes and applies the rules inside its transactions.
package spend

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"time"
)

type ControlError struct {
	message         string
	admissionDenied bool
}

func (e *ControlError) Error() string { return "spend control: " + e.message }

// Blocked is the refusal a rule or ledger backend returns when the budget cannot be used safely.
func Blocked(message string) error  { return &ControlError{message: message} }
func IsControlError(err error) bool { var e *ControlError; return errors.As(err, &e) }

// IsAdmissionDenied identifies a valid budget refusing a new request. Invalid
// configuration, unavailable storage and settlement errors remain failures.
func IsAdmissionDenied(err error) bool {
	var e *ControlError
	return errors.As(err, &e) && e.admissionDenied
}

// Client applies one chat scope's development allowance. A nil Client means spend control is not configured for this
// process, and every method accepts it.
type Client struct {
	project, database, scope, profile string
	ledger                            Ledger
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
			return nil, Blocked("sandbox requires NORI_SPEND_SCOPE and NORI_SPEND_PROFILE")
		}
		return nil, nil
	}
	raw, err := hex.DecodeString(scope)
	if err != nil || len(raw) != 32 || scope != strings.ToLower(scope) || (profile != "ci" && profile != "feature") {
		return nil, Blocked("NORI_SPEND_SCOPE must be a SHA-256 hex ID and NORI_SPEND_PROFILE must be ci or feature")
	}
	project := os.Getenv("NORI_PROJECT_ID")
	if project == "" {
		return nil, Blocked("NORI_PROJECT_ID must be configured")
	}
	return &Client{project: project, database: env("NORI_SPEND_DATABASE", "nori-sandbox"), scope: scope, profile: profile}, nil
}

func (c *Client) Close() {
	if c != nil && c.ledger != nil {
		_ = c.ledger.Close()
	}
}

func (c *Client) open(ctx context.Context) error {
	if openLedger == nil {
		return Blocked("budget ledger is not configured")
	}
	ledger, err := openLedger(ctx, LedgerConfig{Project: c.project, Database: c.database})
	if err != nil {
		if IsControlError(err) {
			return err
		}
		return Blocked("budget database is unavailable")
	}
	c.ledger = ledger
	return nil
}

// recordError names why a ledger record could not be used: it was not decodable, or could not be read.
func recordError(err error, unavailable, invalid string) error {
	if errors.Is(err, ErrInvalidRecord) {
		return Blocked(invalid)
	}
	return Blocked(unavailable)
}

func (c *Client) validate(ctx context.Context) error {
	scope, err := c.ledger.Scope(ctx, c.scope)
	if err != nil {
		return recordError(err, "development allowance is unavailable", "development allowance is invalid")
	}
	if err = ValidateScope(c.profile, scope, time.Now()); err != nil {
		return err
	}
	pool, err := c.ledger.Pool(ctx)
	if err != nil {
		return recordError(err, "shared development budget is unavailable", "shared development budget is invalid")
	}
	return ValidatePool(pool)
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
	scope, err := c.ledger.Scope(ctx, c.scope)
	if err != nil {
		return time.Time{}, recordError(err, "CI context boundary is unavailable", "CI context boundary is invalid")
	}
	if err = ValidateScope(c.profile, scope, time.Now()); err != nil {
		return time.Time{}, err
	}
	if scope.CreatedAt.IsZero() {
		// Scopes issued before created_at was recorded used a fixed 24h lease.
		scope.CreatedAt = scope.ExpiresAt.Add(-24 * time.Hour)
	}
	return scope.CreatedAt, nil
}

func (c *Client) outputCap(provider string) int { return OutputCap(c.profile, provider) }

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
		return nil, 0, Blocked("provider is not covered by this allowance")
	}
	return c, min(normalLimit, cap), nil
}

type Reservation struct {
	client       *Client
	id, provider string
}
type TokenUsage struct{ InputTokens, OutputTokens int64 }

// Reserve holds the request's worst-case cost before the provider call. Settle on the returned reservation replaces
// it with the incurred cost.
func (c *Client) Reserve(ctx context.Context, provider, model string, inputBytes, maxOutputTokens int, requestID string) (*Reservation, error) {
	if c == nil {
		return nil, nil
	}
	req, err := NewReserveRequest(c.profile, c.scope, provider, model, inputBytes, maxOutputTokens, requestID)
	if err != nil {
		return nil, err
	}
	if err = c.ledger.Reserve(ctx, req); err != nil {
		if IsControlError(err) {
			return nil, err
		}
		return nil, Blocked("budget reservation failed; provider request was not sent")
	}
	return &Reservation{client: c, id: requestID, provider: provider}, nil
}

// Settle records the incurred cost. Unknown calls remain pending across all UTC periods. Settling a known call is
// permitted after scope closure so already-incurred spend can still be recorded.
func (r *Reservation) Settle(ctx context.Context, costUSD *float64, _ string, usage *TokenUsage) error {
	if r == nil {
		return nil
	}
	actual, known, err := ActualCost(r.provider, costUSD, usage)
	if err != nil || !known {
		return err
	}
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	c := r.client
	overrun, err := c.ledger.Settle(settle, SettleRequest{ID: r.id, Scope: c.scope, Profile: c.profile, Provider: r.provider, Actual: actual})
	if err != nil {
		if IsControlError(err) {
			return err
		}
		return Blocked("spend settlement failed; reservation retained")
	}
	if overrun {
		return Blocked("provider cost exceeded its allowance; shared development spend is blocked")
	}
	return nil
}
