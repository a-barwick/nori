package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"nori/internal/conversation"
	"nori/internal/state"
)

func (s *server) wakeConversation(ctx context.Context) error {
	if s.Waker == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.Waker.WakeConversation(ctx)
}

func (s *server) snapshot(w http.ResponseWriter, r *http.Request) {
	s.writeSession(w, r, r.URL.Query().Get("before"))
}

func (s *server) writeSession(w http.ResponseWriter, r *http.Request, before string) {
	if len(before) > 256 || strings.Contains(before, "/") {
		writeJSON(w, 400, map[string]string{"error": "Invalid history cursor."})
		return
	}
	session, err := s.Runner.SnapshotPage(r.Context(), before)
	if err != nil {
		writeError(w, err)
		return
	}
	if before == "" && session.Conversation.Revision == 0 && len(session.Messages) == 0 && !userDataResetPending(r.Context()) && s.canIntroduce(r.Context()) {
		if err := s.Runner.EnsureIntroduction(r.Context()); err != nil {
			writeError(w, err)
			return
		}
		session, err = s.Runner.Snapshot(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
	}
	if session.Conversation.Pending && !session.Conversation.Processing {
		s.repairWake(r.Context())
	}
	writeJSON(w, 200, session)
}

func (s *server) canIntroduce(ctx context.Context) bool {
	if s.Assistant == nil || s.Assistant.ModelKeys == nil {
		return true
	}
	keys, err := s.Assistant.ModelKeys.List(ctx)
	if err != nil {
		return false
	}
	if !keys.BYOKRequired {
		return true
	}
	for _, provider := range keys.Providers {
		if provider.Configured {
			return true
		}
	}
	return false
}

const repairWakeTimeout = 5 * time.Second

func (s *server) repairWake(ctx context.Context) {
	if s.Ops == nil {
		return
	}
	repairCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), repairWakeTimeout)
	defer cancel()
	if err := s.Ops.RepairWake(repairCtx); err != nil {
		slog.WarnContext(repairCtx, "saved work wake repair failed", "err", err)
	}
}

func (s *server) turn(w http.ResponseWriter, r *http.Request) {
	var input conversation.Input
	if !decode(w, r, &input) {
		return
	}
	input.Message = strings.TrimSpace(input.Message)
	if !validID(input.ID) || (input.Message == "" && len(input.Attachments) == 0) || len(input.Message) > state.MaxMessageBytes {
		writeJSON(w, 400, map[string]string{"error": "A UUID id and a message of 1–8192 bytes are required."})
		return
	}
	if len(input.Attachments) > state.MaxAttachments {
		writeJSON(w, 400, map[string]string{"error": "A message can have at most 10 attached files."})
		return
	}
	for _, id := range input.Attachments {
		if !state.ValidUploadID(id) {
			writeJSON(w, 400, map[string]string{"error": "Send each attachment's upload ID."})
			return
		}
	}
	sel, ok := s.resolveModelSelection(w, input.ModelSelection)
	if !ok {
		return
	}
	input.ModelSelection = sel
	session, err := s.Runner.HandleInput(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, session)
}

type retryRequest struct {
	TurnID         string `json:"turn_id"`
	TurnIDCamel    string `json:"turnId"`
	MessageID      string `json:"message_id"`
	MessageIDCamel string `json:"messageId"`
}

func (r retryRequest) resolved() (turnID, messageID string, ok bool) {
	turnID = strings.TrimSpace(r.TurnID)
	if turnID == "" {
		turnID = strings.TrimSpace(r.TurnIDCamel)
	}
	messageID = strings.TrimSpace(r.MessageID)
	if messageID == "" {
		messageID = strings.TrimSpace(r.MessageIDCamel)
	}
	if turnID != "" && !state.ValidEvidenceID(turnID) {
		return "", "", false
	}
	if messageID != "" && !state.ValidEvidenceID(messageID) {
		return "", "", false
	}
	return turnID, messageID, true
}

func (s *server) retry(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusAccepted, session)
}

func (s *server) process(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 9*time.Minute)
	defer cancel()
	if err := s.Ops.Process(ctx); err != nil {
		slog.WarnContext(ctx, "conversation process requires retry", "err", err)
		writeJSON(w, 503, map[string]any{"error": "Conversation processing will retry.", "retryable": true})
		return
	}
	writeJSON(w, 200, map[string]bool{"processed": true})
}
