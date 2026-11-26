package worker

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"nori/internal/app"
	"nori/internal/scheduler"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/task"
	"nori/internal/users"
)

const (
	DefaultTaskConcurrency               = 64
	DefaultConversationConcurrency       = 32
	DefaultSchedulerConcurrency          = 16
	DefaultMaxPerUserConcurrency         = 4
	DefaultPollInterval                  = 2 * time.Second
	DefaultRepairInterval                = 60 * time.Second
	DefaultRenewInterval                 = 10 * time.Second
	DefaultDrainTimeout                  = 25 * time.Second
	DefaultCancelGrace                   = 5 * time.Second
	DefaultBackoff                       = 2 * time.Second
	perUserDeferMaxDelay                 = 500 * time.Millisecond
	userWakeRenewTimeout                 = 5 * time.Second
	repairAdvisoryLockKey          int64 = 7162384102
)

type Options struct {
	Owner string

	UserID string

	TaskConcurrency         int
	ConversationConcurrency int
	SchedulerConcurrency    int
	MaxPerUserConcurrency   int

	PollInterval   time.Duration
	RepairInterval time.Duration
	RenewInterval  time.Duration
	DrainTimeout   time.Duration
	CancelGrace    time.Duration
	Backoff        time.Duration

	Now       func() time.Time
	NewTicker func(time.Duration) (<-chan time.Time, func())
	NewTimer  func(time.Duration) (<-chan time.Time, func())

	RunTask              func(ctx context.Context, userApp *app.App, claim docstore.TaskLease, drain <-chan struct{}) (task.RunOutcome, error)
	RunConversation      func(ctx context.Context, userApp *app.App) error
	NextConversationWake func(ctx context.Context, userApp *app.App) (time.Time, error)
	RunScheduler         func(ctx context.Context, userApp *app.App) (*time.Time, error)
}

type LaneStats struct {
	TaskActive           int64
	TaskCapacity         int64
	ConversationActive   int64
	ConversationCapacity int64
	SchedulerActive      int64
	SchedulerCapacity    int64
}

type Pool struct {
	service      *app.Service
	leases       docstore.LeaseStore
	scopedLeases docstore.ScopedLeaseStore
	listener     docstore.WakeListener
	owner        string
	userID       string
	opts         Options

	taskWakeCh  chan struct{}
	convWakeCh  chan struct{}
	schedWakeCh chan struct{}

	taskActive  atomic.Int64
	convActive  atomic.Int64
	schedActive atomic.Int64

	userMu       sync.Mutex
	activeByUser map[string]int

	subMu     sync.Mutex
	subs      map[docstore.TaskKey]map[uint64]chan struct{}
	nextSubID uint64

	drainOnce sync.Once
	drainCh   chan struct{}
}

var _ task.WakeNotifier = (*Pool)(nil)

func New(svc *app.Service, opts Options) (*Pool, error) {
	if svc == nil {
		return nil, errors.New("worker pool requires app.Service")
	}
	leases := svc.Leases()
	if leases == nil {
		return nil, errors.New("worker pool requires a LeaseStore-capable storage backend")
	}
	var scopedLeases docstore.ScopedLeaseStore
	if opts.UserID != "" {
		var ok bool
		scopedLeases, ok = leases.(docstore.ScopedLeaseStore)
		if !ok {
			return nil, fmt.Errorf("worker pool with UserID %q requires a docstore.ScopedLeaseStore backend", opts.UserID)
		}
	}
	if opts.Owner == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "worker"
		}
		opts.Owner = fmt.Sprintf("%s-%s", host, uuid.NewString()[:8])
	}
	if opts.TaskConcurrency <= 0 {
		opts.TaskConcurrency = DefaultTaskConcurrency
	}
	if opts.ConversationConcurrency <= 0 {
		opts.ConversationConcurrency = DefaultConversationConcurrency
	}
	if opts.SchedulerConcurrency <= 0 {
		opts.SchedulerConcurrency = DefaultSchedulerConcurrency
	}
	if opts.MaxPerUserConcurrency <= 0 {
		opts.MaxPerUserConcurrency = DefaultMaxPerUserConcurrency
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.RepairInterval <= 0 {
		opts.RepairInterval = DefaultRepairInterval
	}
	if opts.RenewInterval <= 0 {
		opts.RenewInterval = DefaultRenewInterval
	}
	if opts.DrainTimeout <= 0 {
		opts.DrainTimeout = DefaultDrainTimeout
	}
	if opts.CancelGrace <= 0 {
		opts.CancelGrace = DefaultCancelGrace
	}
	if opts.Backoff <= 0 {
		opts.Backoff = DefaultBackoff
	}
	return &Pool{
		service:      svc,
		leases:       leases,
		scopedLeases: scopedLeases,
		listener:     svc.WakeListener(),
		owner:        opts.Owner,
		userID:       opts.UserID,
		opts:         opts,
		taskWakeCh:   make(chan struct{}, 1),
		convWakeCh:   make(chan struct{}, 1),
		schedWakeCh:  make(chan struct{}, 1),
		activeByUser: map[string]int{},
		subs:         map[docstore.TaskKey]map[uint64]chan struct{}{},
		drainCh:      make(chan struct{}),
	}, nil
}

