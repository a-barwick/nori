package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"nori/internal/chat"
	"nori/internal/conversation"
	"nori/internal/httpjson"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/wake"
)

type Deps struct {
	Features settings.Features
	Chats    *chat.Service
	Model    string
	Runner   *conversation.Runner
	Tasks    *conversation.Tasks
	Waker    wake.Waker
	Ops      Operations
	// Assistant serves the /v1 management routes. Nil leaves them unregistered.
	Assistant *Assistant
}

type Operations interface {
	// Process drains saved conversation work and repairs task delivery. It is the
	// handler of /api/process, so an error makes the queue retry.
	Process(ctx context.Context) error
	// Resume repairs delivery and wakes retained workflows after the operator
	// reopens admission.
	Resume(ctx context.Context) error
	// RepairWake derives missing notification demand from all of this user's
	// committed work, independently of the conversation a client is viewing.
	RepairWake(ctx context.Context) error
}

type server struct {
	Deps
}

func Handler(deps Deps) http.Handler {
	s := &server{deps}
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "model": s.Model})
	}
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("POST /healthz", jsonBody(health))
	mux.HandleFunc("POST /api/healthz", jsonBody(health))
	mux.HandleFunc("GET /v1/capabilities", s.capabilities)
	if s.Chats != nil {
		mux.HandleFunc("GET /v1/conversations", s.listChats)
		mux.HandleFunc("POST /v1/conversations", jsonBody(s.createChat))
		mux.HandleFunc("GET /v1/conversations/{conversation}", s.conversationRoute(s.chatSnapshot))
		mux.HandleFunc("GET /v1/conversations/{conversation}/messages", s.conversationRoute(s.chatHistory))
		mux.HandleFunc("GET /v1/conversations/{conversation}/operations", s.conversationRoute(s.conversationOperations))
		mux.HandleFunc("DELETE /v1/conversations/{conversation}", s.conversationRoute(s.deleteChat))
		mux.HandleFunc("POST /v1/conversations/{conversation}/messages", s.conversationRoute(jsonBody(s.chatMessage)))
		mux.HandleFunc("POST /v1/conversations/{conversation}/cancel", s.conversationRoute(jsonBody(s.chatCancel)))
		mux.HandleFunc("GET /v1/conversations/{conversation}/events", s.conversationRoute(s.chatEvents))
		mux.HandleFunc("POST /v1/conversations/{conversation}/uploads", s.conversationRoute(s.chatUpload))
		mux.HandleFunc("GET /v1/conversations/{conversation}/uploads/{id}", s.conversationRoute(s.chatReadUpload))
	}
	if s.Assistant != nil {
		s.Assistant.register(mux)
	}
	mux.HandleFunc("POST /api/process", jsonBody(s.process))
	mux.HandleFunc("POST /api/resume", jsonBody(s.resume))
	mux.HandleFunc("POST /api/session", s.assistantRoute(jsonBody(s.session)))
	mux.HandleFunc("POST /api/turn", s.assistantRoute(jsonBody(s.turn)))
	mux.HandleFunc("POST /api/input", s.assistantRoute(jsonBody(s.input)))
	mux.HandleFunc("POST /api/command", s.assistantRoute(jsonBody(s.command)))
	mux.HandleFunc("POST /api/cancel", s.assistantRoute(jsonBody(s.cancel)))
	mux.HandleFunc("POST /api/trace", s.assistantRoute(jsonBody(s.trace)))
	mux.HandleFunc("POST /api/artifact", s.assistantRoute(jsonBody(s.artifact)))
	mux.HandleFunc("GET /v1/snapshot", s.assistantRoute(s.snapshot))
	mux.HandleFunc("GET /v1/events", s.assistantRoute(s.events))
	mux.HandleFunc("POST /v1/uploads", s.assistantRoute(s.upload))
	mux.HandleFunc("GET /v1/uploads/{id}", s.assistantRoute(s.readUpload))
	mux.HandleFunc("GET /v1/results/{id}", s.assistantRoute(s.readResult))
	mux.HandleFunc("GET /v1/results/{id}/file", s.assistantRoute(s.readResultFile))
	mux.HandleFunc("POST /api/delivered", s.assistantRoute(jsonBody(s.delivered)))
	mux.HandleFunc("POST /api/seen", s.assistantRoute(jsonBody(s.seen)))
	return mux
}

func jsonBody(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
		if strings.TrimSpace(mediaType) != "application/json" {
			writeJSON(w, 415, map[string]string{"error": "Content-Type must be application/json."})
			return
		}
		handler(w, r)
	}
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
	case errors.Is(err, state.ErrUploadType):
		status = 415
	case errors.Is(err, state.ErrPaused):
		status = 403
	case errors.Is(err, state.ErrNotFound):
		status = 404
	case errors.Is(err, state.ErrCommandInvalid), errors.Is(err, state.ErrAttachmentMissing):
		status = 400
	case errors.Is(err, state.ErrBusy), errors.Is(err, state.ErrDuplicate), errors.Is(err, state.ErrQuestionStale),
		errors.Is(err, state.ErrSuperseded), errors.Is(err, state.ErrCancelled), errors.Is(err, state.ErrConversationBusy),
		errors.Is(err, state.ErrConversationConflict), errors.Is(err, state.ErrConversationStale), errors.Is(err, state.ErrChatMessageTooLarge),
		errors.Is(err, state.ErrMessageConflict), errors.Is(err, state.ErrChatBusy):
		status = 409
	}
	log.Printf("request failed: %v", err)
	message := err.Error()
	if status == 502 {
		message = "Nori could not finish this request. Check service configuration and logs."
	}
	writeJSON(w, status, map[string]string{"error": message})
}
