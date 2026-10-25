package state

import (
	"context"
	"errors"
	"time"
)

var ErrSuperseded = errors.New("task revision changed; reconcile accepted input")
var ErrCancelled = errors.New("task cancellation requested")

// Command and its delivery intent are committed in the acceptance transaction.
// ID is supplied by the caller and is stable across HTTP and relay retries.
type Command struct {
	ID        string    `json:"id" firestore:"id"`
	RunID     string    `json:"run_id" firestore:"run_id"`
	Kind      string    `json:"kind" firestore:"kind"`
	Text      string    `json:"text" firestore:"text"`
	Position  int64     `json:"position" firestore:"position"`
	Revision  int64     `json:"revision" firestore:"revision"`
	Delivered bool      `json:"delivered" firestore:"delivered"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
}

type TaskSnapshot struct {
	Run      Run
	Commands []Command
}

type TaskStore interface {
	Store
	Accept(context.Context, Command) (Run, error)
	OperationRevisions(context.Context, string) ([]int64, error)
	Pending(context.Context) ([]Command, error)
	Delivered(context.Context, string) error
	Reconcile(context.Context, string) (TaskSnapshot, error)
	Incorporate(context.Context, string, int64, int64) error
	Guard(context.Context, string, int64) (Run, error)
	Admit(context.Context, string, int64, string) error
	Publish(context.Context, string, int64, string, string, string) (Run, error)
	Project(context.Context, string, int64, string, string) error
}
