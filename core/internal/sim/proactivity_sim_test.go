package sim_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"nori/internal/agent"
	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/proactivity"
	"nori/internal/push"
	"nori/internal/scheduler"
	"nori/internal/sim/env"
	"nori/internal/sim/invariants"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/postgres"
	"nori/internal/state/records"
	"nori/internal/task"
	"nori/internal/transcript"
	"nori/internal/users"
)

type proactivityBackendMode string

const (
	backendMemstore proactivityBackendMode = "memstore"
	backendPostgres proactivityBackendMode = "postgres"
)

func runProactivityModes(t *testing.T, fn func(t *testing.T, mode proactivityBackendMode)) {
	t.Helper()
	t.Run("memstore", func(t *testing.T) {
		fn(t, backendMemstore)
	})
	t.Run("postgres", func(t *testing.T) {
		if strings.TrimSpace(os.Getenv("NORI_TEST_POSTGRES_URL")) == "" {
			t.Skip("NORI_TEST_POSTGRES_URL is unset; skipping Postgres proactivity DST")
		}
		fn(t, backendPostgres)
	})
}

func isolatedProactivityPostgresURL(t *testing.T, rawURL string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, rawURL)
	if err != nil {
		return rawURL
	}
	defer conn.Close(ctx)

	ident := "nori_proact_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	trimmed := strings.TrimSpace(rawURL)
	if strings.HasPrefix(trimmed, "postgres://") || strings.HasPrefix(trimmed, "postgresql://") {
		if parsed, err := neturl.Parse(trimmed); err == nil {
			if _, err := conn.Exec(ctx, "CREATE DATABASE "+ident); err == nil {
				t.Cleanup(func() {
					dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer dropCancel()
					c, err := pgx.Connect(dropCtx, rawURL)
					if err != nil {
						return
					}
					defer c.Close(dropCtx)
					_, _ = c.Exec(dropCtx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
				})
				parsed.Path = "/" + ident
				return parsed.String()
			}
		}
	}
	return rawURL
}

func boolPtr(v bool) *bool { return &v }

type proactivitySimHarness struct {
	t           *testing.T
	mode        proactivityBackendMode
	user        users.User
	clock       *env.Clock
	mem         *memstore.Backend
	backend     docstore.Backend
	artifacts   *memstore.Artifacts
	db          *records.Database
	store       state.ConversationStore
	perms       *permissions.Service
	sched       *scheduler.Service
	pushStore   push.Store
	pushSender  *push.FakeSender
	pushWorker  *push.OutboxWorker
	checker     *invariants.Checker
	pgURL       string
	mu          sync.Mutex
	prefs       connections.Preferences
	spendFn     func(ctx context.Context, since time.Time) (scheduler.ProactiveSpend, error)
	lastTurnAt  time.Time
	lastActive  time.Time
	accepted    []state.Command
	acceptedIDs map[string]bool
	outerEvents []state.TaskEvent
}

func newProactivitySimHarness(t *testing.T, mode proactivityBackendMode, seed int64, zone string, start time.Time) *proactivitySimHarness {
	t.Helper()
	silenceStdLog(t)
	ctx := context.Background()

	rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))
	ids := env.NewIDGen(rng)
	clock := env.NewClock(start)

	if zone == "" {
		zone = "UTC"
	}
	userID := ids.Next()
	if mode == backendPostgres {
		userID = uuid.NewString()
	}
	u := users.User{
		ID:          userID,
		Email:       fmt.Sprintf("proact-%s@example.com", userID[:8]),
		DisplayName: "Proactivity DST User",
		TimeZone:    zone,
	}
	if err := u.Validate(); err != nil {
		t.Fatalf("invalid user: %v", err)
	}

	var backend docstore.Backend
	var mem *memstore.Backend
	var pushStore push.Store
	var pgURL string

	switch mode {
	case backendMemstore:
		mem = memstore.New(memstore.WithClock(clock.Now))
		t.Cleanup(func() { _ = mem.Close() })
		backend = mem
		ps := push.NewMemoryStore()
		ps.Now = clock.Now
		pushStore = ps
	case backendPostgres:
		rawURL := strings.TrimSpace(os.Getenv("NORI_TEST_POSTGRES_URL"))
		if rawURL == "" {
			t.Skip("NORI_TEST_POSTGRES_URL is unset")
		}
		pgURL = isolatedProactivityPostgresURL(t, rawURL)
		pgBackend, err := postgres.OpenBackend(ctx, pgURL)
		if err != nil {
			t.Fatalf("postgres.OpenBackend: %v", err)
		}
		t.Cleanup(func() { _ = pgBackend.Close() })
		backend = pgBackend
		ps, err := push.OpenPostgresStore(ctx, pgURL, 4)
		if err != nil {
			t.Fatalf("push.OpenPostgresStore: %v", err)
		}
		t.Cleanup(ps.Close)
		pushStore = ps
	default:
		t.Fatalf("unknown backend mode: %s", mode)
	}

	usersDB := records.NewSharedUsers(backend)
	if err := usersDB.Create(ctx, u); err != nil {
		t.Fatalf("Create user: %v", err)
	}
	artifacts := memstore.NewArtifacts()
	db := records.NewShared(backend, u, 10.0)
	db.Now = clock.Now
	t.Cleanup(func() { _ = db.Close() })
	store := state.Compose(db, artifacts)

	perms := &permissions.Service{
		Store:            store,
		UnattendedWrites: "confirm_writes",
		UserSends:        "ask",
		Now:              clock.Now,
	}

	fakePush := push.NewFakeSender()
	fakePush.Now = clock.Now
	pushWorker := &push.OutboxWorker{
		Store:  pushStore,
		Sender: fakePush,
		Now:    clock.Now,
	}

	var checker *invariants.Checker
	if mem != nil {
		checker = invariants.New(invariants.Config{
			Backend: mem,
			Now:     clock.Now,
		})
	}

	h := &proactivitySimHarness{
		t:           t,
		mode:        mode,
		user:        u,
		clock:       clock,
		mem:         mem,
		backend:     backend,
		artifacts:   artifacts,
		db:          db,
		store:       store,
		perms:       perms,
		pushStore:   pushStore,
		pushSender:  fakePush,
		pushWorker:  pushWorker,
		checker:     checker,
		pgURL:       pgURL,
		acceptedIDs: map[string]bool{},
		prefs: connections.Preferences{
			Timezone: zone,
			QuietHours: &connections.QuietHours{
				Enabled: boolPtr(true),
				Start:   "21:00",
				End:     "07:30",
			},
			Proactivity: &connections.ProactivityPreferences{
				PushPolicy:              connections.PushPolicyUrgentAndApprovals,
				MorningBriefTime:        "08:00",
				MorningBriefEnabled:     boolPtr(true),
				DailyProactiveBudgetUSD: 1.0,
			},
		},
	}

	h.sched = &scheduler.Service{
		Store:       store,
		Tasks:       store,
		Permissions: perms,
		Now:         clock.Now,
		Timezone: func(context.Context) string {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.prefs.Timezone
		},
		Preferences: func(context.Context) (connections.Preferences, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.prefs, nil
		},
		AppendProactiveMessage: db.AppendProactiveMessage,
		EnqueueProactiveEvent: func(ctx context.Context, event state.TaskEvent) error {
			h.mu.Lock()
			h.outerEvents = append(h.outerEvents, event)
			h.mu.Unlock()
			return db.EnqueueProactiveTaskEvent(ctx, event)
		},
		DailyProactiveSpend: func(ctx context.Context, since time.Time) (scheduler.ProactiveSpend, error) {
			h.mu.Lock()
			fn := h.spendFn
			h.mu.Unlock()
			if fn != nil {
				return fn(ctx, since)
			}
			// Fallback to actual run spend in store.
			runs, err := store.RecentRuns(ctx, 200)
			if err != nil {
				return scheduler.ProactiveSpend{}, err
			}
			var sp scheduler.ProactiveSpend
			for _, run := range runs {
				if run.StartedAt.Before(since) {
					continue
				}
				if run.Origin == nil || (run.Origin.ConversationID != scheduler.AutomationConversationID && run.Origin.AutomationID == "") {
					continue
				}
				sp.TaskLLMUSD += run.Spent + run.SubtaskSpent
			}
			return sp, nil
		},
		ReadPosition: db.ReadPosition,
		LastUserTurnAt: func(context.Context) (time.Time, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.lastTurnAt, nil
		},
		LastActivity: func(context.Context) (time.Time, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.lastActive, nil
		},
		Accept: func(ctx context.Context, cmd state.Command) (state.Run, error) {
			receipts, err := store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
			if err != nil {
				return state.Run{}, err
			}
			if checker != nil {
				_ = checker.RecordCommandReceipts(u.ID, receipts)
			}
			_ = store.Delivered(ctx, cmd.ID)
			h.mu.Lock()
			if !h.acceptedIDs[cmd.RunID] {
				h.acceptedIDs[cmd.RunID] = true
				h.accepted = append(h.accepted, cmd)
			}
			h.mu.Unlock()
			return receipts[0].Run, nil
		},
	}

	return h
}

func (h *proactivitySimHarness) checkQuiescence(ctx context.Context) {
	h.t.Helper()
	if h.checker == nil {
		return
	}
	targets := []invariants.UserTarget{{
		User:        h.user,
		Store:       h.store,
		Permissions: h.perms,
		Scheduler:   h.sched,
	}}
	if err := h.checker.CheckQuiescence(ctx, targets, h.clock.Now()); err != nil {
		h.t.Fatalf("CheckQuiescence: %v", err)
	}
}

func (h *proactivitySimHarness) commandsFor(autoID string) []state.Command {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []state.Command
	for _, cmd := range h.accepted {
		if cmd.Origin != nil && cmd.Origin.AutomationID == autoID {
			out = append(out, cmd)
		}
	}
	return out
}

func (h *proactivitySimHarness) completeRun(ctx context.Context, runID, result string) {
	h.t.Helper()
	run, err := h.store.GetRun(ctx, runID)
	if err != nil {
		h.t.Fatalf("GetRun(%s): %v", runID, err)
	}
	if run.Incorporated < state.TaskPosition(run) {
		if err := h.store.Incorporate(ctx, state.IncorporateRequest{
			RunID:    run.ID,
			Revision: run.Revision,
			Position: state.TaskPosition(run),
		}); err != nil {
			h.t.Fatalf("Incorporate(%s): %v", runID, err)
		}
		run, err = h.store.GetRun(ctx, run.ID)
		if err != nil {
			h.t.Fatalf("GetRun after incorporate(%s): %v", runID, err)
		}
	}
	pub, err := h.store.Publish(ctx, state.PublishRequest{
		RunID:    run.ID,
		Revision: run.Revision,
		Status:   state.RunCompleted,
		Text:     result,
	})
	if err != nil {
		h.t.Fatalf("Publish completed(%s): %v", runID, err)
	}
	if pub.Finishing != nil || pub.CleanupPending {
		if err := h.store.FinishCleanup(ctx, run.ID, ""); err != nil {
			h.t.Fatalf("FinishCleanup(%s): %v", runID, err)
		}
	}
}

func (h *proactivitySimHarness) failRun(ctx context.Context, runID, errMsg string) {
	h.t.Helper()
	run, err := h.store.GetRun(ctx, runID)
	if err != nil {
		h.t.Fatalf("GetRun(%s): %v", runID, err)
	}
	pub, err := h.store.Publish(ctx, state.PublishRequest{
		RunID:    run.ID,
		Revision: run.Revision,
		Status:   state.RunFailed,
		Text:     errMsg,
	})
	if err != nil {
		h.t.Fatalf("Publish failed(%s): %v", runID, err)
	}
	if pub.Finishing != nil || pub.CleanupPending {
		if err := h.store.FinishCleanup(ctx, run.ID, ""); err != nil {
			h.t.Fatalf("FinishCleanup(%s): %v", runID, err)
		}
	}
}

func (h *proactivitySimHarness) chatMessages(ctx context.Context) []state.Message {
	h.t.Helper()
	msgs, _, err := h.db.ConversationMessages(ctx, "")
	if err != nil {
		h.t.Fatalf("ConversationMessages: %v", err)
	}
	return msgs
}

