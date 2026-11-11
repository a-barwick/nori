package microsoft

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/state"
)

// keyProperty is the single-value extended property that carries Nori's idempotency key on events it creates, so an
// interrupted create can be found again instead of repeated.
const keyProperty = "String {66f5a359-4659-4830-9070-00047ec6ac6e} Name NoriActionKey"

type calendar struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CanEdit   bool   `json:"canEdit"`
	IsDefault bool   `json:"isDefaultCalendar"`
	Owner     struct {
		Address string `json:"address"`
	} `json:"owner"`
}

func listCalendars(ctx context.Context, c *http.Client) ([]calendar, error) {
	var page struct {
		Value []calendar `json:"value"`
	}
	err := get(ctx, c, graph+"/me/calendars?$select=id,name,canEdit,isDefaultCalendar,owner&$top=100", &page)
	return page.Value, err
}

type calendarAdapter struct{}

func operation(call invoke.Call) string {
	_, op, _ := strings.Cut(call.Exec.Binding.Target, "/")
	return op
}

func connectionID(call invoke.Call) string {
	if call.Grant == nil {
		return ""
	}
	return call.Grant.Connection.ID
}

// graphTime turns RFC 3339 or YYYY-MM-DD into the {dateTime, timeZone} Graph expects.
func graphTime(value, zone string, end bool) (map[string]any, bool, error) {
	if d, err := time.Parse("2006-01-02", value); err == nil {
		return map[string]any{"dateTime": d.Format("2006-01-02T15:04:05"), "timeZone": "UTC"}, true, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, false, fmt.Errorf("%w: times must be RFC 3339 or YYYY-MM-DD", invoke.ErrInvalidArguments)
	}
	if zone == "" {
		return map[string]any{"dateTime": t.UTC().Format("2006-01-02T15:04:05"), "timeZone": "UTC"}, false, nil
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, false, fmt.Errorf("%w: unknown time zone %s", invoke.ErrInvalidArguments, zone)
	}
	// Graph accepts IANA names in timeZone.
	return map[string]any{"dateTime": t.In(loc).Format("2006-01-02T15:04:05"), "timeZone": zone}, false, nil
}

func instant(value string) (string, error) {
	if d, err := time.Parse("2006-01-02", value); err == nil {
		return d.UTC().Format(time.RFC3339), nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", fmt.Errorf("%w: times must be RFC 3339 or YYYY-MM-DD", invoke.ErrInvalidArguments)
	}
	return t.UTC().Format(time.RFC3339), nil
}

func (calendarAdapter) Prepare(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	args, conn := call.Args, connectionID(call)
	switch op := operation(call); op {
	case "calendars.list":
		return invoke.Prepared{Summary: "List Outlook calendars", Effect: extensions.EffectRead}, nil
	case "events.list", "schedule":
		start, err := instant(invoke.String(args, "time_min"))
		if err != nil {
			return invoke.Prepared{}, err
		}
		end, err := instant(invoke.String(args, "time_max"))
		if err != nil {
			return invoke.Prepared{}, err
		}
		if op == "schedule" && personal(call) {
			return invoke.Prepared{}, fmt.Errorf("%w: free/busy for other people needs a work or school account; this is a personal Microsoft account", invoke.ErrInvalidArguments)
		}
		verb := "Read Outlook calendar events"
		if op == "schedule" {
			verb = "Check Outlook availability"
		}
		return invoke.Prepared{Summary: fmt.Sprintf("%s from %s to %s", verb, invoke.HumanTime(start, call.TimeZone), invoke.HumanTime(end, call.TimeZone)), Effect: extensions.EffectRead,
			Spec: map[string]string{"time_min": start, "time_max": end}}, nil
	case "events.get":
		return invoke.Prepared{Summary: "Read an Outlook event", Effect: extensions.EffectRead,
			Resources: []state.ProviderRef{{ConnectionID: conn, Kind: "calendar_event", ProviderID: invoke.String(args, "event_id")}}}, nil
	case "events.create":
		title := invoke.String(args, "title")
		if title == "" {
			return invoke.Prepared{}, fmt.Errorf("%w: an event needs a title", invoke.ErrInvalidArguments)
		}
		attendees := invoke.Strings(args, "attendees")
		spec := map[string]string{"calendar_id": invoke.String(args, "calendar_id"), "title": title, "start": invoke.String(args, "start"),
			"end": invoke.String(args, "end"), "timezone": invoke.String(args, "timezone"), "location": invoke.String(args, "location"),
			"attendees": strings.Join(attendees, ","), "body_sha256": digest(invoke.String(args, "description"))}
		summary := fmt.Sprintf("Create the Outlook event %q from %s to %s", title, spec["start"], spec["end"])
		if len(attendees) > 0 {
			summary += " and invite " + strings.Join(attendees, ", ")
		}
		p := invoke.Prepared{Summary: summary, Effect: extensions.EffectReversibleWrite, Recipients: attendees, Spec: spec}
		p.Keys = map[string]string{"action": invoke.ActionKey(call, spec)}
		return p, nil
	case "events.update", "events.cancel":
		id := invoke.String(args, "event_id")
		if id == "" {
			return invoke.Prepared{}, fmt.Errorf("%w: event_id is required", invoke.ErrInvalidArguments)
		}
		c, err := client(call)
		if err != nil {
			return invoke.Prepared{}, err
		}
		var current struct {
			Subject string `json:"subject"`
			ETag    string `json:"@odata.etag"`
		}
		if err := get(ctx, c, graph+"/me/events/"+escape(id)+"?$select=subject", &current); err != nil {
			return invoke.Prepared{}, err
		}
		spec := map[string]string{"event_id": id, "change": op}
		for _, key := range []string{"title", "start", "end", "timezone", "location"} {
			if v := invoke.String(args, key); v != "" {
				spec[key] = v
			}
		}
		verb := "Change"
		if op == "events.cancel" {
			verb = "Cancel"
		}
		// The etag read now is the precondition: a change made in Outlook since then is not overwritten.
		return invoke.Prepared{Summary: fmt.Sprintf("%s the Outlook event %q", verb, current.Subject), Effect: extensions.EffectReversibleWrite,
			Resources: []state.ProviderRef{{ConnectionID: conn, Kind: "calendar_event", ProviderID: id}}, Precondition: current.ETag, Spec: spec}, nil
	}
	return invoke.Prepared{}, fmt.Errorf("%w: unknown calendar operation", invoke.ErrInvalidArguments)
}

