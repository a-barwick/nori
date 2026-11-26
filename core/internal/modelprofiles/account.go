package modelprofiles

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"nori/internal/modelkeys"
	"nori/internal/state"
)

type Account struct {
	Keys  *modelkeys.Service
	Store state.ResourceStore
}

type AccountView struct {
	ModelProfiles   PublicCatalog                   `json:"model_profiles"`
	Selection       state.ModelSelection            `json:"selection"`
	Revision        int64                           `json:"revision"`
	Recommendations map[string]state.ModelSelection `json:"recommendations"`
}

type family struct {
	provider, id, label, direct, routed string
	efforts                             []string
}

var paidEfforts = []string{"low", "medium", "high", "xhigh", "max"}
var optionalEfforts = []string{"none", "low", "medium", "high", "xhigh", "max"}

var byokFamilies = []family{
	{modelkeys.ProviderOpenAI, "sol", "GPT‑6.2 Sol", "gpt-6.2-sol", "openai/gpt-6.2-sol", paidEfforts},
	{modelkeys.ProviderOpenAI, "terra", "GPT‑6.1 Terra", "gpt-6.1-terra", "openai/gpt-6.1-terra", optionalEfforts},
	{modelkeys.ProviderOpenAI, "luna", "GPT‑6 Luna", "gpt-6-luna", "openai/gpt-6-luna", optionalEfforts},
	{modelkeys.ProviderAnthropic, "opus", "Claude Opus 5.5", "claude-opus-5-5", "anthropic/claude-opus-5.5", paidEfforts},
	{modelkeys.ProviderAnthropic, "sonnet", "Claude Sonnet 5.5", "claude-sonnet-5-5", "anthropic/claude-sonnet-5.5", paidEfforts},
	{modelkeys.ProviderAnthropic, "haiku", "Claude Haiku 5.5", "claude-haiku-5-5", "anthropic/claude-haiku-5.5", paidEfforts},
	{modelkeys.ProviderGoogle, "carbon", "Gemini 4 Carbon", "gemini-4-carbon", "google/gemini-4-carbon", []string{"low", "medium", "high"}},
	{modelkeys.ProviderGoogle, "neon", "Gemini 4 Neon", "gemini-4-neon", "google/gemini-4-neon", []string{"low", "medium", "high", "xhigh"}},
	{modelkeys.ProviderGoogle, "hydrogen", "Gemini 4 Hydrogen", "gemini-4-hydrogen", "google/gemini-4-hydrogen", []string{"low", "medium", "high", "xhigh"}},
}

func byokProfiles(provider string) []Profile {
	def, ok := modelkeys.LookupProvider(provider)
	if !ok {
		return nil
	}
	routed := provider == modelkeys.ProviderVercelAIGateway || provider == modelkeys.ProviderOpenRouter
	var profiles []Profile
	for _, model := range byokFamilies {
		if !routed && model.provider != provider {
			continue
		}
		alias, label := model.direct, model.label
		if routed {
			alias, label = model.routed, model.label+" via "+def.Label
		}
		efforts := model.efforts
		if provider == modelkeys.ProviderVercelAIGateway && model.provider != modelkeys.ProviderAnthropic {
			efforts = slices.DeleteFunc(slices.Clone(efforts), func(e string) bool { return e == "max" })
		}
		profiles = append(profiles, Profile{
			ID: provider + "-" + model.id, Label: label, Version: "v1", Backend: provider,
			Model: alias, Lanes: []string{LaneChat, LaneTask}, Paid: true,
			SupportedReasoning: efforts, DefaultReasoning: "low",
		})
	}
	return profiles
}

func byokRecommendation(provider string) state.ModelSelection {
	chat, task, effort := "opus", "haiku", "low"
	switch provider {
	case modelkeys.ProviderOpenAI:
		chat, task, effort = "sol", "sol", "medium"
	case modelkeys.ProviderGoogle:
		chat, task, effort = "neon", "neon", "medium"
	}
	return state.ModelSelection{
		ChatProfileID: provider + "-" + chat, ChatReasoning: "low",
		TaskProfileID: provider + "-" + task, TaskReasoning: effort,
	}
}

