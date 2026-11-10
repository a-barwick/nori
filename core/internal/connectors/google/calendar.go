package google

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/state"
)

type calendarAdapter struct{}

const maxCalendarResult = 48 << 10

func calendarService(ctx context.Context, call invoke.Call) (*calendar.Service, error) {
	if call.Grant == nil {
		return nil, errors.New("calendar tools need a connected account")
	}
	return calendar.NewService(ctx, option.WithHTTPClient(call.Grant.Client))
}

func listCalendars(ctx context.Context, client *http.Client) ([]connections.Resource, error) {
	service, err := calendar.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}
	list, err := service.CalendarList.List().MaxResults(250).Context(ctx).Do()
	if err != nil {
		return nil, classify(err)
	}
	out := make([]connections.Resource, 0, len(list.Items))
	for _, item := range list.Items {
		title := item.SummaryOverride
		if title == "" {
			title = item.Summary
		}
		id := item.Id
		if item.Primary {
			title += " (primary)"
		}
		out = append(out, connections.Resource{
			Ref:   connections.ResourceRef{Kind: "calendar", ProviderID: id, ProviderRevision: item.Etag},
			Title: title, Kind: "calendar", Writable: item.AccessRole == "owner" || item.AccessRole == "writer",
		})
	}
	return out, nil
}

// eventID derives Google's client-chosen event ID from the operation, so a repeated insert names the same event
// and a lost response can be reconciled by reading it.
func eventID(operationID string) string {
	sum := sha256.Sum256([]byte("nori-event:" + operationID))
	return strings.ToLower(strings.TrimRight(base32.HexEncoding.EncodeToString(sum[:20]), "="))
}

type eventTime struct {
	allDay             bool
	startDate, endDate string
	start, end         time.Time
	timezone           string
}

func parseEventTime(start, end, timezone string) (eventTime, error) {
	if len(start) == 10 && len(end) == 10 {
		s, err1 := time.Parse(time.DateOnly, start)
		e, err2 := time.Parse(time.DateOnly, end)
		if err1 != nil || err2 != nil || !e.After(s) {
			return eventTime{}, fmt.Errorf("%w: all-day dates must be YYYY-MM-DD with an exclusive end after the start", invoke.ErrInvalidArguments)
		}
		return eventTime{allDay: true, startDate: start, endDate: end}, nil
	}
	s, err1 := parseTime(start)
	e, err2 := parseTime(end)
	if err1 != nil || err2 != nil || !e.After(s) {
		return eventTime{}, fmt.Errorf("%w: start and end must be RFC 3339 times with the end after the start", invoke.ErrInvalidArguments)
	}
	if timezone == "" {
		return eventTime{}, fmt.Errorf("%w: a timed event needs an IANA timezone", invoke.ErrInvalidArguments)
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return eventTime{}, fmt.Errorf("%w: unknown timezone %s", invoke.ErrInvalidArguments, timezone)
	}
	return eventTime{start: s, end: e, timezone: timezone}, nil
}

func (t eventTime) apply(event *calendar.Event) {
	if t.allDay {
		event.Start = &calendar.EventDateTime{Date: t.startDate}
		event.End = &calendar.EventDateTime{Date: t.endDate}
		return
	}
	event.Start = &calendar.EventDateTime{DateTime: t.start.Format(time.RFC3339), TimeZone: t.timezone}
	event.End = &calendar.EventDateTime{DateTime: t.end.Format(time.RFC3339), TimeZone: t.timezone}
}

func (t eventTime) String() string {
	if t.allDay {
		return t.startDate + " to " + t.endDate + " (all day)"
	}
	location, _ := time.LoadLocation(t.timezone)
	return t.start.In(location).Format("Mon 2 Jan 2006 15:04") + "–" + t.end.In(location).Format("15:04 MST")
}

func boundTime(value string, end bool) (string, error) {
	if len(value) == 10 {
		day, err := time.Parse(time.DateOnly, value)
		if err != nil {
			return "", fmt.Errorf("%w: bad date %s", invoke.ErrInvalidArguments, value)
		}
		return day.Format(time.RFC3339), nil
	}
	if _, err := parseTime(value); err != nil {
		return "", fmt.Errorf("%w: bad time %s", invoke.ErrInvalidArguments, value)
	}
	return value, nil
}

func calendarRef(connectionID, id string) state.ProviderRef {
	return state.ProviderRef{ConnectionID: connectionID, Kind: "calendar", ProviderID: id}
}

