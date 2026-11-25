package inner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"nori/internal/agent"
	"nori/internal/contextview"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/sim/env"
	"nori/internal/sim/inner"
	"nori/internal/sim/invariants"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/task"
	"nori/internal/transcript"
	"nori/internal/users"
)

const (
	paidToolID   = "sim/tools/paid_read"
	paidToolName = "sim_paid_read"
)

type paidReadAdapter struct {
	mu       sync.Mutex
	costUSD  float64
	executed []string
}

func (a *paidReadAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
	key := invoke.String(call.Args, "key")
	if key == "" {
		return invoke.Prepared{}, fmt.Errorf("%w: key is required", invoke.ErrInvalidArguments)
	}
	spec := map[string]string{"key": key}
	return invoke.Prepared{
		Summary: fmt.Sprintf("Paid read %q", key),
		Effect:  extensions.EffectRead,
		Spec:    spec,
		Keys:    map[string]string{"action_key": invoke.ActionKey(call, spec)},
	}, nil
}

func (a *paidReadAdapter) Execute(_ context.Context, call invoke.Call, prepared invoke.Prepared) invoke.Result {
	a.mu.Lock()
	a.executed = append(a.executed, prepared.Spec["key"])
	cost := a.costUSD
	a.mu.Unlock()
	return invoke.Result{
		Outcome: "confirmed",
		Content: map[string]any{"key": prepared.Spec["key"], "value": "paid-val", "run_id": call.RunID},
		CostUSD: cost,
	}
}

func (a *paidReadAdapter) ExecutedKeys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.executed))
	copy(out, a.executed)
	return out
}