// --- Runner helpers for Scenarios 5 & 6 ---

type proactScriptedModel struct {
	mu    sync.Mutex
	calls int
	fn    func(ctx context.Context, callNum int, req agent.Request) (agent.Response, error)
}

func (m *proactScriptedModel) Provider() string { return "sim-proact" }
func (m *proactScriptedModel) Name() string     { return "sim-proact-model" }
func (m *proactScriptedModel) Inspect(context.Context, agent.InspectRequest) (agent.Inspection, error) {
	return agent.Inspection{Observation: "ok", Sent: true, CostKnown: true}, nil
}
func (m *proactScriptedModel) Respond(ctx context.Context, req agent.Request) (agent.Response, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	fn := m.fn
	m.mu.Unlock()
	return fn(ctx, n, req)
}

func proactToolCallResp(id, name string, args map[string]any, cost float64) agent.Response {
	call := transcript.ToolCall{ID: id, Name: name, Args: args}
	msg := transcript.Message{
		Role:  transcript.RoleModel,
		Parts: []transcript.Part{{ToolCall: &call}},
	}
	return agent.Response{
		Message:   &msg,
		Calls:     []transcript.ToolCall{call},
		Cost:      cost,
		CostKnown: true,
	}
}

type proactExtSource struct {
	defs []extensions.Definition
}

func (s *proactExtSource) Inventory(context.Context) (extensions.Inventory, error) {
	return extensions.Inventory{Definitions: s.defs}, nil
}

type proactEmailAdapter struct {
	mu           sync.Mutex
	executedArgs []map[string]any
}

func (a *proactEmailAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
	to := invoke.String(call.Args, "to")
	subj := invoke.String(call.Args, "subject")
	body := invoke.String(call.Args, "body")
	spec := map[string]string{"to": to, "subject": subj, "body": body}
	return invoke.Prepared{
		Summary:                 "To: " + to + "\nSubject: " + subj + "\n\n" + body,
		PromptTitle:             "Send this email?",
		Effect:                  extensions.EffectSensitiveSend,
		Recipients:              []string{to},
		Spec:                    spec,
		Keys:                    map[string]string{"action_key": invoke.ActionKey(call, spec)},
		RequireExplicitApproval: true,
	}, nil
}

func (a *proactEmailAdapter) Execute(_ context.Context, call invoke.Call, _ invoke.Prepared) invoke.Result {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := map[string]any{}
	for k, v := range call.Args {
		cp[k] = v
	}
	a.executedArgs = append(a.executedArgs, cp)
	return invoke.Result{
		Outcome: "confirmed",
		Content: map[string]any{"sent": true, "to": call.Args["to"], "subject": call.Args["subject"], "body": call.Args["body"]},
	}
}

type proactComputerAdapter struct {
	sawReadOnly atomic.Bool
	execCalls   atomic.Int32
}

func (a *proactComputerAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
	a.sawReadOnly.Store(call.ReadOnly)
	return invoke.Prepared{
		Summary: "Run computer shell",
		Effect:  extensions.EffectReversibleWrite,
		Spec:    map[string]string{"cmd": invoke.String(call.Args, "command")},
	}, nil
}

func (a *proactComputerAdapter) Execute(_ context.Context, call invoke.Call, _ invoke.Prepared) invoke.Result {
	a.sawReadOnly.Store(call.ReadOnly)
	a.execCalls.Add(1)
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"ok": true}}
}

type proactStubAdapter struct {
	effect    extensions.Effect
	execCalls atomic.Int32
}

func (a *proactStubAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
	eff := a.effect
	if eff == "" {
		eff = extensions.EffectReversibleWrite
	}
	return invoke.Prepared{
		Summary: "Stub " + call.Exec.Tool.ID,
		Effect:  eff,
		Spec:    map[string]string{"tool": call.Exec.Tool.ID},
	}, nil
}

func (a *proactStubAdapter) Execute(_ context.Context, _ invoke.Call, _ invoke.Prepared) invoke.Result {
	a.execCalls.Add(1)
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"ok": true}}
}

func sealSimDef(t *testing.T, id, extID, modelName, capability, target string, effect extensions.Effect) extensions.Definition {
	t.Helper()
	def, err := extensions.Seal(extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: id, Version: "1"},
		Extension:       extensions.DefinitionRef{ID: extID, Version: "1"},
		ModelName:       modelName,
		Title:           modelName,
		Description:     "Sim tool " + modelName,
		Capability:      capability,
		CapabilityTitle: capability,
		InputSchema:     json.RawMessage(`{"type":"object","properties":{"to":{"type":"string"},"subject":{"type":"string"},"body":{"type":"string"},"command":{"type":"string"}}}`),
		Binding:         extensions.Binding{Adapter: extensions.AdapterNative, Target: target, Version: "1"},
		Policy:          extensions.Policy{Effect: effect, Retry: "reconcile_only", TimeoutMS: 30_000, Location: extensions.LocationRuntime},
		Exposure:        extensions.ExposureDirect,
	})
	if err != nil {
		t.Fatalf("Seal(%s): %v", id, err)
	}
	return def
}

func (h *proactivitySimHarness) claimAndRunTask(
	ctx context.Context,
	runID string,
	workerID string,
	modelFn func(ctx context.Context, callNum int, req agent.Request) (agent.Response, error),
	defs []extensions.Definition,
	adapters *invoke.Registry,
) (task.RunOutcome, *task.Executor) {
	h.t.Helper()
	model := &proactScriptedModel{fn: modelFn}
	extSource := &proactExtSource{defs: defs}
	agentSvc := agent.New(h.store, model)
	agentSvc.Now = h.clock.Now
	agentSvc.Extensions = extSource
	agentSvc.Capabilities = agent.Capabilities{Location: time.UTC}

	sleepFn := func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			h.clock.Advance(d)
		}
		return ctx.Err()
	}
	noopTicker := func(time.Duration) (<-chan time.Time, func()) {
		return make(chan time.Time), func() {}
	}
	exec := &task.Executor{
		Adapters:    adapters,
		Permissions: h.perms,
		Now:         h.clock.Now,
		Sleep:       sleepFn,
	}
	act := &task.Activities{
		Agent:     agentSvc,
		Store:     h.store,
		Executor:  exec,
		Now:       h.clock.Now,
		Sleep:     sleepFn,
		NewTicker: noopTicker,
	}

	leases, ok := any(h.backend).(docstore.LeaseStore)
	if !ok {
		h.t.Fatalf("backend %T does not implement docstore.LeaseStore", h.backend)
	}
	claim, ok, err := leases.ClaimTaskByID(ctx, h.user.ID, runID, workerID, state.TaskLease)
	if err != nil {
		h.t.Fatalf("ClaimTaskByID(%s): %v", runID, err)
	}
	if !ok {
		h.t.Fatalf("ClaimTaskByID(%s): expected claimable lease", runID)
	}
	runner := &task.Runner{
		Activities: act,
		Leases:     leases,
		UserID:     h.user.ID,
		Owner:      workerID,
		Now:        h.clock.Now,
		NewTicker:  noopTicker,
	}
	outcome, err := runner.Run(ctx, claim)
	if err != nil {
		h.t.Fatalf("runner.Run(%s): %v", runID, err)
	}
	return outcome, exec
}

// ============================================================================
// Scenario 1: Silent NO_OP & Origin Isolation
// ============================================================================

func TestProactivitySilentNoOpAndOriginIsolation(t *testing.T) {
	runProactivityModes(t, func(t *testing.T, mode proactivityBackendMode) {
		ctx := context.Background()
		// 10:00 PT (outside quiet hours 21:00–07:30).
		start := time.Date(2026, 5, 4, 17, 0, 0, 0, time.UTC)
		h := newProactivitySimHarness(t, mode, 1001, "America/Los_Angeles", start)
		h.prefs.Proactivity.DailyProactiveBudgetUSD = 100.0

		receipt, err := h.sched.Save(ctx, "", "req-watch-5m", 0, scheduler.Editable{
			Title:       "5-Min Invoice Watch",
			Instruction: "Check inbox for new unpaid invoices.",
			Trigger: scheduler.Trigger{
				Kind:               "watch",
				Source:             &scheduler.ResourceRef{ConnectionID: "conn-gmail", Kind: "gmail", ProviderID: "inbox"},
				PredicateRef:       "new_unpaid_invoice",
				MinIntervalSeconds: 300,
			},
			BudgetUSD:      0.05,
			MaxActiveRuns:  1,
			MissedRun:      "skip",
			DestinationIDs: []string{scheduler.ChatDestination},
		})
		if err != nil {
			t.Fatalf("Save 5-min watch: %v", err)
		}
		auto := mustDecodeAutomation(t, receipt)

		// Run #1: explicit NO_OP -> 0 Chat messages, 0 outer-loop calls, 0 pushes, reason="no_change".
		h.clock.Advance(5*time.Minute + time.Second)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 1: %v", err)
		}
		cmds := h.commandsFor(auto.ID)
		if len(cmds) != 1 {
			t.Fatalf("run 1 commands = %d, want 1", len(cmds))
		}
		run1, err := h.store.GetRun(ctx, cmds[0].RunID)
		if err != nil {
			t.Fatalf("GetRun 1: %v", err)
		}
		if run1.Origin == nil || run1.Origin.ConversationID != scheduler.AutomationConversationID {
			t.Fatalf("run1.Origin = %+v, want ConversationID=%q", run1.Origin, scheduler.AutomationConversationID)
		}
		h.completeRun(ctx, run1.ID, `{"outcome":"no_op","summary":"No new invoices found"}`)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 1 settle: %v", err)
		}

		ctrl, _, err := h.store.ConversationStatus(ctx)
		if err != nil {
			t.Fatalf("ConversationStatus: %v", err)
		}
		if ctrl.Pending() || ctrl.Received != 0 {
			t.Fatalf("NO_OP leaked into outer conversation loop: %+v", ctrl)
		}
		if got := len(h.chatMessages(ctx)); got != 0 {
			t.Fatalf("chat messages after NO_OP = %d, want 0", got)
		}
		if got := len(h.outerEvents); got != 0 {
			t.Fatalf("outer-loop events after NO_OP = %d, want 0", got)
		}
		if got := h.pushSender.Count(); got != 0 {
			t.Fatalf("pushes after NO_OP = %d, want 0", got)
		}
		occs, _ := h.sched.Occurrences(ctx, auto.ID)
		if len(occs) != 1 || occs[0].Status != scheduler.OccurrenceCompleted || occs[0].Reason != "no_change" {
			t.Fatalf("occ 1 = %+v, want completed/no_change", occs)
		}

		// Run #2: new finding_keys -> surfaces alert & checkpoints finding_keys.
		h.clock.Advance(5*time.Minute + time.Second)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 2: %v", err)
		}
		cmds = h.commandsFor(auto.ID)
		if len(cmds) != 2 {
			t.Fatalf("run 2 commands = %d, want 2", len(cmds))
		}
		h.completeRun(ctx, cmds[1].RunID, `{"outcome":"alert","summary":"Invoice #42 from Acme ($500) is due tomorrow.","finding_keys":["inv:42"]}`)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 2 settle: %v", err)
		}
		if got := len(h.outerEvents); got != 1 {
			t.Fatalf("outer-loop events after new alert = %d, want 1", got)
		}
		chatCountAfterAlert := len(h.chatMessages(ctx))

		// Run #3: reworded text with SAME finding_keys -> stays silent, 0 outer-loop calls, 0 Chat messages, 0 pushes, reason="no_change".
		h.clock.Advance(5*time.Minute + time.Second)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 3: %v", err)
		}
		cmds = h.commandsFor(auto.ID)
		if len(cmds) != 3 {
			t.Fatalf("run 3 commands = %d, want 3", len(cmds))
		}
		h.completeRun(ctx, cmds[2].RunID, `{"outcome":"alert","summary":"Reminder: Acme invoice number forty-two for five hundred dollars is still unpaid!","finding_keys":["inv:42"]}`)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 3 settle: %v", err)
		}

		if got := len(h.outerEvents); got != 1 {
			t.Fatalf("reworded same finding_keys triggered outer-loop event: got %d, want 1", got)
		}
		if got := len(h.chatMessages(ctx)); got != chatCountAfterAlert {
			t.Fatalf("reworded same finding_keys added chat messages: got %d, want %d", got, chatCountAfterAlert)
		}
		if got := h.pushSender.Count(); got != 0 {
			t.Fatalf("pushes after unchanged finding_keys = %d, want 0", got)
		}
		occs, _ = h.sched.Occurrences(ctx, auto.ID)
		slices.SortFunc(occs, func(a, b scheduler.Occurrence) int { return a.ScheduledAt.Compare(b.ScheduledAt) })
		if len(occs) != 3 || occs[2].Status != scheduler.OccurrenceCompleted || occs[2].Reason != "no_change" || len(occs[2].DeliveryIDs) != 0 {
			t.Fatalf("occ 3 = %+v, want completed/no_change with 0 deliveries", occs[2])
		}

		h.checkQuiescence(ctx)
	})
}

