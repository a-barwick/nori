package sim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/env"
	"nori/internal/sim/inner"
	"nori/internal/sim/invariants"
	"nori/internal/sim/memstore"
	"nori/internal/sim/outer"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

const (
	fullDrainMaxRounds    = 96
	fullDrainMaxStepPerRd = 64
	fullDrainMaxTicks     = 128
)

type FullWorldConfig struct {
	NumUsers int

	UnattendedWrites string

	UserSends string

	TaskBudgetUSD float64

	StartTime time.Time

	TraceCapacity int

	DisableFaults bool

	StoreFaults         memstore.FaultConfig
	ReplyFaults         outer.ReplyFaultConfig
	ClassifierFaultProb float64
	SummaryFaultProb    float64
	ModelConfig         inner.ModelConfig
	AdapterConfig       inner.AdapterConfig
	CleanupErrProb      float64
}

type FullWorldStats struct {
	OuterInputs        int
	OuterProcessCalls  int
	OuterLeaseTakeover int
	OuterMidInputs     int
	OuterMidCancels    int
	InnerSteps         int
	InnerCrashes       int
	RepairPasses       int
	UserWakesClaimed   int
	SubtasksSpawned    int
	InteractionReplies int
	DrainInteractions  int
	RulesMutated       int
	AutomationsCreated int
	SchedulerTicks     int
	PauseToggles       int
	MemoryMutations    int
	RapidBursts        int
	DelayedConvWakes   int
	ConsolidateCalls   int
}

type fullInteractionRetry struct {
	id        string
	requestID string
	resp      permissions.Response
}

type FullUserWorld struct {
	User        users.User
	DB          *records.Database
	Artifacts   *memstore.Artifacts
	Store       state.ConversationStore
	Permissions *permissions.Service
	Scheduler   *scheduler.Service
	Outer       *outer.Harness
	Inner       *inner.UserRuntime

	ruleIDs           []string
	automationIDs     []string
	interactionCaches []fullInteractionRetry
	hookDepth         int
	introEnsured      bool
}

type FullWorld struct {
	Seed   int64
	Config FullWorldConfig
	RNG    *rand.Rand
	Clock  *env.Clock
	IDs    *env.IDGen
	Trace  *env.Trace

	Faults   *memstore.FaultInjector
	Backend  *memstore.Backend
	UsersDir *records.Users
	WakeHub  *inner.WakeHub
	Checker  *invariants.Checker
	Inner    *inner.Driver

	Users     []*FullUserWorld
	usersByID map[string]*FullUserWorld
	step      int
	workerSeq int64
	Stats     FullWorldStats
}

func NewFullWorld(seed int64, cfg FullWorldConfig) *FullWorld {
	rootRNG := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))
	deriveRNG := func() *rand.Rand {
		return rand.New(rand.NewPCG(rootRNG.Uint64(), rootRNG.Uint64()))
	}

	workloadRNG := deriveRNG()
	idGen := env.NewIDGen(deriveRNG())
	storeFaultRNG := deriveRNG()
	innerRNG := deriveRNG()
	innerIDs := env.NewIDGen(deriveRNG())

	if cfg.NumUsers <= 0 {
		cfg.NumUsers = 2 + workloadRNG.IntN(2)
	}
	if cfg.TaskBudgetUSD <= 0 {
		cfg.TaskBudgetUSD = 0.75
	}
	if cfg.TraceCapacity <= 0 {
		cfg.TraceCapacity = 4096
	}
	if cfg.UnattendedWrites == "" {
		if workloadRNG.IntN(2) == 0 {
			cfg.UnattendedWrites = "confirm_writes"
		} else {
			cfg.UnattendedWrites = "require_rule"
		}
	}
	if cfg.UserSends == "" {
		cfg.UserSends = "ask"
	}

	storeFaults := cfg.StoreFaults
	replyFaults := cfg.ReplyFaults
	modelCfg := cfg.ModelConfig
	adapterCfg := cfg.AdapterConfig
	cleanupErrProb := cfg.CleanupErrProb
	if !cfg.DisableFaults {
		if storeFaults == (memstore.FaultConfig{}) {
			storeFaults = memstore.FaultConfig{
				GetErrProb:        0.02,
				QueryErrProb:      0.02,
				ApplyErrProb:      0.025,
				ArtifactErrProb:   0.025,
				TxConflictProb:    0.05,
				CommitThenErrProb: 0.025,
			}
		}
		if replyFaults == (outer.ReplyFaultConfig{}) {
			replyFaults = outer.DefaultReplyFaultConfig()
		}
		if modelCfg == (inner.ModelConfig{}) {
			modelCfg = inner.DefaultModelConfig()
		}
		if adapterCfg == (inner.AdapterConfig{}) {
			adapterCfg = inner.DefaultAdapterConfig()
		}
		if cleanupErrProb == 0 {
			cleanupErrProb = 0.08
		}
	} else {
		if modelCfg == (inner.ModelConfig{}) {
			modelCfg = inner.ModelConfig{
				CostPerCall:            0.15,
				MaxStepsBeforeComplete: 3,
			}
		}
		cleanupErrProb = 0
	}

	clock := env.NewClock(cfg.StartTime)
	traceLog := env.NewTrace(cfg.TraceCapacity)
	faultInjector := memstore.NewFaultInjector(storeFaults, storeFaultRNG)
	if cfg.DisableFaults {
		faultInjector.SetEnabled(false)
	}
	backend := memstore.New(
		memstore.WithClock(clock.Now),
		memstore.WithFaultInjector(faultInjector),
	)
	usersDir := records.NewSharedUsers(backend)
	wakeHub := inner.NewWakeHub()

	checker := invariants.New(invariants.Config{
		Backend:     backend,
		Faults:      faultInjector,
		Now:         clock.Now,
		CheckLeases: true,
	})

	innerDriver := inner.NewDriver(inner.DriverConfig{
		Clock:          clock,
		Backend:        backend,
		Leases:         backend,
		Wakes:          wakeHub,
		RNG:            innerRNG,
		IDs:            innerIDs,
		Faults:         faultInjector,
		ModelConfig:    modelCfg,
		AdapterConfig:  adapterCfg,
		CleanupErrProb: cleanupErrProb,
		DisableFaults:  cfg.DisableFaults,
	})

	w := &FullWorld{
		Seed:      seed,
		Config:    cfg,
		RNG:       workloadRNG,
		Clock:     clock,
		IDs:       idGen,
		Trace:     traceLog,
		Faults:    faultInjector,
		Backend:   backend,
		UsersDir:  usersDir,
		WakeHub:   wakeHub,
		Checker:   checker,
		Inner:     innerDriver,
		Users:     make([]*FullUserWorld, 0, cfg.NumUsers),
		usersByID: make(map[string]*FullUserWorld, cfg.NumUsers),
	}

	for i := range cfg.NumUsers {
		tzName := simTimeZones[i%len(simTimeZones)]
		loc, err := time.LoadLocation(tzName)
		if err != nil {
			loc = time.UTC
		}

		uid := idGen.Next()
		if i == 0 && seed%2 == 0 {
			uid = users.LegacyID
		}
		u := users.User{
			ID:          uid,
			Email:       fmt.Sprintf("e2e-user-%d@example.com", i+1),
			DisplayName: fmt.Sprintf("E2E User %d", i+1),
			TimeZone:    tzName,
		}
		if err := u.Validate(); err != nil {
			panic(fmt.Sprintf("sim: invalid generated user: %v", err))
		}

		artifacts := memstore.NewArtifacts(memstore.WithArtifactsFaultInjector(faultInjector))
		db := records.NewShared(backend, u, cfg.TaskBudgetUSD)
		db.Now = clock.Now
		store := state.Compose(db, artifacts)

		perms := &permissions.Service{
			Store:            store,
			UnattendedWrites: cfg.UnattendedWrites,
			UserSends:        cfg.UserSends,
			Now:              clock.Now,
		}

		userTZ := u.TimeZone
		userID := u.ID
		sched := &scheduler.Service{
			Store:       store,
			Tasks:       store,
			Permissions: perms,
			Now:         clock.Now,
			Timezone:    func(context.Context) string { return userTZ },
			Arm: func(ctx context.Context, at time.Time) {
				_ = backend.WakeUser(ctx, userID, docstore.LaneScheduler, at)
			},
			Accept: func(ctx context.Context, cmd state.Command) (state.Run, error) {
				receipts, err := store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
				if err != nil {
					return state.Run{}, err
				}
				_ = checker.RecordCommandReceipts(userID, receipts)
				return receipts[0].Run, nil
			},
		}

		outerHarness := outer.NewHarness(outer.Config{
			UserID:              u.ID,
			UserFacts:           fmt.Sprintf("Simulated user %d (%s).", i+1, tzName),
			TimeZone:            loc,
			Store:               store,
			StoreFaults:         faultInjector,
			Clock:               clock,
			IDs:                 env.NewIDGen(deriveRNG()),
			RNG:                 deriveRNG(),
			Trace:               traceLog,
			NumWorkers:          2,
			ReplyFaults:         replyFaults,
			ClassifierFaultProb: cfg.ClassifierFaultProb,
			SummaryFaultProb:    cfg.SummaryFaultProb,
			DisableFaults:       cfg.DisableFaults,
		})

		innerRT, err := innerDriver.AddUser(inner.UserConfig{
			UserID:      u.ID,
			Store:       store,
			Permissions: perms,
			TimeZone:    u.TimeZone,
			UserFacts:   fmt.Sprintf("Simulated user %d (%s).", i+1, tzName),
		})
		if err != nil {
			panic(fmt.Sprintf("sim: inner.Driver.AddUser(%s): %v", u.ID, err))
		}

		uw := &FullUserWorld{
			User:        u,
			DB:          db,
			Artifacts:   artifacts,
			Store:       store,
			Permissions: perms,
			Scheduler:   sched,
			Outer:       outerHarness,
			Inner:       innerRT,
		}
		w.Users = append(w.Users, uw)
		w.usersByID[u.ID] = uw
	}

	slices.SortFunc(w.Users, func(a, b *FullUserWorld) int {
		return strings.Compare(a.User.ID, b.User.ID)
	})

	return w
}

