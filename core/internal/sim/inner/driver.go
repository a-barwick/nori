package inner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"nori/internal/agent"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/sim/env"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/task"
)

// ErrInjectedCleanupFault is returned when a configured transient cleanup failure triggers.
var ErrInjectedCleanupFault = errors.New("sim/inner: injected transient cleanup failure")

// WakeHub is a deterministic in-memory task.WakeNotifier for simulations and tests.
type WakeHub struct {
	mu   sync.Mutex
	next int
	subs map[int]wakeSub
}

type wakeSub struct {
	userID string
	runID  string
	ch     chan struct{}
}

var _ task.WakeNotifier = (*WakeHub)(nil)

// NewWakeHub constructs an empty WakeHub.
func NewWakeHub() *WakeHub {
	return &WakeHub{subs: make(map[int]wakeSub)}
}

// Subscribe registers a wake channel for (userID, runID).
func (h *WakeHub) Subscribe(userID, runID string) (<-chan struct{}, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	ch := make(chan struct{}, 1)
	h.subs[id] = wakeSub{userID: userID, runID: runID, ch: ch}
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, id)
		h.mu.Unlock()
	}
}

// NotifyTask wakes active subscribers matching (userID, runID) (or all tasks for userID when runID == "*").
func (h *WakeHub) NotifyTask(userID, runID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs {
		if (userID == "" || s.userID == userID) && (runID == "" || runID == "*" || s.runID == runID) {
			select {
			case s.ch <- struct{}{}:
			default:
			}
		}
	}
}

// NotifyPause wakes every active task subscriber immediately.
func (h *WakeHub) NotifyPause() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.subs {
		select {
		case s.ch <- struct{}{}:
		default:
		}
	}
}

// Deliver fans out a docstore.WakeEvent to matching subscribers.
func (h *WakeHub) Deliver(ev docstore.WakeEvent) {
	parsed := docstore.ParseWakePayload(ev.Payload)
	switch parsed.Kind {
	case docstore.WakeKindPause, docstore.WakeKindResync:
		h.NotifyPause()
	case docstore.WakeKindTask:
		h.NotifyTask(parsed.UserID, parsed.RunID)
	}
}

// DriverConfig configures a deterministic inner task execution Driver backed by real task.Runner
// and Postgres-native docstore.LeaseStore.
type DriverConfig struct {
	Clock          *env.Clock
	Backend        *memstore.Backend
	Leases         docstore.LeaseStore
	Wakes          task.WakeNotifier
	RNG            *rand.Rand
	IDs            *env.IDGen
	Faults         *memstore.FaultInjector
	ModelConfig    ModelConfig
	AdapterConfig  AdapterConfig
	CleanupErrProb float64
	DisableFaults  bool
}

// UserConfig registers one simulated user with Driver.
type UserConfig struct {
	UserID      string
	Store       state.ConversationStore
	Permissions *permissions.Service
	TimeZone    string
	UserFacts   string
}

// UserRuntime holds the wired real Nori inner-loop components for one user.
type UserRuntime struct {
	UserID      string
	Store       state.ConversationStore
	Permissions *permissions.Service
	Extensions  *ExtensionSource
	Adapters    *invoke.Registry
	Executor    *task.Executor
	Agent       *agent.Service
	Activities  *task.Activities
}

// PublicationRecord captures each committed terminal publication made through Driver.
type PublicationRecord struct {
	UserID             string
	RunID              string
	Revision           int64
	Status             state.RunStatus
	ExpectedIncomplete bool
	BypassRouting      bool
	Detail             string
	Spent              float64
	CostUnknown        bool
	At                 time.Time
}

type virtualLeaseSyncKey struct{}

type virtualLeaseSync struct {
	mu         sync.Mutex
	tick       chan time.Time
	ack        chan struct{}
	lastRenewT time.Time
}

