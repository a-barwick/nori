package state

import "time"

const (
	EventOperation    = "operation"
	EventMessage      = "message"
	EventTask         = "task"
	EventConversation = "conversation"
	EventResult       = "result"
	EventReadPosition = "read_position"
	EventMemory       = "memory"
)

const EventReplayLimit = 1000

type LogEvent struct {
	Seq       int64     `json:"seq" firestore:"seq"`
	Kind      string    `json:"kind" firestore:"kind"`
	Ref       string    `json:"ref,omitempty" firestore:"ref,omitempty"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
}

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
