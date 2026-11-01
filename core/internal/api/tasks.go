package api

import (
	"log"
	"net/http"
	"strings"

	"nori/internal/state"
)

func (s *server) resume(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	if err := s.Ops.Resume(r.Context()); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"resumed": true})
}

func (s *server) input(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID      string `json:"id"`
		RunID   string `json:"run_id"`
		Message string `json:"message"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if !validID(body.ID) || !validID(body.RunID) || body.Message == "" || len(body.Message) > state.MaxMessageBytes {
		writeJSON(w, 400, map[string]string{"error": "UUID id, run_id, and a message of 1–8192 bytes are required."})
		return
	}
	run, err := s.Tasks.Update(r.Context(), body.ID, body.RunID, body.Message)
	if err != nil {
		writeError(w, err)
		return
	}
	if !s.confirmWake(w, r, run) {
		return
	}
	writeJSON(w, 202, map[string]any{"run": run})
}

func (s *server) command(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID         string            `json:"id"`
		RunID      string            `json:"run_id"`
		Kind       state.CommandKind `json:"kind"`
		Text       string            `json:"text,omitempty"`
		QuestionID string            `json:"question_id,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	valid := validID(body.ID) && validID(body.RunID) && len(body.Text) <= state.MaxMessageBytes && len(body.QuestionID) <= 256
	switch body.Kind {
	case "steer":
		valid = valid && body.Text != "" && body.QuestionID == ""
	case "answer":
		valid = valid && body.Text != "" && strings.TrimSpace(body.QuestionID) != ""
	case "continue", "approve", "decline":
		valid = valid && body.Text == "" && strings.TrimSpace(body.QuestionID) != ""
	case "stop":
		valid = valid && body.Text == "" && body.QuestionID == ""
	default:
		valid = false
	}
	if !valid {
		writeJSON(w, 400, map[string]string{
			"error": "Send a valid task command with UUID id and run_id, and the current question_id for Answer or Continue.",
		})
		return
	}
	command := state.Command{ID: body.ID, RunID: body.RunID, Kind: body.Kind, Text: body.Text, QuestionID: body.QuestionID}
	receipts, err := s.Tasks.AcceptBatch(r.Context(), []state.Command{command}, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.wakeConversation(r.Context()); err != nil {
		log.Printf("accepted task command wake failed: %v", err)
		writeJSON(w, 503, map[string]any{
			"error":    "Your task command is saved, but delivery could not be confirmed. Retry the same request.",
			"accepted": true, "retryable": true, "command": receipts[0].Command, "run": receipts[0].Run,
		})
		return
	}
	writeJSON(w, http.StatusAccepted, receipts[0])
}

func (s *server) cancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !validID(body.ID) {
		writeJSON(w, 400, map[string]string{"error": "id must be a UUID"})
		return
	}
	run, err := s.Tasks.Cancel(r.Context(), body.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !s.confirmWake(w, r, run) {
		return
	}
	writeJSON(w, 202, map[string]any{"run": run})
}

func (s *server) trace(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RunID string `json:"run_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !validID(body.RunID) {
		writeJSON(w, 400, map[string]string{"error": "run_id must be a UUID"})
		return
	}
	steps, err := s.Tasks.Store.TaskSteps(r.Context(), body.RunID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"steps": steps})
}

func (s *server) artifact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RunID      string `json:"run_id"`
		ArtifactID string `json:"artifact_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !validID(body.RunID) || !validID(body.ArtifactID) {
		writeJSON(w, 400, map[string]string{"error": "run_id and artifact_id must be UUIDs"})
		return
	}
	data, err := s.Tasks.Store.GetPNG(r.Context(), body.RunID, body.ArtifactID)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// confirmWake schedules the durable fallback for an accepted task update. It
// writes the retryable 503 itself and returns false when the wake could not be
// confirmed, because the update is saved but not yet guaranteed delivery.
func (s *server) confirmWake(w http.ResponseWriter, r *http.Request, run state.Run) bool {
	if err := s.wakeConversation(r.Context()); err != nil {
		log.Printf("accepted task command wake failed: %v", err)
		writeJSON(w, 503, map[string]any{
			"error":    "Your task update is saved, but delivery could not be confirmed. Retry the same request.",
			"accepted": true, "retryable": true, "run": run,
		})
		return false
	}
	return true
}