func withVirtualLeaseSync(ctx context.Context, vs *virtualLeaseSync) context.Context {
	return context.WithValue(ctx, virtualLeaseSyncKey{}, vs)
}

func virtualLeaseSyncFromCtx(ctx context.Context) *virtualLeaseSync {
	vs, _ := ctx.Value(virtualLeaseSyncKey{}).(*virtualLeaseSync)
	return vs
}

func (vs *virtualLeaseSync) newTicker(now time.Time) func(time.Duration) (<-chan time.Time, func()) {
	return func(time.Duration) (<-chan time.Time, func()) {
		tick := make(chan time.Time)
		ack := make(chan struct{}, 1)
		vs.mu.Lock()
		vs.tick = tick
		vs.ack = ack
		vs.lastRenewT = now
		vs.mu.Unlock()
		return tick, func() {
			vs.mu.Lock()
			if vs.tick == tick {
				vs.tick = nil
				vs.ack = nil
			}
			vs.mu.Unlock()
		}
	}
}

func (vs *virtualLeaseSync) onRenewed(_, _ string) {
	vs.mu.Lock()
	ack := vs.ack
	vs.mu.Unlock()
	if ack != nil {
		select {
		case ack <- struct{}{}:
		default:
		}
	}
}

func (vs *virtualLeaseSync) maybeRenew(ctx context.Context, now time.Time) {
	vs.mu.Lock()
	tick := vs.tick
	ack := vs.ack
	due := tick != nil && now.Sub(vs.lastRenewT) >= 10*time.Second
	if due {
		vs.lastRenewT = now
	}
	vs.mu.Unlock()
	if !due {
		return
	}
	select {
	case tick <- now:
	case <-ctx.Done():
		return
	}
	select {
	case <-ack:
	case <-ctx.Done():
	}
}

// observingTaskStore wraps a user's TaskStore for Activities so Driver can observe real
// committed publications and inject cleanup faults without duplicating workflow logic.
type observingTaskStore struct {
	state.TaskStore
	driver *Driver
	userID string
}

func (s *observingTaskStore) Publish(ctx context.Context, req state.PublishRequest) (state.Run, error) {
	run, err := s.TaskStore.Publish(ctx, req)
	if err == nil && (run.Status != state.RunRunning || run.Finishing != nil) {
		status := req.Status
		if run.Status != "" && run.Status != state.RunRunning {
			status = run.Status
		}
		rec := PublicationRecord{
			UserID:             s.userID,
			RunID:              req.RunID,
			Revision:           req.Revision,
			Status:             status,
			ExpectedIncomplete: req.ExpectedIncomplete,
			BypassRouting:      req.BypassRouting,
			Detail:             req.Detail,
			Spent:              max(req.Spent, run.Spent),
			CostUnknown:        req.CostUnknown || run.CostUnknown,
			At:                 s.driver.clock.Now(),
		}
		s.driver.recordPublication(rec)
		if req.Status == state.RunFailed && req.ExpectedIncomplete && strings.Contains(strings.ToLower(req.Detail), "spending limit") {
			s.driver.markBudgetExhausted(req.RunID)
		}
	}
	return run, err
}

func (s *observingTaskStore) FinishCleanup(ctx context.Context, runID, detail string) error {
	if detail == "" && s.driver.hitCleanupFault() {
		_ = s.TaskStore.FinishCleanup(ctx, runID, ErrInjectedCleanupFault.Error())
		return ErrInjectedCleanupFault
	}
	return s.TaskStore.FinishCleanup(ctx, runID, detail)
}

// Driver orchestrates deterministic inner task execution across one or more simulated users
// using Nori's REAL task.Runner, task.Activities, agent.Service, task.Executor, permissions.Service,
// and Postgres-native docstore.LeaseStore.
type Driver struct {
	mu             sync.Mutex
	clock          *env.Clock
	backend        *memstore.Backend
	leases         docstore.LeaseStore
	wakes          task.WakeNotifier
	rng            *rand.Rand
	ids            *env.IDGen
	storeFaults    *memstore.FaultInjector
	cleanupErrProb float64
	faultsEnabled  bool
	workerSeq      int64

	Adapter *FakeAdapter
	Model   *FakeInnerModel

	users           map[string]*UserRuntime
	userOrder       []string
	runUsers        map[string]string
	budgetExhausted map[string]bool
	publications    []PublicationRecord
	pubIndex        map[string]int
}