func (w *FullWorld) User(userID string) *FullUserWorld {
	return w.usersByID[userID]
}

func (w *FullWorld) nextWorkerOwner(prefix string) string {
	w.workerSeq++
	return fmt.Sprintf("%s-%d", prefix, w.workerSeq)
}

func (w *FullWorld) SetFaultsEnabled(enabled bool) {
	w.Faults.SetEnabled(enabled)
	w.Inner.SetFaultsEnabled(enabled)
	for _, uw := range w.Users {
		uw.Outer.SetModelFaultsEnabled(enabled)
	}
}

func (w *FullWorld) SetPaused(ctx context.Context, paused bool) error {
	wasEnabled := w.Faults.Enabled()
	if wasEnabled {
		w.Faults.SetEnabled(false)
		defer w.Faults.SetEnabled(true)
	}
	err := w.UsersDir.SetPaused(ctx, paused)
	if err == nil && paused {
		w.WakeHub.NotifyPause()
	}
	return err
}

func (w *FullWorld) Paused(ctx context.Context) (bool, error) {
	wasEnabled := w.Faults.Enabled()
	if wasEnabled {
		w.Faults.SetEnabled(false)
		defer w.Faults.SetEnabled(true)
	}
	return w.UsersDir.Paused(ctx)
}

func (w *FullWorld) UserTargets() []invariants.UserTarget {
	targets := make([]invariants.UserTarget, len(w.Users))
	for i, uw := range w.Users {
		targets[i] = invariants.UserTarget{
			User:        uw.User,
			Store:       uw.Store,
			Permissions: uw.Permissions,
			Scheduler:   uw.Scheduler,
		}
	}
	return targets
}

func (w *FullWorld) Record(userID, actor, action, detail string) {
	w.Trace.Record(w.step, w.Clock.Now(), userID, actor, action, detail)
}

func (w *FullWorld) Digest() string {
	h := sha256.New()
	writeHashString(h, "sim.full_world.v2")
	writeHashString(h, w.Backend.Digest())
	writeHashUint64(h, uint64(len(w.Users)))
	for _, uw := range w.Users {
		writeHashString(h, uw.User.ID)
		writeHashString(h, uw.Artifacts.Digest())
	}
	writeHashString(h, w.Trace.Digest())
	writeHashString(h, w.Inner.Adapter.Digest())
	writeHashString(h, w.Inner.Model.Digest())
	writeHashInt64(h, w.Clock.Now().UTC().UnixNano())
	return hex.EncodeToString(h.Sum(nil))
}

func (w *FullWorld) DebugDump(ctx context.Context) string {
	wasStore := w.Faults.Enabled()
	w.Faults.SetEnabled(false)
	defer w.Faults.SetEnabled(wasStore)

	var b strings.Builder
	paused, _ := w.Paused(ctx)
	fmt.Fprintf(&b, "=== FullWorld DebugDump seed=%d step=%d paused=%v now=%s ===\n",
		w.Seed, w.step, paused, w.Clock.Now().Format(time.RFC3339Nano))
	for _, uw := range w.Users {
		fmt.Fprintf(&b, "-- User %s (owner=%s tz=%s) --\n", uw.User.ID, uw.User.DataOwner(), uw.User.TimeZone)
		if conv, _, err := uw.Store.ConversationStatus(ctx); err == nil {
			routingPending, _ := uw.Store.UserRoutingPending(ctx)
			fmt.Fprintf(&b, "  conv recv=%d cons=%d urecv=%d urout=%d active=%q owner=%q fence=%d routingPending=%v\n",
				conv.Received, conv.Consumed, conv.UserReceived, conv.UserRouted, conv.Active, conv.Owner, conv.Fence, routingPending)
		}
		if open, err := uw.Store.OpenTasks(ctx); err == nil {
			for _, r := range open {
				qID := ""
				qRes := ""
				if r.Question != nil {
					qID = r.Question.ID + "(" + r.Question.Kind + ")"
					qRes = r.Question.ResolvedBy
				}
				fmt.Fprintf(&b, "  open run=%s status=%s parent=%s rev=%d pos=%d inc=%d cancel=%v finishing=%v cleanup=%v phase=%s q=%s resolved=%q\n",
					r.ID, r.Status, r.ParentID, r.Revision, r.Position, r.Incorporated, r.CancelRequested, r.Finishing != nil, r.CleanupPending, w.Inner.RunPhase(r.ID), qID, qRes)
			}
		}
		if page, err := uw.Store.Pending(ctx, ""); err == nil {
			for _, c := range page.Commands {
				fmt.Fprintf(&b, "  pending cmd=%s run=%s kind=%s pos=%d rev=%d delivered=%v\n",
					c.ID, c.RunID, c.Kind, c.Position, c.Revision, c.Delivered)
			}
		}
		if pendingInter, _, err := uw.Permissions.Interactions(ctx, "pending", "", "", 20); err == nil {
			for _, card := range pendingInter {
				fmt.Fprintf(&b, "  interaction id=%s task=%s kind=%s rev=%d status=%s\n",
					card.ID, card.TaskID, card.Kind, card.Revision, card.Status)
			}
		}
		if leases, err := w.Backend.ListTaskLeases(ctx, uw.User.ID); err == nil {
			for _, l := range leases {
				fmt.Fprintf(&b, "  lease run=%s runnable=%v wait=%q owner=%q seq=%d wakeAt=%s leaseUntil=%v\n",
					l.RunID, l.Runnable, l.WaitingReason, l.Owner, l.WakeSeq, l.WakeAt.Format(time.RFC3339Nano), l.LeaseUntil)
			}
		}
		if wakes, err := w.Backend.ListUserWakes(ctx, uw.User.ID); err == nil {
			for _, wk := range wakes {
				fmt.Fprintf(&b, "  wake lane=%s runnable=%v owner=%q seq=%d wakeAt=%s leaseUntil=%v\n",
					wk.Lane, wk.Runnable, wk.Owner, wk.WakeSeq, wk.WakeAt.Format(time.RFC3339Nano), wk.LeaseUntil)
			}
		}
	}
	return b.String()
}

func (w *FullWorld) CheckAllSafety(ctx context.Context) error {
	targets := w.UserTargets()
	if err := w.Checker.CheckSafetyAt(ctx, targets, w.Clock.Now()); err != nil {
		return fmt.Errorf("coordination safety: %w", err)
	}
	for _, uw := range w.Users {
		if err := uw.Outer.CheckOuterInvariants(ctx); err != nil {
			return fmt.Errorf("outer safety [user=%s]: %w", uw.User.ID, err)
		}
	}
	if err := w.Inner.CheckInnerInvariants(ctx); err != nil {
		return fmt.Errorf("inner safety: %w", err)
	}
	return nil
}

func (w *FullWorld) RunFullSimulation(ctx context.Context, steps int) error {
	if err := w.CheckAllSafety(ctx); err != nil {
		return &SimError{
			Seed:  w.Seed,
			Step:  0,
			Phase: "safety.initial",
			Err:   err,
			Trace: w.Trace.FormatLast(40),
		}
	}

	w.installOuterHooks()
	defer w.clearOuterHooks()

	for i, uw := range w.Users {
		uw.Outer.SetStep(0)
		if id, err := uw.Outer.AcceptInput(ctx, fmt.Sprintf("initial e2e request for user %d", i+1)); err == nil {
			w.Stats.OuterInputs++
			w.Record(uw.User.ID, "user", "chat_seed", fmt.Sprintf("id=%s", id))
		}
	}

	for step := 1; step <= steps; step++ {
		w.step = step
		for _, uw := range w.Users {
			uw.Outer.SetStep(step)
		}

		w.runFullChaosStep(ctx)

		if err := w.CheckAllSafety(ctx); err != nil {
			return &SimError{
				Seed:  w.Seed,
				Step:  step,
				Phase: "safety",
				Err:   err,
				Trace: w.Trace.FormatLast(40),
			}
		}
	}

	if err := w.DrainToQuiescence(ctx); err != nil {
		return &SimError{
			Seed:  w.Seed,
			Step:  steps,
			Phase: "quiescence",
			Err:   err,
			Trace: w.Trace.FormatLast(40),
		}
	}
	return nil
}

func (w *FullWorld) installOuterHooks() {
	for _, uw := range w.Users {
		u := uw
		u.Outer.SetOnBeforeRespond(func(ctx context.Context, ev outer.BeforeRespondEvent) error {
			if u.hookDepth >= 2 {
				return nil
			}
			u.hookDepth++
			defer func() { u.hookDepth-- }()

			if ev.LeaseExpired && len(u.Outer.Workers) > 1 && w.RNG.IntN(100) < 70 {
				otherIdx := (ev.WorkerIdx + 1) % len(u.Outer.Workers)
				w.Stats.OuterLeaseTakeover++
				_ = w.processOuterWorker(ctx, u, otherIdx)
				return nil
			}

			roll := w.RNG.IntN(100)
			switch {
			case roll < 14:
				w.Stats.OuterMidInputs++
				w.Stats.OuterInputs++
				_, _ = u.Outer.AcceptInput(ctx, fmt.Sprintf("mid-flight chat step=%d call=%d", w.step, ev.CallNum))
				if w.RNG.IntN(3) == 0 {
					if c, activeTurn, err := u.Store.ConversationStatus(ctx); err == nil && activeTurn != nil && !activeTurn.Frozen && c.Received > activeTurn.Through {
						u.Outer.TickLeaseWatcher(ctx, ev.WorkerIdx, true)
					}
				}
			case roll < 22:
				if c, _, err := u.Store.ConversationStatus(ctx); err == nil && c.Active != "" {
					w.Stats.OuterMidCancels++
					_ = u.Outer.Cancel(ctx, c.Active)
				}
			case roll < 38 && ev.SimulatedDelay >= 10*time.Second && !ev.LeaseExpired:
				u.Outer.TickLeaseWatcher(ctx, ev.WorkerIdx, false)
			}
			return nil
		})
	}
}

