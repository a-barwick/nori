package connections

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"nori/internal/state"
)

type Preferences struct {
	Revision              int64             `json:"revision"`
	Timezone              string            `json:"timezone"`
	Locale                string            `json:"locale,omitempty"`
	WorkingHours          WorkingHours      `json:"working_hours"`
	QuietHours            *QuietHours       `json:"quiet_hours,omitempty"`
	DefaultDestinationIDs []string          `json:"default_destination_ids"`
	AccountDefaults       map[string]string `json:"account_defaults"`
	Budgets               Budgets           `json:"budgets"`
	Input                 *InputPreferences `json:"input,omitempty"`
	OnboardingCompletedAt *time.Time        `json:"onboarding_completed_at,omitempty"`
	UpdatedAt             *time.Time        `json:"updated_at,omitempty"`
}

type WorkingHours []WorkingInterval

type WorkingInterval struct {
	Day   int    `json:"day"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type QuietHours struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type Budgets struct {
	TaskUSD    float64 `json:"task_usd"`
	MonthlyUSD float64 `json:"monthly_usd,omitempty"`
}

type InputPreferences struct {
	DictationLanguage string `json:"dictation_language,omitempty"`
}

type PreferencesEditable struct {
	Timezone              string            `json:"timezone"`
	Locale                string            `json:"locale,omitempty"`
	WorkingHours          WorkingHours      `json:"working_hours"`
	QuietHours            *QuietHours       `json:"quiet_hours,omitempty"`
	DefaultDestinationIDs []string          `json:"default_destination_ids"`
	AccountDefaults       map[string]string `json:"account_defaults"`
	Budgets               Budgets           `json:"budgets"`
	Input                 *InputPreferences `json:"input,omitempty"`
	OnboardingCompletedAt *time.Time        `json:"onboarding_completed_at,omitempty"`
}

var (
	DefaultTimezone   = "UTC"
	DefaultTaskBudget = 5.0
)

func defaultPreferences(zone string) Preferences {
	if _, err := time.LoadLocation(zone); zone == "" || err != nil {
		zone = DefaultTimezone
	}
	return Preferences{
		Timezone:              zone,
		WorkingHours:          WorkingHours{{1, "09:00", "17:00"}, {2, "09:00", "17:00"}, {3, "09:00", "17:00"}, {4, "09:00", "17:00"}, {5, "09:00", "17:00"}},
		DefaultDestinationIDs: []string{},
		AccountDefaults:       map[string]string{},
		Budgets:               Budgets{TaskUSD: DefaultTaskBudget},
	}
}

func (s *Service) Preferences(ctx context.Context) (Preferences, state.Resource, error) {
	r, err := s.Store.Resource(ctx, CollectionPreferences, PreferencesID)
	if errors.Is(err, state.ErrNotFound) {
		return defaultPreferences(s.UserTimezone), state.Resource{}, nil
	}
	if err != nil {
		return Preferences{}, r, err
	}
	var prefs Preferences
	if err = r.Decode(&prefs); err != nil {
		return Preferences{}, r, err
	}
	prefs.Revision = r.Revision
	updated := r.UpdatedAt
	prefs.UpdatedAt = &updated
	if prefs.AccountDefaults == nil {
		prefs.AccountDefaults = map[string]string{}
	}
	if prefs.WorkingHours == nil {
		prefs.WorkingHours = WorkingHours{}
	}
	if prefs.DefaultDestinationIDs == nil {
		prefs.DefaultDestinationIDs = []string{}
	}
	return prefs, r, nil
}

func (s *Service) SavePreferences(ctx context.Context, requestID string, expected int64, value PreferencesEditable) (state.MutationReceipt, error) {
	if _, err := time.LoadLocation(value.Timezone); err != nil || value.Budgets.TaskUSD <= 0 {
		return state.MutationReceipt{}, state.ErrInvalidResource
	}
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "PUT", Target: "/v1/preferences", ExpectedRevision: expected, Payload: value,
		Collection: CollectionPreferences, ID: PreferencesID,
		Apply: func(current *state.Resource, read func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			for purpose, id := range value.AccountDefaults {
				r, err := read(CollectionConnections, id)
				if err != nil {
					return state.MutationResult{}, err
				}
				if r == nil {
					return state.MutationResult{}, state.ErrInvalidResource
				}
				var record Record
				if err := r.Decode(&record); err != nil {
					return state.MutationResult{}, err
				}
				if !containsString(record.Capabilities, purpose) && record.Status == StatusConnected {
					return state.MutationResult{}, state.ErrInvalidResource
				}
			}
			revision := int64(1)
			onboardingCompletedAt := value.OnboardingCompletedAt
			if current != nil {
				revision = current.Revision + 1
				if onboardingCompletedAt == nil {
					var existing Preferences
					if err := current.Decode(&existing); err == nil {
						onboardingCompletedAt = existing.OnboardingCompletedAt
					}
				}
			}
			prefs := Preferences{
				Revision: revision, Timezone: value.Timezone, Locale: value.Locale, WorkingHours: value.WorkingHours,
				QuietHours: value.QuietHours, DefaultDestinationIDs: nonNil(value.DefaultDestinationIDs),
				AccountDefaults: value.AccountDefaults, Budgets: value.Budgets, Input: value.Input,
				OnboardingCompletedAt: onboardingCompletedAt,
			}
			if prefs.AccountDefaults == nil {
				prefs.AccountDefaults = map[string]string{}
			}
			r, err := state.NewResource(CollectionPreferences, PreferencesID, revision, prefs, nil, time.Now())
			if err != nil {
				return state.MutationResult{}, err
			}
			now := time.Now().UTC()
			prefs.UpdatedAt = &now
			data, _ := json.Marshal(prefs)
			return state.MutationResult{Writes: []state.Resource{r}, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200}}, nil
		},
	})
}

func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
