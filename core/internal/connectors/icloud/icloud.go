// Package icloud is the iCloud Calendar App connector over CalDAV (RFC 4791) with an Apple ID and an app-specific
// password.
//
// Apple offers no OAuth for iCloud calendars. The user creates an app-specific password at account.apple.com (two-
// factor authentication is required) and enters it with their Apple ID; Nori stores the pair as the connection's
// credential. Recovery: revoking that app-specific password, or changing the Apple ID password (which revokes every
// app-specific password), makes CalDAV answer 401; the connection then needs sign-in and the user repairs it with a
// new app-specific password. Disconnect deletes Nori's copy; the user revokes the password at account.apple.com.
//
// Not verified against a real account: provider verification needs a test Apple ID with two-factor authentication
// and an app-specific password.
package icloud

import (
	"bytes"
	"context"
	"encoding/xml"
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
	IntegrationID = "icloud"
	Version       = "1"
	root          = "https://caldav.icloud.com/"
)

type Integration struct{}

func (Integration) Info() connections.Info {
	return connections.Info{
		ID: IntegrationID, Title: "iCloud Calendar", Icon: "apple", Source: "native", Auth: "app_password", MultipleAccounts: true,
		Description: "iCloud calendars over CalDAV, with your Apple ID and an app-specific password from account.apple.com.",
		Categories:  []string{"calendar"},
		Purposes:    []connections.Purpose{{ID: "calendar.write", Title: "Calendar for new events"}},
		Capabilities: []connections.Capability{
			{ID: "calendar.read", Title: "See calendars and events", Effect: "read", Supported: true},
			{ID: "calendar.write", Title: "Create and delete events", Effect: "reversible_write", Supported: true},
			{ID: "mail.read", Title: "iCloud Mail", Effect: "read", Supported: false, Reason: "Nori can't read or send iCloud Mail yet; only iCloud Calendar is available."},
			{ID: "contacts.read", Title: "iCloud Contacts", Effect: "read", Supported: false, Reason: "Nori can't look up iCloud Contacts yet."},
		},
	}
}

func (Integration) OAuth(string) *oauth2.Config { return nil }

func (Integration) Identify(context.Context, *http.Client) (connections.Account, error) {
	return connections.Account{}, errors.New("iCloud signs in with an app-specific password")
}

func (Integration) Revoke(context.Context, *oauth2.Token) error { return nil }

func (Integration) Resources(ctx context.Context, client *http.Client) ([]connections.Resource, error) {
	calendars, err := discover(ctx, client)
	if err != nil {
		return nil, err
	}
	out := []connections.Resource{}
	for _, c := range calendars {
		out = append(out, connections.Resource{Ref: connections.ResourceRef{Kind: "calendar", ProviderID: c.Href}, Title: c.Name, Kind: "calendar", Selected: true, Writable: c.Writable})
	}
	return out, nil
}

func (Integration) Definitions() []extensions.Definition { return definitions }

// VerifyPassword finds the account's principal with the pair; Apple answers 401 for a wrong Apple ID or password.
func (Integration) VerifyPassword(ctx context.Context, client *http.Client, username, password string) (connections.Account, error) {
	c := Integration{}.Client(client, username, password)
	principal, err := currentPrincipal(ctx, c)
	if err != nil {
		return connections.Account{}, err
	}
	return connections.Account{ProviderID: principal, Label: username}, nil
}

func (Integration) Client(base *http.Client, username, password string) *http.Client {
	out := *base
	out.Transport = basicAuth{base: base.Transport, username: username, password: password}
	// CalDAV discovery redirects between iCloud hosts; credentials are only ever sent to icloud.com.
	out.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		if !strings.HasSuffix(req.URL.Hostname(), ".icloud.com") {
			return errors.New("refusing a redirect away from iCloud")
		}
		return nil
	}
	return &out
}

type basicAuth struct {
	base               http.RoundTripper
	username, password string
}

func (b basicAuth) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || !strings.HasSuffix(req.URL.Hostname(), "icloud.com") {
		return nil, errors.New("iCloud credentials are only sent to https icloud.com hosts")
	}
	clone := req.Clone(req.Context())
	clone.SetBasicAuth(b.username, b.password)
	base := b.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func Register(registry *invoke.Registry) {
	registry.Register(extensions.AdapterNative, "icloud.calendar", calendarAdapter{})
}

// davError carries the HTTP status of a failed CalDAV request.
type davError struct {
	Status int
	Body   string
}

func (e *davError) Error() string { return fmt.Sprintf("iCloud CalDAV returned %d", e.Status) }

