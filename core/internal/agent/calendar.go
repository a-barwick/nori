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
	"unicode/utf8"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"nori/internal/prompts"
)

const (
	calendarAPI       = "https://www.googleapis.com/calendar/v3"
	calendarTimeout   = 20 * time.Second
	maxCalendars      = 10
	maxCalendarEvents = 100
	maxEventText      = 300
	// A longer result would risk the saved history's block limit, so the latest events are dropped first.
	maxCalendarResult = 32 << 10
	maxCalendarSpan   = 31 * 24 * time.Hour
)

var calendarConnectHint = prompts.Runtime("calendar-connect", nil)

// GoogleScopes are what the connected account grants: reading and changing calendar
// events, sending mail, and reading a sent message's headers to confirm it.
var GoogleScopes = []string{
	"https://www.googleapis.com/auth/calendar.readonly",
	"https://www.googleapis.com/auth/calendar.events",
	"https://www.googleapis.com/auth/gmail.send",
	"https://www.googleapis.com/auth/gmail.metadata",
}

// CalendarConfig is the one account the user connected. The refresh token stays in
// this process: it never reaches the computer, the model or a saved result.
// WriteCalendar is the one calendar writes go to. APIBase and TokenURL point a local
// test stack at a loopback provider, and are empty everywhere else.
type CalendarConfig struct {
	ClientID, ClientSecret, RefreshToken, Account, WriteCalendar, APIBase, TokenURL string
}

func (c CalendarConfig) target() string {
	if c.WriteCalendar != "" {
		return c.WriteCalendar
	}
	return "primary"
}

func (c CalendarConfig) calendarURL() string {
	if c.APIBase != "" {
		return c.APIBase + "/calendar/v3"
	}
	return calendarAPI
}

func (c CalendarConfig) client(ctx context.Context) *http.Client {
	config := CalendarOAuth(c.ClientID, c.ClientSecret, "")
	if c.TokenURL != "" {
		config.Endpoint.TokenURL = c.TokenURL
	}
	return config.Client(ctx, &oauth2.Token{RefreshToken: c.RefreshToken})
}

func (c CalendarConfig) connected() bool {
	return c.ClientID != "" && c.ClientSecret != "" && c.RefreshToken != ""
}

// CalendarOAuth is shared with the connect command so both ask for the same scope.
func CalendarOAuth(clientID, clientSecret, redirect string) *oauth2.Config {
	return &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirect, Endpoint: google.Endpoint, Scopes: GoogleScopes}
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

// calendarRead returns events from every calendar the user has selected, named with
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
	result, err := readCalendars(ctx, config, config.client(ctx), from, to, s.now())
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

// CalendarEvent is one event from a calendar read. Titles and places come from the calendar, so they are data.
type CalendarEvent struct {
	Calendar string `json:"calendar"`
	EventID  string `json:"event_id"`
	Writable bool   `json:"writable,omitempty"`
	Title    string `json:"title"`
	Start    string `json:"start"`
	End      string `json:"end"`
	AllDay   bool   `json:"all_day,omitempty"`
	Location string `json:"location,omitempty"`
}

// CalendarRead is the account, its time zone and the events between two times, earliest first.
type CalendarRead struct {
	Account, TimeZone string
	Calendars         []string
	Events            []CalendarEvent
	Truncated         bool
}

// ReadCalendarEvents reads the connected account's calendars between two times. The proactive check uses it outside
// any task, with the same account and limits as calendar_read.
func ReadCalendarEvents(ctx context.Context, config CalendarConfig, from, to time.Time) (CalendarRead, error) {
	if !config.connected() {
		return CalendarRead{}, errors.New("Google Calendar is not connected")
	}
	ctx, cancel := context.WithTimeout(ctx, calendarTimeout)
	defer cancel()
	return listCalendarEvents(ctx, config, config.client(ctx), from, to)
}

func listCalendarEvents(ctx context.Context, config CalendarConfig, client *http.Client, from, to time.Time) (CalendarRead, error) {
	var read CalendarRead
	var list struct {
		Items []calendarEntry `json:"items"`
	}
	if err := calendarGet(ctx, client, config.calendarURL()+"/users/me/calendarList?minAccessRole=reader&maxResults=50", &list); err != nil {
		return read, err
	}
	selected := []calendarEntry{}
	for _, entry := range list.Items {
		if entry.Primary {
			read.Account, read.TimeZone = entry.ID, entry.TimeZone
			selected = append([]calendarEntry{entry}, selected...)
		} else if entry.Selected {
			selected = append(selected, entry)
		}
	}
	read.Truncated = len(selected) > maxCalendars
	if read.Truncated {
		selected = selected[:maxCalendars]
	}
	read.Events, read.Calendars = []CalendarEvent{}, []string{}
	for _, entry := range selected {
		read.Calendars = append(read.Calendars, entry.Summary)
		query := url.Values{"timeMin": {from.Format(time.RFC3339)}, "timeMax": {to.Format(time.RFC3339)}, "singleEvents": {"true"}, "orderBy": {"startTime"}, "maxResults": {fmt.Sprint(maxCalendarEvents)}, "fields": {"nextPageToken,items(id,summary,start,end,location,status)"}}
		var page struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				ID, Summary, Location, Status string
				Start, End                    struct {
					DateTime string `json:"dateTime"`
					Date     string `json:"date"`
				}
			} `json:"items"`
		}
		if err := calendarGet(ctx, client, config.calendarURL()+"/calendars/"+url.PathEscape(entry.ID)+"/events?"+query.Encode(), &page); err != nil {
			return read, err
		}
		read.Truncated = read.Truncated || page.NextPageToken != ""
		for _, item := range page.Items {
			if item.Status == "cancelled" {
				continue
			}
			writable := entry.ID == config.target() || (entry.Primary && config.target() == "primary")
			found := CalendarEvent{Calendar: entry.Summary, EventID: item.ID, Writable: writable, Title: clipText(item.Summary, maxEventText), Start: item.Start.DateTime, End: item.End.DateTime, Location: clipText(item.Location, maxEventText)}
			if found.Start == "" {
				found.Start, found.End, found.AllDay = item.Start.Date, item.End.Date, true
			}
			read.Events = append(read.Events, found)
		}
	}
	sort.SliceStable(read.Events, func(i, j int) bool {
		return eventInstant(read.Events[i].Start).Before(eventInstant(read.Events[j].Start))
	})
	return read, nil
}

func readCalendars(ctx context.Context, config CalendarConfig, client *http.Client, from, to, now time.Time) (map[string]any, error) {
	read, err := listCalendarEvents(ctx, config, client, from, to)
	if err != nil {
		return nil, err
	}
	events, truncated := read.Events, read.Truncated
	for len(events) > 0 {
		data, err := json.Marshal(events)
		if err != nil || len(data) <= maxCalendarResult {
			break
		}
		events, truncated = events[:len(events)*3/4], true
	}
	return map[string]any{"account": read.Account, "time_zone": read.TimeZone, "calendars": strings.Join(read.Calendars, ", "), "start": from.Format(time.RFC3339), "end": to.Format(time.RFC3339), "retrieved_at": now.Format(time.RFC3339), "events": events, "truncated": truncated}, nil
}

// clipText shortens provider text without splitting a character.
func clipText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit] + "…"
}

func eventInstant(value string) time.Time {
	if at, err := time.Parse(time.RFC3339, value); err == nil {
		return at
	}
	at, _ := time.Parse("2006-01-02", value)
	return at
}