func (p *Pool) tryAcquireUser(userID string) bool {
	if userID == "" || p.opts.MaxPerUserConcurrency <= 0 {
		return true
	}
	p.userMu.Lock()
	defer p.userMu.Unlock()
	if p.activeByUser[userID] >= p.opts.MaxPerUserConcurrency {
		return false
	}
	p.activeByUser[userID]++
	return true
}

func (p *Pool) releaseUser(userID string) {
	if userID == "" || p.opts.MaxPerUserConcurrency <= 0 {
		return
	}
	p.userMu.Lock()
	wasAtCap := p.activeByUser[userID] >= p.opts.MaxPerUserConcurrency
	if n := p.activeByUser[userID]; n <= 1 {
		delete(p.activeByUser, userID)
	} else {
		p.activeByUser[userID] = n - 1
	}
	p.userMu.Unlock()
	if wasAtCap {
		signalChan(p.taskWakeCh)
		signalChan(p.schedWakeCh)
	}
}

func (p *Pool) perUserDeferDelay() time.Duration {
	if p.opts.Backoff > 0 && p.opts.Backoff < perUserDeferMaxDelay {
		return p.opts.Backoff
	}
	return perUserDeferMaxDelay
}
func (p *Pool) Owner() string { return p.owner }

func (p *Pool) LaneStats() LaneStats {
	if p == nil {
		return LaneStats{}
	}
	return LaneStats{
		TaskActive:           p.taskActive.Load(),
		TaskCapacity:         int64(p.opts.TaskConcurrency),
		ConversationActive:   p.convActive.Load(),
		ConversationCapacity: int64(p.opts.ConversationConcurrency),
		SchedulerActive:      p.schedActive.Load(),
		SchedulerCapacity:    int64(p.opts.SchedulerConcurrency),
	}
}

func (p *Pool) now() time.Time {
	if p.opts.Now != nil {
		return p.opts.Now()
	}
	return time.Now()
}

func (p *Pool) newTicker(d time.Duration) (<-chan time.Time, func()) {
	if p.opts.NewTicker != nil {
		return p.opts.NewTicker(d)
	}
	t := time.NewTicker(d)
	return t.C, t.Stop
}

func (p *Pool) newTimer(d time.Duration) (<-chan time.Time, func()) {
	if p.opts.NewTimer != nil {
		return p.opts.NewTimer(d)
	}
	t := time.NewTimer(d)
	return t.C, func() { t.Stop() }
}

func (p *Pool) closeDrain() {
	p.drainOnce.Do(func() {
		close(p.drainCh)
	})
}

func signalChan(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (p *Pool) Subscribe(userID, runID string) (<-chan struct{}, func()) {
	key := docstore.TaskKey{UserID: userID, RunID: runID}
	ch := make(chan struct{}, 1)
	p.subMu.Lock()
	p.nextSubID++
	id := p.nextSubID
	if p.subs[key] == nil {
		p.subs[key] = map[uint64]chan struct{}{}
	}
	p.subs[key][id] = ch
	p.subMu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			p.subMu.Lock()
			if group := p.subs[key]; group != nil {
				delete(group, id)
				if len(group) == 0 {
					delete(p.subs, key)
				}
			}
			p.subMu.Unlock()
		})
	}
}