func (w *FullWorld) clearOuterHooks() {
	for _, uw := range w.Users {
		uw.Outer.SetOnBeforeRespond(nil)
	}
}

func (w *FullWorld) processOuterWorker(ctx context.Context, uw *FullUserWorld, workerIdx int) error {
	w.Stats.OuterProcessCalls++
	if w.Faults.Enabled() {
		wasKeyed, prevSeed := w.Faults.KeyedMode()
		prevEpoch := w.Faults.KeyedEpoch()
		keyedSeed := uint64(w.Seed) ^ 0x517cc1b727220a95
		epoch := (uint64(w.step) << 32) | uint64(w.Stats.OuterProcessCalls)
		w.Faults.SetKeyedMode(true, keyedSeed)
		w.Faults.SetKeyedEpoch(epoch)
		defer func() {
			if wasKeyed {
				w.Faults.SetKeyedMode(true, prevSeed)
				w.Faults.SetKeyedEpoch(prevEpoch)
			} else {
				w.Faults.SetKeyedMode(false, 0)
			}
		}()
	}
	return uw.Outer.ProcessWorker(ctx, workerIdx)
}

func (w *FullWorld) pickUser() *FullUserWorld {
	return w.Users[w.RNG.IntN(len(w.Users))]
}

func (w *FullWorld) pickOuterWorkUser(ctx context.Context) *FullUserWorld {
	if w.RNG.IntN(100) < 80 {
		was := w.Faults.Enabled()
		if was {
			w.Faults.SetEnabled(false)
		}
		var busy []*FullUserWorld
		for _, uw := range w.Users {
			if c, _, err := uw.Store.ConversationStatus(ctx); err == nil && (c.Pending() || c.Active != "") {
				busy = append(busy, uw)
			}
		}
		if was {
			w.Faults.SetEnabled(true)
		}
		if len(busy) > 0 {
			return busy[w.RNG.IntN(len(busy))]
		}
	}
	return w.pickUser()
}

func (w *FullWorld) pickInnerWorkUser(ctx context.Context) *FullUserWorld {
	if w.RNG.IntN(100) < 85 {
		snap := w.Backend.Snapshot()
		now := w.Clock.Now()
		seen := make(map[string]bool, len(w.Users))
		var active []*FullUserWorld
		for _, l := range snap.TaskLeases {
			if l.Runnable && !l.WakeAt.After(now) && (l.LeaseUntil == nil || !l.LeaseUntil.After(now)) && !seen[l.UserID] {
				if uw := w.User(l.UserID); uw != nil {
					seen[l.UserID] = true
					active = append(active, uw)
				}
			}
		}
		if len(active) > 0 {
			return active[w.RNG.IntN(len(active))]
		}
	}
	return w.pickUser()
}

func (w *FullWorld) pickPermissionWorkUser(ctx context.Context) *FullUserWorld {
	if w.RNG.IntN(100) < 85 {
		was := w.Faults.Enabled()
		if was {
			w.Faults.SetEnabled(false)
		}
		var waiting []*FullUserWorld
		for _, uw := range w.Users {
			if pending, _, err := uw.Permissions.Interactions(ctx, "pending", "", "", 5); err == nil && len(pending) > 0 {
				waiting = append(waiting, uw)
				continue
			}
			if open, err := uw.Store.OpenTasks(ctx); err == nil {
				for _, r := range open {
					if r.Question != nil && r.Question.ResolvedBy == "" {
						waiting = append(waiting, uw)
						break
					}
				}
			}
		}
		if was {
			w.Faults.SetEnabled(true)
		}
		if len(waiting) > 0 {
			return waiting[w.RNG.IntN(len(waiting))]
		}
	}
	return w.pickUser()
}

func (w *FullWorld) runFullChaosStep(ctx context.Context) {
	roll := w.RNG.IntN(100)
	switch {
	case roll < 14:
		w.stepFullOuterInput(ctx, w.pickUser())
	case roll < 28:
		uw := w.pickOuterWorkUser(ctx)
		workerIdx := w.RNG.IntN(len(uw.Outer.Workers))
		err := w.processOuterWorker(ctx, uw, workerIdx)
		w.Record(uw.User.ID, fmt.Sprintf("outer-%d", workerIdx), "process", fmt.Sprintf("err=%v", err))
	case roll < 54:
		w.stepFullInnerExec(ctx, w.pickInnerWorkUser(ctx))
	case roll < 66:
		w.stepFullPermissions(ctx, w.pickPermissionWorkUser(ctx))
	case roll < 75:
		w.stepFullScheduler(ctx, w.pickUser())
	case roll < 85:
		w.stepFullMemory(ctx, w.pickUser())
	case roll < 93:
		w.stepFullReconcileAndWakes(ctx, w.pickUser())
	default:
		w.stepFullTimeAndPause(ctx)
	}
}

func (w *FullWorld) stepFullOuterInput(ctx context.Context, uw *FullUserWorld) {
	if !uw.introEnsured && w.RNG.IntN(5) == 0 {
		uw.introEnsured = true
		err := uw.Outer.EnsureIntroduction(ctx)
		w.Record(uw.User.ID, "outer", "ensure_intro", fmt.Sprintf("err=%v", err))
		return
	}

	choice := w.RNG.IntN(100)
	accepted := uw.Outer.AcceptedInputs()

	switch {
	case choice < 56 || len(accepted) == 0:
		text := fmt.Sprintf("e2e user request step=%d #%d", w.step, w.RNG.IntN(1000))
		if w.RNG.IntN(2) == 0 {
			text = fmt.Sprintf("e2e user request step=%d remember preference for project_omega_%d", w.step, w.RNG.IntN(16))
		}
		id, err := uw.Outer.AcceptInput(ctx, text)
		if err == nil {
			w.Stats.OuterInputs++
		}
		w.Record(uw.User.ID, "user", "chat_input", fmt.Sprintf("id=%s err=%v", id, err))

	case choice < 72:
		burst := 4 + w.RNG.IntN(5)
		acceptedCount := 0
		for i := range burst {
			text := fmt.Sprintf("rapid burst step=%d msg=%d topic=project_omega_%d", w.step, i, w.RNG.IntN(16))
			if _, err := uw.Outer.AcceptInput(ctx, text); err == nil {
				w.Stats.OuterInputs++
				acceptedCount++
			}
			w.Clock.Advance(100 * time.Millisecond)
		}
		w.Stats.RapidBursts++
		w.Record(uw.User.ID, "user", "chat_burst", fmt.Sprintf("count=%d/%d", acceptedCount, burst))

	case choice < 84:
		id := accepted[w.RNG.IntN(len(accepted))]
		if w.RNG.IntN(2) == 0 {
			err := uw.Outer.AcceptInputWithID(ctx, id, "conflicting e2e payload")
			w.Record(uw.User.ID, "user", "chat_conflict", fmt.Sprintf("id=%s err=%v", id, err))
		} else {
			if msg, gerr := uw.Store.ReadConversationMessage(ctx, id); gerr == nil {
				err := uw.Outer.AcceptInputWithID(ctx, id, msg.Text)
				w.Record(uw.User.ID, "user", "chat_replay", fmt.Sprintf("id=%s err=%v", id, err))
			}
		}

	case choice < 93:
		if c, _, err := uw.Store.ConversationStatus(ctx); err == nil {
			if c.Active != "" {
				err = uw.Outer.Cancel(ctx, c.Active)
				w.Record(uw.User.ID, "user", "conv_cancel", fmt.Sprintf("turn=%s err=%v", c.Active, err))
				return
			}
		}
		err := uw.Outer.Cancel(ctx, accepted[len(accepted)-1])
		w.Record(uw.User.ID, "user", "conv_cancel_input", fmt.Sprintf("id=%s err=%v", accepted[len(accepted)-1], err))

	default:
		if msgs, _, err := uw.Store.ConversationMessages(ctx, ""); err == nil {
			for i := len(msgs) - 1; i >= 0; i-- {
				if msgs[i].Role == "assistant" {
					merr := uw.Store.MarkSeen(ctx, msgs[i].ID)
					w.Record(uw.User.ID, "user", "mark_seen", fmt.Sprintf("msg=%s err=%v", msgs[i].ID, merr))
					return
				}
			}
		}
		id, err := uw.Outer.AcceptInput(ctx, fmt.Sprintf("e2e fallback step=%d", w.step))
		if err == nil {
			w.Stats.OuterInputs++
		}
		w.Record(uw.User.ID, "user", "chat_input", fmt.Sprintf("id=%s err=%v", id, err))
	}
}

