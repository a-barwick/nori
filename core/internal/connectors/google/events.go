package google

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
	"nori/internal/connections"
	"nori/internal/scheduler"
)

// EventSource reads one event's start, revision and status for event-relative reminders.
type EventSource struct{ Connections *connections.Service }

// EventStart reads the event named by provider_id "<calendar_id>/<event_id>" (or just an event ID on the primary
// calendar). It reads that event's time and status only.
func (e EventSource) EventStart(ctx context.Context, ref scheduler.ResourceRef) (time.Time, string, string, error) {
	grant, err := e.Connections.Grant(ctx, ref.ConnectionID)
	if err != nil {
		return time.Time{}, "", "", err
	}
	service, err := calendar.NewService(ctx, option.WithHTTPClient(grant.Client))
	if err != nil {
		return time.Time{}, "", "", err
	}
	calendarID, eventID := "primary", ref.ProviderID
	if before, after, ok := strings.Cut(ref.ProviderID, "/"); ok {
		calendarID, eventID = before, after
	}
	event, err := service.Events.Get(calendarID, eventID).Context(ctx).Do()
	if err != nil {
		return time.Time{}, "", "", classify(err)
	}
	if event.Start == nil {
		return time.Time{}, "", "", fmt.Errorf("the event has no start")
	}
	var start time.Time
	if event.Start.DateTime != "" {
		start, err = parseTime(event.Start.DateTime)
	} else {
		start, err = time.Parse(time.DateOnly, event.Start.Date)
	}
	return start, event.Etag, event.Status, err
}
