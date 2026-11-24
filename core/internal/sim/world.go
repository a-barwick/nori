package sim

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/env"
	"nori/internal/sim/invariants"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

var simTimeZones = []string{
	"UTC",
	"America/Los_Angeles",
	"America/New_York",
}

// WorldConfig configures a deterministic Stage 2 Coordination World simulation.
type WorldConfig struct {
	// NumUsers controls how many isolated users share the backend. When <= 0,
	// NewWorld deterministically chooses 2 or 3 users from the seed.
	NumUsers int

	// UnattendedWrites configures permissions.Service.UnattendedWrites
	// ("confirm_writes" or "require_rule"). When empty, chosen from the seed.
	UnattendedWrites string

	// UserSends configures permissions.Service.UserSends ("ask" or "allow").
	// When empty, chosen from the seed.
	UserSends string

	// TaskBudgetUSD sets the default per-task budget. Defaults to 1.0 when <= 0.
	TaskBudgetUSD float64

	// StartTime initializes the virtual clock. Defaults to env.DefaultBaseTime.
	StartTime time.Time

	// TraceCapacity bounds retained trace entries (ring buffer). Defaults to 2048.
	TraceCapacity int

	// DisableFaults disables all default fault injection when true.
	DisableFaults bool

	// StoreFaults overrides the default chaos fault profile when non-zero
	// (unless DisableFaults is true).
	StoreFaults memstore.FaultConfig
}

// DefaultStoreFaults returns the default adversarial memstore fault profile.
func DefaultStoreFaults() memstore.FaultConfig {
	return memstore.FaultConfig{
		GetErrProb:        0.025,
		QueryErrProb:      0.025,
		ApplyErrProb:      0.03,
		ArtifactErrProb:   0.03,
		TxConflictProb:    0.06,
		CommitThenErrProb: 0.03,
	}
}

// WorldStats summarizes workload coverage observed during a Stage 2 coordination run.
type WorldStats struct {
	WorkerSteps      int
	WorkerCrashes    int
	RepairPasses     int
	UserWakesClaimed int
	PauseToggles     int
}

type trackedOp struct {
	runID     string
	id        string
	callID    string
	name      string
	effect    string
	revision  int64
	attempt   int
	digest    string
	outcome   string
	resultRef string
	settled   bool
}

type trackedInteraction struct {
	id            string
	taskID        string
	kind          string
	actionDigest  string
	lastRequestID string
	lastResponse  permissions.Response
	hasResponse   bool
}

type trackedInput struct {
	id   string
	text string
}

// UserWorld holds all wired Nori Postgres-native coordination services for a
// single simulated user.
type UserWorld struct {
	User        users.User
	DB          *records.Database
	Artifacts   *memstore.Artifacts
	Store       state.ConversationStore
	Permissions *permissions.Service
	Scheduler   *scheduler.Service

	// Workload bookkeeping (strictly slices/maps accessed in sorted order).
	submittedCmds     []state.Command
	ops               []*trackedOp
	historyHead       map[string]string
	interactions      []*trackedInteraction
	ruleIDs           []string
	automationIDs     []string
	goalIDs           []string
	inputs            []trackedInput
	assistantMsgIDs   []string
	activeConvFence   *state.ConversationFence
	activeConvTurn    *state.ConversationTurn
	nextOpSeq         int
	nextActionCounter int
}

// World is a self-contained, single-seed deterministic coordination simulator
// running Nori's real records, TaskControl fencing, Postgres-native leases/wakes,
// permissions, and scheduler packages across multiple isolated users.
type World struct {
	Seed   int64
	Config WorldConfig
	RNG    *rand.Rand
	Clock  *env.Clock
	IDs    *env.IDGen
	Trace  *env.Trace

	Faults   *memstore.FaultInjector
	Backend  *memstore.Backend
	UsersDir *records.Users
	Checker  *invariants.Checker

	Users     []*UserWorld
	usersByID map[string]*UserWorld
	step      int
	workerSeq int64
	Stats     WorldStats
}

