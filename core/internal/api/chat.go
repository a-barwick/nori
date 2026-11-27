package api

import (
	"net/http"
	"strings"

	"nori/internal/conversation"
	"nori/internal/state"
)

const ChatContract = "nori-chat-2"

type capabilitiesView struct {
	Contract             string            `json:"contract"`
	ConversationContract string            `json:"conversationContract,omitempty"`
	Service              string            `json:"service"`
	Persistence          string            `json:"persistence,omitempty"`
	Provider             string            `json:"provider,omitempty"`
	Model                string            `json:"model,omitempty"`
	ConversationModes    []string          `json:"conversationModes"`
	Features             map[string]bool   `json:"features"`
	AssistantContract    string            `json:"assistant_contract,omitempty"`
	AssistantFeatures    map[string]bool   `json:"assistant_features,omitempty"`
	UploadAccept         *uploadAcceptView `json:"upload_accept,omitempty"`
}

func (s *server) capabilities(w http.ResponseWriter, r *http.Request) {
	modes := []string{}
	if !s.Features.DisableBackgroundTasks {
		modes = append(modes, "assistant")
	}
	provider := s.ModelProvider
	if provider == "" && (s.Assistant == nil || !s.Assistant.AccountBYOK) {
		provider = "openai"
	}
	body := capabilitiesView{
		Contract: ChatContract, ConversationContract: "nori-conversations-1", Service: "nori",
		Persistence: "durable", Provider: provider, Model: s.Model, ConversationModes: modes,
		Features: map[string]bool{"backgroundTasks": !s.Features.DisableBackgroundTasks, "jev": !s.Features.DisableBackgroundTasks && !s.Features.DisableJev},
	}
	if s.Assistant != nil {
		body.AssistantFeatures = s.Assistant.Features
		accepted := uploadAccept()
		body.UploadAccept = &accepted
	}
	writeJSON(w, 200, body)
}

func (s *server) assistantConversationSnapshot(w http.ResponseWriter, r *http.Request) {
	view, err := s.assistantSnapshot(r.Context(), "")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, view)
	if view.Status != nil && view.Status.Pending && !view.Status.Processing {
		s.repairWake(r.Context())
	}
}

func (s *server) resolveModelSelection(w http.ResponseWriter, sel *state.ModelSelection) (*state.ModelSelection, bool) {
	if s.Assistant != nil && s.Assistant.AccountBYOK {
		// Older clients can still send their device-local picker state.
		return nil, true
	}
	if sel.IsEmpty() {
		return nil, true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{"error": "Model selection is not enabled for this account.", "code": "model_selection_unavailable"})
	return nil, false
}

func (s *server) assistantConversationMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID          string                `json:"id"`
		Text        string                `json:"text"`
		Attachments []string              `json:"attachments,omitempty"`
		Parts       []state.Part          `json:"parts,omitempty"`
		Model       *state.ModelSelection `json:"model,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Parts != nil {
		if body.Text != "" || len(body.Attachments) != 0 {
			writeJSON(w, 400, map[string]string{"error": "Send parts or text and attachments, not both."})
			return
		}
		for _, p := range body.Parts {
			switch p.Type {
			case state.PartText:
				body.Text += p.Text
			case state.PartAttachment:
				body.Attachments = append(body.Attachments, p.ID)
			default:
				writeJSON(w, 400, map[string]string{"error": "User messages accept text and attachment parts."})
				return
			}
		}
	}
	body.Text = strings.TrimSpace(body.Text)
	if !validID(body.ID) || (body.Text == "" && len(body.Attachments) == 0) || len(body.Text) > state.MaxMessageBytes || len(body.Attachments) > state.MaxAttachments {
		writeJSON(w, 400, map[string]string{"error": "A UUID id and message of 1–8192 bytes or attachments are required."})
		return
	}
	for _, id := range body.Attachments {
		if !validID(id) {
			writeJSON(w, 400, map[string]string{"error": "Invalid attachment ID."})
			return
		}
	}
	sel, ok := s.resolveModelSelection(w, body.Model)
	if !ok {
		return
	}
	session, err := s.Runner.HandleInput(r.Context(), conversation.Input{ID: body.ID, Message: body.Text, Attachments: body.Attachments, ModelSelection: sel})
	if err != nil {
		writeError(w, err)
		return
	}
	view := assistantView(session)
	writeJSON(w, 202, map[string]any{"turnId": body.ID, "conversation": view})
}

func (s *server) assistantConversationCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TurnID string `json:"turnId"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !validID(body.TurnID) {
		writeJSON(w, 400, map[string]string{"error": "Invalid turn ID."})
		return
	}
	if err := s.Tasks.Store.CancelConversation(r.Context(), body.TurnID); err != nil {
		writeError(w, err)
		return
	}
	view, err := s.assistantSnapshot(r.Context(), "")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, view)
}

func (s *server) assistantConversationRetry(w http.ResponseWriter, r *http.Request) {
	var body retryRequest
	if !decode(w, r, &body) {
		return
	}
	turnID, messageID, ok := body.resolved()
	if !ok {
		writeJSON(w, 400, map[string]string{"error": "Invalid turn or message ID."})
		return
	}
	session, err := s.Runner.Retry(r.Context(), turnID, messageID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"conversation": assistantView(session)})
}

func (s *server) assistantConversationHistory(w http.ResponseWriter, r *http.Request) {
	before := r.URL.Query().Get("before")
	if len(before) > 256 || strings.Contains(before, "/") {
		writeError(w, state.ErrCommandInvalid)
		return
	}
	messages, next, err := s.Tasks.Store.ConversationMessages(r.Context(), before)
	if err != nil {
		writeError(w, err)
		return
	}
	views := make([]messageView, 0, len(messages))
	for _, m := range messages {
		views = append(views, assistantMessage(m))
	}
	writeJSON(w, 200, map[string]any{"messages": views, "nextCursor": next})
}