func (w *FullWorld) stepFullInnerExec(ctx context.Context, uw *FullUserWorld) {
	if paused, err := w.Paused(ctx); err == nil && paused {
		err := w.Inner.RepairUser(ctx, uw.User.ID)
		w.Record(uw.User.ID, "inner", "paused_repair", fmt.Sprintf("err=%v", err))
		return
	}

	leases, err := w.Backend.ListTaskLeases(ctx, uw.User.ID)
	if err != nil {
		w.Record(uw.User.ID, "inner", "list_leases_err", err.Error())
		return
	}
	now := w.Clock.Now()
	var claimable []docstore.TaskLease
	for _, l := range leases {
		if !l.Runnable || l.WakeAt.After(now) {
			continue
		}
		if l.LeaseUntil != nil && l.LeaseUntil.After(now) {
			continue
		}
		claimable = append(claimable, l)
	}

	if len(claimable) == 0 {
		if open, oerr := uw.Store.OpenTasks(ctx); oerr == nil {
			if parent, ok := pickParentWithWorkQuestion(open, w.RNG); ok {
				children, serr := w.Inner.SpawnSubtask(ctx, parent.ID, fmt.Sprintf("E2E subtask for %s at step %d", parent.ID[:8], w.step))
				if serr == nil {
					w.Stats.SubtasksSpawned += len(children)
				}
				w.Record(uw.User.ID, "inner", "spawn_subtask", fmt.Sprintf("parent=%s children=%d err=%v", parent.ID, len(children), serr))
				return
			}
		}
		rerr := w.Inner.RepairUser(ctx, uw.User.ID)
		w.Record(uw.User.ID, "inner", "repair_for_start", fmt.Sprintf("err=%v", rerr))
		return
	}

	chosen := claimable[w.RNG.IntN(len(claimable))]
	runID := chosen.RunID

	actionRoll := w.RNG.IntN(100)
	if actionRoll < 6 {
		if w.Inner.CrashRun(runID) {
			w.Stats.InnerCrashes++
			w.Record(uw.User.ID, "inner", "crash_run", fmt.Sprintf("run=%s", runID))
			return
		}
	}

	progressed, err := w.Inner.StepUserRun(ctx, uw.User.ID, runID)
	if progressed {
		w.Stats.InnerSteps++
		w.Record(uw.User.ID, "inner", "step_run", fmt.Sprintf("run=%s phase=%s err=%v", runID, w.Inner.RunPhase(runID), err))
		return
	}

	for _, other := range claimable {
		if other.RunID == runID {
			continue
		}
		if p, stepErr := w.Inner.StepUserRun(ctx, uw.User.ID, other.RunID); p {
			w.Stats.InnerSteps++
			w.Record(uw.User.ID, "inner", "step_run_alt", fmt.Sprintf("run=%s phase=%s err=%v", other.RunID, w.Inner.RunPhase(other.RunID), stepErr))
			return
		}
	}

	if open, oerr := uw.Store.OpenTasks(ctx); oerr == nil {
		if parent, ok := pickParentWithWorkQuestion(open, w.RNG); ok {
			children, serr := w.Inner.SpawnSubtask(ctx, parent.ID, fmt.Sprintf("E2E subtask for %s at step %d", parent.ID[:8], w.step))
			if serr == nil {
				w.Stats.SubtasksSpawned += len(children)
			}
			w.Record(uw.User.ID, "inner", "spawn_subtask", fmt.Sprintf("parent=%s children=%d err=%v", parent.ID, len(children), serr))
			return
		}
	}

	w.Record(uw.User.ID, "inner", "all_parked", fmt.Sprintf("claimable=%d", len(claimable)))
}

func (w *FullWorld) stepFullPermissions(ctx context.Context, uw *FullUserWorld) {
	roll := w.RNG.IntN(100)

	switch {
	case roll < 55:
		if len(uw.interactionCaches) > 0 && w.RNG.IntN(6) == 0 {
			cached := uw.interactionCaches[w.RNG.IntN(len(uw.interactionCaches))]
			_, ans, err := uw.Permissions.Respond(ctx, cached.id, cached.requestID, 1, cached.resp)
			w.Record(uw.User.ID, "perms", "respond_retry", fmt.Sprintf("id=%s ans=%v err=%v", cached.id, ans != nil, err))
			return
		}

		pending, _, err := uw.Permissions.Interactions(ctx, "pending", "", "", 20)
		if err != nil {
			w.Record(uw.User.ID, "perms", "list_err", err.Error())
			return
		}
		if len(pending) > 0 {
			card := pending[w.RNG.IntN(len(pending))]

			if card.Kind == "approval" && card.TaskID != "" && w.RNG.IntN(100) < 15 {
				steerCmd := state.Command{
					ID:        w.IDs.Next(),
					RunID:     card.TaskID,
					Kind:      state.CommandSteer,
					Text:      fmt.Sprintf("steer superseding approval %s at step %d", card.ID, w.step),
					Interrupt: w.RNG.IntN(2) == 0,
				}
				receipts, serr := uw.Store.AcceptCommandBatch(ctx, []state.Command{steerCmd}, nil)
				if serr == nil {
					_ = w.Checker.RecordCommandReceipts(uw.User.ID, receipts)
					w.WakeHub.NotifyTask(uw.User.ID, card.TaskID)
				}
				w.Record(uw.User.ID, "perms", "steer_supersede", fmt.Sprintf("card=%s task=%s err=%v", card.ID, card.TaskID, serr))
				return
			}

			var resp permissions.Response
			switch card.Kind {
			case "approval":
				r := w.RNG.IntN(10)
				switch {
				case r < 5:
					resp = permissions.Response{Kind: "approve", ActionDigest: card.ActionDigest}
				case r < 7:
					resp = permissions.Response{Kind: "approve", ActionDigest: card.ActionDigest, RememberRule: true}
				case r < 9:
					resp = permissions.Response{Kind: "decline", ActionDigest: card.ActionDigest}
				default:
					resp = permissions.Response{Kind: "approve", ActionDigest: "wrong_digest"}
				}
			default:
				resp = permissions.Response{Kind: "answer", Text: fmt.Sprintf("e2e answer at step %d", w.step)}
			}

			expectedRev := card.Revision
			if w.RNG.IntN(10) == 0 {
				expectedRev = card.Revision + 1
			}

			reqID := w.IDs.Next()
			_, ans, rerr := uw.Permissions.Respond(ctx, card.ID, reqID, expectedRev, resp)
			if rerr == nil || errors.Is(rerr, memstore.ErrCommitThenError) {
				w.Stats.InteractionReplies++
				uw.interactionCaches = append(uw.interactionCaches, fullInteractionRetry{
					id:        card.ID,
					requestID: reqID,
					resp:      resp,
				})
				if len(uw.interactionCaches) > 32 {
					uw.interactionCaches = uw.interactionCaches[len(uw.interactionCaches)-32:]
				}
			}
			if rerr == nil && ans != nil && w.RNG.IntN(4) != 0 {
				w.deliverSingleAnswer(ctx, uw, card.ID, *ans)
			}
			w.Record(uw.User.ID, "perms", "respond", fmt.Sprintf("id=%s task=%s kind=%s ans=%v err=%v",
				card.ID, card.TaskID, resp.Kind, ans != nil, rerr))
			return
		}

		if open, oerr := uw.Store.OpenTasks(ctx); oerr == nil {
			if qRun, ok := pickRunWithUnresolvedQuestion(open, w.RNG); ok {
				cmd := state.Command{
					ID:         w.IDs.Next(),
					RunID:      qRun.ID,
					Kind:       state.CommandAnswer,
					QuestionID: qRun.Question.ID,
					Text:       fmt.Sprintf("direct e2e answer step %d", w.step),
				}
				receipts, aerr := uw.Store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
				if aerr == nil {
					_ = w.Checker.RecordCommandReceipts(uw.User.ID, receipts)
					w.WakeHub.NotifyTask(uw.User.ID, qRun.ID)
				}
				w.Record(uw.User.ID, "perms", "answer_question", fmt.Sprintf("run=%s q=%s err=%v", qRun.ID, qRun.Question.ID, aerr))
				return
			}
		}
		derr := w.deliverUndeliveredAnswers(ctx, uw)
		w.Record(uw.User.ID, "perms", "deliver_answers", fmt.Sprintf("err=%v", derr))

	case roll < 80:
		if len(uw.ruleIDs) > 0 && w.RNG.IntN(3) == 0 {
			id := uw.ruleIDs[w.RNG.IntN(len(uw.ruleIDs))]
			if rule, err := uw.Permissions.Rule(ctx, id); err == nil {
				if w.RNG.IntN(2) == 0 {
					_, rerr := uw.Permissions.RevokeRule(ctx, id, w.IDs.Next(), rule.Revision)
					if rerr == nil {
						w.Stats.RulesMutated++
					}
					w.Record(uw.User.ID, "perms", "rule.revoke", fmt.Sprintf("id=%s rev=%d err=%v", id, rule.Revision, rerr))
				} else {
					_, uerr := uw.Permissions.SaveRule(ctx, id, w.IDs.Next(), rule.Revision, w.randomFullRuleEditable())
					if uerr == nil {
						w.Stats.RulesMutated++
					}
					w.Record(uw.User.ID, "perms", "rule.update", fmt.Sprintf("id=%s rev=%d err=%v", id, rule.Revision, uerr))
				}
				return
			}
		}
		if len(uw.ruleIDs) < 6 {
			reqID := w.IDs.Next()
			receipt, err := uw.Permissions.SaveRule(ctx, "", reqID, 0, w.randomFullRuleEditable())
			if err == nil {
				w.Stats.RulesMutated++
				var saved permissions.Rule
				if json.Unmarshal(receipt.Resource, &saved) == nil && saved.ID != "" {
					uw.ruleIDs = append(uw.ruleIDs, saved.ID)
				}
			}
			w.Record(uw.User.ID, "perms", "rule.create", fmt.Sprintf("req=%s err=%v", reqID, err))
			return
		}
		derr := w.deliverUndeliveredAnswers(ctx, uw)
		w.Record(uw.User.ID, "perms", "deliver_answers", fmt.Sprintf("err=%v", derr))

	default:
		err := w.deliverUndeliveredAnswers(ctx, uw)
		w.Record(uw.User.ID, "perms", "deliver_answers", fmt.Sprintf("err=%v", err))
	}
}

func (w *FullWorld) randomFullRuleEditable() permissions.RuleEditable {
	effect := []string{"allow", "ask", "deny"}[w.RNG.IntN(3)]
	toolSets := [][]string{
		{inner.ToolIDWrite},
		{inner.ToolIDSend},
		{inner.ToolIDWrite, inner.ToolIDSend},
		{permissions.Wildcard},
	}
	classes := [][]string{
		{"reversible_write"},
		{"sensitive_send"},
		{"unknown"},
		{"read", "reversible_write", "sensitive_send", "unknown"},
	}
	rule := permissions.RuleEditable{
		Effect:        effect,
		ToolIDs:       toolSets[w.RNG.IntN(len(toolSets))],
		ConnectionIDs: []string{permissions.Wildcard},
		ActionClasses: classes[w.RNG.IntN(len(classes))],
		Title:         fmt.Sprintf("e2e rule %d", w.step),
	}
	if effect == "allow" && w.RNG.IntN(2) == 0 {
		rule.MaxOperations = 1 + w.RNG.IntN(3)
	}
	if w.RNG.IntN(2) == 0 {
		exp := w.Clock.Now().Add(time.Duration(120+w.RNG.IntN(1800)) * time.Second)
		rule.ExpiresAt = &exp
	}
	return rule
}

