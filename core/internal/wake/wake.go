// Package wake is the contract for waking compute that sleeps between requests.
//
// A wake carries no conversation or task content. Firestore owns what is to be
// done and in what order; a wake only makes some process look. Wakes may be
// duplicated or lost, and callers repair a lost one from saved state.
package wake

import (
	"context"
	"errors"
)

// ErrNotConfigured is returned by a Waker that has no target for the requested
// wake, for example a worker-only adapter asked to wake the conversation.
var ErrNotConfigured = errors.New("wake target is not configured")

type Waker interface {
	// WakeConversation asks the API to process saved conversation input. It
	// returns only after the transport has accepted the wake, so the caller can
	// keep its saved intent unacknowledged on any error.
	WakeConversation(ctx context.Context) error
	// WakeWorker asks the immutable worker build to poll for the named workflow
	// executions. Like WakeConversation, it returns only once the wake is accepted.
	WakeWorker(ctx context.Context, build string, workflowIDs ...string) error
	// WorkersNeedWake reports whether workers run only when woken. If so, a
	// signal to a parked workflow is not enough: someone must also call
	// WakeWorker, and signalling while execution is paused must be retained.
	WorkersNeedWake() bool
}

// Disabled does nothing. It is the Waker for local development, where the
// worker polls Temporal itself and nothing sleeps.
type Disabled struct{}

func (Disabled) WakeConversation(context.Context) error              { return nil }
func (Disabled) WakeWorker(context.Context, string, ...string) error { return nil }
func (Disabled) WorkersNeedWake() bool                               { return false }

// InProcess wakes the conversation loop of the same process through a channel,
// which is how local development replaces a queue. Worker wakes go to Workers.
type InProcess struct {
	Workers       Waker
	notifications chan struct{}
}

func NewInProcess(workers Waker) *InProcess {
	return &InProcess{Workers: workers, notifications: make(chan struct{}, 1)}
}

// Notifications delivers at most one pending wake; further wakes coalesce.
func (w *InProcess) Notifications() <-chan struct{} { return w.notifications }

func (w *InProcess) WakeConversation(context.Context) error {
	select {
	case w.notifications <- struct{}{}:
	default:
	}
	return nil
}

func (w *InProcess) WakeWorker(ctx context.Context, build string, workflowIDs ...string) error {
	return w.Workers.WakeWorker(ctx, build, workflowIDs...)
}

func (w *InProcess) WorkersNeedWake() bool { return w.Workers.WorkersNeedWake() }
