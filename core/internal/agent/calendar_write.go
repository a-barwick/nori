package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxEventTitle       = 512
	maxEventDescription = 2048
	// A deleted event keeps its ID, so a new event for the same action needs another one.
	maxEventIDTries = 8
)

// eventIDs are what Google accepts: lowercase base32hex.
var eventIDs = base32.HexEncoding.WithPadding(base32.NoPadding)

type savedEvent struct {
	ID, ETag, Summary, Status string
	Start, End                struct {
		DateTime string `json:"dateTime"`
		Date     string `json:"date"`
	}
	Organizer struct {
		Self bool `json:"self"`
	} `json:"organizer"`
	Attendees []struct {
		Self bool `json:"self"`
	} `json:"attendees"`
}

func (s *Service) eventURL(id string) string {
	config := s.Capabilities.Calendar
	address := config.calendarURL() + "/calendars/" + url.PathEscape(config.target()) + "/events"
	if id != "" {
		address += "/" + url.PathEscape(id)
	}
	return address
}

func parseEventTimes(start, end string) (time.Time, time.Time, error) {
	from, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return from, from, errors.New("start must be RFC 3339 with a time zone offset")
	}
	to, err := time.Parse(time.RFC3339, end)
	if err != nil {
		return from, to, errors.New("end must be RFC 3339 with a time zone offset")
	}
	if !to.After(from) || to.Sub(from) > maxCalendarSpan {
		return from, to, errors.New("end must be after start, at most 31 days later")
	}
	return from, to, nil
}

func sameInstant(a, b string) bool {
	from, err := time.Parse(time.RFC3339, a)
	to, otherErr := time.Parse(time.RFC3339, b)
	return err == nil && otherErr == nil && from.Equal(to)
}

// calendarName is the name the user knows the write calendar by.
func (s *Service) calendarName(ctx context.Context) string {
	var entry struct {
		Summary string `json:"summary"`
	}
	config := s.Capabilities.Calendar
	status, err := googleCall(ctx, config.client(ctx), http.MethodGet, config.calendarURL()+"/calendars/"+url.PathEscape(config.target()), nil, nil, &entry)
	if err != nil || status != http.StatusOK || entry.Summary == "" {
		return config.target()
	}
	return entry.Summary
}

func eventOutput(event savedEvent, account, calendar string) map[string]any {
	return map[string]any{"event_id": event.ID, "title": event.Summary, "start": event.Start.DateTime, "end": event.End.DateTime, "account": account, "calendar": calendar, "read_back": true}
}

func (s *Service) prepareEvent(ctx context.Context, title, start, end, description string, private bool) (PreparedWrite, error) {
	if title == "" || len(title) > maxEventTitle || len(description) > maxEventDescription {
		return PreparedWrite{}, errors.New("calendar_create needs a title of 1–512 bytes and a description within 2048 bytes")
	}
	from, to, err := parseEventTimes(start, end)
	if err != nil {
		return PreparedWrite{}, err
	}
	if !from.After(time.Now()) {
		return PreparedWrite{}, errors.New("the start time has passed")
	}
	config := s.Capabilities.Calendar
	client := config.client(ctx)
	calendar := s.calendarName(ctx)
	// The ID comes from the action, so a repeat, even from another task, finds the first event.
	seed := strings.Join([]string{config.Account, config.target(), title, from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339)}, "\n")
	for try := 0; try < maxEventIDTries; try++ {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s\n%d", seed, try)))
		id := strings.ToLower(eventIDs.EncodeToString(sum[:]))
		var event savedEvent
		status, err := googleCall(ctx, client, http.MethodGet, s.eventURL(id), nil, nil, &event)
		if err != nil {
			return PreparedWrite{}, err
		}
		switch {
		case status == http.StatusOK && event.Status == "cancelled":
			continue
		case status == http.StatusOK:
			output := eventOutput(event, config.Account, calendar)
			output["already_done"] = true
			return PreparedWrite{Result: output}, nil
		case status != http.StatusNotFound:
			return PreparedWrite{}, fmt.Errorf("the calendar returned HTTP %d", status)
		}
		visibility := "default"
		note := ""
		if private {
			visibility, note = "private", "\nVisibility: private"
		}
		if description != "" {
			note += "\nNote: " + description
		}
		return PreparedWrite{
			Text: "Add this event?\nAccount: " + config.Account + "\nCalendar: " + calendar + "\nTitle: " + title +
				"\nStart: " + s.when(from) + "\nEnd: " + s.when(to) + note + "\nNo one is invited or notified.",
			Spec: map[string]string{"event_id": id, "calendar": calendar, "title": title, "start": from.Format(time.RFC3339), "end": to.Format(time.RFC3339), "description": description, "visibility": visibility},
		}, nil
	}
	return PreparedWrite{}, errors.New("this event was added and deleted too many times")
}

