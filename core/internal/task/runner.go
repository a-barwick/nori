package task

import (
	"context"
	"errors"
	"sync"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

type WakeNotifier interface {
	Subscribe(userID, runID string) (ch <-chan struct{}, unsubscribe func())
}

type RunOutcome string

const (
	OutcomeDone     RunOutcome = "done"
	OutcomeParked   RunOutcome = "parked"
	OutcomeBusy     RunOutcome = "busy"
	OutcomeReleased RunOutcome = "released"
	OutcomeBackoff  RunOutcome = "backoff"
)

type Runner struct {
	Activities       *Activities
	Leases           docstore.LeaseStore
	UserID           string
	Owner            string
	Wakes            WakeNotifier
	Now              func() time.Time
	NewTicker        func(time.Duration) (<-chan time.Time, func())
	StepBudget       int
	MaxModelAttempts int
	Drain            <-chan struct{}
	OnLeaseRenewed   func(userID, runID string)
}

const (
	stepTimeoutLong    = 240 * time.Second
	stepTimeoutCleanup = 200 * time.Second
	stepTimeoutQuick   = 20 * time.Second

	leaseRenewInterval     = 10 * time.Second
	leaseRenewTimeout      = 5 * time.Second
	shutdownReleaseTimeout = 5 * time.Second

	busyBackoff      = 5 * time.Second
	transientBackoff = 2 * time.Second

	modelStepDeadline = 9 * time.Minute
	modelPassLimit    = modelRequestLimit + modelSaveReserve
)

type leaseWatcher struct {
	ctx     context.Context
	stop    func()
	lastErr func() error
}

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

	initialRun, res := s.preAcquire()
	if res.done {
		return res.outcome, res.err
	}

	if res := s.acquireControl(initialRun); res.done {
		return res.outcome, res.err
	}

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

func (r *Runner) withStepTimeout(parent context.Context, act *Activities, d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, d)
	hasVirtualClock := (r != nil && r.Now != nil) ||
		(act != nil && (act.Now != nil || (act.Executor != nil && act.Executor.Now != nil)))
	if hasVirtualClock {
		ctx = withReceiptDeadline(ctx, r.now(act).Add(d))
	}
	return ctx, cancel
}

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