// ============================================================================
// Scenario 2: Failure Throttling & Auto-Pause
// ============================================================================

func TestProactivityFailureThrottlingAndAutoPause(t *testing.T) {
	runProactivityModes(t, func(t *testing.T, mode proactivityBackendMode) {
		ctx := context.Background()
		start := time.Date(2026, 5, 4, 17, 0, 0, 0, time.UTC)
		h := newProactivitySimHarness(t, mode, 1002, "America/Los_Angeles", start)
		h.prefs.Proactivity.DailyProactiveBudgetUSD = 100.0
		const threshold = 4
		h.sched.FailurePauseThreshold = threshold

		receipt, err := h.sched.Save(ctx, "", "req-broken-watch", 0, scheduler.Editable{
			Title:       "Broken Watch",
			Instruction: "Poll broken endpoint.",
			Trigger: scheduler.Trigger{
				Kind:               "watch",
				Source:             &scheduler.ResourceRef{ConnectionID: "conn-broken", Kind: "webhook", ProviderID: "broken"},
				PredicateRef:       "always_fail",
				MinIntervalSeconds: 300,
			},
			BudgetUSD:      0.05,
			MaxActiveRuns:  1,
			MissedRun:      "skip",
			DestinationIDs: []string{scheduler.ChatDestination},
		})
		if err != nil {
			t.Fatalf("Save broken watch: %v", err)
		}
		auto := mustDecodeAutomation(t, receipt)

		for attempt := 1; attempt <= threshold; attempt++ {
			h.clock.Advance(5*time.Minute + time.Second)
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick attempt %d: %v", attempt, err)
			}
			cmds := h.commandsFor(auto.ID)
			if len(cmds) != attempt {
				t.Fatalf("attempt %d commands = %d, want %d", attempt, len(cmds), attempt)
			}
			h.failRun(ctx, cmds[attempt-1].RunID, fmt.Sprintf("permanent schema error #%d", attempt))
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick settle attempt %d: %v", attempt, err)
			}

			msgs := h.chatMessages(ctx)
			cur, err := h.sched.Get(ctx, auto.ID)
			if err != nil {
				t.Fatalf("Get automation attempt %d: %v", attempt, err)
			}
			switch {
			case attempt == 1:
				if len(msgs) != 1 {
					t.Fatalf("failure #1 chat messages = %d, want 1", len(msgs))
				}
				if cur.Status != scheduler.AutomationEnabled {
					t.Fatalf("failure #1 status = %s, want enabled", cur.Status)
				}
			case attempt < threshold:
				if len(msgs) != 1 {
					t.Fatalf("failure #%d should be suppressed: chat messages = %d, want 1", attempt, len(msgs))
				}
				if cur.Status != scheduler.AutomationEnabled {
					t.Fatalf("failure #%d status = %s, want enabled", attempt, cur.Status)
				}
			case attempt == threshold:
				if len(msgs) != 2 {
					t.Fatalf("failure #%d should alert on auto-pause: chat messages = %d, want 2", attempt, len(msgs))
				}
				if !strings.Contains(msgs[1].Text, "Paused") {
					t.Fatalf("failure #%d alert text = %q, want pause notice", attempt, msgs[1].Text)
				}
				if cur.Status != scheduler.AutomationPaused || cur.PausedReason != "consecutive_failures" || cur.NextAt != nil {
					t.Fatalf("failure #%d automation = (status=%s, reason=%q, next=%v), want paused/consecutive_failures/nil", attempt, cur.Status, cur.PausedReason, cur.NextAt)
				}
			}
		}

		// Verify occurrences #2..N-1 recorded reason="failure_throttled" with 0 deliveries.
		occs, err := h.sched.Occurrences(ctx, auto.ID)
		if err != nil {
			t.Fatalf("Occurrences: %v", err)
		}
		slices.SortFunc(occs, func(a, b scheduler.Occurrence) int { return a.ScheduledAt.Compare(b.ScheduledAt) })
		if len(occs) != threshold {
			t.Fatalf("len(occs) = %d, want %d", len(occs), threshold)
		}
		if len(occs[0].DeliveryIDs) != 1 {
			t.Fatalf("occ #1 deliveries = %v, want 1", occs[0].DeliveryIDs)
		}
		for i := 1; i < threshold-1; i++ {
			if occs[i].Reason != "failure_throttled" || len(occs[i].DeliveryIDs) != 0 {
				t.Fatalf("occ #%d = (reason=%q, deliveries=%v), want failure_throttled/empty", i+1, occs[i].Reason, occs[i].DeliveryIDs)
			}
		}
		if len(occs[threshold-1].DeliveryIDs) != 1 {
			t.Fatalf("occ #%d deliveries = %v, want 1", threshold, occs[threshold-1].DeliveryIDs)
		}

		// Advancing further must not schedule any new occurrences while paused.
		h.clock.Advance(30 * time.Minute)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick after auto-pause: %v", err)
		}
		if got := len(h.commandsFor(auto.ID)); got != threshold {
			t.Fatalf("commands after auto-pause = %d, want %d", got, threshold)
		}

		h.checkQuiescence(ctx)
	})
}

// ============================================================================
// Scenario 3: Quiet Hours Across Midnight & DST Nights
// ============================================================================

