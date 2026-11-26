package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"nori/internal/state"
)

const streamHeartbeat = 20 * time.Second

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	store := s.Tasks.Store
	stream := prepareEventStream(w, "no-cache, no-transform")
	position, err := store.EventPosition(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	after, supplied, valid := resumePosition(r)
	if !supplied || !valid || after > position || position-after > state.EventReplayLimit {
		w.WriteHeader(200)
		stream.send("reset", 0, map[string]int64{"position": position})
		return
	}
	w.WriteHeader(200)
	ctx, cancel := context.WithTimeout(r.Context(), streamLifetime)
	defer cancel()

	batches, failed := watchEventBatches(ctx, func(ctx context.Context, deliver func([]state.LogEvent) error) error {
		return store.WatchEvents(ctx, after, deliver)
	})

	var deltaCh <-chan AssistantDelta
	if s.Deltas != nil {
		var unsub func()
		deltaCh, unsub = s.Deltas.Subscribe()
		defer unsub()
	}

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
				slog.WarnContext(ctx, "event stream ended", "err", err)
			}
			return
		case delta := <-deltaCh:
			if stream.send("assistant_delta", 0, delta) != nil {
				return
			}
		case <-heartbeat.C:
			if stream.comment("heartbeat") != nil {
				return
			}
			presence = s.sendPresence(ctx, stream, presence)
			open = s.sendOpenTasks(ctx, stream, open)
		case batch := <-batches:
			for drained := false; !drained && deltaCh != nil; {
				select {
				case <-deltaCh:
				default:
					drained = true
				}
			}
			for _, event := range state.CoalesceEvents(batch) {
				err := s.sendEvent(ctx, stream, event)
				if errors.Is(err, state.ErrNotFound) {
					err = stream.send("unknown", event.Seq, map[string]string{"kind": event.Kind})
				}
				if err != nil {
					slog.WarnContext(ctx, "event send failed", "seq", event.Seq, "kind", event.Kind, "err", err)
					return
				}
			}
		}
	}
}

func (s *server) sendEvent(ctx context.Context, stream eventStream, event state.LogEvent) error {
	switch event.Kind {
	case state.EventDataReset:
		return stream.send("reset", event.Seq, map[string]int64{"position": event.Seq})
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
	case state.EventReadPosition:
		position, err := s.Tasks.Store.ReadPosition(ctx)
		if err != nil {
			return err
		}
		return stream.send(event.Kind, event.Seq, map[string]any{"read_position": position})
	case state.EventConversation:
		presence, err := s.Runner.Presence(ctx)
		if err != nil {
			return err
		}
		return stream.send(event.Kind, event.Seq, presence)
	case state.EventMemory:
		return stream.send(state.EventMemory, event.Seq, map[string]any{"ref": event.Ref})
	}
	return stream.send("unknown", event.Seq, map[string]string{"kind": event.Kind})
}

func (s *server) sendPresence(ctx context.Context, stream eventStream, last string) string {
	presence, err := s.Runner.Presence(ctx)
	if err != nil {
		return last
	}
	s.repairWake(ctx)
	encoded, err := json.Marshal(presence)
	if err != nil || string(encoded) == last {
		return last
	}
	stream.send("presence", 0, presence)
	return string(encoded)
}

func (s *server) sendOpenTasks(ctx context.Context, stream eventStream, last map[string]string) map[string]string {
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
