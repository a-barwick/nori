package state

const (
	ModelSettingsCollection = "model_settings"
	ModelSettingsID         = "models"
	ModelSettingsVersion    = "account-v1"
)

type PinnedProfile struct {
	Backend            string   `json:"backend,omitempty" firestore:"backend,omitempty"`
	Model              string   `json:"model,omitempty" firestore:"model,omitempty"`
	Adapter            string   `json:"adapter,omitempty" firestore:"adapter,omitempty"`
	Endpoint           string   `json:"endpoint,omitempty" firestore:"endpoint,omitempty"`
	SupportedReasoning []string `json:"supported_reasoning,omitempty" firestore:"supported_reasoning,omitempty"`
	DefaultReasoning   string   `json:"default_reasoning,omitempty" firestore:"default_reasoning,omitempty"`
	Paid               bool     `json:"paid,omitempty" firestore:"paid,omitempty"`
}

func (p *PinnedProfile) Clone() *PinnedProfile {
	if p == nil {
		return nil
	}
	cp := *p
	if len(p.SupportedReasoning) > 0 {
		cp.SupportedReasoning = append([]string(nil), p.SupportedReasoning...)
	}
	return &cp
}

type ModelSelection struct {
	ChatProfileID      string         `json:"chat_profile_id,omitempty" firestore:"chat_profile_id,omitempty"`
	ChatProfileVersion string         `json:"chat_profile_version,omitempty" firestore:"chat_profile_version,omitempty"`
	ChatReasoning      string         `json:"chat_reasoning,omitempty" firestore:"chat_reasoning,omitempty"`
	ChatPinned         *PinnedProfile `json:"chat_pinned,omitempty" firestore:"chat_pinned,omitempty"`
	TaskProfileID      string         `json:"task_profile_id,omitempty" firestore:"task_profile_id,omitempty"`
	TaskProfileVersion string         `json:"task_profile_version,omitempty" firestore:"task_profile_version,omitempty"`
	TaskReasoning      string         `json:"task_reasoning,omitempty" firestore:"task_reasoning,omitempty"`
	TaskPinned         *PinnedProfile `json:"task_pinned,omitempty" firestore:"task_pinned,omitempty"`
}

func (s *ModelSelection) IsEmpty() bool {
	if s == nil {
		return true
	}
	return s.ChatProfileID == "" && s.ChatReasoning == "" && s.TaskProfileID == "" && s.TaskReasoning == ""
}

func (s *ModelSelection) Clone() *ModelSelection {
	if s.IsEmpty() {
		return nil
	}
	cp := *s
	cp.ChatPinned = s.ChatPinned.Clone()
	cp.TaskPinned = s.TaskPinned.Clone()
	return &cp
}

func (s *ModelSelection) TaskOnly() *ModelSelection {
	if s == nil || (s.TaskProfileID == "" && s.TaskReasoning == "") {
		return nil
	}
	return &ModelSelection{
		TaskProfileID:      s.TaskProfileID,
		TaskProfileVersion: s.TaskProfileVersion,
		TaskReasoning:      s.TaskReasoning,
		TaskPinned:         s.TaskPinned.Clone(),
	}
}

func ModelSelectionEqual(a, b *ModelSelection) bool {
	if a.IsEmpty() && b.IsEmpty() {
		return true
	}
	if a.IsEmpty() || b.IsEmpty() {
		return false
	}
	return a.ChatProfileID == b.ChatProfileID &&
		a.ChatReasoning == b.ChatReasoning &&
		a.TaskProfileID == b.TaskProfileID &&
		a.TaskReasoning == b.TaskReasoning
}
