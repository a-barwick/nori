package platform

import (
	"context"
	"time"

	"nori/internal/connections"
	"nori/internal/users"
)

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
