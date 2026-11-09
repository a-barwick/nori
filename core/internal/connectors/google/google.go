// Package google is the Google App connector: one OAuth integration whose granted capabilities enable Google
// Calendar, Gmail and Contacts tools. Native adapters are used because calendar and mail semantics (recurrence
// scopes, provider concurrency tokens, client-chosen event IDs, drafts versus sends) need precise handling.
package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	googleoauth "golang.org/x/oauth2/google"
	"google.golang.org/api/googleapi"
	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
)

const IntegrationID = "google"

// Version is the adapter implementation version bound into every Google tool definition.
const Version = "1"

const (
	scopeCalendarRead  = "https://www.googleapis.com/auth/calendar.readonly"
	scopeCalendarWrite = "https://www.googleapis.com/auth/calendar.events"
	scopeMailRead      = "https://www.googleapis.com/auth/gmail.readonly"
	scopeMailCompose   = "https://www.googleapis.com/auth/gmail.compose"
	scopeMailSend      = "https://www.googleapis.com/auth/gmail.send"
	scopeMailModify    = "https://www.googleapis.com/auth/gmail.modify"
	scopeContacts      = "https://www.googleapis.com/auth/contacts.readonly"
	scopeOtherContacts = "https://www.googleapis.com/auth/contacts.other.readonly"
)

// Integration implements connections.Integration for Google.
type Integration struct {
	ClientID, ClientSecret string
}

func (g Integration) Info() connections.Info {
	return connections.Info{
		ID: IntegrationID, Title: "Google", Icon: "google", Source: "native", Auth: "oauth", MultipleAccounts: true,
		Description: "Google Calendar, Gmail and Contacts for personal and work Google accounts.",
		Categories:  []string{"calendar", "email", "contacts"},
		Purposes: []connections.Purpose{
			{ID: "calendar.write", Title: "Calendar for new events"}, {ID: "mail.send", Title: "Sender for email"},
			{ID: "mail.draft", Title: "Account for drafts"},
		},
		Capabilities: []connections.Capability{
			{ID: "calendar.read", Title: "See calendars and events", Effect: "read", Supported: true, Scopes: []string{scopeCalendarRead}},
			{ID: "calendar.write", Title: "Create and change events, invitations and replies", Effect: "reversible_write", Supported: true, Scopes: []string{scopeCalendarRead, scopeCalendarWrite}},
			{ID: "mail.read", Title: "Search and read email", Effect: "read", Supported: true, Scopes: []string{scopeMailRead}},
			{ID: "mail.draft", Title: "Create and edit drafts", Effect: "reversible_write", Supported: true, Scopes: []string{scopeMailCompose}},
			{ID: "mail.send", Title: "Send email", Effect: "sensitive_send", Supported: true, Scopes: []string{scopeMailCompose, scopeMailSend}},
			{ID: "mail.organize", Title: "Label, archive and mark email", Effect: "reversible_write", Supported: true, Scopes: []string{scopeMailModify}},
			{ID: "contacts.read", Title: "Look up contacts", Effect: "read", Supported: true, Scopes: []string{scopeContacts, scopeOtherContacts}},
		},
		BaseScopes: []string{"openid", "email"},
	}
}

func (g Integration) OAuth(redirectURL string) *oauth2.Config {
	if g.ClientID == "" || g.ClientSecret == "" || redirectURL == "" {
		return nil
	}
	return &oauth2.Config{ClientID: g.ClientID, ClientSecret: g.ClientSecret, RedirectURL: redirectURL, Endpoint: googleoauth.Endpoint}
}

// Identify reads only the account's stable subject and email address.
func (g Integration) Identify(ctx context.Context, client *http.Client) (connections.Account, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
	resp, err := client.Do(req)
	if err != nil {
		return connections.Account{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return connections.Account{}, fmt.Errorf("userinfo returned %d", resp.StatusCode)
	}
	var info struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Sub == "" {
		return connections.Account{}, errors.New("userinfo did not identify the account")
	}
	return connections.Account{ProviderID: info.Sub, Label: info.Email}, nil
}

func (g Integration) Revoke(ctx context.Context, token *oauth2.Token) error {
	value := token.RefreshToken
	if value == "" {
		value = token.AccessToken
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/revoke", strings.NewReader(url.Values{"token": {value}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		return fmt.Errorf("revocation returned %d", resp.StatusCode)
	}
	return nil
}

func (g Integration) Resources(ctx context.Context, client *http.Client) ([]connections.Resource, error) {
	return listCalendars(ctx, client)
}

func (g Integration) Definitions() []extensions.Definition { return definitions }

// Register adds the Google adapters to an executor registry.
func Register(registry *invoke.Registry) {
	registry.Register(extensions.AdapterNative, "google.calendar", calendarAdapter{})
	registry.Register(extensions.AdapterNative, "google.mail", mailAdapter{})
	registry.Register(extensions.AdapterNative, "google.contacts", contactsAdapter{})
}

// classify maps Google API errors onto connection and executor error classes.
func classify(err error) error {
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == http.StatusUnauthorized:
			return fmt.Errorf("%w: %s", connections.ErrUnauthorized, apiErr.Message)
		case apiErr.Code == http.StatusForbidden && strings.Contains(strings.ToLower(apiErr.Message), "insufficient"):
			return fmt.Errorf("%w: %s", connections.ErrInsufficientScope, apiErr.Message)
		case apiErr.Code == http.StatusTooManyRequests || apiErr.Code == http.StatusForbidden && strings.Contains(apiErr.Message, "ate Limit"):
			return fmt.Errorf("%w: %s", connections.ErrRateLimited, apiErr.Message)
		}
		return fmt.Errorf("google returned %d: %s", apiErr.Code, apiErr.Message)
	}
	return err
}

// definite reports whether an error proves the provider did not apply a write.
func definite(err error) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code >= 400 && apiErr.Code < 500 && apiErr.Code != http.StatusRequestTimeout
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339, value)
}
