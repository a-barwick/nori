// Package users defines the people Nori runs for. A user is selected by verified
// sign-in or durable work, never by browser-supplied request data.
package users

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

// LegacyID owns the existing single-owner documents, files and workflows.
const LegacyID = "owner"
const LegacyOwner = "austinbarwickdavis@gmail.com"

type User struct {
	ID          string   `json:"id" firestore:"id"`
	Email       string   `json:"email" firestore:"email"`
	DisplayName string   `json:"display_name" firestore:"display_name"`
	TimeZone    string   `json:"time_zone" firestore:"time_zone"`
	NotifyEmail string   `json:"notification_email" firestore:"notification_email"`
	Calendar    Calendar `json:"calendar" firestore:"calendar"`
	Computer    Computer `json:"computer" firestore:"computer"`
}

type Calendar struct {
	RefreshToken string `json:"refresh_token,omitempty" firestore:"refresh_token"`
	Account      string `json:"account" firestore:"account"`
	WriteID      string `json:"write_id" firestore:"write_id"`
}

type Computer struct {
	Zone     string `json:"zone" firestore:"zone"`
	Instance string `json:"instance" firestore:"instance"`
	LocalURL string `json:"local_url,omitempty" firestore:"local_url"`
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
	if u.NotifyEmail != "" && u.NotifyEmail != "off" {
		a, err := mail.ParseAddress(u.NotifyEmail)
		if err != nil || a.Address != u.NotifyEmail {
			return fmt.Errorf("notification email must be one email address or off")
		}
	}
	return nil
}

// DataOwner preserves the old documents without moving or rewriting them.
func (u User) DataOwner() string {
	if u.ID == LegacyID {
		return LegacyOwner
	}
	return u.ID
}

// FilePrefix leaves only the current user's existing files at their old paths.
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
	SetCalendar(context.Context, string, Calendar) error
	Close() error
}

type contextKey struct{}

func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}
func ID(ctx context.Context) string { id, _ := ctx.Value(contextKey{}).(string); return id }

// Facts contains only the user details models need; credentials and infrastructure
// addresses never enter a prompt.
func (u User) Facts() string {
	b, _ := json.Marshal(map[string]string{"display_name": u.DisplayName, "email": u.Email, "time_zone": u.TimeZone})
	return string(b)
}