func TestProactivityQuietHoursMidnightAndDST(t *testing.T) {
	runProactivityModes(t, func(t *testing.T, mode proactivityBackendMode) {
		ctx := context.Background()
		la, err := time.LoadLocation("America/Los_Angeles")
		if err != nil {
			t.Fatalf("LoadLocation: %v", err)
		}

		// --- Part A: Normal midnight wrap -> held at 22:30, folded into 08:00 AM PartBrief ---
		t.Run("normal_midnight_wrap_folds_into_brief", func(t *testing.T) {
			nightStart := time.Date(2026, 5, 4, 22, 25, 0, 0, la).UTC()
			h := newProactivitySimHarness(t, mode, 1003, "America/Los_Angeles", nightStart)
			h.prefs.Proactivity.DailyProactiveBudgetUSD = 10.0

			if _, err := h.sched.EnsureMorningBrief(ctx, h.prefs); err != nil {
				t.Fatalf("EnsureMorningBrief: %v", err)
			}
			alertAt := time.Date(2026, 5, 4, 22, 30, 0, 0, la).UTC()
			rec, err := h.sched.Save(ctx, "", "req-night-alert", 0, scheduler.Editable{
				Title:          "Overnight Alert",
				Instruction:    "Check overnight servers.",
				Trigger:        scheduler.Trigger{Kind: "once", At: &alertAt},
				MaxActiveRuns:  1,
				DestinationIDs: []string{scheduler.ChatDestination},
			})
			if err != nil {
				t.Fatalf("Save overnight alert: %v", err)
			}
			alertAuto := mustDecodeAutomation(t, rec)

			h.clock.Set(alertAt.Add(time.Second))
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick 22:30: %v", err)
			}
			cmds := h.commandsFor(alertAuto.ID)
			if len(cmds) != 1 {
				t.Fatalf("overnight alert commands = %d, want 1", len(cmds))
			}
			h.completeRun(ctx, cmds[0].RunID, `{"outcome":"alert","summary":"Build pipeline turned red.","finding_keys":["ci:red:1"]}`)
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick 22:30 settle: %v", err)
			}

			occs, _ := h.sched.Occurrences(ctx, alertAuto.ID)
			if len(occs) != 1 || len(occs[0].DeliveryIDs) != 1 {
				t.Fatalf("overnight occ = %+v", occs)
			}
			heldID := occs[0].DeliveryIDs[0]
			deliv, err := h.sched.Delivery(ctx, heldID)
			if err != nil {
				t.Fatalf("Delivery(%s): %v", heldID, err)
			}
			wantQuietEnd := time.Date(2026, 5, 5, 7, 30, 0, 0, la).UTC()
			if deliv.State != scheduler.DeliveryStateHeld || deliv.ScheduledFor == nil || !deliv.ScheduledFor.Equal(wantQuietEnd) {
				t.Fatalf("held delivery = (state=%s, scheduled_for=%v), want held @ %s", deliv.State, deliv.ScheduledFor, wantQuietEnd)
			}
			if len(h.chatMessages(ctx)) != 0 || len(h.outerEvents) != 0 {
				t.Fatalf("quiet-hours alert leaked to chat/outer-loop before quiet end")
			}

			// At 07:30 local, Morning Brief is enabled so held item stays held waiting for 08:00 brief.
			h.clock.Set(wantQuietEnd.Add(time.Second))
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick 07:30: %v", err)
			}
			deliv, _ = h.sched.Delivery(ctx, heldID)
			if deliv.State != scheduler.DeliveryStateHeld {
				t.Fatalf("at 07:30 with enabled brief, state = %s, want held", deliv.State)
			}

			// At 08:00 local, Morning Brief runs and folds the held delivery into PartBrief.
			briefAt := time.Date(2026, 5, 5, 8, 0, 0, 0, la).UTC()
			h.clock.Set(briefAt.Add(time.Second))
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick 08:00 brief: %v", err)
			}
			briefCmds := h.commandsFor(scheduler.MorningBriefAutomationID)
			if len(briefCmds) != 1 {
				t.Fatalf("brief commands = %d, want 1", len(briefCmds))
			}
			h.completeRun(ctx, briefCmds[0].RunID, `{"outcome":"update","greeting":"Good morning","items":[{"title":"Team standup","trailing":"9:30 AM"}]}`)
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick 08:00 brief settle: %v", err)
			}

			deliv, _ = h.sched.Delivery(ctx, heldID)
			if deliv.State != scheduler.DeliveryStateFolded {
				t.Fatalf("after brief success, held delivery state = %s, want folded", deliv.State)
			}
			msgs := h.chatMessages(ctx)
			if len(msgs) != 1 || len(msgs[0].Parts) != 1 || msgs[0].Parts[0].Type != state.PartBrief {
				t.Fatalf("expected 1 PartBrief chat message, got %+v", msgs)
			}
			foundFolded := false
			for _, it := range msgs[0].Parts[0].Items {
				if strings.Contains(it.Title, "Overnight Alert") || strings.Contains(it.Trailing, "Build pipeline turned red") {
					foundFolded = true
				}
			}
			if !foundFolded {
				t.Fatalf("PartBrief items did not contain folded overnight alert: %+v", msgs[0].Parts[0].Items)
			}
			h.checkQuiescence(ctx)
		})

		// --- Part B: US Spring-Forward 23h night (2026-03-07 22:30 PST -> 2026-03-08 07:30 PDT) with brief disabled -> flushes at 07:30 PDT ---
		t.Run("spring_forward_23h_brief_disabled_flushes_at_0730", func(t *testing.T) {
			springNight := time.Date(2026, 3, 7, 22, 25, 0, 0, la).UTC()
			h := newProactivitySimHarness(t, mode, 1004, "America/Los_Angeles", springNight)
			h.prefs.Proactivity.MorningBriefEnabled = boolPtr(false)
			h.prefs.Proactivity.DailyProactiveBudgetUSD = 10.0

			alertAt := time.Date(2026, 3, 7, 22, 30, 0, 0, la).UTC() // 2026-03-08T06:30:00Z
			inQuiet, quietEnd := scheduler.InQuietHours(alertAt, h.prefs.EffectiveQuietHours(), "America/Los_Angeles")
			wantSpringEnd := time.Date(2026, 3, 8, 14, 30, 0, 0, time.UTC) // 07:30 PDT (UTC-7)
			if !inQuiet || !quietEnd.Equal(wantSpringEnd) {
				t.Fatalf("InQuietHours spring-forward = (%v, %s), want (true, %s)", inQuiet, quietEnd, wantSpringEnd)
			}

			rec, err := h.sched.Save(ctx, "", "req-spring-alert", 0, scheduler.Editable{
				Title:          "Spring Forward Alert",
				Instruction:    "Alert on 23h DST night.",
				Trigger:        scheduler.Trigger{Kind: "once", At: &alertAt},
				MaxActiveRuns:  1,
				DestinationIDs: []string{scheduler.ChatDestination},
			})
			if err != nil {
				t.Fatalf("Save spring alert: %v", err)
			}
			auto := mustDecodeAutomation(t, rec)

			h.clock.Set(alertAt.Add(time.Second))
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick spring alert: %v", err)
			}
			cmds := h.commandsFor(auto.ID)
			h.completeRun(ctx, cmds[0].RunID, `{"outcome":"update","summary":"Spring night delivery"}`)
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick spring settle: %v", err)
			}

			occs, _ := h.sched.Occurrences(ctx, auto.ID)
			heldID := occs[0].DeliveryIDs[0]
			deliv, _ := h.sched.Delivery(ctx, heldID)
			if deliv.State != scheduler.DeliveryStateHeld || deliv.ScheduledFor == nil || !deliv.ScheduledFor.Equal(wantSpringEnd) {
				t.Fatalf("spring held delivery = (%s, %v), want held @ %s", deliv.State, deliv.ScheduledFor, wantSpringEnd)
			}

			// 1 minute before 07:30 PDT: still held.
			h.clock.Set(wantSpringEnd.Add(-time.Minute))
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick 07:29 PDT: %v", err)
			}
			deliv, _ = h.sched.Delivery(ctx, heldID)
			if deliv.State != scheduler.DeliveryStateHeld {
				t.Fatalf("at 07:29 PDT state = %s, want held", deliv.State)
			}

			// At 07:30 PDT: flushes immediately because Morning Brief is disabled.
			h.clock.Set(wantSpringEnd)
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick 07:30 PDT: %v", err)
			}
			deliv, _ = h.sched.Delivery(ctx, heldID)
			if deliv.State != scheduler.DeliveryStateDelivered {
				t.Fatalf("at 07:30 PDT state = %s, want delivered", deliv.State)
			}
			if len(h.chatMessages(ctx)) != 1 {
				t.Fatalf("flushed chat messages = %d, want 1", len(h.chatMessages(ctx)))
			}
			h.checkQuiescence(ctx)
		})

		// --- Part C: US Fall-Back 25h night (2026-10-31 22:30 PDT -> 2026-11-01 07:30 PST) with brief failing -> flushes ---
		t.Run("fall_back_25h_brief_failure_flushes", func(t *testing.T) {
			fallNight := time.Date(2026, 10, 31, 22, 25, 0, 0, la).UTC()
			h := newProactivitySimHarness(t, mode, 1005, "America/Los_Angeles", fallNight)
			h.prefs.Proactivity.DailyProactiveBudgetUSD = 10.0

			if _, err := h.sched.EnsureMorningBrief(ctx, h.prefs); err != nil {
				t.Fatalf("EnsureMorningBrief fall-back: %v", err)
			}
			alertAt := time.Date(2026, 10, 31, 22, 30, 0, 0, la).UTC() // 2026-11-01T05:30:00Z
			inQuiet, quietEnd := scheduler.InQuietHours(alertAt, h.prefs.EffectiveQuietHours(), "America/Los_Angeles")
			wantFallEnd := time.Date(2026, 11, 1, 15, 30, 0, 0, time.UTC) // 07:30 PST (UTC-8)
			if !inQuiet || !quietEnd.Equal(wantFallEnd) {
				t.Fatalf("InQuietHours fall-back = (%v, %s), want (true, %s)", inQuiet, quietEnd, wantFallEnd)
			}

			rec, err := h.sched.Save(ctx, "", "req-fall-alert", 0, scheduler.Editable{
				Title:          "Fall Back Alert",
				Instruction:    "Alert on 25h DST night.",
				Trigger:        scheduler.Trigger{Kind: "once", At: &alertAt},
				MaxActiveRuns:  1,
				DestinationIDs: []string{scheduler.ChatDestination},
			})
			if err != nil {
				t.Fatalf("Save fall alert: %v", err)
			}
			auto := mustDecodeAutomation(t, rec)

			h.clock.Set(alertAt.Add(time.Second))
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick fall alert: %v", err)
			}
			cmds := h.commandsFor(auto.ID)
			h.completeRun(ctx, cmds[0].RunID, `{"outcome":"update","summary":"Fall-back overnight update"}`)
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick fall settle: %v", err)
			}

			occs, _ := h.sched.Occurrences(ctx, auto.ID)
			heldID := occs[0].DeliveryIDs[0]
			deliv, _ := h.sched.Delivery(ctx, heldID)
			if deliv.State != scheduler.DeliveryStateHeld || deliv.ScheduledFor == nil || !deliv.ScheduledFor.Equal(wantFallEnd) {
				t.Fatalf("fall held delivery = (%s, %v), want held @ %s", deliv.State, deliv.ScheduledFor, wantFallEnd)
			}

			// At 08:00 PST (16:00Z), Morning Brief runs and FAILS -> held delivery must flush to delivered!
			briefAt := time.Date(2026, 11, 1, 16, 0, 0, 0, time.UTC)
			h.clock.Set(briefAt.Add(time.Second))
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick 08:00 PST brief: %v", err)
			}
			briefCmds := h.commandsFor(scheduler.MorningBriefAutomationID)
			if len(briefCmds) != 1 {
				t.Fatalf("fall-back brief commands = %d, want 1", len(briefCmds))
			}
			h.failRun(ctx, briefCmds[0].RunID, "calendar connector failed")
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick 08:00 PST brief fail settle: %v", err)
			}

			deliv, _ = h.sched.Delivery(ctx, heldID)
			if deliv.State != scheduler.DeliveryStateDelivered {
				t.Fatalf("after brief failure, held delivery state = %s, want delivered", deliv.State)
			}
			h.checkQuiescence(ctx)
		})
	})
}

// ============================================================================
// Scenario 4: Anti-Nagging & Single-Fold Decay
// ============================================================================

