package connections

import (
	"context"
	"errors"
	"testing"

	"nori/internal/secret"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func TestPreferencesProactivityDefaultsAndNilPreservation(t *testing.T) {
	ctx := context.Background()
	store := records.NewShared(memstore.New(), users.User{
		ID:       "11111111-1111-4111-8111-111111111111",
		Email:    "owner@example.com",
		TimeZone: "America/Los_Angeles",
	}, 1)
	svc := New(store, secret.Unavailable{}, "https://nori.test/callback")

	// 1. Default read populates quiet hours (21:00–07:30 enabled) and proactivity defaults.
	prefs, _, err := svc.Preferences(ctx)
	if err != nil {
		t.Fatalf("Preferences: %v", err)
	}
	if prefs.QuietHours == nil || !prefs.QuietHours.IsEnabled() || prefs.QuietHours.Start != DefaultQuietHoursStart || prefs.QuietHours.End != DefaultQuietHoursEnd {
		t.Fatalf("unexpected default QuietHours: %+v", prefs.QuietHours)
	}
	if prefs.Proactivity == nil || prefs.Proactivity.PushPolicy != PushPolicyUrgentAndApprovals || prefs.Proactivity.MorningBriefTime != DefaultMorningBriefTime || prefs.Proactivity.DailyProactiveBudgetUSD != DefaultDailyProactiveBudgetUSD {
		t.Fatalf("unexpected default Proactivity: %+v", prefs.Proactivity)
	}

	// 2. Save explicit proactivity + quiet hours.
	disabled := false
	_, err = svc.SavePreferences(ctx, "req-1", 0, PreferencesEditable{
		Timezone:              "America/Los_Angeles",
		WorkingHours:          WorkingHours{},
		QuietHours:            &QuietHours{Enabled: &disabled, Start: "22:00", End: "07:00"},
		DefaultDestinationIDs: []string{},
		AccountDefaults:       map[string]string{},
		Budgets:               Budgets{TaskUSD: 5.0},
		Proactivity: &ProactivityPreferences{
			PushPolicy:              PushPolicyIncludeBrief,
			MorningBriefTime:        "07:45",
			DailyProactiveBudgetUSD: 2.5,
		},
	})
	if err != nil {
		t.Fatalf("SavePreferences(1): %v", err)
	}

	// 3. Older client saves PreferencesEditable with Proactivity == nil and QuietHours == nil.
	// Existing values must be preserved.
	_, err = svc.SavePreferences(ctx, "req-2", 1, PreferencesEditable{
		Timezone:              "America/New_York",
		WorkingHours:          WorkingHours{},
		DefaultDestinationIDs: []string{},
		AccountDefaults:       map[string]string{},
		Budgets:               Budgets{TaskUSD: 5.0},
	})
	if err != nil {
		t.Fatalf("SavePreferences(2): %v", err)
	}

	after, _, err := svc.Preferences(ctx)
	if err != nil {
		t.Fatalf("Preferences(after): %v", err)
	}
	if after.Timezone != "America/New_York" {
		t.Fatalf("timezone = %q, want America/New_York", after.Timezone)
	}
	if after.QuietHours == nil || after.QuietHours.IsEnabled() || after.QuietHours.Start != "22:00" || after.QuietHours.End != "07:00" {
		t.Fatalf("expected QuietHours to be preserved, got %+v", after.QuietHours)
	}
	if after.Proactivity == nil || after.Proactivity.PushPolicy != PushPolicyIncludeBrief || after.Proactivity.MorningBriefTime != "07:45" || after.Proactivity.DailyProactiveBudgetUSD != 2.5 {
		t.Fatalf("expected Proactivity to be preserved, got %+v", after.Proactivity)
	}

	// 4. Invalid push policy / time rejected.
	_, err = svc.SavePreferences(ctx, "req-3", after.Revision, PreferencesEditable{
		Timezone:              "America/New_York",
		WorkingHours:          WorkingHours{},
		DefaultDestinationIDs: []string{},
		AccountDefaults:       map[string]string{},
		Budgets:               Budgets{TaskUSD: 5.0},
		Proactivity:           &ProactivityPreferences{PushPolicy: "bogus"},
	})
	if !errors.Is(err, state.ErrInvalidResource) {
		t.Fatalf("expected ErrInvalidResource for bogus push_policy, got %v", err)
	}
}