func (p *Pool) notifyTaskSubscribers(userID, runID string) {
	p.subMu.Lock()
	defer p.subMu.Unlock()
	if userID == "" {
		for _, group := range p.subs {
			for _, ch := range group {
				signalChan(ch)
			}
		}
		return
	}
	if runID == "*" {
		for key, group := range p.subs {
			if key.UserID != userID {
				continue
			}
			for _, ch := range group {
				signalChan(ch)
			}
		}
		return
	}
	for _, ch := range p.subs[docstore.TaskKey{UserID: userID, RunID: runID}] {
		signalChan(ch)
	}
}

func (p *Pool) HandleWake(ev docstore.WakeEvent) {
	parsed := docstore.ParseWakePayload(ev.Payload)
	switch parsed.Kind {
	case docstore.WakeKindTask:
		if p.userID != "" && parsed.UserID != "" && parsed.UserID != p.userID {
			return
		}
		signalChan(p.taskWakeCh)
		p.notifyTaskSubscribers(parsed.UserID, parsed.RunID)
	case docstore.WakeKindPause:
		p.notifyTaskSubscribers("", "")
	case docstore.WakeKindConversation:
		if p.userID != "" && parsed.UserID != "" && parsed.UserID != p.userID {
			return
		}
		signalChan(p.convWakeCh)
	case docstore.WakeKindScheduler:
		if p.userID != "" && parsed.UserID != "" && parsed.UserID != p.userID {
			return
		}
		signalChan(p.schedWakeCh)
	case docstore.WakeKindResync:
		signalChan(p.taskWakeCh)
		signalChan(p.convWakeCh)
		signalChan(p.schedWakeCh)
		p.notifyTaskSubscribers("", "")
	}
}

func (p *Pool) Run(ctx context.Context) error {
	claimCtx, cancelClaim := context.WithCancel(ctx)
	defer cancelClaim()

	workCtx, cancelWork := context.WithCancel(context.Background())
	defer cancelWork()

	var claimWG sync.WaitGroup
	var workWG sync.WaitGroup

	if p.listener != nil {
		claimWG.Add(1)
		go func() {
			defer claimWG.Done()
			if err := p.listener.ListenWakes(claimCtx, p.HandleWake); err != nil && claimCtx.Err() == nil {
				slog.WarnContext(claimCtx, "worker wake listener exited", "owner", p.owner, "err", err)
			}
		}()
	}

	claimWG.Add(1)
	go func() {
		defer claimWG.Done()
		p.runRepairLoop(claimCtx)
	}()

	claimWG.Add(3)
	go func() {
		defer claimWG.Done()
		p.runTaskLoop(claimCtx, workCtx, &workWG)
	}()
	go func() {
		defer claimWG.Done()
		p.runConversationLoop(claimCtx, workCtx, &workWG)
	}()
	go func() {
		defer claimWG.Done()
		p.runSchedulerLoop(claimCtx, workCtx, &workWG)
	}()

	<-ctx.Done()

	cancelClaim()
	p.closeDrain()
	claimWG.Wait()

	workDone := make(chan struct{})
	go func() {
		workWG.Wait()
		close(workDone)
	}()

	timerCh, stopTimer := p.newTimer(p.opts.DrainTimeout)
	defer stopTimer()
	select {
	case <-workDone:
		cancelWork()
		return nil
	case <-timerCh:
		slog.WarnContext(ctx, "worker drain timeout reached; canceling remaining in-flight work", "owner", p.owner, "drain_timeout", p.opts.DrainTimeout)
		cancelWork()
		<-workDone
		return nil
	}
}

func (p *Pool) runRepairLoop(ctx context.Context) {
	if err := p.RepairOnce(ctx); err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "worker startup repair sweep failed", "owner", p.owner, "err", err)
	}
	tickCh, stopTick := p.newTicker(p.opts.RepairInterval)
	defer stopTick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tickCh:
			p.service.EvictIdle()
			if err := p.RepairOnce(ctx); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "worker periodic repair sweep failed", "owner", p.owner, "err", err)
			}
		}
	}
}

func repairAdvisoryLockKeyForUser(userID string) int64 {
	if userID == "" {
		return repairAdvisoryLockKey
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte("nori-repair-user:"))
	_, _ = h.Write([]byte(userID))
	key := int64(h.Sum64() & 0x7fffffffffffffff)
	if key == 0 || key == docstore.MigrationAdvisoryLockKey || key == repairAdvisoryLockKey {
		key += 3
	}
	return key
}

