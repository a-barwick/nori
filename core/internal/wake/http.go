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

func (rt *Runtime) Handler(schedule Schedule, local bool) http.Handler {
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "model": rt.Model})
	}
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("POST /healthz", health)
	mux.HandleFunc("POST /api/healthz", health)

	// Reconcile a durable input whose original enqueue was interrupted. Queue
	// notifications may duplicate; only the Firestore lease can process a turn.
	ensureWake := func(ctx context.Context, session conversation.Session) error {
		if !session.Conversation.Pending && !session.Conversation.Processing {
			return nil
		}
		wakeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return schedule(wakeCtx)
	}
	mux.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
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
		session, err := rt.Runner.SnapshotPage(r.Context(), body.Before)
		if err != nil {
			writeError(w, err)
			return
		}
		if body.Before == "" && session.Conversation.Revision == 0 && len(session.Messages) == 0 {
			if err := rt.Runner.EnsureIntroduction(r.Context()); err != nil {
				writeError(w, err)
				return
			}
			session, err = rt.Runner.Snapshot(r.Context())
			if err != nil {
				writeError(w, err)
				return
			}
		}
		var wakeErr error
		if !session.Conversation.Processing {
			wakeErr = ensureWake(r.Context(), session)
		}
		if wakeErr != nil {
			log.Printf("conversation wake reconciliation failed: %v", wakeErr)
			writeJSON(w, 503, map[string]any{"error": "Conversation delivery is unavailable. Retry shortly.", "retryable": true, "session": session})
			return
		}
		writeJSON(w, 200, session)
	})
	mux.HandleFunc("POST /api/turn", func(w http.ResponseWriter, r *http.Request) {
		var input conversation.Input
		if !decode(w, r, &input) {
			return
		}
		input.Message = strings.TrimSpace(input.Message)
		if !validID(input.ID) || input.Message == "" || len(input.Message) > 8192 {
			writeJSON(w, 400, map[string]string{"error": "A UUID id and a message of 1–8192 bytes are required."})
			return
		}
		session, err := rt.Runner.HandleInput(r.Context(), input)
		if err != nil {
			writeError(w, err)
			return
		}
		if err := ensureWake(r.Context(), session); err != nil {
			log.Printf("accepted conversation input wake failed: %v", err)
			writeJSON(w, 503, map[string]any{"error": "Your message is saved, but delivery could not be confirmed. Retry with the same message ID.", "accepted": true, "retryable": true, "session": session})
			return
		}
		writeJSON(w, http.StatusAccepted, session)
	})
	mux.HandleFunc("POST /api/process", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decode(w, r, &body) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 9*time.Minute)
		defer cancel()
		if err := rt.Process(ctx); err != nil {
			// Never acknowledge Cloud Tasks while another request owns the lease
			// or a bounded slice still has work; retry the durable queue item.
			log.Printf("conversation process requires retry: %v", err)
			writeJSON(w, 503, map[string]any{"error": "Conversation processing will retry.", "retryable": true})
			return
		}
		writeJSON(w, 200, map[string]bool{"processed": true})
	})
	mux.HandleFunc("POST /api/resume", func(w http.ResponseWriter, r *http.Request) {
		var body struct{}
		if !decode(w, r, &body) {
			return
		}
		if err := task.Deliver(r.Context(), rt.Tasks.Store, rt.Tasks.Client); err != nil {
			writeError(w, err)
			return
		}
		if err := task.Wake(r.Context(), rt.Tasks.Store, rt.Tasks.Client); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"resumed": true})
	})
	mux.HandleFunc("POST /api/input", func(w http.ResponseWriter, r *http.Request) {
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
		run, err := rt.Tasks.Update(r.Context(), body.ID, body.RunID, body.Message)
		if err != nil {
			writeError(w, err)
			return
		}
		if !scheduleCommand(w, r, schedule, run) {
			return
		}
		writeJSON(w, 202, map[string]any{"run": run})
	})
	mux.HandleFunc("POST /api/cancel", func(w http.ResponseWriter, r *http.Request) {
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
		run, err := rt.Tasks.Cancel(r.Context(), body.ID)
		if err != nil {
			writeError(w, err)
			return
		}
		if !scheduleCommand(w, r, schedule, run) {
			return
		}
		writeJSON(w, 202, map[string]any{"run": run})
	})
	mux.HandleFunc("POST /api/trace", func(w http.ResponseWriter, r *http.Request) {
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
		steps, err := rt.Tasks.Store.TaskSteps(r.Context(), body.RunID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"steps": steps})
	})
	mux.HandleFunc("POST /api/artifact", func(w http.ResponseWriter, r *http.Request) {
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
		data, err := rt.Tasks.Store.GetPNG(r.Context(), body.RunID, body.ArtifactID)
		if err != nil {
			writeError(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	})
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

func scheduleCommand(w http.ResponseWriter, r *http.Request, schedule Schedule, run state.Run) bool {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := schedule(ctx); err != nil {
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
	case errors.Is(err, state.ErrBusy), errors.Is(err, state.ErrDuplicate), errors.Is(err, conversation.ErrBusy), errors.Is(err, conversation.ErrConflict):
		status = 409
	}
	log.Printf("request failed: %v", err)
	message := err.Error()
	if status == 502 {
		message = "Nori could not finish this request. Check service configuration and logs."
	}
	writeJSON(w, status, map[string]string{"error": message})
}
