package state

import "time"

// ReadMark is the user's read position, shared by every client. It only moves forward.
type ReadMark struct {
	MessageID string    `json:"message_id" firestore:"message_id"`
	Through   time.Time `json:"through" firestore:"through"`
	// Position orders updates across snapshots and the event stream. Older saved marks have position zero.
	Position int64 `json:"position" firestore:"position"`
}
