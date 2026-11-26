package google

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
	"time"

	"golang.org/x/oauth2"
	googleoauth "golang.org/x/oauth2/google"
	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
)

const IntegrationID = "google"
const Version = "1"

const (
	scopeCalendarRead  = "https://www.googleapis.com/auth/calendar.readonly"
	scopeCalendarWrite = "https://www.googleapis.com/auth/calendar.events"
	scopeDriveFile     = "https://www.googleapis.com/auth/drive.file"
	scopeMailSend      = "https://www.googleapis.com/auth/gmail.send"
)

type Integration struct{ ClientID, ClientSecret string }

func (g Integration) Info() connections.Info {
	return connections.Info{
		ID: IntegrationID, Title: "Google", Icon: "google", Source: "native", Auth: "oauth", MultipleAccounts: false,
		Description: "Google Calendar, selected Drive files, and Gmail sending.",
		Categories:  []string{"calendar", "files", "email"},
		Purposes:    []connections.Purpose{{ID: "calendar.write", Title: "Calendar for events"}, {ID: "mail.send", Title: "Sender for email"}},
		Capabilities: []connections.Capability{
			{ID: "calendar.read", Title: "Read calendars and events", Effect: "read", Supported: true, Scopes: []string{scopeCalendarRead}},
			{ID: "calendar.write", Title: "Create and change calendar events", Effect: "reversible_write", Supported: true, Scopes: []string{scopeCalendarRead, scopeCalendarWrite}},
			{ID: "files.read", Title: "Read selected Drive files", Effect: "read", Supported: true, Scopes: []string{scopeDriveFile}},
			{ID: "files.write", Title: "Change selected Drive files", Effect: "reversible_write", Supported: true, Scopes: []string{scopeDriveFile}},
			{ID: "mail.send", Title: "Send email", Effect: "sensitive_send", Supported: true, Scopes: []string{scopeMailSend}},
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

func (g Integration) Identify(ctx context.Context, client *http.Client) (connections.Account, error) {
	var data struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := jsonRequest(ctx, client, "GET", "https://openidconnect.googleapis.com/v1/userinfo", nil, nil, &data); err != nil {
		return connections.Account{}, err
	}
	if data.Sub == "" || data.Email == "" || !data.EmailVerified {
		return connections.Account{}, errors.New("Google did not return a verified account email")
	}
	return connections.Account{ProviderID: data.Sub, Label: data.Email}, nil
}

func (g Integration) Revoke(ctx context.Context, token *oauth2.Token) error {
	value := token.RefreshToken
	if value == "" {
		value = token.AccessToken
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://oauth2.googleapis.com/revoke", strings.NewReader(url.Values{"token": {value}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := http.DefaultClient
	if c, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && c != nil {
		client = c
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusBadRequest {
		return fmt.Errorf("Google revocation returned %d", resp.StatusCode)
	}
	return nil
}

func (g Integration) Resources(ctx context.Context, client *http.Client) ([]connections.Resource, error) {
	return listCalendars(ctx, client)
}
func (g Integration) Definitions() []extensions.Definition { return definitions }

func Register(registry *invoke.Registry) {
	registry.Register(extensions.AdapterNative, "google.calendar", calendarAdapter{})
	registry.Register(extensions.AdapterNative, "google.drive", driveAdapter{})
	registry.Register(extensions.AdapterNative, "google.mail", mailAdapter{})
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string { return fmt.Sprintf("Google returned %d: %s", e.Status, e.Message) }

func googleError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	message := payload.Error.Message
	if message == "" {
		message = http.StatusText(status)
	}
	base := &apiError{Status: status, Message: message}
	switch {
	case status == http.StatusUnauthorized:
		return fmt.Errorf("%w: %v", connections.ErrUnauthorized, base)
	case status == http.StatusForbidden && strings.Contains(strings.ToLower(message), "insufficient"):
		return fmt.Errorf("%w: %v", connections.ErrInsufficientScope, base)
	case status == http.StatusTooManyRequests:
		return &invoke.TemporaryError{After: time.Minute, Err: fmt.Errorf("%w: %v", connections.ErrRateLimited, base)}
	case status == http.StatusServiceUnavailable || status == http.StatusBadGateway:
		return &invoke.TemporaryError{After: time.Second, Err: base}
	default:
		return base
	}
}

func definite(err error) bool {
	var e *apiError
	return errors.As(err, &e) && e.Status >= 400 && e.Status < 500 && e.Status != http.StatusRequestTimeout && e.Status != http.StatusTooManyRequests
}

func request(ctx context.Context, client *http.Client, method, endpoint string, body io.Reader, headers map[string]string, max int64) ([]byte, http.Header, error) {
	if client == nil {
		return nil, nil, errors.New("a connected Google account is required")
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, resp.Header, err
	}
	if int64(len(data)) > max {
		return nil, resp.Header, errors.New("Google response exceeded the size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header, googleError(resp.StatusCode, data)
	}
	return data, resp.Header, nil
}

func jsonRequest(ctx context.Context, client *http.Client, method, endpoint string, value any, headers map[string]string, out any) error {
	var body io.Reader
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = "application/json"
	}
	data, _, err := request(ctx, client, method, endpoint, body, headers, 4<<20)
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func clientFor(call invoke.Call) (*http.Client, error) {
	if call.Grant == nil || call.Grant.Client == nil {
		return nil, errors.New("a connected Google account is required")
	}
	return call.Grant.Client, nil
}

func connectionID(call invoke.Call) string {
	if call.Grant == nil {
		return ""
	}
	return call.Grant.Connection.ID
}
func pathID(value string) string { return url.PathEscape(value) }
