package proactive

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"nori/internal/agent"
	"nori/internal/state"
)

const (
	// Buffer is the time beyond the drive a pair of events needs, for parking and walking in.
	Buffer = 10 * time.Minute
	// maxGap is the longest gap worth asking about. Anything longer has room for any local drive.
	maxGap = 3 * time.Hour
	// maxRouteCalls bounds the Routes requests one check makes.
	maxRouteCalls = 20
)

// virtual places have no address to drive to.
var virtual = []string{"http://", "https://", "zoom", "meet.google", "teams.microsoft", "webex", "online", "virtual", "phone call"}

func place(location string) string {
	text := strings.Join(strings.Fields(strings.ToLower(location)), " ")
	for _, word := range virtual {
		if strings.Contains(text, word) {
			return ""
		}
	}
	return text
}

type timed struct {
	event      agent.CalendarEvent
	start, end time.Time
}

// Conflicts finds each pair of consecutive timed events at different addresses whose gap is shorter than the drive
// between them plus Buffer. The second event must start after now. Pairs whose drive couldn't be read are reported in
// skipped, and never as conflicts.
func Conflicts(ctx context.Context, events []agent.CalendarEvent, router Router, now time.Time) (found []state.TravelFinding, skipped []string, err error) {
	list := []timed{}
	for _, e := range events {
		if e.AllDay {
			continue
		}
		start, err1 := time.Parse(time.RFC3339, e.Start)
		end, err2 := time.Parse(time.RFC3339, e.End)
		if err1 != nil || err2 != nil || !end.After(start) {
			continue
		}
		list = append(list, timed{e, start, end})
	}
	calls := 0
	for i, first := range list {
		// The next event is the first one starting at or after this one ends. Overlapping events aren't travel.
		var second *timed
		for j := i + 1; j < len(list); j++ {
			if !list[j].start.Before(first.end) {
				second = &list[j]
				break
			}
		}
		if second == nil || !second.start.After(now) {
			continue
		}
		// Another event during the gap, such as an all-afternoon block, means the drive may not be between these two.
		if busyBetween(list, first, *second) {
			continue
		}
		gap := second.start.Sub(first.end)
		from, to := place(first.event.Location), place(second.event.Location)
		if gap >= maxGap || from == "" || to == "" || from == to {
			continue
		}
		if calls >= maxRouteCalls {
			skipped = append(skipped, "route limit reached")
			break
		}
		calls++
		depart := first.end
		if depart.Before(now) {
			depart = now
		}
		drive, err := router.DriveTime(ctx, first.event.Location, second.event.Location, depart)
		if ctx.Err() != nil {
			return found, skipped, ctx.Err()
		}
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s → %s: %v", first.event.EventID, second.event.EventID, err))
			continue
		}
		if gap >= drive+Buffer {
			continue
		}
		found = append(found, state.TravelFinding{
			First:      state.OfferEvent{ID: first.event.EventID, Title: first.event.Title, Start: first.start, End: first.end, Location: first.event.Location},
			Second:     state.OfferEvent{ID: second.event.EventID, Title: second.event.Title, Start: second.start, End: second.end, Location: second.event.Location},
			GapMinutes: int(gap.Minutes()), DriveMinutes: int(math.Ceil(drive.Minutes())),
		})
	}
	return found, skipped, nil
}

// busyBetween reports whether any event other than first and second overlaps the time from first's end to second's
// start.
func busyBetween(list []timed, first, second timed) bool {
	for _, e := range list {
		if e.event.EventID == first.event.EventID || e.event.EventID == second.event.EventID {
			continue
		}
		if e.start.Before(second.start) && e.end.After(first.end) {
			return true
		}
	}
	return false
}
