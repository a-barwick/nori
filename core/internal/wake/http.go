package wake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/conversation"
	"nori/internal/state"
	"nori/internal/task"
)

type Schedule func(context.Context) error

type server struct {
	rt       *Runtime
	schedule Schedule
}

func (rt *Runtime) Handler(schedule Schedule, local bool) http.Handler {
	s := &server{rt: rt, schedule: schedule}
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "model": rt.Model})
	}
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("POST /healthz", health)
	mux.HandleFunc("POST /api/healthz", health)
	mux.HandleFunc("POST /api/session", s.session)
	mux.HandleFunc("POST /api/turn", s.turn)
	mux.HandleFunc("POST /api/process", s.process)
	mux.HandleFunc("POST /api/resume", s.resume)
	mux.HandleFunc("POST /api/input", s.input)
	mux.HandleFunc("POST /api/command", s.command)
	mux.HandleFunc("POST /api/cancel", s.cancel)
	mux.HandleFunc("POST /api/trace", s.trace)
	mux.HandleFunc("POST /api/artifact", s.artifact)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if local {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
				writeJSON(w, 403, map[string]string{"error": "Cross-origin requests are not allowed."})
				return
			}
		}
		if r.Method == http.MethodPost && strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]) != "application/json" {
			writeJSON(w, 415, map[string]string{"error": "Content-Type must be application/json."})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// ensureWake reconciles a durable input whose original enqueue was
// interrupted. Queue notifications may duplicate; only the Firestore lease
// can process a turn.
func (s *server) ensureWake(ctx context.Context, session conversation.Session) error {
	if !session.Conversation.Pending && !session.Conversation.Processing {
		return nil
	}
	wakeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return s.schedule(wakeCtx)
}

func (s *server) session(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Before string `json:"before,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Before) > 256 || strings.Contains(body.Before, "/") {
		writeJSON(w, 400, map[string]string{"error": "Invalid history cursor."})
		return
	}
	session, err := s.rt.Runner.SnapshotPage(r.Context(), body.Before)
	if err != nil {
		writeError(w, err)
		return
	}
	if body.Before == "" && session.Conversation.Revision == 0 && len(session.Messages) == 0 {
		if err := s.rt.Runner.EnsureIntroduction(r.Context()); err != nil {
			writeError(w, err)
			return
		}
		session, err = s.rt.Runner.Snapshot(r.Context())
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
	if !validID(input.ID) || input.Message == "" || len(input.Message) > 8192 {
		writeJSON(w, 400, map[string]string{"error": "A UUID id and a message of 1–8192 bytes are required."})
		return
	}
	session, err := s.rt.Runner.HandleInput(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.ensureWake(r.Context(), session); err != nil {
		log.Printf("accepted conversation input wake failed: %v", err)
		writeJSON(w, 503, map[string]any{"error": "Your message is saved, but delivery could not be confirmed. Retry with the same message ID.", "accepted": true, "retryable": true, "session": session})
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
	if err := s.rt.Process(ctx); err != nil {
		// Never acknowledge Cloud Tasks while another request owns the lease
		// or a bounded slice still has work; retry the durable queue item.
		log.Printf("conversation process requires retry: %v", err)
		writeJSON(w, 503, map[string]any{"error": "Conversation processing will retry.", "retryable": true})
		return
	}
	writeJSON(w, 200, map[string]bool{"processed": true})
}

func (s *server) resume(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	if err := task.Deliver(r.Context(), s.rt.Tasks.Store, s.rt.Tasks.Client); err != nil {
		writeError(w, err)
		return
	}
	if err := task.Wake(r.Context(), s.rt.Tasks.Store, s.rt.Tasks.Client); err != nil {
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
	if !validID(body.ID) || !validID(body.RunID) || body.Message == "" || len(body.Message) > 8192 {
		writeJSON(w, 400, map[string]string{"error": "UUID id, run_id, and a message of 1–8192 bytes are required."})
		return
	}
	run, err := s.rt.Tasks.Update(r.Context(), body.ID, body.RunID, body.Message)
	if err != nil {
		writeError(w, err)
		return
	}
	if !s.scheduleCommand(w, r, run) {
		return
	}
	writeJSON(w, 202, map[string]any{"run": run})
}

func (s *server) command(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID         string `json:"id"`
		RunID      string `json:"run_id"`
		Kind       string `json:"kind"`
		Text       string `json:"text,omitempty"`
		QuestionID string `json:"question_id,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	valid := validID(body.ID) && validID(body.RunID) && len(body.Text) <= 8192 && len(body.QuestionID) <= 256
	switch body.Kind {
	case "steer":
		valid = valid && body.Text != "" && body.QuestionID == ""
	case "answer":
		valid = valid && body.Text != "" && strings.TrimSpace(body.QuestionID) != ""
	case "continue":
		valid = valid && body.Text == "" && strings.TrimSpace(body.QuestionID) != ""
	case "stop":
		valid = valid && body.Text == "" && body.QuestionID == ""
	default:
		valid = false
	}
	if !valid {
		writeJSON(w, 400, map[string]string{"error": "Send a valid task command with UUID id and run_id, and the current question_id for Answer or Continue."})
		return
	}
	receipts, err := s.rt.Tasks.AcceptBatch(r.Context(), []state.Command{{ID: body.ID, RunID: body.RunID, Kind: body.Kind, Text: body.Text, QuestionID: body.QuestionID}}, nil)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.schedule(ctx); err != nil {
		log.Printf("accepted task command wake failed: %v", err)
		writeJSON(w, 503, map[string]any{"error": "Your task command is saved, but delivery could not be confirmed. Retry the same request.", "accepted": true, "retryable": true, "command": receipts[0].Command, "run": receipts[0].Run})
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
	run, err := s.rt.Tasks.Cancel(r.Context(), body.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	if !s.scheduleCommand(w, r, run) {
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
	steps, err := s.rt.Tasks.Store.TaskSteps(r.Context(), body.RunID)
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
	data, err := s.rt.Tasks.Store.GetPNG(r.Context(), body.RunID, body.ArtifactID)
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (s *server) scheduleCommand(w http.ResponseWriter, r *http.Request, run state.Run) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.schedule(ctx); err != nil {
		log.Printf("accepted task command wake failed: %v", err)
		writeJSON(w, 503, map[string]any{"error": "Your task update is saved, but delivery could not be confirmed. Retry the same request.", "accepted": true, "retryable": true, "run": run})
		return false
	}
	return true
}

func validID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	var raw json.RawMessage
	if err := d.Decode(&raw); err != nil {
		writeJSON(w, 400, map[string]string{"error": "Send a valid JSON object."})
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "Send one JSON object."})
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		writeJSON(w, 400, map[string]string{"error": "Send a valid JSON object."})
		return false
	}
	body := json.NewDecoder(bytes.NewReader(raw))
	body.DisallowUnknownFields()
	if err := body.Decode(value); err != nil {
		writeJSON(w, 400, map[string]string{"error": "Send a valid JSON object."})
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	status := 502
	switch {
	case errors.Is(err, state.ErrPaused):
		status = 403
	case errors.Is(err, state.ErrNotFound):
		status = 404
	case errors.Is(err, state.ErrCommandInvalid):
		status = 400
	case errors.Is(err, state.ErrBusy), errors.Is(err, state.ErrDuplicate), errors.Is(err, state.ErrQuestionStale), errors.Is(err, state.ErrSuperseded), errors.Is(err, state.ErrCancelled), errors.Is(err, conversation.ErrBusy), errors.Is(err, conversation.ErrConflict):
		status = 409
	}
	log.Printf("request failed: %v", err)
	message := err.Error()
	if status == 502 {
		message = "Nori could not finish this request. Check service configuration and logs."
	}
	writeJSON(w, status, map[string]string{"error": message})
}
