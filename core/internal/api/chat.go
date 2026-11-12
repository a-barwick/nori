package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nori/internal/conversation"
	"nori/internal/state"
)

const ChatContract = "nori-chat-2"

// capabilitiesView is the GET /v1/capabilities payload (common.schema.json#/$defs/Capabilities).
// Assistant fields are omitted when the assistant routes are not registered, matching AssistantCapabilities.
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
	PushPublicKey        string            `json:"push_public_key,omitempty"`
}

func (s *server) capabilities(w http.ResponseWriter, r *http.Request) {
	modes := []string{"assistant", "chat"}
	if s.Features.DisableBackgroundTasks {
		modes = []string{"chat"}
	}
	body := capabilitiesView{
		Contract: ChatContract, ConversationContract: "nori-conversations-1", Service: "nori",
		Persistence: "durable", Provider: "openai", Model: s.Model, ConversationModes: modes,
		Features: map[string]bool{"backgroundTasks": !s.Features.DisableBackgroundTasks, "jev": !s.Features.DisableBackgroundTasks && !s.Features.DisableJev, "computer": !s.Features.DisableComputer},
	}
	if s.Assistant != nil {
		// The assistant contract label is advertised only once every family it names is live; until then each
		// family reports itself.
		body.AssistantFeatures = s.Assistant.Features
		accepted := uploadAccept()
		body.UploadAccept = &accepted
		// The browser subscribes to push with this key; it is absent when push has no transport.
		if s.Assistant.PushPublicKey != "" {
			body.PushPublicKey = s.Assistant.PushPublicKey
		}
	}
	writeJSON(w, 200, body)
}

type conversationList struct {
	Conversations []conversationHeader `json:"conversations"`
}

type conversationCreate struct {
	Title string `json:"title,omitempty"`
}

type conversationSend struct {
	ID          string       `json:"id"`
	Text        string       `json:"text,omitempty"`
	Attachments []string     `json:"attachments,omitempty"`
	Parts       []state.Part `json:"parts,omitempty"`
}

type conversationCancel struct {
	TurnID string `json:"turnId"`
}

type conversationAccepted struct {
	TurnID       string           `json:"turnId"`
	Conversation conversationView `json:"conversation"`
}

type conversationHistory struct {
	Messages   []messageView `json:"messages"`
	NextBefore *int64        `json:"nextBefore,omitempty"`
	NextCursor string        `json:"nextCursor"`
}

type conversationOperationList struct {
	Operations []operationView `json:"operations"`
}

func (s *server) listChats(w http.ResponseWriter, r *http.Request) {
	chats, err := s.Chats.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	rows := make([]conversationHeader, 0, len(chats)+1)
	if !s.Features.DisableBackgroundTasks {
		position, err := s.Tasks.Store.EventPosition(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		presence, err := s.Runner.Presence(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		header := assistantHeader(&presence.Conversation, position)
		header.Paused = &presence.Paused
		rows = append(rows, header)
	}
	for _, c := range chats {
		rows = append(rows, chatHeader(state.ChatHeader{ID: c.ID, Title: c.Title, Mode: c.Mode, ActiveTurn: c.ActiveTurn, UpdatedAt: c.UpdatedAt}))
	}
	writeJSON(w, 200, conversationList{Conversations: rows})
}
func (s *server) createChat(w http.ResponseWriter, r *http.Request) {
	var body conversationCreate
	if !decode(w, r, &body) {
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	if len(body.Title) > 200 {
		writeJSON(w, 400, map[string]string{"error": "Title is too long."})
		return
	}
	c, err := s.Chats.Create(r.Context(), body.Title)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 201, chatView(c))
}
func (s *server) deleteChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("conversation")
	if !validID(id) {
		writeJSON(w, 404, map[string]string{"error": "Conversation not found."})
		return
	}
	if err := s.Chats.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}
func (s *server) chatSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("conversation")
	if id == "assistant" {
		view, err := s.assistantSnapshot(r.Context(), "")
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, view)
		s.repairWake(r.Context())
		return
	}
	c, err := s.Chats.Store.Chat(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, chatView(c))
	s.repairWake(r.Context())
}