func (p *Pool) RepairOnce(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if locker := p.service.AdvisoryLocker(); locker != nil {
		unlock, acquired, err := locker.TryAdvisoryLock(ctx, repairAdvisoryLockKeyForUser(p.userID))
		if err != nil {
			return err
		}
		if !acquired {
			return nil
		}
		if unlock != nil {
			defer unlock()
		}
	}
	var rows []users.User
	if p.userID != "" {
		row, err := p.service.Users.Get(ctx, p.userID)
		if err != nil {
			if !errors.Is(err, state.ErrNotFound) && !errors.Is(err, docstore.ErrNotFound) {
				return err
			}
		} else {
			rows = []users.User{row}
		}
	} else {
		var err error
		rows, err = p.service.Users.List(ctx)
		if err != nil {
			return err
		}
	}
	knownUsers := make(map[string]bool, len(rows))
	var failures []error
	for _, row := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		knownUsers[row.ID] = true
		if repairErr := p.service.RepairUser(ctx, row); repairErr != nil {
			failures = append(failures, fmt.Errorf("user %s: %w", row.ID, repairErr))
		}
	}

	if allLeases, err := p.leases.ListTaskLeases(ctx, p.userID); err != nil {
		failures = append(failures, err)
	} else {
		for _, lease := range allLeases {
			if !knownUsers[lease.UserID] {
				if _, delErr := p.leases.DeleteTaskLease(ctx, lease.UserID, lease.RunID, lease.WakeSeq); delErr != nil {
					failures = append(failures, delErr)
				}
			}
		}
	}

	signalChan(p.taskWakeCh)
	signalChan(p.convWakeCh)
	signalChan(p.schedWakeCh)
	return errors.Join(failures...)
}

func (p *Pool) waitNext(ctx context.Context, wakeCh <-chan struct{}, tickCh <-chan time.Time) bool {
	select {
	case <-ctx.Done():
		return false
	case <-wakeCh:
		return true
	case <-tickCh:
		return true
	}
}

type claimLoopSpec[T any] struct {
	lane          string
	claimErrMsg   string
	concurrency   int
	wakeCh        <-chan struct{}
	active        *atomic.Int64
	limitPerUser  bool
	userID        func(T) string
	claim         func(context.Context) (T, bool, error)
	deferClaim    func(context.Context, T)
	handle        func(context.Context, T)
}

func runClaimLoop[T any](p *Pool, claimCtx, workCtx context.Context, workWG *sync.WaitGroup, spec claimLoopSpec[T]) {
	sem := make(chan struct{}, spec.concurrency)
	tickCh, stopTick := p.newTicker(p.opts.PollInterval)
	defer stopTick()

	for {
		select {
		case <-claimCtx.Done():
			return
		case sem <- struct{}{}:
		}
		if claimCtx.Err() != nil {
			<-sem
			return
		}
		item, ok, err := spec.claim(claimCtx)
		if err != nil {
			<-sem
			if claimCtx.Err() != nil {
				return
			}
			slog.WarnContext(claimCtx, spec.claimErrMsg, "owner", p.owner, "lane", spec.lane, "err", err)
			if !p.waitNext(claimCtx, spec.wakeCh, tickCh) {
				return
			}
			continue
		}
		if !ok {
			<-sem
			if !p.waitNext(claimCtx, spec.wakeCh, tickCh) {
				return
			}
			continue
		}
		uid := ""
		if spec.limitPerUser {
			uid = spec.userID(item)
			if !p.tryAcquireUser(uid) {
				<-sem
				rctx, cancel := context.WithTimeout(context.WithoutCancel(claimCtx), p.opts.CancelGrace)
				spec.deferClaim(rctx, item)
				cancel()
				continue
			}
		}
		workWG.Add(1)
		spec.active.Add(1)
		go func(item T, uid string) {
			defer workWG.Done()
			if spec.limitPerUser {
				defer p.releaseUser(uid)
			}
			defer func() { <-sem }()
			defer spec.active.Add(-1)
			spec.handle(workCtx, item)
		}(item, uid)
	}
}