func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

type event struct {
	ID       string `json:"id"`
	ETag     string `json:"@odata.etag,omitempty"`
	Subject  string `json:"subject"`
	WebLink  string `json:"webLink,omitempty"`
	IsAllDay bool   `json:"isAllDay"`
	Start    struct {
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	} `json:"start"`
	End struct {
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	} `json:"end"`
	Location struct {
		DisplayName string `json:"displayName"`
	} `json:"location"`
	Organizer struct {
		EmailAddress struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"organizer"`
	Attendees []struct {
		EmailAddress struct {
			Address string `json:"address"`
		} `json:"emailAddress"`
		Status struct {
			Response string `json:"response"`
		} `json:"status"`
	} `json:"attendees,omitempty"`
	ShowAs      string `json:"showAs,omitempty"`
	IsOrganizer bool   `json:"isOrganizer"`
}

func (e event) view(conn string) map[string]any {
	attendees := []map[string]string{}
	for _, a := range e.Attendees {
		attendees = append(attendees, map[string]string{"email": a.EmailAddress.Address, "response": a.Status.Response})
	}
	return map[string]any{"event_id": e.ID, "title": e.Subject, "start": e.Start.DateTime, "start_time_zone": e.Start.TimeZone,
		"end": e.End.DateTime, "end_time_zone": e.End.TimeZone, "all_day": e.IsAllDay, "location": e.Location.DisplayName,
		"organizer": e.Organizer.EmailAddress.Address, "is_organizer": e.IsOrganizer, "attendees": attendees, "show_as": e.ShowAs,
		"link": e.WebLink, "revision": e.ETag, "connection_id": conn}
}

const eventFields = "id,subject,start,end,isAllDay,location,organizer,attendees,showAs,webLink,isOrganizer"

func (calendarAdapter) Execute(ctx context.Context, call invoke.Call, p invoke.Prepared) invoke.Result {
	c, err := client(call)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	conn, args := connectionID(call), call.Args
	// Times come back in the zone the request prefers.
	zone := invoke.String(args, "timezone")
	if zone == "" {
		zone = "UTC"
	}
	prefer := map[string]string{"Prefer": `outlook.timezone="` + zone + `"`}
	switch operation(call) {
	case "calendars.list":
		calendars, err := listCalendars(ctx, c)
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		out := []map[string]any{}
		for _, cal := range calendars {
			out = append(out, map[string]any{"calendar_id": cal.ID, "name": cal.Name, "writable": cal.CanEdit, "default": cal.IsDefault, "owner": cal.Owner.Address})
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"calendars": out, "connection_id": conn}}
	case "events.list":
		q := url.Values{"startDateTime": {p.Spec["time_min"]}, "endDateTime": {p.Spec["time_max"]}, "$select": {eventFields}, "$top": {"100"}, "$orderby": {"start/dateTime"}}
		path := "/me/calendarView"
		if id := invoke.String(args, "calendar_id"); id != "" {
			path = "/me/calendars/" + escape(id) + "/calendarView"
		}
		var page struct {
			Value    []event `json:"value"`
			NextLink string  `json:"@odata.nextLink"`
		}
		if _, err := do(ctx, c, http.MethodGet, graph+path+"?"+q.Encode(), nil, prefer, &page); err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		events := []map[string]any{}
		query := strings.ToLower(invoke.String(args, "query"))
		for _, e := range page.Value {
			if query == "" || strings.Contains(strings.ToLower(e.Subject+" "+e.Location.DisplayName), query) {
				events = append(events, e.view(conn))
			}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"events": events, "more": page.NextLink != "", "time_zone": zone}}
	case "events.get":
		var e event
		if _, err := do(ctx, c, http.MethodGet, graph+"/me/events/"+escape(invoke.String(args, "event_id"))+"?$select="+eventFields, nil, prefer, &e); err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"event": e.view(conn)}}
	case "schedule":
		people := invoke.Strings(args, "attendees")
		var me struct {
			Mail string `json:"mail"`
		}
		_ = get(ctx, c, graph+"/me?$select=mail", &me)
		if me.Mail != "" {
			people = append([]string{me.Mail}, people...)
		}
		body := map[string]any{"schedules": people, "availabilityViewInterval": 30,
			"startTime": map[string]string{"dateTime": strings.TrimSuffix(p.Spec["time_min"], "Z"), "timeZone": "UTC"},
			"endTime":   map[string]string{"dateTime": strings.TrimSuffix(p.Spec["time_max"], "Z"), "timeZone": "UTC"}}
		var resp struct {
			Value []struct {
				ScheduleID string `json:"scheduleId"`
				Error      *struct {
					Message string `json:"message"`
				} `json:"error"`
				Items []struct {
					Status string `json:"status"`
					Start  struct {
						DateTime string `json:"dateTime"`
					} `json:"start"`
					End struct {
						DateTime string `json:"dateTime"`
					} `json:"end"`
				} `json:"scheduleItems"`
			} `json:"value"`
		}
		if _, err := do(ctx, c, http.MethodPost, graph+"/me/calendar/getSchedule", body, nil, &resp); err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		out := []map[string]any{}
		for _, s := range resp.Value {
			entry := map[string]any{"who": s.ScheduleID}
			if s.Error != nil {
				// Graph could not see this calendar: unknown, never free.
				entry["unknown"], entry["reason"] = true, s.Error.Message
			} else {
				busy := []map[string]string{}
				for _, item := range s.Items {
					if item.Status != "free" {
						busy = append(busy, map[string]string{"start": item.Start.DateTime + "Z", "end": item.End.DateTime + "Z", "status": item.Status})
					}
				}
				entry["busy"] = busy
			}
			out = append(out, entry)
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"schedules": out}}
	case "events.create":
		return createEvent(ctx, c, call, p, conn)
	case "events.update", "events.cancel":
		return changeEvent(ctx, c, call, p, conn)
	}
	return invoke.Result{Outcome: "failed", Err: fmt.Errorf("%w: unknown calendar operation", invoke.ErrInvalidArguments)}
}

func createEvent(ctx context.Context, c *http.Client, call invoke.Call, p invoke.Prepared, conn string) invoke.Result {
	args := call.Args
	start, allDay, err := graphTime(p.Spec["start"], p.Spec["timezone"], false)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	end, _, err := graphTime(p.Spec["end"], p.Spec["timezone"], true)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	body := map[string]any{"subject": p.Spec["title"], "start": start, "end": end, "isAllDay": allDay,
		// transactionId lets Graph drop a duplicate of the same create; the extended property lets Nori find it.
		"transactionId":                 p.Keys["action"][:36],
		"singleValueExtendedProperties": []map[string]string{{"id": keyProperty, "value": p.Keys["action"]}}}
	if loc := p.Spec["location"]; loc != "" {
		body["location"] = map[string]string{"displayName": loc}
	}
	if desc := invoke.String(args, "description"); desc != "" {
		body["body"] = map[string]string{"contentType": "text", "content": desc}
	}
	if attendees := invoke.Strings(args, "attendees"); len(attendees) > 0 {
		list := []map[string]any{}
		for _, a := range attendees {
			list = append(list, map[string]any{"emailAddress": map[string]string{"address": a}, "type": "required"})
		}
		body["attendees"] = list
	}
	path := "/me/events"
	if id := p.Spec["calendar_id"]; id != "" {
		path = "/me/calendars/" + escape(id) + "/events"
	}
	var created event
	if _, err := do(ctx, c, http.MethodPost, graph+path, body, nil, &created); err != nil {
		if definite(err) {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		return invoke.Result{Outcome: "uncertain", Err: err}
	}
	return readBack(ctx, c, conn, created.ID, "created")
}

// readBack reports an event as Outlook now has it.
func readBack(ctx context.Context, c *http.Client, conn, id, verb string) invoke.Result {
	var e event
	ref := state.ProviderRef{ConnectionID: conn, Kind: "calendar_event", ProviderID: id}
	if err := get(ctx, c, graph+"/me/events/"+escape(id)+"?$select="+eventFields, &e); err != nil {
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{verb: true, "event_id": id, "read_back": false}, ProviderRefs: []state.ProviderRef{ref}}
	}
	ref.ProviderRevision, ref.URL = e.ETag, e.WebLink
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{verb: true, "event": e.view(conn)}, ProviderRefs: []state.ProviderRef{ref}}
}

// Reconcile finds an interrupted create by its key, or confirms an interrupted change or cancel by reading the event.
func (calendarAdapter) Reconcile(ctx context.Context, call invoke.Call, p invoke.Prepared) (invoke.Result, bool) {
	c, err := client(call)
	if err != nil {
		return invoke.Result{}, false
	}
	conn := connectionID(call)
	switch operation(call) {
	case "events.create":
		filter := fmt.Sprintf("singleValueExtendedProperties/Any(ep: ep/id eq '%s' and ep/value eq '%s')", keyProperty, p.Keys["action"])
		var page struct {
			Value []event `json:"value"`
		}
		if err := get(ctx, c, graph+"/me/events?$select=id&$filter="+url.QueryEscape(filter), &page); err != nil {
			return invoke.Result{}, false
		}
		if len(page.Value) == 0 {
			return invoke.Result{Outcome: "failed", Err: fmt.Errorf("Outlook has no event from this action; it was not created"), Cleared: state.ClearedDidNotRun}, true
		}
		return readBack(ctx, c, conn, page.Value[0].ID, "created"), true
	case "events.cancel":
		var e event
		err := get(ctx, c, graph+"/me/events/"+escape(p.Spec["event_id"])+"?$select=id", &e)
		var ge *graphError
		if err != nil && errors.As(err, &ge) && ge.Status == http.StatusNotFound {
			return invoke.Result{Outcome: "confirmed", Content: map[string]any{"cancelled": true, "event_id": p.Spec["event_id"]}}, true
		}
	}
	return invoke.Result{}, false
}

func changeEvent(ctx context.Context, c *http.Client, call invoke.Call, p invoke.Prepared, conn string) invoke.Result {
	id := p.Spec["event_id"]
	headers := map[string]string{"If-Match": p.Precondition}
	if p.Spec["change"] == "events.cancel" {
		var e struct {
			IsOrganizer bool `json:"isOrganizer"`
		}
		_ = get(ctx, c, graph+"/me/events/"+escape(id)+"?$select=isOrganizer", &e)
		var err error
		if e.IsOrganizer {
			// Cancelling as organizer notifies attendees, as the approval said.
			_, err = do(ctx, c, http.MethodPost, graph+"/me/events/"+escape(id)+"/cancel", map[string]string{"comment": invoke.String(call.Args, "comment")}, nil, nil)
		} else {
			_, err = do(ctx, c, http.MethodDelete, graph+"/me/events/"+escape(id), nil, headers, nil)
		}
		if err != nil {
			if definite(err) {
				return invoke.Result{Outcome: "failed", Err: err}
			}
			return invoke.Result{Outcome: "uncertain", Err: err}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"cancelled": true, "event_id": id},
			ProviderRefs: []state.ProviderRef{{ConnectionID: conn, Kind: "calendar_event", ProviderID: id}}}
	}
	patch := map[string]any{}
	if v := p.Spec["title"]; v != "" {
		patch["subject"] = v
	}
	if v := p.Spec["location"]; v != "" {
		patch["location"] = map[string]string{"displayName": v}
	}
	if v := p.Spec["start"]; v != "" {
		t, allDay, err := graphTime(v, p.Spec["timezone"], false)
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		patch["start"], patch["isAllDay"] = t, allDay
	}
	if v := p.Spec["end"]; v != "" {
		t, _, err := graphTime(v, p.Spec["timezone"], true)
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		patch["end"] = t
	}
	if len(patch) == 0 {
		return invoke.Result{Outcome: "failed", Err: fmt.Errorf("%w: nothing to change", invoke.ErrInvalidArguments), Cleared: state.ClearedDidNotRun}
	}
	if _, err := do(ctx, c, http.MethodPatch, graph+"/me/events/"+escape(id), patch, headers, nil); err != nil {
		var ge *graphError
		if errors.As(err, &ge) && ge.Status == http.StatusPreconditionFailed {
			return invoke.Result{Outcome: "failed", Err: fmt.Errorf("the event changed in Outlook after it was read; read it again before changing it"), Cleared: state.ClearedDidNotRun}
		}
		if definite(err) {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		return invoke.Result{Outcome: "uncertain", Err: err}
	}
	return readBack(ctx, c, conn, id, "updated")
}
