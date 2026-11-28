package connections

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"nori/internal/state"
)

const (
	PushPolicyUrgentAndApprovals = "urgent_and_approvals"
	PushPolicyIncludeBrief       = "include_brief"
	PushPolicyQuietOnly          = "quiet_only"

	DefaultQuietHoursStart         = "21:00"
	DefaultQuietHoursEnd           = "07:30"
	DefaultMorningBriefTime        = "08:00"
	DefaultDailyProactiveBudgetUSD = 1.0
)

var hhmmRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

type Preferences struct {
	Revision              int64                       `json:"revision"`
	Timezone              string                      `json:"timezone"`
	Locale                string                      `json:"locale,omitempty"`
	WorkingHours          WorkingHours                `json:"working_hours"`
	QuietHours            *QuietHours                 `json:"quiet_hours,omitempty"`
	DefaultDestinationIDs []string                    `json:"default_destination_ids"`
	AccountDefaults       map[string]string           `json:"account_defaults"`
	Budgets               Budgets                     `json:"budgets"`
	Input                 *InputPreferences           `json:"input,omitempty"`
	Proactivity           *ProactivityPreferences     `json:"proactivity,omitempty"`
	Personalization       *PersonalizationPreferences `json:"personalization,omitempty"`
	OnboardingCompletedAt *time.Time                  `json:"onboarding_completed_at,omitempty"`
	DataResetAt           string                      `json:"data_reset_at,omitempty"`
	DataResetPending      bool                        `json:"data_reset_pending,omitempty"`
	UpdatedAt             *time.Time                  `json:"updated_at,omitempty"`
}

type WorkingHours []WorkingInterval

type WorkingInterval struct {
	Day   int    `json:"day"`
	Start string `json:"start"`
	End   string `json:"end"`
}

