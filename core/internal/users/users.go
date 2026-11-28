package users

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

const LegacyID = "owner"

type User struct {
	ID              string `json:"id" firestore:"id"`
	Email           string `json:"email" firestore:"email"`
	DisplayName     string `json:"display_name" firestore:"display_name"`
	TimeZone        string `json:"time_zone" firestore:"time_zone"`
	IdentityIssuer  string `json:"identity_issuer,omitempty" firestore:"identity_issuer,omitempty"`
	IdentitySubject string `json:"identity_subject,omitempty" firestore:"identity_subject,omitempty"`

	DataResetAt  string `json:"data_reset_at,omitempty" firestore:"data_reset_at,omitempty"`
	ResetPending bool   `json:"reset_pending,omitempty" firestore:"reset_pending,omitempty"`
}

type Identity struct {
	Issuer             string
	Subject            string
	EmailAuthoritative bool
}

var (
	ErrIdentityInvalid  = errors.New("invalid sign-in identity")
	ErrIdentityConflict = errors.New("sign-in identity does not match the account")
	ErrIdentityUnbound  = errors.New("account identity requires verification")
	ErrIdentityNotFound = errors.New("no account for this sign-in")
	ErrInviteRequired   = errors.New("invite code required")
)

func (i Identity) Validate() error {
	if i.Issuer == "" || i.Subject == "" || len(i.Issuer) > 512 || len(i.Subject) > 1024 ||
		strings.TrimSpace(i.Issuer) != i.Issuer || strings.TrimSpace(i.Subject) != i.Subject {
		return ErrIdentityInvalid
	}
	for _, value := range []string{i.Issuer, i.Subject} {
		for _, c := range value {
			if c < 0x21 || c > 0x7e {
				return ErrIdentityInvalid
			}
		}
	}
	return nil
}

func (u User) HasIdentity(i Identity) bool {
	return u.IdentityIssuer != "" && u.IdentityIssuer == i.Issuer && u.IdentitySubject == i.Subject
}

func MayBindLegacyAccount(identity Identity, email string) bool {
	_, domain, ok := strings.Cut(Email(email), "@")
	return ok && domain == "gmail.com" && identity.Issuer == "https://accounts.google.com" && identity.EmailAuthoritative
}

type IdentityDirectory interface {
	ResolveIdentity(ctx context.Context, identity Identity, email, displayName, mode, inviteCode string) (User, error)
	BindIdentity(ctx context.Context, userID string, identity Identity) (User, error)
}

func ValidID(id string) bool {
	if id == LegacyID {
		return true
	}
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

func Email(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func (u User) Validate() error {
	if !ValidID(u.ID) {
		return fmt.Errorf("user ID must be a canonical UUID or owner")
	}
	a, err := mail.ParseAddress(u.Email)
	if err != nil || a.Address != u.Email || Email(u.Email) != u.Email {
		return fmt.Errorf("user email must be one normalized email address")
	}
	if u.TimeZone == "" {
		return fmt.Errorf("user time zone is required")
	}
	if _, err := time.LoadLocation(u.TimeZone); err != nil {
		return fmt.Errorf("user time zone must be an IANA time zone")
	}
	if u.IdentityIssuer != "" || u.IdentitySubject != "" {
		if err := (Identity{Issuer: u.IdentityIssuer, Subject: u.IdentitySubject}).Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (u User) DataOwner() string {
	if u.ID == LegacyID {
		if u.Email != "" {
			return u.Email
		}
		return LegacyID
	}
	return u.ID
}

func (u User) FilePrefix() string {
	if u.ID == LegacyID {
		return ""
	}
	return "users/" + u.ID + "/"
}

type Directory interface {
	Get(context.Context, string) (User, error)
	FindEmail(context.Context, string) (User, error)
	List(context.Context) ([]User, error)
	Paused(context.Context) (bool, error)
	Create(context.Context, User) error
	Close() error
}

// TrialCapSetter is an optional extension on Directory / Backend allowing callers
// to configure the daily new-trial signup cap enforced during atomic user creation.
type TrialCapSetter interface {
	SetTrialMaxSignupsPerDay(maxPerDay int)
}

type contextKey struct{}

func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}
func ID(ctx context.Context) string { id, _ := ctx.Value(contextKey{}).(string); return id }

func (u User) Facts() string { return u.FactsIn(u.TimeZone) }

func (u User) FactsIn(zone string) string {
	b, _ := json.Marshal(map[string]string{"display_name": u.DisplayName, "nori_sign_in": u.Email, "time_zone": zone,
		"note": "nori_sign_in is the address the user signs in to Nori with, not their mailbox; for \"email me\" use the connected mail account's own address"})
	return string(b)
}

type Profile func(ctx context.Context) (facts string, location *time.Location)
