package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"nori/internal/connections"
	"nori/internal/state"
)

// ParseHHMM parses a 24-hour "HH:MM" clock string.
func ParseHHMM(hhmm string) (hour, min int, ok bool) {
	hhmm = strings.TrimSpace(hhmm)
	parts := strings.Split(hhmm, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, 0, false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// InQuietHours reports whether now falls inside the user's quiet hours window
// in the given IANA timezone. It handles midnight wrap (e.g. 21:00–07:30) and
// DST spring-forward/fall-back transitions cleanly using LocalTime.
// When inQuiet is true, quietEnd is the exact UTC instant when quiet hours end.
func InQuietHours(now time.Time, q connections.QuietHours, zone string) (inQuiet bool, quietEnd time.Time) {
	if !q.IsEnabled() {
		return false, time.Time{}
	}
	startH, startM, ok1 := ParseHHMM(q.Start)
	endH, endM, ok2 := ParseHHMM(q.End)
	if !ok1 || !ok2 || (startH == endH && startM == endM) {
		return false, time.Time{}
	}
	loc, err := time.LoadLocation(strings.TrimSpace(zone))
	if err != nil || loc == nil {
		loc = time.UTC
	}
	localNow := now.In(loc)
	y, mo, d := localNow.Year(), localNow.Month(), localNow.Day()

	startMins := startH*60 + startM
	endMins := endH*60 + endM

	checkWindow := func(startDay, endDay time.Time) (bool, time.Time) {
		ws, _ := LocalTime(startDay.Year(), startDay.Month(), startDay.Day(), startH, startM, 0, loc)
		we, _ := LocalTime(endDay.Year(), endDay.Month(), endDay.Day(), endH, endM, 0, loc)
		if !we.After(ws) {
			return false, time.Time{}
		}
		if !now.Before(ws) && now.Before(we) {
			return true, we.UTC()
		}
		return false, time.Time{}
	}

	today := time.Date(y, mo, d, 12, 0, 0, 0, time.UTC)
	if startMins < endMins {
		// Same-day window (e.g. 13:00–15:00).
		return checkWindow(today, today)
	}
	// Midnight-wrapping window (e.g. 21:00–07:30).
	// Check yesterday->today first, then today->tomorrow.
	yesterday := today.AddDate(0, 0, -1)
	if ok, end := checkWindow(yesterday, today); ok {
		return true, end
	}
	tomorrow := today.AddDate(0, 0, 1)
	return checkWindow(today, tomorrow)
}

func (s *Service) loadPreferences(ctx context.Context) (connections.Preferences, bool) {
	if s.Preferences != nil {
		prefs, err := s.Preferences(ctx)
		if err == nil {
			return prefs, true
		}
	}
	if s.Store != nil {
		r, err := s.Store.Resource(ctx, connections.CollectionPreferences, connections.PreferencesID)
		if err == nil {
			var prefs connections.Preferences
			if r.Decode(&prefs) == nil {
				return prefs, true
			}
		}
	}
	return connections.Preferences{}, false
}

func (s *Service) userTimezone(ctx context.Context) string {
	if prefs, ok := s.loadPreferences(ctx); ok && strings.TrimSpace(prefs.Timezone) != "" {
		return strings.TrimSpace(prefs.Timezone)
	}
	if s.Timezone != nil {
		if z := strings.TrimSpace(s.Timezone(ctx)); z != "" {
			return z
		}
	}
	return "UTC"
}

func (s *Service) quietHoursCheck(ctx context.Context, now time.Time) (bool, time.Time) {
	prefs, ok := s.loadPreferences(ctx)
	if !ok {
		return false, time.Time{}
	}
	q := prefs.EffectiveQuietHours()
	zone := strings.TrimSpace(prefs.Timezone)
	if zone == "" {
		zone = s.userTimezone(ctx)
	}
	return InQuietHours(now, q, zone)
}

func (s *Service) heldDeliveryResources(ctx context.Context) ([]state.Resource, []Delivery, error) {
	resources, err := s.allResources(ctx, CollectionDeliveries, map[string]string{"state": DeliveryStateHeld}, 200)
	if err != nil {
		return nil, nil, err
	}
	deliveries := make([]Delivery, 0, len(resources))
	validRes := make([]state.Resource, 0, len(resources))
	for _, r := range resources {
		var d Delivery
		if err := r.Decode(&d); err != nil {
			continue
		}
		validRes = append(validRes, r)
		deliveries = append(deliveries, d)
	}
	return validRes, deliveries, nil
}

// shouldFlushHeldAtQuietEnd returns true when held deliveries should be flushed
// directly at quiet_end instead of waiting for the Morning Brief to fold them.
// That happens when the Morning Brief is disabled, paused/cancelled/missing, or
// its most recent occurrence for today failed/expired/skipped.
func (s *Service) shouldFlushHeldAtQuietEnd(ctx context.Context, now time.Time) bool {
	if prefs, ok := s.loadPreferences(ctx); ok {
		pro := prefs.EffectiveProactivity()
		if !pro.IsMorningBriefEnabled() {
			return true
		}
	}
	r, err := s.Store.Resource(ctx, CollectionAutomations, MorningBriefAutomationID)
	if err != nil {
		return true
	}
	brief, err := decodeAutomation(r)
	if err != nil || brief.Status != AutomationEnabled {
		return true
	}
	// Check today's Morning Brief occurrence state.
	if brief.LastOccurrenceID != "" {
		if occ, err := s.Occurrence(ctx, brief.LastOccurrenceID); err == nil {
			// If a Morning Brief occurrence is currently in-flight, keep held
			// deliveries waiting so the brief can fold them on completion.
			if occ.Status == OccurrencePending || occ.Status == OccurrenceAccepted || occ.Status == OccurrenceRunning {
				return false
			}
			loc, err := time.LoadLocation(s.userTimezone(ctx))
			if err != nil {
				loc = time.UTC
			}
			nowLocal := now.In(loc)
			occLocal := occ.ScheduledAt.In(loc)
			sameDay := nowLocal.Year() == occLocal.Year() && nowLocal.YearDay() == occLocal.YearDay()
			if sameDay && (occ.Status == OccurrenceFailed || occ.Status == OccurrenceExpired || occ.Status == OccurrenceSkipped || occ.Status == OccurrenceCancelled) {
				return true
			}
		}
	}
	// If the next scheduled brief is more than 12 hours away (e.g. tomorrow's brief),
	// today's brief window has already passed, so flush.
	if brief.NextAt != nil && brief.NextAt.Sub(now) > 12*time.Hour {
		return true
	}
	return false
}

// flushHeldDeliveries delivers any held deliveries whose ScheduledFor <= now
// when Morning Brief is disabled/failed (or when force is true).
func (s *Service) flushHeldDeliveries(ctx context.Context, now time.Time, force bool) error {
	resources, deliveries, err := s.heldDeliveryResources(ctx)
	if err != nil {
		return err
	}
	if len(deliveries) == 0 {
		return nil
	}
	if !force && !s.shouldFlushHeldAtQuietEnd(ctx, now) {
		return nil
	}
	for i, d := range deliveries {
		if !force && (d.ScheduledFor == nil || d.ScheduledFor.After(now)) {
			continue
		}
		updated := d
		if err := s.dispatchDelivery(ctx, &updated, now); err != nil {
			slog.WarnContext(ctx, "scheduler flush held delivery failed", "delivery_id", d.ID, "err", err)
			continue
		}
		updated.UpdatedAt = &now
		nextRes, err := state.NewResource(CollectionDeliveries, updated.ID, 0, updated, map[string]string{"occurrence_id": updated.OccurrenceID, "state": updated.State}, now)
		if err != nil {
			return err
		}
		if _, err := s.Store.PutResource(ctx, nextRes, resources[i].Revision); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
			return err
		}
	}
	return nil
}