func (p *Pool) runTaskLoop(claimCtx, workCtx context.Context, workWG *sync.WaitGroup) {
	runClaimLoop(p, claimCtx, workCtx, workWG, claimLoopSpec[docstore.TaskLease]{
		lane:         "task",
		claimErrMsg:  "worker claim task failed",
		concurrency:  p.opts.TaskConcurrency,
		wakeCh:       p.taskWakeCh,
		active:       &p.taskActive,
		limitPerUser: true,
		userID:       func(c docstore.TaskLease) string { return c.UserID },
		claim: func(ctx context.Context) (docstore.TaskLease, bool, error) {
			if p.userID != "" {
				return p.scopedLeases.ClaimTaskForUser(ctx, p.userID, p.owner, state.TaskLease)
			}
			return p.leases.ClaimTask(ctx, p.owner, state.TaskLease)
		},
		deferClaim: func(rctx context.Context, claim docstore.TaskLease) {
			if relErr := p.leases.ReleaseTask(rctx, claim.UserID, claim.RunID, p.owner, claim.WakeSeq, p.now().Add(p.perUserDeferDelay())); relErr != nil {
				slog.WarnContext(claimCtx, "worker release deferred task failed", "owner", p.owner, "lane", "task", "user_id", claim.UserID, "run_id", claim.RunID, "err", relErr)
			}
		},
		handle: p.handleTask,
	})
}

func (p *Pool) handleTask(workCtx context.Context, claim docstore.TaskLease) {
	finCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(workCtx), p.opts.CancelGrace)
	}
	userApp, release, err := p.service.AcquireByID(workCtx, claim.UserID)
	if err != nil {
		cctx, cancel := finCtx()
		defer cancel()
		if errors.Is(err, state.ErrNotFound) {
			_, _ = p.leases.DeleteTaskLease(cctx, claim.UserID, claim.RunID, claim.WakeSeq)
			return
		}
		_ = p.leases.ReleaseTask(cctx, claim.UserID, claim.RunID, p.owner, claim.WakeSeq, p.now().Add(p.opts.Backoff))
		if workCtx.Err() == nil {
			slog.WarnContext(workCtx, "worker acquire user failed", "owner", p.owner, "lane", "task", "user_id", claim.UserID, "run_id", claim.RunID, "err", err)
		}
		return
	}
	defer release()

	var outcome task.RunOutcome
	if p.opts.RunTask != nil {
		outcome, err = p.opts.RunTask(workCtx, userApp, claim, p.drainCh)
	} else {
		if userApp.TaskActivities == nil {
			cctx, cancel := finCtx()
			defer cancel()
			_ = p.leases.ReleaseTask(cctx, claim.UserID, claim.RunID, p.owner, claim.WakeSeq, p.now().Add(p.opts.Backoff))
			return
		}
		act := *userApp.TaskActivities
		act.UserID = claim.UserID
		act.Wakes = p
		runner := &task.Runner{
			Activities: &act,
			Leases:     p.leases,
			UserID:     claim.UserID,
			Owner:      p.owner,
			Wakes:      p,
			Now:        p.opts.Now,
			Drain:      p.drainCh,
		}
		outcome, err = runner.Run(workCtx, claim)
	}
	if err != nil && workCtx.Err() == nil && !errors.Is(err, context.Canceled) {
		slog.WarnContext(workCtx, "worker task execution failed", "owner", p.owner, "lane", "task", "user_id", claim.UserID, "run_id", claim.RunID, "outcome", outcome, "err", err)
	}
}

func (p *Pool) runConversationLoop(claimCtx, workCtx context.Context, workWG *sync.WaitGroup) {
	runClaimLoop(p, claimCtx, workCtx, workWG, claimLoopSpec[docstore.UserWake]{
		lane:        docstore.LaneConversation,
		claimErrMsg: "worker claim user wake failed",
		concurrency: p.opts.ConversationConcurrency,
		wakeCh:      p.convWakeCh,
		active:      &p.convActive,
		claim: func(ctx context.Context) (docstore.UserWake, bool, error) {
			if p.userID != "" {
				return p.scopedLeases.ClaimUserWakeForUser(ctx, p.userID, docstore.LaneConversation, p.owner, state.ConversationLease)
			}
			return p.leases.ClaimUserWake(ctx, docstore.LaneConversation, p.owner, state.ConversationLease)
		},
		handle: p.handleConversation,
	})
}