// preparedWithKeys adds provider idempotency keys derived from the prepared action.
type preparedWithKeys invoke.Prepared

func (p preparedWithKeys) withKey(name string, call invoke.Call) invoke.Prepared {
	prepared := invoke.Prepared(p)
	key := invoke.ActionKey(call, prepared.Spec)
	if prepared.Keys == nil {
		prepared.Keys = map[string]string{}
	}
	switch name {
	case "event_id":
		prepared.Keys[name] = eventID(key)
	case "message_id":
		prepared.Keys[name] = messageID(key)
	}
	return prepared
}

func connectionID(call invoke.Call) string {
	if call.Grant == nil {
		return ""
	}
	return call.Grant.Connection.ID
}

func (calendarAdapter) Prepare(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	args := call.Args
	_, operation, _ := strings.Cut(call.Exec.Binding.Target, "/")
	conn := connectionID(call)
	switch operation {
	case "calendars.list":
		return invoke.Prepared{Summary: "List calendars", Effect: extensions.EffectRead}, nil
	case "events.list", "freebusy":
		minTime, err := boundTime(invoke.String(args, "time_min"), false)
		if err != nil {
			return invoke.Prepared{}, err
		}
		maxTime, err := boundTime(invoke.String(args, "time_max"), true)
		if err != nil {
			return invoke.Prepared{}, err
		}
		calendars := selectedCalendars(call)
		resources := []state.ProviderRef{}
		for _, id := range calendars {
			resources = append(resources, calendarRef(conn, id))
		}
		verb := "Read calendar events"
		if operation == "freebusy" {
			verb = "Check availability"
		}
		return invoke.Prepared{Summary: fmt.Sprintf("%s from %s to %s", verb, invoke.HumanTime(minTime, call.TimeZone), invoke.HumanTime(maxTime, call.TimeZone)), Effect: extensions.EffectRead,
			Resources: resources, Spec: map[string]string{"time_min": minTime, "time_max": maxTime, "calendars": strings.Join(calendars, ",")}}, nil
	case "events.get":
		return invoke.Prepared{Summary: "Read an event", Effect: extensions.EffectRead,
			Resources: []state.ProviderRef{{ConnectionID: conn, Kind: "calendar_event", ProviderID: invoke.String(args, "event_id")}}}, nil
	case "events.insert":
		return prepareInsert(ctx, call)
	case "events.patch", "events.delete", "events.respond":
		return prepareChange(ctx, call, operation)
	case "ics.preview", "ics.import":
		return prepareICS(ctx, call, operation)
	}
	return invoke.Prepared{}, fmt.Errorf("%w: unknown calendar operation", invoke.ErrInvalidArguments)
}

func selectedCalendars(call invoke.Call) []string {
	if ids := invoke.Strings(call.Args, "calendar_ids"); len(ids) > 0 {
		return ids
	}
	if call.Grant != nil && len(call.Grant.Connection.SelectedResourceIDs) > 0 {
		return call.Grant.Connection.SelectedResourceIDs
	}
	return []string{"primary"}
}

func notifyText(sendUpdates string, attendees int) []string {
	if attendees == 0 || sendUpdates == "none" {
		return nil
	}
	if sendUpdates == "external" {
		return []string{"Google emails attendees outside the account's organization."}
	}
	return []string{fmt.Sprintf("Google emails %d attendee(s) about this change.", attendees)}
}

func sendUpdatesValue(args map[string]any) string {
	value := invoke.String(args, "send_updates")
	if value == "" {
		return "all"
	}
	return value
}

// calendarLabel names a calendar the way the user sees it in Google Calendar, for approval summaries. The primary
// calendar is "your main calendar"; another is its (possibly overridden) title. When the list cannot be read the ID is
// used, so a summary never blocks the call.
func calendarLabel(ctx context.Context, call invoke.Call, calendarID string) string {
	if calendarID == "primary" {
		return "your main calendar"
	}
	if call.Grant != nil {
		if service, err := calendarService(ctx, call); err == nil {
			if list, err := service.CalendarList.List().MaxResults(250).Context(ctx).Do(); err == nil {
				for _, item := range list.Items {
					if item.Id != calendarID {
						continue
					}
					if item.Primary {
						return "your main calendar"
					}
					if title := cmp.Or(item.SummaryOverride, item.Summary); title != "" {
						return fmt.Sprintf("the %q calendar", title)
					}
				}
			}
		}
	}
	return calendarID
}

