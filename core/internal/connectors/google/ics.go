package google

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/api/calendar/v3"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/state"
)

// icsEvent is one VEVENT from an uploaded calendar file. An imported file is not a live account connection.
type icsEvent struct {
	UID, Summary, Description, Location, Status string
	Start, End                                  *calendar.EventDateTime
	Recurrence                                  []string
}

const maxICSEvents = 200

// parseICS reads VEVENTs from RFC 5545 text: unfolds lines, keeps DTSTART/DTEND with TZID, UTC or date values, and
// recurrence lines. Unsupported components are ignored; a file without events is an error.
func parseICS(data []byte) ([]icsEvent, error) {
	text := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += line[1:]
			continue
		}
		lines = append(lines, line)
	}
	var events []icsEvent
	var current *icsEvent
	for _, line := range lines {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, params, _ := strings.Cut(name, ";")
		key = strings.ToUpper(key)
		switch {
		case key == "BEGIN" && strings.EqualFold(value, "VEVENT"):
			current = &icsEvent{}
		case key == "END" && strings.EqualFold(value, "VEVENT") && current != nil:
			if current.Start != nil {
				events = append(events, *current)
			}
			current = nil
		case current == nil:
		case key == "UID":
			current.UID = value
		case key == "SUMMARY":
			current.Summary = unescape(value)
		case key == "DESCRIPTION":
			current.Description = unescape(value)
		case key == "LOCATION":
			current.Location = unescape(value)
		case key == "STATUS":
			current.Status = strings.ToLower(value)
		case key == "DTSTART" || key == "DTEND":
			when, err := icsTime(params, value)
			if err != nil {
				return nil, fmt.Errorf("%w: %s %q: %v", invoke.ErrInvalidArguments, key, value, err)
			}
			if key == "DTSTART" {
				current.Start = when
			} else {
				current.End = when
			}
		case key == "RRULE" || key == "EXDATE" || key == "RDATE":
			current.Recurrence = append(current.Recurrence, key+":"+value)
		}
		if len(events) > maxICSEvents {
			return nil, fmt.Errorf("%w: the file has more than %d events", invoke.ErrInvalidArguments, maxICSEvents)
		}
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: the file has no events", invoke.ErrInvalidArguments)
	}
	for i := range events {
		if events[i].End == nil {
			events[i].End = events[i].Start
		}
	}
	return events, nil
}

func unescape(value string) string {
	return strings.NewReplacer(`\n`, "\n", `\N`, "\n", `\,`, ",", `\;`, ";", `\\`, `\`).Replace(value)
}

func icsTime(params, value string) (*calendar.EventDateTime, error) {
	tzid := ""
	for _, param := range strings.Split(params, ";") {
		k, v, _ := strings.Cut(param, "=")
		switch strings.ToUpper(k) {
		case "TZID":
			tzid = strings.Trim(v, `"`)
		case "VALUE":
			if strings.EqualFold(v, "DATE") {
				day, err := time.Parse("20060102", value)
				if err != nil {
					return nil, err
				}
				return &calendar.EventDateTime{Date: day.Format(time.DateOnly)}, nil
			}
		}
	}
	if strings.HasSuffix(value, "Z") {
		t, err := time.Parse("20060102T150405Z", value)
		if err != nil {
			return nil, err
		}
		return &calendar.EventDateTime{DateTime: t.Format(time.RFC3339), TimeZone: "UTC"}, nil
	}
	if len(value) == 8 {
		day, err := time.Parse("20060102", value)
		if err != nil {
			return nil, err
		}
		return &calendar.EventDateTime{Date: day.Format(time.DateOnly)}, nil
	}
	loc := time.UTC
	if tzid != "" {
		var err error
		if loc, err = time.LoadLocation(tzid); err != nil {
			return nil, fmt.Errorf("unknown time zone %s", tzid)
		}
	}
	t, err := time.ParseInLocation("20060102T150405", value, loc)
	if err != nil {
		return nil, err
	}
	return &calendar.EventDateTime{DateTime: t.Format(time.RFC3339), TimeZone: loc.String()}, nil
}

func describe(e icsEvent, zone string) string {
	if e.Start.Date != "" {
		return fmt.Sprintf("%s (%s, all day)", e.Summary, invoke.HumanTime(e.Start.Date, zone))
	}
	return fmt.Sprintf("%s (%s)", e.Summary, invoke.HumanTime(e.Start.DateTime, zone))
}

