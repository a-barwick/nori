package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nori/internal/conversation"
	"nori/internal/state"
)

// ChatContract names the HTTP contract the chat client checks before it connects.
const ChatContract = "nori-chat-2"

func (s *server) capabilities(w http.ResponseWriter, r *http.Request) {
	modes := []string{"assistant", "chat"}
	if s.Features.DisableBackgroundTasks {
		modes = []string{"chat"}
	}
	writeJSON(w, 200, map[string]any{"conversationContract": "nori-conversations-1", "contract": ChatContract, "service": "nori", "persistence": "durable", "provider": "openai", "model": s.Model, "conversationModes": modes, "features": map[string]bool{"backgroundTasks": !s.Features.DisableBackgroundTasks, "jev": !s.Features.DisableBackgroundTasks && !s.Features.DisableJev, "computer": !s.Features.DisableComputer, "calendarOffers": !s.Features.DisableBackgroundTasks && !s.Features.DisableOffers, "schedules": !s.Features.DisableBackgroundTasks && !s.Features.DisableSchedules}})
}
func (s *server) listChats(w http.ResponseWriter, r *http.Request) {
	chats, err := s.Chats.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	rows := make([]conversationHeader, 0, len(chats)+1)
	if !s.Features.DisableBackgroundTasks {
		position, err := s.Tasks.Store.EventPosition(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		presence, err := s.Runner.Presence(r.Context())
		if err != nil {
			writeError(w, err)
			return
		}
		header := assistantHeader(presence.Conversation, position)
		header.Paused = &presence.Paused
		rows = append(rows, header)
	}
	for _, c := range chats {
		rows = append(rows, conversationHeader{ChatHeader: state.ChatHeader{ID: c.ID, Title: c.Title, Mode: c.Mode, ActiveTurn: c.ActiveTurn, UpdatedAt: c.UpdatedAt}, TurnPolicy: "reject_while_running"})
	}
	writeJSON(w, 200, map[string]any{"conversations": rows})
}
func (s *server) createChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Title = strings.TrimSpace(body.Title)
	if len(body.Title) > 200 {
		writeJSON(w, 400, map[string]string{"error": "Title is too long."})
		return
	}
	c, err := s.Chats.Create(r.Context(), body.Title)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 201, chatView(c))
}
func (s *server) deleteChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("conversation")
	if !validID(id) {
		writeJSON(w, 404, map[string]string{"error": "Conversation not found."})
		return
	}
	if err := s.Chats.Delete(r.Context(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"deleted": true})
}
func (s *server) chatSnapshot(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("conversation")
	if id == "assistant" {
		view, err := s.assistantSnapshot(r.Context(), "")
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, view)
		return
	}
	c, err := s.Chats.Store.Chat(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, chatView(c))
}

// repairWake queues processing for a turn that should be running but may have
// lost its wake. Duplicate wakes are harmless: only the turn's lease runs work.
func (s *server) repairWake(ctx context.Context, c state.Chat) {
	if c.ActiveTurn == nil {
		return
	}
	if err := s.wakeConversation(ctx); err != nil {
		log.Printf("chat wake repair failed: %v", err)
	}
}

func (s *server) chatMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID          string       `json:"id"`
		Text        string       `json:"text"`
		Attachments []string     `json:"attachments,omitempty"`
		Parts       []state.Part `json:"parts,omitempty"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Parts != nil {
		if body.Text != "" || len(body.Attachments) != 0 {
			writeJSON(w, 400, map[string]string{"error": "Send parts or text and attachments, not both."})
			return
		}
		for _, p := range body.Parts {
			switch p.Type {
			case state.PartText:
				body.Text += p.Text
			case state.PartAttachment:
				body.Attachments = append(body.Attachments, p.ID)
			default:
				writeJSON(w, 400, map[string]string{"error": "User messages accept text and attachment parts."})
				return
			}
		}
	}
	body.Text = strings.TrimSpace(body.Text)
	if !validID(body.ID) || (body.Text == "" && len(body.Attachments) == 0) || len(body.Text) > state.MaxMessageBytes || len(body.Attachments) > state.MaxAttachments {
		writeJSON(w, 400, map[string]string{"error": "A UUID id and message of 1–8192 bytes or attachments are required."})
		return
	}
	for _, id := range body.Attachments {
		if !validID(id) {
			writeJSON(w, 400, map[string]string{"error": "Invalid attachment ID."})
			return
		}
	}
	if r.PathValue("conversation") == "assistant" {
		session, err := s.Runner.HandleInput(r.Context(), conversation.Input{ID: body.ID, Message: body.Text, Attachments: body.Attachments})
		if err != nil {
			writeError(w, err)
			return
		}
		view, err := s.assistantView(r.Context(), session)
		if err != nil {
			writeError(w, err)
			return
		}
		if err = s.ensureWake(r.Context(), session); err != nil {
			writeJSON(w, 503, map[string]any{"accepted": true, "conversation": view, "error": "Message saved; retry with the same ID to confirm delivery."})
			return
		}
		writeJSON(w, 202, map[string]any{"turnId": body.ID, "conversation": view})
		return
	}
	c, err := s.Chats.Accept(r.Context(), r.PathValue("conversation"), body.ID, body.Text, body.Attachments)
	if errors.Is(err, state.ErrChatBusy) {
		// The open turn may have lost its wake; a new message is a good moment to repair it.
		if current, e := s.Chats.Store.Chat(r.Context(), r.PathValue("conversation")); e == nil {
			s.repairWake(r.Context(), current)
		}
	}
	if err != nil {
		writeError(w, err)
		return
	}
	if c.ActiveTurn != nil {
		if err = s.wakeConversation(r.Context()); err != nil {
			writeJSON(w, 503, map[string]any{"error": "Message saved; retry with the same ID to confirm delivery.", "accepted": true, "conversation": chatView(c)})
			return
		}
	}
	writeJSON(w, 202, map[string]any{"turnId": body.ID, "conversation": chatView(c)})
}
func (s *server) chatCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TurnID string `json:"turnId"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !validID(body.TurnID) {
		writeJSON(w, 400, map[string]string{"error": "Invalid turn ID."})
		return
	}
	if r.PathValue("conversation") == "assistant" {
		if err := s.Tasks.Store.CancelConversation(r.Context(), body.TurnID); err != nil {
			writeError(w, err)
			return
		}
		view, err := s.assistantSnapshot(r.Context(), "")
		if err != nil {
			writeError(w, err)
			return
		}
		if err = s.wakeConversation(r.Context()); err != nil {
			writeJSON(w, 503, map[string]any{"accepted": true, "conversation": view, "error": "Stop is saved; delivery unavailable."})
			return
		}
		writeJSON(w, 200, view)
		return
	}
	c, err := s.Chats.Cancel(r.Context(), r.PathValue("conversation"), body.TurnID)
	if err != nil {
		writeError(w, err)
		return
	}
	if err = s.wakeConversation(r.Context()); err != nil {
		writeJSON(w, 503, map[string]any{"accepted": true, "conversation": chatView(c), "error": "Stop is saved; delivery unavailable."})
		return
	}
	writeJSON(w, 200, chatView(c))
}

