package modelprofiles

import (
	"slices"

	"nori/internal/state"
)

const (
	BackendHome   = "home"
	BackendHosted = "hosted"

	AdapterChatCompletions = "chat_completions"
	AdapterResponses       = "responses"

	LaneChat = "chat"
	LaneTask = "task"
)

type Profile struct {
	HomeDeviceID       string   `json:"home_device_id,omitempty"`
	ModelDigest        string   `json:"model_digest,omitempty"`
	Availability       string   `json:"availability,omitempty"`
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
	HomeDeviceID       string   `json:"home_device_id,omitempty"`
	Availability       string   `json:"availability,omitempty"`
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
			HomeDeviceID:       p.HomeDeviceID,
			Availability:       p.Availability,
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

func (p Profile) SupportsLane(lane string) bool {
	return slices.Contains(p.Lanes, lane)
}

func (p Profile) SupportsReasoning(effort string) bool {
	if effort == "" {
		return true
	}
	return slices.Contains(p.SupportedReasoning, effort)
}

func pinProfile(p Profile, lane string) *state.PinnedProfile {
	adapter := p.ChatAdapter
	if lane == LaneTask {
		adapter = p.TaskAdapter
	}
	return &state.PinnedProfile{
		HomeDeviceID:       p.HomeDeviceID,
		ModelDigest:        p.ModelDigest,
		Backend:            p.Backend,
		Model:              p.Model,
		Adapter:            adapter,
		SupportedReasoning: append([]string(nil), p.SupportedReasoning...),
		DefaultReasoning:   p.DefaultReasoning,
		Paid:               p.Paid,
	}
}