func accountProfile(id string) (Profile, bool) {
	for _, provider := range modelkeys.Providers() {
		for _, p := range byokProfiles(provider.ID) {
			if p.ID == id {
				return p, true
			}
		}
	}
	return Profile{}, false
}

func (a *Account) stored(ctx context.Context) (state.ModelSelection, int64, error) {
	rec, err := a.Store.Resource(ctx, state.ModelSettingsCollection, state.ModelSettingsID)
	if errors.Is(err, state.ErrNotFound) {
		return state.ModelSelection{}, 0, nil
	}
	if err != nil {
		return state.ModelSelection{}, 0, err
	}
	var selection state.ModelSelection
	err = rec.Decode(&selection)
	return selection, rec.Revision, err
}

func (a *Account) write(ctx context.Context, selection state.ModelSelection, revision int64) (int64, error) {
	rec, err := state.NewResource(state.ModelSettingsCollection, state.ModelSettingsID, 0, selection, nil, time.Now())
	if err != nil {
		return 0, err
	}
	rec, err = a.Store.PutResource(ctx, rec, revision)
	return rec.Revision, err
}

// The first key initializes both lanes. Persist the choice so deleting it
// cannot implicitly switch a lane to another provider.
func (a *Account) selected(ctx context.Context, keys []modelkeys.ProviderStatus) (state.ModelSelection, int64, error) {
	sel, revision, err := a.stored(ctx)
	if err != nil || revision != 0 {
		return sel, revision, err
	}
	if keys == nil {
		status, err := a.Keys.List(ctx)
		if err != nil {
			return state.ModelSelection{}, 0, err
		}
		keys = status.Providers
	}
	var first *modelkeys.ProviderStatus
	for i := range keys {
		key := &keys[i]
		if key.Configured && (first == nil || (key.UpdatedAt != nil && first.UpdatedAt != nil && key.UpdatedAt.Before(*first.UpdatedAt))) {
			first = key
		}
	}
	if first == nil {
		return state.ModelSelection{}, 0, nil
	}
	sel = byokRecommendation(first.Provider)
	revision, err = a.write(ctx, sel, 0)
	if errors.Is(err, state.ErrRevisionConflict) {
		return a.stored(ctx)
	}
	return sel, revision, err
}

// PinAccountSelection captures concrete execution details while the account's
// settings keep their stable family IDs across catalog updates.
func PinAccountSelection(selection *state.ModelSelection) *state.ModelSelection {
	pinned := selection.Clone()
	if pinned == nil {
		return nil
	}
	if p, ok := accountProfile(pinned.ChatProfileID); ok {
		pinned.ChatProfileVersion = state.ModelSettingsVersion
		pinned.ChatPinned = pinProfile(p, LaneChat)
	}
	if p, ok := accountProfile(pinned.TaskProfileID); ok {
		pinned.TaskProfileVersion = state.ModelSettingsVersion
		pinned.TaskPinned = pinProfile(p, LaneTask)
	}
	return pinned
}

func (a *Account) CurrentSelection(ctx context.Context) (*state.ModelSelection, error) {
	if a == nil {
		return nil, nil
	}
	sel, _, err := a.selected(ctx, nil)
	return PinAccountSelection(&sel), err
}

func (a *Account) EnsureSelection(ctx context.Context) error {
	_, _, err := a.selected(ctx, nil)
	return err
}

func (a *Account) View(ctx context.Context) (AccountView, error) {
	keys, err := a.Keys.List(ctx)
	if err != nil {
		return AccountView{}, err
	}
	sel, revision, err := a.selected(ctx, keys.Providers)
	if err != nil {
		return AccountView{}, err
	}
	return accountView(keys.Providers, sel, revision), nil
}

func accountView(keys []modelkeys.ProviderStatus, sel state.ModelSelection, revision int64) AccountView {
	var cat Catalog
	out := AccountView{Selection: sel, Revision: revision, Recommendations: map[string]state.ModelSelection{}}
	for _, key := range keys {
		if key.Configured {
			cat.Profiles = append(cat.Profiles, byokProfiles(key.Provider)...)
			out.Recommendations[key.Provider] = byokRecommendation(key.Provider)
		}
	}
	out.ModelProfiles = cat.Public()
	return out
}

