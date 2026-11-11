package platform

import (
	"context"
	"time"

	"nori/internal/connections"
	"nori/internal/users"
)

// Profile reads the user's time zone from their preferences at the time of use, so a change in Settings reaches the
// next prompt without restarting the process that holds the user (D-054). Preferences not saved yet, or unreadable,
// fall back to the zone on the user row.
func Profile(user users.User, conns *connections.Service) users.Profile {
	return func(ctx context.Context) (string, *time.Location) {
		zone := user.TimeZone
		if conns != nil {
			if prefs, _, err := conns.Preferences(ctx); err == nil && prefs.Timezone != "" {
				zone = prefs.Timezone
			}
		}
		location, err := time.LoadLocation(zone)
		if err != nil {
			zone, location = "UTC", time.UTC
		}
		return user.FactsIn(zone), location
	}
}