func (w *FullWorld) deliverSingleAnswer(ctx context.Context, uw *FullUserWorld, interID string, ans permissions.Answer) {
	receipts, err := uw.Store.AcceptCommandBatch(ctx, []state.Command{{
		ID:         ans.CommandID,
		RunID:      ans.TaskID,
		Kind:       state.CommandAnswer,
		QuestionID: ans.Question,
		Text:       ans.Text,
	}}, nil)
	if err == nil {
		_ = w.Checker.RecordCommandReceipts(uw.User.ID, receipts)
		w.WakeHub.NotifyTask(uw.User.ID, ans.TaskID)
		uw.Permissions.MarkDelivered(ctx, interID)
	}
}

func (w *FullWorld) deliverUndeliveredAnswers(ctx context.Context, uw *FullUserWorld) error {
	answers, ids, err := uw.Permissions.UndeliveredAnswers(ctx)
	if err != nil {
		return err
	}
	for i, answer := range answers {
		run, runErr := uw.Store.GetRun(ctx, answer.TaskID)
		waiting := runErr == nil && run.Status == state.RunRunning && run.Question != nil && run.Question.ID == answer.Question && run.Question.ResolvedBy == ""
		if answer.Recheck && !waiting {
			continue
		}
		receipts, err := uw.Store.AcceptCommandBatch(ctx, []state.Command{{
			ID:         answer.CommandID,
			RunID:      answer.TaskID,
			Kind:       state.CommandAnswer,
			QuestionID: answer.Question,
			Text:       answer.Text,
		}}, nil)
		if err == nil {
			_ = w.Checker.RecordCommandReceipts(uw.User.ID, receipts)
			w.WakeHub.NotifyTask(uw.User.ID, answer.TaskID)
		}
		if errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy) {
			if current, e := uw.Store.GetRun(ctx, answer.TaskID); e == nil && current.Status == state.RunRunning && (current.Question == nil || current.Question.ID == answer.Question) {
				continue
			}
		}
		if (err == nil || errors.Is(err, state.ErrDuplicate) || errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy)) && !answer.Recheck {
			uw.Permissions.MarkDelivered(ctx, ids[i])
		}
	}
	return nil
}

func (w *FullWorld) stepFullScheduler(ctx context.Context, uw *FullUserWorld) {
	roll := w.RNG.IntN(100)

	switch {
	case roll < 28 && len(uw.automationIDs) < 3:
		reqID := w.IDs.Next()
		editable := w.randomFullAutomationEditable(uw)
		receipt, err := uw.Scheduler.Save(ctx, "", reqID, 0, editable)
		if err == nil {
			var a scheduler.Automation
			if json.Unmarshal(receipt.Resource, &a) == nil && a.ID != "" {
				uw.automationIDs = append(uw.automationIDs, a.ID)
				w.Stats.AutomationsCreated++
			}
		} else if errors.Is(err, memstore.ErrCommitThenError) {
			if a, cerr := uw.Scheduler.Created(ctx, reqID); cerr == nil && a.ID != "" {
				uw.automationIDs = append(uw.automationIDs, a.ID)
				w.Stats.AutomationsCreated++
			}
		}
		w.Record(uw.User.ID, "sched", "auto.create", fmt.Sprintf("kind=%s missed=%s err=%v", editable.Trigger.Kind, editable.MissedRun, err))

	case roll < 42 && len(uw.automationIDs) > 0:
		id := uw.automationIDs[w.RNG.IntN(len(uw.automationIDs))]
		cur, err := uw.Scheduler.Get(ctx, id)
		if err != nil {
			w.Record(uw.User.ID, "sched", "auto.edit_read_err", fmt.Sprintf("id=%s err=%v", id, err))
			return
		}
		editable := w.randomFullAutomationEditable(uw)
		_, err = uw.Scheduler.Save(ctx, id, w.IDs.Next(), cur.Revision, editable)
		w.Record(uw.User.ID, "sched", "auto.edit", fmt.Sprintf("id=%s rev=%d err=%v", id, cur.Revision, err))

	case roll < 62 && len(uw.automationIDs) > 0:
		id := uw.automationIDs[w.RNG.IntN(len(uw.automationIDs))]
		cur, err := uw.Scheduler.Get(ctx, id)
		if err != nil {
			w.Record(uw.User.ID, "sched", "auto.act_read_err", fmt.Sprintf("id=%s err=%v", id, err))
			return
		}
		var act scheduler.Action
		switch {
		case cur.Status == "paused":
			if w.RNG.IntN(4) == 0 {
				act = scheduler.Action{Action: "cancel"}
			} else {
				act = scheduler.Action{Action: "resume"}
			}
		case cur.Status == "enabled":
			switch w.RNG.IntN(5) {
			case 0:
				act = scheduler.Action{Action: "pause"}
			case 1:
				until := w.Clock.Now().Add(time.Duration(120+w.RNG.IntN(600)) * time.Second)
				act = scheduler.Action{Action: "snooze", Until: &until}
			case 2, 3:
				act = scheduler.Action{Action: "run_now"}
			default:
				act = scheduler.Action{Action: "cancel"}
			}
		default:
			act = scheduler.Action{Action: "run_now"}
		}
		_, err = uw.Scheduler.Act(ctx, id, w.IDs.Next(), cur.Revision, act)
		w.Record(uw.User.ID, "sched", "auto.act", fmt.Sprintf("id=%s status=%s action=%s err=%v", id, cur.Status, act.Action, err))

	default:
		next, err := uw.Scheduler.Tick(ctx)
		if err == nil {
			w.Stats.SchedulerTicks++
		}
		nextStr := "<nil>"
		if next != nil {
			nextStr = next.UTC().Format(time.RFC3339)
		}
		w.Record(uw.User.ID, "sched", "tick", fmt.Sprintf("next=%s err=%v", nextStr, err))
	}
}

func (w *FullWorld) randomFullAutomationEditable(uw *FullUserWorld) scheduler.Editable {
	missed := []string{"skip", "latest", "all"}
	now := w.Clock.Now()
	var trig scheduler.Trigger
	switch w.RNG.IntN(3) {
	case 0:
		at := now.Add(time.Duration(60+w.RNG.IntN(600)) * time.Second).Truncate(time.Second)
		trig = scheduler.Trigger{Kind: "once", At: &at}
	case 1:
		loc, _ := time.LoadLocation(uw.User.TimeZone)
		localNow := now.In(loc).Truncate(time.Minute)
		trig = scheduler.Trigger{
			Kind:       "recurring",
			LocalStart: localNow.Format("2006-01-02T15:04:05"),
			Timezone:   uw.User.TimeZone,
			RRule:      "FREQ=DAILY;INTERVAL=1;BYHOUR=9,21;BYMINUTE=0",
		}
	default:
		intervals := []int64{600, 1200, 1800}
		trig = scheduler.Trigger{
			Kind: "watch",
			Source: &scheduler.ResourceRef{
				ConnectionID: "conn_1",
				Kind:         "mail",
				ProviderID:   "inbox",
			},
			PredicateRef:       "unread_important",
			MinIntervalSeconds: intervals[w.RNG.IntN(len(intervals))],
		}
	}

	ed := scheduler.Editable{
		Title:                  fmt.Sprintf("E2E Auto %d", w.step),
		Instruction:            fmt.Sprintf("Check state for e2e step %d", w.step),
		Trigger:                trig,
		BudgetUSD:              0.45,
		MaxActiveRuns:          1,
		MissedRun:              missed[w.RNG.IntN(len(missed))],
		ExecutionWindowSeconds: []int64{0, 300, 1800}[w.RNG.IntN(3)],
		DestinationIDs:         []string{scheduler.InAppDestination},
		MaxOccurrences:         2 + w.RNG.IntN(3),
	}
	if w.RNG.IntN(2) == 0 {
		ed.RequestedAuthority = &scheduler.RequestedAuthority{
			ActionClasses: []string{"read", "reversible_write"},
			ToolIDs:       []string{permissions.Wildcard},
		}
	}
	return ed
}