func TestProactivityAntiNaggingAndSingleFoldDecay(t *testing.T) {
	runProactivityModes(t, func(t *testing.T, mode proactivityBackendMode) {
		ctx := context.Background()
		la, _ := time.LoadLocation("America/Los_Angeles")
		start := time.Date(2026, 5, 4, 22, 0, 0, 0, la).UTC()
		h := newProactivitySimHarness(t, mode, 1006, "America/Los_Angeles", start)
		h.prefs.Proactivity.DailyProactiveBudgetUSD = 10.0

		if _, err := h.sched.EnsureMorningBrief(ctx, h.prefs); err != nil {
			t.Fatalf("EnsureMorningBrief: %v", err)
		}

		// Helper to fire a one-shot alert at a given instant and settle it.
		fireAlert := func(reqID string, at time.Time, payload string) (scheduler.Occurrence, string) {
			t.Helper()
			rec, err := h.sched.Save(ctx, "", reqID, 0, scheduler.Editable{
				Title:          "Bill Reminder " + reqID,
				Instruction:    "Remind user.",
				Trigger:        scheduler.Trigger{Kind: "once", At: &at},
				MaxActiveRuns:  1,
				DestinationIDs: []string{scheduler.ChatDestination},
			})
			if err != nil {
				t.Fatalf("Save(%s): %v", reqID, err)
			}
			a := mustDecodeAutomation(t, rec)
			h.clock.Set(at.Add(time.Second))
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick(%s): %v", reqID, err)
			}
			cmds := h.commandsFor(a.ID)
			if len(cmds) != 1 {
				t.Fatalf("cmds(%s) = %d, want 1", reqID, len(cmds))
			}
			h.completeRun(ctx, cmds[0].RunID, payload)
			if _, err := h.sched.Tick(ctx); err != nil {
				t.Fatalf("Tick settle(%s): %v", reqID, err)
			}
			occs, _ := h.sched.Occurrences(ctx, a.ID)
			delivID := ""
			if len(occs[0].DeliveryIDs) > 0 {
				delivID = occs[0].DeliveryIDs[0]
			}
			return occs[0], delivID
		}

		// Night 1 (22:30): held alert with finding_key="bill:water".
		_, heldNight1 := fireAlert("req-nag-1", time.Date(2026, 5, 4, 22, 30, 0, 0, la).UTC(),
			`{"outcome":"update","summary":"Water bill due Friday","finding_keys":["bill:water"]}`)

		// Morning Brief #1 (May 5 08:00): folds it once (BriefFoldCount = 1).
		brief1At := time.Date(2026, 5, 5, 8, 0, 0, 0, la).UTC()
		h.clock.Set(brief1At.Add(time.Second))
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick brief 1: %v", err)
		}
		briefCmds := h.commandsFor(scheduler.MorningBriefAutomationID)
		h.completeRun(ctx, briefCmds[0].RunID, `{"outcome":"update","greeting":"Morning 1"}`)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick brief 1 settle: %v", err)
		}
		d1, _ := h.sched.Delivery(ctx, heldNight1)
		if d1.State != scheduler.DeliveryStateFolded {
			t.Fatalf("night 1 delivery state = %s, want folded", d1.State)
		}

		// User does NOT engage. Daytime repeat with same finding_key is dropped by anti-nag.
		occDay, delivDay := fireAlert("req-nag-day", time.Date(2026, 5, 5, 14, 0, 0, 0, la).UTC(),
			`{"outcome":"update","summary":"Water bill still unpaid","finding_keys":["bill:water"]}`)
		if occDay.Reason != "anti_nag" || delivDay != "" {
			t.Fatalf("unengaged repeat = (reason=%q, deliv=%q), want anti_nag/empty", occDay.Reason, delivDay)
		}

		// Also verify a second held item for a finding already folded once is dropped on Brief #2.
		// Simulate a held delivery that was already queued before Brief #1 finished or arrives on Night 2.
		heldNight2ID := "dlv_nag_night2"
		night2Time := time.Date(2026, 5, 5, 22, 30, 0, 0, la).UTC()
		schedEnd2 := time.Date(2026, 5, 6, 7, 30, 0, 0, la).UTC()
		heldRes, err := state.NewResource(scheduler.CollectionDeliveries, heldNight2ID, 0, scheduler.Delivery{
			ID:            heldNight2ID,
			OccurrenceID:  "occ_night2",
			AutomationID:  "auto_night2",
			DestinationID: scheduler.ChatDestination,
			State:         scheduler.DeliveryStateHeld,
			Title:         "Water Bill Repeat",
			Body:          "Water bill repeat",
			Outcome:       scheduler.OutcomeUpdate,
			FindingKeys:   []string{"bill:water"},
			ScheduledFor:  &schedEnd2,
			CreatedAt:     &night2Time,
		}, map[string]string{"occurrence_id": "occ_night2", "state": scheduler.DeliveryStateHeld}, night2Time)
		if err != nil {
			t.Fatalf("NewResource heldNight2: %v", err)
		}
		if _, err := h.store.PutResource(ctx, heldRes, 0); err != nil {
			t.Fatalf("PutResource heldNight2: %v", err)
		}

		// Morning Brief #2 (May 6 08:00): drops the already-folded unengaged item.
		brief2At := time.Date(2026, 5, 6, 8, 0, 0, 0, la).UTC()
		h.clock.Set(brief2At.Add(time.Second))
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick brief 2: %v", err)
		}
		briefCmds = h.commandsFor(scheduler.MorningBriefAutomationID)
		h.completeRun(ctx, briefCmds[1].RunID, `{"outcome":"update","greeting":"Morning 2"}`)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick brief 2 settle: %v", err)
		}
		d2, _ := h.sched.Delivery(ctx, heldNight2ID)
		if d2.State != scheduler.DeliveryStateDropped || d2.Receipt != "anti_nag" {
			t.Fatalf("night 2 delivery = (state=%s, receipt=%q), want dropped/anti_nag", d2.State, d2.Receipt)
		}

		// --- Engagement Path 1: Read mark clears nag state ---
		readAt := time.Date(2026, 5, 6, 9, 0, 0, 0, la).UTC()
		h.clock.Set(readAt)
		msgs := h.chatMessages(ctx)
		if len(msgs) == 0 {
			t.Fatalf("expected brief messages in chat before MarkSeen")
		}
		if err := h.db.MarkSeen(ctx, msgs[len(msgs)-1].ID); err != nil {
			t.Fatalf("MarkSeen: %v", err)
		}
		occRead, delivRead := fireAlert("req-engaged-read", time.Date(2026, 5, 6, 10, 0, 0, 0, la).UTC(),
			`{"outcome":"update","summary":"Water bill follow-up after read","finding_keys":["bill:water"]}`)
		if occRead.Reason == "anti_nag" || delivRead == "" {
			t.Fatalf("after readmark engagement, expected delivery, got reason=%q deliv=%q", occRead.Reason, delivRead)
		}

		// --- Engagement Path 2: Interaction resolution clears nag state ---
		inter, err := h.perms.Open(ctx, permissions.Interaction{
			ID:           "int_nag_eng",
			Kind:         "approval",
			Prompt:       "Approve bill payment?",
			ActionDigest: "digest_bill_eng",
		}, "req-open-nag-inter")
		if err != nil {
			t.Fatalf("Open interaction: %v", err)
		}
		// Fold finding_key="bill:electric" linked to inter.ID once into Brief #3.
		_, _ = fireAlert("req-elec-1", time.Date(2026, 5, 6, 22, 30, 0, 0, la).UTC(),
			fmt.Sprintf(`{"outcome":"update","summary":"Electric bill","finding_keys":["bill:electric"],"interaction_id":%q}`, inter.ID))
		brief3At := time.Date(2026, 5, 7, 8, 0, 0, 0, la).UTC()
		h.clock.Set(brief3At.Add(time.Second))
		_, _ = h.sched.Tick(ctx)
		briefCmds = h.commandsFor(scheduler.MorningBriefAutomationID)
		h.completeRun(ctx, briefCmds[2].RunID, `{"outcome":"update","greeting":"Morning 3"}`)
		_, _ = h.sched.Tick(ctx)

		// Before resolving interaction, bill:electric is suppressed by anti-nag.
		occElecDrop, _ := fireAlert("req-elec-drop", time.Date(2026, 5, 7, 10, 0, 0, 0, la).UTC(),
			`{"outcome":"update","summary":"Electric bill again","finding_keys":["bill:electric"]}`)
		if occElecDrop.Reason != "anti_nag" {
			t.Fatalf("unengaged electric bill reason = %q, want anti_nag", occElecDrop.Reason)
		}
		// Resolve interaction -> clears nag state!
		if _, _, err := h.perms.Respond(ctx, inter.ID, "req-resolve-elec", inter.Revision, permissions.Response{
			Kind: "approve", ActionDigest: inter.ActionDigest,
		}); err != nil {
			t.Fatalf("Respond interaction: %v", err)
		}
		occElecOK, delivElecOK := fireAlert("req-elec-ok", time.Date(2026, 5, 7, 11, 0, 0, 0, la).UTC(),
			`{"outcome":"update","summary":"Electric bill receipt","finding_keys":["bill:electric"]}`)
		if occElecOK.Reason == "anti_nag" || delivElecOK == "" {
			t.Fatalf("after interaction resolution, expected delivery, got reason=%q deliv=%q", occElecOK.Reason, delivElecOK)
		}

		// --- Engagement Path 3: User turn clears nag state ---
		_, _ = fireAlert("req-gas-1", time.Date(2026, 5, 7, 22, 30, 0, 0, la).UTC(),
			`{"outcome":"update","summary":"Gas bill","finding_keys":["bill:gas"]}`)
		brief4At := time.Date(2026, 5, 8, 8, 0, 0, 0, la).UTC()
		h.clock.Set(brief4At.Add(time.Second))
		_, _ = h.sched.Tick(ctx)
		briefCmds = h.commandsFor(scheduler.MorningBriefAutomationID)
		h.completeRun(ctx, briefCmds[3].RunID, `{"outcome":"update","greeting":"Morning 4"}`)
		_, _ = h.sched.Tick(ctx)

		// Before user turn, bill:gas is suppressed.
		occGasDrop, _ := fireAlert("req-gas-drop", time.Date(2026, 5, 8, 10, 0, 0, 0, la).UTC(),
			`{"outcome":"update","summary":"Gas bill again","finding_keys":["bill:gas"]}`)
		if occGasDrop.Reason != "anti_nag" {
			t.Fatalf("unengaged gas bill reason = %q, want anti_nag", occGasDrop.Reason)
		}
		// Record a user turn after surfacing -> clears nag state!
		h.mu.Lock()
		h.lastTurnAt = time.Date(2026, 5, 8, 10, 30, 0, 0, la).UTC()
		h.mu.Unlock()
		occGasOK, delivGasOK := fireAlert("req-gas-ok", time.Date(2026, 5, 8, 11, 0, 0, 0, la).UTC(),
			`{"outcome":"update","summary":"Gas bill update","finding_keys":["bill:gas"]}`)
		if occGasOK.Reason == "anti_nag" || delivGasOK == "" {
			t.Fatalf("after user turn engagement, expected delivery, got reason=%q deliv=%q", occGasOK.Reason, delivGasOK)
		}

		h.checkQuiescence(ctx)
	})
}

// ============================================================================
// Scenario 5: Non-Blocking Draft + Editable Replacement Security
// ============================================================================

func TestProactivityNonBlockingDraftAndEditableReplacement(t *testing.T) {
	runProactivityModes(t, func(t *testing.T, mode proactivityBackendMode) {
		ctx := context.Background()
		la, _ := time.LoadLocation("America/Los_Angeles")
		// 22:55 local (11 PM draft runs at 23:00).
		start := time.Date(2026, 5, 4, 22, 55, 0, 0, la).UTC()
		h := newProactivitySimHarness(t, mode, 1007, "America/Los_Angeles", start)
		h.prefs.Proactivity.DailyProactiveBudgetUSD = 10.0

		sendDef := sealSimDef(t, "mock/tools/email_send", "mock/ext", "email_send", "mock", "mock/email_send", extensions.EffectSensitiveSend)
		emailAdapter := &proactEmailAdapter{}
		adapters := invoke.NewRegistry()
		adapters.Register(extensions.AdapterNative, "mock", emailAdapter)

		// Hourly recurring automation with MaxActiveRuns=1 starting at 23:00 local.
		rec, err := h.sched.Save(ctx, "", "req-11pm-draft", 0, scheduler.Editable{
			Title:       "Nightly Follow-up",
			Instruction: "Draft follow-up email if needed.",
			Trigger: scheduler.Trigger{
				Kind:       "recurring",
				LocalStart: "2026-05-04T23:00:00",
				Timezone:   "America/Los_Angeles",
				RRule:      "FREQ=DAILY;BYHOUR=0,23;BYMINUTE=0",
			},
			BudgetUSD:      0.5,
			MaxActiveRuns:  1,
			MissedRun:      "skip",
			DestinationIDs: []string{scheduler.ChatDestination},
		})
		if err != nil {
			t.Fatalf("Save 11 PM automation: %v", err)
		}
		auto := mustDecodeAutomation(t, rec)

		// 11:00 PM occurrence fires and tries to call email_send.
		slot1 := time.Date(2026, 5, 4, 23, 0, 0, 0, la).UTC()
		h.clock.Set(slot1.Add(time.Second))
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 23:00: %v", err)
		}
		cmds := h.commandsFor(auto.ID)
		if len(cmds) != 1 {
			t.Fatalf("23:00 commands = %d, want 1", len(cmds))
		}

		var sawDraftSaved atomic.Bool
		out1, exec := h.claimAndRunTask(ctx, cmds[0].RunID, "worker-11pm", func(_ context.Context, callNum int, req agent.Request) (agent.Response, error) {
			if callNum == 1 {
				return proactToolCallResp("c-send-11pm", "email_send", map[string]any{
					"to":      "alice@example.com",
					"subject": "11 PM Draft Subject",
					"body":    "Original 11 PM draft body.",
				}, 0.05), nil
			}
			for _, m := range req.Messages {
				raw, _ := json.Marshal(m)
				if strings.Contains(string(raw), "draft_saved") {
					sawDraftSaved.Store(true)
				}
			}
			return proactToolCallResp("c-done-11pm", "complete", map[string]any{
				"answer":   `{"outcome":"no_op","summary":"Saved draft for morning review."}`,
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.05), nil
		}, []extensions.Definition{sendDef}, adapters)

		if out1 != task.OutcomeDone || !sawDraftSaved.Load() {
			t.Fatalf("11 PM run = (outcome=%s, sawDraftSaved=%v), want (done, true)", out1, sawDraftSaved.Load())
		}
		if len(emailAdapter.executedArgs) != 0 {
			t.Fatalf("unattended draft must not execute email_send directly, got %d executions", len(emailAdapter.executedArgs))
		}

		// Settle 11:00 PM occurrence -> must be completed so MaxActiveRuns=1 is free!
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 23:00 settle: %v", err)
		}
		occs, _ := h.sched.Occurrences(ctx, auto.ID)
		if len(occs) != 1 || occs[0].Status != scheduler.OccurrenceCompleted {
			t.Fatalf("11 PM occurrence = %+v, want completed", occs)
		}

		// Subsequent 12:00 AM occurrence runs unimpeded even while 11 PM approval interaction is still pending!
		slot2 := time.Date(2026, 5, 5, 0, 0, 0, 0, la).UTC()
		h.clock.Set(slot2.Add(time.Second))
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 00:00: %v", err)
		}
		cmds = h.commandsFor(auto.ID)
		if len(cmds) != 2 {
			t.Fatalf("subsequent 00:00 occurrence was blocked! commands = %d, want 2", len(cmds))
		}
		h.completeRun(ctx, cmds[1].RunID, `{"outcome":"no_op","summary":"Midnight check quiet"}`)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick 00:00 settle: %v", err)
		}

		// Inspect pending 11 PM approval interaction.
		pending, _, err := h.perms.Interactions(ctx, "pending", cmds[0].RunID, "", 10)
		if err != nil || len(pending) != 1 {
			t.Fatalf("pending interactions = (%+v, %v), want 1", pending, err)
		}
		orig := pending[0]

		// Security check 1: recipient tampering is rejected.
		for _, field := range []string{"recipients", "to", "cc", "bcc"} {
			if _, _, err := h.perms.Respond(ctx, orig.ID, "req-tamper-"+field, orig.Revision, permissions.Response{
				Kind:         "approve",
				ActionDigest: orig.ActionDigest,
				EditedFields: map[string]string{field: "attacker@evil.com"},
			}); !errors.Is(err, state.ErrInvalidResource) {
				t.Fatalf("tampering %q err = %v, want ErrInvalidResource", field, err)
			}
		}

		// Security check 2: RememberRule on edited payloads is rejected.
		if _, _, err := h.perms.Respond(ctx, orig.ID, "req-remember-edited", orig.Revision, permissions.Response{
			Kind:         "approve",
			ActionDigest: orig.ActionDigest,
			RememberRule: true,
			EditedFields: map[string]string{"body": "Edited body with remember rule"},
		}); !errors.Is(err, state.ErrInvalidResource) {
			t.Fatalf("RememberRule + EditedFields err = %v, want ErrInvalidResource", err)
		}

		// Legitimate edit of subject & body creates superseded replacement with recomputed digest.
		receipt, ans, err := h.perms.Respond(ctx, orig.ID, "req-edit-ok", orig.Revision, permissions.Response{
			Kind:         "approve",
			ActionDigest: orig.ActionDigest,
			EditedFields: map[string]string{
				"subject": "Edited Morning Subject",
				"body":    "Edited morning body text.",
			},
		})
		if err != nil || ans == nil || ans.ReplacementInteractionID == "" {
			t.Fatalf("Respond edited = (ans=%+v, err=%v)", ans, err)
		}
		if ans.ActionDigest == "" || ans.ActionDigest == orig.ActionDigest {
			t.Fatalf("expected recomputed ActionDigest != original; got %q vs %q", ans.ActionDigest, orig.ActionDigest)
		}
		var repl permissions.Interaction
		if err := json.Unmarshal(receipt.Resource, &repl); err != nil {
			t.Fatalf("unmarshal replacement: %v", err)
		}
		origAfter, err := h.perms.Interaction(ctx, orig.ID)
		if err != nil {
			t.Fatalf("Interaction(orig): %v", err)
		}
		if origAfter.Status != "superseded" || origAfter.SupersededBy != repl.ID {
			t.Fatalf("origAfter = (status=%s, superseded_by=%s), want superseded/%s", origAfter.Status, origAfter.SupersededBy, repl.ID)
		}

		// Execute the approved edited draft via ProactiveDraftService -> adapter receives edited payload.
		draftSvc := &task.ProactiveDraftService{
			Store:       h.store,
			Permissions: h.perms,
			Executor:    exec,
			Now:         h.clock.Now,
		}
		var spawnedReq *task.DigestExecutionRequest
		draftSvc.StartRun = func(_ context.Context, req task.DigestExecutionRequest) (string, error) {
			cp := req
			spawnedReq = &cp
			return "run-digest-exec-dst", nil
		}
		execRes, err := draftSvc.ExecuteApprovedDraft(ctx, orig.ID)
		if err != nil {
			t.Fatalf("ExecuteApprovedDraft: %v", err)
		}
		if execRes.Draft.Status != task.DraftStatusExecuted || spawnedReq == nil {
			t.Fatalf("execRes=%+v spawnedReq=%+v", execRes, spawnedReq)
		}
		invRes, err := draftSvc.RunDigestExecution(ctx, *spawnedReq)
		if err != nil || invRes.Outcome != "confirmed" {
			t.Fatalf("RunDigestExecution = (%+v, %v)", invRes, err)
		}
		if len(emailAdapter.executedArgs) != 1 {
			t.Fatalf("executed calls = %d, want 1", len(emailAdapter.executedArgs))
		}
		got := emailAdapter.executedArgs[0]
		if got["to"] != "alice@example.com" || got["subject"] != "Edited Morning Subject" || got["body"] != "Edited morning body text." {
			t.Fatalf("executed payload = %+v, want edited subject/body and original recipient", got)
		}

		h.checkQuiescence(ctx)
	})
}