func prepareInsert(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	args := call.Args
	when, err := parseEventTime(invoke.String(args, "start"), invoke.String(args, "end"), invoke.String(args, "timezone"))
	if err != nil {
		return invoke.Prepared{}, err
	}
	calendarID := invoke.String(args, "calendar_id")
	if calendarID == "" {
		calendarID = "primary"
	}
	attendees := invoke.Strings(args, "attendees")
	sort.Strings(attendees)
	updates := sendUpdatesValue(args)
	effect := extensions.EffectReversibleWrite
	if len(attendees) > 0 && updates != "none" {
		effect = extensions.EffectSensitiveSend
	}
	title := invoke.String(args, "title")
	summary := fmt.Sprintf("Create %q on %s, %s", title, calendarLabel(ctx, call, calendarID), when)
	if len(attendees) > 0 {
		summary += ", inviting " + strings.Join(attendees, ", ")
	}
	return preparedWithKeys{
		Summary: summary, Effect: effect, Recipients: attendees, MaterialEffects: notifyText(updates, len(attendees)),
		Resources: []state.ProviderRef{calendarRef(connectionID(call), calendarID)},
		Spec: map[string]string{
			"calendar": calendarID, "title": title, "time": when.String(), "start": invoke.String(args, "start"), "end": invoke.String(args, "end"),
			"timezone": invoke.String(args, "timezone"), "location": invoke.String(args, "location"), "description": invoke.String(args, "description"),
			"attendees": strings.Join(attendees, ","), "recurrence": strings.Join(invoke.Strings(args, "recurrence"), "\n"), "send_updates": updates,
			"meeting_link": fmt.Sprint(invoke.Bool(args, "add_meeting_link")),
		},
	}.withKey("event_id", call), nil
}

// prepareChange reads the event's current state, so the change is bound to the provider revision it was prepared
// against and a concurrent edit in Google is not overwritten.
func prepareChange(ctx context.Context, call invoke.Call, operation string) (invoke.Prepared, error) {
	service, err := calendarService(ctx, call)
	if err != nil {
		return invoke.Prepared{}, err
	}
	args := call.Args
	calendarID, id := invoke.String(args, "calendar_id"), invoke.String(args, "event_id")
	current, err := service.Events.Get(calendarID, id).Context(ctx).Do()
	if err != nil {
		if definite(err) {
			return invoke.Prepared{}, fmt.Errorf("%w: the event could not be read: %v", invoke.ErrInvalidArguments, classify(err))
		}
		return invoke.Prepared{}, classify(err)
	}
	scope := invoke.String(args, "scope")
	if scope == "" {
		scope = "instance"
		if current.RecurringEventId == "" && len(current.Recurrence) > 0 {
			scope = "series"
		}
	}
	if scope != "instance" && current.RecurringEventId == "" && len(current.Recurrence) == 0 {
		scope = "instance"
	}
	target, revision := id, current.Etag
	if scope == "series" && current.RecurringEventId != "" {
		parent, err := service.Events.Get(calendarID, current.RecurringEventId).Context(ctx).Do()
		if err != nil {
			return invoke.Prepared{}, classify(err)
		}
		target, revision = parent.Id, parent.Etag
	}
	attendees := []string{}
	for _, attendee := range current.Attendees {
		if !attendee.Self && !attendee.Resource {
			attendees = append(attendees, attendee.Email)
		}
	}
	updates := sendUpdatesValue(args)
	spec := map[string]string{"calendar": calendarID, "event": id, "target": target, "scope": scope, "send_updates": updates}
	effect := extensions.EffectReversibleWrite
	var summary string
	switch operation {
	case "events.patch":
		for _, key := range []string{"title", "start", "end", "timezone", "location", "description"} {
			if value := invoke.String(args, key); value != "" {
				spec[key] = value
			}
		}
		add, remove := invoke.Strings(args, "add_attendees"), invoke.Strings(args, "remove_attendees")
		sort.Strings(add)
		sort.Strings(remove)
		spec["add_attendees"], spec["remove_attendees"] = strings.Join(add, ","), strings.Join(remove, ",")
		attendees = append(attendees, add...)
		summary = fmt.Sprintf("Change %q (%s)", current.Summary, scope)
		if spec["start"] != "" {
			when, err := parseEventTime(spec["start"], spec["end"], spec["timezone"])
			if err != nil {
				return invoke.Prepared{}, err
			}
			summary += " to " + when.String()
		}
		if len(add) > 0 {
			summary += ", inviting " + strings.Join(add, ", ")
		}
		if len(remove) > 0 {
			summary += ", removing " + strings.Join(remove, ", ")
		}
	case "events.delete":
		summary = fmt.Sprintf("Cancel %q (%s)", current.Summary, scope)
	case "events.respond":
		response := invoke.String(args, "response")
		spec["response"], spec["comment"] = response, invoke.String(args, "comment")
		summary = fmt.Sprintf("Reply %s to %q", response, current.Summary)
		updates = "all"
		attendees = []string{}
		if current.Organizer != nil {
			attendees = []string{current.Organizer.Email}
		}
	}
	if len(attendees) > 0 && updates != "none" {
		effect = extensions.EffectSensitiveSend
	}
	sort.Strings(attendees)
	return invoke.Prepared{
		Summary: summary, Effect: effect, Recipients: attendees, Precondition: revision, Spec: spec,
		MaterialEffects: notifyText(updates, len(attendees)),
		Resources:       []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "calendar_event", ProviderID: target, ProviderRevision: revision}},
	}, nil
}

