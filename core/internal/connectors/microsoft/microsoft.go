// Package microsoft is the Microsoft App connector: one Microsoft identity platform (v2) OAuth integration whose
// granted delegated permissions enable Outlook calendar, mail and contacts tools through Microsoft Graph v1.0.
//
// Personal Microsoft accounts (outlook.com, hotmail.com, live.com) and work or school accounts (Microsoft Entra ID)
// share one integration, but not every capability. What differs, per Graph documentation:
//   - Free/busy of other people (calendar getSchedule) works only for work or school accounts.
//   - A work or school tenant may require an administrator to consent before users can grant these permissions; the
//     sign-in then fails with an admin-consent error and the connection reports it.
//   - Microsoft offers no endpoint to revoke a delegated refresh token for one app. Disconnect deletes Nori's copy;
//     the user removes the grant at account.live.com/consent/Manage (personal) or myapps.microsoft.com (work or
//     school).
//
// Not verified against a real account: Austin has no Microsoft app registration yet. Provider verification needs
// MICROSOFT_CLIENT_ID and MICROSOFT_CLIENT_SECRET from an Entra app registration that allows "Accounts in any
// organizational directory and personal Microsoft accounts", with NORI_OAUTH_REDIRECT_URL as a Web redirect URI.
package microsoft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/oauth2"
	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
)

const (
	IntegrationID = "microsoft"
	Version       = "1"
	graph         = "https://graph.microsoft.com/v1.0"
	// msaTenant is the tenant of every personal Microsoft account.
	msaTenant = "9188040d-6c67-4c5b-b112-36a304b66dad"
)

const (
	scopeUser          = "User.Read"
	scopeCalendarRead  = "Calendars.Read"
	scopeCalendarWrite = "Calendars.ReadWrite"
	scopeMailRead      = "Mail.Read"
	scopeMailWrite     = "Mail.ReadWrite"
	scopeMailSend      = "Mail.Send"
	scopeContacts      = "Contacts.Read"
	scopePeople        = "People.Read"
)

type Integration struct {
	ClientID, ClientSecret string
}

func (m Integration) Info() connections.Info {
	return connections.Info{
		ID: IntegrationID, Title: "Microsoft", Icon: "microsoft", Source: "native", Auth: "oauth", MultipleAccounts: true,
		Description: "Outlook calendar, mail and contacts for personal Microsoft accounts and work or school accounts.",
		Categories:  []string{"calendar", "email", "contacts"},
		Purposes: []connections.Purpose{
			{ID: "calendar.write", Title: "Calendar for new events"}, {ID: "mail.send", Title: "Sender for email"},
			{ID: "mail.draft", Title: "Account for drafts"},
		},
		Capabilities: []connections.Capability{
			{ID: "calendar.read", Title: "See calendars and events", Effect: "read", Supported: true, Scopes: []string{scopeCalendarRead}},
			{ID: "calendar.availability", Title: "See other people's free/busy", Effect: "read", Supported: true, Scopes: []string{scopeCalendarRead},
				Reason: "Work or school accounts only; personal Microsoft accounts can see only their own calendars."},
			{ID: "calendar.write", Title: "Create, change and cancel events", Effect: "reversible_write", Supported: true, Scopes: []string{scopeCalendarWrite}},
			{ID: "mail.read", Title: "Search and read email", Effect: "read", Supported: true, Scopes: []string{scopeMailRead}},
			{ID: "mail.draft", Title: "Create and edit drafts", Effect: "reversible_write", Supported: true, Scopes: []string{scopeMailWrite}},
			{ID: "mail.send", Title: "Send email", Effect: "sensitive_send", Supported: true, Scopes: []string{scopeMailWrite, scopeMailSend}},
			{ID: "mail.organize", Title: "Move, flag and mark email", Effect: "reversible_write", Supported: true, Scopes: []string{scopeMailWrite}},
			{ID: "contacts.read", Title: "Look up contacts", Effect: "read", Supported: true, Scopes: []string{scopeContacts, scopePeople}},
		},
		BaseScopes: []string{"openid", "email", "offline_access", scopeUser},
	}
}

func (m Integration) OAuth(redirectURL string) *oauth2.Config {
	if m.ClientID == "" || m.ClientSecret == "" || redirectURL == "" {
		return nil
	}
	// The common endpoint accepts both personal and work or school accounts.
	return &oauth2.Config{ClientID: m.ClientID, ClientSecret: m.ClientSecret, RedirectURL: redirectURL, Endpoint: oauth2.Endpoint{
		AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token", AuthStyle: oauth2.AuthStyleInParams,
	}}
}

