package dispatch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/observability"
	"nori/internal/state"
	"nori/internal/wake"
)

var tracer = otel.Tracer("nori/task")

// Steps selects which parts of a reconciliation pass run.
type Steps uint8

const (
	// RepairClosed publishes the terminal state of tasks whose execution closed.
	RepairClosed Steps = 1 << iota
	// Commands delivers saved task commands to their executions.
	Commands
	// Publications wakes executions holding a finished result, once no chat
	// input is waiting to be routed.
	Publications
	// ConversationWake wakes the API for saved chat input that nothing is
	// processing. It suits a poller, so repeat wakes for the same input are
	// throttled.
	ConversationWake
)

// Dispatcher runs one reconciliation pass over the gaps between Firestore and
// the task engine. Every process that delivers commands calls it: the API after
// accepting a command, the conversation wake handler, the worker loop and
// nori-deliver. Callers choose Steps; the order below never changes.
type Dispatcher struct {
	Store    state.ConversationStore
	Engine   Orchestrator
	Waker    wake.Waker
	Recovery Recovery

	mu               sync.Mutex
	lastWakeReceived int64
	lastWakeAttempt  time.Time
}

// Reconcile runs the selected steps once, in this order: repair of closed
// executions, command delivery, held publication wakes, conversation wake. A
// failed step does not stop the later ones, because each is independent and
// idempotent. Errors are joined, labelled by step.
func (d *Dispatcher) Reconcile(ctx context.Context, steps Steps) error {
	var failures []error
	step := func(selected Steps, label string, run func(context.Context) error) {
		if steps&selected == 0 {
			return
		}
		if err := run(ctx); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", label, err))
		}
	}
	step(RepairClosed, "terminal recovery", func(ctx context.Context) error { return d.Recovery.RepairClosed(ctx) })
	step(Commands, "pending command delivery", d.deliverCommands)
	step(Publications, "held publication wake", d.wakePublications)
	step(ConversationWake, "pending task event wake", d.wakeConversation)
	return errors.Join(failures...)
}

// StopPaused completes an accepted Stop for a task that cannot run while
// execution is paused. See Recovery.
func (d *Dispatcher) StopPaused(ctx context.Context, runID string) (state.Run, error) {
	return d.Recovery.StopPaused(ctx, runID)
}

