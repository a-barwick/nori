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

type Steps uint8

const (
	RepairClosed Steps = 1 << iota
	Commands
	// Publications wakes executions holding a finished result, once no chat
	// input is waiting to be routed.
	Publications
	// ConversationWake wakes the API for saved chat input that nothing is
	// processing. It suits a poller, so repeat wakes for the same input are
	// throttled.
	ConversationWake
)

type Dispatcher struct {
	UserID   string
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
		delivered, err := d.relayCommand(ctx, cmd, requestDriven)
		if delivered {
			err = d.Store.Delivered(ctx, cmd.ID)
		}
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (d *Dispatcher) relayCommand(ctx context.Context, cmd state.Command, requestDriven bool) (bool, error) {
	run, err := d.Store.GetRun(ctx, cmd.RunID)
	if err != nil {
		return false, err
	}
	terminal := run.Status != state.RunRunning
	if terminal {
		if !run.CancelRequested || cmd.Kind != state.CommandStop {
			return true, nil
		}
		// Stop may have projected cancellation while its workflow was parked.
		err = d.Engine.Signal(ctx, cmd.RunID, cmd)
		if errors.Is(err, ErrNoExecution) {
			return true, nil
		}
		if err != nil || !requestDriven {
			return err == nil, err
		}
	}
	control, err := d.Store.Control(ctx)
	if err != nil {
		return false, err
	}
	if control.Paused {
		if terminal || cmd.Kind != state.CommandStop {
			// A parked execution still needs its original delivery intent after resume.
			return false, nil
		}
		// An accepted Stop can signal an existing execution without starting one.
		if err := d.Engine.Signal(ctx, cmd.RunID, cmd); err != nil {
			return false, fmt.Errorf("command %s delivery: %w", cmd.ID, err)
		}
		return !requestDriven, nil
	}
	if !terminal {
		if err := d.Engine.SignalOrStart(ctx, cmd); err != nil {
			return false, fmt.Errorf("command %s delivery: %w", cmd.ID, err)
		}
	}
	err = d.wakeWorker(ctx, cmd.RunID)
	return err == nil, err
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

func (d *Dispatcher) wakeConversation(ctx context.Context) error {
	relay, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	control, _, err := d.Store.ConversationStatus(relay)
	if err != nil {
		return err
	}
	now := time.Now()
	if !control.Pending() || control.Processing(now) {
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
		signaled, err := d.resumeTask(ctx, run.ID)
		if err != nil {
			failures = append(failures, err)
		} else if signaled {
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
		signaled, err := d.resumeTask(ctx, runID)
		if err != nil {
			failures = append(failures, err)
		}
		if signaled {
			seen[runID] = true
		}
	}
	return errors.Join(failures...)
}

func (d *Dispatcher) resumeTask(ctx context.Context, runID string) (signaled bool, err error) {
	err = d.Engine.Signal(ctx, runID, state.Command{RunID: runID, Kind: "resume"})
	if errors.Is(err, ErrNoExecution) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, d.wakeWorker(ctx, runID)
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
	return d.Waker.WakeWorker(ctx, build, WorkflowID(d.UserID, runID))
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
		cleanupIDs[WorkflowID(d.UserID, run.ID)] = true
	}
	cleanup, err := d.Store.CleanupTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, run := range cleanup {
		cleanupIDs[WorkflowID(d.UserID, run.ID)] = true
	}
	for _, run := range append(runs, cleanup...) {
		ids[WorkflowID(d.UserID, run.ID)] = true
	}
	for id := range ids {
		runID, ok := RunID(d.UserID, id)
		if !ok {
			continue // Another user is handled by its own dispatcher.
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
