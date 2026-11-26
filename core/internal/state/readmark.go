package state

import "time"

type ReadMark struct {
	MessageID string    `json:"message_id" firestore:"message_id"`
	Through   time.Time `json:"through" firestore:"through"`
	Position  int64     `json:"position" firestore:"position"`
}