func (w *FullWorld) stepFullMemory(ctx context.Context, uw *FullUserWorld) {
	formatRes := func(res state.MemoryMutationResult) (json.RawMessage, error) {
		return json.Marshal(res)
	}
	roll := w.RNG.IntN(100)

	switch {
	case roll < 28:
		sections := []string{"Preferences", "People", "Projects", "Context"}
		section := sections[w.RNG.IntN(len(sections))]
		text := fmt.Sprintf("e2e memory step=%d topic=project_omega_%d", w.step, w.RNG.IntN(24))
		if w.RNG.IntN(3) == 0 {
			text += " " + strings.Repeat("detail ", 120)
			if len(text) > state.MaxMemoryText {
				text = text[:state.MaxMemoryText]
			}
		}
		var pinned *bool
		if w.RNG.IntN(5) == 0 {
			p := true
			pinned = &p
		}
		reqID := w.IDs.Next()
		_, err := uw.Store.MutateMemory(ctx, state.MemoryMutationRequest{
			RequestID:      reqID,
			Kind:           state.MemoryMutateCreateCore,
			Text:           text,
			About:          "the user",
			Section:        section,
			Pinned:         pinned,
			FormatResource: formatRes,
		})
		if err == nil {
			w.Stats.MemoryMutations++
			if w.RNG.IntN(4) == 0 {
				_, _ = uw.Store.MutateMemory(ctx, state.MemoryMutationRequest{
					RequestID:      reqID,
					Kind:           state.MemoryMutateCreateCore,
					Text:           text,
					About:          "the user",
					Section:        section,
					Pinned:         pinned,
					FormatResource: formatRes,
				})
			}
		}
		w.Record(uw.User.ID, "memory", "core.create", fmt.Sprintf("section=%s pinned=%v err=%v", section, pinned != nil && *pinned, err))

	case roll < 60:
		mems, err := uw.Store.Memories(ctx)
		if err != nil || len(mems) == 0 {
			w.Record(uw.User.ID, "memory", "core.list_empty", fmt.Sprintf("n=%d err=%v", len(mems), err))
			return
		}
		target := mems[w.RNG.IntN(len(mems))]
		rev := target.UpdatedAt.UnixMilli()
		if w.RNG.IntN(12) == 0 {
			rev++
		}
		sub := w.RNG.IntN(5)
		req := state.MemoryMutationRequest{
			RequestID:        w.IDs.Next(),
			ExpectedRevision: rev,
			TargetID:         target.ID,
			FormatResource:   formatRes,
		}
		action := ""
		switch sub {
		case 0:
			action = "edit"
			req.Kind = state.MemoryMutateEditCore
			req.Text = fmt.Sprintf("edited memory step=%d id=%s topic=project_omega", w.step, target.ID[:min(8, len(target.ID))])
			req.About = "the user"
			req.Section = target.Section
		case 1:
			if target.Pinned {
				action = "unpin"
				req.Kind = state.MemoryMutateUnpinCore
			} else {
				action = "pin"
				req.Kind = state.MemoryMutatePinCore
			}
		case 2:
			action = "unpin"
			req.Kind = state.MemoryMutateUnpinCore
		case 3:
			action = "demote"
			req.Kind = state.MemoryMutateDemoteCore
		default:
			action = "forget"
			req.Kind = state.MemoryMutateForgetCore
		}
		_, merr := uw.Store.MutateMemory(ctx, req)
		if merr == nil {
			w.Stats.MemoryMutations++
		}
		w.Record(uw.User.ID, "memory", "core."+action, fmt.Sprintf("id=%s err=%v", target.ID, merr))

	case roll < 72:
		logs, err := uw.Store.MemoryLogs(ctx, "", 5)
		if err != nil || len(logs) == 0 {
			w.Record(uw.User.ID, "memory", "log.empty", fmt.Sprintf("err=%v", err))
			return
		}
		logDoc := logs[w.RNG.IntN(len(logs))]
		if len(logDoc.Entries) == 0 {
			return
		}
		entry := logDoc.Entries[w.RNG.IntN(len(logDoc.Entries))]
		req := state.MemoryMutationRequest{
			RequestID:        w.IDs.Next(),
			ExpectedRevision: logDoc.UpdatedAt.UnixMilli(),
			Date:             logDoc.Date,
			TargetID:         entry.ID,
			FormatResource:   formatRes,
		}
		action := "promote"
		if w.RNG.IntN(3) == 0 {
			action = "forget"
			req.Kind = state.MemoryMutateForgetLog
		} else {
			req.Kind = state.MemoryMutatePromoteLog
			if entry.Provenance == state.ProvenanceTaskOutcome && w.RNG.IntN(2) == 0 {
				req.Text = fmt.Sprintf("User-confirmed takeaway step=%d from %s", w.step, entry.ID[:min(8, len(entry.ID))])
			}
		}
		_, merr := uw.Store.MutateMemory(ctx, req)
		if merr == nil {
			w.Stats.MemoryMutations++
		}
		w.Record(uw.User.ID, "memory", "log."+action, fmt.Sprintf("date=%s entry=%s err=%v", logDoc.Date, entry.ID, merr))

	case roll < 82:
		if w.RNG.IntN(2) == 0 {
			sum, err := uw.Store.ConversationSummary(ctx)
			if err == nil && len(sum.Items) > 0 {
				item := sum.Items[w.RNG.IntN(len(sum.Items))]
				itemID := state.SummaryItemID(item)
				kind := state.MemoryMutatePromoteWorking
				action := "promote"
				if w.RNG.IntN(2) == 0 {
					kind = state.MemoryMutateDismissWorking
					action = "dismiss"
				}
				_, merr := uw.Store.MutateMemory(ctx, state.MemoryMutationRequest{
					RequestID:        w.IDs.Next(),
					ExpectedRevision: sum.UpdatedAt.UnixMilli(),
					Kind:             kind,
					TargetID:         itemID,
					FormatResource:   formatRes,
				})
				if merr == nil {
					w.Stats.MemoryMutations++
				}
				w.Record(uw.User.ID, "memory", "working."+action, fmt.Sprintf("item=%s err=%v", itemID, merr))
				return
			}
		}
		from, _ := uw.Store.ContextFrom(ctx)
		hits, serr := uw.Store.SearchMemory(ctx, docstore.MemorySearchQuery{
			Query:        "project_omega",
			CreatedAfter: from,
			Limit:        8,
			Now:          w.Clock.Now().UTC(),
		})
		w.Record(uw.User.ID, "memory", "search", fmt.Sprintf("hits=%d err=%v", len(hits), serr))

	default:
		w.Stats.ConsolidateCalls++
		_, cerr := uw.Outer.Workers[0].ConsolidateNow(ctx)
		w.Record(uw.User.ID, "memory", "consolidate", fmt.Sprintf("err=%v", cerr))
	}
}

func (w *FullWorld) stepFullReconcileAndWakes(ctx context.Context, uw *FullUserWorld) {
	roll := w.RNG.IntN(100)

	switch {
	case roll < 45:
		w.Stats.RepairPasses++
		err := w.repairFullUser(ctx, uw)
		w.Record(uw.User.ID, "repair", "leases", fmt.Sprintf("err=%v", err))

	case roll < 72:
		owner := w.nextWorkerOwner("e2e-conv-lane")
		claim, ok, err := w.Backend.ClaimUserWake(ctx, docstore.LaneConversation, owner, state.ConversationLease)
		if err != nil || !ok {
			w.Record(uw.User.ID, "wake", "claim_conv", fmt.Sprintf("ok=%v err=%v", ok, err))
			return
		}
		w.Stats.UserWakesClaimed++
		target := w.User(claim.UserID)
		if target == nil {
			_, _ = w.Backend.ParkUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, claim.WakeSeq)
			return
		}
		procErr := w.processOuterWorker(ctx, target, 0)
		if procErr != nil {
			_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, claim.WakeSeq, w.Clock.Now().Add(2*time.Second))
			w.Record(claim.UserID, "wake", "conv_retry", fmt.Sprintf("err=%v", procErr))
			return
		}
		conv, _, statusErr := target.Store.ConversationStatus(ctx)
		if statusErr == nil && !conv.Pending() && conv.Active == "" {
			wakeAt, wakeErr := target.Outer.Workers[0].NextConversationWake(ctx)
			if wakeErr != nil {
				_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, claim.WakeSeq, w.Clock.Now().Add(2*time.Second))
				w.Record(claim.UserID, "wake", "conv_next_wake_err", fmt.Sprintf("err=%v", wakeErr))
				return
			}
			if !wakeAt.IsZero() {
				w.Stats.DelayedConvWakes++
				_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, claim.WakeSeq, wakeAt)
				w.Record(claim.UserID, "wake", "conv_rearm_delayed", fmt.Sprintf("wakeAt=%s", wakeAt.UTC().Format(time.RFC3339)))
				return
			}
			parked, pErr := w.Backend.ParkUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, claim.WakeSeq)
			if pErr != nil || !parked {
				_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, 0, time.Time{})
			}
			w.Record(claim.UserID, "wake", "conv_park", fmt.Sprintf("parked=%v err=%v", parked, pErr))
			return
		}
		_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneConversation, owner, claim.WakeSeq, w.Clock.Now().Add(2*time.Second))
		w.Record(claim.UserID, "wake", "conv_release", fmt.Sprintf("err=%v", statusErr))

	default:
		owner := w.nextWorkerOwner("e2e-sched-lane")
		claim, ok, err := w.Backend.ClaimUserWake(ctx, docstore.LaneScheduler, owner, state.ConversationLease)
		if err != nil || !ok {
			w.Record(uw.User.ID, "wake", "claim_sched", fmt.Sprintf("ok=%v err=%v", ok, err))
			return
		}
		w.Stats.UserWakesClaimed++
		target := w.User(claim.UserID)
		if target == nil {
			_, _ = w.Backend.ParkUserWake(ctx, claim.UserID, docstore.LaneScheduler, owner, claim.WakeSeq)
			return
		}
		_, tickErr := target.Scheduler.Tick(ctx)
		if tickErr == nil {
			w.Stats.SchedulerTicks++
		}
		_ = w.deliverUndeliveredAnswers(ctx, target)
		if tickErr != nil {
			_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneScheduler, owner, claim.WakeSeq, w.Clock.Now().Add(2*time.Second))
			w.Record(claim.UserID, "wake", "sched_retry", fmt.Sprintf("err=%v", tickErr))
			return
		}
		nextDue := target.Scheduler.NextScheduled(ctx)
		if nextDue == nil {
			parked, pErr := w.Backend.ParkUserWake(ctx, claim.UserID, docstore.LaneScheduler, owner, claim.WakeSeq)
			if pErr != nil || !parked {
				_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneScheduler, owner, 0, time.Time{})
			}
			w.Record(claim.UserID, "wake", "sched_park", fmt.Sprintf("parked=%v err=%v", parked, pErr))
			return
		}
		_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, docstore.LaneScheduler, owner, claim.WakeSeq, *nextDue)
		w.Record(claim.UserID, "wake", "sched_rearm", fmt.Sprintf("next=%s", nextDue.UTC().Format(time.RFC3339)))
	}
}

