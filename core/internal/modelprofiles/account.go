package modelprofiles

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"nori/internal/billing"
	"nori/internal/modelkeys"
	"nori/internal/settings"
	"nori/internal/state"
)

var (
	ErrHomeOnly            = errors.New("Home-only mode requires a home model.")
	ErrHomeWorkUnavailable = errors.New("This home model is unavailable for Work.")
)

type Account struct {
	Keys          *modelkeys.Service
	Store         state.ResourceStore
	HomeProfiles  func(context.Context) ([]Profile, error)
	HostedEnabled bool
	AllowBYOK     func(context.Context) (bool, error)
	AllowHomeAI   func(context.Context) (bool, error)
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

// HostedProfiles returns the allowlisted platform-hosted model profiles routed
// through Nori's Vercel AI Gateway.
func HostedProfiles() []Profile {
	profiles := make([]Profile, 0, len(byokFamilies))
	for _, model := range byokFamilies {
		if !settings.IsHostedModelAllowed(model.routed) {
			continue
		}
		efforts := model.efforts
		if model.provider != modelkeys.ProviderAnthropic {
			efforts = slices.DeleteFunc(slices.Clone(efforts), func(e string) bool { return e == "max" })
		}
		profiles = append(profiles, Profile{
			ID: BackendHosted + "-" + model.id, Label: model.label, Version: "v1", Backend: BackendHosted,
			Model: model.routed, Lanes: []string{LaneChat, LaneTask}, Paid: true,
			SupportedReasoning: efforts, DefaultReasoning: "low",
		})
	}
	return profiles
}

// HostedRecommendation returns the default Chat and Work selection for Nori Hosted models.
func HostedRecommendation() state.ModelSelection {
	return state.ModelSelection{
		ChatProfileID: BackendHosted + "-neon", ChatReasoning: "low",
		TaskProfileID: BackendHosted + "-hydrogen", TaskReasoning: "low",
	}
}

// LookupHostedProfile returns the hosted profile for id when it is on the allowlist.
func LookupHostedProfile(id string) (Profile, bool) {
	for _, p := range HostedProfiles() {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

func accountProfile(id string) (Profile, bool) {
	if p, ok := LookupHostedProfile(id); ok {
		return p, true
	}
	for _, provider := range modelkeys.Providers() {
		for _, p := range byokProfiles(provider.ID) {
			if p.ID == id {
				return p, true
			}
		}
	}
	return Profile{}, false
}

// Describe returns a human-readable model name, provider label, and hosting
// location ("cloud" or "home_ai") for user-facing model self-knowledge.
func Describe(profileID, backend, model string) (name, provider, location string) {
	backend = strings.TrimSpace(backend)
	model = strings.TrimSpace(model)
	profileID = strings.TrimSpace(profileID)
	if backend == BackendHome {
		if model == "" {
			model = profileID
		}
		return model, "Home AI Server", "home_ai"
	}
	location = "cloud"
	if backend == BackendHosted {
		provider = "Nori Hosted"
	} else if def, ok := modelkeys.LookupProvider(backend); ok {
		provider = def.Label
	} else {
		switch backend {
		case "gateway":
			provider = "Model Gateway"
		default:
			provider = backend
		}
	}
	for _, fam := range byokFamilies {
		if model == fam.direct || model == fam.routed || strings.HasSuffix(profileID, "-"+fam.id) {
			return fam.label, provider, location
		}
	}
	if model != "" {
		return model, provider, location
	}
	return profileID, provider, location
}

func (a *Account) stored(ctx context.Context) (state.ModelSelection, int64, error) {
	if a == nil || a.Store == nil {
		return state.ModelSelection{}, 0, nil
	}
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

func (a *Account) byokAllowed(ctx context.Context) (bool, error) {
	if a == nil || a.AllowBYOK == nil {
		return true, nil
	}
	return a.AllowBYOK(ctx)
}

func (a *Account) homeAIAllowed(ctx context.Context) (bool, error) {
	if a == nil || a.AllowHomeAI == nil {
		return true, nil
	}
	return a.AllowHomeAI(ctx)
}

// The first key initializes both lanes. Persist the choice so deleting it
// cannot implicitly switch a lane to another provider.
func (a *Account) selected(ctx context.Context, keys []modelkeys.ProviderStatus) (state.ModelSelection, int64, error) {
	sel, revision, err := a.stored(ctx)
	if err != nil || revision != 0 {
		return sel, revision, err
	}
	allowBYOK, err := a.byokAllowed(ctx)
	if err != nil {
		return state.ModelSelection{}, 0, err
	}
	if allowBYOK {
		if keys == nil && a != nil && a.Keys != nil {
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
		if first != nil {
			sel = byokRecommendation(first.Provider)
			revision, err = a.write(ctx, sel, 0)
			if errors.Is(err, state.ErrRevisionConflict) {
				return a.stored(ctx)
			}
			return sel, revision, err
		}
	}
	if a != nil && a.HostedEnabled {
		sel = HostedRecommendation()
		revision, err = a.write(ctx, sel, 0)
		if errors.Is(err, state.ErrRevisionConflict) {
			return a.stored(ctx)
		}
		return sel, revision, err
	}
	return state.ModelSelection{}, 0, nil
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
	if a == nil || a.Store == nil {
		return nil
	}
	_, _, err := a.selected(ctx, nil)
	return err
}

func (a *Account) HomeOnly(ctx context.Context) (bool, error) {
	if a == nil || a.Store == nil {
		return false, nil
	}
	selection, _, err := a.stored(ctx)
	return selection.HomeOnly, err
}

func (a *Account) CheckCloudDispatch(ctx context.Context) error {
	if a == nil {
		return nil
	}
	homeOnly, err := a.HomeOnly(ctx)
	if err != nil {
		return err
	}
	if homeOnly {
		return ErrHomeOnly
	}
	return nil
}

func (a *Account) homeProfiles(ctx context.Context) ([]Profile, error) {
	if a == nil || a.HomeProfiles == nil {
		return nil, nil
	}
	return a.HomeProfiles(ctx)
}

func (a *Account) View(ctx context.Context) (AccountView, error) {
	if a == nil || a.Store == nil {
		return AccountView{}, modelkeys.ErrMissingAPIKey
	}
	allowBYOK, err := a.byokAllowed(ctx)
	if err != nil {
		return AccountView{}, err
	}
	allowHome, err := a.homeAIAllowed(ctx)
	if err != nil {
		return AccountView{}, err
	}
	var providers []modelkeys.ProviderStatus
	if allowBYOK && a.Keys != nil {
		keys, err := a.Keys.List(ctx)
		if err != nil {
			return AccountView{}, err
		}
		providers = keys.Providers
	}
	sel, revision, err := a.selected(ctx, providers)
	if err != nil {
		return AccountView{}, err
	}
	var home []Profile
	if allowHome {
		home, err = a.homeProfiles(ctx)
		if err != nil {
			slog.WarnContext(ctx, "failed to load home AI model profiles", "err", err)
		}
	}
	return accountView(providers, home, a.HostedEnabled, sel, revision), nil
}

func accountView(keys []modelkeys.ProviderStatus, home []Profile, hostedEnabled bool, sel state.ModelSelection, revision int64) AccountView {
	var cat Catalog
	out := AccountView{Selection: sel, Revision: revision, Recommendations: map[string]state.ModelSelection{}}
	if hostedEnabled {
		cat.Profiles = append(cat.Profiles, HostedProfiles()...)
		out.Recommendations[BackendHosted] = HostedRecommendation()
	}
	for _, key := range keys {
		if key.Configured {
			cat.Profiles = append(cat.Profiles, byokProfiles(key.Provider)...)
			out.Recommendations[key.Provider] = byokRecommendation(key.Provider)
		}
	}
	cat.Profiles = append(cat.Profiles, home...)
	out.ModelProfiles = cat.Public()
	return out
}

func (a *Account) Save(ctx context.Context, update state.ModelSelection, expected int64, homeOnly *bool) (AccountView, error) {
	if a == nil || a.Store == nil {
		return AccountView{}, modelkeys.ErrMissingAPIKey
	}
	allowBYOK, err := a.byokAllowed(ctx)
	if err != nil {
		return AccountView{}, err
	}
	allowHome, err := a.homeAIAllowed(ctx)
	if err != nil {
		return AccountView{}, err
	}
	var providers []modelkeys.ProviderStatus
	if allowBYOK && a.Keys != nil {
		keys, err := a.Keys.List(ctx)
		if err != nil {
			return AccountView{}, err
		}
		providers = keys.Providers
	}
	current, revision, err := a.selected(ctx, providers)
	if err != nil {
		return AccountView{}, err
	}
	if revision != expected {
		return AccountView{}, state.ErrRevisionConflict
	}
	sel := state.ModelSelection{
		HomeOnly:      current.HomeOnly,
		ChatProfileID: strings.TrimSpace(update.ChatProfileID), ChatReasoning: strings.ToLower(strings.TrimSpace(update.ChatReasoning)),
		TaskProfileID: strings.TrimSpace(update.TaskProfileID), TaskReasoning: strings.ToLower(strings.TrimSpace(update.TaskReasoning)),
	}
	if homeOnly != nil {
		sel.HomeOnly = *homeOnly
	}
	var home []Profile
	var homeErr error
	if allowHome {
		home, homeErr = a.homeProfiles(ctx)
	}
	check := func(lane, id, effort, oldID, oldEffort string, oldPin *state.PinnedProfile) (*state.PinnedProfile, error) {
		if id == "" {
			optionalWork := lane == LaneTask && (oldID == "" || sel.ChatPinned != nil)
			unconfiguredChat := lane == LaneChat && oldID == "" && !sel.HomeOnly
			if optionalWork || unconfiguredChat {
				return nil, nil
			}
			return nil, fmt.Errorf("%w: Choose a model for %s.", state.ErrInvalidResource, lane)
		}
		if idx := slices.IndexFunc(home, func(p Profile) bool { return p.ID == id }); idx >= 0 {
			if !allowHome {
				return nil, billing.ErrBYOKNotAllowed
			}
			p := home[idx]
			if !p.SupportsLane(lane) || !p.SupportsReasoning(effort) {
				return nil, fmt.Errorf("%w: Choose a supported model and reasoning level for %s.", state.ErrInvalidResource, lane)
			}
			return pinProfile(p, lane), nil
		}
		if id == oldID && effort == oldEffort && oldPin != nil && oldPin.Backend == BackendHome {
			if !allowHome {
				return nil, billing.ErrBYOKNotAllowed
			}
			return oldPin, nil
		}
		if strings.HasPrefix(id, "home-") {
			if !allowHome {
				return nil, billing.ErrBYOKNotAllowed
			}
			if homeErr != nil {
				return nil, homeErr
			}
		}
		if sel.HomeOnly {
			return nil, fmt.Errorf("%w: Choose a home model for %s.", state.ErrInvalidResource, lane)
		}
		if id == oldID && effort == oldEffort {
			return nil, nil
		}
		p, ok := accountProfile(id)
		if !ok || !p.SupportsReasoning(effort) {
			return nil, fmt.Errorf("%w: Choose a model and a supported reasoning level for %s.", state.ErrInvalidResource, lane)
		}
		if p.Backend == BackendHosted {
			if !a.HostedEnabled {
				return nil, fmt.Errorf("%w: Hosted models are unavailable for %s.", state.ErrInvalidResource, lane)
			}
			return nil, nil
		}
		if !allowBYOK {
			return nil, billing.ErrBYOKNotAllowed
		}
		for _, key := range providers {
			if key.Provider == p.Backend && key.Configured {
				return nil, nil
			}
		}
		return nil, fmt.Errorf("%w: Add a key for the selected %s model.", state.ErrInvalidResource, lane)
	}
	sel.ChatPinned, err = check(LaneChat, sel.ChatProfileID, sel.ChatReasoning, current.ChatProfileID, current.ChatReasoning, current.ChatPinned)
	if err != nil {
		return AccountView{}, err
	}
	sel.TaskPinned, err = check(LaneTask, sel.TaskProfileID, sel.TaskReasoning, current.TaskProfileID, current.TaskReasoning, current.TaskPinned)
	if err != nil {
		return AccountView{}, err
	}
	if sel.ChatPinned != nil {
		sel.ChatProfileVersion = state.ModelSettingsVersion
	}
	if sel.TaskPinned != nil {
		sel.TaskProfileVersion = state.ModelSettingsVersion
	}
	if !state.ModelSelectionEqual(&current, &sel) {
		revision, err = a.write(ctx, sel, expected)
		if err != nil {
			return AccountView{}, err
		}
	}
	return accountView(providers, home, a.HostedEnabled, sel, revision), nil
}

func (a *Account) Resolve(ctx context.Context, lane string, selection *state.ModelSelection) (Profile, string, string, error) {
	if a == nil || a.Store == nil {
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
	if pin.Backend == BackendHome {
		allowed, err := a.homeAIAllowed(ctx)
		if err != nil {
			return Profile{}, "", "", err
		}
		if !allowed {
			return Profile{}, "", "", billing.ErrBYOKNotAllowed
		}
		if a.HomeProfiles == nil {
			return Profile{}, "", "", fmt.Errorf("%w Home AI Server is unavailable.", modelkeys.ErrModelAccess)
		}
		return Profile{ID: id, Backend: pin.Backend, Model: pin.Model, HomeDeviceID: pin.HomeDeviceID, ModelDigest: pin.ModelDigest}, effort, "", nil
	}
	if err := a.CheckCloudDispatch(ctx); err != nil {
		return Profile{}, "", "", err
	}
	if pin.Backend == BackendHosted {
		if !a.HostedEnabled || !settings.IsHostedModelAllowed(pin.Model) {
			return Profile{}, "", "", fmt.Errorf("%w Hosted model is unavailable.", modelkeys.ErrModelAccess)
		}
		p := Profile{
			ID: id, Backend: BackendHosted, Model: pin.Model, Paid: true,
			SupportedReasoning: pin.SupportedReasoning, DefaultReasoning: pin.DefaultReasoning,
		}
		if effort == "" || !p.SupportsReasoning(effort) {
			effort = p.DefaultReasoning
		}
		return p, effort, "", nil
	}
	allowed, err := a.byokAllowed(ctx)
	if err != nil {
		return Profile{}, "", "", err
	}
	if !allowed {
		return Profile{}, "", "", billing.ErrBYOKNotAllowed
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
	if a.Keys == nil {
		return Profile{}, "", "", modelkeys.ErrMissingAPIKey
	}
	key, err := a.Keys.ResolveKey(ctx, p.Backend)
	return p, effort, key, err
}