// NewDriver constructs a deterministic inner-loop Driver wired to real task.Runner.
func NewDriver(cfg DriverConfig) *Driver {
	clock := cfg.Clock
	if clock == nil {
		clock = env.NewClock(time.Time{})
	}
	rng := cfg.RNG
	if rng == nil {
		rng = rand.New(rand.NewPCG(42, 42))
	}
	derive := func() *rand.Rand {
		return rand.New(rand.NewPCG(rng.Uint64(), rng.Uint64()))
	}
	driverRNG := derive()
	adapterRNG := derive()
	modelRNG := derive()
	ids := cfg.IDs
	if ids == nil {
		ids = env.NewIDGen(derive())
	}
	leases := cfg.Leases
	if leases == nil && cfg.Backend != nil {
		leases = cfg.Backend
	}
	wakes := cfg.Wakes
	if wakes == nil {
		wakes = NewWakeHub()
	}

	d := &Driver{
		clock:           clock,
		backend:         cfg.Backend,
		leases:          leases,
		wakes:           wakes,
		rng:             driverRNG,
		ids:             ids,
		storeFaults:     cfg.Faults,
		cleanupErrProb:  cfg.CleanupErrProb,
		faultsEnabled:   !cfg.DisableFaults,
		users:           make(map[string]*UserRuntime),
		runUsers:        make(map[string]string),
		budgetExhausted: make(map[string]bool),
		pubIndex:        make(map[string]int),
	}

	d.Adapter = NewFakeAdapter(clock, cfg.AdapterConfig, adapterRNG)
	d.Model = NewFakeInnerModel(clock, cfg.ModelConfig, modelRNG, d.readFamilySpend)
	if cfg.DisableFaults {
		d.Adapter.SetFaultsEnabled(false)
		d.Model.SetFaultsEnabled(false)
	}
	return d
}

// Leases returns the underlying LeaseStore configured for this Driver.
func (d *Driver) Leases() docstore.LeaseStore { return d.leases }

// Wakes returns the WakeNotifier configured for this Driver.
func (d *Driver) Wakes() task.WakeNotifier { return d.wakes }