type QuietHours struct {
	Enabled *bool  `json:"enabled,omitempty"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

// IsEnabled reports whether quiet hours are enabled (defaults to true when Enabled is nil).
func (q *QuietHours) IsEnabled() bool {
	if q == nil || q.Enabled == nil {
		return true
	}
	return *q.Enabled
}

type ProactivityPreferences struct {
	PushPolicy              string  `json:"push_policy,omitempty"`
	PushChats               *bool   `json:"push_chats,omitempty"`
	PushUrgentAndApprovals  *bool   `json:"push_urgent_and_approvals,omitempty"`
	PushMorningBrief        *bool   `json:"push_morning_brief,omitempty"`
	MorningBriefTime        string  `json:"morning_brief_time,omitempty"`
	MorningBriefEnabled     *bool   `json:"morning_brief_enabled,omitempty"`
	DailyProactiveBudgetUSD float64 `json:"daily_proactive_budget_usd,omitempty"`
}

// IsPushChatsEnabled reports whether chat & background work completion pushes are enabled (defaults to true unless legacy push_policy is quiet_only).
func (p *ProactivityPreferences) IsPushChatsEnabled() bool {
	if p == nil {
		return true
	}
	if p.PushChats != nil {
		return *p.PushChats
	}
	return p.PushPolicy != PushPolicyQuietOnly
}

// IsPushUrgentAndApprovalsEnabled reports whether urgent alert & approval pushes are enabled (defaults to true unless legacy push_policy is quiet_only).
func (p *ProactivityPreferences) IsPushUrgentAndApprovalsEnabled() bool {
	if p == nil {
		return true
	}
	if p.PushUrgentAndApprovals != nil {
		return *p.PushUrgentAndApprovals
	}
	return p.PushPolicy != PushPolicyQuietOnly
}

// IsPushMorningBriefEnabled reports whether morning brief pushes are enabled (defaults to true when legacy push_policy is include_brief, false otherwise).
func (p *ProactivityPreferences) IsPushMorningBriefEnabled() bool {
	if p == nil {
		return false
	}
	if p.PushMorningBrief != nil {
		return *p.PushMorningBrief
	}
	return p.PushPolicy == PushPolicyIncludeBrief
}

// IsMorningBriefEnabled reports whether the Morning Brief is enabled (defaults to true when nil).
func (p *ProactivityPreferences) IsMorningBriefEnabled() bool {
	if p == nil || p.MorningBriefEnabled == nil {
		return true
	}
	return *p.MorningBriefEnabled
}

type Budgets struct {
	TaskUSD    float64 `json:"task_usd"`
	MonthlyUSD float64 `json:"monthly_usd,omitempty"`
}

type InputPreferences struct {
	DictationLanguage string `json:"dictation_language,omitempty"`
}

type PersonalizationPreferences struct {
	Name               string   `json:"name,omitempty"`
	ReplyStyle         string   `json:"reply_style,omitempty"`
	CustomInstructions string   `json:"custom_instructions,omitempty"`
	Focus              []string `json:"focus,omitempty"`
	WorkingOn          string   `json:"working_on,omitempty"`
}

func (p *PersonalizationPreferences) IsEmpty() bool {
	if p == nil {
		return true
	}
	return strings.TrimSpace(p.Name) == "" &&
		strings.TrimSpace(p.ReplyStyle) == "" &&
		strings.TrimSpace(p.CustomInstructions) == "" &&
		len(p.Focus) == 0 &&
		strings.TrimSpace(p.WorkingOn) == ""
}

type PreferencesEditable struct {
	Timezone              string                      `json:"timezone"`
	Locale                string                      `json:"locale,omitempty"`
	WorkingHours          WorkingHours                `json:"working_hours"`
	QuietHours            *QuietHours                 `json:"quiet_hours,omitempty"`
	DefaultDestinationIDs []string                    `json:"default_destination_ids"`
	AccountDefaults       map[string]string           `json:"account_defaults"`
	Budgets               Budgets                     `json:"budgets"`
	Input                 *InputPreferences           `json:"input,omitempty"`
	Proactivity           *ProactivityPreferences     `json:"proactivity,omitempty"`
	Personalization       *PersonalizationPreferences `json:"personalization,omitempty"`
	OnboardingCompletedAt *time.Time                  `json:"onboarding_completed_at,omitempty"`
}

var (
	DefaultTimezone   = "UTC"
	DefaultTaskBudget = 5.0
)

func boolPtr(v bool) *bool { return &v }

func defaultQuietHours() *QuietHours {
	return &QuietHours{
		Enabled: boolPtr(true),
		Start:   DefaultQuietHoursStart,
		End:     DefaultQuietHoursEnd,
	}
}

func defaultProactivityPreferences() *ProactivityPreferences {
	return &ProactivityPreferences{
		PushPolicy:              PushPolicyUrgentAndApprovals,
		PushChats:               boolPtr(true),
		PushUrgentAndApprovals:  boolPtr(true),
		PushMorningBrief:        boolPtr(false),
		MorningBriefTime:        DefaultMorningBriefTime,
		MorningBriefEnabled:     boolPtr(true),
		DailyProactiveBudgetUSD: DefaultDailyProactiveBudgetUSD,
	}
}

func normalizeProactivityPushFlags(p *ProactivityPreferences) {
	if p.PushPolicy == "" {
		p.PushPolicy = PushPolicyUrgentAndApprovals
	}
	if p.PushChats == nil {
		p.PushChats = boolPtr(p.PushPolicy != PushPolicyQuietOnly)
	}
	if p.PushUrgentAndApprovals == nil {
		p.PushUrgentAndApprovals = boolPtr(p.PushPolicy != PushPolicyQuietOnly)
	}
	if p.PushMorningBrief == nil {
		p.PushMorningBrief = boolPtr(p.PushPolicy == PushPolicyIncludeBrief)
	}
}

// EffectiveQuietHours returns quiet hours with read-time defaults applied.
func (p Preferences) EffectiveQuietHours() QuietHours {
	if p.QuietHours == nil {
		return *defaultQuietHours()
	}
	q := *p.QuietHours
	if q.Enabled == nil {
		q.Enabled = boolPtr(true)
	}
	if q.Start == "" {
		q.Start = DefaultQuietHoursStart
	}
	if q.End == "" {
		q.End = DefaultQuietHoursEnd
	}
	return q
}

// EffectiveProactivity returns proactivity preferences with read-time defaults applied.
func (p Preferences) EffectiveProactivity() ProactivityPreferences {
	if p.Proactivity == nil {
		return *defaultProactivityPreferences()
	}
	out := *p.Proactivity
	normalizeProactivityPushFlags(&out)
	if out.MorningBriefTime == "" {
		out.MorningBriefTime = DefaultMorningBriefTime
	}
	if out.MorningBriefEnabled == nil {
		out.MorningBriefEnabled = boolPtr(true)
	}
	if out.DailyProactiveBudgetUSD <= 0 {
		out.DailyProactiveBudgetUSD = DefaultDailyProactiveBudgetUSD
	}
	return out
}

func applyReadDefaults(prefs *Preferences) {
	if prefs.AccountDefaults == nil {
		prefs.AccountDefaults = map[string]string{}
	}
	if prefs.WorkingHours == nil {
		prefs.WorkingHours = WorkingHours{}
	}
	if prefs.DefaultDestinationIDs == nil {
		prefs.DefaultDestinationIDs = []string{}
	}
	if prefs.QuietHours == nil {
		prefs.QuietHours = defaultQuietHours()
	} else {
		if prefs.QuietHours.Enabled == nil {
			prefs.QuietHours.Enabled = boolPtr(true)
		}
		if prefs.QuietHours.Start == "" {
			prefs.QuietHours.Start = DefaultQuietHoursStart
		}
		if prefs.QuietHours.End == "" {
			prefs.QuietHours.End = DefaultQuietHoursEnd
		}
	}
	if prefs.Proactivity == nil {
		prefs.Proactivity = defaultProactivityPreferences()
	} else {
		normalizeProactivityPushFlags(prefs.Proactivity)
		if prefs.Proactivity.MorningBriefTime == "" {
			prefs.Proactivity.MorningBriefTime = DefaultMorningBriefTime
		}
		if prefs.Proactivity.MorningBriefEnabled == nil {
			prefs.Proactivity.MorningBriefEnabled = boolPtr(true)
		}
		if prefs.Proactivity.DailyProactiveBudgetUSD <= 0 {
			prefs.Proactivity.DailyProactiveBudgetUSD = DefaultDailyProactiveBudgetUSD
		}
	}
}

func validateProactivity(p *ProactivityPreferences) bool {
	if p == nil {
		return true
	}
	if p.PushPolicy != "" {
		switch p.PushPolicy {
		case PushPolicyUrgentAndApprovals, PushPolicyIncludeBrief, PushPolicyQuietOnly:
		default:
			return false
		}
	}
	if p.MorningBriefTime != "" && !hhmmRe.MatchString(p.MorningBriefTime) {
		return false
	}
	if p.DailyProactiveBudgetUSD < 0 {
		return false
	}
	return true
}

func validateQuietHours(q *QuietHours) bool {
	if q == nil {
		return true
	}
	return hhmmRe.MatchString(q.Start) && hhmmRe.MatchString(q.End)
}

func validatePersonalization(p *PersonalizationPreferences) bool {
	if p == nil {
		return true
	}
	if utf8.RuneCountInString(p.Name) > 120 ||
		utf8.RuneCountInString(p.ReplyStyle) > 120 ||
		utf8.RuneCountInString(p.CustomInstructions) > 4000 ||
		utf8.RuneCountInString(p.WorkingOn) > 240 ||
		len(p.Focus) > 20 {
		return false
	}
	for _, item := range p.Focus {
		if utf8.RuneCountInString(item) > 80 {
			return false
		}
	}
	return true
}

func normalizePersonalization(p *PersonalizationPreferences) *PersonalizationPreferences {
	if p == nil {
		return nil
	}
	out := &PersonalizationPreferences{
		Name:               strings.TrimSpace(p.Name),
		ReplyStyle:         strings.TrimSpace(p.ReplyStyle),
		CustomInstructions: strings.TrimSpace(p.CustomInstructions),
		WorkingOn:          strings.TrimSpace(p.WorkingOn),
	}
	if len(p.Focus) > 0 {
		seen := make(map[string]struct{}, len(p.Focus))
		for _, item := range p.Focus {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			if _, ok := seen[trimmed]; ok {
				continue
			}
			seen[trimmed] = struct{}{}
			out.Focus = append(out.Focus, trimmed)
		}
	}
	if out.IsEmpty() {
		return nil
	}
	return out
}

func defaultPreferences(zone string) Preferences {
	if _, err := time.LoadLocation(zone); zone == "" || err != nil {
		zone = DefaultTimezone
	}
	return Preferences{
		Timezone:              zone,
		WorkingHours:          WorkingHours{{1, "09:00", "17:00"}, {2, "09:00", "17:00"}, {3, "09:00", "17:00"}, {4, "09:00", "17:00"}, {5, "09:00", "17:00"}},
		QuietHours:            defaultQuietHours(),
		DefaultDestinationIDs: []string{},
		AccountDefaults:       map[string]string{},
		Budgets:               Budgets{TaskUSD: DefaultTaskBudget},
		Proactivity:           defaultProactivityPreferences(),
	}
}

func (s *Service) Preferences(ctx context.Context) (Preferences, state.Resource, error) {
	r, err := s.Store.Resource(ctx, CollectionPreferences, PreferencesID)
	if errors.Is(err, state.ErrNotFound) {
		prefs := defaultPreferences(s.UserTimezone)
		prefs.DataResetAt = s.DataResetAt
		return prefs, state.Resource{}, nil
	}
	if err != nil {
		return Preferences{}, r, err
	}
	var prefs Preferences
	if err = r.Decode(&prefs); err != nil {
		return Preferences{}, r, err
	}
	prefs.Revision = r.Revision
	prefs.DataResetAt = s.DataResetAt
	updated := r.UpdatedAt
	prefs.UpdatedAt = &updated
	applyReadDefaults(&prefs)
	return prefs, r, nil
}

func (s *Service) SavePreferences(ctx context.Context, requestID string, expected int64, value PreferencesEditable) (state.MutationReceipt, error) {
	if _, err := time.LoadLocation(value.Timezone); err != nil || value.Budgets.TaskUSD <= 0 {
		return state.MutationReceipt{}, state.ErrInvalidResource
	}
	if !validateQuietHours(value.QuietHours) || !validateProactivity(value.Proactivity) || !validatePersonalization(value.Personalization) {
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
			quietHours := value.QuietHours
			proactivity := value.Proactivity
			personalization := value.Personalization
			personalizationExplicit := value.Personalization != nil
			if current != nil {
				revision = current.Revision + 1
				var existing Preferences
				if err := current.Decode(&existing); err == nil {
					if onboardingCompletedAt == nil {
						onboardingCompletedAt = existing.OnboardingCompletedAt
					}
					if quietHours == nil {
						quietHours = existing.QuietHours
					}
					if proactivity == nil {
						proactivity = existing.Proactivity
					}
					if !personalizationExplicit {
						personalization = existing.Personalization
					}
				}
			}
			if personalizationExplicit {
				personalization = normalizePersonalization(personalization)
			}
			prefs := Preferences{
				Revision: revision, Timezone: value.Timezone, Locale: value.Locale, WorkingHours: value.WorkingHours,
				QuietHours: quietHours, DefaultDestinationIDs: nonNil(value.DefaultDestinationIDs),
				AccountDefaults: value.AccountDefaults, Budgets: value.Budgets, Input: value.Input,
				Proactivity:           proactivity,
				Personalization:       personalization,
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
			prefs.DataResetAt = s.DataResetAt
			prefs.UpdatedAt = &now
			applyReadDefaults(&prefs)
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