func (s *Service) sendEvent(ctx context.Context, spec map[string]string) (string, map[string]any) {
	from, err := time.Parse(time.RFC3339, spec["start"])
	if err != nil || !from.After(time.Now()) {
		return WriteFailed, map[string]any{"applied": false, "reason": "The start time has passed, so this was not sent."}
	}
	body := map[string]any{"id": spec["event_id"], "summary": spec["title"], "visibility": spec["visibility"],
		"start": map[string]string{"dateTime": spec["start"]}, "end": map[string]string{"dateTime": spec["end"]}}
	if spec["description"] != "" {
		body["description"] = spec["description"]
	}
	config := s.Capabilities.Calendar
	status, _ := googleCall(ctx, config.client(ctx), http.MethodPost, s.eventURL("")+"?sendUpdates=none", nil, body, nil)
	if refusedByProvider(status) {
		return WriteFailed, refusal(status)
	}
	sent := ""
	if status >= 200 && status < 300 {
		sent = fmt.Sprint(status)
	}
	return s.checkEvent(ctx, spec, sent)
}

// checkEvent compares the saved event with the action. sent is the provider's status
// when this attempt received one.
func (s *Service) checkEvent(ctx context.Context, spec map[string]string, sent string) (string, map[string]any) {
	config := s.Capabilities.Calendar
	var event savedEvent
	status, err := googleCall(ctx, config.client(ctx), http.MethodGet, s.eventURL(spec["event_id"]), nil, nil, &event)
	switch {
	case err == nil && status == http.StatusOK && event.Status != "cancelled":
		output := eventOutput(event, config.Account, spec["calendar"])
		if sent != "" {
			output["provider_status"] = sent
		}
		if event.Summary == spec["title"] && sameInstant(event.Start.DateTime, spec["start"]) && sameInstant(event.End.DateTime, spec["end"]) {
			return WriteConfirmed, output
		}
		output["reason"] = "An event with this ID exists, but it doesn't match the approved action."
		return WriteUncertain, output
	case err == nil && (status == http.StatusNotFound || (status == http.StatusOK && event.Status == "cancelled")):
		if sent != "" {
			return WriteUncertain, map[string]any{"provider_status": sent, "reason": "The provider accepted the write, but the event can't be found."}
		}
		return WriteFailed, map[string]any{"applied": false, "event_id": spec["event_id"], "reason": "The event isn't on the calendar, so this write didn't happen."}
	}
	if sent != "" {
		return WriteSubmitted, map[string]any{"provider_status": sent, "event_id": spec["event_id"], "read_back": false, "account": config.Account, "calendar": spec["calendar"]}
	}
	return WriteUncertain, map[string]any{"event_id": spec["event_id"], "reason": "The calendar couldn't be read, so this write may or may not have happened."}
}

