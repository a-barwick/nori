package api

import (
	"context"
	"log"
	"net/http"
	"strings"

	"nori/internal/state"
)

// Both modes use unnamed SSE messages with the same envelope. Assistant records
// are hydrated server-side, in log order, without coalescing cursor positions.
// Payloads are current upserts, not historical versions of mutable records.
func (s *server) assistantEvents(w http.ResponseWriter, r *http.Request) {
	store := s.Tasks.Store
	after, _, valid := resumePosition(r)
	if !valid {
		writeJSON(w, 400, map[string]string{"error": "Invalid event position."})
		return
	}
	position, err := store.EventPosition(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	streamConversation(w, r, after, conversationFeed[state.LogEvent]{
		header:    assistantHeader(nil, position),
		limit:     state.EventReplayLimit,
		heartbeat: streamHeartbeat,
		comment:   "heartbeat",
		watch: func(ctx context.Context, deliver func([]state.LogEvent) error) error {
			return store.WatchEvents(ctx, after, deliver)
		},
		sequence: func(event state.LogEvent) int64 { return event.Seq },
		project:  s.assistantEvent,
	})
}

func (s *server) assistantEvent(ctx context.Context, e state.LogEvent) (conversationEvent, error) {
	v := conversationEvent{Seq: e.Seq, Kind: e.Kind, Conversation: assistantHeader(nil, e.Seq), Messages: []messageView{}}
	store := s.Tasks.Store
	presence, err := s.Runner.Presence(ctx)
	if err != nil {
		return v, err
	}
	v.Conversation = assistantHeader(&presence.Conversation, e.Seq)
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