func (w *FullWorld) repairFullUser(ctx context.Context, uw *FullUserWorld) error {
	var failures []error
	if err := w.Inner.RepairUser(ctx, uw.User.ID); err != nil {
		failures = append(failures, err)
	}
	wakesByLane := map[string]docstore.UserWake{}
	if wakes, err := w.Backend.ListUserWakes(ctx, uw.User.ID); err != nil {
		failures = append(failures, err)
	} else {
		for _, wk := range wakes {
			wakesByLane[wk.Lane] = wk
		}
	}
	if conv, _, err := uw.Store.ConversationStatus(ctx); err == nil {
		wk, ok := wakesByLane[docstore.LaneConversation]
		if conv.Pending() || conv.Active != "" {
			if !ok || !wk.Runnable {
				if err := w.Backend.WakeUser(ctx, uw.User.ID, docstore.LaneConversation, time.Time{}); err != nil {
					failures = append(failures, err)
				}
			}
		} else if wakeAt, werr := uw.Outer.Workers[0].NextConversationWake(ctx); werr == nil && !wakeAt.IsZero() {
			if !ok || !wk.Runnable || wk.WakeAt.After(wakeAt) {
				if err := w.Backend.WakeUser(ctx, uw.User.ID, docstore.LaneConversation, wakeAt); err != nil {
					failures = append(failures, err)
				}
			}
		}
	}
	if nextDue := uw.Scheduler.NextWake(ctx); nextDue != nil {
		wk, ok := wakesByLane[docstore.LaneScheduler]
		if !ok || !wk.Runnable || wk.WakeAt.After(*nextDue) {
			if err := w.Backend.WakeUser(ctx, uw.User.ID, docstore.LaneScheduler, *nextDue); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (w *FullWorld) stepFullTimeAndPause(ctx context.Context) {
	paused, err := w.Paused(ctx)
	if err == nil {
		toggle := (paused && w.RNG.IntN(100) < 75) || (!paused && w.RNG.IntN(100) < 20)
		if toggle {
			next := !paused
			if err := w.SetPaused(ctx, next); err != nil {
				w.Record("", "control", "pause_set_err", err.Error())
				return
			}
			w.Stats.PauseToggles++
			if !next && w.RNG.IntN(2) == 0 {
				uw := w.pickUser()
				_ = w.repairFullUser(ctx, uw)
			}
			w.Record("", "control", "set_paused", fmt.Sprintf("paused=%v", next))
			return
		}
	}

	durations := []time.Duration{
		200 * time.Millisecond,
		2 * time.Second,
		15 * time.Second,
		45 * time.Second,
		state.IdleExtractDelay + 5*time.Second,
		11 * time.Minute,
		state.IdleDreamDelay + 5*time.Second,
		31 * time.Minute,
		2 * time.Hour,
		state.DreamMinInterval + 5*time.Minute,
		25 * time.Hour,
	}
	d := durations[w.RNG.IntN(len(durations))]
	now := w.Clock.Advance(d)
	w.Record("", "clock", "advance", fmt.Sprintf("delta=%s now=%s", d, now.Format(time.RFC3339Nano)))
}

func (w *FullWorld) DrainToQuiescence(ctx context.Context) error {
	w.SetFaultsEnabled(false)
	w.clearOuterHooks()

	if err := w.SetPaused(ctx, false); err != nil {
		return fmt.Errorf("unpause for drain: %w", err)
	}
	w.Record("", "drain", "start", "faults=false paused=false")

	if _, err := w.advancePastFullLeases(ctx); err != nil {
		return err
	}

	for round := 1; round <= fullDrainMaxRounds; round++ {
		for _, uw := range w.Users {
			if err := uw.Outer.DrainConversation(ctx); err != nil {
				return fmt.Errorf("drain round %d user %s outer: %w", round, uw.User.ID, err)
			}

			if err := w.deliverUndeliveredAnswers(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s deliverAnswers: %w", round, uw.User.ID, err)
			}

			if err := w.drainUserInteractions(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s interactions: %w", round, uw.User.ID, err)
			}

			if err := w.drainUserOpenQuestions(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s openQuestions: %w", round, uw.User.ID, err)
			}

			if err := w.drainUserScheduler(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s scheduler: %w", round, uw.User.ID, err)
			}

			if err := w.repairFullUser(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s repair: %w", round, uw.User.ID, err)
			}

			if err := w.drainUserInnerTasks(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s inner: %w", round, uw.User.ID, err)
			}

			if err := uw.Outer.DrainConversation(ctx); err != nil {
				return fmt.Errorf("drain round %d user %s post-inner outer: %w", round, uw.User.ID, err)
			}

			if err := w.repairFullUser(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s final repair: %w", round, uw.User.ID, err)
			}
			if err := w.drainUserScheduler(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s final scheduler: %w", round, uw.User.ID, err)
			}
			if err := w.drainFullUserWakes(ctx, uw); err != nil {
				return fmt.Errorf("drain round %d user %s user wakes: %w", round, uw.User.ID, err)
			}
		}

		quiescent, err := w.isQuiescent(ctx)
		if err != nil {
			return fmt.Errorf("drain round %d quiescent check: %w", round, err)
		}
		if quiescent {
			w.Record("", "drain", "converged", fmt.Sprintf("rounds=%d", round))
			break
		}
		if nextWake, ok := w.Inner.NextWakeTime(ctx); ok {
			delta := nextWake.Sub(w.Clock.Now()) + time.Millisecond
			w.Clock.Advance(delta)
			w.Record("", "drain", "advance_next_wake", fmt.Sprintf("delta=%s", delta))
		} else if advanced, err := w.advancePastFullLeases(ctx); err != nil {
			return err
		} else if !advanced {
			if nextConvWake, ok := w.nextConversationMaintenanceWake(ctx); ok {
				delta := time.Second
				if nextConvWake.After(w.Clock.Now()) {
					delta = nextConvWake.Sub(w.Clock.Now()) + time.Second
				}
				w.Clock.Advance(delta)
				w.Record("", "drain", "advance_conv_wake", fmt.Sprintf("delta=%s", delta))
			} else if w.hasUnflushedSummaries(ctx) {
				delta := 10*time.Minute + time.Second
				w.Clock.Advance(delta)
				w.Record("", "drain", "advance_idle_flush", fmt.Sprintf("delta=%s", delta))
			}
		}
	}

	if err := w.CheckAllSafety(ctx); err != nil {
		return fmt.Errorf("post-drain safety: %w", err)
	}
	if err := w.Checker.CheckQuiescence(ctx, w.UserTargets(), w.Clock.Now()); err != nil {
		return err
	}

	for _, uw := range w.Users {
		conv, _, err := uw.Store.ConversationStatus(ctx)
		if err != nil {
			return fmt.Errorf("post-drain ConversationStatus [user=%s]: %w", uw.User.ID, err)
		}
		routingPending, err := uw.Store.UserRoutingPending(ctx)
		if err != nil {
			return fmt.Errorf("post-drain UserRoutingPending [user=%s]: %w", uw.User.ID, err)
		}
		if conv.Pending() || conv.Active != "" || routingPending {
			return fmt.Errorf("post-drain conversation not idle [user=%s]: pending=%v active=%q routingPending=%v",
				uw.User.ID, conv.Pending(), conv.Active, routingPending)
		}
		sum, err := uw.Store.ConversationSummary(ctx)
		if err != nil {
			return fmt.Errorf("post-drain ConversationSummary [user=%s]: %w", uw.User.ID, err)
		}
		if sum.Applies(conv.ContextFrom) && !sum.Through.IsZero() && !sum.EffectiveFlushedThrough().Equal(sum.Through) {
			return fmt.Errorf("post-drain summary not flushed [user=%s]: flushed=%s through=%s",
				uw.User.ID, sum.EffectiveFlushedThrough().UTC().Format(time.RFC3339Nano), sum.Through.UTC().Format(time.RFC3339Nano))
		}
		turns, err := uw.Store.ConversationTurnsAfter(ctx, conv.ContextFrom)
		if err != nil {
			return fmt.Errorf("post-drain ConversationTurnsAfter [user=%s]: %w", uw.User.ID, err)
		}
		for _, t := range turns {
			if t.Status == "completed" {
				if !sum.From.Equal(conv.ContextFrom) || !sum.EffectiveExtractedThrough().Equal(t.StartedAt) {
					return fmt.Errorf("post-drain summary not extracted [user=%s]: from=%s context_from=%s extracted=%s newest_completed=%s",
						uw.User.ID,
						sum.From.UTC().Format(time.RFC3339Nano),
						conv.ContextFrom.UTC().Format(time.RFC3339Nano),
						sum.EffectiveExtractedThrough().UTC().Format(time.RFC3339Nano),
						t.StartedAt.UTC().Format(time.RFC3339Nano),
					)
				}
				break
			}
		}
		wakes, err := w.Backend.ListUserWakes(ctx, uw.User.ID)
		if err != nil {
			return fmt.Errorf("post-drain ListUserWakes [user=%s]: %w", uw.User.ID, err)
		}
		for _, wk := range wakes {
			if wk.Lane == docstore.LaneConversation && (wk.Runnable || wk.Owner != "" || wk.LeaseUntil != nil) {
				return fmt.Errorf("post-drain LaneConversation wake not parked [user=%s]: runnable=%v owner=%q wakeAt=%s",
					uw.User.ID, wk.Runnable, wk.Owner, wk.WakeAt.UTC().Format(time.RFC3339Nano))
			}
		}
	}
	return nil
}

func (w *FullWorld) advancePastFullLeases(ctx context.Context) (bool, error) {
	now := w.Clock.Now()
	maxLease := now
	consider := func(t time.Time) {
		if t.After(maxLease) {
			maxLease = t
		}
	}
	snap := w.Backend.Snapshot()
	for _, uw := range w.Users {
		conv, _, err := uw.Store.ConversationStatus(ctx)
		if err != nil {
			return false, fmt.Errorf("drain read conversation status for %s: %w", uw.User.ID, err)
		}
		if conv.Owner != "" {
			consider(conv.LeaseUntil)
		}
		for _, fields := range snap.Collections["owners/"+uw.User.DataOwner()+"/task_controls"] {
			var tc state.TaskControl
			if docstore.Decode(fields, &tc) == nil && tc.Owner != "" {
				consider(tc.LeaseUntil)
			}
		}
	}
	if leases, err := w.Backend.ListTaskLeases(ctx, ""); err == nil {
		for _, l := range leases {
			if l.LeaseUntil != nil {
				consider(*l.LeaseUntil)
			}
			if l.Runnable {
				consider(l.WakeAt)
			}
		}
	}
	if wakes, err := w.Backend.ListUserWakes(ctx, ""); err == nil {
		for _, wk := range wakes {
			if wk.LeaseUntil != nil {
				consider(*wk.LeaseUntil)
			}
		}
	}
	if maxLease.After(now) {
		delta := maxLease.Sub(now) + time.Second
		w.Clock.Advance(delta)
		w.Record("", "drain", "advance_past_lease", fmt.Sprintf("delta=%s", delta))
		return true, nil
	}
	return false, nil
}

func (w *FullWorld) drainFullUserWakes(ctx context.Context, uw *FullUserWorld) error {
	now := w.Clock.Now()
	wakes, err := w.Backend.ListUserWakes(ctx, uw.User.ID)
	if err != nil {
		return err
	}
	for _, wk := range wakes {
		if !wk.Runnable && wk.Owner == "" {
			continue
		}
		if wk.Owner == "" && wk.WakeAt.After(now) {
			continue
		}
		if wk.LeaseUntil != nil && wk.LeaseUntil.After(now) {
			w.Clock.Advance(wk.LeaseUntil.Sub(now) + time.Second)
			now = w.Clock.Now()
		}
		owner := w.nextWorkerOwner("e2e-drain-wake")
		for range len(w.Users) + 4 {
			claim, ok, err := w.Backend.ClaimUserWake(ctx, wk.Lane, owner, state.ConversationLease)
			if err != nil {
				return err
			}
			if !ok {
				break
			}
			target := w.User(claim.UserID)
			if target == nil {
				_, _ = w.Backend.ParkUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq)
				continue
			}
			switch wk.Lane {
			case docstore.LaneConversation:
				conv, _, err := target.Store.ConversationStatus(ctx)
				if err != nil {
					return err
				}
				if !conv.Pending() && conv.Active == "" {
					_ = target.Outer.ProcessWorker(ctx, 0)
					wakeAt, werr := target.Outer.Workers[0].NextConversationWake(ctx)
					if werr == nil && !wakeAt.IsZero() {
						if !wakeAt.After(w.Clock.Now()) {
							wakeAt = w.Clock.Now().Add(time.Second)
						}
						_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq, wakeAt)
					} else {
						_, _ = w.Backend.ParkUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq)
					}
				} else {
					_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq, now)
				}
			case docstore.LaneScheduler:
				nextDue := target.Scheduler.NextWake(ctx)
				if nextDue == nil {
					_, _ = w.Backend.ParkUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq)
				} else {
					_ = w.Backend.ReleaseUserWake(ctx, claim.UserID, wk.Lane, owner, claim.WakeSeq, *nextDue)
				}
			}
		}
	}
	return nil
}

