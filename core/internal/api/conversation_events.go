package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nori/internal/state"
)

// Both modes use unnamed SSE messages with the same envelope. Assistant records
// are hydrated server-side, in log order, without coalescing cursor positions.
// Payloads are current upserts, not historical versions of mutable records.
func (s *server) assistantEvents(w http.ResponseWriter, r *http.Request) {
	store := s.Tasks.Store
	after, valid := conversationResumePosition(r)
	if !valid {
		writeJSON(w, 400, map[string]string{"error": "Invalid event position."})
		return
	}
	position, err := store.EventPosition(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if after > position {
		writeJSON(w, 400, map[string]string{"error": "Event position is ahead of the conversation."})
		return
	}
	control := http.NewResponseController(w)
	_ = control.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	send := func(e conversationEvent) error {
		data, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, data); err != nil {
			return err
		}
		return control.Flush()
	}
	if position-after > state.EventReplayLimit {
		_ = send(conversationEvent{Kind: "reset", Seq: position, Conversation: assistantHeaderZero(position), Messages: []messageView{}})
		return
	}
	_ = control.Flush()
	ctx, cancel := context.WithTimeout(r.Context(), streamLifetime)
	defer cancel()
	batches := make(chan []state.LogEvent)
	failed := make(chan error, 1)
	go func() {
		failed <- store.WatchEvents(ctx, after, func(batch []state.LogEvent) error {
			select {
			case batches <- batch:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-failed:
			if err != nil && ctx.Err() == nil {
				log.Printf("conversation event stream ended: %v", err)
			}
			return
		case <-heartbeat.C:
			if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if control.Flush() != nil {
				return
			}
		case batch := <-batches:
			for _, event := range batch {
				if event.Seq <= after {
					continue
				}
				value, err := s.assistantEvent(ctx, event)
				if errors.Is(err, state.ErrNotFound) {
					value.Missing = true
					err = nil
				}
				if err != nil {
					log.Printf("conversation event %d: %v", event.Seq, err)
					return
				}
				if send(value) != nil {
					return
				}
				after = event.Seq
			}
		}
	}
}
func assistantHeaderZero(position int64) conversationHeader {
	return conversationHeader{ChatHeader: state.ChatHeader{ID: "assistant", Title: "Nori", Mode: "assistant", Position: position}, TurnPolicy: "fold_into_turn"}
}
func (s *server) assistantEvent(ctx context.Context, e state.LogEvent) (conversationEvent, error) {
	v := conversationEvent{Seq: e.Seq, Kind: e.Kind, Conversation: assistantHeaderZero(e.Seq), Messages: []messageView{}}
	store := s.Tasks.Store
	presence, err := s.Runner.Presence(ctx)
	if err != nil {
		return v, err
	}
	v.Conversation = assistantHeader(presence.Conversation, e.Seq)
	switch e.Kind {
	case state.EventMessage:
		m, err := store.ConversationMessage(ctx, e.Ref)
		if err != nil {
			return v, err
		}
		v.Messages = append(v.Messages, assistantMessage(m))
	case state.EventTask:
		run, err := store.GetRun(ctx, e.Ref)
		if err != nil {
			return v, err
		}
		v.Tasks = []state.Run{run}
	case state.EventResult:
		result, err := store.GetResult(ctx, e.Ref)
		if err != nil {
			return v, err
		}
		v.Results = []state.Result{result.Summary()}
	case state.EventConversation:
		v.Conversation.Paused = &presence.Paused
		if presence.Conversation.Pending && !presence.Conversation.Processing {
			if err = s.wakeConversation(ctx); err != nil {
				log.Printf("conversation wake repair: %v", err)
			}
		}
	case state.EventReadPosition:
		p, err := store.ReadPosition(ctx)
		if err != nil {
			return v, err
		}
		v.ReadPosition = &p
	case state.EventSchedule:
		schedule, err := store.GetSchedule(ctx, e.Ref)
		if err != nil {
			return v, err
		}
		v.Schedules = []state.Schedule{schedule}

	case state.EventOffer:
		offer, err := store.GetOffer(ctx, e.Ref)
		if err != nil {
			return v, err
		}
		v.Offers = []state.Offer{offer}
	case state.EventOperation:
		runID, opID, ok := strings.Cut(e.Ref, "/")
		if !ok {
			return v, state.ErrNotFound
		}
		op, err := store.Operation(ctx, runID, opID)
		if err != nil {
			return v, err
		}
		v.Operations = []operationView{{OperationRecord: op, Owner: operationOwner{Type: "task", ID: runID}}}
	}
	return v, nil
}

// Last-Event-ID wins when both cursors are supplied; zero starts a new stream.
func conversationResumePosition(r *http.Request) (int64, bool) {
	text := r.Header.Get("Last-Event-ID")
	if text == "" {
		text = r.URL.Query().Get("after")
	}
	if text == "" {
		return 0, true
	}
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	return value, err == nil && value >= 0
}