func (a *Account) Save(ctx context.Context, update state.ModelSelection, expected int64) (AccountView, error) {
	keys, err := a.Keys.List(ctx)
	if err != nil {
		return AccountView{}, err
	}
	current, revision, err := a.selected(ctx, keys.Providers)
	if err != nil {
		return AccountView{}, err
	}
	if revision != expected {
		return AccountView{}, state.ErrRevisionConflict
	}
	sel := state.ModelSelection{
		ChatProfileID: strings.TrimSpace(update.ChatProfileID), ChatReasoning: strings.ToLower(strings.TrimSpace(update.ChatReasoning)),
		TaskProfileID: strings.TrimSpace(update.TaskProfileID), TaskReasoning: strings.ToLower(strings.TrimSpace(update.TaskReasoning)),
	}
	check := func(lane, id, effort, oldID, oldEffort string) error {
		if id == oldID && effort == oldEffort {
			return nil
		}
		p, ok := accountProfile(id)
		if !ok || !p.SupportsReasoning(effort) {
			return fmt.Errorf("%w: Choose a model and a supported reasoning level for %s.", state.ErrInvalidResource, lane)
		}
		for _, key := range keys.Providers {
			if key.Provider == p.Backend && key.Configured {
				return nil
			}
		}
		return fmt.Errorf("%w: Add a key for the selected %s model.", state.ErrInvalidResource, lane)
	}
	if err := check("Chat", sel.ChatProfileID, sel.ChatReasoning, current.ChatProfileID, current.ChatReasoning); err != nil {
		return AccountView{}, err
	}
	if err := check("Work", sel.TaskProfileID, sel.TaskReasoning, current.TaskProfileID, current.TaskReasoning); err != nil {
		return AccountView{}, err
	}
	if !state.ModelSelectionEqual(&current, &sel) {
		revision, err = a.write(ctx, sel, expected)
		if err != nil {
			return AccountView{}, err
		}
	}
	return accountView(keys.Providers, sel, revision), nil
}

func (a *Account) Resolve(ctx context.Context, lane string, selection *state.ModelSelection) (Profile, string, string, error) {
	if a == nil || a.Keys == nil {
		return Profile{}, "", "", modelkeys.ErrMissingAPIKey
	}
	if selection.IsEmpty() ||
		(lane == LaneChat && selection.ChatProfileVersion != state.ModelSettingsVersion) ||
		(lane == LaneTask && selection.TaskProfileVersion != state.ModelSettingsVersion) {
		var err error
		selection, err = a.CurrentSelection(ctx)
		if err != nil {
			return Profile{}, "", "", err
		}
	}
	if selection == nil {
		return Profile{}, "", "", modelkeys.ErrMissingAPIKey
	}
	id, effort, pin := selection.ChatProfileID, selection.ChatReasoning, selection.ChatPinned
	if lane == LaneTask {
		id, effort, pin = selection.TaskProfileID, selection.TaskReasoning, selection.TaskPinned
	}
	if id == "" {
		return Profile{}, "", "", modelkeys.ErrMissingAPIKey
	}
	if pin == nil {
		return Profile{}, "", "", fmt.Errorf("%w Choose a model in More → Models.", modelkeys.ErrModelAccess)
	}
	if _, ok := modelkeys.LookupProvider(pin.Backend); !ok {
		return Profile{}, "", "", fmt.Errorf("%w Choose a model in More → Models.", modelkeys.ErrModelAccess)
	}
	p := Profile{
		ID: id, Backend: pin.Backend, Model: pin.Model, Paid: pin.Paid,
		SupportedReasoning: pin.SupportedReasoning, DefaultReasoning: pin.DefaultReasoning,
	}
	if effort == "" || !p.SupportsReasoning(effort) {
		effort = p.DefaultReasoning
	}
	key, err := a.Keys.ResolveKey(ctx, p.Backend)
	return p, effort, key, err
}
