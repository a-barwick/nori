package state

import "time"

// Kinds of log event. Each names a record that changed, and the stream reads its current state when it sends the event.
const (
	EventMessage      = "message"
	EventTask         = "task"
	EventConversation = "conversation"
	EventResult       = "result"
)

// EventReplayLimit is how far behind a client may resume. A client further behind starts again from a snapshot.
const EventReplayLimit = 1000

// LogEvent is one entry in the owner's ordered event log. It's written in the transaction that changes the record it
// names, so the log can't miss or invent a change.
type LogEvent struct {
	Seq       int64     `json:"seq" firestore:"seq"`
	Kind      string    `json:"kind" firestore:"kind"`
	Ref       string    `json:"ref,omitempty" firestore:"ref,omitempty"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
}

// CoalesceEvents keeps the last event for each record, in order. The stream reads current state, so an earlier event
// for the same record would send the same thing twice.
func CoalesceEvents(events []LogEvent) []LogEvent {
	last := map[[2]string]int64{}
	for _, event := range events {
		last[[2]string{event.Kind, event.Ref}] = event.Seq
	}
	kept := make([]LogEvent, 0, len(last))
	for _, event := range events {
		if last[[2]string{event.Kind, event.Ref}] == event.Seq {
			kept = append(kept, event)
		}
	}
	return kept
}
