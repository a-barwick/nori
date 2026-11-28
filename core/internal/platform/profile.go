package platform

import (
	"context"
	"encoding/json"
	"time"

	"nori/internal/connections"
	"nori/internal/users"
)

func Profile(user users.User, conns *connections.Service) users.Profile {
	return func(ctx context.Context) (string, *time.Location) {
		zone := user.TimeZone
		var personalization *connections.PersonalizationPreferences
		if conns != nil {
			if prefs, _, err := conns.Preferences(ctx); err == nil {
				if prefs.Timezone != "" {
					zone = prefs.Timezone
				}
				personalization = prefs.Personalization
			}
		}
		location, err := time.LoadLocation(zone)
		if err != nil {
			zone, location = "UTC", time.UTC
		}
		if personalization == nil || personalization.IsEmpty() {
			return user.FactsIn(zone), location
		}
		facts := map[string]any{
			"display_name": user.DisplayName,
			"nori_sign_in": user.Email,
			"time_zone":    zone,
			"note":         `nori_sign_in is the address the user signs in to Nori with, not their mailbox; for "email me" use the connected mail account's own address`,
		}
		if personalization.Name != "" {
			facts["preferred_name"] = personalization.Name
		}
		if personalization.ReplyStyle != "" {
			facts["reply_style"] = personalization.ReplyStyle
		}
		if personalization.CustomInstructions != "" {
			facts["custom_instructions"] = personalization.CustomInstructions
		}
		if len(personalization.Focus) > 0 {
			facts["focus"] = personalization.Focus
		}
		if personalization.WorkingOn != "" {
			facts["working_on"] = personalization.WorkingOn
		}
		b, _ := json.Marshal(facts)
		return string(b), location
	}
}
