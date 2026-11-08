package workerservice

import (
	"context"
	"errors"
	"log"
	"time"

	"nori/internal/dispatch"
	"nori/internal/state"
	"nori/internal/wake"
)

type Worker interface {
	Activity() (workflowIDs []string, active int, quiet time.Duration)
	// Stop stops polling and waits for running activities.
	Stop()
}

// Slice is one worker lifetime. A bounded slice belongs to one wake request: it
// polls for a window, exits once the build has nothing left to run, and hands
// anything unfinished to a fresh wake, so no worker idles between requests. An
// unbounded slice polls until its process stops, which is how an always-on
// worker runs.
type Slice struct {
	ID      string
	Build   string
	Known   []string
	Bounded bool
	Steps   dispatch.Steps
	// PollWindow ends a bounded slice's polling; IdleWindow is how long a bounded
	// slice must be quiet before it checks whether it can exit.
	PollWindow, IdleWindow time.Duration
	Waker                  wake.Waker
}

func (s *Slice) Poll(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.Bounded {
		return context.WithTimeout(ctx, s.PollWindow)
	}
	return context.WithCancel(ctx)
}

// Paused ends a slice that finds admission closed before starting a worker. A
// bounded slice first wakes the executions its request named again, so the wake
// is retained for resume instead of being acknowledged away.
func (s *Slice) Paused(ctx context.Context) error {
	// The operator fences the native queue before closing admission. Retain this
	// dispatched wake without starting any SDK worker.
	if s.Bounded && len(s.Known) > 0 {
		if err := s.Waker.WakeWorker(ctx, s.Build, s.Known...); err != nil {
			return err
		}
		log.Printf("worker_slice slice=%s build=%s phase=paused-retained workflows=%q", s.ID, s.Build, s.Known)
	}
	log.Print("execution paused; worker exiting without cancelling durable tasks")
	return nil
}

// Quiet reports whether a bounded slice can end: nothing has run for the idle
// window and the engine has no more work for this build.
func (s *Slice) Quiet(ctx context.Context, w Worker, d Dispatch) (bool, error) {
	if !s.Bounded {
		return false, nil
	}
	return s.settled(ctx, w, d, s.IdleWindow)
}

// settled checks both running activities and the workflow steps between them.
func (s *Slice) settled(ctx context.Context, w Worker, d Dispatch, quietWindow time.Duration) (bool, error) {
	ids, active, quiet := w.Activity()
	if active != 0 || quiet < quietWindow {
		return false, nil
	}
	pending, err := d.Runnable(ctx, s.Build, ids)
	return err == nil && len(pending) == 0, err
}

// Finish stops the worker, draining running activities, and returns result
// joined with any handoff failure. A bounded slice then hands off: while paused
// it retains every execution it saw; otherwise it wakes whatever the engine
// still has runnable for this build.
func (s *Slice) Finish(w Worker, d Dispatch, result error) error {
	_, active, _ := w.Activity()
	log.Printf("worker_slice slice=%s build=%s phase=draining active=%d", s.ID, s.Build, active)
	w.Stop()
	log.Printf("worker_slice slice=%s build=%s phase=stopped", s.ID, s.Build)
	if !s.Bounded {
		return result
	}
	check, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Activities only commit outcomes. After draining them, this coordinator
	// confirms the API wake, including outcomes committed after the last poll.
	// Failure keeps the incoming queue notification retryable without changing
	// the task's already committed success or question into a task failure.
	if s.Steps&dispatch.ConversationWake != 0 {
		result = errors.Join(result, d.Reconcile(check, dispatch.ConversationWake))
	}
	control, err := d.Control(check)
	if err != nil {
		return errors.Join(result, err)
	}
	ids, _, _ := w.Activity()
	if control.Paused {
		if len(ids) > 0 {
			result = errors.Join(result, s.Waker.WakeWorker(check, s.Build, ids...))
		}
		log.Printf("worker_slice slice=%s build=%s phase=paused-retained workflows=%q", s.ID, s.Build, ids)
		return result
	}
	pending, err := d.Runnable(check, s.Build, ids)
	if err == nil && len(pending) > 0 {
		err = s.Waker.WakeWorker(check, s.Build, pending...)
	}
	log.Printf("worker_slice slice=%s build=%s phase=handoff runnable=%q error=%v", s.ID, s.Build, pending, err)
	return errors.Join(result, err)
}

const (
	tick        = 2 * time.Second
	repairEvery = 30 * time.Second
)

// Loop reconciles while a worker polls: every tick it delivers saved commands
// and wakes, initially and every 30 seconds it also repairs closed executions.
// This process never opens admission or resumes a paused task on its own.
// Reconciliation failures keep a bounded request retryable when polling ends,
// without cutting short polling for unrelated healthy executions.
// On pause, an unfenced poller first settles admitted bookkeeping to a durable
// wait or end. Admission guards prevent new execution while it does so.
func (s *Slice) Loop(ctx context.Context, w Worker, d Dispatch) error {
	log.Printf("worker_slice slice=%s build=%s phase=polling poll_window=%s", s.ID, s.Build, s.PollWindow)
	var lastRepair time.Time
	var reconcileErr error
	pausing := false
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return reconcileErr
		}
		pass := s.Steps
		if time.Since(lastRepair) >= repairEvery {
			lastRepair = time.Now()
			pass |= dispatch.RepairClosed
		}
		if err := d.Reconcile(ctx, pass); err != nil && ctx.Err() == nil {
			if s.Bounded {
				// Retain the failure even if later, narrower passes succeed.
				// They may not retry the failed closed-execution repair.
				reconcileErr = err
			}
			log.Printf("worker reconciliation: %v", err)
		}
		if quiet, err := s.Quiet(ctx, w, d); err != nil {
			if ctx.Err() != nil {
				return reconcileErr
			}
			return errors.Join(reconcileErr, err)
		} else if quiet {
			return reconcileErr
		}
		select {
		case <-ctx.Done():
			return reconcileErr
		case <-ticker.C:
		}
		control, err := d.Control(ctx)
		if err != nil {
			log.Print("control unavailable; worker exiting")
			return reconcileErr
		}
		if err = control.Check(); err != nil {
			// Request-bound workers retain their work for resume behind the native
			// queue fence. An unfenced poller can first finish admitted bookkeeping.
			if s.Bounded {
				log.Print("execution paused; worker exiting without cancelling durable tasks")
				return reconcileErr
			}
			if !pausing {
				log.Print("execution paused; settling admitted work before stopping polling")
				pausing = true
			}
			settled, err := s.settled(ctx, w, d, 0)
			if err != nil {
				return err
			}
			if settled {
				return nil
			}
		}
	}
}

type Dispatch interface {
	Control(context.Context) (state.Control, error)
	Reconcile(context.Context, dispatch.Steps) error
	Runnable(context.Context, string, []string) ([]string, error)
}