func buildPaidReadDef() extensions.Definition {
	def, err := extensions.Seal(extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: paidToolID, Version: "1"},
		Extension:       extensions.DefinitionRef{ID: inner.ExtensionID, Version: "1"},
		ModelName:       paidToolName,
		Title:           "Sim Paid Read",
		Description:     "Paid read tool for budget simulation.",
		Capability:      "sim",
		CapabilityTitle: "Simulated tools",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"key": {"type": "string", "minLength": 1}},
			"required": ["key"],
			"additionalProperties": false
		}`),
		Binding: extensions.Binding{
			Adapter: extensions.AdapterNative,
			Target:  "simpaid/read",
			Version: "1",
		},
		Policy: extensions.Policy{
			Effect:    extensions.EffectRead,
			Retry:     "safe",
			TimeoutMS: 30_000,
			Location:  extensions.LocationRuntime,
		},
		Exposure: extensions.ExposureDirect,
	})
	if err != nil {
		panic(err)
	}
	return def
}

type wfHookStore struct {
	state.TaskStore
	mu              sync.Mutex
	onAfterProject  func(ctx context.Context, req state.ProjectRequest, err error)
	onAfterGuard    func(ctx context.Context, runID string, rev int64, err error)
	onAfterAdvance  func(ctx context.Context, ctrl state.TaskControl, err error)
	onBeforePublish func(ctx context.Context, req state.PublishRequest)
	onAfterPublish  func(ctx context.Context, req state.PublishRequest, err error)
}

func (s *wfHookStore) Project(ctx context.Context, req state.ProjectRequest) error {
	err := s.TaskStore.Project(ctx, req)
	s.mu.Lock()
	fn := s.onAfterProject
	s.mu.Unlock()
	if fn != nil {
		fn(ctx, req, err)
	}
	return err
}

func (s *wfHookStore) Guard(ctx context.Context, runID string, revision int64) (state.Run, error) {
	run, err := s.TaskStore.Guard(ctx, runID, revision)
	s.mu.Lock()
	fn := s.onAfterGuard
	s.mu.Unlock()
	if fn != nil {
		fn(ctx, runID, revision, err)
	}
	return run, err
}

func (s *wfHookStore) AdvanceTaskStep(ctx context.Context, f state.TaskFence, ctrl state.TaskControl, spend *state.SpendUpdate) (state.TaskControl, state.Run, error) {
	saved, run, err := s.TaskStore.AdvanceTaskStep(ctx, f, ctrl, spend)
	s.mu.Lock()
	fn := s.onAfterAdvance
	s.mu.Unlock()
	if fn != nil {
		fn(ctx, saved, err)
	}
	return saved, run, err
}

func (s *wfHookStore) Publish(ctx context.Context, req state.PublishRequest) (state.Run, error) {
	s.mu.Lock()
	before := s.onBeforePublish
	after := s.onAfterPublish
	s.mu.Unlock()
	if before != nil {
		before(ctx, req)
	}
	run, err := s.TaskStore.Publish(ctx, req)
	if after != nil {
		after(ctx, req, err)
	}
	return run, err
}

type wfHarnessOpts struct {
	budgetUSD   float64
	costPerCall float64
	userSends   string
	paidCostUSD float64
	userID      string
	modelCfg    inner.ModelConfig
	adapterCfg  inner.AdapterConfig
}

type wfHarness struct {
	t          *testing.T
	seed       int64
	rng        *rand.Rand
	clock      *env.Clock
	ids        *env.IDGen
	faults     *memstore.FaultInjector
	backend    *memstore.Backend
	artifacts  *memstore.Artifacts
	usersDB    *records.Users
	wakes      *inner.WakeHub
	user       users.User
	db         *records.Database
	store      state.ConversationStore
	hookStore  *wfHookStore
	perms      *permissions.Service
	adapter    *inner.FakeAdapter
	paid       *paidReadAdapter
	model      *inner.FakeInnerModel
	extSource  *inner.ExtensionSource
	adapters   *invoke.Registry
	executor   *task.Executor
	agentSvc   *agent.Service
	activities *task.Activities
	checker    *invariants.Checker

	mu        sync.Mutex
	reqSeq    int
	workerSeq int
}

func newWFHarness(t *testing.T, seed int64, opts wfHarnessOpts) *wfHarness {
	t.Helper()
	rootRNG := rand.New(rand.NewPCG(uint64(seed), uint64(seed)^0x9e3779b97f4a7c15))
	derive := func() *rand.Rand {
		return rand.New(rand.NewPCG(rootRNG.Uint64(), rootRNG.Uint64()))
	}
	if opts.budgetUSD <= 0 {
		opts.budgetUSD = 1.0
	}
	if opts.userSends == "" {
		opts.userSends = "ask"
	}
	modelCfg := opts.modelCfg
	if modelCfg == (inner.ModelConfig{}) {
		modelCfg = inner.ModelConfig{
			CostPerCall:            0.15,
			MaxStepsBeforeComplete: 4,
		}
	}
	if opts.costPerCall > 0 {
		modelCfg.CostPerCall = opts.costPerCall
	}

	clock := env.NewClock(env.DefaultBaseTime)
	ids := env.NewIDGen(derive())
	faultInj := memstore.NewFaultInjector(memstore.FaultConfig{}, derive())
	faultInj.SetEnabled(false)
	backend := memstore.New(memstore.WithClock(clock.Now), memstore.WithFaultInjector(faultInj))
	artifacts := memstore.NewArtifacts(memstore.WithArtifactsFaultInjector(faultInj))
	usersDB := records.NewSharedUsers(backend)
	wakes := inner.NewWakeHub()

	uid := opts.userID
	if uid == "" {
		uid = ids.Next()
	}
	u := users.User{
		ID:          uid,
		Email:       "sim-wf@example.com",
		DisplayName: "Sim Runner User",
		TimeZone:    "UTC",
	}
	db := records.NewShared(backend, u, opts.budgetUSD)
	db.Now = clock.Now
	store := state.Compose(db, artifacts)
	hookStore := &wfHookStore{TaskStore: store}
	perms := &permissions.Service{
		Store:            store,
		UnattendedWrites: "confirm_writes",
		UserSends:        opts.userSends,
		Now:              clock.Now,
	}

	adapter := inner.NewFakeAdapter(clock, opts.adapterCfg, derive())
	adapter.SetFaultsEnabled(false)

	defs := append([]extensions.Definition{}, inner.ToolDefinitions()...)
	adapters := invoke.NewRegistry()
	adapter.Register(adapters)
	var paid *paidReadAdapter
	if opts.paidCostUSD > 0 {
		paid = &paidReadAdapter{costUSD: opts.paidCostUSD}
		adapters.Register(extensions.AdapterNative, "simpaid", paid)
		defs = append(defs, buildPaidReadDef())
	}
	extSource := &inner.ExtensionSource{Definitions: defs}

	h := &wfHarness{
		t:         t,
		seed:      seed,
		rng:       derive(),
		clock:     clock,
		ids:       ids,
		faults:    faultInj,
		backend:   backend,
		artifacts: artifacts,
		usersDB:   usersDB,
		wakes:     wakes,
		user:      u,
		db:        db,
		store:     store,
		hookStore: hookStore,
		perms:     perms,
		adapter:   adapter,
		paid:      paid,
		extSource: extSource,
		adapters:  adapters,
	}

	h.model = inner.NewFakeInnerModel(clock, modelCfg, derive(), h.readFamilySpend)
	h.model.SetFaultsEnabled(false)

	sleepFn := func(ctx context.Context, dur time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if dur > 0 {
			clock.Advance(dur)
		}
		return ctx.Err()
	}
	tickerFn := func(_ time.Duration) (<-chan time.Time, func()) {
		return make(chan time.Time), func() {}
	}

	agentSvc := agent.New(store, h.model)
	agentSvc.UserFacts = "Simulated runner user"
	agentSvc.Extensions = extSource
	agentSvc.Now = clock.Now
	agentSvc.Capabilities = agent.Capabilities{Location: time.UTC}

	exec := &task.Executor{
		Adapters:    adapters,
		Permissions: perms,
		Now:         clock.Now,
		Sleep:       sleepFn,
	}
	activities := &task.Activities{
		Agent:     agentSvc,
		Store:     hookStore,
		Executor:  exec,
		UserID:    u.ID,
		Wakes:     wakes,
		Now:       clock.Now,
		Sleep:     sleepFn,
		NewTicker: tickerFn,
	}

	h.executor = exec
	h.agentSvc = agentSvc
	h.activities = activities
	h.checker = invariants.New(invariants.Config{
		Backend:     backend,
		Faults:      faultInj,
		Now:         clock.Now,
		CheckLeases: true,
	})
	return h
}

func (h *wfHarness) readFamilySpend(ctx context.Context, _, runID string) (inner.SpendSnapshot, bool) {
	run, err := h.store.GetRun(ctx, runID)
	if err != nil {
		return inner.SpendSnapshot{}, false
	}
	var parent *state.Run
	if run.ParentID != "" {
		owner, err := h.store.GetRun(ctx, run.ParentID)
		if err == nil {
			parent = &owner
		}
	}
	others, budget, _ := state.FamilyCost(run, parent)
	return inner.SpendSnapshot{
		Revision:     run.Revision,
		SpentBefore:  run.Spent + others,
		OthersBefore: others,
		Budget:       budget,
	}, true
}

func (h *wfHarness) nextReqID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reqSeq++
	return fmt.Sprintf("wf-req-%d", h.reqSeq)
}

func (h *wfHarness) nextOwner(prefix string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.workerSeq++
	return fmt.Sprintf("%s-%d", prefix, h.workerSeq)
}

func (h *wfHarness) userTargets() []invariants.UserTarget {
	return []invariants.UserTarget{
		{
			User:        h.user,
			Store:       h.store,
			Permissions: h.perms,
		},
	}
}

func (h *wfHarness) acceptCommands(ctx context.Context, cmds ...state.Command) []state.CommandReceipt {
	h.t.Helper()
	receipts, err := h.store.AcceptCommandBatch(ctx, cmds, nil)
	if err != nil {
		h.t.Fatalf("AcceptCommandBatch: %v", err)
	}
	if err := h.checker.RecordCommandReceipts(h.user.ID, receipts); err != nil {
		h.t.Fatalf("RecordCommandReceipts: %v", err)
	}
	return receipts
}

func (h *wfHarness) startTaskRecord(ctx context.Context, text string) state.Run {
	h.t.Helper()
	runID := h.ids.Next()
	receipts := h.acceptCommands(ctx, state.Command{
		ID:    h.ids.Next(),
		RunID: runID,
		Kind:  state.CommandStart,
		Text:  text,
	})
	return receipts[0].Run
}

func (h *wfHarness) setPaused(ctx context.Context, paused bool) error {
	return h.usersDB.SetPaused(ctx, paused)
}

func (h *wfHarness) runTask(ctx context.Context, runID string, owner string, drain <-chan struct{}) (task.RunOutcome, error) {
	h.t.Helper()
	if owner == "" {
		owner = h.nextOwner("wf-worker")
	}
	claim, ok, err := h.backend.ClaimTaskByID(ctx, h.user.ID, runID, owner, state.TaskLease)
	if err != nil {
		h.t.Fatalf("ClaimTaskByID(%s): %v", runID, err)
	}
	if !ok {
		h.t.Fatalf("ClaimTaskByID(%s): task lease was not claimable", runID)
	}
	runner := &task.Runner{
		Activities: h.activities,
		Leases:     h.backend,
		UserID:     h.user.ID,
		Owner:      owner,
		Wakes:      h.wakes,
		Drain:      drain,
		Now:        h.clock.Now,
		NewTicker: func(time.Duration) (<-chan time.Time, func()) {
			return make(chan time.Time), func() {}
		},
	}
	return runner.Run(users.WithID(ctx, h.user.ID), claim)
}

func (h *wfHarness) routeOneConversationTurn(ctx context.Context, reply string) bool {
	owner := "wf-router-" + h.ids.Next()
	turnID := h.ids.Next()
	ctrl, turn, err := h.store.AcquireConversation(ctx, owner, turnID)
	if err != nil || turn == nil {
		return false
	}
	fence := state.ConversationFence{
		Owner:       ctrl.Owner,
		Fence:       ctrl.Fence,
		TurnID:      turn.ID,
		Through:     turn.Through,
		UserThrough: turn.UserThrough,
		Frozen:      turn.Frozen,
	}
	if turn.Attempt.ID == "" {
		turn.Attempt.ID = fmt.Sprintf("%s-att-%d", turn.ID, turn.Restarts+1)
	}
	turn.Attempt.TurnID = turn.ID
	turn.Attempt.Number = turn.Restarts + 1
	turn.Attempt.Status = "completed"
	if turn.Attempt.StartedAt.IsZero() {
		turn.Attempt.StartedAt = turn.StartedAt
	}
	fin := h.clock.Now().UTC()
	turn.Attempt.FinishedAt = &fin
	return h.store.PublishConversation(ctx, fence, *turn, reply) == nil
}

func (h *wfHarness) drainUserWakes(ctx context.Context) {
	for range 16 {
		claim, ok, err := h.backend.ClaimUserWake(ctx, docstore.LaneConversation, "wf-conv-drain", 30*time.Second)
		if err != nil || !ok {
			break
		}
		for h.routeOneConversationTurn(ctx, "Drain routed.") {
		}
		parked, _ := h.backend.ParkUserWake(ctx, claim.UserID, claim.Lane, "wf-conv-drain", claim.WakeSeq)
		if !parked {
			_ = h.backend.ReleaseUserWake(ctx, claim.UserID, claim.Lane, "wf-conv-drain", claim.WakeSeq, time.Time{})
		}
	}
}

func (h *wfHarness) checkSafetyAndQuiescence(ctx context.Context) {
	h.t.Helper()
	h.drainUserWakes(ctx)
	targets := h.userTargets()
	if err := h.checker.CheckSafety(ctx, targets); err != nil {
		h.t.Fatalf("CheckSafety: %v", err)
	}
	if err := h.checker.CheckQuiescence(ctx, targets, h.clock.Now()); err != nil {
		h.t.Fatalf("CheckQuiescence: %v", err)
	}
}

func scriptedMultiCalls(calls []transcript.ToolCall, cost float64) agent.Response {
	parts := make([]transcript.Part, 0, len(calls))
	for i := range calls {
		c := calls[i]
		parts = append(parts, transcript.Part{ToolCall: &c})
	}
	msg := transcript.Message{
		Role:  transcript.RoleModel,
		Parts: parts,
	}
	return agent.Response{
		Message:   &msg,
		Calls:     calls,
		Usage:     &agent.Usage{InputTokens: 120, OutputTokens: 40, TotalTokens: 160},
		Cost:      cost,
		CostKnown: true,
	}
}

func readTaskTranscript(t *testing.T, ctx context.Context, store state.ConversationStore, runID, ref string) []transcript.Message {
	t.Helper()
	if ref == "" {
		return nil
	}
	blocks, err := store.ReadHistory(ctx, runID, ref)
	if err != nil {
		t.Fatalf("ReadHistory(%s): %v", runID, err)
	}
	var out []transcript.Message
	for _, block := range blocks {
		var chunk []transcript.Message
		if err := json.Unmarshal(block, &chunk); err != nil {
			t.Fatalf("unmarshal history block: %v", err)
		}
		out = append(out, chunk...)
	}
	return out
}

func TestWorkflowSimNormalReadApprovalWriteComplete(t *testing.T) {
	ctx := context.Background()
	h := newWFHarness(t, 2001, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})

	turn := 0
	var writeOpID string
	h.model.SetScript(func(_ context.Context, _ string, req agent.Request) (agent.Response, error, bool) {
		turn++
		switch turn {
		case 1:
			return scriptedCall("wfc-1", inner.ToolNameRead, map[string]any{"key": "key-1"}, 0.10), nil, true
		case 2:
			return scriptedCall("wfc-2", inner.ToolNameWrite, map[string]any{"target": "key-1", "value": "approved-v1", "resource_id": "key-1"}, 0.10), nil, true
		case 3:
			return scriptedCall("wfc-3", inner.ToolNameWrite, map[string]any{"target": "key-1", "value": "approved-v1", "resource_id": "key-1"}, 0.10), nil, true
		default:
			var ev []any
			for _, m := range req.Messages {
				for _, p := range m.Parts {
					if p.ToolResult != nil {
						if outcome, _ := p.ToolResult.Output["outcome"].(string); outcome == "confirmed" {
							if opID, _ := p.ToolResult.Output["operation_id"].(string); opID != "" {
								ev = append(ev, opID)
								if p.ToolResult.Name == inner.ToolNameWrite {
									writeOpID = opID
								}
							}
						}
					}
				}
			}
			return scriptedCall("wfc-4", "complete", map[string]any{
				"answer":   "Read and approved write finished.",
				"evidence": ev,
				"unmet":    []any{},
			}, 0.10), nil, true
		}
	})

	run := h.startTaskRecord(ctx, "Read key-1 then write approved-v1")

	// Leg 1: executes read, requests write approval, parks on WaitQuestion.
	outcome, err := h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("expected OutcomeParked waiting for approval, got outcome=%s err=%v", outcome, err)
	}

	list, _, err := h.perms.Interactions(ctx, "pending", run.ID, "", 20)
	if err != nil || len(list) != 1 {
		t.Fatalf("expected 1 pending approval interaction, got %d (err=%v)", len(list), err)
	}
	card := list[0]
	if card.Kind != "approval" {
		t.Fatalf("expected approval interaction, got %q", card.Kind)
	}
	_, ans, err := h.perms.Respond(ctx, card.ID, h.nextReqID(), card.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: card.ActionDigest,
	})
	if err != nil || ans == nil {
		t.Fatalf("approve Respond failed: ans=%v err=%v", ans, err)
	}
	h.acceptCommands(ctx, state.Command{
		ID:         ans.CommandID,
		RunID:      ans.TaskID,
		Kind:       state.CommandAnswer,
		QuestionID: ans.Question,
		Text:       ans.Text,
	})
	h.perms.MarkDelivered(ctx, card.ID)

	// Leg 2: woken by CommandAnswer, runs approved write and completes.
	outcome, err = h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("expected OutcomeDone after approval, got outcome=%s err=%v", outcome, err)
	}

	stored, err := h.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Status != state.RunCompleted || stored.Finishing != nil || stored.CleanupPending || stored.CleanupError != "" || stored.FinishedAt == nil {
		t.Fatalf("unexpected final run state: %+v", stored)
	}
	if writeOpID == "" {
		t.Fatal("expected write operation to run and appear in evidence")
	}

	executed := h.adapter.Executed()
	if len(executed) != 2 {
		t.Fatalf("expected 2 executed tool calls (read+write), got %d: %+v", len(executed), executed)
	}
	if executed[0].ToolName != inner.ToolNameRead || executed[1].ToolName != inner.ToolNameWrite || !executed[1].SideEffectApplied {
		t.Fatalf("unexpected executed calls: %+v", executed)
	}
	h.checkSafetyAndQuiescence(ctx)
}

func TestWorkflowSimMidBatchSteerAndSupersededCards(t *testing.T) {
	ctx := context.Background()
	h := newWFHarness(t, 2002, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})

	turn := 0
	h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
		turn++
		switch turn {
		case 1:
			return scriptedMultiCalls([]transcript.ToolCall{
				{ID: "mb-1", Name: inner.ToolNameRead, Args: map[string]any{"key": "key-1"}},
				{ID: "mb-2", Name: inner.ToolNameRead, Args: map[string]any{"key": "key-2"}},
				{ID: "mb-3", Name: inner.ToolNameRead, Args: map[string]any{"key": "key-3"}},
			}, 0.10), nil, true
		case 2:
			return scriptedCall("mb-4", inner.ToolNameWrite, map[string]any{
				"target": "key-9", "value": "stale-write", "resource_id": "key-9",
			}, 0.10), nil, true
		default:
			return scriptedCall("mb-5", "complete", map[string]any{
				"answer":   "Followed both steers and skipped stale calls.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		}
	})

	run := h.startTaskRecord(ctx, "Batch read three keys")

	projCount := 0
	steeredMidBatch := false
	sawSupersededGuard := false
	h.hookStore.onAfterProject = func(_ context.Context, _ state.ProjectRequest, err error) {
		if err != nil || steeredMidBatch {
			return
		}
		projCount++
		if projCount == 3 {
			// Project 1 = thinking (ModelRef), Project 2 = mb-1, Project 3 = mb-2 right before ToolRef!
			steeredMidBatch = true
			h.acceptCommands(ctx, state.Command{
				ID:    h.ids.Next(),
				RunID: run.ID,
				Kind:  state.CommandSteer,
				Text:  "Stop reading key-2 and key-3; write key-9 instead",
			})
		}
	}
	h.hookStore.onAfterGuard = func(_ context.Context, _ string, _ int64, err error) {
		if errors.Is(err, state.ErrSuperseded) {
			sawSupersededGuard = true
		}
	}

	// Leg 1: executes mb-1, hits ErrSuperseded on mb-2, skips mb-2 & mb-3, runs rev-2 write -> parks on approval card.
	outcome, err := h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("leg 1: expected OutcomeParked on rev-2 approval card, got outcome=%s err=%v", outcome, err)
	}

	list, _, err := h.perms.Interactions(ctx, "pending", run.ID, "", 20)
	if err != nil || len(list) != 1 {
		t.Fatalf("expected 1 pending approval card before rev-3 steer, got %d (err=%v)", len(list), err)
	}
	staleCard := list[0]

	// Steer rev 3 -> supersedes rev-2 approval card and wakes task!
	h.acceptCommands(ctx, state.Command{
		ID:    h.ids.Next(),
		RunID: run.ID,
		Kind:  state.CommandSteer,
		Text:  "Never mind the write, just finish now",
	})

	// Leg 2: completes at rev 3.
	outcome, err = h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("leg 2: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
	}

	stored, err := h.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Status != state.RunCompleted || stored.Revision != 3 {
		t.Fatalf("expected completed at revision 3, got status=%s rev=%d", stored.Status, stored.Revision)
	}
	if !sawSupersededGuard {
		t.Fatal("expected ToolRef to hit state.ErrSuperseded boundary on call 2")
	}

	executed := h.adapter.Executed()
	if len(executed) != 1 || executed[0].ToolName != inner.ToolNameRead || executed[0].Args["key"] != "key-1" {
		t.Fatalf("expected only key-1 read to execute, got %+v", executed)
	}

	ctrl, err := h.store.GetTaskControl(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetTaskControl: %v", err)
	}
	msgs := readTaskTranscript(t, ctx, h.store, run.ID, ctrl.Ref)
	skippedByCall := map[string]map[string]any{}
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.ToolResult == nil {
				continue
			}
			if outcome, _ := p.ToolResult.Output["outcome"].(string); outcome == "uncertain" {
				t.Fatalf("unexpected uncertain outcome in transcript for call %s: %+v", p.ToolResult.CallID, p.ToolResult.Output)
			}
			if skipped, _ := p.ToolResult.Output["skipped"].(bool); skipped {
				skippedByCall[p.ToolResult.CallID] = p.ToolResult.Output
			}
		}
	}
	if skippedByCall["mb-2"] == nil || skippedByCall["mb-2"]["outcome"] != "skipped" {
		t.Fatalf("expected mb-2 skipped cleanly with outcome=skipped, got %+v", skippedByCall["mb-2"])
	}
	if skippedByCall["mb-3"] == nil {
		t.Fatalf("expected mb-3 skipped cleanly, got %+v", skippedByCall)
	}

	_, _, staleErr := h.perms.Respond(ctx, staleCard.ID, h.nextReqID(), staleCard.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: staleCard.ActionDigest,
	})
	if !errors.Is(staleErr, permissions.ErrInteractionStale) {
		t.Fatalf("expected ErrInteractionStale for superseded rev-2 approval card, got %v", staleErr)
	}
	h.checkSafetyAndQuiescence(ctx)
}

func TestWorkflowSimPausedToolRefBoundaryNoUncertain(t *testing.T) {
	ctx := context.Background()
	h := newWFHarness(t, 2012, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})

	turn := 0
	h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
		turn++
		if turn == 1 {
			return scriptedMultiCalls([]transcript.ToolCall{
				{ID: "pt-1", Name: inner.ToolNameRead, Args: map[string]any{"key": "key-1"}},
				{ID: "pt-2", Name: inner.ToolNameRead, Args: map[string]any{"key": "key-2"}},
			}, 0.10), nil, true
		}
		return scriptedCall("pt-done", "complete", map[string]any{
			"answer":   "Resumed after paused ToolRef boundary.",
			"evidence": []any{},
			"unmet":    []any{},
		}, 0.10), nil, true
	})

	run := h.startTaskRecord(ctx, "Pause between Project and ToolRef")

	projCount := 0
	pausedOnTool := false
	sawPausedGuard := false
	h.hookStore.onAfterProject = func(_ context.Context, _ state.ProjectRequest, err error) {
		if err != nil || pausedOnTool {
			return
		}
		projCount++
		if projCount == 2 {
			// Project 1 = thinking, Project 2 = pt-1 right before ToolRef!
			pausedOnTool = true
			if pErr := h.setPaused(ctx, true); pErr != nil {
				t.Errorf("setPaused(true): %v", pErr)
			}
		}
	}
	h.hookStore.onAfterGuard = func(_ context.Context, _ string, _ int64, err error) {
		if errors.Is(err, state.ErrPaused) {
			sawPausedGuard = true
		}
	}

	// Leg 1: ToolRef hits ErrPaused, records pt-1/pt-2 as skipped (NO uncertain), parks on WaitPaused.
	outcome, err := h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("leg 1: expected OutcomeParked on WaitPaused, got outcome=%s err=%v", outcome, err)
	}
	if !sawPausedGuard {
		t.Fatal("expected ToolRef to hit state.ErrPaused boundary")
	}
	if len(h.adapter.Executed()) != 0 {
		t.Fatalf("expected paused ToolRef calls never to execute, got %+v", h.adapter.Executed())
	}

	// Unpause -> atomically wakes WaitPaused lease!
	if err := h.setPaused(ctx, false); err != nil {
		t.Fatalf("setPaused(false): %v", err)
	}

	// Leg 2: completes cleanly.
	outcome, err = h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("leg 2: expected OutcomeDone after unpause, got outcome=%s err=%v", outcome, err)
	}

	ctrl, err := h.store.GetTaskControl(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetTaskControl: %v", err)
	}
	msgs := readTaskTranscript(t, ctx, h.store, run.ID, ctrl.Ref)
	sawSkipped := false
	for _, m := range msgs {
		for _, p := range m.Parts {
			if p.ToolResult == nil {
				continue
			}
			if outcome, _ := p.ToolResult.Output["outcome"].(string); outcome == "uncertain" {
				t.Fatalf("paused ToolRef must not record uncertain outcome: %+v", p.ToolResult.Output)
			}
			if p.ToolResult.CallID == "pt-1" && p.ToolResult.Output["outcome"] == "skipped" {
				sawSkipped = true
			}
		}
	}
	if !sawSkipped {
		t.Fatal("expected pt-1 to be recorded with outcome=skipped")
	}
	h.checkSafetyAndQuiescence(ctx)
}

func TestWorkflowSimStopSignals(t *testing.T) {
	t.Run("NormalStopDuringExecution", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 2003, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})

		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			return scriptedCall("stop-1", inner.ToolNameRead, map[string]any{"key": "key-1"}, 0.10), nil, true
		})

		run := h.startTaskRecord(ctx, "Run until stopped")
		stopped := false
		h.hookStore.onAfterAdvance = func(_ context.Context, ctrl state.TaskControl, err error) {
			if err == nil && ctrl.Tools == 1 && !stopped {
				stopped = true
				h.acceptCommands(ctx, state.Command{
					ID:    h.ids.Next(),
					RunID: run.ID,
					Kind:  state.CommandStop,
				})
			}
		}

		outcome, err := h.runTask(ctx, run.ID, "", nil)
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("expected OutcomeDone after stop cleanup, got outcome=%s err=%v", outcome, err)
		}
		stored, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if stored.Status != state.RunCancelled || stored.Finishing != nil || stored.CleanupPending || stored.FinishedAt == nil {
			t.Fatalf("unexpected stopped run state: %+v", stored)
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("StopWhileWaitingOnQuestion", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 2004, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})

		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			return scriptedCall("stop-q-1", "request_input", map[string]any{"question": "Which environment?"}, 0.10), nil, true
		})

		run := h.startTaskRecord(ctx, "Ask a question then get stopped")

		// Parks on WaitQuestion.
		outcome, err := h.runTask(ctx, run.ID, "", nil)
		if err != nil || outcome != task.OutcomeParked {
			t.Fatalf("expected OutcomeParked on question, got outcome=%s err=%v", outcome, err)
		}

		// CommandStop wakes the parked task lease in the same transaction.
		h.acceptCommands(ctx, state.Command{
			ID:    h.ids.Next(),
			RunID: run.ID,
			Kind:  state.CommandStop,
		})

		outcome, err = h.runTask(ctx, run.ID, "", nil)
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("expected OutcomeDone after stop, got outcome=%s err=%v", outcome, err)
		}
		stored, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if stored.Status != state.RunCancelled || stored.Finishing != nil || stored.CleanupPending || stored.Question != nil {
			t.Fatalf("unexpected stopped-question run state: %+v", stored)
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("StopWithRunningSubtask", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 2005, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})

		var parentID, childID string
		h.model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
			if runID == parentID {
				return scriptedCall("sub-p-1", "request_work", map[string]any{
					"request": "Inspect external state",
					"why":     "Need subtask data",
				}, 0.10), nil, true
			}
			return scriptedCall("sub-c-1", inner.ToolNameRead, map[string]any{"key": "key-1"}, 0.10), nil, true
		})

		parent := h.startTaskRecord(ctx, "Parent task delegating to subtask")
		parentID = parent.ID

		// Parent runs until parked on QuestionWork.
		outcome, err := h.runTask(ctx, parentID, "", nil)
		if err != nil || outcome != task.OutcomeParked {
			t.Fatalf("parent leg 1: expected OutcomeParked on work request, got outcome=%s err=%v", outcome, err)
		}
		cur, err := h.store.GetRun(ctx, parentID)
		if err != nil || cur.Question == nil {
			t.Fatalf("expected parent parked on work question, got %+v err=%v", cur.Question, err)
		}

		// Spawn child and immediately stop parent!
		childID = h.ids.Next()
		h.acceptCommands(ctx, state.Command{
			ID:         h.ids.Next(),
			RunID:      childID,
			ParentID:   parentID,
			QuestionID: cur.Question.ID,
			Kind:       state.CommandStart,
			Text:       "Run delegated inspection",
		})
		h.acceptCommands(ctx, state.Command{
			ID:    h.ids.Next(),
			RunID: parentID,
			Kind:  state.CommandStop,
		})

		// Parent cascades stop to child, enters Finishing barrier, and parks on WaitFinishing.
		outcome, err = h.runTask(ctx, parentID, "", nil)
		if err != nil || outcome != task.OutcomeParked {
			t.Fatalf("parent leg 2: expected OutcomeParked on WaitFinishing, got outcome=%s err=%v", outcome, err)
		}

		midParent, err := h.store.GetRun(ctx, parentID)
		if err != nil {
			t.Fatalf("GetRun parent: %v", err)
		}
		if midParent.Status != state.RunRunning || midParent.Finishing == nil {
			t.Fatalf("expected parent held in Finishing barrier while child runs, got status=%s finishing=%v", midParent.Status, midParent.Finishing)
		}
		midChild, err := h.store.GetRun(ctx, childID)
		if err != nil {
			t.Fatalf("GetRun child: %v", err)
		}
		if !midChild.CancelRequested {
			t.Fatalf("expected parent stop to cascade CancelRequested to child, got %+v", midChild)
		}

		// Child runs -> cancels, cleans up, finalizes parent and wakes parent lease.
		outcome, err = h.runTask(ctx, childID, "", nil)
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("child: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
		}

		// Parent late-wake cleanup deletes parent's now-terminal lease.
		outcome, err = h.runTask(ctx, parentID, "", nil)
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("parent finalization wake: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
		}

		finalChild, err := h.store.GetRun(ctx, childID)
		if err != nil {
			t.Fatalf("GetRun final child: %v", err)
		}
		finalParent, err := h.store.GetRun(ctx, parentID)
		if err != nil {
			t.Fatalf("GetRun final parent: %v", err)
		}
		if finalChild.Status != state.RunCancelled || finalChild.CleanupPending {
			t.Fatalf("expected child cancelled & cleaned up, got %+v", finalChild)
		}
		if finalParent.Status != state.RunCancelled || finalParent.Finishing != nil || finalParent.CleanupPending {
			t.Fatalf("expected parent finalized to cancelled after child ended, got %+v", finalParent)
		}
		h.checkSafetyAndQuiescence(ctx)
	})
}

func TestWorkflowSimOperatorPauseResumeAndHeldPublication(t *testing.T) {
	ctx := context.Background()
	h := newWFHarness(t, 2006, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})

	turn := 0
	h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
		turn++
		if turn == 1 {
			return scriptedCall("pr-1", inner.ToolNameRead, map[string]any{"key": "key-1"}, 0.10), nil, true
		}
		return scriptedCall("pr-2", "complete", map[string]any{
			"answer":   "Resumed after pause and published after routing.",
			"evidence": []any{},
			"unmet":    []any{},
		}, 0.10), nil, true
	})

	run := h.startTaskRecord(ctx, "Exercise pause/resume and held publication")

	pausedOnce := false
	heldOnce := false
	sawRoutingPending := false

	h.hookStore.onAfterAdvance = func(_ context.Context, ctrl state.TaskControl, err error) {
		if err == nil && ctrl.Tools == 1 && !pausedOnce {
			pausedOnce = true
			if pauseErr := h.setPaused(ctx, true); pauseErr != nil {
				t.Errorf("setPaused(true): %v", pauseErr)
			}
		}
	}
	h.hookStore.onBeforePublish = func(_ context.Context, req state.PublishRequest) {
		if req.Status == state.RunCompleted && !heldOnce {
			heldOnce = true
			if inErr := h.store.AcceptConversationInput(ctx, h.ids.Next(), "Hold on, checking one more thing", nil); inErr != nil {
				t.Errorf("AcceptConversationInput: %v", inErr)
			}
		}
	}
	h.hookStore.onAfterPublish = func(_ context.Context, _ state.PublishRequest, err error) {
		if errors.Is(err, state.ErrRoutingPending) {
			sawRoutingPending = true
		}
	}

	// Leg 1: tool runs -> pauses -> parks on WaitPaused.
	outcome, err := h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("leg 1: expected OutcomeParked on WaitPaused, got outcome=%s err=%v", outcome, err)
	}
	if !pausedOnce {
		t.Fatal("expected pause trigger after first tool")
	}

	// Unpause wakes WaitPaused.
	if err := h.setPaused(ctx, false); err != nil {
		t.Fatalf("setPaused(false): %v", err)
	}

	// Leg 2: calls complete -> PublishRef hits ErrRoutingPending -> parks on WaitRoutingPending.
	outcome, err = h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("leg 2: expected OutcomeParked on WaitRoutingPending, got outcome=%s err=%v", outcome, err)
	}
	if !heldOnce || !sawRoutingPending {
		t.Fatalf("expected held publication (heldOnce=%v sawRoutingPending=%v)", heldOnce, sawRoutingPending)
	}

	heldRun, err := h.store.GetRun(ctx, run.ID)
	if err != nil || !heldRun.PublicationPending || heldRun.Status != state.RunRunning {
		t.Fatalf("expected held publication (status=%s pending=%v err=%v)", heldRun.Status, heldRun.PublicationPending, err)
	}

	// Route conversation turn -> clears UserRoutingPending and atomically wakes WaitRoutingPending!
	if !h.routeOneConversationTurn(ctx, "Routing resolved.") {
		t.Fatal("expected conversation turn to publish and clear UserRoutingPending")
	}

	// Leg 3: completes publication and cleanup.
	outcome, err = h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("leg 3: expected OutcomeDone after routing cleared, got outcome=%s err=%v", outcome, err)
	}

	stored, err := h.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if stored.Status != state.RunCompleted || stored.PublicationPending || stored.CleanupPending {
		t.Fatalf("unexpected final run state: %+v", stored)
	}
	h.checkSafetyAndQuiescence(ctx)
}

func TestWorkflowSimLongRunningMultiTurnHandoff(t *testing.T) {
	ctx := context.Background()
	h := newWFHarness(t, 2007, wfHarnessOpts{budgetUSD: 10.0, costPerCall: 0.05})

	const totalReads = 35
	const handoffAfterTools = 20
	modelTurn := 0
	h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
		modelTurn++
		if modelTurn <= totalReads {
			return scriptedCall(fmt.Sprintf("mt-%d", modelTurn), inner.ToolNameRead, map[string]any{
				"key": fmt.Sprintf("key-%d", (modelTurn%4)+1),
			}, 0.05), nil, true
		}
		return scriptedCall("mt-done", "complete", map[string]any{
			"answer":   "Finished across durable TaskControl handoff.",
			"evidence": []any{},
			"unmet":    []any{},
		}, 0.05), nil, true
	})

	run := h.startTaskRecord(ctx, "Long task exceeding 32 turns with mid-run worker drain handoff")

	drainCh := make(chan struct{})
	drainedOnce := false
	h.hookStore.onAfterAdvance = func(_ context.Context, ctrl state.TaskControl, err error) {
		if err == nil && ctrl.Tools == handoffAfterTools && !drainedOnce {
			drainedOnce = true
			close(drainCh)
		}
	}

	// Worker 1 executes 20 turns, then drains cleanly at step boundary, persisting TaskControl cursor.
	outcome, err := h.runTask(ctx, run.ID, "worker-1", drainCh)
	if err != nil || outcome != task.OutcomeReleased {
		t.Fatalf("worker-1: expected OutcomeReleased on Drain, got outcome=%s err=%v", outcome, err)
	}

	ctrlMid, err := h.store.GetTaskControl(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetTaskControl mid: %v", err)
	}
	if ctrlMid.Models != handoffAfterTools || ctrlMid.Tools != handoffAfterTools || ctrlMid.Ref == "" {
		t.Fatalf("unexpected mid-handoff TaskControl cursor: %+v", ctrlMid)
	}
	midRun, err := h.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun mid: %v", err)
	}
	if midRun.Status != state.RunRunning {
		t.Fatalf("expected run still running across worker handoff, got %s", midRun.Status)
	}

	// Worker 2 claims the released lease and completes all remaining turns seamlessly.
	outcome, err = h.runTask(ctx, run.ID, "worker-2", nil)
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("worker-2: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
	}

	finalRun, err := h.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun final: %v", err)
	}
	if finalRun.Status != state.RunCompleted || finalRun.CleanupPending || finalRun.FinishedAt == nil {
		t.Fatalf("unexpected final run state after handoff: %+v", finalRun)
	}
	if len(h.adapter.Executed()) != totalReads {
		t.Fatalf("expected %d read executions across both workers, got %d", totalReads, len(h.adapter.Executed()))
	}
	ctrlFinal, err := h.store.GetTaskControl(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetTaskControl final: %v", err)
	}
	if ctrlFinal.Models != totalReads+1 || ctrlFinal.Tools != totalReads || ctrlFinal.Fence <= ctrlMid.Fence {
		t.Fatalf("unexpected final TaskControl: %+v (mid fence=%d)", ctrlFinal, ctrlMid.Fence)
	}
	h.checkSafetyAndQuiescence(ctx)
}

func TestWorkflowSimMultiToolBatchBudgetCeiling(t *testing.T) {
	t.Run("ToolCostCrossesBudgetMidBatch", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 2008, wfHarnessOpts{
			budgetUSD:   0.40,
			costPerCall: 0.15,
			paidCostUSD: 0.30,
		})

		h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			return scriptedMultiCalls([]transcript.ToolCall{
				{ID: "bud-1", Name: paidToolName, Args: map[string]any{"key": "k1"}},
				{ID: "bud-2", Name: paidToolName, Args: map[string]any{"key": "k2"}},
				{ID: "bud-3", Name: paidToolName, Args: map[string]any{"key": "k3"}},
			}, 0.15), nil, true
		})

		run := h.startTaskRecord(ctx, "Run three paid tools in one batch")
		outcome, err := h.runTask(ctx, run.ID, "", nil)
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("expected clean budget-exhausted OutcomeDone, got outcome=%s err=%v", outcome, err)
		}
		if len(h.model.ModelCalls()) != 1 {
			t.Fatalf("expected exactly 1 model call before budget stop, got %d", len(h.model.ModelCalls()))
		}
		if got := h.paid.ExecutedKeys(); len(got) != 1 || got[0] != "k1" {
			t.Fatalf("expected only first paid tool call k1 to execute before budget ceiling, got %v", got)
		}
		finalRun, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if finalRun.Status != state.RunFailed || finalRun.CleanupPending {
			t.Fatalf("expected budget-exhausted run failed & cleaned up, got %+v", finalRun)
		}
		h.checkSafetyAndQuiescence(ctx)
	})

	t.Run("SharedFamilySpendCrossesBudgetBeforeBatch", func(t *testing.T) {
		ctx := context.Background()
		h := newWFHarness(t, 2009, wfHarnessOpts{
			budgetUSD:   0.50,
			costPerCall: 0.15,
		})

		var parentID, childID string
		parentTurns := 0
		childTurns := 0
		h.model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
			if runID == parentID {
				parentTurns++
				if parentTurns == 1 {
					return scriptedCall("fam-p-1", "request_work", map[string]any{
						"request": "Spend family budget in subtask",
						"why":     "Delegated work",
					}, 0.15), nil, true
				}
				return scriptedMultiCalls([]transcript.ToolCall{
					{ID: "fam-p-2", Name: inner.ToolNameRead, Args: map[string]any{"key": "key-1"}},
					{ID: "fam-p-3", Name: inner.ToolNameRead, Args: map[string]any{"key": "key-2"}},
				}, 0.15), nil, true
			}
			childTurns++
			if childTurns == 1 {
				return scriptedCall("fam-c-1", inner.ToolNameRead, map[string]any{"key": "key-3"}, 0.15), nil, true
			}
			return scriptedCall("fam-c-2", "complete", map[string]any{
				"answer":   "Subtask finished.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.15), nil, true
		})

		parent := h.startTaskRecord(ctx, "Parent sharing budget with subtask")
		parentID = parent.ID

		// Parent parks on QuestionWork.
		outcome, err := h.runTask(ctx, parentID, "", nil)
		if err != nil || outcome != task.OutcomeParked {
			t.Fatalf("parent leg 1: expected OutcomeParked on work request, got outcome=%s err=%v", outcome, err)
		}
		cur, err := h.store.GetRun(ctx, parentID)
		if err != nil || cur.Question == nil {
			t.Fatalf("expected parent work question, got err=%v", err)
		}

		// Spawn and run child to completion (rolls up $0.30 spend to parent and wakes parent).
		childID = h.ids.Next()
		h.acceptCommands(ctx, state.Command{
			ID:         h.ids.Next(),
			RunID:      childID,
			ParentID:   parentID,
			QuestionID: cur.Question.ID,
			Kind:       state.CommandStart,
			Text:       "Run budgeted subtask",
		})
		outcome, err = h.runTask(ctx, childID, "", nil)
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("child: expected OutcomeDone, got outcome=%s err=%v", outcome, err)
		}

		// Parent resumes: model call 2 pushes family spend to $0.60 >= $0.50 -> halts BEFORE executing batch tools!
		outcome, err = h.runTask(ctx, parentID, "", nil)
		if err != nil || outcome != task.OutcomeDone {
			t.Fatalf("parent leg 2: expected clean budget-exhausted OutcomeDone, got outcome=%s err=%v", outcome, err)
		}

		for _, exec := range h.adapter.Executed() {
			if exec.RunID == parentID {
				t.Fatalf("parent must not execute batch tools after crossing shared family budget, got %+v", exec)
			}
		}
		if parentTurns != 2 {
			t.Fatalf("expected exactly 2 parent model turns (request_work + over-budget batch), got %d", parentTurns)
		}
		finalParent, err := h.store.GetRun(ctx, parentID)
		if err != nil {
			t.Fatalf("GetRun parent: %v", err)
		}
		if finalParent.Status != state.RunFailed || finalParent.CleanupPending {
			t.Fatalf("expected parent failed on budget ceiling, got %+v", finalParent)
		}
		h.checkSafetyAndQuiescence(ctx)
	})
}

func TestWorkflowSimContextOverflowLargeBackground(t *testing.T) {
	ctx := context.Background()
	h := newWFHarness(t, 2010, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})

	priorRun := h.startTaskRecord(ctx, "Prior completed task with large background")
	h.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
		return scriptedCall("prior-done", "complete", map[string]any{
			"answer":   "Prior completed.",
			"evidence": []any{},
			"unmet":    []any{},
		}, 0.05), nil, true
	})
	if outcome, err := h.runTask(ctx, priorRun.ID, "", nil); err != nil || outcome != task.OutcomeDone {
		t.Fatalf("prior run error: outcome=%s err=%v", outcome, err)
	}

	payloadChunk := strings.Repeat("background-quote-\"data\"-", 290)
	for i := range 9 {
		h.clock.Advance(time.Second)
		msgID := h.ids.Next()
		msg := state.Message{
			ID:        msgID,
			RunID:     priorRun.ID,
			Role:      "assistant",
			Text:      fmt.Sprintf("segment-%d:%s", i, payloadChunk),
			CreatedAt: h.clock.Now().UTC(),
		}
		raw, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal seeded message: %v", err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatalf("unmarshal seeded message: %v", err)
		}
		path := fmt.Sprintf("owners/%s/messages/%s", h.user.DataOwner(), msgID)
		if err := h.backend.Apply(ctx, docstore.Mutation{Kind: "set", Path: path, Fields: fields}); err != nil {
			t.Fatalf("seed background message %d: %v", i, err)
		}
	}

	const currentRequest = "Continue from the prior task despite large background"
	var seenSizes []int
	var sawCurrentRequestOnRetry bool
	h.model.SetScript(func(_ context.Context, _ string, req agent.Request) (agent.Response, error, bool) {
		raw, _ := json.Marshal(req.Messages)
		seenSizes = append(seenSizes, len(raw))
		if len(seenSizes) == 1 {
			return agent.Response{NotSent: true, Cost: 0, CostKnown: true}, contextview.ErrOverflow, true
		}
		for _, m := range req.Messages {
			for _, p := range m.Parts {
				if strings.Contains(p.Text, currentRequest) {
					sawCurrentRequestOnRetry = true
				}
			}
		}
		return scriptedCall("ovf-done", "complete", map[string]any{
			"answer":   "Recovered from context overflow with trimmed background.",
			"evidence": []any{},
			"unmet":    []any{},
		}, 0.10), nil, true
	})

	runID := h.ids.Next()
	receipts, err := h.store.AcceptCommandBatch(ctx, []state.Command{{
		ID:        runID,
		RunID:     runID,
		Kind:      state.CommandStart,
		Text:      currentRequest,
		CreatedAt: h.clock.Now().UTC(),
		Origin:    &state.TaskOrigin{ContinuesTaskID: priorRun.ID},
	}}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch continued run: %v", err)
	}
	run := receipts[0].Run
	if _, err := h.backend.EnsureTaskLease(ctx, h.user.ID, run.ID, time.Time{}); err != nil {
		t.Fatalf("EnsureTaskLease continued run: %v", err)
	}
	if outcome, err := h.runTask(ctx, run.ID, "", nil); err != nil || outcome != task.OutcomeDone {
		t.Fatalf("continued run failed on overflow retry: outcome=%s err=%v", outcome, err)
	}
	if len(seenSizes) != 2 {
		t.Fatalf("expected 2 model attempts (overflow + reduced retry), got %d (sizes=%v)", len(seenSizes), seenSizes)
	}
	if seenSizes[0] <= contextview.TaskBytes/2 {
		t.Fatalf("expected initial request with large background to exceed 64 KiB, got %d bytes", seenSizes[0])
	}
	if seenSizes[1] >= seenSizes[0] || seenSizes[1] > contextview.TaskBytes/2+4096 {
		t.Fatalf("expected reduced retry strictly smaller than initial (%d) and within reduced budget, got %d", seenSizes[0], seenSizes[1])
	}
	if !sawCurrentRequestOnRetry {
		t.Fatal("expected current task user request to be preserved in reduced retry")
	}

	finalRun, err := h.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if finalRun.Status != state.RunCompleted || finalRun.CleanupPending {
		t.Fatalf("expected RunCompleted after overflow retry, got %+v", finalRun)
	}
	h.checkSafetyAndQuiescence(ctx)

	t.Run("SingleOversizedUTF8Record", func(t *testing.T) {
		h2 := newWFHarness(t, 2011, wfHarnessOpts{budgetUSD: 2.0, costPerCall: 0.10})
		prior := h2.startTaskRecord(ctx, "Prior task with single giant UTF-8 message")
		h2.model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			return scriptedCall("prior-u-done", "complete", map[string]any{
				"answer":   "Done.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.05), nil, true
		})
		if outcome, err := h2.runTask(ctx, prior.ID, "", nil); err != nil || outcome != task.OutcomeDone {
			t.Fatalf("prior run error: outcome=%s err=%v", outcome, err)
		}

		msgID := h2.ids.Next()
		giantUTF8 := strings.Repeat("こんにちは世界🚀", 2400)
		msg := state.Message{
			ID:        msgID,
			RunID:     prior.ID,
			Role:      "assistant",
			Text:      giantUTF8,
			CreatedAt: h2.clock.Now().UTC(),
		}
		raw, _ := json.Marshal(msg)
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		path := fmt.Sprintf("owners/%s/messages/%s", h2.user.DataOwner(), msgID)
		if err := h2.backend.Apply(ctx, docstore.Mutation{Kind: "set", Path: path, Fields: fields}); err != nil {
			t.Fatalf("seed giant UTF-8 message: %v", err)
		}

		var sizes []int
		validUTF8 := true
		h2.model.SetScript(func(_ context.Context, _ string, req agent.Request) (agent.Response, error, bool) {
			b, _ := json.Marshal(req.Messages)
			sizes = append(sizes, len(b))
			if len(sizes) == 1 {
				return agent.Response{NotSent: true, Cost: 0, CostKnown: true}, contextview.ErrOverflow, true
			}
			for _, m := range req.Messages {
				for _, p := range m.Parts {
					if !utf8.ValidString(p.Text) {
						validUTF8 = false
					}
				}
			}
			return scriptedCall("ovf-u-done", "complete", map[string]any{
				"answer":   "Recovered with valid UTF-8 truncated background.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.10), nil, true
		})

		nextID := h2.ids.Next()
		nextReceipts, err := h2.store.AcceptCommandBatch(ctx, []state.Command{{
			ID:        nextID,
			RunID:     nextID,
			Kind:      state.CommandStart,
			Text:      "Continue after giant UTF-8 prior task",
			CreatedAt: h2.clock.Now().UTC(),
			Origin:    &state.TaskOrigin{ContinuesTaskID: prior.ID},
		}}, nil)
		if err != nil {
			t.Fatalf("AcceptCommandBatch continued utf8 run: %v", err)
		}
		next := nextReceipts[0].Run
		if _, err := h2.backend.EnsureTaskLease(ctx, h2.user.ID, next.ID, time.Time{}); err != nil {
			t.Fatalf("EnsureTaskLease continued utf8 run: %v", err)
		}
		if outcome, err := h2.runTask(ctx, next.ID, "", nil); err != nil || outcome != task.OutcomeDone {
			t.Fatalf("overflow retry failed: outcome=%s err=%v", outcome, err)
		}
		if len(sizes) != 2 || sizes[1] >= sizes[0] || !validUTF8 {
			t.Fatalf("unexpected retry stats: sizes=%v validUTF8=%v", sizes, validUTF8)
		}
		h2.checkSafetyAndQuiescence(ctx)
	})
}

func runDeterministicWorkflowScenario(t *testing.T, seed int64) (string, string, string) {
	t.Helper()
	ctx := context.Background()
	h := newWFHarness(t, seed, wfHarnessOpts{budgetUSD: 3.0, costPerCall: 0.10})

	scenarioRNG := rand.New(rand.NewPCG(uint64(seed)^0x12345678, uint64(seed)^0x87654321))
	keyA := fmt.Sprintf("key-%d", scenarioRNG.IntN(4)+1)
	keyB := fmt.Sprintf("key-%d", scenarioRNG.IntN(4)+1)
	valB := fmt.Sprintf("val-%d", scenarioRNG.IntN(1000))

	turn := 0
	h.model.SetScript(func(_ context.Context, _ string, req agent.Request) (agent.Response, error, bool) {
		turn++
		switch turn {
		case 1:
			return scriptedMultiCalls([]transcript.ToolCall{
				{ID: "det-1", Name: inner.ToolNameRead, Args: map[string]any{"key": keyA}},
				{ID: "det-2", Name: inner.ToolNameRead, Args: map[string]any{"key": keyB}},
			}, 0.10), nil, true
		case 2:
			return scriptedCall("det-3", inner.ToolNameWrite, map[string]any{
				"target": keyB, "value": valB, "resource_id": keyB,
			}, 0.10), nil, true
		case 3:
			return scriptedCall("det-4", inner.ToolNameWrite, map[string]any{
				"target": keyB, "value": valB, "resource_id": keyB,
			}, 0.10), nil, true
		default:
			var ev []any
			for _, m := range req.Messages {
				for _, p := range m.Parts {
					if p.ToolResult != nil {
						if outcome, _ := p.ToolResult.Output["outcome"].(string); outcome == "confirmed" {
							if opID, _ := p.ToolResult.Output["operation_id"].(string); opID != "" {
								ev = append(ev, opID)
							}
						}
					}
				}
			}
			return scriptedCall("det-5", "complete", map[string]any{
				"answer":   fmt.Sprintf("Completed deterministic seed %d.", seed),
				"evidence": ev,
				"unmet":    []any{},
			}, 0.10), nil, true
		}
	})

	run := h.startTaskRecord(ctx, fmt.Sprintf("Deterministic run for seed %d", seed))

	steered := false
	projCount := 0
	h.hookStore.onAfterProject = func(_ context.Context, _ state.ProjectRequest, err error) {
		if err == nil && !steered {
			projCount++
			if projCount == 3 {
				steered = true
				h.acceptCommands(ctx, state.Command{
					ID:    h.ids.Next(),
					RunID: run.ID,
					Kind:  state.CommandSteer,
					Text:  "Proceed to write now",
				})
			}
		}
	}

	// Leg 1: reads keyA, steers mid-batch, opens approval card for keyB, parks on WaitQuestion.
	outcome, err := h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("seed %d leg 1: expected OutcomeParked on approval, got outcome=%s err=%v", seed, outcome, err)
	}

	// Approve card AND immediately pause system before next leg!
	list, _, err := h.perms.Interactions(ctx, "pending", run.ID, "", 20)
	if err != nil || len(list) != 1 {
		t.Fatalf("seed %d: expected 1 approval card, got %d (err=%v)", seed, len(list), err)
	}
	card := list[0]
	_, ans, err := h.perms.Respond(ctx, card.ID, h.nextReqID(), card.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: card.ActionDigest,
	})
	if err != nil || ans == nil {
		t.Fatalf("seed %d Respond failed: ans=%v err=%v", seed, ans, err)
	}
	h.acceptCommands(ctx, state.Command{
		ID:         ans.CommandID,
		RunID:      ans.TaskID,
		Kind:       state.CommandAnswer,
		QuestionID: ans.Question,
		Text:       ans.Text,
	})
	h.perms.MarkDelivered(ctx, card.ID)
	if err := h.setPaused(ctx, true); err != nil {
		t.Fatalf("seed %d setPaused(true): %v", seed, err)
	}

	// Leg 2: woken by answer while paused -> parks on WaitPaused.
	outcome, err = h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("seed %d leg 2: expected OutcomeParked on WaitPaused, got outcome=%s err=%v", seed, outcome, err)
	}

	// Unpause -> wakes WaitPaused.
	if err := h.setPaused(ctx, false); err != nil {
		t.Fatalf("seed %d setPaused(false): %v", seed, err)
	}

	// Leg 3: executes approved write and completes.
	outcome, err = h.runTask(ctx, run.ID, "", nil)
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("seed %d leg 3: expected OutcomeDone, got outcome=%s err=%v", seed, outcome, err)
	}
	h.checkSafetyAndQuiescence(ctx)
	return memstore.CombinedDigest(h.backend, h.artifacts), h.adapter.Digest(), h.model.Digest()
}

func TestWorkflowSimDeterminism(t *testing.T) {
	b1, a1, m1 := runDeterministicWorkflowScenario(t, 20261122)
	b2, a2, m2 := runDeterministicWorkflowScenario(t, 20261122)
	if b1 != b2 {
		t.Fatalf("backend digest mismatch across identical seeds: %s != %s", b1, b2)
	}
	if a1 != a2 {
		t.Fatalf("adapter digest mismatch across identical seeds: %s != %s", a1, a2)
	}
	if m1 != m2 {
		t.Fatalf("model digest mismatch across identical seeds: %s != %s", m1, m2)
	}
	for _, seed := range []int64{101, 202, 303} {
		runDeterministicWorkflowScenario(t, seed)
	}
}
