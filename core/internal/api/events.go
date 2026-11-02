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

const (
	// A stream ends before the web service's 180-second request limit, and the client resumes from the last event.
	streamLifetime = 150 * time.Second
	// The heartbeat keeps the connection open, and sees what writes no event: an expired lease or the operator's pause.
	streamHeartbeat = 20 * time.Second
)

// events sends the saved event log as server-sent events. Agent Runtime doesn't forward the Last-Event-ID header, so
// the position also comes as ?after=, which is how the web proxy passes it on.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	store := s.Tasks.Store
	stream := &eventStream{w: w, control: http.NewResponseController(w)}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	position, err := store.EventPosition(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	after, valid := resumePosition(r)
	// The client starts again from a snapshot when its position can't be resumed: the log never reached it, or it's
	// too far behind to replay.
	if !valid || after > position || position-after > state.EventReplayLimit {
		w.WriteHeader(200)
		stream.send("reset", 0, map[string]int64{"position": position})
		return
	}
	w.WriteHeader(200)
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

	presence := s.sendPresence(ctx, stream, "")
	open := map[string]string{}
	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-failed:
			if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				log.Printf("event stream ended: %v", err)
			}
			return
		case <-heartbeat.C:
			stream.comment()
			presence = s.sendPresence(ctx, stream, presence)
			open = s.sendOpenTasks(ctx, stream, open)
		case batch := <-batches:
			for _, event := range state.CoalesceEvents(batch) {
				err := s.sendEvent(ctx, stream, event)
				// A record that's gone can't be sent, and it mustn't block every event after it.
				if errors.Is(err, state.ErrNotFound) {
					err = stream.send("unknown", event.Seq, map[string]string{"kind": event.Kind})
				}
				if err != nil {
					log.Printf("event %d could not be sent: %v", event.Seq, err)
					return
				}
			}
		}
	}
}

func resumePosition(r *http.Request) (int64, bool) {
	text := r.Header.Get("Last-Event-ID")
	if text == "" {
		text = r.URL.Query().Get("after")
	}
	if text == "" {
		return 0, false
	}
	after, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	return after, err == nil && after >= 0
}

// sendEvent reads the record's current state, so a replay can't show something older than the client already has.
func (s *server) sendEvent(ctx context.Context, stream *eventStream, event state.LogEvent) error {
	switch event.Kind {
	case state.EventMessage:
		message, err := s.Tasks.Store.ConversationMessage(ctx, event.Ref)
		if err != nil {
			return err
		}
		return stream.send(event.Kind, event.Seq, map[string]any{"message": message.WithParts()})
	case state.EventTask:
		run, err := s.Tasks.Store.GetRun(ctx, event.Ref)
		if err != nil {
			return err
		}
		return stream.send(event.Kind, event.Seq, map[string]any{"run": run})
	case state.EventResult:
		result, err := s.Tasks.Store.GetResult(ctx, event.Ref)
		if err != nil {
			return err
		}
		return stream.send(event.Kind, event.Seq, map[string]any{"result": result.Summary()})
	case state.EventConversation:
		presence, err := s.Runner.Presence(ctx)
		if err != nil {
			return err
		}
		return stream.send(event.Kind, event.Seq, presence)
	}
	// A kind this server doesn't know still moves the client's position forward.
	return stream.send("unknown", event.Seq, map[string]string{"kind": event.Kind})
}

// sendPresence sends the conversation's state when it changed, and repairs a wake that was never queued. A browser's
// polling used to do that repair.
func (s *server) sendPresence(ctx context.Context, stream *eventStream, last string) string {
	presence, err := s.Runner.Presence(ctx)
	if err != nil {
		return last
	}
	if presence.Conversation.Pending && !presence.Conversation.Processing {
		if err := s.wakeConversation(ctx); err != nil {
			log.Printf("conversation wake reconciliation failed: %v", err)
		}
	}
	encoded, err := json.Marshal(presence)
	if err != nil || string(encoded) == last {
		return last
	}
	stream.send("presence", 0, presence)
	return string(encoded)
}

// sendOpenTasks catches what the log can't: a worker on an older build writes no events, and a few small changes, such
// as a new screenshot, don't either. It sends each open task that changed since the last heartbeat, and the final state
// of one that has since closed. These carry no log position.
func (s *server) sendOpenTasks(ctx context.Context, stream *eventStream, last map[string]string) map[string]string {
	runs, err := s.Tasks.Store.OpenTasks(ctx)
	if err != nil {
		return last
	}
	current := make(map[string]string, len(runs))
	for _, run := range runs {
		encoded, err := json.Marshal(run)
		if err != nil {
			continue
		}
		current[run.ID] = string(encoded)
		if last[run.ID] != string(encoded) {
			stream.send(state.EventTask, 0, map[string]any{"run": run})
		}
	}
	for id := range last {
		if _, still := current[id]; still {
			continue
		}
		if run, err := s.Tasks.Store.GetRun(ctx, id); err == nil {
			stream.send(state.EventTask, 0, map[string]any{"run": run})
		}
	}
	return current
}

type eventStream struct {
	w       http.ResponseWriter
	control *http.ResponseController
}

// send writes one event. seq is 0 for an event that isn't in the log, which leaves the client's position alone.
func (e *eventStream) send(kind string, seq int64, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	id := ""
	if seq > 0 {
		id = fmt.Sprintf("id: %d\n", seq)
	}
	if _, err = fmt.Fprintf(e.w, "%sevent: %s\ndata: %s\n\n", id, kind, data); err != nil {
		return err
	}
	return e.control.Flush()
}

func (e *eventStream) comment() {
	if _, err := fmt.Fprint(e.w, ": heartbeat\n\n"); err == nil {
		e.control.Flush()
	}
}
