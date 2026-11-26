package wake

import (
	"context"
	"errors"
	"nori/internal/users"
	"time"
)

var ErrNotConfigured = errors.New("wake target is not configured")

type Waker interface {
	WakeConversation(ctx context.Context) error
	WakeWorker(ctx context.Context, build string, workflowIDs ...string) error
	WorkersNeedWake() bool
}

type Disabled struct{}

func (Disabled) WakeConversation(context.Context) error              { return nil }
func (Disabled) WakeWorker(context.Context, string, ...string) error { return nil }
func (Disabled) WorkersNeedWake() bool                               { return false }

func ForUser(w Waker, id string) Waker { return userWaker{Waker: w, id: id} }

type userWaker struct {
	Waker
	id string
}

func (w userWaker) WakeConversation(ctx context.Context) error {
	return w.Waker.WakeConversation(users.WithID(ctx, w.id))
}
func (w userWaker) WakeWorker(ctx context.Context, build string, ids ...string) error {
	return w.Waker.WakeWorker(users.WithID(ctx, w.id), build, ids...)
}

type DelayedWaker interface {
	WakeConversationAt(ctx context.Context, at time.Time) error
}

func At(ctx context.Context, w Waker, at time.Time) error {
	if delayed, ok := w.(DelayedWaker); ok {
		return delayed.WakeConversationAt(ctx, at)
	}
	if !w.WorkersNeedWake() {
		return nil
	}
	return ErrNotConfigured
}

func (w userWaker) WakeConversationAt(ctx context.Context, at time.Time) error {
	return At(users.WithID(ctx, w.id), w.Waker, at)
}