// NewWorld constructs a fully wired deterministic Coordination World from seed.
func NewWorld(seed int64, cfg WorldConfig) *World {
	rootRNG := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))
	deriveRNG := func() *rand.Rand {
		return rand.New(rand.NewPCG(rootRNG.Uint64(), rootRNG.Uint64()))
	}

	workloadRNG := deriveRNG()
	idGen := env.NewIDGen(deriveRNG())
	storeFaultRNG := deriveRNG()

	if cfg.NumUsers <= 0 {
		cfg.NumUsers = 2 + workloadRNG.IntN(2) // 2 or 3 users
	}
	if cfg.TaskBudgetUSD <= 0 {
		cfg.TaskBudgetUSD = 1.0
	}
	if cfg.TraceCapacity <= 0 {
		cfg.TraceCapacity = 2048
	}
	if cfg.UnattendedWrites == "" {
		if workloadRNG.IntN(2) == 0 {
			cfg.UnattendedWrites = "confirm_writes"
		} else {
			cfg.UnattendedWrites = "require_rule"
		}
	}
	if cfg.UserSends == "" {
		if workloadRNG.IntN(2) == 0 {
			cfg.UserSends = "ask"
		} else {
			cfg.UserSends = "allow"
		}
	}

	storeFaults := cfg.StoreFaults
	if !cfg.DisableFaults && storeFaults == (memstore.FaultConfig{}) {
		storeFaults = DefaultStoreFaults()
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

	checker := invariants.New(invariants.Config{
		Backend:     backend,
		Faults:      faultInjector,
		Now:         clock.Now,
		CheckLeases: true,
	})

	w := &World{
		Seed:      seed,
		Config:    cfg,
		RNG:       workloadRNG,
		Clock:     clock,
		IDs:       idGen,
		Trace:     traceLog,
		Faults:    faultInjector,
		Backend:   backend,
		UsersDir:  usersDir,
		Checker:   checker,
		Users:     make([]*UserWorld, 0, cfg.NumUsers),
		usersByID: make(map[string]*UserWorld, cfg.NumUsers),
	}

	for i := range cfg.NumUsers {
		tz := simTimeZones[i%len(simTimeZones)]
		uid := idGen.Next()
		// Exercise LegacyID (ID="owner" != DataOwner=email) on user 0 for even seeds.
		if i == 0 && seed%2 == 0 {
			uid = users.LegacyID
		}
		u := users.User{
			ID:          uid,
			Email:       fmt.Sprintf("sim-user-%d@example.com", i+1),
			DisplayName: fmt.Sprintf("Sim User %d", i+1),
			TimeZone:    tz,
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

		uw := &UserWorld{
			User:        u,
			DB:          db,
			Artifacts:   artifacts,
			Store:       store,
			Permissions: perms,
			Scheduler:   sched,
			historyHead: make(map[string]string),
		}
		w.Users = append(w.Users, uw)
		w.usersByID[u.ID] = uw
	}

	// Keep Users slice in canonical ID-sorted order for deterministic iteration.
	slices.SortFunc(w.Users, func(a, b *UserWorld) int {
		return strings.Compare(a.User.ID, b.User.ID)
	})

	return w
}

// User looks up a UserWorld by user ID.
func (w *World) User(userID string) *UserWorld {
	return w.usersByID[userID]
}

// nextWorkerOwner produces a deterministic worker owner identity.
func (w *World) nextWorkerOwner(prefix string) string {
	w.workerSeq++
	return fmt.Sprintf("%s-%d", prefix, w.workerSeq)
}

// SetFaultsEnabled toggles fault injection across memstore.
func (w *World) SetFaultsEnabled(enabled bool) {
	w.Faults.SetEnabled(enabled)
}

// SetPaused updates system/control.paused and emits matching pause/unpause wakes fault-free.
func (w *World) SetPaused(ctx context.Context, paused bool) error {
	wasEnabled := w.Faults.Enabled()
	if wasEnabled {
		w.Faults.SetEnabled(false)
		defer w.Faults.SetEnabled(true)
	}
	return w.UsersDir.SetPaused(ctx, paused)
}

// Paused reads system/control.paused fault-free.
func (w *World) Paused(ctx context.Context) (bool, error) {
	wasEnabled := w.Faults.Enabled()
	if wasEnabled {
		w.Faults.SetEnabled(false)
		defer w.Faults.SetEnabled(true)
	}
	return w.UsersDir.Paused(ctx)
}

// UserTargets returns UserTarget handles for all simulated users in canonical order.
func (w *World) UserTargets() []invariants.UserTarget {
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

// Record appends a diagnostic event to the deterministic simulation trace.
func (w *World) Record(userID, actor, action, detail string) {
	w.Trace.Record(w.step, w.Clock.Now(), userID, actor, action, detail)
}

// DebugDump returns a fault-free diagnostic dump of open runs, pending commands,
// task leases, and user wakes for all users.
func (w *World) DebugDump(ctx context.Context) string {
	wasStore := w.Faults.Enabled()
	w.Faults.SetEnabled(false)
	defer w.Faults.SetEnabled(wasStore)

	var b strings.Builder
	paused, _ := w.Paused(ctx)
	fmt.Fprintf(&b, "=== World DebugDump seed=%d step=%d paused=%v now=%s ===\n", w.Seed, w.step, paused, w.Clock.Now().Format(time.RFC3339Nano))
	for _, uw := range w.Users {
		fmt.Fprintf(&b, "-- User %s (owner=%s) --\n", uw.User.ID, uw.User.DataOwner())
		if open, err := uw.Store.OpenTasks(ctx); err == nil {
			for _, r := range open {
				fmt.Fprintf(&b, "  open run=%s status=%s parent=%s rev=%d pos=%d inc=%d cancel=%v finishing=%v cleanup=%v subtasks=%+v\n",
					r.ID, r.Status, r.ParentID, r.Revision, r.Position, r.Incorporated, r.CancelRequested, r.Finishing != nil, r.CleanupPending, r.Subtasks)
			}
		}
		if page, err := uw.Store.Pending(ctx, ""); err == nil {
			for _, c := range page.Commands {
				fmt.Fprintf(&b, "  pending cmd=%s run=%s kind=%s pos=%d rev=%d delivered=%v\n",
					c.ID, c.RunID, c.Kind, c.Position, c.Revision, c.Delivered)
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

// Digest returns a canonical hex-encoded SHA-256 hash combining the shared
// memstore backend (including nori_task_leases and nori_user_wakes), every
// user's artifacts store, the trace log, and the virtual clock.
func (w *World) Digest() string {
	h := sha256.New()
	writeHashString(h, "sim.world.v2")
	writeHashString(h, w.Backend.Digest())
	writeHashUint64(h, uint64(len(w.Users)))
	for _, uw := range w.Users {
		writeHashString(h, uw.User.ID)
		writeHashString(h, uw.Artifacts.Digest())
	}
	writeHashString(h, w.Trace.Digest())
	writeHashInt64(h, w.Clock.Now().UTC().UnixNano())
	return hex.EncodeToString(h.Sum(nil))
}

func writeHashUint64(h interface{ Write([]byte) (int, error) }, v uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	_, _ = h.Write(buf[:])
}

func writeHashInt64(h interface{ Write([]byte) (int, error) }, v int64) {
	writeHashUint64(h, uint64(v))
}

func writeHashString(h interface{ Write([]byte) (int, error) }, s string) {
	writeHashUint64(h, uint64(len(s)))
	_, _ = h.Write([]byte(s))
}