func (a calendarAdapter) Execute(ctx context.Context, call invoke.Call, p invoke.Prepared) invoke.Result {
	service, err := calendarService(ctx, call)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	_, operation, _ := strings.Cut(call.Exec.Binding.Target, "/")
	switch operation {
	case "calendars.list":
		list, err := listCalendars(ctx, call.Grant.Client)
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"account": call.Grant.Connection.Account.Label, "calendars": list}}
	case "events.list":
		return listEvents(ctx, service, call, p)
	case "events.get":
		event, err := service.Events.Get(invoke.String(call.Args, "calendar_id"), invoke.String(call.Args, "event_id")).Context(ctx).Do()
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		projected := projectEvent(event, connectionID(call), invoke.String(call.Args, "calendar_id"))
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"event": projected}, ProviderRefs: []state.ProviderRef{eventRef(call, event)}}
	case "freebusy":
		return freeBusy(ctx, service, call, p)
	case "events.insert":
		return insertEvent(ctx, service, call, p)
	case "events.patch":
		return patchEvent(ctx, service, call, p)
	case "events.delete":
		return deleteEvent(ctx, service, call, p)
	case "events.respond":
		return respondEvent(ctx, service, call, p)
	case "ics.preview":
		content, _, err := icsPreview(ctx, service, call, p.Spec["calendar"])
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		return invoke.Result{Outcome: "confirmed", Content: content}
	case "ics.import":
		return icsImport(ctx, service, call, p)
	}
	return invoke.Result{Outcome: "failed", Err: errors.New("unknown calendar operation")}
}

func eventRef(call invoke.Call, event *calendar.Event) state.ProviderRef {
	return state.ProviderRef{ConnectionID: connectionID(call), Kind: "calendar_event", ProviderID: event.Id, ProviderRevision: event.Etag, URL: event.HtmlLink}
}

// projectEvent builds the domain CalendarEvent shape.
func projectEvent(event *calendar.Event, connection, calendarID string) map[string]any {
	when := map[string]any{}
	if event.Start != nil && event.Start.Date != "" {
		when = map[string]any{"kind": "all_day", "start_date": event.Start.Date, "end_date": event.End.Date}
	} else if event.Start != nil {
		timezone := event.Start.TimeZone
		if timezone == "" {
			timezone = "UTC"
		}
		when = map[string]any{"kind": "timed", "start": event.Start.DateTime, "end": event.End.DateTime, "timezone": timezone}
	}
	attendees := []map[string]any{}
	for _, attendee := range event.Attendees {
		role := "required"
		switch {
		case attendee.Organizer:
			role = "organizer"
		case attendee.Resource:
			role = "resource"
		case attendee.Optional:
			role = "optional"
		}
		response := map[string]string{"accepted": "accepted", "declined": "declined", "tentative": "tentative", "needsAction": "needs_action"}[attendee.ResponseStatus]
		if response == "" {
			response = "unknown"
		}
		entry := map[string]any{"email": attendee.Email, "role": role, "response": response}
		if attendee.DisplayName != "" {
			entry["name"] = attendee.DisplayName
		}
		if attendee.Self {
			entry["self"] = true
		}
		attendees = append(attendees, entry)
	}
	status := event.Status
	if status == "" {
		status = "confirmed"
	}
	out := map[string]any{
		"ref":      map[string]any{"connection_id": connection, "kind": "calendar_event", "provider_id": event.Id, "provider_revision": event.Etag},
		"calendar": map[string]any{"connection_id": connection, "kind": "calendar", "provider_id": calendarID},
		"title":    event.Summary, "time": when, "attendees": attendees, "status": status, "source_url": event.HtmlLink,
		"imported_from": "live",
		"supports": map[string]any{"update": true, "cancel": true, "invite": true, "rsvp": true,
			"recurrence_scopes": []string{"instance", "following", "series"}},
	}
	if event.Location != "" {
		out["location"] = event.Location
	}
	if event.HangoutLink != "" {
		out["meeting_url"] = event.HangoutLink
	}
	if event.Description != "" {
		description := event.Description
		if len(description) > 2000 {
			description = description[:2000] + "…"
		}
		out["description"] = description
	}
	if event.RecurringEventId != "" || len(event.Recurrence) > 0 {
		recurrence := map[string]any{}
		if len(event.Recurrence) > 0 {
			recurrence["rrule"] = strings.Join(event.Recurrence, "\n")
		}
		if event.RecurringEventId != "" {
			recurrence["series_id"] = event.RecurringEventId
			recurrence["occurrence_id"] = event.Id
		}
		out["recurrence"] = recurrence
	}
	return out
}

