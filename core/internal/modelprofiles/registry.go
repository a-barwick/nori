package modelprofiles

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"nori/internal/settings"
	"nori/internal/state"
)

const (
	BackendLocal   = "local"
	BackendGateway = "gateway"

	AdapterChatCompletions   = "chat_completions"
	AdapterResponses         = "responses"
	AdapterAnthropicMessages = "anthropic_messages"

	LaneChat = "chat"
	LaneTask = "task"
)

type Profile struct {
	ID                 string   `json:"id"`
	Version            string   `json:"version"`
	Label              string   `json:"label"`
	Backend            string   `json:"backend"`
	Model              string   `json:"model"`
	ChatAdapter        string   `json:"chat_adapter,omitempty"`
	TaskAdapter        string   `json:"task_adapter,omitempty"`
	Lanes              []string `json:"lanes"`
	SupportedReasoning []string `json:"supported_reasoning,omitempty"`
	DefaultReasoning   string   `json:"default_reasoning,omitempty"`
	Paid               bool     `json:"paid"`
	Endpoint           string   `json:"endpoint,omitempty"`
}

type PublicProfile struct {
	ID                 string   `json:"id"`
	Version            string   `json:"version"`
	Label              string   `json:"label"`
	Backend            string   `json:"backend"`
	Model              string   `json:"model"`
	Lanes              []string `json:"lanes"`
	SupportedReasoning []string `json:"supported_reasoning,omitempty"`
	DefaultReasoning   string   `json:"default_reasoning,omitempty"`
	Paid               bool     `json:"paid"`
}

type PublicCatalog struct {
	DefaultChatProfileID string          `json:"default_chat_profile_id,omitempty"`
	DefaultChatReasoning string          `json:"default_chat_reasoning,omitempty"`
	DefaultTaskProfileID string          `json:"default_task_profile_id,omitempty"`
	DefaultTaskReasoning string          `json:"default_task_reasoning,omitempty"`
	Profiles             []PublicProfile `json:"profiles"`
}

type Catalog struct {
	DefaultChatProfileID string
	DefaultChatReasoning string
	DefaultTaskProfileID string
	DefaultTaskReasoning string
	Profiles             []Profile
	byID                 map[string]Profile
}

func (c Catalog) Public() PublicCatalog {
	out := PublicCatalog{
		DefaultChatProfileID: c.DefaultChatProfileID,
		DefaultChatReasoning: c.DefaultChatReasoning,
		DefaultTaskProfileID: c.DefaultTaskProfileID,
		DefaultTaskReasoning: c.DefaultTaskReasoning,
		Profiles:             make([]PublicProfile, 0, len(c.Profiles)),
	}
	for _, p := range c.Profiles {
		out.Profiles = append(out.Profiles, PublicProfile{
			ID:                 p.ID,
			Version:            p.Version,
			Label:              p.Label,
			Backend:            p.Backend,
			Model:              p.Model,
			Lanes:              append([]string(nil), p.Lanes...),
			SupportedReasoning: append([]string(nil), p.SupportedReasoning...),
			DefaultReasoning:   p.DefaultReasoning,
			Paid:               p.Paid,
		})
	}
	return out
}

func (c Catalog) Get(id string) (Profile, bool) {
	p, ok := c.byID[id]
	return p, ok
}

func (p Profile) SupportsLane(lane string) bool {
	return slices.Contains(p.Lanes, lane)
}

func (p Profile) SupportsReasoning(effort string) bool {
	if effort == "" {
		return true
	}
	return slices.Contains(p.SupportedReasoning, effort)
}

type filePayload struct {
	DefaultChatProfileID string    `json:"default_chat_profile_id,omitempty"`
	DefaultChatReasoning string    `json:"default_chat_reasoning,omitempty"`
	DefaultTaskProfileID string    `json:"default_task_profile_id,omitempty"`
	DefaultTaskReasoning string    `json:"default_task_reasoning,omitempty"`
	Profiles             []Profile `json:"profiles"`
}

type Registry struct {
	cloud       string
	envProfile  string
	models      settings.Models
	pickerUsers map[string]bool
	filePath    string

	mu      sync.RWMutex
	base    Catalog
	current Catalog
	lastMod time.Time
	hasMod  bool
}