// ============================================================================
// Scenario 6: Cost Governor & Unattended Origin Safety
// ============================================================================

func TestProactivityCostGovernorAndUnattendedOriginSafety(t *testing.T) {
	runProactivityModes(t, func(t *testing.T, mode proactivityBackendMode) {
		ctx := context.Background()
		start := time.Date(2026, 5, 4, 17, 0, 0, 0, time.UTC)
		h := newProactivitySimHarness(t, mode, 1008, "America/Los_Angeles", start)
		h.prefs.Proactivity.DailyProactiveBudgetUSD = 1.0 // $1/day cap

		// --- Part A: High-frequency Computer watch returns needs_confirmation; model retry cannot bypass ---
		highFreqEditable := scheduler.Editable{
			Title:       "1-Min Computer Watch",
			Instruction: "Poll local app state on Personal Agent Computer.",
			Trigger: scheduler.Trigger{
				Kind:               "watch",
				Source:             &scheduler.ResourceRef{ConnectionID: "comp-1", Kind: "computer", ProviderID: "screen"},
				PredicateRef:       "app_state_changed",
				MinIntervalSeconds: 60, // 1440 runs/day * $0.05 = $72/day >> $1.00/day cap
			},
			BudgetUSD:      0.05,
			MaxActiveRuns:  1,
			MissedRun:      "skip",
			DestinationIDs: []string{scheduler.ChatDestination},
		}

		rec1, err := h.sched.Save(ctx, "", "req-high-freq-1", 0, highFreqEditable)
		if err != nil {
			t.Fatalf("Save high-freq #1: %v", err)
		}
		if rec1.Status != scheduler.StatusNeedsConfirmation {
			t.Fatalf("high-freq #1 status = %q, want %q", rec1.Status, scheduler.StatusNeedsConfirmation)
		}
		var conf scheduler.BudgetConfirmation
		if err := json.Unmarshal(rec1.Resource, &conf); err != nil {
			t.Fatalf("unmarshal BudgetConfirmation: %v", err)
		}
		if conf.InteractionID == "" || conf.ActionDigest == "" || conf.ProjectedDailyUSD <= conf.DailyCapUSD {
			t.Fatalf("unexpected BudgetConfirmation: %+v", conf)
		}

		// Model retry with a different request ID or unapproved interaction ID CANNOT bypass.
		retryEditable := highFreqEditable
		retryEditable.ConfirmationInteractionID = conf.InteractionID
		recRetry, err := h.sched.Save(ctx, "", "req-high-freq-retry", 0, retryEditable)
		if err != nil {
			t.Fatalf("Save high-freq retry: %v", err)
		}
		if recRetry.Status != scheduler.StatusNeedsConfirmation {
			t.Fatalf("model retry bypassed cost governor! status = %q, want needs_confirmation", recRetry.Status)
		}

		// Real user approval of the Interaction allows Save to proceed.
		inter, err := h.perms.Interaction(ctx, conf.InteractionID)
		if err != nil {
			t.Fatalf("Interaction(%s): %v", conf.InteractionID, err)
		}
		if _, _, err := h.perms.Respond(ctx, inter.ID, "req-approve-budget", inter.Revision, permissions.Response{
			Kind:         "approve",
			ActionDigest: inter.ActionDigest,
		}); err != nil {
			t.Fatalf("Respond approve budget: %v", err)
		}
		recApproved, err := h.sched.Save(ctx, "", "req-high-freq-1", 0, retryEditable)
		if err != nil {
			t.Fatalf("Save after approval: %v", err)
		}
		if recApproved.Status != "applied" {
			t.Fatalf("Save after user approval status = %q, want applied", recApproved.Status)
		}
		watchAuto := mustDecodeAutomation(t, recApproved)

		// --- Part B: Runtime budget_exhausted skips in advance() ---
		h.mu.Lock()
		h.spendFn = func(context.Context, time.Time) (scheduler.ProactiveSpend, error) {
			return scheduler.ProactiveSpend{TaskLLMUSD: 0.75, ComputerWakeUSD: 0.30}, nil // $1.05 >= $1.00
		}
		h.mu.Unlock()

		h.clock.Advance(time.Minute + time.Second)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick under budget_exhausted: %v", err)
		}
		if got := len(h.commandsFor(watchAuto.ID)); got != 0 {
			t.Fatalf("expected 0 tasks when budget_exhausted, got %d", got)
		}
		occs, _ := h.sched.Occurrences(ctx, watchAuto.ID)
		if len(occs) != 1 || occs[0].Status != scheduler.OccurrenceSkipped || occs[0].Reason != "budget_exhausted" {
			t.Fatalf("budget_exhausted occ = %+v, want skipped/budget_exhausted", occs)
		}

		// --- Part C: Unattended automation-origin denies automation_create and forces ReadOnly on Computer ---
		createDef := sealSimDef(t, "nori/assistant/automation-create", "nori/assistant", "automation_create", "nori_automations", "nori.automations/create", extensions.EffectReversibleWrite)
		compDef := sealSimDef(t, "nori/computer/computer-shell", "nori/computer", "computer_shell", "personal_computer", "nori.computer/shell", extensions.EffectReversibleWrite)

		autoStub := &proactStubAdapter{}
		compAdapter := &proactComputerAdapter{}
		adapters := invoke.NewRegistry()
		adapters.Register(extensions.AdapterNative, "nori.automations", autoStub)
		adapters.Register(extensions.AdapterNative, "nori.computer", compAdapter)

		receipts, err := h.store.AcceptCommandBatch(ctx, []state.Command{{
			ID:    "cmd-unattended-safety",
			RunID: "run-unattended-safety",
			Kind:  state.CommandStart,
			Text:  "Try creating an automation and writing on Computer",
			Origin: &state.TaskOrigin{
				ConversationID: task.AutomationConversationID,
				AutomationID:   watchAuto.ID,
			},
		}}, nil)
		if err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}

		var sawCreateInModelTools atomic.Bool
		var sawCreateBlocked atomic.Bool
		out, _ := h.claimAndRunTask(ctx, receipts[0].Run.ID, "worker-safety", func(_ context.Context, callNum int, req agent.Request) (agent.Response, error) {
			if callNum == 1 {
				for _, tool := range req.Tools {
					if tool.Name == "automation_create" {
						sawCreateInModelTools.Store(true)
					}
				}
				return proactToolCallResp("c-forb-create", "automation_create", map[string]any{"title": "Rogue"}, 0.02), nil
			}
			switch callNum {
			case 2:
				for _, m := range req.Messages {
					raw, _ := json.Marshal(m)
					s := string(raw)
					if strings.Contains(s, `"outcome":"denied"`) || strings.Contains(s, `\"outcome\":\"denied\"`) ||
						strings.Contains(s, `"outcome":"failed"`) || strings.Contains(s, `\"outcome\":\"failed\"`) ||
						strings.Contains(s, "not available") || strings.Contains(s, "unknown") {
						sawCreateBlocked.Store(true)
					}
				}
				return proactToolCallResp("c-comp-write", "computer_shell", map[string]any{"command": "touch /tmp/bad"}, 0.02), nil
			default:
				return proactToolCallResp("c-done-safety", "complete", map[string]any{
					"answer": "Blocked.", "evidence": []any{}, "unmet": []any{},
				}, 0.02), nil
			}
		}, []extensions.Definition{createDef, compDef}, adapters)

		if out != task.OutcomeDone {
			t.Fatalf("unattended safety run outcome = %s, want done", out)
		}
		if sawCreateInModelTools.Load() {
			t.Fatalf("automation_create was offered in model tools for automation-origin run; expected upfront filtering")
		}
		if autoStub.execCalls.Load() != 0 || !sawCreateBlocked.Load() {
			t.Fatalf("automation_create was not blocked (execs=%d, sawBlocked=%v)", autoStub.execCalls.Load(), sawCreateBlocked.Load())
		}
		if !compAdapter.sawReadOnly.Load() || compAdapter.execCalls.Load() != 0 {
			t.Fatalf("Computer write was not forced ReadOnly/blocked (sawReadOnly=%v, execs=%d)", compAdapter.sawReadOnly.Load(), compAdapter.execCalls.Load())
		}

		h.checkQuiescence(ctx)
	})
}

// ============================================================================
// Scenario 7: APNs Outbox & Token Privacy
// ============================================================================