func davRequest(ctx context.Context, c *http.Client, method, address, depth string, body []byte, headers map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		ct := "application/xml; charset=utf-8"
		if method == http.MethodPut {
			ct = "text/calendar; charset=utf-8"
		}
		req.Header.Set("Content-Type", ct)
	}
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		e := &davError{Status: resp.StatusCode, Body: string(data)}
		if resp.StatusCode == http.StatusUnauthorized {
			return resp, data, fmt.Errorf("%w: %w; the app-specific password was revoked or the Apple ID password changed", connections.ErrUnauthorized, e)
		}
		return resp, data, e
	}
	return resp, data, nil
}

// multistatus is the subset of a WebDAV 207 response Nori reads.
type multistatus struct {
	Responses []struct {
		Href      string `xml:"href"`
		Propstats []struct {
			Status string `xml:"status"`
			Prop   struct {
				Principal struct {
					Href string `xml:"href"`
				} `xml:"current-user-principal"`
				Home struct {
					Href string `xml:"href"`
				} `xml:"calendar-home-set"`
				DisplayName  string `xml:"displayname"`
				ResourceType struct {
					Calendar *struct{} `xml:"calendar"`
				} `xml:"resourcetype"`
				Components struct {
					Comp []struct {
						Name string `xml:"name,attr"`
					} `xml:"comp"`
				} `xml:"supported-calendar-component-set"`
				Privileges struct {
					Privilege []struct {
						Write    *struct{} `xml:"write"`
						WriteAll *struct{} `xml:"all"`
					} `xml:"privilege"`
				} `xml:"current-user-privilege-set"`
				ETag         string `xml:"getetag"`
				CalendarData string `xml:"calendar-data"`
			} `xml:"prop"`
		} `xml:"propstat"`
	} `xml:"response"`
}

func resolve(base, href string) string {
	b, err := url.Parse(base)
	if err != nil {
		return href
	}
	h, err := url.Parse(href)
	if err != nil {
		return href
	}
	return b.ResolveReference(h).String()
}

func currentPrincipal(ctx context.Context, c *http.Client) (string, error) {
	body := []byte(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:current-user-principal/></d:prop></d:propfind>`)
	resp, data, err := davRequest(ctx, c, "PROPFIND", root, "0", body, nil)
	if err != nil {
		var de *davError
		if errors.As(err, &de) && de.Status == http.StatusUnauthorized {
			return "", fmt.Errorf("%w: %w", connections.ErrBadPassword, err)
		}
		return "", err
	}
	var ms multistatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return "", err
	}
	for _, r := range ms.Responses {
		for _, ps := range r.Propstats {
			if ps.Prop.Principal.Href != "" {
				return resolve(resp.Request.URL.String(), ps.Prop.Principal.Href), nil
			}
		}
	}
	return "", errors.New("iCloud did not name the account's principal")
}

type calendarInfo struct {
	Href, Name string
	Writable   bool
}

// discover follows the principal to the calendar home and lists its event calendars.
func discover(ctx context.Context, c *http.Client) ([]calendarInfo, error) {
	principal, err := currentPrincipal(ctx, c)
	if err != nil {
		return nil, err
	}
	body := []byte(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:calendar-home-set/></d:prop></d:propfind>`)
	resp, data, err := davRequest(ctx, c, "PROPFIND", principal, "0", body, nil)
	if err != nil {
		return nil, err
	}
	var ms multistatus
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, err
	}
	home := ""
	for _, r := range ms.Responses {
		for _, ps := range r.Propstats {
			if ps.Prop.Home.Href != "" {
				home = resolve(resp.Request.URL.String(), ps.Prop.Home.Href)
			}
		}
	}
	if home == "" {
		return nil, errors.New("iCloud did not name the calendar home")
	}
	body = []byte(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:displayname/><d:resourcetype/><c:supported-calendar-component-set/><d:current-user-privilege-set/></d:prop></d:propfind>`)
	resp, data, err = davRequest(ctx, c, "PROPFIND", home, "1", body, nil)
	if err != nil {
		return nil, err
	}
	ms = multistatus{}
	if err := xml.Unmarshal(data, &ms); err != nil {
		return nil, err
	}
	var out []calendarInfo
	for _, r := range ms.Responses {
		for _, ps := range r.Propstats {
			p := ps.Prop
			if p.ResourceType.Calendar == nil {
				continue
			}
			events := len(p.Components.Comp) == 0
			for _, comp := range p.Components.Comp {
				events = events || strings.EqualFold(comp.Name, "VEVENT")
			}
			if !events {
				continue
			}
			writable := false
			for _, priv := range p.Privileges.Privilege {
				writable = writable || priv.Write != nil || priv.WriteAll != nil
			}
			out = append(out, calendarInfo{Href: resolve(resp.Request.URL.String(), r.Href), Name: p.DisplayName, Writable: writable})
		}
	}
	return out, nil
}
