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
	mux := newRouteMux(true, false)
	(&server{deps}).register(mux)
	return mux.mux
}

func (s *server) register(mux *routeMux) {
	health := func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "model": s.Model})
	}
	mux.handle("GET /healthz", healthRule, health)
	mux.handle("POST /healthz", healthRule.body("json", jsonLimit), jsonBody(health))
	mux.handle("POST /api/healthz", healthRule.body("json", jsonLimit), jsonBody(health))
	mux.handle("GET /v1/capabilities", clientRead, s.capabilities)
	if s.Chats != nil || mux.manifest {
		mux.handle("GET /v1/conversations", clientRead, s.listChats)
		mux.handle("POST /v1/conversations", clientJSON, jsonBody(s.createChat))
		mux.handle("GET /v1/conversations/{conversation}", clientRead.param("conversation", "assistant|"+uuidSegment), s.conversationRoute(s.chatSnapshot))
		mux.handle("GET /v1/conversations/{conversation}/messages", clientRead.param("conversation", "assistant|"+uuidSegment), s.conversationRoute(s.chatHistory))
		mux.handle("GET /v1/conversations/{conversation}/operations", clientRead.param("conversation", "assistant|"+uuidSegment), s.conversationRoute(s.conversationOperations))
		mux.handle("DELETE /v1/conversations/{conversation}", browserRead.token("mutate").param("conversation", uuidSegment), s.conversationRoute(s.deleteChat))
		mux.handle("POST /v1/conversations/{conversation}/messages", clientJSON.param("conversation", "assistant|"+uuidSegment), s.conversationRoute(jsonBody(s.chatMessage)))
		mux.handle("POST /v1/conversations/{conversation}/cancel", clientJSON.param("conversation", "assistant|"+uuidSegment), s.conversationRoute(jsonBody(s.chatCancel)))
		mux.handle("GET /v1/conversations/{conversation}/events", clientRead.param("conversation", "assistant|"+uuidSegment).responses("text/event-stream"), s.conversationRoute(s.chatEvents))
		mux.handle("POST /v1/conversations/{conversation}/uploads", browserRead.token("mutate").param("conversation", "assistant|"+uuidSegment).body("upload", state.MaxUploadBytes), s.conversationRoute(s.chatUpload))
		mux.handle("GET /v1/conversations/{conversation}/uploads/{id}", clientRead.param("conversation", "assistant|"+uuidSegment).responses(fileTypes...).param("id", uuidSegment), s.conversationRoute(s.chatReadUpload))
	}
	if s.Assistant != nil {
		s.Assistant.register(mux)
	}
	mux.handle("POST /api/process", internal.body("json", jsonLimit), jsonBody(s.process))
	mux.handle("POST /api/resume", internal.body("json", jsonLimit), jsonBody(s.resume))
	mux.handle("POST /api/session", localOnly.body("json", jsonLimit), s.assistantRoute(jsonBody(s.session)))
	mux.handle("POST /api/turn", browserJSON, s.assistantRoute(jsonBody(s.turn)))
	mux.handle("POST /api/input", browserJSON, s.assistantRoute(jsonBody(s.input)))
	mux.handle("POST /api/command", browserJSON, s.assistantRoute(jsonBody(s.command)))
	mux.handle("POST /api/cancel", browserJSON, s.assistantRoute(jsonBody(s.cancel)))
	mux.handle("POST /api/trace", browserJSON, s.assistantRoute(jsonBody(s.trace)))
	mux.handle("POST /api/artifact", browserJSON.responses("image/png"), s.assistantRoute(jsonBody(s.artifact)))
	mux.handle("GET /v1/snapshot", browserRead, s.assistantRoute(s.snapshot))
	mux.handle("GET /v1/events", browserRead.responses("text/event-stream"), s.assistantRoute(s.events))
	mux.handle("POST /v1/uploads", browserRead.body("upload", state.MaxUploadBytes), s.assistantRoute(s.upload))
	mux.handle("GET /v1/uploads/{id}", browserRead.responses(fileTypes...).param("id", uuidSegment), s.assistantRoute(s.readUpload))
	mux.handle("GET /v1/results/{id}", clientRead.param("id", uuidSegment), s.assistantRoute(s.readResult))
	mux.handle("GET /v1/results/{id}/file", clientRead.responses(fileTypes...).param("id", uuidSegment), s.assistantRoute(s.readResultFile))
	mux.handle("POST /api/delivered", browserJSON, s.assistantRoute(jsonBody(s.delivered)))
	mux.handle("POST /api/seen", browserJSON, s.assistantRoute(jsonBody(s.seen)))
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
	if err := httpjson.Decode(w, r, value, jsonLimit); err != nil {
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