func (s *server) chatMessage(w http.ResponseWriter, r *http.Request) {
	var body conversationSend
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
	id := r.PathValue("conversation")
	var view conversationView
	var wakeErr error
	if id == "assistant" {
		session, err := s.Runner.HandleInput(r.Context(), conversation.Input{ID: body.ID, Message: body.Text, Attachments: body.Attachments})
		if err != nil {
			writeError(w, err)
			return
		}
		view = assistantView(session)
		wakeErr = s.ensureWake(r.Context(), session)
	} else {
		c, err := s.Chats.Accept(r.Context(), id, body.ID, body.Text, body.Attachments)
		if errors.Is(err, state.ErrChatBusy) {
			// A busy chat may have lost the wake that starts its saved turn.
			s.repairWake(r.Context())
		}
		if err != nil {
			writeError(w, err)
			return
		}
		view = chatView(c)
		if c.ActiveTurn != nil {
			wakeErr = s.wakeConversation(r.Context())
		}
	}
	if wakeErr != nil {
		writeJSON(w, 503, map[string]any{"accepted": true, "conversation": view, "error": "Message saved; retry with the same ID to confirm delivery."})
		return
	}
	writeJSON(w, 202, conversationAccepted{TurnID: body.ID, Conversation: view})
}
func (s *server) chatCancel(w http.ResponseWriter, r *http.Request) {
	var body conversationCancel
	if !decode(w, r, &body) {
		return
	}
	if !validID(body.TurnID) {
		writeJSON(w, 400, map[string]string{"error": "Invalid turn ID."})
		return
	}
	id := r.PathValue("conversation")
	var view conversationView
	var err error
	if id == "assistant" {
		if err = s.Tasks.Store.CancelConversation(r.Context(), body.TurnID); err == nil {
			view, err = s.assistantSnapshot(r.Context(), "")
		}
	} else {
		var c state.Chat
		c, err = s.Chats.Cancel(r.Context(), id, body.TurnID)
		if err == nil {
			view = chatView(c)
		}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if err = s.wakeConversation(r.Context()); err != nil {
		writeJSON(w, 503, map[string]any{"accepted": true, "conversation": view, "error": "Stop is saved; delivery unavailable."})
		return
	}
	writeJSON(w, 200, view)
}

func (s *server) chatEvents(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("conversation") == "assistant" {
		s.assistantEvents(w, r)
		return
	}
	id := r.PathValue("conversation")
	after, _, valid := resumePosition(r)
	if !valid {
		writeJSON(w, 400, map[string]string{"error": "Invalid event position."})
		return
	}

	c, err := s.Chats.Store.Chat(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	streamConversation(w, r, after, conversationFeed[state.ChatEvent]{
		header:    chatHeader(c.Header()),
		limit:     state.ChatEventReplayLimit,
		heartbeat: 15 * time.Second,
		comment:   "keepalive",
		repair:    func() { s.repairWake(r.Context()) },
		watch: func(ctx context.Context, deliver func([]state.ChatEvent) error) error {
			return s.Chats.Store.WatchChatEvents(ctx, id, after, deliver)
		},
		sequence: func(event state.ChatEvent) int64 { return event.Seq },
		project: func(_ context.Context, event state.ChatEvent) (conversationEvent, error) {
			return chatEventView(event), nil
		},
	})
}

func (s *server) chatUpload(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("conversation") == "assistant" {
		s.upload(w, r)
		return
	}
	id := r.URL.Query().Get("id")
	if !validID(id) {
		writeJSON(w, 400, map[string]string{"error": "Invalid upload ID."})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, state.MaxUploadBytes))
	if err != nil {
		writeJSON(w, 413, map[string]string{"error": "File exceeds limit or upload interrupted."})
		return
	}
	u, err := s.Chats.SaveUpload(r.Context(), r.PathValue("conversation"), id, r.URL.Query().Get("filename"), r.Header.Get("Content-Type"), data)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, u.Attachment)
}
func (s *server) chatReadUpload(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("conversation") == "assistant" {
		s.readUpload(w, r)
		return
	}
	id := r.PathValue("id")
	if !validID(id) {
		writeJSON(w, 404, map[string]string{"error": "File not found."})
		return
	}
	u, data, err := s.Chats.ReadUpload(r.Context(), r.PathValue("conversation"), id)
	if err != nil {
		writeError(w, err)
		return
	}
	serveFile(w, u.Name, u.MediaType, data)
}

func (s *server) chatHistory(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("conversation") == "assistant" {
		view, err := s.assistantSnapshot(r.Context(), r.URL.Query().Get("before"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, conversationHistory{Messages: view.Messages, NextCursor: view.NextCursor})
		return
	}
	before := int64(0)
	if value := r.URL.Query().Get("before"); value != "" {
		var err error
		before, err = strconv.ParseInt(value, 10, 64)
		if err != nil || before < 1 {
			writeJSON(w, 400, map[string]string{"error": "Invalid message position."})
			return
		}
	}
	page, err := s.Chats.Store.ChatMessages(r.Context(), r.PathValue("conversation"), before)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, conversationHistory{Messages: chatMessages(page.Messages), NextBefore: &page.NextBefore, NextCursor: chatCursor(page.NextBefore)})
}