func NewRegistry(cfg settings.Settings) (*Registry, error) {
	allow := make(map[string]bool, len(cfg.Models.PickerUsers))
	for _, u := range cfg.Models.PickerUsers {
		if trimmed := strings.ToLower(strings.TrimSpace(u)); trimmed != "" {
			allow[trimmed] = true
		}
	}
	r := &Registry{
		cloud:       cfg.Cloud,
		envProfile:  cfg.EnvProfile,
		models:      cfg.Models,
		pickerUsers: allow,
		filePath:    cfg.Models.ProfilesFile,
	}
	base := buildDefaultCatalog(cfg.Cloud, cfg.EnvProfile, cfg.Models)
	if cfg.Models.ProfilesJSON != "" {
		merged, err := applyOverrideJSON(base, []byte(cfg.Models.ProfilesJSON), cfg.Cloud, cfg.Models)
		if err != nil {
			return nil, fmt.Errorf("invalid NORI_MODEL_PROFILES: %w", err)
		}
		base = merged
	}
	r.base = base
	r.current = base
	if r.filePath != "" {
		if err := r.reloadFileLocked(true); err != nil {
			slog.Warn("initial NORI_MODEL_PROFILES_FILE load failed; using built-in profiles", "path", r.filePath, "err", err)
		}
	}
	return r, nil
}

func (r *Registry) CanSelect(userID, email string) bool {
	if r == nil {
		return false
	}
	if r.cloud == "local" {
		return true
	}
	if id := strings.ToLower(strings.TrimSpace(userID)); id != "" && r.pickerUsers[id] {
		return true
	}
	if em := strings.ToLower(strings.TrimSpace(email)); em != "" && r.pickerUsers[em] {
		return true
	}
	return false
}

