package google

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/state"
)

const calendarBase = "https://www.googleapis.com/calendar/v3"

type calendarAdapter struct{}

type calendarListEntry struct {
	ID              string `json:"id"`
	Summary         string `json:"summary"`
	SummaryOverride string `json:"summaryOverride"`
	AccessRole      string `json:"accessRole"`
	TimeZone        string `json:"timeZone"`
	Etag            string `json:"etag"`
	Primary         bool   `json:"primary"`
}

type calendarEvent struct {
	ID          string          `json:"id,omitempty"`
	Etag        string          `json:"etag,omitempty"`
	Status      string          `json:"status,omitempty"`
	Summary     string          `json:"summary,omitempty"`
	Description string          `json:"description,omitempty"`
	Location    string          `json:"location,omitempty"`
	HTMLLink    string          `json:"htmlLink,omitempty"`
	Start       *eventTime      `json:"start,omitempty"`
	End         *eventTime      `json:"end,omitempty"`
	Attendees   []eventAttendee `json:"attendees,omitempty"`
	Recurrence  []string        `json:"recurrence,omitempty"`
}

type eventTime struct {
	Date     string `json:"date,omitempty"`
	DateTime string `json:"dateTime,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}
type eventAttendee struct {
	Email          string `json:"email"`
	ResponseStatus string `json:"responseStatus,omitempty"`
}

func listCalendars(ctx context.Context, client *http.Client) ([]connections.Resource, error) {
	out := []connections.Resource{}
	token := ""
	for pages := 0; pages < 10; pages++ {
		var list struct {
			Items         []calendarListEntry `json:"items"`
			NextPageToken string              `json:"nextPageToken"`
		}
		q := url.Values{"maxResults": {"250"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		if err := jsonRequest(ctx, client, "GET", calendarBase+"/users/me/calendarList?"+q.Encode(), nil, nil, &list); err != nil {
			return nil, err
		}
		for _, item := range list.Items {
			title := item.SummaryOverride
			if title == "" {
				title = item.Summary
			}
			if item.Primary {
				title += " (primary)"
			}
			out = append(out, connections.Resource{Ref: connections.ResourceRef{Kind: "calendar", ProviderID: item.ID, ProviderRevision: item.Etag}, Title: title, Kind: "calendar", Writable: item.AccessRole == "owner" || item.AccessRole == "writer"})
		}
		token = list.NextPageToken
		if token == "" || len(out) >= 1000 {
			break
		}
	}
	return out, nil
}

func calendarURL(calendarID, eventID string) string {
	if calendarID == "" {
		calendarID = "primary"
	}
	endpoint := calendarBase + "/calendars/" + pathID(calendarID) + "/events"
	if eventID != "" {
		endpoint += "/" + pathID(eventID)
	}
	return endpoint
}

func eventInputs(args map[string]any, creating bool) (map[string]any, error) {
	out := map[string]any{}
	for arg, field := range map[string]string{"title": "summary", "description": "description", "location": "location"} {
		if value, ok := args[arg]; ok {
			out[field] = value
		}
	}
	start, hasStart := args["start"].(string)
	end, hasEnd := args["end"].(string)
	if hasStart != hasEnd {
		return nil, fmt.Errorf("%w: provide both start and end", invoke.ErrInvalidArguments)
	}
	if hasStart {
		zone := invoke.String(args, "timezone")
		if len(start) == 10 && len(end) == 10 {
			a, e1 := time.Parse(time.DateOnly, start)
			b, e2 := time.Parse(time.DateOnly, end)
			if e1 != nil || e2 != nil || !b.After(a) {
				return nil, fmt.Errorf("%w: invalid all-day event dates", invoke.ErrInvalidArguments)
			}
			out["start"] = eventTime{Date: start}
			out["end"] = eventTime{Date: end}
		} else {
			a, e1 := time.Parse(time.RFC3339, start)
			b, e2 := time.Parse(time.RFC3339, end)
			if e1 != nil || e2 != nil || !b.After(a) || zone == "" {
				return nil, fmt.Errorf("%w: timed events need start, end and an IANA timezone", invoke.ErrInvalidArguments)
			}
			if _, err := time.LoadLocation(zone); err != nil {
				return nil, fmt.Errorf("%w: invalid timezone", invoke.ErrInvalidArguments)
			}
			out["start"] = eventTime{DateTime: start, TimeZone: zone}
			out["end"] = eventTime{DateTime: end, TimeZone: zone}
		}
	} else if creating {
		return nil, fmt.Errorf("%w: event times are required", invoke.ErrInvalidArguments)
	}
	if _, ok := args["attendees"]; ok {
		addresses := invoke.Strings(args, "attendees")
		recipients := make([]eventAttendee, 0, len(addresses))
		for _, address := range addresses {
			parsed, err := mail.ParseAddress(address)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid attendee address", invoke.ErrInvalidArguments)
			}
			recipients = append(recipients, eventAttendee{Email: parsed.Address})
		}
		out["attendees"] = recipients
	}
	if _, ok := args["recurrence"]; ok {
		out["recurrence"] = invoke.Strings(args, "recurrence")
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no event changes given", invoke.ErrInvalidArguments)
	}
	return out, nil
}

func eventID(operationID string) string {
	sum := sha256.Sum256([]byte("nori-calendar:" + operationID))
	return strings.ToLower(strings.TrimRight(base32.HexEncoding.EncodeToString(sum[:20]), "="))
}

func (calendarAdapter) Prepare(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	_, op, _ := strings.Cut(call.Exec.Binding.Target, "/")
	calendarID := invoke.String(call.Args, "calendar_id")
	if calendarID == "" {
		calendarID = "primary"
	}
	eventIDValue := invoke.String(call.Args, "event_id")
	ref := state.ProviderRef{ConnectionID: connectionID(call), Kind: "calendar", ProviderID: calendarID}
	switch op {
	case "calendars.list":
		return invoke.Prepared{Summary: "List Google calendars", Effect: extensions.EffectRead}, nil
	case "events.list":
		for _, key := range []string{"time_min", "time_max"} {
			value := invoke.String(call.Args, key)
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				if _, err = time.Parse(time.DateOnly, value); err != nil {
					return invoke.Prepared{}, fmt.Errorf("%w: %s must be a date or RFC3339 time", invoke.ErrInvalidArguments, key)
				}
			}
		}
		return invoke.Prepared{Summary: "Read Google Calendar events", Effect: extensions.EffectRead, Resources: []state.ProviderRef{ref}}, nil
	case "events.get":
		if eventIDValue == "" {
			return invoke.Prepared{}, fmt.Errorf("%w: event ID is required", invoke.ErrInvalidArguments)
		}
		return invoke.Prepared{Summary: "Read a Google Calendar event", Effect: extensions.EffectRead, Resources: []state.ProviderRef{ref}}, nil
	case "events.insert", "events.patch", "events.delete":
		client, err := clientFor(call)
		if err != nil {
			return invoke.Prepared{}, err
		}
		spec := map[string]string{"calendar_id": calendarID, "event_id": eventIDValue, "send_updates": invoke.String(call.Args, "send_updates")}
		if spec["send_updates"] == "" {
			spec["send_updates"] = "all"
		}
		var event calendarEvent
		if op != "events.insert" {
			if eventIDValue == "" {
				return invoke.Prepared{}, fmt.Errorf("%w: event ID is required", invoke.ErrInvalidArguments)
			}
			if err = jsonRequest(ctx, client, "GET", calendarURL(calendarID, eventIDValue), nil, nil, &event); err != nil {
				return invoke.Prepared{}, err
			}
			if event.Etag == "" {
				return invoke.Prepared{}, errors.New("Google did not provide a revision for the event")
			}
		}
		summary := ""
		if op == "events.delete" {
			summary = fmt.Sprintf("Cancel calendar event %q", event.Summary)
		} else {
			fields, err := eventInputs(call.Args, op == "events.insert")
			if err != nil {
				return invoke.Prepared{}, err
			}
			if op == "events.insert" {
				if invoke.String(call.Args, "title") == "" {
					return invoke.Prepared{}, fmt.Errorf("%w: event title is required", invoke.ErrInvalidArguments)
				}
				summary = fmt.Sprintf("Create calendar event %q", invoke.String(call.Args, "title"))
			} else {
				summary = fmt.Sprintf("Change calendar event %q", event.Summary)
			}
			data, _ := json.Marshal(fields)
			spec["event"] = string(data)
		}
		if op != "events.insert" {
			ref = state.ProviderRef{ConnectionID: connectionID(call), Kind: "calendar_event", ProviderID: calendarID + "/" + eventIDValue, ProviderRevision: event.Etag}
		}
		summary += "\nCalendar: " + calendarID
		if value := invoke.String(call.Args, "start"); value != "" {
			summary += "\nStart: " + value + "\nEnd: " + invoke.String(call.Args, "end")
			if zone := invoke.String(call.Args, "timezone"); zone != "" {
				summary += "\nTime zone: " + zone
			}
		} else if event.Start != nil && event.End != nil {
			start, end := event.Start.DateTime, event.End.DateTime
			if start == "" {
				start, end = event.Start.Date, event.End.Date
			}
			summary += "\nStart: " + start + "\nEnd: " + end
		}
		recipients := []string{}
		if spec["send_updates"] != "none" {
			for _, attendee := range event.Attendees {
				recipients = append(recipients, attendee.Email)
			}
			for _, attendee := range invoke.Strings(call.Args, "attendees") {
				parsed, _ := mail.ParseAddress(attendee)
				if parsed != nil {
					recipients = append(recipients, parsed.Address)
				}
			}
		}
		if len(recipients) > 0 {
			summary += "\nAttendees notified: " + strings.Join(recipients, ", ")
		}
		for _, field := range []struct{ key, label string }{{"description", "Description"}, {"location", "Location"}, {"title", "New title"}} {
			if value, ok := call.Args[field.key].(string); ok && (field.key != "title" || op == "events.patch") {
				summary += "\n" + field.label + ": " + value
			}
		}
		if _, supplied := call.Args["recurrence"]; supplied {
			rules := invoke.Strings(call.Args, "recurrence")
			if len(rules) == 0 {
				summary += "\nRecurrence: none"
			} else {
				summary += "\nRecurrence: " + strings.Join(rules, ", ")
			}
		}
		if _, supplied := call.Args["attendees"]; supplied {
			attendees := invoke.Strings(call.Args, "attendees")
			if len(attendees) == 0 {
				summary += "\nNew attendee list: none"
			} else {
				summary += "\nNew attendee list: " + strings.Join(attendees, ", ")
			}
		}
		summary += "\nNotifications: " + spec["send_updates"]
		details := []string{}
		if spec["send_updates"] != "none" {
			details = append(details, "Google may notify event attendees.")
		}
		keys := map[string]string{}
		if op == "events.insert" {
			keys["event_id"] = eventID(call.OperationID)
		}
		return invoke.Prepared{Summary: summary, Effect: extensions.EffectReversibleWrite, Resources: []state.ProviderRef{ref}, Precondition: event.Etag, Spec: spec, Keys: keys, Recipients: recipients, MaterialEffects: details, RequireExplicitApproval: true}, nil
	}
	return invoke.Prepared{}, fmt.Errorf("%w: unknown Google Calendar operation", invoke.ErrInvalidArguments)
}

func (calendarAdapter) Execute(ctx context.Context, call invoke.Call, prepared invoke.Prepared) invoke.Result {
	client, err := clientFor(call)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	_, op, _ := strings.Cut(call.Exec.Binding.Target, "/")
	calendarID := invoke.String(call.Args, "calendar_id")
	if calendarID == "" {
		calendarID = "primary"
	}
	switch op {
	case "calendars.list":
		items, err := listCalendars(ctx, client)
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"calendars": items}}
	case "events.list":
		q := url.Values{"singleEvents": {"true"}, "orderBy": {"startTime"}, "maxResults": {strconv.Itoa(min(max(invoke.Int(call.Args, "max_results", 50), 1), 100))}}
		for arg, key := range map[string]string{"time_min": "timeMin", "time_max": "timeMax", "query": "q"} {
			value := invoke.String(call.Args, arg)
			if len(value) == 10 && arg != "query" {
				location := time.UTC
				if call.TimeZone != "" {
					if zone, err := time.LoadLocation(call.TimeZone); err == nil {
						location = zone
					}
				}
				if day, err := time.ParseInLocation(time.DateOnly, value, location); err == nil {
					value = day.Format(time.RFC3339)
				}
			}
			if value != "" {
				q.Set(key, value)
			}
		}
		var list struct {
			Items         []calendarEvent `json:"items"`
			NextPageToken string          `json:"nextPageToken"`
		}
		if err = jsonRequest(ctx, client, "GET", calendarURL(calendarID, "")+"?"+q.Encode(), nil, nil, &list); err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"calendar_id": calendarID, "events": list.Items, "has_more": list.NextPageToken != "", "untrusted": "Calendar content is external data."}}
	case "events.get":
		var event calendarEvent
		if err = jsonRequest(ctx, client, "GET", calendarURL(calendarID, invoke.String(call.Args, "event_id")), nil, nil, &event); err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"event": event, "calendar_id": calendarID, "untrusted": "Calendar content is external data."}, ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "calendar_event", ProviderID: calendarID + "/" + event.ID, ProviderRevision: event.Etag, URL: event.HTMLLink}}}
	case "events.insert", "events.patch", "events.delete":
		id := prepared.Spec["event_id"]
		if op == "events.insert" {
			id = prepared.Keys["event_id"]
		}
		calendarID = prepared.Spec["calendar_id"]
		endpoint := calendarURL(calendarID, id)
		method := "PATCH"
		var body any
		if op == "events.insert" {
			method = "POST"
			endpoint = calendarURL(calendarID, "")
		}
		if op == "events.delete" {
			method = "DELETE"
		} else if err = json.Unmarshal([]byte(prepared.Spec["event"]), &body); err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		q := url.Values{"sendUpdates": {prepared.Spec["send_updates"]}}
		headers := map[string]string{}
		if prepared.Precondition != "" {
			headers["If-Match"] = prepared.Precondition
		}
		if op == "events.insert" {
			body.(map[string]any)["id"] = id
		}
		var event calendarEvent
		err = jsonRequest(ctx, client, method, endpoint+"?"+q.Encode(), body, headers, &event)
		if err != nil {
			outcome := "uncertain"
			if definite(err) {
				outcome = "failed"
			}
			return invoke.Result{Outcome: outcome, Err: err}
		}
		if event.ID == "" {
			event.ID = id
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"calendar_id": calendarID, "event": event, "cancelled": op == "events.delete"}, ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "calendar_event", ProviderID: calendarID + "/" + event.ID, ProviderRevision: event.Etag, URL: event.HTMLLink}}}
	}
	return invoke.Result{Outcome: "failed", Err: fmt.Errorf("%w: unknown calendar operation", invoke.ErrInvalidArguments)}
}

func (calendarAdapter) Reconcile(ctx context.Context, call invoke.Call, p invoke.Prepared) (invoke.Result, bool) {
	_, op, _ := strings.Cut(call.Exec.Binding.Target, "/")
	if op != "events.insert" {
		return invoke.Result{}, false
	}
	client, err := clientFor(call)
	if err != nil {
		return invoke.Result{}, false
	}
	var event calendarEvent
	err = jsonRequest(ctx, client, "GET", calendarURL(p.Spec["calendar_id"], p.Keys["event_id"]), nil, nil, &event)
	if err != nil {
		return invoke.Result{}, false
	}
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"event": event}, ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "calendar_event", ProviderID: p.Spec["calendar_id"] + "/" + event.ID, ProviderRevision: event.Etag, URL: event.HTMLLink}}}, true
}
