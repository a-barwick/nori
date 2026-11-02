package api

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"nori/internal/conversation"
	"nori/internal/state"
)

// wakeConversation asks the API to process saved input. The deadline keeps a
// slow queue from holding the request.
func (s *server) wakeConversation(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.Waker.WakeConversation(ctx)
}

// ensureWake reconciles a durable input whose original enqueue was
// interrupted. Queue notifications may duplicate; only the Firestore lease
// can process a turn.
func (s *server) ensureWake(ctx context.Context, session conversation.Session) error {
	if !session.Conversation.Pending && !session.Conversation.Processing {
		return nil
	}
	return s.wakeConversation(ctx)
}

func (s *server) session(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Before string `json:"before,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	s.writeSession(w, r, body.Before)
}

// snapshot is the session read a client starts from before it follows the stream.
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
	if before == "" && session.Conversation.Revision == 0 && len(session.Messages) == 0 {
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
	var wakeErr error
	if !session.Conversation.Processing {
		wakeErr = s.ensureWake(r.Context(), session)
	}
	if wakeErr != nil {
		log.Printf("conversation wake reconciliation failed: %v", wakeErr)
		writeJSON(w, 503, map[string]any{"error": "Conversation delivery is unavailable. Retry shortly.", "retryable": true, "session": session})
		return
	}
	writeJSON(w, 200, session)
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
		writeJSON(w, 400, map[string]string{"error": "A message can have at most 5 attached files."})
		return
	}
	for _, id := range input.Attachments {
		if !state.ValidUploadID(id) {
			writeJSON(w, 400, map[string]string{"error": "Send each attachment's upload ID."})
			return
		}
	}
	session, err := s.Runner.HandleInput(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.ensureWake(r.Context(), session); err != nil {
		log.Printf("accepted conversation input wake failed: %v", err)
		writeJSON(w, 503, map[string]any{
			"error":    "Your message is saved, but delivery could not be confirmed. Retry with the same message ID.",
			"accepted": true, "retryable": true, "session": session,
		})
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
		// Never acknowledge Cloud Tasks while another request owns the lease
		// or a bounded slice still has work; retry the durable queue item.
		log.Printf("conversation process requires retry: %v", err)
		writeJSON(w, 503, map[string]any{"error": "Conversation processing will retry.", "retryable": true})
		return
	}
	writeJSON(w, 200, map[string]bool{"processed": true})
}