// readICS loads and parses an uploaded calendar file the task was given.
func readICS(ctx context.Context, call invoke.Call) ([]icsEvent, string, error) {
	if call.Upload == nil {
		return nil, "", errors.New("uploads are not available to this call")
	}
	name, mediaType, data, err := call.Upload(ctx, invoke.String(call.Args, "upload_id"))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", invoke.ErrInvalidArguments, err)
	}
	if mediaType != "text/calendar" {
		return nil, "", fmt.Errorf("%w: %s is not an ICS calendar file", invoke.ErrInvalidArguments, name)
	}
	events, err := parseICS(data)
	return events, name, err
}

// icsPreview lists the file's events and which already exist on the target calendar (matched by iCalUID).
func icsPreview(ctx context.Context, service *calendar.Service, call invoke.Call, calendarID string) (map[string]any, []icsEvent, error) {
	events, name, err := readICS(ctx, call)
	if err != nil {
		return nil, nil, err
	}
	rows := []map[string]any{}
	for _, e := range events {
		row := map[string]any{"uid": e.UID, "title": e.Summary, "start": e.Start, "end": e.End, "location": e.Location}
		if len(e.Recurrence) > 0 {
			row["recurrence"] = e.Recurrence
		}
		if e.UID != "" {
			existing, err := service.Events.List(calendarID).ICalUID(e.UID).ShowDeleted(false).MaxResults(1).Context(ctx).Do()
			// Only an event carrying this exact iCalUID is a duplicate.
			if err == nil && len(existing.Items) > 0 && existing.Items[0].ICalUID == e.UID {
				row["duplicate_of"] = existing.Items[0].Id
			}
		}
		rows = append(rows, row)
	}
	return map[string]any{"file": name, "calendar_id": calendarID, "events": rows, "imported_from": "ics",
		"note": "The events come from the uploaded file and nothing was imported. Each event was checked against the live calendar by its iCalUID: duplicate_of marks one that already exists there (import updates it instead of adding a copy); the rest are not on the calendar yet."}, events, nil
}

func prepareICS(ctx context.Context, call invoke.Call, operation string) (invoke.Prepared, error) {
	calendarID := invoke.String(call.Args, "calendar_id")
	if calendarID == "" {
		calendarID = "primary"
	}
	events, name, err := readICS(ctx, call)
	if err != nil {
		return invoke.Prepared{}, err
	}
	if operation == "ics.preview" {
		return invoke.Prepared{Summary: "Preview " + name, Effect: extensions.EffectRead, Spec: map[string]string{"calendar": calendarID}}, nil
	}
	var titles []string
	for _, e := range events {
		titles = append(titles, describe(e, call.TimeZone))
	}
	return invoke.Prepared{
		Summary: fmt.Sprintf("Import %d events from %s into %s: %s", len(events), name, calendarID, strings.Join(titles, "; ")),
		Effect:  extensions.EffectReversibleWrite, Resources: []state.ProviderRef{calendarRef(connectionID(call), calendarID)},
		Spec: map[string]string{"calendar": calendarID, "upload": invoke.String(call.Args, "upload_id"), "events": strings.Join(titles, "\n")},
		// Import never notifies attendees.
		MaterialEffects: []string{"Events are added without sending invitations."},
	}, nil
}

// icsImport imports by iCalUID, so importing the same file again updates the same events instead of duplicating.
func icsImport(ctx context.Context, service *calendar.Service, call invoke.Call, p invoke.Prepared) invoke.Result {
	events, _, err := readICS(ctx, call)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	imported := []map[string]any{}
	var refs []state.ProviderRef
	for i, e := range events {
		uid := e.UID
		if uid == "" {
			uid = fmt.Sprintf("%s-%d@nori.invalid", invoke.ActionKey(call, p.Spec)[:24], i)
		}
		event := &calendar.Event{ICalUID: uid, Summary: e.Summary, Description: e.Description, Location: e.Location, Start: e.Start, End: e.End, Recurrence: e.Recurrence}
		created, err := service.Events.Import(p.Spec["calendar"], event).Context(ctx).Do()
		if err != nil {
			outcome := "uncertain"
			if definite(err) && len(imported) == 0 {
				outcome = "failed"
			}
			return invoke.Result{Outcome: outcome, Err: fmt.Errorf("imported %d of %d events, then: %v", len(imported), len(events), classify(err)),
				Content: map[string]any{"imported": imported}, ProviderRefs: refs}
		}
		// Report the event as Google now has it, not the import response.
		if got, err := service.Events.Get(p.Spec["calendar"], created.Id).Context(ctx).Do(); err == nil {
			created = got
		}
		imported = append(imported, projectEvent(created, connectionID(call), p.Spec["calendar"]))
		refs = append(refs, eventRef(call, created))
	}
	return invoke.Result{Outcome: "confirmed", ProviderRefs: refs, Content: map[string]any{"summary": fmt.Sprintf("Imported %d events.", len(imported)), "events": imported}}
}
