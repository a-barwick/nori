package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	CalendarScope       = "https://www.googleapis.com/auth/calendar.readonly"
	calendarAPI         = "https://www.googleapis.com/calendar/v3"
	calendarTimeout     = 20 * time.Second
	maxCalendars        = 10
	maxCalendarEvents   = 100
	maxCalendarSpan     = 31 * 24 * time.Hour
	calendarConnectHint = "Austin needs to run nori-connect-calendar."
)

// CalendarConfig is the one account Austin connected. The refresh token stays in
// this process: it never reaches the computer, the model or a saved result.
type CalendarConfig struct {
	ClientID, ClientSecret, RefreshToken, Account string
}

func (c CalendarConfig) connected() bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.RefreshToken != ""
}

// CalendarOAuth is shared with the connect command so both ask for the same scope.
func CalendarOAuth(clientID, clientSecret, redirect string) *oauth2.Config {
	return &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirect, Endpoint: google.Endpoint, Scopes: []string{CalendarScope}}
}

type calendarEntry struct {
	ID       string `json:"id"`
	Summary  string `json:"summary"`
	Primary  bool   `json:"primary"`
	Selected bool   `json:"selected"`
	TimeZone string `json:"timeZone"`
}

func calendarGet(ctx context.Context, client *http.Client, address string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return errors.New("calendar request could not be built")
	}
	res, err := client.Do(req)
	if err != nil {
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) {
			return errCalendarDisconnected
		}
		return errors.New("calendar request failed before a response arrived")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return errCalendarDisconnected
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("calendar returned HTTP %d", res.StatusCode)
	}
	if json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out) != nil {
		return errors.New("calendar returned an unreadable response")
	}
	return nil
}

var errCalendarDisconnected = errors.New("calendar connection is no longer accepted")

// calendarRead returns events from every calendar Austin has selected, named with
// the connected account. Attendees and descriptions stay out of the model's input.
func (s *Service) calendarRead(ctx context.Context, start, end string) (map[string]any, error) {
	config := s.Capabilities.Calendar
	if !config.connected() {
		return map[string]any{"available": false, "reason": "Google Calendar is not connected. " + calendarConnectHint}, nil
	}
	from, to, err := calendarRange(start, end)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, calendarTimeout)
	defer cancel()
	client := CalendarOAuth(config.ClientID, config.ClientSecret, "").Client(ctx, &oauth2.Token{RefreshToken: config.RefreshToken})
	result, err := readCalendars(ctx, client, from, to, s.now())
	if errors.Is(err, errCalendarDisconnected) {
		// An app in Google's testing state loses its refresh token after 7 days.
		return map[string]any{"available": false, "reason": "The Google Calendar connection has expired. " + calendarConnectHint}, nil
	}
	return result, err
}

func calendarRange(start, end string) (time.Time, time.Time, error) {
	from, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return from, from, errors.New("calendar_read start must be RFC 3339 with a time zone offset")
	}
	to, err := time.Parse(time.RFC3339, end)
	if err != nil {
		return from, to, errors.New("calendar_read end must be RFC 3339 with a time zone offset")
	}
	if !to.After(from) || to.Sub(from) > maxCalendarSpan {
		return from, to, errors.New("calendar_read needs an end after start, at most 31 days later")
	}
	return from, to, nil
}

func readCalendars(ctx context.Context, client *http.Client, from, to, now time.Time) (map[string]any, error) {
	var list struct {
		Items []calendarEntry `json:"items"`
	}
	if err := calendarGet(ctx, client, calendarAPI+"/users/me/calendarList?minAccessRole=reader&maxResults=50", &list); err != nil {
		return nil, err
	}
	account, zone := "", ""
	selected := []calendarEntry{}
	for _, entry := range list.Items {
		if entry.Primary {
			account, zone = entry.ID, entry.TimeZone
			selected = append([]calendarEntry{entry}, selected...)
		} else if entry.Selected {
			selected = append(selected, entry)
		}
	}
	truncated := len(selected) > maxCalendars
	if truncated {
		selected = selected[:maxCalendars]
	}
	type event struct {
		Calendar string `json:"calendar"`
		Title    string `json:"title"`
		Start    string `json:"start"`
		End      string `json:"end"`
		AllDay   bool   `json:"all_day,omitempty"`
		Location string `json:"location,omitempty"`
	}
	events := []event{}
	names := []string{}
	for _, entry := range selected {
		names = append(names, entry.Summary)
		query := url.Values{"timeMin": {from.Format(time.RFC3339)}, "timeMax": {to.Format(time.RFC3339)}, "singleEvents": {"true"}, "orderBy": {"startTime"}, "maxResults": {fmt.Sprint(maxCalendarEvents)}, "fields": {"nextPageToken,items(summary,start,end,location,status)"}}
		var page struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				Summary, Location, Status string
				Start, End                struct {
					DateTime string `json:"dateTime"`
					Date     string `json:"date"`
				}
			} `json:"items"`
		}
		if err := calendarGet(ctx, client, calendarAPI+"/calendars/"+url.PathEscape(entry.ID)+"/events?"+query.Encode(), &page); err != nil {
			return nil, err
		}
		truncated = truncated || page.NextPageToken != ""
		for _, item := range page.Items {
			if item.Status == "cancelled" {
				continue
			}
			found := event{Calendar: entry.Summary, Title: item.Summary, Start: item.Start.DateTime, End: item.End.DateTime, Location: item.Location}
			if found.Start == "" {
				found.Start, found.End, found.AllDay = item.Start.Date, item.End.Date, true
			}
			events = append(events, found)
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return eventInstant(events[i].Start).Before(eventInstant(events[j].Start)) })
	return map[string]any{"account": account, "time_zone": zone, "calendars": strings.Join(names, ", "), "start": from.Format(time.RFC3339), "end": to.Format(time.RFC3339), "retrieved_at": now.Format(time.RFC3339), "events": events, "truncated": truncated}, nil
}

func eventInstant(value string) time.Time {
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at
	}
	at, _ := time.Parse("2006-01-02", value)
	return at
}