func listEvents(ctx context.Context, service *calendar.Service, call invoke.Call, p invoke.Prepared) invoke.Result {
	limit := invoke.Int(call.Args, "max_results", 50)
	type found struct {
		start string
		event map[string]any
	}
	var events []found
	var unavailable []string
	for _, id := range strings.Split(p.Spec["calendars"], ",") {
		request := service.Events.List(id).TimeMin(p.Spec["time_min"]).TimeMax(p.Spec["time_max"]).SingleEvents(true).OrderBy("startTime").MaxResults(int64(limit)).Context(ctx)
		if query := invoke.String(call.Args, "query"); query != "" {
			request = request.Q(query)
		}
		list, err := request.Do()
		if err != nil {
			if len(strings.Split(p.Spec["calendars"], ",")) == 1 {
				return invoke.Result{Outcome: "failed", Err: classify(err)}
			}
			unavailable = append(unavailable, id)
			continue
		}
		for _, item := range list.Items {
			start := ""
			if item.Start != nil {
				start = item.Start.DateTime + item.Start.Date
			}
			events = append(events, found{start, projectEvent(item, connectionID(call), id)})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].start < events[j].start })
	out := []map[string]any{}
	size := 0
	truncated := false
	for _, e := range events {
		if len(out) >= limit || size > maxCalendarResult {
			truncated = true
			break
		}
		size += len(fmt.Sprint(e.event))
		out = append(out, e.event)
	}
	content := map[string]any{"account": call.Grant.Connection.Account.Label, "time_min": p.Spec["time_min"], "time_max": p.Spec["time_max"], "events": out}
	if truncated {
		content["truncated"] = true
	}
	if len(unavailable) > 0 {
		content["unavailable_calendars"] = unavailable
	}
	return invoke.Result{Outcome: "confirmed", Content: content}
}

func freeBusy(ctx context.Context, service *calendar.Service, call invoke.Call, p invoke.Prepared) invoke.Result {
	ids := strings.Split(p.Spec["calendars"], ",")
	ids = append(ids, invoke.Strings(call.Args, "attendees")...)
	items := make([]*calendar.FreeBusyRequestItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, &calendar.FreeBusyRequestItem{Id: id})
	}
	response, err := service.Freebusy.Query(&calendar.FreeBusyRequest{TimeMin: p.Spec["time_min"], TimeMax: p.Spec["time_max"], Items: items}).Context(ctx).Do()
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: classify(err)}
	}
	sources := []map[string]any{}
	for _, id := range ids {
		entry := map[string]any{"id": id}
		busy := []map[string]any{}
		calendarInfo, ok := response.Calendars[id]
		if !ok || len(calendarInfo.Errors) > 0 {
			// Google could not see this calendar: the whole range is unknown, not free.
			entry["slots"] = []map[string]any{{"start": p.Spec["time_min"], "end": p.Spec["time_max"], "status": "unknown"}}
			if ok && len(calendarInfo.Errors) > 0 {
				entry["reason"] = calendarInfo.Errors[0].Reason
			}
			sources = append(sources, entry)
			continue
		}
		for _, period := range calendarInfo.Busy {
			busy = append(busy, map[string]any{"start": period.Start, "end": period.End, "status": "busy"})
		}
		entry["slots"] = busy
		sources = append(sources, entry)
	}
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{
		"time_min": p.Spec["time_min"], "time_max": p.Spec["time_max"], "sources": sources,
		"note": "Times not listed as busy are free only for sources whose availability is known.",
	}}
}