func (w *FullWorld) drainUserInteractions(ctx context.Context, uw *FullUserWorld) error {
	pending, _, err := uw.Permissions.Interactions(ctx, "pending", "", "", 50)
	if err != nil {
		return err
	}
	for _, card := range pending {
		var resp permissions.Response
		if card.Kind == "approval" {
			resp = permissions.Response{Kind: "approve", ActionDigest: card.ActionDigest}
		} else {
			resp = permissions.Response{Kind: "answer", Text: "Drain answer"}
		}
		_, ans, err := uw.Permissions.Respond(ctx, card.ID, w.IDs.Next(), card.Revision, resp)
		if err != nil {
			if errors.Is(err, permissions.ErrInteractionStale) || errors.Is(err, state.ErrRevisionConflict) {
				continue
			}
			return err
		}
		if ans != nil {
			w.Stats.DrainInteractions++
			w.deliverSingleAnswer(ctx, uw, card.ID, *ans)
		}
	}
	return nil
}

func (w *FullWorld) drainUserOpenQuestions(ctx context.Context, uw *FullUserWorld) error {
	open, err := uw.Store.OpenTasks(ctx)
	if err != nil {
		return err
	}
	for _, r := range open {
		if r.Status != state.RunRunning || r.CancelRequested || r.Finishing != nil {
			continue
		}
		if r.Question == nil || r.Question.ResolvedBy != "" || r.Question.Revision != r.Revision {
			continue
		}
		cmd := state.Command{
			ID:         w.IDs.Next(),
			RunID:      r.ID,
			Kind:       state.CommandAnswer,
			QuestionID: r.Question.ID,
			Text:       "Drain resolved open question",
		}
		receipts, err := uw.Store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
		if err != nil {
			if errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy) {
				continue
			}
			return err
		}
		_ = w.Checker.RecordCommandReceipts(uw.User.ID, receipts)
		w.WakeHub.NotifyTask(uw.User.ID, r.ID)
	}
	return nil
}

func (w *FullWorld) drainUserScheduler(ctx context.Context, uw *FullUserWorld) error {
	for range fullDrainMaxTicks {
		if _, err := uw.Scheduler.Tick(ctx); err != nil {
			return err
		}
		due, err := w.hasDueAutomations(ctx, uw)
		if err != nil {
			return err
		}
		if !due {
			return nil
		}
	}
	return nil
}

func (w *FullWorld) hasDueAutomations(ctx context.Context, uw *FullUserWorld) (bool, error) {
	now := w.Clock.Now()
	autos, err := uw.Scheduler.List(ctx, "enabled")
	if err != nil {
		return false, err
	}
	for _, a := range autos {
		if a.NextAt != nil && !a.NextAt.After(now) {
			return true, nil
		}
	}
	return false, nil
}

func (w *FullWorld) drainUserInnerTasks(ctx context.Context, uw *FullUserWorld) error {
	for range fullDrainMaxStepPerRd {
		open, err := uw.Store.OpenTasks(ctx)
		if err != nil {
			return err
		}
		var orderedIDs []string
		for _, r := range open {
			if r.ParentID != "" {
				orderedIDs = append(orderedIDs, r.ID)
			}
		}
		for _, r := range open {
			if r.ParentID == "" {
				orderedIDs = append(orderedIDs, r.ID)
			}
		}
		if leases, lerr := w.Backend.ListTaskLeases(ctx, uw.User.ID); lerr == nil {
			for _, l := range leases {
				if !slices.Contains(orderedIDs, l.RunID) {
					orderedIDs = append(orderedIDs, l.RunID)
				}
			}
		}

		progressedAny := false
		for _, runID := range orderedIDs {
			progressed, stepErr := w.Inner.StepUserRun(ctx, uw.User.ID, runID)
			if stepErr != nil {
				return fmt.Errorf("StepUserRun(%s): %w", runID, stepErr)
			}
			if progressed {
				w.Stats.InnerSteps++
				progressedAny = true
			}
		}
		if !progressedAny {
			return nil
		}
	}
	return nil
}

func (w *FullWorld) hasUnflushedSummaries(ctx context.Context) bool {
	for _, uw := range w.Users {
		sum, err := uw.Store.ConversationSummary(ctx)
		if err != nil {
			continue
		}
		from, err := uw.Store.ContextFrom(ctx)
		if err != nil {
			continue
		}
		if sum.Applies(from) && sum.EffectiveFlushedThrough().Before(sum.Through) {
			return true
		}
	}
	return false
}

func (w *FullWorld) nextConversationMaintenanceWake(ctx context.Context) (time.Time, bool) {
	var earliest time.Time
	hasAny := false
	consider := func(t time.Time) {
		if !hasAny || t.Before(earliest) {
			earliest = t
			hasAny = true
		}
	}
	for _, uw := range w.Users {
		if wakeAt, err := uw.Outer.Workers[0].NextConversationWake(ctx); err == nil && !wakeAt.IsZero() {
			consider(wakeAt)
		}
		if wakes, err := w.Backend.ListUserWakes(ctx, uw.User.ID); err == nil {
			for _, wk := range wakes {
				if wk.Lane == docstore.LaneConversation && wk.Runnable {
					consider(wk.WakeAt)
				}
			}
		}
	}
	return earliest, hasAny
}

func (w *FullWorld) isQuiescent(ctx context.Context) (bool, error) {
	if w.hasUnflushedSummaries(ctx) {
		return false, nil
	}
	if _, pendingConvWake := w.nextConversationMaintenanceWake(ctx); pendingConvWake {
		return false, nil
	}
	for _, uw := range w.Users {
		page, err := uw.Store.Pending(ctx, "")
		if err != nil {
			return false, err
		}
		if len(page.Commands) > 0 {
			return false, nil
		}
		open, err := uw.Store.OpenTasks(ctx)
		if err != nil {
			return false, err
		}
		if len(open) > 0 {
			return false, nil
		}
		cleanup, err := uw.Store.CleanupTasks(ctx)
		if err != nil {
			return false, err
		}
		if len(cleanup) > 0 {
			return false, nil
		}
		pubs, err := uw.Store.PublicationTasks(ctx)
		if err != nil {
			return false, err
		}
		if len(pubs) > 0 {
			return false, nil
		}
		conv, _, err := uw.Store.ConversationStatus(ctx)
		if err != nil {
			return false, err
		}
		if conv.Pending() || conv.Active != "" {
			return false, nil
		}
		routingPending, err := uw.Store.UserRoutingPending(ctx)
		if err != nil {
			return false, err
		}
		if routingPending {
			return false, nil
		}
		wv, err := uw.Store.WorkVersion(ctx, true)
		if err != nil {
			return false, err
		}
		if wv != "" {
			return false, nil
		}
		due, err := w.hasDueAutomations(ctx, uw)
		if err != nil {
			return false, err
		}
		if due {
			return false, nil
		}
		for _, status := range []string{"pending", "accepted", "running"} {
			occPage, _, err := uw.Store.Resources(ctx, scheduler.CollectionOccurrences, map[string]string{"status": status}, "", 10)
			if err != nil {
				return false, err
			}
			if len(occPage) > 0 {
				return false, nil
			}
		}
		leases, err := w.Backend.ListTaskLeases(ctx, uw.User.ID)
		if err != nil {
			return false, err
		}
		if len(leases) > 0 {
			return false, nil
		}
		wakes, err := w.Backend.ListUserWakes(ctx, uw.User.ID)
		if err != nil {
			return false, err
		}
		now := w.Clock.Now()
		for _, wk := range wakes {
			if wk.Owner != "" || wk.LeaseUntil != nil {
				return false, nil
			}
			if wk.Lane == docstore.LaneConversation && wk.Runnable {
				return false, nil
			}
			if wk.Runnable && !wk.WakeAt.After(now) {
				return false, nil
			}
		}
	}
	return true, nil
}