// deliverCommands is called after acceptance and on worker/API startup. A crash
// before marking the durable intent delivered merely sends a duplicate on repair.
func (d *Dispatcher) deliverCommands(ctx context.Context) (resultErr error) {
	pending, e := d.Store.Pending(ctx)
	if e != nil {
		if !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "task.delivery", e)
		}
		return e
	}
	if len(pending) == 0 {
		return nil
	}
	ctx, span := tracer.Start(ctx, "task.deliver_pending", trace.WithAttributes(
		attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID())))
	defer func() {
		if resultErr != nil && !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "task.delivery", resultErr)
		}
		span.End()
	}()
	span.SetAttributes(attribute.Int("nori.command.count", len(pending)))
	var failures []error
	requestDriven := d.Waker.WorkersNeedWake()
	for _, cmd := range pending {
		run, e := d.Store.GetRun(ctx, cmd.RunID)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if run.Status != state.RunRunning {
			if run.CancelRequested && cmd.Kind == state.CommandStop {
				// A paused Stop may already be persisted while its workflow waits.
				// Retain delivery intent until it receives a wake or is absent.
				e = d.Engine.Signal(ctx, cmd.RunID, cmd)
				if e != nil && !errors.Is(e, ErrNoExecution) {
					failures = append(failures, e)
					continue
				}
				if e == nil && requestDriven {
					control, err := d.Store.Control(ctx)
					if err != nil {
						failures = append(failures, err)
						continue
					}
					// A paused Stop may have projected cancellation before its
					// parked workflow drains. Keep this existing command intent
					// until resume can durably enqueue the exact workflow wake.
					if control.Paused {
						continue
					}
					if err = d.wakeWorker(ctx, cmd.RunID); err != nil {
						failures = append(failures, err)
						continue
					}
				}
			}
			if e = d.Store.Delivered(ctx, cmd.ID); e != nil {
				failures = append(failures, e)
			}
			continue
		}
		control, e := d.Store.Control(ctx)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if e = control.Check(); e != nil {
			// Stop can signal an existing workflow after pause; it must not start one.
			if cmd.Kind == state.CommandStop {
				e = d.Engine.Signal(ctx, cmd.RunID, cmd)
			} else {
				continue
			}
		} else {
			e = d.Engine.SignalOrStart(ctx, cmd)
		}
		if e != nil {
			failures = append(failures, fmt.Errorf("command %s delivery: %w", cmd.ID, e))
			continue
		}
		if control.Paused && requestDriven {
			continue
		}
		if !control.Paused {
			if e = d.wakeWorker(ctx, cmd.RunID); e != nil {
				failures = append(failures, e)
				continue
			}
		}
		if e = d.Store.Delivered(ctx, cmd.ID); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

// wakePublications delivers one durable wake for each held result. The generation
// fence preserves a newer hold if another user input arrives during delivery.
// It reuses the user-only routing fence that terminal publication waits on.
func (d *Dispatcher) wakePublications(ctx context.Context) error {
	pending, err := d.Store.UserRoutingPending(ctx)
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	runs, err := d.Store.PublicationTasks(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, run := range runs {
		if err = d.Engine.Signal(ctx, run.ID, state.Command{RunID: run.ID, Kind: "routing-resolved"}); err != nil {
			failures = append(failures, err)
			continue
		}
		if err = d.wakeWorker(ctx, run.ID); err != nil {
			failures = append(failures, err)
			continue
		}
		if err = d.Store.PublicationWoken(ctx, run.ID, run.PublicationGeneration); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// wakeConversation wakes the API for input that is saved but not being processed.
func (d *Dispatcher) wakeConversation(ctx context.Context) error {
	relay, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	control, _, err := d.Store.ConversationStatus(relay)
	if err != nil {
		return err
	}
	now := time.Now()
	if control.Received <= control.Consumed || (control.Active != "" && control.LeaseUntil.After(now)) {
		return nil
	}
	d.mu.Lock()
	if control.Received == d.lastWakeReceived && now.Sub(d.lastWakeAttempt) < time.Minute {
		d.mu.Unlock()
		return nil
	}
	// Record the attempt before scheduling: an error may still mean the queue
	// accepted the wake. Bound duplicate fallback wakes while preserving
	// immediate delivery for newly received input.
	d.lastWakeReceived, d.lastWakeAttempt = control.Received, now
	d.mu.Unlock()
	return d.Waker.WakeConversation(relay)
}

// Resume signals existing durable tasks after admission reopens. User-input waits
// reconcile and remain waiting when their revision has not changed. No workflow
// or computer is started by this function.
func (d *Dispatcher) Resume(ctx context.Context) error {
	if err := d.Reconcile(ctx, Commands); err != nil {
		return err
	}
	control, err := d.Store.Control(ctx)
	if err != nil {
		return err
	}
	if err = control.Check(); err != nil {
		return err
	}
	runs, err := d.Store.OpenTasks(ctx)
	if err != nil {
		return err
	}
	var failures []error
	seen := map[string]bool{}
	for _, run := range runs {
		if run.Status != state.RunRunning {
			continue
		}
		if err = d.Engine.Signal(ctx, run.ID, state.Command{RunID: run.ID, Kind: "resume"}); err != nil {
			if !errors.Is(err, ErrNoExecution) {
				failures = append(failures, err)
			}
		} else if err = d.wakeWorker(ctx, run.ID); err != nil {
			failures = append(failures, err)
		} else {
			seen[run.ID] = true
		}
	}
	if d.Waker.WorkersNeedWake() {
		failures = append(failures, d.recoverWakes(ctx, seen))
	}
	return errors.Join(failures...)
}

// recoverWakes reconstructs disposable queue notifications from the engine's
// durable running executions. This also finds terminal Firestore projections
// whose deferred activities outlived a long pause and queue retention. It is
// never a periodic controller and never changes an Activity retry policy.
func (d *Dispatcher) recoverWakes(ctx context.Context, seen map[string]bool) error {
	ids, failure := d.Engine.PendingWork(ctx)
	failures := []error{failure}
	for _, runID := range ids {
		if seen[runID] {
			continue
		}
		if err := d.Engine.Signal(ctx, runID, state.Command{RunID: runID, Kind: "resume"}); err != nil {
			if !errors.Is(err, ErrNoExecution) {
				failures = append(failures, err)
			}
			continue
		}
		if err := d.wakeWorker(ctx, runID); err != nil {
			failures = append(failures, err)
		}
		seen[runID] = true
	}
	return errors.Join(failures...)
}

// wakeWorker asks the worker build that owns the task to poll for it. It does
// nothing when workers poll on their own, which also spares the engine lookup.
func (d *Dispatcher) wakeWorker(ctx context.Context, runID string) error {
	if !d.Waker.WorkersNeedWake() {
		return nil
	}
	build, err := d.Engine.WorkerBuild(ctx, runID)
	if err != nil {
		return err
	}
	return d.Waker.WakeWorker(ctx, build, WorkflowID(runID))
}

// Runnable inspects the engine, not the UI's question/status projection. In
// particular a terminal task may still have Spend/CloseBrowser work queued.
// known contains every workflow observed by the worker before stopping polling.
// It returns the workflow IDs that build must still run.
func (d *Dispatcher) Runnable(ctx context.Context, build string, known []string) ([]string, error) {
	var runnable []string
	ids := map[string]bool{}
	cleanupIDs := map[string]bool{}
	for _, id := range known {
		ids[id] = true
	}
	runs, err := d.Store.OpenTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, run := range runs {
		cleanupIDs[WorkflowID(run.ID)] = true
	}
	cleanup, err := d.Store.CleanupTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, run := range cleanup {
		cleanupIDs[WorkflowID(run.ID)] = true
	}
	for _, run := range append(runs, cleanup...) {
		ids[WorkflowID(run.ID)] = true
	}
	for id := range ids {
		runID, ok := RunID(id)
		if !ok {
			return nil, fmt.Errorf("unexpected task workflow identity")
		}
		execution, err := d.Engine.Inspect(ctx, runID)
		if errors.Is(err, ErrNoExecution) {
			continue
		}
		if err != nil {
			return nil, err
		}
		repair := execution.Closed && cleanupIDs[id]
		if execution.Closed && !repair {
			continue
		}
		if !repair && !execution.NeedsWorker {
			continue
		}
		assigned, err := d.Engine.WorkerBuild(ctx, runID)
		if err != nil {
			return nil, err
		}
		if assigned == build {
			runnable = append(runnable, id)
		}
	}
	return runnable, nil
}