// Identify reads the account's stable id and address, and whether it is a work or school account. Organization
// details are readable with User.Read only for Entra ID accounts; a personal account has none.
func (m Integration) Identify(ctx context.Context, client *http.Client) (connections.Account, error) {
	var me struct {
		ID                string `json:"id"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := get(ctx, client, graph+"/me?$select=id,mail,userPrincipalName", &me); err != nil || me.ID == "" {
		return connections.Account{}, fmt.Errorf("Microsoft Graph did not identify the account: %v", err)
	}
	label := me.Mail
	if label == "" {
		label = me.UserPrincipalName
	}
	kind := "personal"
	var org struct {
		Value []struct {
			ID string `json:"id"`
		} `json:"value"`
	}
	if get(ctx, client, graph+"/organization?$select=id", &org) == nil && len(org.Value) > 0 && org.Value[0].ID != msaTenant {
		kind = "work or school"
	}
	return connections.Account{ProviderID: me.ID, Label: label + " (" + kind + ")"}, nil
}

// Revoke is unsupported for delegated grants; see the package comment.
func (m Integration) Revoke(context.Context, *oauth2.Token) error { return nil }

func (m Integration) Resources(ctx context.Context, client *http.Client) ([]connections.Resource, error) {
	calendars, err := listCalendars(ctx, client)
	if err != nil {
		return nil, err
	}
	out := []connections.Resource{}
	for _, c := range calendars {
		out = append(out, connections.Resource{Ref: connections.ResourceRef{Kind: "calendar", ProviderID: c.ID}, Title: c.Name, Kind: "calendar",
			Selected: c.IsDefault, Writable: c.CanEdit})
	}
	return out, nil
}

func (m Integration) Definitions() []extensions.Definition { return definitions }

// Register adds the Microsoft adapters to an executor registry.
func Register(registry *invoke.Registry) {
	registry.Register(extensions.AdapterNative, "microsoft.calendar", calendarAdapter{})
	registry.Register(extensions.AdapterNative, "microsoft.mail", mailAdapter{})
	registry.Register(extensions.AdapterNative, "microsoft.contacts", contactsAdapter{})
}

// graphError is Graph's error body.
type graphError struct {
	Status int
	Code   string
	Msg    string
}

func (e *graphError) Error() string {
	return fmt.Sprintf("Microsoft Graph returned %d %s: %s", e.Status, e.Code, e.Msg)
}

func do(ctx context.Context, client *http.Client, method, address string, body any, headers map[string]string, out any) (http.Header, error) {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, address, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		return resp.Header, classify(&graphError{Status: resp.StatusCode, Code: e.Error.Code, Msg: e.Error.Message})
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.Header, err
		}
	}
	return resp.Header, nil
}

func get(ctx context.Context, client *http.Client, address string, out any) error {
	_, err := do(ctx, client, http.MethodGet, address, nil, nil, out)
	return err
}

// classify maps Graph errors onto connection and executor error classes, keeping the Graph error for definite().
func classify(e *graphError) error {
	switch {
	case e.Status == http.StatusUnauthorized:
		return fmt.Errorf("%w: %w", connections.ErrUnauthorized, e)
	case e.Status == http.StatusForbidden && (e.Code == "ErrorAccessDenied" || e.Code == "Authorization_RequestDenied" || strings.Contains(strings.ToLower(e.Msg), "scope")):
		return fmt.Errorf("%w: %w", connections.ErrInsufficientScope, e)
	case e.Status == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", connections.ErrRateLimited, e)
	}
	return e
}

// definite reports whether an error proves Graph did not apply a write.
func definite(err error) bool {
	var e *graphError
	return errors.As(err, &e) && e.Status >= 400 && e.Status < 500 && e.Status != http.StatusRequestTimeout
}

func client(call invoke.Call) (*http.Client, error) {
	if call.Grant == nil || call.Grant.Client == nil {
		return nil, errors.New("no Microsoft account is available for this call")
	}
	return call.Grant.Client, nil
}

func personal(call invoke.Call) bool {
	return call.Grant != nil && strings.HasSuffix(call.Grant.Connection.Account.Label, "(personal)")
}

func escape(id string) string { return url.PathEscape(id) }
