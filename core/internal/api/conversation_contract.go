package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nori/internal/conversation"
	"nori/internal/state"
)

// These are HTTP projections, not a second persistence model. Typed parts retain
// files and pages; text/attachments remain for existing chat clients.
type messageView struct {
	ID          string             `json:"id"`
	Role        string             `json:"role"`
	Text        string             `json:"text"`
	Status      string             `json:"status"`
	Parts       []state.Part       `json:"parts"`
	Sequence    int64              `json:"sequence,omitempty"`
	TurnID      string             `json:"turnId,omitempty"`
	CreatedAt   *time.Time         `json:"createdAt,omitempty"`
	Attachments []state.Attachment `json:"attachments,omitempty"`
	Operations  []operationView    `json:"operations,omitempty"`
	ResultIDs   []string           `json:"resultIds,omitempty"`
}
type operationOwner struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
type operationView struct {
	state.OperationRecord
	Owner  operationOwner `json:"owner"`
	Result map[string]any `json:"result,omitempty"`
}
type assistantExtras struct {
	Tasks        []state.Run    `json:"tasks,omitempty"`
	Results      []state.Result `json:"results,omitempty"`
	ReadPosition state.ReadMark `json:"readPosition"`
}
type conversationHeader struct {
	state.ChatHeader
	TurnPolicy string                           `json:"turnPolicy"`
	Status     *conversation.ConversationStatus `json:"status,omitempty"`
	Paused     *bool                            `json:"paused,omitempty"`
}
type conversationView struct {
	conversationHeader
	Messages   []messageView    `json:"messages"`
	NextBefore int64            `json:"nextBefore,omitempty"`
	NextCursor string           `json:"nextCursor,omitempty"`
	Assistant  *assistantExtras `json:"assistant,omitempty"`
}
type conversationEvent struct {
	Seq          int64              `json:"seq"`
	Kind         string             `json:"kind"`
	Conversation conversationHeader `json:"conversation"`
	Messages     []messageView      `json:"messages"`
	Tasks        []state.Run        `json:"tasks,omitempty"`
	Results      []state.Result     `json:"results,omitempty"`
	Operations   []operationView    `json:"operations,omitempty"`
	ReadPosition *state.ReadMark    `json:"readPosition,omitempty"`
	// Missing says a referenced record no longer exists; it does not invent a result.
	Missing bool `json:"missing,omitempty"`
}

func chatCursor(before int64) string {
	if before == 0 {
		return ""
	}
	return strconv.FormatInt(before, 10)
}
func chatMessages(messages []state.ChatMessage) []messageView {
	result := make([]messageView, 0, len(messages))
	for _, m := range messages {
		parts := state.TextParts(m.Text)
		for _, a := range m.Attachments {
			parts = append(parts, state.AttachmentPart(a))
		}
		operations := make([]operationView, 0, len(m.Operations))
		for _, op := range m.Operations {
			operations = append(operations, operationView{OperationRecord: op.OperationRecord, Owner: operationOwner{Type: "message", ID: m.ID}, Result: op.Result})
		}
		result = append(result, messageView{ID: m.ID, Role: m.Role, Text: m.Text, Status: m.Status, Parts: parts, Sequence: m.Sequence, Attachments: m.Attachments, Operations: operations})
	}
	return result
}
func assistantMessage(m state.Message) messageView {
	m = m.WithParts()
	status := m.Status
	if status == "" {
		status = "completed"
	}
	return messageView{ID: m.ID, Role: m.Role, Text: m.Text, Status: status, Parts: m.Parts, TurnID: m.TurnID, CreatedAt: &m.CreatedAt, ResultIDs: m.ResultIDs}
}
func chatView(c state.Chat) conversationView {
	return conversationView{conversationHeader: conversationHeader{ChatHeader: c.Header(), TurnPolicy: "reject_while_running"}, Messages: chatMessages(c.Messages), NextBefore: c.NextBefore, NextCursor: chatCursor(c.NextBefore)}
}
func chatEventView(e state.ChatEvent) conversationEvent {
	return conversationEvent{Seq: e.Seq, Kind: e.Kind, Conversation: conversationHeader{ChatHeader: e.Conversation, TurnPolicy: "reject_while_running"}, Messages: chatMessages(e.Messages)}
}
func (s *server) assistantSnapshot(ctx context.Context, before string) (conversationView, error) {
	if len(before) > 256 || strings.Contains(before, "/") {
		return conversationView{}, state.ErrCommandInvalid
	}
	session, err := s.Runner.SnapshotPage(ctx, before)
	if err != nil {
		return conversationView{}, err
	}
	return s.assistantView(ctx, session)
}
func (s *server) assistantView(ctx context.Context, session conversation.Session) (conversationView, error) {
	v := conversationView{conversationHeader: assistantHeader(session.Conversation, session.Position)}
	v.Paused = &session.Paused
	v.Messages = make([]messageView, 0, len(session.Messages))
	v.NextCursor = session.NextCursor
	v.Assistant = &assistantExtras{Tasks: session.Runs, Results: session.Results, ReadPosition: session.ReadPosition}
	for _, m := range session.Messages {
		v.Messages = append(v.Messages, assistantMessage(m))
	}
	return v, nil
}
func assistantHeader(status conversation.ConversationStatus, position int64) conversationHeader {
	var active *string
	if status.TurnID != "" {
		id := status.TurnID
		active = &id
	}
	return conversationHeader{ChatHeader: state.ChatHeader{ID: "assistant", Title: "Nori", Mode: "assistant", ActiveTurn: active, Position: position}, TurnPolicy: "fold_into_turn", Status: &status}
}

// Operations keep their durable owner: message for foreground chat, task for
// assistant work. Their outcome, provider evidence and recovery fields are unchanged.
func (s *server) conversationOperations(w http.ResponseWriter, r *http.Request) {
	id, owner := r.PathValue("conversation"), r.URL.Query().Get("ownerId")
	if !state.ValidEvidenceID(owner) {
		writeJSON(w, 400, map[string]string{"error": "Send an ownerId naming a message or task."})
		return
	}
	ops := []operationView{}
	if id == "assistant" {
		saved, err := s.Tasks.Store.Operations(r.Context(), owner)
		if err != nil {
			writeError(w, err)
			return
		}
		for _, op := range saved {
			ops = append(ops, operationView{OperationRecord: op, Owner: operationOwner{Type: "task", ID: owner}})
		}
	} else {
		c, err := s.Chats.Store.UpdateChat(r.Context(), id, func(*state.Chat) (string, error) { return "", nil }, owner)
		if err != nil {
			writeError(w, err)
			return
		}
		found := false
		for _, m := range chatMessages(c.Messages) {
			if m.ID == owner {
				ops = m.Operations
				found = true
				break
			}
		}
		if !found {
			writeError(w, state.ErrNotFound)
			return
		}
	}
	if ops == nil {
		ops = []operationView{}
	}
	writeJSON(w, 200, map[string]any{"operations": ops})
}
