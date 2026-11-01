// Package api serves Nori's HTTP routes. Handlers validate requests and call the
// conversation, task and wake contracts; they know no infrastructure.
package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"nori/internal/conversation"
	"nori/internal/httpjson"
	"nori/internal/state"
	"nori/internal/wake"
)

// Deps is everything the routes call into.
type Deps struct {
	// Model is the outer conversation model reported by health checks.
	Model  string
	Runner *conversation.Runner
	Tasks  *conversation.Tasks
	Waker  wake.Waker
	Ops    Operations
}

// Operations are the process-level actions behind the internal routes.
type Operations interface {
	// Process drains saved conversation work and repairs task delivery. It is the
	// handler of /api/process, so an error makes the queue retry.
	Process(ctx context.Context) error
	// Resume repairs delivery and wakes retained workflows after the operator
	// reopens admission.
	Resume(ctx context.Context) error
}

type server struct {
	Deps
}

// Handler serves every route. local additionally rejects cross-origin requests,
// because a loopback server has no IAM in front of it.
func Handler(deps Deps, local bool) http.Handler {
	s := &server{deps}
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "model": s.Model})
	}
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("POST /healthz", health)
	mux.HandleFunc("POST /api/healthz", health)
	mux.HandleFunc("POST /api/session", s.session)
	mux.HandleFunc("POST /api/status", s.status)
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

func validID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := httpjson.Decode(w, r, value, 64<<10); err != nil {
		message := "Send a valid JSON object."
		if errors.Is(err, httpjson.ErrMultiple) {
			message = "Send one JSON object."
		}
		writeJSON(w, 400, map[string]string{"error": message})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	httpjson.Write(w, status, value)
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
	case errors.Is(err, state.ErrBusy), errors.Is(err, state.ErrDuplicate), errors.Is(err, state.ErrQuestionStale),
		errors.Is(err, state.ErrSuperseded), errors.Is(err, state.ErrCancelled), errors.Is(err, state.ErrConversationBusy),
		errors.Is(err, state.ErrConversationConflict):
		status = 409
	}
	log.Printf("request failed: %v", err)
	message := err.Error()
	if status == 502 {
		message = "Nori could not finish this request. Check service configuration and logs."
	}
	writeJSON(w, status, map[string]string{"error": message})
}
