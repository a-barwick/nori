package task

import (
	"context"
	"errors"
	"sync"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

// WakeNotifier delivers in-memory wake notifications to an active task context
// whenever a wake arrives for (userID, runID) or a global "pause" event fires.
// Implemented by worker.Pool (fanning out LISTEN nori_wake) and by sim harnesses.
type WakeNotifier interface {
	Subscribe(userID, runID string) (ch <-chan struct{}, unsubscribe func())
}

// RunOutcome reports why Runner.Run stopped executing a claimed task.
type RunOutcome string

const (
	OutcomeDone     RunOutcome = "done"     // Task is terminal & !CleanupPending; lease deleted.
	OutcomeParked   RunOutcome = "parked"   // Task parked (question, paused, routing_pending, finishing).
	OutcomeBusy     RunOutcome = "busy"     // Lost fence or ErrTaskBusy; another worker owns it.
	OutcomeReleased RunOutcome = "released" // Context canceled / graceful drain; lease released cleanly.
	OutcomeBackoff  RunOutcome = "backoff"  // Transient error; lease released with backoff wake_at.
)

// Runner executes a single inner task against Postgres-native state.TaskControl
// and docstore.LeaseStore without Temporal.
type Runner struct {
	Activities *Activities
	Leases     docstore.LeaseStore
	UserID     string
	Owner      string
	Wakes      WakeNotifier
	Now        func() time.Time
	NewTicker  func(time.Duration) (<-chan time.Time, func())
	// StepBudget optionally limits how many state-machine steps one Run call executes
	// (0 = unlimited). Used by deterministic simulation drivers to interleave workers.
	StepBudget int
	// MaxModelAttempts optionally overrides the default per-task model attempt ceiling
	// (default: 24 for main tasks, 14 for subtasks).
	MaxModelAttempts int
	// Drain, when closed, asks Run to stop cleanly at the next checkpoint boundary
	// and release its TaskControl + discovery lease for immediate takeover by another replica.
	Drain <-chan struct{}
	// OnLeaseRenewed is an optional hook invoked on successful lease renewal (for tests/metrics).
	OnLeaseRenewed func(userID, runID string)
}

const (
	// Per-step explicit context deadlines (Design Doc §4A).
	stepTimeoutLong    = 240 * time.Second // ModelRef and ToolRef
	stepTimeoutCleanup = 200 * time.Second // Cleanup, StopPaused, and failure PublishRef
	stepTimeoutQuick   = 20 * time.Second  // SyncHistory, Ask, CheckCompletion, InputRef, AppendToolResult, CurrentPosition, Project, normal PublishRef

	leaseRenewInterval     = 10 * time.Second
	leaseRenewTimeout      = 5 * time.Second
	shutdownReleaseTimeout = 5 * time.Second

	busyBackoff      = 5 * time.Second
	transientBackoff = 2 * time.Second

	// D-053: a re-ask after an interrupted model request starts only while a normal pass
	// (one request plus its checkpoint save) still fits within modelStepDeadline of the step's start.
	modelStepDeadline = 9 * time.Minute
	modelPassLimit    = modelRequestLimit + modelSaveReserve
)

type leaseWatcher struct {
	ctx     context.Context
	stop    func()
	lastErr func() error
}

// Run executes the claimed task lease until it reaches a terminal/parked/released/backoff
// outcome, loses its authoritative TaskControl fence, or hits StepBudget.
func (r *Runner) Run(ctx context.Context, claim docstore.TaskLease) (RunOutcome, error) {
	if r == nil || r.Activities == nil || r.Activities.Store == nil || r.Leases == nil {
		return OutcomeBackoff, errors.New("task.Runner requires Activities (with Store) and Leases")
	}
	userID := r.UserID
	if userID == "" {
		userID = claim.UserID
	}
	runID := claim.RunID
	owner := r.Owner
	if owner == "" {
		owner = claim.Owner
	}
	if userID == "" || runID == "" || owner == "" {
		return OutcomeBackoff, errors.New("task.Runner requires non-empty userID, runID, and owner")
	}

	// Shallow copy so Runner can bind UserID/Wakes/Now/NewTicker per run without mutating
	// a shared *Activities across concurrent tasks for the same user.
	act := *r.Activities
	if act.UserID == "" {
		act.UserID = userID
	}
	if act.Wakes == nil {
		act.Wakes = r.Wakes
	}
	if act.Now == nil && r.Now != nil {
		act.Now = r.Now
	}
	if act.NewTicker == nil && r.NewTicker != nil {
		act.NewTicker = r.NewTicker
	}

	// Capture wake_seq once on entry before reading commands/state. Never refresh mid-turn;
	// only update via RefreshTaskClaim when ParkTask reports parked == false.
	s := &runSession{
		runner:      r,
		ctx:         ctx,
		act:         act,
		store:       act.Store,
		userID:      userID,
		runID:       runID,
		owner:       owner,
		capturedSeq: claim.WakeSeq,
	}
	defer s.close()

	// Step 1: Pre-acquire late-wake and pause check.
	initialRun, res := s.preAcquire()
	if res.done {
		return res.outcome, res.err
	}

	// Step 2: Acquire authoritative TaskControl fence and start lease renewal watcher.
	if res := s.acquireControl(initialRun); res.done {
		return res.outcome, res.err
	}

	// Step 3: Main explicit state-machine loop.
	for steps := 0; ; steps++ {
		if r.StepBudget > 0 && steps >= r.StepBudget {
			return OutcomeReleased, nil
		}
		if res, abort := s.checkLoopAbort(); abort {
			return res.outcome, res.err
		}
		if res := s.runPhase(); res.done {
			return res.outcome, res.err
		}
	}
}

func (r *Runner) now(act *Activities) time.Time {
	if r != nil && r.Now != nil {
		return r.Now()
	}
	if act != nil {
		return act.now()
	}
	return time.Now()
}

func (r *Runner) newTicker(d time.Duration) (<-chan time.Time, func()) {
	if r != nil && r.NewTicker != nil {
		return r.NewTicker(d)
	}
	if r != nil && r.Activities != nil && r.Activities.NewTicker != nil {
		return r.Activities.NewTicker(d)
	}
	t := time.NewTicker(d)
	return t.C, t.Stop
}

func (r *Runner) draining() bool {
	if r == nil || r.Drain == nil {
		return false
	}
	select {
	case <-r.Drain:
		return true
	default:
		return false
	}
}

// withStepTimeout applies an explicit real-time context deadline (for ctx.Deadline(),
// receiptContext, and modelRequestDeadline) and, when a virtual clock is configured,
// attaches a matching virtual receipt deadline so RecoveryUntil stays deterministic.
func (r *Runner) withStepTimeout(parent context.Context, act *Activities, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, d)
	hasVirtualClock := (r != nil && r.Now != nil) ||
		(act != nil && (act.Now != nil || (act.Executor != nil && act.Executor.Now != nil)))
	if hasVirtualClock {
		ctx = withReceiptDeadline(ctx, r.now(act).Add(d))
	}
	return ctx, cancel
}