func insertEvent(ctx context.Context, service *calendar.Service, call invoke.Call, p invoke.Prepared) invoke.Result {
	args := call.Args
	when, err := parseEventTime(p.Spec["start"], p.Spec["end"], p.Spec["timezone"])
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	event := &calendar.Event{Id: p.Keys["event_id"], Summary: p.Spec["title"], Location: p.Spec["location"], Description: p.Spec["description"]}
	when.apply(event)
	for _, email := range invoke.Strings(args, "attendees") {
		event.Attendees = append(event.Attendees, &calendar.EventAttendee{Email: email})
	}
	event.Recurrence = invoke.Strings(args, "recurrence")
	request := service.Events.Insert(p.Spec["calendar"], event).SendUpdates(p.Spec["send_updates"]).Context(ctx)
	if p.Spec["meeting_link"] == "true" {
		event.ConferenceData = &calendar.ConferenceData{CreateRequest: &calendar.CreateConferenceRequest{
			RequestId: p.Keys["event_id"], ConferenceSolutionKey: &calendar.ConferenceSolutionKey{Type: "hangoutsMeet"}}}
		request = request.ConferenceDataVersion(1)
	}
	created, err := request.Do()
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict {
		// The event ID is ours, so a conflict means an earlier attempt created it.
		created, err = service.Events.Get(p.Spec["calendar"], p.Keys["event_id"]).Context(ctx).Do()
	}
	if err != nil {
		if definite(err) {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		return invoke.Result{Outcome: "uncertain", Err: classify(err)}
	}
	return readBack(ctx, service, call, p.Spec["calendar"], created.Id, "Created")
}

// readBack confirms a write by reading the provider's canonical event.
func readBack(ctx context.Context, service *calendar.Service, call invoke.Call, calendarID, id, verb string) invoke.Result {
	event, err := service.Events.Get(calendarID, id).Context(ctx).Do()
	if err != nil {
		return invoke.Result{Outcome: "submitted", Err: fmt.Errorf("google accepted the change but the read-back failed: %v", classify(err)),
			ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "calendar_event", ProviderID: id}}}
	}
	return invoke.Result{Outcome: "confirmed", ProviderRefs: []state.ProviderRef{eventRef(call, event)}, Content: map[string]any{
		"summary": verb + " " + fmt.Sprintf("%q", event.Summary) + " on " + call.Grant.Connection.Account.Label,
		"event":   projectEvent(event, connectionID(call), calendarID),
	}}
}

func (a calendarAdapter) Reconcile(ctx context.Context, call invoke.Call, p invoke.Prepared) (invoke.Result, bool) {
	service, err := calendarService(ctx, call)
	if err != nil {
		return invoke.Result{}, false
	}
	_, operation, _ := strings.Cut(call.Exec.Binding.Target, "/")
	switch operation {
	case "events.insert":
		event, err := service.Events.Get(p.Spec["calendar"], p.Keys["event_id"]).Context(ctx).Do()
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
			return invoke.Result{Outcome: "failed", Err: errors.New("the interrupted attempt did not create the event")}, true
		}
		if err != nil {
			return invoke.Result{}, false
		}
		return readBack(ctx, service, call, p.Spec["calendar"], event.Id, "Created"), true
	case "events.delete":
		event, err := service.Events.Get(p.Spec["calendar"], p.Spec["target"]).Context(ctx).Do()
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && (apiErr.Code == http.StatusNotFound || apiErr.Code == http.StatusGone) || err == nil && event.Status == "cancelled" {
			return invoke.Result{Outcome: "confirmed", Content: map[string]any{"summary": "The event is cancelled."}}, true
		}
		return invoke.Result{}, false
	case "events.patch", "events.respond":
		event, err := service.Events.Get(p.Spec["calendar"], p.Spec["target"]).Context(ctx).Do()
		if err != nil {
			return invoke.Result{}, false
		}
		if event.Etag == p.Precondition {
			return invoke.Result{Outcome: "failed", Err: errors.New("the interrupted attempt did not change the event")}, true
		}
		if operation == "events.patch" && (p.Spec["title"] == "" || p.Spec["title"] == event.Summary) {
			return readBack(ctx, service, call, p.Spec["calendar"], event.Id, "Changed"), true
		}
		return invoke.Result{}, false
	}
	return invoke.Result{}, false
}