func (p *Pool) startUserWakeRenewer(parent context.Context, userID, lane string, lease time.Duration) (context.Context, func()) {
	turnCtx, cancelTurn := context.WithCancel(parent)
	stopRenew := make(chan struct{})
	doneRenew := make(chan struct{})
	go func() {
		defer close(doneRenew)
		tickCh, stopTick := p.newTicker(p.opts.RenewInterval)
		defer stopTick()
		for {
			select {
			case <-stopRenew:
				return
			case <-turnCtx.Done():
				return
			case <-tickCh:
				renewCtx, cancelRenew := context.WithTimeout(turnCtx, userWakeRenewTimeout)
				renewed, err := p.leases.RenewUserWake(renewCtx, userID, lane, p.owner, lease)
				cancelRenew()
				if err != nil || !renewed {
					if err != nil && turnCtx.Err() == nil {
						slog.WarnContext(turnCtx, "worker renew user wake failed", "owner", p.owner, "user_id", userID, "lane", lane, "err", err)
					}
					cancelTurn()
					return
				}
			}
		}
	}()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			close(stopRenew)
			<-doneRenew
			cancelTurn()
		})
	}
	return turnCtx, stop
}

func (p *Pool) handleConversation(workCtx context.Context, uw docstore.UserWake) {
	finCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(workCtx), p.opts.CancelGrace)
	}

	userApp, release, err := p.service.AcquireByID(workCtx, uw.UserID)
	if err != nil {
		fCtx, fCancel := finCtx()
		defer fCancel()
		if errors.Is(err, state.ErrNotFound) {
			_, _ = p.leases.ParkUserWake(fCtx, uw.UserID, docstore.LaneConversation, p.owner, uw.WakeSeq)
			return
		}
		_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneConversation, p.owner, uw.WakeSeq, p.now().Add(p.opts.Backoff))
		if workCtx.Err() == nil {
			slog.WarnContext(workCtx, "worker acquire user failed", "owner", p.owner, "lane", docstore.LaneConversation, "user_id", uw.UserID, "err", err)
		}
		return
	}
	defer release()

	turnCtx, stopRenew := p.startUserWakeRenewer(workCtx, uw.UserID, docstore.LaneConversation, state.ConversationLease)
	var runErr error
	if p.opts.RunConversation != nil {
		runErr = p.opts.RunConversation(turnCtx, userApp)
	} else {
		runErr = userApp.ProcessConversation(turnCtx)
	}
	stopRenew()

	fCtx, fCancel := finCtx()
	defer fCancel()

	if runErr != nil {
		if !errors.Is(runErr, state.ErrConversationBusy) && !errors.Is(runErr, context.Canceled) && workCtx.Err() == nil {
			slog.WarnContext(workCtx, "worker conversation will retry", "owner", p.owner, "lane", docstore.LaneConversation, "user_id", uw.UserID, "err", runErr)
		}
		_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneConversation, p.owner, uw.WakeSeq, p.now().Add(p.opts.Backoff))
		return
	}

	conv, _, statusErr := userApp.Store.ConversationStatus(fCtx)
	if statusErr != nil {
		_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneConversation, p.owner, uw.WakeSeq, p.now().Add(p.opts.Backoff))
		return
	}
	if !conv.Pending() && conv.Active == "" {
		var wakeAt time.Time
		var wakeErr error
		if p.opts.NextConversationWake != nil {
			wakeAt, wakeErr = p.opts.NextConversationWake(fCtx, userApp)
		} else {
			wakeAt, wakeErr = userApp.NextConversationWake(fCtx)
		}
		if wakeErr != nil {
			_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneConversation, p.owner, uw.WakeSeq, p.now().Add(p.opts.Backoff))
			return
		}
		if !wakeAt.IsZero() {
			_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneConversation, p.owner, uw.WakeSeq, wakeAt)
			if !wakeAt.After(p.now()) {
				signalChan(p.convWakeCh)
			}
			return
		}
		parked, parkErr := p.leases.ParkUserWake(fCtx, uw.UserID, docstore.LaneConversation, p.owner, uw.WakeSeq)
		if parkErr != nil {
			_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneConversation, p.owner, uw.WakeSeq, p.now().Add(p.opts.Backoff))
			return
		}
		if !parked {
			_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneConversation, p.owner, 0, time.Time{})
			signalChan(p.convWakeCh)
		}
		return
	}
	_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneConversation, p.owner, uw.WakeSeq, p.now().Add(p.opts.Backoff))
}