func generateSimP8Key(t *testing.T) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestProactivityAPNsOutboxAndTokenPrivacy(t *testing.T) {
	runProactivityModes(t, func(t *testing.T, mode proactivityBackendMode) {
		ctx := context.Background()
		start := time.Date(2026, 5, 4, 17, 0, 0, 0, time.UTC)
		h := newProactivitySimHarness(t, mode, 1009, "America/Los_Angeles", start)

		userA := uuid.NewString()
		userB := uuid.NewString()
		sharedToken := "apns-shared-" + uuid.NewString()

		// 1. Cross-user sign-in reassigns token.
		devA, err := h.pushStore.RegisterDevice(ctx, userA, sharedToken, "iphone-shared", push.EnvSandbox)
		if err != nil {
			t.Fatalf("RegisterDevice userA: %v", err)
		}
		devB, err := h.pushStore.RegisterDevice(ctx, userB, sharedToken, "iphone-shared", push.EnvProduction)
		if err != nil {
			t.Fatalf("RegisterDevice userB: %v", err)
		}
		if devA.ID == devB.ID {
			t.Fatalf("expected reassigned token to get new device ID, got %q", devB.ID)
		}
		devsA, err := h.pushStore.ListDevices(ctx, userA)
		if err != nil || len(devsA) != 0 {
			t.Fatalf("userA devices after reassignment = %+v (err=%v), want 0", devsA, err)
		}
		devsB, err := h.pushStore.ListDevices(ctx, userB)
		if err != nil || len(devsB) != 1 {
			t.Fatalf("userB devices after reassignment = %+v (err=%v), want 1", devsB, err)
		}

		// 2. Retries coalesce via delivery_id / apns-collapse-id.
		h.pushWorker.DailyCap = 3
		delivID := "dlv-coalesce-" + uuid.NewString()
		_, created1, err := h.pushWorker.Enqueue(ctx, push.EnqueueRequest{
			DeliveryID:    delivID,
			UserID:        userB,
			InteractionID: "int-push-1",
			Kind:          push.KindApproval,
		})
		if err != nil || !created1 {
			t.Fatalf("Enqueue #1 = (created=%v, err=%v), want (true, nil)", created1, err)
		}
		_, created2, err := h.pushWorker.Enqueue(ctx, push.EnqueueRequest{
			DeliveryID:    delivID,
			UserID:        userB,
			InteractionID: "int-push-1",
			Kind:          push.KindApproval,
		})
		if err != nil || created2 {
			t.Fatalf("duplicate Enqueue = (created=%v, err=%v), want (false, nil)", created2, err)
		}

		// First attempt hits transient 503 -> stays pending; second attempt succeeds with same CollapseID.
		h.pushSender.NextErr = &push.APNSError{StatusCode: http.StatusServiceUnavailable, Reason: "ServiceUnavailable"}
		stats1, err := h.pushWorker.ProcessPending(ctx, 10)
		if err != nil || stats1.Retried != 1 || stats1.Sent != 0 {
			t.Fatalf("ProcessPending transient 503 = (%+v, %v), want retried=1 sent=0", stats1, err)
		}
		stats2, err := h.pushWorker.ProcessPending(ctx, 10)
		if err != nil || stats2.Sent != 1 {
			t.Fatalf("ProcessPending retry = (%+v, %v), want sent=1", stats2, err)
		}
		sent := h.pushSender.Sent()
		if len(sent) != 1 || sent[0].Notification.CollapseID != delivID {
			t.Fatalf("sent pushes = %+v, want 1 with CollapseID=%q", sent, delivID)
		}

		// 3. Hard daily push cap enforced (DailyCap=3; 1 already sent, so 2 more send and #4/#5 suppress).
		for i := 2; i <= 5; i++ {
			id := fmt.Sprintf("dlv-cap-%d-%s", i, uuid.NewString()[:8])
			if _, _, err := h.pushWorker.Enqueue(ctx, push.EnqueueRequest{
				DeliveryID: id,
				UserID:     userB,
				Kind:       push.KindUrgent,
			}); err != nil {
				t.Fatalf("Enqueue cap %d: %v", i, err)
			}
		}
		capStats, err := h.pushWorker.ProcessPending(ctx, 20)
		if err != nil {
			t.Fatalf("ProcessPending cap: %v", err)
		}
		if capStats.Sent != 2 || capStats.Suppressed != 2 {
			t.Fatalf("capStats = %+v, want Sent=2 Suppressed=2 (total 3/day)", capStats)
		}

		// 4. Sign-out deletes token.
		if err := h.pushStore.DeleteUserDevices(ctx, userB); err != nil {
			t.Fatalf("DeleteUserDevices(userB): %v", err)
		}
		devsB, err = h.pushStore.ListDevices(ctx, userB)
		if err != nil || len(devsB) != 0 {
			t.Fatalf("userB devices after sign-out = %+v (err=%v), want 0", devsB, err)
		}

		// 5. APNs 410 Unregistered and 400 BadDeviceToken delete invalid tokens.
		userC := uuid.NewString()
		tok410 := "apns-410-" + uuid.NewString()
		tok400 := "apns-400-" + uuid.NewString()
		if _, err := h.pushStore.RegisterDevice(ctx, userC, tok410, "iphone-410", push.EnvProduction); err != nil {
			t.Fatalf("RegisterDevice tok410: %v", err)
		}
		if _, err := h.pushStore.RegisterDevice(ctx, userC, tok400, "iphone-400", push.EnvProduction); err != nil {
			t.Fatalf("RegisterDevice tok400: %v", err)
		}
		h.pushSender.TokenErrors[tok410] = &push.APNSError{StatusCode: http.StatusGone, Reason: "Unregistered"}
		h.pushSender.TokenErrors[tok400] = &push.APNSError{StatusCode: http.StatusBadRequest, Reason: "BadDeviceToken"}
		if _, _, err := h.pushWorker.Enqueue(ctx, push.EnqueueRequest{
			DeliveryID: "dlv-invalid-" + uuid.NewString(),
			UserID:     userC,
			Kind:       push.KindBrief,
		}); err != nil {
			t.Fatalf("Enqueue invalid-token test: %v", err)
		}
		invStats, err := h.pushWorker.ProcessPending(ctx, 10)
		if err != nil || invStats.Suppressed != 1 {
			t.Fatalf("invalid-token ProcessPending = (%+v, %v), want Suppressed=1", invStats, err)
		}
		devsC, err := h.pushStore.ListDevices(ctx, userC)
		if err != nil || len(devsC) != 0 {
			t.Fatalf("userC devices after 410+400 = %+v (err=%v), want 0 (both deleted)", devsC, err)
		}

		// 6. Lock-screen payload contains zero body content (verify via real APNSClient over HTTP/2 test server).
		var gotCollapseID string
		var gotBody string
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotCollapseID = r.Header.Get("apns-collapse-id")
			var raw map[string]any
			_ = json.NewDecoder(r.Body).Decode(&raw)
			b, _ := json.Marshal(raw)
			gotBody = string(b)
			w.WriteHeader(http.StatusOK)
		}))
		srv.EnableHTTP2 = true
		srv.StartTLS()
		defer srv.Close()

		apnsClient, err := push.NewAPNSClient(push.APNSConfig{
			KeyID:         "SIMKEY1234",
			TeamID:        "SIMTEAM123",
			BundleID:      "com.nori.ios",
			PrivateKeyPEM: generateSimP8Key(t),
			SandboxURL:    srv.URL,
			ProductionURL: srv.URL,
			HTTPClient:    srv.Client(),
			Now:           h.clock.Now,
		})
		if err != nil {
			t.Fatalf("NewAPNSClient: %v", err)
		}
		secretEmailBody := "CONFIDENTIAL wire transfer $250,000 to account 998877"
		if err := apnsClient.Send(ctx, push.Device{
			ID: "d-priv", UserID: userA, Token: "tok-priv", Environment: push.EnvProduction,
		}, push.Notification{
			DeliveryID:    "dlv-priv-1",
			InteractionID: "int-priv-1",
			Title:         "CEO Secret Subject",
			Body:          secretEmailBody,
		}); err != nil {
			t.Fatalf("APNSClient.Send: %v", err)
		}
		if gotCollapseID != "dlv-priv-1" {
			t.Fatalf("apns-collapse-id = %q, want dlv-priv-1", gotCollapseID)
		}
		if strings.Contains(gotBody, "CONFIDENTIAL") || strings.Contains(gotBody, "250,000") || strings.Contains(gotBody, "CEO Secret") {
			t.Fatalf("lock-screen APNs payload leaked sensitive body content: %s", gotBody)
		}
		if !strings.Contains(gotBody, `"title":"Nori"`) || !strings.Contains(gotBody, `"body":"Ready to review"`) {
			t.Fatalf("expected generic sanitized lock-screen copy, got %s", gotBody)
		}
	})
}

// ============================================================================
// Scenario 8: Day 1 Brief, Timezone Travel & Abandoned Account Decay
// ============================================================================

func TestProactivityDay1BriefTravelAndAbandonedDecay(t *testing.T) {
	runProactivityModes(t, func(t *testing.T, mode proactivityBackendMode) {
		ctx := context.Background()
		la, _ := time.LoadLocation("America/Los_Angeles")
		tokyo, _ := time.LoadLocation("Asia/Tokyo")

		// Start Nov 10, 2026 07:00 PST (15:00Z).
		start := time.Date(2026, 11, 10, 7, 0, 0, 0, la).UTC()
		h := newProactivitySimHarness(t, mode, 1010, "America/Los_Angeles", start)
		h.prefs.Proactivity.DailyProactiveBudgetUSD = 10.0
		h.sched.InactivityPauseDays = 14
		h.lastActive = start

		// 1. Day 1 provisioning via proactivity.EnsureMorningBrief is idempotent.
		day1Res1, err := proactivity.EnsureMorningBrief(ctx, h.sched, proactivity.MorningBriefOptions{
			Timezone:       "America/Los_Angeles",
			MorningBriefAt: "08:00",
			Now:            start,
		})
		if err != nil || !day1Res1.Created || day1Res1.AutomationID != scheduler.MorningBriefAutomationID {
			t.Fatalf("Day 1 EnsureMorningBrief #1 = (%+v, %v), want Created=true", day1Res1, err)
		}
		day1Res2, err := proactivity.EnsureMorningBrief(ctx, h.sched, proactivity.MorningBriefOptions{
			Timezone:       "America/Los_Angeles",
			MorningBriefAt: "08:00",
			Now:            start,
		})
		if err != nil || day1Res2.Created {
			t.Fatalf("Day 1 EnsureMorningBrief #2 = (%+v, %v), want Created=false", day1Res2, err)
		}

		// Sync with scheduler.EnsureMorningBrief to enable FollowUserTimezone=true and ExecutionWindowSeconds=7200.
		briefAuto, err := h.sched.EnsureMorningBrief(ctx, h.prefs)
		if err != nil {
			t.Fatalf("sched.EnsureMorningBrief: %v", err)
		}
		if !briefAuto.Trigger.FollowUserTimezone || briefAuto.ExecutionWindowSeconds != scheduler.DefaultMorningBriefExecutionWindowSeconds {
			t.Fatalf("briefAuto = %+v, want FollowUserTimezone=true and ExecutionWindowSeconds=7200", briefAuto)
		}
		wantLA8AM := time.Date(2026, 11, 10, 8, 0, 0, 0, la).UTC()
		if briefAuto.NextAt == nil || !briefAuto.NextAt.Equal(wantLA8AM) {
			t.Fatalf("initial NextAt = %v, want %s (08:00 PST)", briefAuto.NextAt, wantLA8AM)
		}

		// Fire Day 1 brief at 08:00 PST.
		h.clock.Set(wantLA8AM.Add(time.Second))
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick Day 1 LA brief: %v", err)
		}
		cmds := h.commandsFor(scheduler.MorningBriefAutomationID)
		if len(cmds) != 1 {
			t.Fatalf("Day 1 LA brief commands = %d, want 1", len(cmds))
		}
		h.completeRun(ctx, cmds[0].RunID, `{"outcome":"update","greeting":"Good morning LA"}`)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick Day 1 LA settle: %v", err)
		}

		// 2. User travels to Asia/Tokyo -> Preferences.Timezone changes, auto_morning_brief follows!
		h.mu.Lock()
		h.prefs.Timezone = "Asia/Tokyo"
		h.lastActive = h.clock.Now()
		h.mu.Unlock()
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick after travel to Tokyo: %v", err)
		}
		briefAfterTravel, err := h.sched.Get(ctx, scheduler.MorningBriefAutomationID)
		if err != nil {
			t.Fatalf("Get brief after travel: %v", err)
		}
		// Current time is 2026-11-10 16:00:01Z = 2026-11-11 01:00:01 JST, so next 08:00 JST is 2026-11-11 08:00 JST (2026-11-10 23:00Z).
		wantTokyo8AM := time.Date(2026, 11, 11, 8, 0, 0, 0, tokyo).UTC()
		if briefAfterTravel.NextAt == nil || !briefAfterTravel.NextAt.Equal(wantTokyo8AM) {
			t.Fatalf("after Tokyo travel NextAt = %v, want %s (08:00 JST)", briefAfterTravel.NextAt, wantTokyo8AM)
		}

		// Fire at 08:00 JST.
		h.clock.Set(wantTokyo8AM.Add(time.Second))
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick Tokyo 08:00 JST: %v", err)
		}
		cmds = h.commandsFor(scheduler.MorningBriefAutomationID)
		if len(cmds) != 2 {
			t.Fatalf("Tokyo brief commands = %d, want 2", len(cmds))
		}
		h.completeRun(ctx, cmds[1].RunID, `{"outcome":"update","greeting":"Ohayo Tokyo"}`)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick Tokyo settle: %v", err)
		}

		// 3. Skip past ExecutionWindowSeconds (7200s = 2h): jump to 10:05 JST next day -> skipped/missed_window.
		h.mu.Lock()
		h.lastActive = time.Date(2026, 11, 12, 0, 0, 0, 0, tokyo).UTC()
		h.mu.Unlock()
		lateTokyo := time.Date(2026, 11, 12, 10, 5, 0, 0, tokyo).UTC()
		h.clock.Set(lateTokyo)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick late past ExecutionWindowSeconds: %v", err)
		}
		if got := len(h.commandsFor(scheduler.MorningBriefAutomationID)); got != 2 {
			t.Fatalf("late brief past ExecutionWindowSeconds should skip, got %d commands (want 2)", got)
		}
		occs, _ := h.sched.Occurrences(ctx, scheduler.MorningBriefAutomationID)
		slices.SortFunc(occs, func(a, b scheduler.Occurrence) int { return a.ScheduledAt.Compare(b.ScheduledAt) })
		if len(occs) != 3 || occs[2].Status != scheduler.OccurrenceSkipped || occs[2].Reason != "missed_window" {
			t.Fatalf("late brief occ = %+v, want skipped/missed_window", occs[len(occs)-1])
		}

		// 4. Abandoned account decay: 15 days after LastActivity -> auto-pauses with paused_reason="inactive_account".
		h.clock.Set(h.lastActive.Add(15 * 24 * time.Hour))
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick abandoned account: %v", err)
		}
		pausedBrief, err := h.sched.Get(ctx, scheduler.MorningBriefAutomationID)
		if err != nil {
			t.Fatalf("Get paused brief: %v", err)
		}
		if pausedBrief.Status != scheduler.AutomationPaused || pausedBrief.PausedReason != "inactive_account" || pausedBrief.NextAt != nil {
			t.Fatalf("abandoned brief = (status=%s, reason=%q, next=%v), want paused/inactive_account/nil", pausedBrief.Status, pausedBrief.PausedReason, pausedBrief.NextAt)
		}
		if got := len(h.commandsFor(scheduler.MorningBriefAutomationID)); got != 2 {
			t.Fatalf("abandoned account fired extra commands: got %d, want 2", got)
		}

		h.checkQuiescence(ctx)
	})
}