func patchEvent(ctx context.Context, service *calendar.Service, call invoke.Call, p invoke.Prepared) invoke.Result {
	spec := p.Spec
	if spec["scope"] == "following" {
		return splitSeries(ctx, service, call, p)
	}
	current, err := service.Events.Get(spec["calendar"], spec["target"]).Context(ctx).Do()
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: classify(err)}
	}
	if current.Etag != p.Precondition {
		return invoke.Result{Outcome: "failed", Err: errors.New("the event changed in Google after this change was prepared; read it again before changing it")}
	}
	patch := &calendar.Event{}
	applyPatch(patch, current, spec)
	request := service.Events.Patch(spec["calendar"], spec["target"], patch).SendUpdates(spec["send_updates"]).Context(ctx)
	request.Header().Set("If-Match", p.Precondition)
	_, err = request.Do()
	if err != nil {
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusPreconditionFailed {
			return invoke.Result{Outcome: "failed", Err: errors.New("the event changed in Google after this change was prepared; read it again")}
		}
		if definite(err) {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		return invoke.Result{Outcome: "uncertain", Err: classify(err)}
	}
	return readBack(ctx, service, call, spec["calendar"], spec["target"], "Changed")
}

func applyPatch(patch, current *calendar.Event, spec map[string]string) {
	if spec["title"] != "" {
		patch.Summary = spec["title"]
	}
	if spec["location"] != "" {
		patch.Location = spec["location"]
	}
	if spec["description"] != "" {
		patch.Description = spec["description"]
	}
	if spec["start"] != "" {
		if when, err := parseEventTime(spec["start"], spec["end"], spec["timezone"]); err == nil {
			when.apply(patch)
		}
	}
	if spec["add_attendees"] != "" || spec["remove_attendees"] != "" {
		remove := strings.Split(spec["remove_attendees"], ",")
		for _, attendee := range current.Attendees {
			if !slices.Contains(remove, attendee.Email) {
				patch.Attendees = append(patch.Attendees, attendee)
			}
		}
		for _, email := range strings.Split(spec["add_attendees"], ",") {
			if email != "" {
				patch.Attendees = append(patch.Attendees, &calendar.EventAttendee{Email: email})
			}
		}
		if len(patch.Attendees) == 0 {
			patch.ForceSendFields = append(patch.ForceSendFields, "Attendees")
		}
	}
}

// splitSeries applies a change to this and following occurrences: the original series ends before this
// occurrence and a new series starts at it with the change.
func splitSeries(ctx context.Context, service *calendar.Service, call invoke.Call, p invoke.Prepared) invoke.Result {
	spec := p.Spec
	instance, err := service.Events.Get(spec["calendar"], spec["event"]).Context(ctx).Do()
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: classify(err)}
	}
	if instance.RecurringEventId == "" {
		return invoke.Result{Outcome: "failed", Err: errors.New("this event is not part of a recurring series")}
	}
	parent, err := service.Events.Get(spec["calendar"], instance.RecurringEventId).Context(ctx).Do()
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: classify(err)}
	}
	until, err := occurrenceStart(instance)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	ended := endRecurrence(parent.Recurrence, until.Add(-time.Second))
	next := &calendar.Event{Id: eventID(call.OperationID), Summary: parent.Summary, Location: parent.Location, Description: parent.Description,
		Start: instance.Start, End: instance.End, Attendees: parent.Attendees, Recurrence: parent.Recurrence}
	applyPatch(next, parent, spec)
	created, err := service.Events.Insert(spec["calendar"], next).SendUpdates(spec["send_updates"]).Context(ctx).Do()
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict {
		created, err = service.Events.Get(spec["calendar"], next.Id).Context(ctx).Do()
	}
	if err != nil {
		if definite(err) {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		return invoke.Result{Outcome: "uncertain", Err: classify(err)}
	}
	request := service.Events.Patch(spec["calendar"], parent.Id, &calendar.Event{Recurrence: ended}).SendUpdates(spec["send_updates"]).Context(ctx)
	request.Header().Set("If-Match", parent.Etag)
	if _, err = request.Do(); err != nil {
		return invoke.Result{Outcome: "submitted", Err: fmt.Errorf("the new series was created but the original could not be ended: %v", classify(err)),
			ProviderRefs: []state.ProviderRef{eventRef(call, created)}}
	}
	return readBack(ctx, service, call, spec["calendar"], created.Id, "Changed this and following occurrences of")
}

