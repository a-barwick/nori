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
	position, err := store.EventPosition(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	stream := prepareEventStream(w, "no-cache, no-transform")
	_ = stream.control.SetWriteDeadline(time.Time{})
	after, supplied, valid := resumePosition(r)
	if !supplied || !valid || after > position || position-after > state.EventReplayLimit {
		w.WriteHeader(http.StatusOK)
		if err := stream.send("reset", 0, map[string]int64{"position": position}); err != nil {
			slog.WarnContext(r.Context(), "event stream reset send failed", "err", err)
		}
		return
	}
	w.WriteHeader(http.StatusOK)
	if err := stream.control.Flush(); err != nil {
		return
	}
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

	presence, err := s.sendPresence(ctx, stream, "")
	if err != nil {
		return
	}
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
			if presence, err = s.sendPresence(ctx, stream, presence); err != nil {
				return
			}
			if open, err = s.sendOpenTasks(ctx, stream, open); err != nil {
				return
			}
		case batch := <-batches:
			if batchSupersedesDeltas(batch) && deltaCh != nil {
				drainDeltas(deltaCh)
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

func batchSupersedesDeltas(batch []state.LogEvent) bool {
	for _, ev := range batch {
		if ev.Kind == state.EventMessage || ev.Kind == state.EventDataReset {
			return true
		}
	}
	return false
}

func drainDeltas(ch <-chan AssistantDelta) {
	for {
		select {
		case <-ch:
		default:
			return
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

func (s *server) sendPresence(ctx context.Context, stream eventStream, last string) (string, error) {
	presence, err := s.Runner.Presence(ctx)
	if err != nil {
		return last, nil
	}
	s.repairWake(ctx)
	encoded, err := json.Marshal(presence)
	if err != nil || string(encoded) == last {
		return last, nil
	}
	if err := stream.send("presence", 0, presence); err != nil {
		return last, err
	}
	return string(encoded), nil
}

func (s *server) sendOpenTasks(ctx context.Context, stream eventStream, last map[string]string) (map[string]string, error) {
	runs, err := s.Tasks.Store.OpenTasks(ctx)
	if err != nil {
		return last, nil
	}
	current := make(map[string]string, len(runs))
	for _, run := range runs {
		encoded, err := json.Marshal(run)
		if err != nil {
			continue
		}
		current[run.ID] = string(encoded)
		if last[run.ID] != string(encoded) {
			if err := stream.send(state.EventTask, 0, map[string]any{"run": run}); err != nil {
				return current, err
			}
		}
	}
	for id := range last {
		if _, still := current[id]; still {
			continue
		}
		if run, err := s.Tasks.Store.GetRun(ctx, id); err == nil {
			if err := stream.send(state.EventTask, 0, map[string]any{"run": run}); err != nil {
				return current, err
			}
		}
	}
	return current, nil
}