// ============================================================================
// Scenario 9: Multi-Instance Contention & Crash-Restart Recovery
// ============================================================================

func TestProactivityMultiInstanceContentionAndCrashRestart(t *testing.T) {
	runProactivityModes(t, func(t *testing.T, mode proactivityBackendMode) {
		ctx := context.Background()
		start := time.Date(2026, 5, 4, 17, 0, 0, 0, time.UTC)
		h := newProactivitySimHarness(t, mode, 1011, "America/Los_Angeles", start)
		h.prefs.Proactivity.DailyProactiveBudgetUSD = 100.0

		receipt, err := h.sched.Save(ctx, "", "req-contention-watch", 0, scheduler.Editable{
			Title:       "Contention Watch",
			Instruction: "Check inbox and report any new urgent items.",
			Trigger: scheduler.Trigger{
				Kind:               "watch",
				Source:             &scheduler.ResourceRef{ConnectionID: "conn-gmail", Kind: "gmail", ProviderID: "inbox"},
				PredicateRef:       "urgent_items",
				MinIntervalSeconds: 300,
			},
			BudgetUSD:      0.10,
			MaxActiveRuns:  1,
			MissedRun:      "skip",
			DestinationIDs: []string{scheduler.ChatDestination},
		})
		if err != nil {
			t.Fatalf("Save contention watch: %v", err)
		}
		auto := mustDecodeAutomation(t, receipt)

		// Part A: Two scheduler replicas tick concurrently for the same due slot.
		schedReplica := *h.sched
		h.clock.Advance(5*time.Minute + time.Second)

		barrier := make(chan struct{})
		var wg sync.WaitGroup
		errs := make([]error, 2)
		for i, svc := range []*scheduler.Service{h.sched, &schedReplica} {
			wg.Add(1)
			go func(idx int, s *scheduler.Service) {
				defer wg.Done()
				<-barrier
				_, errs[idx] = s.Tick(ctx)
			}(i, svc)
		}
		close(barrier)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("replica %d Tick error: %v", i, err)
			}
		}

		occs, err := h.sched.Occurrences(ctx, auto.ID)
		if err != nil {
			t.Fatalf("Occurrences after concurrent tick: %v", err)
		}
		if len(occs) != 1 {
			t.Fatalf("occurrences after concurrent tick = %d, want 1", len(occs))
		}
		cmds := h.commandsFor(auto.ID)
		if len(cmds) != 1 {
			t.Fatalf("accepted commands after concurrent tick = %d, want 1", len(cmds))
		}
		runID := cmds[0].RunID

		// Part B: Worker-1 runs through tool execution and exits mid-run (crash/drain handoff);
		// worker-2 takes over the fenced lease and completes the run with zero duplicate side effects.
		readDef := sealSimDef(t, "google/mail/mail-list", "google/mail", "mail_list", "gmail", "google.mail/list", extensions.EffectRead)
		mailStub := &proactStubAdapter{effect: extensions.EffectRead}
		adapters := invoke.NewRegistry()
		adapters.Register(extensions.AdapterNative, "google.mail", mailStub)

		leases, ok := any(h.backend).(docstore.LeaseStore)
		if !ok {
			t.Fatalf("backend %T does not implement docstore.LeaseStore", h.backend)
		}

		var totalModelCalls atomic.Int32
		sharedModelFn := func(_ context.Context, _ int, _ agent.Request) (agent.Response, error) {
			n := totalModelCalls.Add(1)
			switch n {
			case 1:
				return proactToolCallResp("c-list-1", "mail_list", map[string]any{}, 0.01), nil
			default:
				return proactToolCallResp("c-done-1", "complete", map[string]any{
					"answer":   `{"outcome":"alert","summary":"Recovered alert after worker handoff","finding_keys":["crash:1"]}`,
					"evidence": []any{},
					"unmet":    []any{},
				}, 0.01), nil
			}
		}

		// Worker-1 executes the first 3 steps (SyncHistory -> Model -> mail_list) and then stops mid-run.
		claim1, claimed1, err := leases.ClaimTaskByID(ctx, h.user.ID, runID, "worker-crash-1", state.TaskLease)
		if err != nil || !claimed1 {
			t.Fatalf("ClaimTaskByID worker-1 = (%v, %v), want claimed", claimed1, err)
		}
		crashAgent := agent.New(h.store, &proactScriptedModel{fn: sharedModelFn})
		crashAgent.Now = h.clock.Now
		crashAgent.Extensions = &proactExtSource{defs: []extensions.Definition{readDef}}
		crashAgent.Capabilities = agent.Capabilities{Location: time.UTC}
		noopTicker := func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} }
		sleepFn := func(ctx context.Context, d time.Duration) error {
			if d > 0 {
				h.clock.Advance(d)
			}
			return ctx.Err()
		}
		worker1 := &task.Runner{
			Activities: &task.Activities{
				Agent:     crashAgent,
				Store:     h.store,
				Executor:  &task.Executor{Adapters: adapters, Permissions: h.perms, Now: h.clock.Now, Sleep: sleepFn},
				Now:       h.clock.Now,
				Sleep:     sleepFn,
				NewTicker: noopTicker,
			},
			Leases:     leases,
			UserID:     h.user.ID,
			Owner:      "worker-crash-1",
			Now:        h.clock.Now,
			NewTicker:  noopTicker,
			StepBudget: 3,
		}
		out1, err := worker1.Run(ctx, claim1)
		if err != nil || out1 != task.OutcomeReleased {
			t.Fatalf("worker-1 mid-run handoff = (%s, %v), want (released, nil)", out1, err)
		}
		ctrlAfterW1, err := h.store.GetTaskControl(ctx, runID)
		if err != nil {
			t.Fatalf("GetTaskControl after worker-1: %v", err)
		}

		// Worker-2 takes over and completes the remaining steps.
		outcome, _ := h.claimAndRunTask(ctx, runID, "worker-recovery-2", sharedModelFn, []extensions.Definition{readDef}, adapters)
		if outcome != task.OutcomeDone {
			t.Fatalf("recovery worker outcome = %s, want done", outcome)
		}
		ctrlAfterW2, err := h.store.GetTaskControl(ctx, runID)
		if err != nil {
			t.Fatalf("GetTaskControl after worker-2: %v", err)
		}
		if ctrlAfterW2.Fence <= ctrlAfterW1.Fence {
			t.Fatalf("expected Fence incremented on worker-2 takeover: %d -> %d", ctrlAfterW1.Fence, ctrlAfterW2.Fence)
		}
		if got := mailStub.execCalls.Load(); got != 1 {
			t.Fatalf("mail_list exec calls = %d, want 1 (zero duplicate side effects)", got)
		}
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick settle after crash recovery: %v", err)
		}
		occs, err = h.sched.Occurrences(ctx, auto.ID)
		if err != nil || len(occs) != 1 || occs[0].Status != scheduler.OccurrenceCompleted {
			t.Fatalf("settled occurrence after recovery = %+v (err=%v), want 1 completed", occs, err)
		}

		// Part C: Concurrent OutboxWorker contention settles the delivery cleanly.
		if _, err := h.pushStore.RegisterDevice(ctx, h.user.ID, "apns-contend-"+uuid.NewString(), "iphone-contend", push.EnvProduction); err != nil {
			t.Fatalf("RegisterDevice contention: %v", err)
		}
		delivID := "dlv-contend-" + uuid.NewString()
		if _, created, err := h.pushWorker.Enqueue(ctx, push.EnqueueRequest{
			DeliveryID: delivID,
			UserID:     h.user.ID,
			Kind:       push.KindUrgent,
		}); err != nil || !created {
			t.Fatalf("Enqueue contention = (created=%v, err=%v), want (true, nil)", created, err)
		}
		worker2 := &push.OutboxWorker{
			Store:  h.pushStore,
			Sender: h.pushSender,
			Now:    h.clock.Now,
		}
		pushBarrier := make(chan struct{})
		var pushWG sync.WaitGroup
		pushErrs := make([]error, 2)
		for i, w := range []*push.OutboxWorker{h.pushWorker, worker2} {
			pushWG.Add(1)
			go func(idx int, worker *push.OutboxWorker) {
				defer pushWG.Done()
				<-pushBarrier
				_, pushErrs[idx] = worker.ProcessPending(ctx, 10)
			}(i, w)
		}
		close(pushBarrier)
		pushWG.Wait()
		for i, err := range pushErrs {
			if err != nil {
				t.Fatalf("push worker %d ProcessPending error: %v", i, err)
			}
		}
		item, err := h.pushStore.GetOutbox(ctx, delivID)
		if err != nil || item.Status != push.StatusSent {
			t.Fatalf("outbox item after concurrent workers = (%+v, %v), want StatusSent", item, err)
		}

		h.checkQuiescence(ctx)
	})
}
