package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"nori/internal/conversation"
	"nori/internal/state"
)

// These are HTTP projections, not a second persistence model. Typed parts retain
// files and pages; text/attachments remain for existing clients.
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
	ID         string                           `json:"id"`
	Title      string                           `json:"title"`
	Mode       string                           `json:"mode"`
	ActiveTurn *string                          `json:"activeTurn"`
	Position   int64                            `json:"position"`
	UpdatedAt  time.Time                        `json:"updatedAt"`
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

func assistantMessage(m state.Message) messageView {
	m = m.WithParts()
	status := m.Status
	if status == "" {
		status = "completed"
	}
	return messageView{ID: m.ID, Role: m.Role, Text: m.Text, Status: status, Parts: m.Parts, TurnID: m.TurnID, CreatedAt: &m.CreatedAt, ResultIDs: m.ResultIDs}
}

func (s *server) assistantSnapshot(ctx context.Context, before string) (conversationView, error) {
	if len(before) > 256 || strings.Contains(before, "/") {
		return conversationView{}, state.ErrCommandInvalid
	}
	session, err := s.Runner.SnapshotPage(ctx, before)
	if err != nil {
		return conversationView{}, err
	}
	return assistantView(session), nil
}

func assistantView(session conversation.Session) conversationView {
	v := conversationView{conversationHeader: assistantHeader(&session.Conversation, session.Position)}
	v.Paused = &session.Paused
	v.Messages = make([]messageView, 0, len(session.Messages))
	v.NextCursor = session.NextCursor
	v.Assistant = &assistantExtras{Tasks: session.Runs, Results: session.Results, ReadPosition: session.ReadPosition}
	for _, m := range session.Messages {
		v.Messages = append(v.Messages, assistantMessage(m))
	}
	return v
}

func assistantHeader(status *conversation.ConversationStatus, position int64) conversationHeader {
	header := conversationHeader{
		ID:         "assistant",
		Title:      "Nori",
		Mode:       "assistant",
		Position:   position,
		TurnPolicy: "fold_into_turn",
		Status:     status,
	}
	if status != nil && status.TurnID != "" {
		header.ActiveTurn = &status.TurnID
	}
	return header
}

func (s *server) assistantConversationOperations(w http.ResponseWriter, r *http.Request) {
	owner := r.URL.Query().Get("ownerId")
	if !state.ValidEvidenceID(owner) {
		writeJSON(w, 400, map[string]string{"error": "Send an ownerId naming a task."})
		return
	}
	saved, err := s.Tasks.Store.Operations(r.Context(), owner)
	if err != nil {
		writeError(w, err)
		return
	}
	ops := make([]operationView, 0, len(saved))
	for _, op := range saved {
		ops = append(ops, operationView{OperationRecord: op, Owner: operationOwner{Type: "task", ID: owner}})
	}
	writeJSON(w, 200, map[string]any{"operations": ops})
}