// AddUser wires real Nori inner-loop components (agent.Service, task.Executor, task.Activities)
// for one user.
func (d *Driver) AddUser(cfg UserConfig) (*UserRuntime, error) {
	if cfg.UserID == "" {
		return nil, errors.New("sim/inner: UserConfig.UserID is required")
	}
	if cfg.Store == nil {
		return nil, errors.New("sim/inner: UserConfig.Store is required")
	}

	perms := cfg.Permissions
	if perms == nil {
		perms = &permissions.Service{
			Store:            cfg.Store,
			UnattendedWrites: "confirm_writes",
			UserSends:        "ask",
			Now:              d.clock.Now,
		}
	}

	loc := time.UTC
	if cfg.TimeZone != "" {
		if parsed, err := time.LoadLocation(cfg.TimeZone); err == nil {
			loc = parsed
		}
	}

	extSource := NewExtensionSource()
	adapters := invoke.NewRegistry()
	d.Adapter.Register(adapters)

	sleepFn := func(ctx context.Context, dur time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if dur > 0 {
			d.clock.Advance(dur)
			if vs := virtualLeaseSyncFromCtx(ctx); vs != nil {
				vs.maybeRenew(ctx, d.clock.Now())
			}
		}
		return ctx.Err()
	}
	noopTicker := func(_ time.Duration) (<-chan time.Time, func()) {
		// Never fires on wall-clock time; bounded() exits cleanly on wake or ctx cancel.
		return make(chan time.Time), func() {}
	}

	agentSvc := agent.New(cfg.Store, d.Model)
	agentSvc.UserFacts = cfg.UserFacts
	agentSvc.Extensions = extSource
	agentSvc.Now = d.clock.Now
	agentSvc.Capabilities = agent.Capabilities{Location: loc}

	exec := &task.Executor{
		Adapters:    adapters,
		Permissions: perms,
		Now:         d.clock.Now,
		Sleep:       sleepFn,
	}
	observedStore := &observingTaskStore{
		TaskStore: cfg.Store,
		driver:    d,
		userID:    cfg.UserID,
	}
	activities := &task.Activities{
		Agent:     agentSvc,
		Store:     observedStore,
		Executor:  exec,
		UserID:    cfg.UserID,
		Wakes:     d.wakes,
		Now:       d.clock.Now,
		Sleep:     sleepFn,
		NewTicker: noopTicker,
	}

	rt := &UserRuntime{
		UserID:      cfg.UserID,
		Store:       cfg.Store,
		Permissions: perms,
		Extensions:  extSource,
		Adapters:    adapters,
		Executor:    exec,
		Agent:       agentSvc,
		Activities:  activities,
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.users[cfg.UserID]; !exists {
		d.userOrder = append(d.userOrder, cfg.UserID)
		slices.Sort(d.userOrder)
	}
	d.users[cfg.UserID] = rt
	return rt, nil
}

// User returns the wired UserRuntime for userID, if registered.
func (d *Driver) User(userID string) *UserRuntime {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.users[userID]
}

// SetFaultsEnabled toggles fault injection across Adapter, Model, and Cleanup faults.
func (d *Driver) SetFaultsEnabled(enabled bool) {
	d.mu.Lock()
	d.faultsEnabled = enabled
	d.mu.Unlock()
	d.Adapter.SetFaultsEnabled(enabled)
	d.Model.SetFaultsEnabled(enabled)
}

// Publications returns a copy of all committed PublicationRecords.
func (d *Driver) Publications() []PublicationRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]PublicationRecord, len(d.publications))
	copy(out, d.publications)
	return out
}

// RunPhase returns the authoritative TaskControl.Phase for runID, or "untracked" if absent.
func (d *Driver) RunPhase(runID string) string {
	restore := d.pauseStoreFaults()
	defer restore()

	ctx := context.Background()
	userID, err := d.userForRun(ctx, runID)
	if err != nil {
		return "untracked"
	}
	rt := d.User(userID)
	if rt == nil {
		return "untracked"
	}
	ctrl, err := rt.Store.GetTaskControl(ctx, runID)
	if err != nil {
		return "untracked"
	}
	if ctrl.Phase == "" {
		return string(state.TaskPhaseSync)
	}
	return string(ctrl.Phase)
}

// CrashRun simulates a hard worker crash mid-task by acquiring both the discovery lease
// and authoritative TaskControl under a crashed worker identity without releasing either,
// leaving them stale until TaskLease (30s) expires and another worker takes over with Fence+1.
func (d *Driver) CrashRun(runID string) bool {
	if d.leases == nil {
		return false
	}
	restore := d.pauseStoreFaults()
	defer restore()

	ctx := context.Background()
	userID, err := d.userForRun(ctx, runID)
	if err != nil {
		return false
	}
	rt := d.User(userID)
	if rt == nil {
		return false
	}
	run, err := rt.Store.GetRun(ctx, runID)
	if err != nil || (run.Status != state.RunRunning && !run.CleanupPending) {
		return false
	}
	owner := d.nextWorkerOwner("crashed-worker")
	if _, ok, err := d.leases.ClaimTaskByID(ctx, userID, runID, owner, state.TaskLease); err != nil || !ok {
		return false
	}
	if _, _, err := rt.Store.AcquireTaskControl(ctx, runID, owner); err != nil {
		return false
	}
	return true
}