func (p *Pool) runSchedulerLoop(claimCtx, workCtx context.Context, workWG *sync.WaitGroup) {
	runClaimLoop(p, claimCtx, workCtx, workWG, claimLoopSpec[docstore.UserWake]{
		lane:         docstore.LaneScheduler,
		claimErrMsg:  "worker claim user wake failed",
		concurrency:  p.opts.SchedulerConcurrency,
		wakeCh:       p.schedWakeCh,
		active:       &p.schedActive,
		limitPerUser: true,
		userID:       func(uw docstore.UserWake) string { return uw.UserID },
		claim: func(ctx context.Context) (docstore.UserWake, bool, error) {
			if p.userID != "" {
				return p.scopedLeases.ClaimUserWakeForUser(ctx, p.userID, docstore.LaneScheduler, p.owner, state.SchedulerLease)
			}
			return p.leases.ClaimUserWake(ctx, docstore.LaneScheduler, p.owner, state.SchedulerLease)
		},
		deferClaim: func(rctx context.Context, uw docstore.UserWake) {
			if relErr := p.leases.ReleaseUserWake(rctx, uw.UserID, docstore.LaneScheduler, p.owner, uw.WakeSeq, p.now().Add(p.perUserDeferDelay())); relErr != nil {
				slog.WarnContext(claimCtx, "worker release deferred user wake failed", "owner", p.owner, "lane", docstore.LaneScheduler, "user_id", uw.UserID, "err", relErr)
			}
		},
		handle: p.handleScheduler,
	})
}

func (p *Pool) handleScheduler(workCtx context.Context, uw docstore.UserWake) {
	finCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.WithoutCancel(workCtx), p.opts.CancelGrace)
	}

	userApp, release, err := p.service.AcquireByID(workCtx, uw.UserID)
	if err != nil {
		fCtx, fCancel := finCtx()
		defer fCancel()
		if errors.Is(err, state.ErrNotFound) {
			_, _ = p.leases.ParkUserWake(fCtx, uw.UserID, docstore.LaneScheduler, p.owner, uw.WakeSeq)
			return
		}
		_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneScheduler, p.owner, uw.WakeSeq, p.now().Add(p.opts.Backoff))
		if workCtx.Err() == nil {
			slog.WarnContext(workCtx, "worker acquire user failed", "owner", p.owner, "lane", docstore.LaneScheduler, "user_id", uw.UserID, "err", err)
		}
		return
	}
	defer release()

	turnCtx, stopRenew := p.startUserWakeRenewer(workCtx, uw.UserID, docstore.LaneScheduler, state.SchedulerLease)
	var nextDue *time.Time
	var runErr error
	if p.opts.RunScheduler != nil {
		nextDue, runErr = p.opts.RunScheduler(turnCtx, userApp)
	} else {
		runErr = userApp.Schedule(scheduler.WithoutTickArm(turnCtx))
	}
	stopRenew()

	fCtx, fCancel := finCtx()
	defer fCancel()

	if runErr != nil {
		if !errors.Is(runErr, context.Canceled) && workCtx.Err() == nil {
			slog.WarnContext(workCtx, "worker scheduler will retry", "owner", p.owner, "lane", docstore.LaneScheduler, "user_id", uw.UserID, "err", runErr)
		}
		_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneScheduler, p.owner, uw.WakeSeq, p.now().Add(p.opts.Backoff))
		return
	}

	if p.opts.RunScheduler == nil {
		nextDue = userApp.NextSchedulerWake(fCtx)
	}

	if nextDue == nil {
		parked, parkErr := p.leases.ParkUserWake(fCtx, uw.UserID, docstore.LaneScheduler, p.owner, uw.WakeSeq)
		if parkErr != nil {
			_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneScheduler, p.owner, uw.WakeSeq, p.now().Add(p.opts.Backoff))
			return
		}
		if !parked {
			_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneScheduler, p.owner, 0, time.Time{})
			signalChan(p.schedWakeCh)
		}
		return
	}

	_ = p.leases.ReleaseUserWake(fCtx, uw.UserID, docstore.LaneScheduler, p.owner, uw.WakeSeq, *nextDue)
	if !nextDue.After(p.now()) {
		signalChan(p.schedWakeCh)
	}
}