func (r *Registry) Snapshot() Catalog {
	if r == nil {
		return Catalog{}
	}
	if r.filePath != "" {
		r.maybeReload()
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

func (r *Registry) maybeReload() {
	info, err := os.Stat(r.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			r.mu.Lock()
			if r.hasMod {
				r.current = r.base
				r.hasMod = false
				r.lastMod = time.Time{}
			}
			r.mu.Unlock()
		}
		return
	}
	mod := info.ModTime()
	r.mu.RLock()
	unchanged := r.hasMod && mod.Equal(r.lastMod)
	r.mu.RUnlock()
	if unchanged {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.reloadFileLocked(false); err != nil {
		slog.Warn("hot-reload of NORI_MODEL_PROFILES_FILE failed; keeping previous catalog", "path", r.filePath, "err", err)
	}
}

func (r *Registry) reloadFileLocked(initial bool) error {
	info, err := os.Stat(r.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			r.current = r.base
			r.hasMod = false
			r.lastMod = time.Time{}
			if initial {
				return nil
			}
		}
		return err
	}
	r.lastMod = info.ModTime()
	r.hasMod = true

	data, err := os.ReadFile(r.filePath)
	if err != nil {
		return err
	}
	merged, err := applyOverrideJSON(r.base, data, r.cloud, r.models)
	if err != nil {
		return err
	}
	r.current = merged
	return nil
}

func (r *Registry) ValidateAndResolve(sel *state.ModelSelection) (*state.ModelSelection, error) {
	if sel.IsEmpty() {
		return nil, nil
	}
	cat := r.Snapshot()
	out := &state.ModelSelection{
		ChatProfileID: strings.TrimSpace(sel.ChatProfileID),
		ChatReasoning: strings.ToLower(strings.TrimSpace(sel.ChatReasoning)),
		TaskProfileID: strings.TrimSpace(sel.TaskProfileID),
		TaskReasoning: strings.ToLower(strings.TrimSpace(sel.TaskReasoning)),
	}
	if out.ChatProfileID != "" {
		p, ok := cat.Get(out.ChatProfileID)
		if !ok || !p.SupportsLane(LaneChat) {
			return nil, fmt.Errorf("unknown or disallowed chat model profile %q", out.ChatProfileID)
		}
		if !p.SupportsReasoning(out.ChatReasoning) {
			return nil, fmt.Errorf("reasoning effort %q is not supported by chat profile %q", out.ChatReasoning, out.ChatProfileID)
		}
		out.ChatProfileVersion = p.Version
		out.ChatPinned = pinProfile(p, LaneChat)
	} else if out.ChatReasoning != "" {
		if cat.DefaultChatProfileID == "" {
			return nil, fmt.Errorf("chat reasoning override requires selecting a chat model profile")
		}
		p, ok := cat.Get(cat.DefaultChatProfileID)
		if !ok || !p.SupportsLane(LaneChat) || !p.SupportsReasoning(out.ChatReasoning) {
			return nil, fmt.Errorf("reasoning effort %q is not supported by default chat profile %q", out.ChatReasoning, cat.DefaultChatProfileID)
		}
		out.ChatProfileID = p.ID
		out.ChatProfileVersion = p.Version
		out.ChatPinned = pinProfile(p, LaneChat)
	}
	if out.TaskProfileID != "" {
		p, ok := cat.Get(out.TaskProfileID)
		if !ok || !p.SupportsLane(LaneTask) {
			return nil, fmt.Errorf("unknown or disallowed task model profile %q", out.TaskProfileID)
		}
		if !p.SupportsReasoning(out.TaskReasoning) {
			return nil, fmt.Errorf("reasoning effort %q is not supported by task profile %q", out.TaskReasoning, out.TaskProfileID)
		}
		out.TaskProfileVersion = p.Version
		out.TaskPinned = pinProfile(p, LaneTask)
	} else if out.TaskReasoning != "" {
		if cat.DefaultTaskProfileID == "" {
			return nil, fmt.Errorf("task reasoning override requires selecting a task model profile")
		}
		p, ok := cat.Get(cat.DefaultTaskProfileID)
		if !ok || !p.SupportsLane(LaneTask) || !p.SupportsReasoning(out.TaskReasoning) {
			return nil, fmt.Errorf("reasoning effort %q is not supported by default task profile %q", out.TaskReasoning, cat.DefaultTaskProfileID)
		}
		out.TaskProfileID = p.ID
		out.TaskProfileVersion = p.Version
		out.TaskPinned = pinProfile(p, LaneTask)
	}
	return out, nil
}

func pinProfile(p Profile, lane string) *state.PinnedProfile {
	adapter := p.ChatAdapter
	if lane == LaneTask {
		adapter = p.TaskAdapter
	}
	endpoint := ""
	if p.Backend == BackendLocal {
		endpoint = p.Endpoint
	}
	return &state.PinnedProfile{
		Backend:            p.Backend,
		Model:              p.Model,
		Adapter:            adapter,
		Endpoint:           endpoint,
		SupportedReasoning: append([]string(nil), p.SupportedReasoning...),
		DefaultReasoning:   p.DefaultReasoning,
		Paid:               p.Paid,
	}
}

func (r *Registry) profileFromPin(id, version, lane string, pin *state.PinnedProfile) (Profile, error) {
	p := Profile{
		ID:                 id,
		Version:            version,
		Backend:            pin.Backend,
		Model:              pin.Model,
		Lanes:              []string{lane},
		SupportedReasoning: append([]string(nil), pin.SupportedReasoning...),
		DefaultReasoning:   pin.DefaultReasoning,
		Paid:               pin.Paid,
	}
	if lane == LaneTask {
		p.TaskAdapter = pin.Adapter
	} else {
		p.ChatAdapter = pin.Adapter
	}
	if p.Backend == BackendGateway {
		p.Endpoint = r.models.GatewayURL
		p.Paid = true
		if !hasCloudGateway(r.models) {
			return Profile{}, fmt.Errorf("pinned gateway profile %q requires configured gateway credentials", id)
		}
	} else if p.Backend == BackendLocal {
		if r.cloud != "local" || !isStrictLoopbackURL(pin.Endpoint) {
			return Profile{}, fmt.Errorf("pinned local profile %q is not allowed in this environment", id)
		}
		p.Endpoint = pin.Endpoint
		p.Paid = false
	} else {
		return Profile{}, fmt.Errorf("pinned profile %q has unsupported backend %q", id, p.Backend)
	}
	return p, nil
}

func (r *Registry) ResolveChat(sel *state.ModelSelection) (Profile, string, bool, error) {
	if r == nil || sel == nil || (sel.ChatProfileID == "" && sel.ChatReasoning == "") {
		return Profile{}, "", false, nil
	}
	reasoning := sel.ChatReasoning
	if sel.ChatPinned != nil {
		p, err := r.profileFromPin(sel.ChatProfileID, sel.ChatProfileVersion, LaneChat, sel.ChatPinned)
		if err != nil {
			return Profile{}, "", true, err
		}
		if reasoning == "" {
			reasoning = p.DefaultReasoning
		}
		return p, reasoning, true, nil
	}
	cat := r.Snapshot()
	profileID := sel.ChatProfileID
	if profileID == "" {
		profileID = cat.DefaultChatProfileID
	}
	p, ok := cat.Get(profileID)
	if !ok || !p.SupportsLane(LaneChat) {
		return Profile{}, "", true, fmt.Errorf("pinned chat model profile %q is unavailable", profileID)
	}
	if sel.ChatProfileVersion != "" && p.Version != sel.ChatProfileVersion {
		return Profile{}, "", true, fmt.Errorf("pinned chat model profile %q version %q no longer matches catalog version %q", profileID, sel.ChatProfileVersion, p.Version)
	}
	if reasoning == "" {
		reasoning = p.DefaultReasoning
	}
	return p, reasoning, true, nil
}

func (r *Registry) ResolveTask(sel *state.ModelSelection) (Profile, string, bool, error) {
	if r == nil || sel == nil || (sel.TaskProfileID == "" && sel.TaskReasoning == "") {
		return Profile{}, "", false, nil
	}
	reasoning := sel.TaskReasoning
	if sel.TaskPinned != nil {
		p, err := r.profileFromPin(sel.TaskProfileID, sel.TaskProfileVersion, LaneTask, sel.TaskPinned)
		if err != nil {
			return Profile{}, "", true, err
		}
		if reasoning == "" {
			reasoning = p.DefaultReasoning
		}
		return p, reasoning, true, nil
	}
	cat := r.Snapshot()
	profileID := sel.TaskProfileID
	if profileID == "" {
		profileID = cat.DefaultTaskProfileID
	}
	p, ok := cat.Get(profileID)
	if !ok || !p.SupportsLane(LaneTask) {
		return Profile{}, "", true, fmt.Errorf("pinned task model profile %q is unavailable", profileID)
	}
	if sel.TaskProfileVersion != "" && p.Version != sel.TaskProfileVersion {
		return Profile{}, "", true, fmt.Errorf("pinned task model profile %q version %q no longer matches catalog version %q", profileID, sel.TaskProfileVersion, p.Version)
	}
	if reasoning == "" {
		reasoning = p.DefaultReasoning
	}
	return p, reasoning, true, nil
}

var claudeReasoningLevels = []string{"low", "medium", "high", "xhigh", "max"}

func buildDefaultCatalog(cloud, envProfile string, m settings.Models) Catalog {
	var profiles []Profile

	if cloud == "local" && m.LocalURL != "" && m.LocalModel != "" {
		profiles = append(profiles, Profile{
			ID:          "local-inkling-2-mini",
			Version:     "v1",
			Label:       "Inkling 2 Mini · Local",
			Backend:     BackendLocal,
			Model:       m.LocalModel,
			ChatAdapter: AdapterChatCompletions,
			TaskAdapter: AdapterResponses,
			Lanes:       []string{LaneChat, LaneTask},
			Paid:        false,
			Endpoint:    m.LocalURL,
		})
	}

	if hasCloudGateway(m) {
		if envProfile == "prod" {
			profiles = append(profiles,
				Profile{
					ID:                 "cloud-claude-opus-5-5",
					Version:            "v1",
					Label:              "Claude Opus 5.5 · Cloud",
					Backend:            BackendGateway,
					Model:              "anthropic/claude-opus-5.5",
					ChatAdapter:        AdapterAnthropicMessages,
					TaskAdapter:        AdapterAnthropicMessages,
					Lanes:              []string{LaneChat, LaneTask},
					SupportedReasoning: append([]string(nil), claudeReasoningLevels...),
					DefaultReasoning:   "low",
					Paid:               true,
					Endpoint:           m.GatewayURL,
				},
				Profile{
					ID:                 "cloud-claude-haiku-5-5",
					Version:            "v1",
					Label:              "Claude Haiku 5.5 · Cloud",
					Backend:            BackendGateway,
					Model:              "anthropic/claude-haiku-5.5",
					ChatAdapter:        AdapterAnthropicMessages,
					TaskAdapter:        AdapterAnthropicMessages,
					Lanes:              []string{LaneChat, LaneTask},
					SupportedReasoning: append([]string(nil), claudeReasoningLevels...),
					DefaultReasoning:   "low",
					Paid:               true,
					Endpoint:           m.GatewayURL,
				},
				Profile{
					ID:                 "cloud-gemini-4-neon",
					Version:            "v1",
					Label:              "Gemini 4 Neon · Cloud",
					Backend:            BackendGateway,
					Model:              "google/gemini-4-neon",
					ChatAdapter:        AdapterChatCompletions,
					TaskAdapter:        AdapterResponses,
					Lanes:              []string{LaneChat, LaneTask},
					SupportedReasoning: []string{"low", "medium", "high", "xhigh"},
					DefaultReasoning:   "medium",
					Paid:               true,
					Endpoint:           m.GatewayURL,
				},
				Profile{
					ID:          "cloud-gemini-4-hydrogen",
					Version:     "v1",
					Label:       "Gemini 4 Hydrogen · Cloud",
					Backend:     BackendGateway,
					Model:       "google/gemini-4-hydrogen",
					ChatAdapter: AdapterChatCompletions,
					TaskAdapter: AdapterResponses,
					Lanes:       []string{LaneChat, LaneTask},
					Paid:        true,
					Endpoint:    m.GatewayURL,
				},
				Profile{
					ID:          "cloud-gpt-6-2-sol",
					Version:     "v1",
					Label:       "GPT-6.2 Sol · Cloud",
					Backend:     BackendGateway,
					Model:       "openai/gpt-6.2-sol",
					ChatAdapter: AdapterChatCompletions,
					TaskAdapter: AdapterResponses,
					Lanes:       []string{LaneChat, LaneTask},
					Paid:        true,
					Endpoint:    m.GatewayURL,
				},
				Profile{
					ID:          "cloud-gpt-6-1-luna",
					Version:     "v1",
					Label:       "GPT-6.1 Luna · Cloud",
					Backend:     BackendGateway,
					Model:       "openai/gpt-6.1-luna",
					ChatAdapter: AdapterChatCompletions,
					TaskAdapter: AdapterResponses,
					Lanes:       []string{LaneChat, LaneTask},
					Paid:        true,
					Endpoint:    m.GatewayURL,
				},
			)
		} else {
			profiles = append(profiles,
				Profile{
					ID:                 "cloud-claude-haiku-5-5",
					Version:            "v1",
					Label:              "Claude Haiku 5.5 · Cloud",
					Backend:            BackendGateway,
					Model:              "anthropic/claude-haiku-5.5",
					ChatAdapter:        AdapterAnthropicMessages,
					TaskAdapter:        AdapterAnthropicMessages,
					Lanes:              []string{LaneChat, LaneTask},
					SupportedReasoning: append([]string(nil), claudeReasoningLevels...),
					DefaultReasoning:   "low",
					Paid:               true,
					Endpoint:           m.GatewayURL,
				},
			)
		}
	}

	cat := indexCatalog(profiles)
	for _, p := range cat.Profiles {
		if cat.DefaultChatProfileID == "" && p.SupportsLane(LaneChat) && p.Model == m.Outer {
			cat.DefaultChatProfileID = p.ID
			if p.SupportsReasoning(m.OuterReasoning) {
				cat.DefaultChatReasoning = m.OuterReasoning
			}
		}
		if cat.DefaultTaskProfileID == "" && p.SupportsLane(LaneTask) && p.Model == m.Inner {
			cat.DefaultTaskProfileID = p.ID
			if p.SupportsReasoning(m.InnerReasoning) {
				cat.DefaultTaskReasoning = m.InnerReasoning
			}
		}
	}
	if cat.DefaultChatProfileID == "" {
		for _, preferredID := range []string{"cloud-gemini-4-neon", "cloud-claude-opus-5-5", "cloud-gpt-6-2-sol", "local-inkling-2-mini"} {
			if p, ok := cat.Get(preferredID); ok && p.SupportsLane(LaneChat) {
				cat.DefaultChatProfileID = p.ID
				cat.DefaultChatReasoning = p.DefaultReasoning
				break
			}
		}
	}
	if cat.DefaultTaskProfileID == "" {
		for _, preferredID := range []string{"cloud-gemini-4-hydrogen", "cloud-claude-haiku-5-5", "cloud-gpt-6-1-luna", "local-inkling-2-mini"} {
			if p, ok := cat.Get(preferredID); ok && p.SupportsLane(LaneTask) {
				cat.DefaultTaskProfileID = p.ID
				cat.DefaultTaskReasoning = p.DefaultReasoning
				break
			}
		}
	}
	return cat
}

func hasCloudGateway(m settings.Models) bool {
	if m.GatewayURL == "" || (m.GatewayKey == "" && !m.BYOKRequired) {
		return false
	}
	u, err := url.Parse(m.GatewayURL)
	return err == nil && u != nil && u.Scheme == "https" && u.Hostname() != ""
}

func isStrictLoopbackURL(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, "?#") || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || u.Scheme != "http" || u.User != nil {
		return false
	}
	h := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if h == "localhost" || h == "127.0.0.1" || h == "::1" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func applyOverrideJSON(base Catalog, raw []byte, cloud string, m settings.Models) (Catalog, error) {
	var payload filePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Catalog{}, err
	}
	if len(payload.Profiles) == 0 {
		return Catalog{}, errors.New("profiles list must not be empty")
	}
	validated := make([]Profile, 0, len(payload.Profiles))
	seen := map[string]bool{}
	for _, p := range payload.Profiles {
		p, err := validateProfile(p, cloud, m)
		if err != nil {
			return Catalog{}, err
		}
		if seen[p.ID] {
			return Catalog{}, fmt.Errorf("duplicate profile id %q", p.ID)
		}
		seen[p.ID] = true
		validated = append(validated, p)
	}
	cat := indexCatalog(validated)
	if payload.DefaultChatProfileID != "" {
		p, ok := cat.Get(payload.DefaultChatProfileID)
		if !ok || !p.SupportsLane(LaneChat) {
			return Catalog{}, fmt.Errorf("default_chat_profile_id %q is not a valid chat profile", payload.DefaultChatProfileID)
		}
		cat.DefaultChatProfileID = p.ID
		if p.SupportsReasoning(payload.DefaultChatReasoning) {
			cat.DefaultChatReasoning = payload.DefaultChatReasoning
		}
	} else if _, ok := cat.Get(base.DefaultChatProfileID); ok {
		cat.DefaultChatProfileID = base.DefaultChatProfileID
		cat.DefaultChatReasoning = base.DefaultChatReasoning
	}
	if payload.DefaultTaskProfileID != "" {
		p, ok := cat.Get(payload.DefaultTaskProfileID)
		if !ok || !p.SupportsLane(LaneTask) {
			return Catalog{}, fmt.Errorf("default_task_profile_id %q is not a valid task profile", payload.DefaultTaskProfileID)
		}
		cat.DefaultTaskProfileID = p.ID
		if p.SupportsReasoning(payload.DefaultTaskReasoning) {
			cat.DefaultTaskReasoning = payload.DefaultTaskReasoning
		}
	} else if _, ok := cat.Get(base.DefaultTaskProfileID); ok {
		cat.DefaultTaskProfileID = base.DefaultTaskProfileID
		cat.DefaultTaskReasoning = base.DefaultTaskReasoning
	}
	return cat, nil
}