func occurrenceStart(event *calendar.Event) (time.Time, error) {
	if event.OriginalStartTime != nil {
		if event.OriginalStartTime.DateTime != "" {
			return parseTime(event.OriginalStartTime.DateTime)
		}
		return time.Parse(time.DateOnly, event.OriginalStartTime.Date)
	}
	if event.Start.DateTime != "" {
		return parseTime(event.Start.DateTime)
	}
	return time.Parse(time.DateOnly, event.Start.Date)
}

// endRecurrence sets UNTIL on the series' RRULE, replacing any COUNT.
func endRecurrence(rules []string, until time.Time) []string {
	out := []string{}
	for _, rule := range rules {
		if !strings.HasPrefix(rule, "RRULE:") {
			out = append(out, rule)
			continue
		}
		parts := []string{}
		for _, part := range strings.Split(strings.TrimPrefix(rule, "RRULE:"), ";") {
			if !strings.HasPrefix(part, "UNTIL=") && !strings.HasPrefix(part, "COUNT=") {
				parts = append(parts, part)
			}
		}
		parts = append(parts, "UNTIL="+until.UTC().Format("20060102T150405Z"))
		out = append(out, "RRULE:"+strings.Join(parts, ";"))
	}
	return out
}

func deleteEvent(ctx context.Context, service *calendar.Service, call invoke.Call, p invoke.Prepared) invoke.Result {
	spec := p.Spec
	if spec["scope"] == "following" {
		instance, err := service.Events.Get(spec["calendar"], spec["event"]).Context(ctx).Do()
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		parent, err := service.Events.Get(spec["calendar"], instance.RecurringEventId).Context(ctx).Do()
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		until, err := occurrenceStart(instance)
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		request := service.Events.Patch(spec["calendar"], parent.Id, &calendar.Event{Recurrence: endRecurrence(parent.Recurrence, until.Add(-time.Second))}).SendUpdates(spec["send_updates"]).Context(ctx)
		request.Header().Set("If-Match", parent.Etag)
		if _, err = request.Do(); err != nil {
			if definite(err) {
				return invoke.Result{Outcome: "failed", Err: classify(err)}
			}
			return invoke.Result{Outcome: "uncertain", Err: classify(err)}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"summary": fmt.Sprintf("Cancelled this and following occurrences of %q.", parent.Summary)},
			ProviderRefs: []state.ProviderRef{eventRef(call, parent)}}
	}
	request := service.Events.Delete(spec["calendar"], spec["target"]).SendUpdates(spec["send_updates"]).Context(ctx)
	request.Header().Set("If-Match", p.Precondition)
	err := request.Do()
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusGone {
		err = nil
	}
	if err != nil {
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusPreconditionFailed {
			return invoke.Result{Outcome: "failed", Err: errors.New("the event changed in Google after this was prepared; read it again")}
		}
		if definite(err) {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		return invoke.Result{Outcome: "uncertain", Err: classify(err)}
	}
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"summary": "Cancelled the event (" + spec["scope"] + ")."},
		ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "calendar_event", ProviderID: spec["target"]}}}
}

func respondEvent(ctx context.Context, service *calendar.Service, call invoke.Call, p invoke.Prepared) invoke.Result {
	spec := p.Spec
	current, err := service.Events.Get(spec["calendar"], spec["target"]).Context(ctx).Do()
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: classify(err)}
	}
	found := false
	patch := &calendar.Event{}
	for _, attendee := range current.Attendees {
		copyAttendee := *attendee
		if attendee.Self {
			copyAttendee.ResponseStatus = spec["response"]
			if spec["comment"] != "" {
				copyAttendee.Comment = spec["comment"]
			}
			found = true
		}
		patch.Attendees = append(patch.Attendees, &copyAttendee)
	}
	if !found {
		return invoke.Result{Outcome: "failed", Err: errors.New("the connected account is not an attendee of this event")}
	}
	request := service.Events.Patch(spec["calendar"], spec["target"], patch).SendUpdates("all").Context(ctx)
	request.Header().Set("If-Match", current.Etag)
	if _, err = request.Do(); err != nil {
		if definite(err) {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		return invoke.Result{Outcome: "uncertain", Err: classify(err)}
	}
	return readBack(ctx, service, call, spec["calendar"], spec["target"], "Replied "+spec["response"]+" to")
}