// chatEvents follows a chat's event log with a database listener, so an idle
// stream costs nothing. Like the assistant's stream it ends before the web
// service's request limit, and the client resumes from its last event.
func (s *server) chatEvents(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("conversation") == "assistant" {
		s.assistantEvents(w, r)
		return
	}
	id := r.PathValue("conversation")
	after, valid := conversationResumePosition(r)
	if !valid {
		writeJSON(w, 400, map[string]string{"error": "Invalid event position."})
		return
	}

	c, err := s.Chats.Store.Chat(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if after > c.Position {
		writeJSON(w, 400, map[string]string{"error": "Event position is ahead of the conversation."})
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	send := func(value any, seq int64) error {
		data, e := json.Marshal(value)
		if e != nil {
			return e
		}
		if _, e = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", seq, data); e != nil {
			return e
		}
		return controller.Flush()
	}
	// A client too far behind reloads the snapshot instead of replaying every event.
	if c.Position-after > state.ChatEventReplayLimit {
		_ = send(conversationEvent{Seq: c.Position, Kind: "reset", Conversation: conversationHeader{ChatHeader: c.Header(), TurnPolicy: "reject_while_running"}, Messages: []messageView{}}, c.Position)
		return
	}
	_ = controller.Flush()
	s.repairWake(r.Context(), c)
	ctx, cancel := context.WithTimeout(r.Context(), streamLifetime)
	defer cancel()
	batches := make(chan []state.ChatEvent)
	failed := make(chan error, 1)
	go func() {
		failed <- s.Chats.Store.WatchChatEvents(ctx, id, after, func(batch []state.ChatEvent) error {
			select {
			case batches <- batch:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-failed:
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				log.Printf("chat event stream ended: %v", err)
			}
			return
		case <-heartbeat.C:
			if _, e := io.WriteString(w, ": keepalive\n\n"); e != nil || controller.Flush() != nil {
				return
			}
		case batch := <-batches:
			for _, event := range batch {
				if event.Seq <= after {
					continue
				}
				if send(chatEventView(event), event.Seq) != nil {
					return
				}
				after = event.Seq
			}
		}
	}
}
func (s *server) chatUpload(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("conversation") == "assistant" {
		s.upload(w, r)
		return
	}
	id := r.URL.Query().Get("id")
	if !validID(id) {
		writeJSON(w, 400, map[string]string{"error": "Invalid upload ID."})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, state.MaxUploadBytes))
	if err != nil {
		writeJSON(w, 413, map[string]string{"error": "File exceeds limit or upload interrupted."})
		return
	}
	u, err := s.Chats.SaveUpload(r.Context(), r.PathValue("conversation"), id, r.URL.Query().Get("filename"), r.Header.Get("Content-Type"), data)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, u.Attachment)
}
func (s *server) chatReadUpload(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("conversation") == "assistant" {
		s.readUpload(w, r)
		return
	}
	id := r.PathValue("id")
	if !validID(id) {
		writeJSON(w, 404, map[string]string{"error": "File not found."})
		return
	}
	u, data, err := s.Chats.ReadUpload(r.Context(), r.PathValue("conversation"), id)
	if err != nil {
		writeError(w, err)
		return
	}
	serveFile(w, u.Name, u.MediaType, data)
}

func (s *server) chatHistory(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("conversation") == "assistant" {
		view, err := s.assistantSnapshot(r.Context(), r.URL.Query().Get("before"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"messages": view.Messages, "nextCursor": view.NextCursor})
		return
	}
	before := int64(0)
	if value := r.URL.Query().Get("before"); value != "" {
		var err error
		before, err = strconv.ParseInt(value, 10, 64)
		if err != nil || before < 1 {
			writeJSON(w, 400, map[string]string{"error": "Invalid message position."})
			return
		}
	}
	page, err := s.Chats.Store.ChatMessages(r.Context(), r.PathValue("conversation"), before)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"messages": chatMessages(page.Messages), "nextBefore": page.NextBefore, "nextCursor": chatCursor(page.NextBefore)})
}
