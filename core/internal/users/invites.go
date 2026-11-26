package users

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

var (
	ErrInviteNotFound      = errors.New("invite code is invalid")
	ErrInviteExpired       = errors.New("invite code has expired")
	ErrInviteExhausted     = errors.New("invite code has already been used")
	ErrInviteRevoked       = errors.New("invite code has been revoked")
	ErrInviteEmailMismatch = errors.New("invite code is not valid for this email address")
	ErrInviteInvalid       = errors.New("invite configuration is invalid")
)

const inviteAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

type Invite struct {
	Code       string     `json:"code" firestore:"code"`
	MaxUses    int        `json:"max_uses" firestore:"max_uses"`
	UseCount   int        `json:"use_count" firestore:"use_count"`
	Email      string     `json:"email,omitempty" firestore:"email,omitempty"`
	Domain     string     `json:"domain,omitempty" firestore:"domain,omitempty"`
	Note       string     `json:"note,omitempty" firestore:"note,omitempty"`
	CreatedAt  time.Time  `json:"created_at" firestore:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty" firestore:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty" firestore:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty" firestore:"last_used_at,omitempty"`
}

type CreateInviteOptions struct {
	Code      string
	MaxUses   int
	Email     string
	Domain    string
	Note      string
	ExpiresAt *time.Time
}

// AutoProvisioner is an optional extension on Directory supporting race-safe
// self-service user creation on first login.
type AutoProvisioner interface {
	FindOrCreateByEmail(ctx context.Context, email, displayName string) (User, bool, error)
}

// InviteStore manages invite codes and atomic invite redemption + user creation.
type InviteStore interface {
	CreateInvite(ctx context.Context, opts CreateInviteOptions) (Invite, error)
	ListInvites(ctx context.Context) ([]Invite, error)
	RevokeInvite(ctx context.Context, code string) (Invite, error)
	RedeemInviteAndCreateUser(ctx context.Context, code, email, displayName string) (User, Invite, error)
}

func NormalizeInviteCode(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

func NormalizeDomain(raw string) string {
	d := strings.ToLower(strings.TrimSpace(raw))
	d = strings.TrimPrefix(d, "@")
	return d
}

func GenerateInviteCode() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate invite code: %w", err)
	}
	var b strings.Builder
	b.Grow(19)
	b.WriteString("NORI")
	for i, v := range raw {
		if i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(inviteAlphabet[int(v)%len(inviteAlphabet)])
	}
	return b.String(), nil
}

func PrepareInvite(opts CreateInviteOptions, now time.Time) (Invite, error) {
	code := NormalizeInviteCode(opts.Code)
	if code == "" {
		var err error
		code, err = GenerateInviteCode()
		if err != nil {
			return Invite{}, err
		}
	}
	if strings.ContainsAny(code, "/\\ \t\r\n") {
		return Invite{}, fmt.Errorf("%w: code contains invalid characters", ErrInviteInvalid)
	}
	maxUses := opts.MaxUses
	if maxUses == 0 {
		maxUses = 1
	}
	if maxUses < 1 {
		return Invite{}, fmt.Errorf("%w: max_uses must be at least 1", ErrInviteInvalid)
	}
	email := Email(opts.Email)
	if email != "" {
		a, err := mail.ParseAddress(email)
		if err != nil || a.Address != email {
			return Invite{}, fmt.Errorf("%w: email must be a valid email address", ErrInviteInvalid)
		}
	}
	domain := NormalizeDomain(opts.Domain)
	if domain != "" && (strings.ContainsAny(domain, "@ \t\r\n/") || !strings.Contains(domain, ".")) {
		return Invite{}, fmt.Errorf("%w: domain must be a valid domain name", ErrInviteInvalid)
	}
	var expiresAt *time.Time
	if opts.ExpiresAt != nil {
		t := opts.ExpiresAt.UTC()
		if !t.After(now.UTC()) {
			return Invite{}, fmt.Errorf("%w: expires_at must be in the future", ErrInviteInvalid)
		}
		expiresAt = &t
	}
	return Invite{
		Code:      code,
		MaxUses:   maxUses,
		UseCount:  0,
		Email:     email,
		Domain:    domain,
		Note:      strings.TrimSpace(opts.Note),
		CreatedAt: now.UTC(),
		ExpiresAt: expiresAt,
	}, nil
}

func (inv Invite) ValidateForRedemption(email string, now time.Time) error {
	if inv.RevokedAt != nil {
		return ErrInviteRevoked
	}
	if inv.ExpiresAt != nil && !now.UTC().Before(inv.ExpiresAt.UTC()) {
		return ErrInviteExpired
	}
	if inv.MaxUses > 0 && inv.UseCount >= inv.MaxUses {
		return ErrInviteExhausted
	}
	normalized := Email(email)
	if inv.Email != "" && normalized != Email(inv.Email) {
		return ErrInviteEmailMismatch
	}
	if inv.Domain != "" {
		_, domain, ok := strings.Cut(normalized, "@")
		if !ok || domain != NormalizeDomain(inv.Domain) {
			return ErrInviteEmailMismatch
		}
	}
	return nil
}

func (inv Invite) Status(now time.Time) string {
	if inv.RevokedAt != nil {
		return "revoked"
	}
	if inv.ExpiresAt != nil && !now.UTC().Before(inv.ExpiresAt.UTC()) {
		return "expired"
	}
	if inv.MaxUses > 0 && inv.UseCount >= inv.MaxUses {
		return "exhausted"
	}
	return "active"
}