func (s *Service) prepareMove(ctx context.Context, id, start, end string) (PreparedWrite, error) {
	if id == "" || len(id) > 1024 {
		return PreparedWrite{}, errors.New("calendar_move needs the event_id calendar_read returned")
	}
	config := s.Capabilities.Calendar
	var event savedEvent
	status, err := googleCall(ctx, config.client(ctx), http.MethodGet, s.eventURL(id), nil, nil, &event)
	if err != nil {
		return PreparedWrite{}, err
	}
	if status != http.StatusOK || event.Status == "cancelled" {
		return PreparedWrite{}, fmt.Errorf("no such event on the write calendar (HTTP %d); read the calendar again", status)
	}
	if event.Start.DateTime == "" {
		return PreparedWrite{}, errors.New("all-day events can't be moved yet")
	}
	for _, attendee := range event.Attendees {
		if !attendee.Self {
			return PreparedWrite{Result: map[string]any{"applied": false, "available": false, "reason": "This event has other attendees, and moving it would change their copies. Tell Austin to move it himself."}}, nil
		}
	}
	oldStart, oldEnd, err := parseEventTimes(event.Start.DateTime, event.End.DateTime)
	if err != nil {
		return PreparedWrite{}, errors.New("the saved event has unreadable times")
	}
	from, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return PreparedWrite{}, errors.New("start must be RFC 3339 with a time zone offset")
	}
	// The event keeps its length unless the call asks for another.
	to := from.Add(oldEnd.Sub(oldStart))
	if end != "" {
		if _, to, err = parseEventTimes(start, end); err != nil {
			return PreparedWrite{}, err
		}
	}
	if !from.After(time.Now()) {
		return PreparedWrite{}, errors.New("the start time has passed")
	}
	calendar := s.calendarName(ctx)
	if from.Equal(oldStart) && to.Equal(oldEnd) {
		output := eventOutput(event, config.Account, calendar)
		output["already_done"] = true
		return PreparedWrite{Result: output}, nil
	}
	return PreparedWrite{
		Text: "Move this event?\nAccount: " + config.Account + "\nCalendar: " + calendar + "\nTitle: " + event.Summary +
			"\nFrom: " + s.when(oldStart) + " to " + s.when(oldEnd) + "\nTo: " + s.when(from) + " to " + s.when(to) +
			"\nThis changes the calendar only. No one is notified.",
		Spec: map[string]string{"event_id": id, "calendar": calendar, "title": event.Summary, "etag": event.ETag,
			"old_start": oldStart.Format(time.RFC3339), "old_end": oldEnd.Format(time.RFC3339), "start": from.Format(time.RFC3339), "end": to.Format(time.RFC3339)},
	}, nil
}

func (s *Service) sendMove(ctx context.Context, spec map[string]string) (string, map[string]any) {
	from, err := time.Parse(time.RFC3339, spec["start"])
	if err != nil || !from.After(time.Now()) {
		return WriteFailed, map[string]any{"applied": false, "reason": "The start time has passed, so this was not sent."}
	}
	config := s.Capabilities.Calendar
	body := map[string]any{"start": map[string]string{"dateTime": spec["start"]}, "end": map[string]string{"dateTime": spec["end"]}}
	status, _ := googleCall(ctx, config.client(ctx), http.MethodPatch, s.eventURL(spec["event_id"])+"?sendUpdates=none",
		map[string]string{"If-Match": spec["etag"]}, body, nil)
	if refusedByProvider(status) {
		return WriteFailed, refusal(status)
	}
	sent := ""
	if status >= 200 && status < 300 {
		sent = fmt.Sprint(status)
	}
	return s.checkMove(ctx, spec, sent)
}

func (s *Service) checkMove(ctx context.Context, spec map[string]string, sent string) (string, map[string]any) {
	config := s.Capabilities.Calendar
	var event savedEvent
	status, err := googleCall(ctx, config.client(ctx), http.MethodGet, s.eventURL(spec["event_id"]), nil, nil, &event)
	if err != nil || status != http.StatusOK {
		if sent != "" {
			return WriteSubmitted, map[string]any{"provider_status": sent, "event_id": spec["event_id"], "read_back": false, "account": config.Account, "calendar": spec["calendar"]}
		}
		return WriteUncertain, map[string]any{"event_id": spec["event_id"], "reason": "The calendar couldn't be read, so this move may or may not have happened."}
	}
	output := eventOutput(event, config.Account, spec["calendar"])
	if sent != "" {
		output["provider_status"] = sent
	}
	switch {
	case sameInstant(event.Start.DateTime, spec["start"]) && sameInstant(event.End.DateTime, spec["end"]):
		return WriteConfirmed, output
	case sent == "" && sameInstant(event.Start.DateTime, spec["old_start"]) && sameInstant(event.End.DateTime, spec["old_end"]):
		output["applied"], output["reason"] = false, "The event is still at its old time, so this move didn't happen."
		return WriteFailed, output
	}
	output["reason"] = "The event is at a time that matches neither the old time nor the approved one."
	return WriteUncertain, output
}