// startTaskLeaseWatcher renews both TaskControl (fenced) and nori_task_leases every 10s.
// Mandatory self-stop rule (Design Doc §4B): ANY renewal error or renewed != 1 IMMEDIATELY
// cancels the returned watcher context so in-flight model/tool I/O stops cleanly.
func (r *Runner) startTaskLeaseWatcher(
	parent context.Context,
	store state.TaskStore,
	userID, runID, owner string,
	fence state.TaskFence,
) *leaseWatcher {
	watchCtx, cancelWatch := context.WithCancelCause(parent)
	done := make(chan struct{})
	exited := make(chan struct{})

	var mu sync.Mutex
	var renewErr error

	tickCh, stopTick := r.newTicker(leaseRenewInterval)
	go func() {
		defer close(exited)
		defer stopTick()
		key := []docstore.TaskKey{{UserID: userID, RunID: runID}}
		for {
			select {
			case <-done:
				return
			case <-watchCtx.Done():
				return
			case _, ok := <-tickCh:
				if !ok {
					return
				}
				renewCtx, cancelRenew := context.WithTimeout(context.WithoutCancel(parent), leaseRenewTimeout)
				err := store.RenewTaskControl(renewCtx, runID, fence)
				if err == nil {
					renewed, lErr := r.Leases.RenewTaskLeases(renewCtx, owner, key, state.TaskLease)
					if lErr != nil {
						err = lErr
					} else if renewed != 1 {
						err = state.ErrTaskBusy
					}
				}
				cancelRenew()
				if err != nil {
					mu.Lock()
					renewErr = err
					mu.Unlock()
					cancelWatch(err)
					return
				}
				if r.OnLeaseRenewed != nil {
					r.OnLeaseRenewed(userID, runID)
				}
			}
		}
	}()

	var stopOnce sync.Once
	return &leaseWatcher{
		ctx: watchCtx,
		stop: func() {
			stopOnce.Do(func() {
				close(done)
				<-exited
				cancelWatch(nil)
			})
		},
		lastErr: func() error {
			mu.Lock()
			defer mu.Unlock()
			return renewErr
		},
	}
}
