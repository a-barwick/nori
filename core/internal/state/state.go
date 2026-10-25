// Package state owns the durable single-owner conversation and private artifacts.
package state

import (
	"context"
	"errors"
	"time"
)

const Owner = "austinbarwickdavis@gmail.com"

// CleanupGrace reserves time for stopping computer work and persisting its result.
const CleanupGrace = 20 * time.Second

var ErrBusy = errors.New("another run is active")
var ErrDuplicate = errors.New("run ID already exists")
var ErrPaused = errors.New("session paused or expired")
var ErrNotFound = errors.New("not found")

type Control struct {
	Paused    bool   `json:"paused" firestore:"paused"`
	ExpiresAt string `json:"expires_at" firestore:"expires_at"`
}

func (c Control) Deadline() (time.Time, error) {
	t, e := time.Parse(time.RFC3339, c.ExpiresAt)
	if e != nil || c.Paused || !t.After(time.Now()) {
		return time.Time{}, ErrPaused
	}
	return t, nil
}

type Message struct {
	ID        string    `json:"id" firestore:"id"`
	RunID     string    `json:"run_id" firestore:"run_id"`
	Role      string    `json:"role" firestore:"role"`
	Text      string    `json:"text" firestore:"text"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
}
type Event struct {
	Kind      string    `json:"kind" firestore:"kind"`
	Text      string    `json:"text" firestore:"text"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
}
type Artifact struct {
	ID   string `json:"id" firestore:"id"`
	Kind string `json:"kind" firestore:"kind"`
}
type Run struct {
	ID              string     `json:"id" firestore:"id"`
	Status          string     `json:"status" firestore:"status"`
	CancelRequested bool       `json:"cancel_requested" firestore:"cancel_requested"`
	StartedAt       time.Time  `json:"started_at" firestore:"started_at"`
	Deadline        time.Time  `json:"deadline" firestore:"deadline"`
	FinishedAt      *time.Time `json:"finished_at,omitempty" firestore:"finished_at,omitempty"`
	Error           string     `json:"error,omitempty" firestore:"error,omitempty"`
	Events          []Event    `json:"events" firestore:"events"`
	Artifacts       []Artifact `json:"artifacts" firestore:"artifacts"`
}
type Session struct {
	Messages  []Message `json:"messages"`
	Runs      []Run     `json:"runs"`
	Paused    bool      `json:"paused"`
	ExpiresAt string    `json:"expires_at"`
}
type Store interface {
	Control(context.Context) (Control, error)
	Session(context.Context) (Session, error)
	Begin(context.Context, string, string) (Run, error)
	GetRun(context.Context, string) (Run, error)
	RequestCancel(context.Context, string) (Run, error)
	Event(context.Context, string, Event) error
	Artifact(context.Context, string, Artifact) error
	Finish(context.Context, string, string, string, string) (Run, error)
	PutPNG(context.Context, string, string, []byte) error
	GetPNG(context.Context, string, string) ([]byte, error)
	Close() error
}