func validateProfile(p Profile, cloud string, m settings.Models) (Profile, error) {
	p.ID = strings.TrimSpace(p.ID)
	p.Version = strings.TrimSpace(p.Version)
	p.Label = strings.TrimSpace(p.Label)
	p.Backend = strings.ToLower(strings.TrimSpace(p.Backend))
	p.Model = strings.TrimSpace(p.Model)
	p.ChatAdapter = strings.ToLower(strings.TrimSpace(p.ChatAdapter))
	p.TaskAdapter = strings.ToLower(strings.TrimSpace(p.TaskAdapter))
	p.DefaultReasoning = strings.ToLower(strings.TrimSpace(p.DefaultReasoning))
	p.Endpoint = strings.TrimSpace(p.Endpoint)

	if p.ID == "" || strings.IndexFunc(p.ID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return p, fmt.Errorf("invalid profile id %q", p.ID)
	}
	if p.Version == "" {
		p.Version = "v1"
	}
	if p.Label == "" {
		p.Label = p.ID
	}
	if p.Model == "" || strings.IndexFunc(p.Model, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return p, fmt.Errorf("profile %q has invalid model %q", p.ID, p.Model)
	}
	switch p.Backend {
	case BackendLocal:
		if cloud != "local" {
			return p, fmt.Errorf("profile %q uses local backend outside NORI_CLOUD=local", p.ID)
		}
		if p.Endpoint == "" {
			p.Endpoint = m.LocalURL
		}
		if !isStrictLoopbackURL(p.Endpoint) {
			return p, fmt.Errorf("local profile %q must use an http loopback endpoint without credentials, query or fragment", p.ID)
		}
		p.Paid = false
	case BackendGateway:
		if p.Endpoint != "" && p.Endpoint != m.GatewayURL {
			return p, fmt.Errorf("gateway profile %q cannot override NORI_MODEL_GATEWAY_URL", p.ID)
		}
		p.Endpoint = m.GatewayURL
		if !hasCloudGateway(m) {
			return p, fmt.Errorf("gateway profile %q requires HTTPS NORI_MODEL_GATEWAY_URL and NORI_MODEL_GATEWAY_API_KEY", p.ID)
		}
		p.Paid = true
	default:
		return p, fmt.Errorf("profile %q has unsupported backend %q", p.ID, p.Backend)
	}
	if len(p.Lanes) == 0 {
		p.Lanes = []string{LaneChat, LaneTask}
	}
	for _, lane := range p.Lanes {
		if lane != LaneChat && lane != LaneTask {
			return p, fmt.Errorf("profile %q has invalid lane %q", p.ID, lane)
		}
	}
	if p.ChatAdapter == "" {
		p.ChatAdapter = AdapterChatCompletions
	}
	if p.ChatAdapter != AdapterChatCompletions && p.ChatAdapter != AdapterAnthropicMessages {
		return p, fmt.Errorf("profile %q has unsupported chat_adapter %q", p.ID, p.ChatAdapter)
	}
	if p.TaskAdapter == "" {
		p.TaskAdapter = AdapterResponses
	}
	if p.TaskAdapter != AdapterResponses && p.TaskAdapter != AdapterAnthropicMessages {
		return p, fmt.Errorf("profile %q has unsupported task_adapter %q", p.ID, p.TaskAdapter)
	}
	for i, effort := range p.SupportedReasoning {
		effort = strings.ToLower(strings.TrimSpace(effort))
		if !settings.ValidReasoningEffort(effort) || effort == "" {
			return p, fmt.Errorf("profile %q has invalid supported_reasoning %q", p.ID, effort)
		}
		p.SupportedReasoning[i] = effort
	}
	if p.DefaultReasoning != "" && !p.SupportsReasoning(p.DefaultReasoning) {
		return p, fmt.Errorf("profile %q default_reasoning %q must be in supported_reasoning", p.ID, p.DefaultReasoning)
	}
	return p, nil
}

func indexCatalog(profiles []Profile) Catalog {
	byID := make(map[string]Profile, len(profiles))
	for _, p := range profiles {
		byID[p.ID] = p
	}
	return Catalog{Profiles: profiles, byID: byID}
}