// RepairUser runs Postgres-native lease/run reconciliation (and StopPaused when paused) for userID.
func (d *Driver) RepairUser(ctx context.Context, userID string) error {
	rt := d.User(userID)
	if rt == nil {
		return fmt.Errorf("sim/inner: unknown user %q", userID)
	}
	if d.leases == nil {
		return errors.New("sim/inner: Driver Leases store is nil")
	}
	control, err := rt.Store.Control(ctx)
	if err != nil {
		return err
	}
	now := d.clock.Now()
	var errs []error

	openRuns, err := rt.Store.OpenTasks(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	cleanupRuns, err := rt.Store.CleanupTasks(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	runsByID := make(map[string]state.Run, len(openRuns)+len(cleanupRuns))
	for _, r := range openRuns {
		runsByID[r.ID] = r
	}
	for _, r := range cleanupRuns {
		runsByID[r.ID] = r
	}
	runIDs := make([]string, 0, len(runsByID))
	for id := range runsByID {
		runIDs = append(runIDs, id)
	}
	slices.Sort(runIDs)

	for _, id := range runIDs {
		run := runsByID[id]
		if control.Paused && (run.CancelRequested || run.Finishing != nil || (run.Status != state.RunRunning && run.CleanupPending)) {
			after, err := task.StopPaused(ctx, rt.Store, run.ID)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			if after.Status != state.RunRunning && !after.CleanupPending {
				if _, err := d.leases.DeleteTaskLease(ctx, userID, run.ID, 0); err != nil {
					errs = append(errs, err)
				}
				continue
			}
			run = after
			runsByID[id] = after
		}
		if run.Status == state.RunRunning || run.CleanupPending {
			if _, err := d.leases.EnsureTaskLease(ctx, userID, run.ID, now); err != nil {
				errs = append(errs, err)
			}
		}
	}

	leases, err := d.leases.ListTaskLeases(ctx, userID)
	if err != nil {
		errs = append(errs, err)
		return errors.Join(errs...)
	}
	routingPending, rpErr := rt.Store.UserRoutingPending(ctx)
	if rpErr != nil {
		errs = append(errs, rpErr)
	}

	for _, lease := range leases {
		run, err := rt.Store.GetRun(ctx, lease.RunID)
		if errors.Is(err, state.ErrNotFound) {
			if _, dErr := d.leases.DeleteTaskLease(ctx, userID, lease.RunID, 0); dErr != nil {
				errs = append(errs, dErr)
			}
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if run.Status != state.RunRunning && !run.CleanupPending {
			snap, sErr := rt.Store.Reconcile(ctx, lease.RunID)
			if sErr != nil {
				errs = append(errs, sErr)
				continue
			}
			var delCmdErr error
			for _, cmd := range snap.Commands {
				if !cmd.Delivered {
					if dErr := rt.Store.Delivered(ctx, cmd.ID); dErr != nil {
						delCmdErr = errors.Join(delCmdErr, dErr)
					}
				}
			}
			if delCmdErr != nil {
				errs = append(errs, delCmdErr)
				continue
			}
			if _, dErr := d.leases.DeleteTaskLease(ctx, userID, lease.RunID, 0); dErr != nil {
				errs = append(errs, dErr)
			}
			continue
		}
		if control.Paused || lease.Runnable {
			continue
		}
		shouldWake := false
		switch lease.WaitingReason {
		case docstore.WaitPaused:
			shouldWake = true
		case docstore.WaitRoutingPending:
			shouldWake = rpErr == nil && !routingPending
		case docstore.WaitQuestion:
			waitingOnSubtasks := run.Question != nil && run.Question.Kind == state.QuestionWork && len(state.RunningSubtasks(run)) > 0
			shouldWake = run.Question == nil || run.CancelRequested || (run.Question.ResolvedBy != "" && !waitingOnSubtasks)
		case docstore.WaitFinishing:
			shouldWake = !d.hasActiveOrUncleanChild(ctx, rt, run, runsByID)
		default:
			shouldWake = true
		}
		if shouldWake {
			if wErr := d.leases.WakeTask(ctx, userID, lease.RunID, now); wErr != nil {
				errs = append(errs, wErr)
			}
		}
	}

	// Reconcile any residual undelivered commands for terminal & clean runs whose lease is already gone.
	after := ""
	for {
		page, err := rt.Store.Pending(ctx, after)
		if err != nil {
			errs = append(errs, err)
			break
		}
		for _, cmd := range page.Commands {
			if _, active := runsByID[cmd.RunID]; active {
				continue
			}
			run, err := rt.Store.GetRun(ctx, cmd.RunID)
			if errors.Is(err, state.ErrNotFound) || (err == nil && run.Status != state.RunRunning && !run.CleanupPending) {
				if dErr := rt.Store.Delivered(ctx, cmd.ID); dErr != nil {
					errs = append(errs, dErr)
				}
			} else if err != nil {
				errs = append(errs, err)
			} else {
				if _, eErr := d.leases.EnsureTaskLease(ctx, userID, cmd.RunID, now); eErr != nil {
					errs = append(errs, eErr)
				}
			}
		}
		if page.Next == "" {
			break
		}
		after = page.Next
	}
	return errors.Join(errs...)
}

func (d *Driver) hasActiveOrUncleanChild(ctx context.Context, rt *UserRuntime, parent state.Run, activeRuns map[string]state.Run) bool {
	for _, link := range parent.Subtasks {
		child, err := rt.Store.GetRun(ctx, link.ID)
		if err != nil {
			if link.Status == state.RunRunning {
				return true
			}
			continue
		}
		if child.Status == state.RunRunning || child.CleanupPending {
			return true
		}
	}
	for _, other := range activeRuns {
		if other.ParentID == parent.ID && (other.Status == state.RunRunning || other.CleanupPending) {
			return true
		}
	}
	return false
}

// RepairAll runs RepairUser for every registered user in deterministic order.
func (d *Driver) RepairAll(ctx context.Context) error {
	d.mu.Lock()
	ids := slices.Clone(d.userOrder)
	d.mu.Unlock()
	var errs []error
	for _, uid := range ids {
		if err := d.RepairUser(ctx, uid); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SpawnSubtask creates 1..N child tasks for a parent parked on a QuestionWork request via
// real Store.AcceptCommandBatch (which atomically writes child task leases in the same tx).
func (d *Driver) SpawnSubtask(ctx context.Context, parentRunID string, childTexts ...string) ([]state.Run, error) {
	if len(childTexts) == 0 {
		childTexts = []string{"Complete delegated subtask work"}
	}
	userID, err := d.userForRun(ctx, parentRunID)
	if err != nil {
		return nil, err
	}
	rt := d.User(userID)
	if rt == nil {
		return nil, fmt.Errorf("sim/inner: unknown user %q", userID)
	}
	parent, err := rt.Store.GetRun(ctx, parentRunID)
	if err != nil {
		return nil, err
	}
	if parent.Question == nil || parent.Question.Kind != state.QuestionWork {
		return nil, fmt.Errorf("sim/inner: parent %s is not parked on a work request", parentRunID)
	}

	cmds := make([]state.Command, 0, len(childTexts))
	for _, text := range childTexts {
		childID := d.ids.Next()
		cmds = append(cmds, state.Command{
			ID:         d.ids.Next(),
			RunID:      childID,
			ParentID:   parent.ID,
			QuestionID: parent.Question.ID,
			Kind:       state.CommandStart,
			Text:       text,
		})
	}

	receipts, err := rt.Store.AcceptCommandBatch(ctx, cmds, nil)
	if err != nil {
		return nil, err
	}

	d.mu.Lock()
	for _, r := range receipts {
		d.runUsers[r.Run.ID] = userID
	}
	d.mu.Unlock()

	children := make([]state.Run, 0, len(receipts))
	for _, r := range receipts {
		children = append(children, r.Run)
	}
	return children, nil
}

// StepRun advances the task lease for runID by ONE real task.Runner step.
func (d *Driver) StepRun(ctx context.Context, runID string) (bool, error) {
	userID, err := d.userForRun(ctx, runID)
	if err != nil {
		return false, nil
	}
	return d.StepUserRun(ctx, userID, runID)
}

// StepUserRun claims runID from LeaseStore and executes ONE state-machine step through real task.Runner.
func (d *Driver) StepUserRun(ctx context.Context, userID, runID string) (bool, error) {
	_, progressed, err := d.runWithBudget(ctx, userID, runID, "", 1)
	return progressed, err
}

// RunUserTask claims runID from LeaseStore and runs real task.Runner until completion, park,
// busy, backoff, or cancellation (unlimited StepBudget).
func (d *Driver) RunUserTask(ctx context.Context, userID, runID, owner string) (task.RunOutcome, bool, error) {
	return d.runWithBudget(ctx, userID, runID, owner, 0)
}

func (d *Driver) runWithBudget(ctx context.Context, userID, runID, owner string, stepBudget int) (task.RunOutcome, bool, error) {
	rt := d.User(userID)
	if rt == nil {
		return "", false, fmt.Errorf("sim/inner: user %q not registered", userID)
	}
	if d.leases == nil {
		return "", false, errors.New("sim/inner: Driver Leases store is nil")
	}
	if owner == "" {
		owner = d.nextWorkerOwner("sim-worker")
	}

	claim, ok, err := d.leases.ClaimTaskByID(ctx, userID, runID, owner, state.TaskLease)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}

	d.mu.Lock()
	d.runUsers[runID] = userID
	d.mu.Unlock()

	vs := &virtualLeaseSync{}
	runCtx := withVirtualLeaseSync(withSimUser(ctx, userID), vs)
	runner := &task.Runner{
		Activities:     rt.Activities,
		Leases:         d.leases,
		UserID:         userID,
		Owner:          owner,
		Wakes:          d.wakes,
		Now:            d.clock.Now,
		NewTicker:      vs.newTicker(d.clock.Now()),
		StepBudget:     stepBudget,
		OnLeaseRenewed: vs.onRenewed,
	}
	outcome, err := runner.Run(runCtx, claim)
	return outcome, true, err
}

// StepAny picks one currently claimable task lease across all users in deterministic sorted
// order and steps it once via real task.Runner. Returns ("", false, nil) when no claimable lease exists.
func (d *Driver) StepAny(ctx context.Context) (string, bool, error) {
	if d.leases == nil {
		return "", false, nil
	}
	leases, err := d.leases.ListTaskLeases(ctx, "")
	if err != nil {
		return "", false, err
	}
	now := d.clock.Now()
	for _, row := range leases {
		if !row.Runnable || row.WakeAt.After(now) {
			continue
		}
		if row.LeaseUntil != nil && row.LeaseUntil.After(now) {
			continue
		}
		if d.User(row.UserID) == nil {
			continue
		}
		progressed, err := d.StepUserRun(ctx, row.UserID, row.RunID)
		if progressed || err != nil {
			return row.RunID, progressed, err
		}
	}
	return "", false, nil
}

// NextWakeTime returns the earliest future WakeAt or LeaseUntil among runnable task leases,
// allowing deterministic drain loops to advance virtual time past backoffs or crashed leases.
func (d *Driver) NextWakeTime(ctx context.Context) (time.Time, bool) {
	if d.leases == nil {
		return time.Time{}, false
	}
	leases, err := d.leases.ListTaskLeases(ctx, "")
	if err != nil {
		return time.Time{}, false
	}
	now := d.clock.Now()
	var earliest time.Time
	found := false
	consider := func(t time.Time) {
		if !t.After(now) {
			return
		}
		if !found || t.Before(earliest) {
			earliest = t
			found = true
		}
	}
	for _, row := range leases {
		if !row.Runnable {
			continue
		}
		if row.WakeAt.After(now) {
			consider(row.WakeAt)
		}
		if row.LeaseUntil != nil && row.LeaseUntil.After(now) {
			consider(*row.LeaseUntil)
		}
	}
	return earliest, found
}

func (d *Driver) nextWorkerOwner(prefix string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.workerSeq++
	return fmt.Sprintf("%s-%d", prefix, d.workerSeq)
}

func (d *Driver) hitCleanupFault() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.faultsEnabled || d.cleanupErrProb <= 0 {
		return false
	}
	return d.rng.Float64() < d.cleanupErrProb
}

func (d *Driver) recordPublication(rec PublicationRecord) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := rec.UserID + ":" + rec.RunID
	if idx, exists := d.pubIndex[key]; exists {
		d.publications[idx] = rec
		return
	}
	d.pubIndex[key] = len(d.publications)
	d.publications = append(d.publications, rec)
}

func (d *Driver) markBudgetExhausted(runID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.budgetExhausted[runID] = true
}

func (d *Driver) pauseStoreFaults() func() {
	if d.storeFaults == nil {
		return func() {}
	}
	was := d.storeFaults.Enabled()
	if !was {
		return func() {}
	}
	d.storeFaults.SetEnabled(false)
	return func() { d.storeFaults.SetEnabled(true) }
}

func (d *Driver) userForRun(ctx context.Context, runID string) (string, error) {
	d.mu.Lock()
	if uid, ok := d.runUsers[runID]; ok {
		d.mu.Unlock()
		return uid, nil
	}
	ids := slices.Clone(d.userOrder)
	d.mu.Unlock()

	if d.leases != nil {
		if leases, err := d.leases.ListTaskLeases(ctx, ""); err == nil {
			for _, l := range leases {
				if l.RunID == runID {
					d.mu.Lock()
					d.runUsers[runID] = l.UserID
					d.mu.Unlock()
					return l.UserID, nil
				}
			}
		}
	}

	for _, uid := range ids {
		rt := d.User(uid)
		if rt == nil {
			continue
		}
		if _, err := rt.Store.GetRun(ctx, runID); err == nil {
			d.mu.Lock()
			d.runUsers[runID] = uid
			d.mu.Unlock()
			return uid, nil
		}
	}
	return "", fmt.Errorf("sim/inner: no user owns run %q", runID)
}

// readFamilySpend reads authoritative family spend fault-free right before a model call.
func (d *Driver) readFamilySpend(ctx context.Context, userID, runID string) (SpendSnapshot, bool) {
	restore := d.pauseStoreFaults()
	defer restore()

	if userID == "" {
		var err error
		userID, err = d.userForRun(ctx, runID)
		if err != nil {
			return SpendSnapshot{}, false
		}
	}
	rt := d.User(userID)
	if rt == nil {
		return SpendSnapshot{}, false
	}
	run, err := rt.Store.GetRun(ctx, runID)
	if err != nil {
		return SpendSnapshot{}, false
	}
	var parent *state.Run
	if run.ParentID != "" {
		if p, err := rt.Store.GetRun(ctx, run.ParentID); err == nil {
			parent = &p
		}
	}
	others, budget, _ := state.FamilyCost(run, parent)
	return SpendSnapshot{
		Revision:     run.Revision,
		SpentBefore:  run.Spent + others,
		OthersBefore: others,
		Budget:       budget,
	}, true
}

// sortedRunIDs returns all tracked run IDs in deterministic sorted order.
func (d *Driver) sortedRunIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]string, 0, len(d.runUsers))
	for id := range d.runUsers {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, cmp.Compare)
	return ids
}
