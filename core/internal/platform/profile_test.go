package platform

import (
	"context"
	"encoding/json"
	"testing"

	"nori/internal/connections"
	"nori/internal/secret"
	"nori/internal/sim/memstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestProfileIncludesPersonalizationWhenSet(t *testing.T) {
	ctx := context.Background()
	user := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "owner@example.com",
		DisplayName: "Austin",
		TimeZone:    "America/Los_Angeles",
	}
	store := records.NewShared(memstore.New(), user, 1)
	conns := connections.New(store, secret.Unavailable{}, "https://nori.test/callback")
	conns.UserTimezone = user.TimeZone

	profile := Profile(user, conns)

	// Without personalization, matches user.FactsIn(zone).
	plainFacts, loc := profile(ctx)
	if loc.String() != "America/Los_Angeles" {
		t.Fatalf("location = %q, want America/Los_Angeles", loc.String())
	}
	if plainFacts != user.FactsIn("America/Los_Angeles") {
		t.Fatalf("plain facts mismatch:\ngot:  %s\nwant: %s", plainFacts, user.FactsIn("America/Los_Angeles"))
	}

	// With personalization saved, facts include structured personalization fields.
	_, err := conns.SavePreferences(ctx, "req-prof-1", 0, connections.PreferencesEditable{
		Timezone:              "America/Los_Angeles",
		WorkingHours:          connections.WorkingHours{},
		DefaultDestinationIDs: []string{},
		AccountDefaults:       map[string]string{},
		Budgets:               connections.Budgets{TaskUSD: 5.0},
		Personalization: &connections.PersonalizationPreferences{
			Name:               "A",
			ReplyStyle:         "Bullets first",
			CustomInstructions: "Keep answers short.",
			Focus:              []string{"Engineering"},
			WorkingOn:          "Nori iOS",
		},
	})
	if err != nil {
		t.Fatalf("SavePreferences: %v", err)
	}

	richFacts, _ := profile(ctx)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(richFacts), &decoded); err != nil {
		t.Fatalf("unmarshal richFacts: %v", err)
	}
	if decoded["preferred_name"] != "A" ||
		decoded["reply_style"] != "Bullets first" ||
		decoded["custom_instructions"] != "Keep answers short." ||
		decoded["working_on"] != "Nori iOS" {
		t.Fatalf("unexpected decoded personalization facts: %#v", decoded)
	}
	focus, ok := decoded["focus"].([]any)
	if !ok || len(focus) != 1 || focus[0] != "Engineering" {
		t.Fatalf("unexpected decoded focus: %#v", decoded["focus"])
	}
}
