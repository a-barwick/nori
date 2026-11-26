package inner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/contextview"
	"nori/internal/permissions"
	"nori/internal/sim/env"
	"nori/internal/sim/inner"
	"nori/internal/sim/invariants"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/transcript"
	"nori/internal/users"
)

type testHarness struct {
	t         *testing.T
	seed      int64
	rng       *rand.Rand
	clock     *env.Clock
	ids       *env.IDGen
	faults    *memstore.FaultInjector
	backend   *memstore.Backend
	artifacts *memstore.Artifacts
	usersDB   *records.Users
	user      users.User
	db        *records.Database
	store     state.ConversationStore
	perms     *permissions.Service
	driver    *inner.Driver
	userRT    *inner.UserRuntime
	checker   *invariants.Checker
	reqSeq    int
}

type harnessOpts struct {
	budgetUSD      float64
	costPerCall    float64
	userSends      string
	userID         string
	disableFaults  bool
	storeFaults    memstore.FaultConfig
	modelCfg       inner.ModelConfig
	adapterCfg     inner.AdapterConfig
	cleanupErrProb float64
}

func newTestHarness(t *testing.T, seed int64, opts harnessOpts) *testHarness {
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
		modelCfg = inner.DefaultModelConfig()
	}
	if opts.costPerCall > 0 {
		modelCfg.CostPerCall = opts.costPerCall
	}

	clock := env.NewClock(env.DefaultBaseTime)
	ids := env.NewIDGen(derive())
	faultInj := memstore.NewFaultInjector(opts.storeFaults, derive())
	if opts.disableFaults {
		faultInj.SetEnabled(false)
	}
	backend := memstore.New(memstore.WithClock(clock.Now), memstore.WithFaultInjector(faultInj))
	artifacts := memstore.NewArtifacts(memstore.WithArtifactsFaultInjector(faultInj))
	usersDB := records.NewSharedUsers(backend)

	uid := opts.userID
	if uid == "" {
		uid = ids.Next()
	}
	u := users.User{
		ID:          uid,
		Email:       "sim-inner@example.com",
		DisplayName: "Sim Inner User",
		TimeZone:    "UTC",
	}
	db := records.NewShared(backend, u, opts.budgetUSD)
	db.Now = clock.Now
	store := state.Compose(db, artifacts)
	perms := &permissions.Service{
		Store:            store,
		UnattendedWrites: "confirm_writes",
		UserSends:        opts.userSends,
		Now:              clock.Now,
	}
	checker := invariants.New(invariants.Config{
		Backend:     backend,
		Faults:      faultInj,
		Now:         clock.Now,
		CheckLeases: true,
	})

	driver := inner.NewDriver(inner.DriverConfig{
		Clock:          clock,
		Backend:        backend,
		RNG:            derive(),
		IDs:            ids,
		Faults:         faultInj,
		ModelConfig:    modelCfg,
		AdapterConfig:  opts.adapterCfg,
		CleanupErrProb: opts.cleanupErrProb,
		DisableFaults:  opts.disableFaults,
	})
	rt, err := driver.AddUser(inner.UserConfig{
		UserID:      u.ID,
		Store:       store,
		Permissions: perms,
		TimeZone:    u.TimeZone,
		UserFacts:   "Simulated user",
	})
	if err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	return &testHarness{
		t:         t,
		seed:      seed,
		rng:       derive(),
		clock:     clock,
		ids:       ids,
		faults:    faultInj,
		backend:   backend,
		artifacts: artifacts,
		usersDB:   usersDB,
		user:      u,
		db:        db,
		store:     store,
		perms:     perms,
		driver:    driver,
		userRT:    rt,
		checker:   checker,
	}
}

func (h *testHarness) nextReqID() string {
	h.reqSeq++
	return fmt.Sprintf("req-%d", h.reqSeq)
}

func (h *testHarness) startTask(ctx context.Context, text string) state.Run {
	h.t.Helper()
	runID := h.ids.Next()
	cmd := state.Command{
		ID:    h.ids.Next(),
		RunID: runID,
		Kind:  state.CommandStart,
		Text:  text,
	}
	receipts, err := h.store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
	if err != nil {
		h.t.Fatalf("AcceptCommandBatch start: %v", err)
	}
	if err := h.checker.RecordCommandReceipts(h.user.ID, receipts); err != nil {
		h.t.Fatalf("RecordCommandReceipts: %v", err)
	}
	return receipts[0].Run
}

func (h *testHarness) steerTask(ctx context.Context, runID, text string) error {
	cmd := state.Command{
		ID:    h.ids.Next(),
		RunID: runID,
		Kind:  state.CommandSteer,
		Text:  text,
	}
	receipts, err := h.store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
	if err != nil {
		return err
	}
	_ = h.checker.RecordCommandReceipts(h.user.ID, receipts)
	return nil
}

func (h *testHarness) stopTask(ctx context.Context, runID string) error {
	cmd := state.Command{
		ID:    h.ids.Next(),
		RunID: runID,
		Kind:  state.CommandStop,
	}
	receipts, err := h.store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
	if err != nil {
		return err
	}
	_ = h.checker.RecordCommandReceipts(h.user.ID, receipts)
	return nil
}

func (h *testHarness) deliverAnswer(ctx context.Context, ans *permissions.Answer) error {
	if ans == nil {
		return nil
	}
	cmd := state.Command{
		ID:         ans.CommandID,
		RunID:      ans.TaskID,
		Kind:       state.CommandAnswer,
		QuestionID: ans.Question,
		Text:       ans.Text,
	}
	receipts, err := h.store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
	if err != nil {
		return err
	}
	_ = h.checker.RecordCommandReceipts(h.user.ID, receipts)
	return nil
}

func (h *testHarness) stepUntilParkedOrDone(ctx context.Context, runID string, maxSteps int) int {
	h.t.Helper()
	steps := 0
	for range maxSteps {
		h.clock.Advance(250 * time.Millisecond)
		progressed, _ := h.driver.StepRun(ctx, runID)
		if !progressed {
			break
		}
		steps++
	}
	return steps
}

func (h *testHarness) setPaused(ctx context.Context, paused bool) error {
	was := h.faults.Enabled()
	if was {
		h.faults.SetEnabled(false)
		defer h.faults.SetEnabled(true)
	}
	return h.usersDB.SetPaused(ctx, paused)
}

func (h *testHarness) drainUserWakes(ctx context.Context) {
	for h.routeOneConversationTurn(ctx, "Drain routed.") {
	}
	for range 16 {
		claim, ok, err := h.backend.ClaimUserWake(ctx, docstore.LaneConversation, "sim-conv-drain", 30*time.Second)
		if err != nil || !ok {
			break
		}
		for h.routeOneConversationTurn(ctx, "Drain routed.") {
		}
		pending, pErr := h.store.UserRoutingPending(ctx)
		if pErr == nil && !pending {
			parked, _ := h.backend.ParkUserWake(ctx, claim.UserID, claim.Lane, "sim-conv-drain", claim.WakeSeq)
			if !parked {
				_ = h.backend.ReleaseUserWake(ctx, claim.UserID, claim.Lane, "sim-conv-drain", claim.WakeSeq, time.Time{})
			}
		} else {
			_ = h.backend.ReleaseUserWake(ctx, claim.UserID, claim.Lane, "sim-conv-drain", claim.WakeSeq, h.clock.Now().Add(time.Second))
			break
		}
	}
}

func (h *testHarness) userTargets() []invariants.UserTarget {
	return []invariants.UserTarget{
		{
			User:        h.user,
			Store:       h.store,
			Permissions: h.perms,
		},
	}
}

func scriptedCall(id, name string, args map[string]any, cost float64) agent.Response {
	call := transcript.ToolCall{ID: id, Name: name, Args: args}
	msg := transcript.Message{
		Role:  transcript.RoleModel,
		Parts: []transcript.Part{{ToolCall: &call}},
	}
	return agent.Response{
		Message:   &msg,
		Calls:     []transcript.ToolCall{call},
		Usage:     &agent.Usage{InputTokens: 100, OutputTokens: 30, TotalTokens: 130},
		Cost:      cost,
		CostKnown: true,
	}
}

func TestReadWriteApprovalAndSteerSupersedes(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 101, harnessOpts{disableFaults: true})

	turn := 0
	h.driver.Model.SetScript(func(_ context.Context, _ string, req agent.Request) (agent.Response, error, bool) {
		turn++
		switch turn {
		case 1:
			return scriptedCall("c1", inner.ToolNameRead, map[string]any{"key": "config"}, 0.05), nil, true
		case 2:
			return scriptedCall("c2", inner.ToolNameWrite, map[string]any{"target": "config", "value": "v1", "resource_id": "config"}, 0.05), nil, true
		case 3:
			return scriptedCall("c3", inner.ToolNameWrite, map[string]any{"target": "config", "value": "v2", "resource_id": "config"}, 0.05), nil, true
		case 4:
			return scriptedCall("c4", inner.ToolNameWrite, map[string]any{"target": "config", "value": "v2", "resource_id": "config"}, 0.05), nil, true
		default:
			return scriptedCall(fmt.Sprintf("c%d", turn), "complete", map[string]any{
				"answer":   "Updated config to v2 after approval.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.05), nil, true
		}
	})

	run := h.startTask(ctx, "Read and update config")
	h.stepUntilParkedOrDone(ctx, run.ID, 30)

	pending, _, err := h.perms.Interactions(ctx, "pending", run.ID, "", 20)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected 1 pending approval interaction, got %d (err=%v)", len(pending), err)
	}
	card1 := pending[0]
	if card1.Kind != "approval" || card1.ActionDigest == "" || card1.InstructionRevision == nil || *card1.InstructionRevision != 1 {
		t.Fatalf("unexpected rev-1 approval card: %+v", card1)
	}
	for _, ex := range h.driver.Adapter.Executed() {
		if ex.ToolName == inner.ToolNameWrite {
			t.Fatalf("write executed before approval: %+v", ex)
		}
	}

	if err := h.steerTask(ctx, run.ID, "Actually write v2 instead"); err != nil {
		t.Fatalf("steerTask: %v", err)
	}

	if _, _, err := h.perms.Respond(ctx, card1.ID, h.nextReqID(), card1.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: card1.ActionDigest,
	}); !errors.Is(err, permissions.ErrInteractionStale) {
		t.Fatalf("expected ErrInteractionStale for superseded approval card, got: %v", err)
	}

	h.stepUntilParkedOrDone(ctx, run.ID, 30)
	pending, _, err = h.perms.Interactions(ctx, "pending", run.ID, "", 20)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected 1 pending rev-2 approval interaction, got %d (err=%v)", len(pending), err)
	}
	card2 := pending[0]
	if card2.InstructionRevision == nil || *card2.InstructionRevision != 2 {
		t.Fatalf("expected card2 bound to rev 2, got %+v", card2)
	}

	if _, _, err := h.perms.Respond(ctx, card2.ID, h.nextReqID(), card2.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: card1.ActionDigest,
	}); !errors.Is(err, permissions.ErrInteractionStale) {
		t.Fatalf("expected ErrInteractionStale for mismatched digest, got: %v", err)
	}

	_, ans, err := h.perms.Respond(ctx, card2.ID, h.nextReqID(), card2.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: card2.ActionDigest,
	})
	if err != nil {
		t.Fatalf("Respond approve card2: %v", err)
	}
	if err := h.deliverAnswer(ctx, ans); err != nil {
		t.Fatalf("deliverAnswer: %v", err)
	}
	h.perms.MarkDelivered(ctx, card2.ID)

	h.stepUntilParkedOrDone(ctx, run.ID, 30)

	finalRun, err := h.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if finalRun.Status != state.RunCompleted {
		t.Fatalf("expected RunCompleted, got %s (err=%q)", finalRun.Status, finalRun.Error)
	}

	writeCount := 0
	for _, ex := range h.driver.Adapter.Executed() {
		if ex.ToolName == inner.ToolNameWrite {
			writeCount++
			if ex.Revision != 2 || ex.ActionDigest != card2.ActionDigest || ex.Args["value"] != "v2" {
				t.Fatalf("unexpected executed write: %+v", ex)
			}
		}
	}
	if writeCount != 1 {
		t.Fatalf("expected exactly 1 executed write, got %d", writeCount)
	}

	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("CheckInnerInvariants: %v", err)
	}
}

func TestRecipientScopedRules(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 102, harnessOpts{disableFaults: true, userSends: "ask"})

	if _, err := h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{inner.ToolIDSend},
		ConnectionIDs: []string{},
		ResourceIDs:   []string{},
		ActionClasses: []string{"sensitive_send"},
		Recipients:    []string{"alice@example.com"},
		Title:         "Allow sends to Alice",
	}); err != nil {
		t.Fatalf("SaveRule allow: %v", err)
	}
	if _, err := h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
		Effect:        "deny",
		ToolIDs:       []string{inner.ToolIDSend},
		ConnectionIDs: []string{},
		ResourceIDs:   []string{},
		ActionClasses: []string{"sensitive_send"},
		Recipients:    []string{"blocked@example.com"},
		Title:         "Deny sends to Blocked",
	}); err != nil {
		t.Fatalf("SaveRule deny: %v", err)
	}

	turn := 0
	h.driver.Model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
		turn++
		switch turn {
		case 1:
			return scriptedCall("s1", inner.ToolNameSend, map[string]any{"to": "alice@example.com", "body": "hello alice"}, 0.05), nil, true
		case 2:
			return scriptedCall("s2", inner.ToolNameSend, map[string]any{"to": "blocked@example.com", "body": "should be denied"}, 0.05), nil, true
		case 3:
			return scriptedCall("s3", inner.ToolNameSend, map[string]any{"to": "bob@example.com", "body": "needs approval"}, 0.05), nil, true
		default:
			return scriptedCall(fmt.Sprintf("s%d", turn), "complete", map[string]any{
				"answer":   "Sent to alice; blocked was denied; bob declined.",
				"evidence": []any{},
				"unmet":    []any{"blocked@example.com denied", "bob@example.com declined"},
			}, 0.05), nil, true
		}
	})

	run := h.startTask(ctx, "Send test messages")
	h.stepUntilParkedOrDone(ctx, run.ID, 30)

	pending, _, err := h.perms.Interactions(ctx, "pending", run.ID, "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected 1 pending interaction for bob, got %d (err=%v)", len(pending), err)
	}
	if !strings.Contains(pending[0].Prompt, "bob@example.com") {
		t.Fatalf("expected bob approval prompt, got %q", pending[0].Prompt)
	}

	_, ans, err := h.perms.Respond(ctx, pending[0].ID, h.nextReqID(), pending[0].Revision, permissions.Response{
		Kind:         "decline",
		ActionDigest: pending[0].ActionDigest,
	})
	if err != nil {
		t.Fatalf("decline bob: %v", err)
	}
	if err := h.deliverAnswer(ctx, ans); err != nil {
		t.Fatalf("deliverAnswer: %v", err)
	}
	h.stepUntilParkedOrDone(ctx, run.ID, 30)

	executed := h.driver.Adapter.Executed()
	if len(executed) != 1 || executed[0].Args["to"] != "alice@example.com" {
		t.Fatalf("expected only alice@example.com in Executed, got: %+v", executed)
	}

	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("CheckInnerInvariants: %v", err)
	}
}

func TestSubtaskSpawningAndFinishingBarrier(t *testing.T) {
	ctx := context.Background()

	t.Run("NormalSubtaskCompletion", func(t *testing.T) {
		h := newTestHarness(t, 103, harnessOpts{disableFaults: true})
		parentTurn := 0
		childTurn := 0
		var parentID string
		h.driver.Model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
			if runID == parentID {
				parentTurn++
				if parentTurn == 1 {
					return scriptedCall("p1", "request_work", map[string]any{
						"request": "Read key-1 in subtask",
						"why":     "delegation test",
					}, 0.05), nil, true
				}
				return scriptedCall("p2", "complete", map[string]any{
					"answer":   "Parent completed with subtask output.",
					"evidence": []any{},
					"unmet":    []any{},
				}, 0.05), nil, true
			}
			childTurn++
			if childTurn == 1 {
				return scriptedCall("ch1", inner.ToolNameRead, map[string]any{"key": "key-1"}, 0.05), nil, true
			}
			return scriptedCall("ch2", "complete", map[string]any{
				"answer":   "Child read key-1 successfully.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.05), nil, true
		})

		parent := h.startTask(ctx, "Parent delegating work")
		parentID = parent.ID
		h.stepUntilParkedOrDone(ctx, parent.ID, 20)

		gotParent, err := h.store.GetRun(ctx, parent.ID)
		if err != nil || gotParent.Question == nil || gotParent.Question.Kind != state.QuestionWork {
			t.Fatalf("expected parent parked on QuestionWork, got %+v (err=%v)", gotParent.Question, err)
		}

		children, err := h.driver.SpawnSubtask(ctx, parent.ID, "Child task: read key-1")
		if err != nil || len(children) != 1 {
			t.Fatalf("SpawnSubtask: len=%d err=%v", len(children), err)
		}
		childID := children[0].ID

		h.stepUntilParkedOrDone(ctx, childID, 30)

		h.stepUntilParkedOrDone(ctx, parent.ID, 30)

		finalParent, _ := h.store.GetRun(ctx, parent.ID)
		finalChild, _ := h.store.GetRun(ctx, childID)
		if finalChild.Status != state.RunCompleted || finalParent.Status != state.RunCompleted {
			t.Fatalf("expected both completed, got parent=%s child=%s", finalParent.Status, finalChild.Status)
		}
		if err := h.driver.CheckInnerInvariants(ctx); err != nil {
			t.Fatalf("CheckInnerInvariants: %v", err)
		}
	})

	t.Run("ParentFinishingBarrierWaitsForChild", func(t *testing.T) {
		h := newTestHarness(t, 104, harnessOpts{disableFaults: true})
		var parentID string
		h.driver.Model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
			if runID == parentID {
				return scriptedCall("p1", "request_work", map[string]any{
					"request": "Long running child work",
					"why":     "finishing barrier test",
				}, 0.05), nil, true
			}
			return scriptedCall("ch1", inner.ToolNameRead, map[string]any{"key": "slow"}, 0.05), nil, true
		})

		parent := h.startTask(ctx, "Parent that gets stopped while child runs")
		parentID = parent.ID
		h.stepUntilParkedOrDone(ctx, parent.ID, 20)

		children, err := h.driver.SpawnSubtask(ctx, parent.ID, "Child work")
		if err != nil || len(children) != 1 {
			t.Fatalf("SpawnSubtask: %v", err)
		}
		childID := children[0].ID

		if err := h.stopTask(ctx, parent.ID); err != nil {
			t.Fatalf("stopTask parent: %v", err)
		}
		h.stepUntilParkedOrDone(ctx, parent.ID, 20)

		midParent, err := h.store.GetRun(ctx, parent.ID)
		if err != nil {
			t.Fatalf("GetRun parent: %v", err)
		}
		if midParent.Status != state.RunRunning || midParent.Finishing == nil {
			t.Fatalf("expected parent held at finishing barrier while child running, got status=%s finishing=%v", midParent.Status, midParent.Finishing)
		}

		h.stepUntilParkedOrDone(ctx, childID, 20)
		h.stepUntilParkedOrDone(ctx, parent.ID, 10)
		h.drainUserWakes(ctx)

		finalParent, err := h.store.GetRun(ctx, parent.ID)
		if err != nil {
			t.Fatalf("GetRun final parent: %v", err)
		}
		finalChild, err := h.store.GetRun(ctx, childID)
		if err != nil {
			t.Fatalf("GetRun final child: %v", err)
		}
		if finalChild.Status != state.RunCancelled {
			t.Fatalf("expected child RunCancelled, got %s", finalChild.Status)
		}
		if finalParent.Status != state.RunCancelled || finalParent.Finishing != nil {
			t.Fatalf("expected parent RunCancelled with Finishing==nil after child finished, got status=%s finishing=%v", finalParent.Status, finalParent.Finishing)
		}
		if err := h.driver.CheckInnerInvariants(ctx); err != nil {
			t.Fatalf("CheckInnerInvariants: %v", err)
		}
		if err := h.checker.CheckQuiescence(ctx, h.userTargets(), h.clock.Now()); err != nil {
			t.Fatalf("CheckQuiescence (including CheckLeaseQuiescence): %v", err)
		}
	})
}

func TestBudgetExhaustionSharedFamily(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 105, harnessOpts{
		disableFaults: true,
		budgetUSD:     0.40,
		costPerCall:   0.18,
	})

	var parentID string
	childCalls := 0
	h.driver.Model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
		if runID == parentID {
			return scriptedCall("pm1", "request_work", map[string]any{
				"request": "Do expensive child work",
				"why":     "budget ceiling test",
			}, 0.18), nil, true
		}
		childCalls++
		return scriptedCall(fmt.Sprintf("cm%d", childCalls), inner.ToolNameRead, map[string]any{"key": fmt.Sprintf("k%d", childCalls)}, 0.18), nil, true
	})

	parent := h.startTask(ctx, "Shared family budget test")
	parentID = parent.ID
	h.stepUntilParkedOrDone(ctx, parent.ID, 20)

	children, err := h.driver.SpawnSubtask(ctx, parent.ID, "Expensive child")
	if err != nil {
		t.Fatalf("SpawnSubtask: %v", err)
	}
	childID := children[0].ID

	h.stepUntilParkedOrDone(ctx, childID, 40)
	h.stepUntilParkedOrDone(ctx, parent.ID, 40)

	finalChild, _ := h.store.GetRun(ctx, childID)
	finalParent, _ := h.store.GetRun(ctx, parent.ID)
	if finalChild.Status != state.RunFailed {
		t.Fatalf("expected child RunFailed on budget exhaustion, got %s", finalChild.Status)
	}
	if finalParent.Status != state.RunFailed {
		t.Fatalf("expected parent RunFailed on shared family budget exhaustion, got %s", finalParent.Status)
	}

	calls := h.driver.Model.ModelCalls()
	if len(calls) != 3 {
		t.Fatalf("expected 3 total model calls (1 parent + 2 child), got %d: %+v", len(calls), calls)
	}
	for i, c := range calls {
		if c.SpentBefore >= c.Budget {
			t.Fatalf("model call %d issued at/above budget: %+v", i, c)
		}
	}

	pubs := h.driver.Publications()
	if len(pubs) != 2 {
		t.Fatalf("expected 2 publications, got %d: %+v", len(pubs), pubs)
	}
	for _, p := range pubs {
		if p.Status != state.RunFailed || !p.ExpectedIncomplete || p.BypassRouting {
			t.Fatalf("expected RunFailed with ExpectedIncomplete=true BypassRouting=false, got: %+v", p)
		}
	}

	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("CheckInnerInvariants: %v", err)
	}
}

func TestToolTemporaryRetryAndUncertainReconcile(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 106, harnessOpts{disableFaults: true})

	if _, err := h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{inner.ToolIDWrite},
		ConnectionIDs: []string{},
		ResourceIDs:   []string{"*"},
		ActionClasses: []string{"unknown"},
		Title:         "Allow sim_write for reconcile test",
	}); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	h.driver.Adapter.QueueForcedOutcome(
		inner.ForcedOutcome{Outcome: "temporary_error"},
		inner.ForcedOutcome{Outcome: "confirmed"},
		inner.ForcedOutcome{Outcome: "uncertain", ReconcileMiss: false},
		inner.ForcedOutcome{Outcome: "uncertain", ReconcileMiss: true},
	)

	turn := 0
	h.driver.Model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
		turn++
		switch turn {
		case 1:
			return scriptedCall("t1", inner.ToolNameRead, map[string]any{"key": "a"}, 0.05), nil, true
		case 2:
			return scriptedCall("t2", inner.ToolNameWrite, map[string]any{"target": "a", "value": "1", "resource_id": "a"}, 0.05), nil, true
		case 3:
			return scriptedCall("t3", inner.ToolNameWrite, map[string]any{"target": "b", "value": "2", "resource_id": "b"}, 0.05), nil, true
		default:
			return scriptedCall(fmt.Sprintf("t%d", turn), "complete", map[string]any{
				"answer":   "Handled uncertain write after user answer.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.05), nil, true
		}
	})

	run := h.startTask(ctx, "Test retries and uncertain writes")
	h.stepUntilParkedOrDone(ctx, run.ID, 40)

	midRun, err := h.store.GetRun(ctx, run.ID)
	if err != nil || midRun.Question == nil || len(midRun.Question.Clears) == 0 {
		t.Fatalf("expected task parked on uncertain-action question with Clears, got: %+v (err=%v)", midRun.Question, err)
	}

	if err := h.deliverAnswer(ctx, &permissions.Answer{
		CommandID: h.ids.Next(),
		TaskID:    run.ID,
		Question:  midRun.Question.ID,
		Text:      "Checked externally, proceed.",
	}); err != nil {
		t.Fatalf("deliverAnswer: %v", err)
	}
	h.stepUntilParkedOrDone(ctx, run.ID, 30)

	finalRun, _ := h.store.GetRun(ctx, run.ID)
	if finalRun.Status != state.RunCompleted || finalRun.Error != "" {
		t.Fatalf("expected RunCompleted after user answered uncertain write, got status=%s err=%q", finalRun.Status, finalRun.Error)
	}
	if h.driver.Adapter.ReconcileCount() < 2 {
		t.Fatalf("expected at least 2 Reconcile invocations, got %d", h.driver.Adapter.ReconcileCount())
	}

	readEntries := 0
	writeEntries := 0
	for _, ex := range h.driver.Adapter.Executed() {
		if ex.ToolName == inner.ToolNameRead {
			readEntries++
		} else if ex.ToolName == inner.ToolNameWrite {
			writeEntries++
		}
	}
	if readEntries != 2 {
		t.Fatalf("expected 2 sim_read executions (1 temp error + 1 retry), got %d", readEntries)
	}
	if writeEntries != 2 {
		t.Fatalf("expected 2 sim_write executions (1 per op, zero duplicates), got %d", writeEntries)
	}

	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("CheckInnerInvariants: %v", err)
	}
}

func (h *testHarness) routeOneConversationTurn(ctx context.Context, reply string) bool {
	owner := "sim-router-" + h.ids.Next()
	turnID := h.ids.Next()
	ctrl, turn, err := h.store.AcquireConversation(ctx, owner, turnID)
	if err != nil || turn == nil {
		return false
	}
	for range 4 {
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
		err = h.store.PublishConversation(ctx, fence, *turn, reply)
		if err == nil {
			return true
		}
		if errors.Is(err, state.ErrConversationStale) {
			restarted, _, rErr := h.store.RestartConversation(ctx, fence, *turn)
			if rErr != nil {
				return false
			}
			turn = &restarted
			continue
		}
		return false
	}
	return false
}

func TestUserRoutingPendingHeldPublication(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 107, harnessOpts{disableFaults: true})

	h.driver.Model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
		return scriptedCall("c1", "complete", map[string]any{
			"answer":   "All done.",
			"evidence": []any{},
			"unmet":    []any{},
		}, 0.05), nil, true
	})

	run := h.startTask(ctx, "Hold publication while user input routes")
	if err := h.store.AcceptConversationInput(ctx, h.ids.Next(), "Wait, one quick thing", nil); err != nil {
		t.Fatalf("AcceptConversationInput: %v", err)
	}

	h.stepUntilParkedOrDone(ctx, run.ID, 20)

	heldRun, err := h.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if heldRun.Status != state.RunRunning || !heldRun.PublicationPending {
		t.Fatalf("expected run held with PublicationPending=true, got status=%s pending=%v", heldRun.Status, heldRun.PublicationPending)
	}

	if !h.routeOneConversationTurn(ctx, "Acknowledged.") {
		t.Fatalf("expected conversation turn to be acquired and published")
	}

	h.stepUntilParkedOrDone(ctx, run.ID, 20)
	h.drainUserWakes(ctx)

	finalRun, _ := h.store.GetRun(ctx, run.ID)
	if finalRun.Status != state.RunCompleted {
		t.Fatalf("expected RunCompleted after routing resolved, got %s", finalRun.Status)
	}
	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("CheckInnerInvariants: %v", err)
	}
	if err := h.checker.CheckQuiescence(ctx, h.userTargets(), h.clock.Now()); err != nil {
		t.Fatalf("CheckQuiescence: %v", err)
	}
}

func TestMidStepCrashAndStopPausedRecovery(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 108, harnessOpts{disableFaults: true})

	run1Turn := 0
	var run1ID string
	h.driver.Model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
		if runID != run1ID {
			return scriptedCall("other-read", inner.ToolNameRead, map[string]any{"key": "k2"}, 0.05), nil, true
		}
		run1Turn++
		if run1Turn == 1 {
			return scriptedCall("r1-read", inner.ToolNameRead, map[string]any{"key": "k1"}, 0.05), nil, true
		}
		return scriptedCall("r1-done", "complete", map[string]any{
			"answer":   "Recovered after crash and completed.",
			"evidence": []any{},
			"unmet":    []any{},
		}, 0.05), nil, true
	})

	run1 := h.startTask(ctx, "Task that crashes mid-execution")
	run1ID = run1.ID
	_, _ = h.driver.StepRun(ctx, run1.ID)
	_, _ = h.driver.StepRun(ctx, run1.ID)
	if !h.driver.CrashRun(run1.ID) {
		t.Fatalf("expected CrashRun to claim and abandon active lease")
	}
	ctrlBefore, err := h.store.GetTaskControl(ctx, run1.ID)
	if err != nil {
		t.Fatalf("GetTaskControl before expiry: %v", err)
	}

	h.clock.Advance(10 * time.Second)
	if progressed, err := h.driver.StepRun(ctx, run1.ID); err != nil || progressed {
		t.Fatalf("expected crashed task fenced before 30s expiry, got progressed=%v err=%v", progressed, err)
	}

	h.clock.Advance(state.TaskLease + time.Second)
	h.stepUntilParkedOrDone(ctx, run1.ID, 40)

	r1, err := h.store.GetRun(ctx, run1.ID)
	if err != nil {
		t.Fatalf("GetRun run1: %v", err)
	}
	if r1.Status != state.RunCompleted || r1.CleanupPending {
		t.Fatalf("expected crashed run to recover and complete after lease takeover, got status=%s cleanup=%v err=%q", r1.Status, r1.CleanupPending, r1.Error)
	}
	ctrlAfter, err := h.store.GetTaskControl(ctx, run1.ID)
	if err != nil {
		t.Fatalf("GetTaskControl after takeover: %v", err)
	}
	if ctrlAfter.Fence <= ctrlBefore.Fence {
		t.Fatalf("expected takeover fence > crashed fence (%d), got %d", ctrlBefore.Fence, ctrlAfter.Fence)
	}

	run2 := h.startTask(ctx, "Task stopped while system paused")
	_, _ = h.driver.StepRun(ctx, run2.ID)
	if err := h.setPaused(ctx, true); err != nil {
		t.Fatalf("setPaused(true): %v", err)
	}
	if err := h.stopTask(ctx, run2.ID); err != nil {
		t.Fatalf("stopTask while paused: %v", err)
	}
	callsBefore := len(h.driver.Model.ModelCalls())
	if err := h.driver.RepairAll(ctx); err != nil {
		t.Fatalf("RepairAll paused: %v", err)
	}
	if len(h.driver.Model.ModelCalls()) != callsBefore {
		t.Fatalf("expected zero model calls while paused, got %d -> %d", callsBefore, len(h.driver.Model.ModelCalls()))
	}
	r2, _ := h.store.GetRun(ctx, run2.ID)
	if r2.Status != state.RunCancelled || r2.CleanupPending {
		t.Fatalf("expected paused stop repaired to RunCancelled with CleanupPending=false, got status=%s cleanup=%v", r2.Status, r2.CleanupPending)
	}
	if err := h.setPaused(ctx, false); err != nil {
		t.Fatalf("setPaused(false): %v", err)
	}
	h.drainUserWakes(ctx)

	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("CheckInnerInvariants: %v", err)
	}
	if err := h.checker.CheckQuiescence(ctx, h.userTargets(), h.clock.Now()); err != nil {
		t.Fatalf("CheckQuiescence: %v", err)
	}
}

func TestContextOverflowRetry(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 109, harnessOpts{disableFaults: true})
	h.driver.Model.QueueForcedError(contextview.ErrOverflow)

	run := h.startTask(ctx, "Task encountering context overflow")
	h.stepUntilParkedOrDone(ctx, run.ID, 20)

	finalRun, err := h.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if finalRun.Status != state.RunFailed {
		t.Fatalf("expected RunFailed after unshrinkable context overflow, got %s", finalRun.Status)
	}
	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("CheckInnerInvariants: %v", err)
	}
}

func runRandomizedSim(t *testing.T, seed int64, steps int) *testHarness {
	t.Helper()
	ctx := context.Background()

	h := newTestHarness(t, seed, harnessOpts{
		budgetUSD:   0.60,
		costPerCall: 0.16,
		userSends:   "ask",
		storeFaults: memstore.FaultConfig{
			GetErrProb:        0.008,
			QueryErrProb:      0.008,
			ApplyErrProb:      0.008,
			TxConflictProb:    0.025,
			CommitThenErrProb: 0.01,
		},
		modelCfg:       inner.DefaultModelConfig(),
		adapterCfg:     inner.DefaultAdapterConfig(),
		cleanupErrProb: 0.12,
	})

	h.faults.SetEnabled(false)
	_, _ = h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{inner.ToolIDSend},
		ConnectionIDs: []string{},
		ResourceIDs:   []string{},
		ActionClasses: []string{"sensitive_send"},
		Recipients:    []string{"alice@example.com"},
		Title:         "Allow alice",
	})
	_, _ = h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
		Effect:        "deny",
		ToolIDs:       []string{inner.ToolIDSend},
		ConnectionIDs: []string{},
		ResourceIDs:   []string{},
		ActionClasses: []string{"sensitive_send"},
		Recipients:    []string{"ops@example.com"},
		Title:         "Deny ops",
	})
	h.faults.SetEnabled(true)

	var runIDs []string
	paused := false

	startNew := func(idx int) {
		runID := h.ids.Next()
		cmd := state.Command{
			ID:    h.ids.Next(),
			RunID: runID,
			Kind:  state.CommandStart,
			Text:  fmt.Sprintf("Sim task #%d (seed %d)", idx, seed),
		}
		receipts, err := h.store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
		if err == nil {
			_ = h.checker.RecordCommandReceipts(h.user.ID, receipts)
			runIDs = append(runIDs, runID)
		}
	}

	startNew(1)
	startNew(2)

	for step := 1; step <= steps; step++ {
		h.clock.Advance(time.Duration(100+h.rng.IntN(400)) * time.Millisecond)

		roll := h.rng.IntN(100)
		switch {
		case roll < 46:
			leases, err := h.backend.ListTaskLeases(ctx, h.user.ID)
			if err == nil {
				now := h.clock.Now()
				var active []string
				for _, l := range leases {
					if l.Runnable && !l.WakeAt.After(now) && (l.LeaseUntil == nil || !l.LeaseUntil.After(now)) {
						active = append(active, l.RunID)
					}
				}
				if len(active) > 0 {
					pick := active[h.rng.IntN(len(active))]
					_, _ = h.driver.StepRun(ctx, pick)
				}
			}

		case roll < 60:
			if pending, _, err := h.perms.Interactions(ctx, "pending", "", "", 20); err == nil && len(pending) > 0 {
				card := pending[h.rng.IntN(len(pending))]
				var resp permissions.Response
				switch card.Kind {
				case "approval":
					if h.rng.IntN(5) == 0 {
						resp = permissions.Response{Kind: "decline", ActionDigest: card.ActionDigest}
					} else {
						resp = permissions.Response{Kind: "approve", ActionDigest: card.ActionDigest}
					}
				default:
					resp = permissions.Response{Kind: "answer", Text: fmt.Sprintf("User answer at step %d", step)}
				}
				if _, ans, err := h.perms.Respond(ctx, card.ID, h.nextReqID(), card.Revision, resp); err == nil && ans != nil {
					if h.deliverAnswer(ctx, ans) == nil {
						h.perms.MarkDelivered(ctx, card.ID)
					}
				}
			}

		case roll < 70:
			if open, err := h.store.OpenTasks(ctx); err == nil {
				for _, r := range open {
					if r.ParentID == "" && r.Question != nil && r.Question.Kind == state.QuestionWork && r.Question.ResolvedBy == "" {
						if children, err := h.driver.SpawnSubtask(ctx, r.ID, fmt.Sprintf("Subtask for %s at step %d", r.ID, step)); err == nil {
							for _, ch := range children {
								runIDs = append(runIDs, ch.ID)
							}
						}
						break
					}
				}
			}

		case roll < 77:
			if open, err := h.store.OpenTasks(ctx); err == nil && len(open) > 0 {
				target := open[h.rng.IntN(len(open))]
				_ = h.steerTask(ctx, target.ID, fmt.Sprintf("Steer at step %d", step))
			}

		case roll < 81:
			if open, err := h.store.OpenTasks(ctx); err == nil && len(open) > 0 {
				target := open[h.rng.IntN(len(open))]
				_ = h.stopTask(ctx, target.ID)
			}

		case roll < 87:
			if len(runIDs) < 12 {
				startNew(len(runIDs) + 1)
			}

		case roll < 92:
			_ = h.driver.RepairAll(ctx)

		case roll < 94:
			if open, err := h.store.OpenTasks(ctx); err == nil && len(open) > 0 {
				victim := open[h.rng.IntN(len(open))]
				h.driver.CrashRun(victim.ID)
			}

		case roll < 97:
			paused = !paused
			_ = h.setPaused(ctx, paused)

		default:
			if !h.routeOneConversationTurn(ctx, "Routed.") {
				_ = h.store.AcceptConversationInput(ctx, h.ids.Next(), fmt.Sprintf("Chat ping %d", step), nil)
			}
		}

		if step%25 == 0 {
			if err := h.driver.CheckInnerInvariants(ctx); err != nil {
				t.Fatalf("seed=%d step=%d CheckInnerInvariants failed: %v", seed, step, err)
			}
		}
	}

	h.faults.SetEnabled(false)
	h.driver.SetFaultsEnabled(false)
	if err := h.setPaused(ctx, false); err != nil {
		t.Fatalf("unpause: %v", err)
	}
	h.clock.Advance(state.TaskLease + time.Second)

	for drain := 0; drain < 200; drain++ {
		h.clock.Advance(200 * time.Millisecond)

		h.drainUserWakes(ctx)

		if answers, ids, err := h.perms.UndeliveredAnswers(ctx); err == nil {
			for i := range answers {
				if h.deliverAnswer(ctx, &answers[i]) == nil {
					h.perms.MarkDelivered(ctx, ids[i])
				}
			}
		}

		if pending, _, err := h.perms.Interactions(ctx, "pending", "", "", 50); err == nil {
			for _, card := range pending {
				var resp permissions.Response
				if card.Kind == "approval" {
					resp = permissions.Response{Kind: "approve", ActionDigest: card.ActionDigest}
				} else {
					resp = permissions.Response{Kind: "answer", Text: "Drain answer"}
				}
				if _, ans, err := h.perms.Respond(ctx, card.ID, h.nextReqID(), card.Revision, resp); err == nil && ans != nil {
					if h.deliverAnswer(ctx, ans) == nil {
						h.perms.MarkDelivered(ctx, card.ID)
					}
				}
			}
		}

		if open, err := h.store.OpenTasks(ctx); err == nil {
			for _, r := range open {
				if r.Question != nil && r.Question.ResolvedBy == "" {
					if r.Question.Kind == state.QuestionWork && r.ParentID == "" {
						_, _ = h.driver.SpawnSubtask(ctx, r.ID, "Drain subtask")
					} else {
						_ = h.deliverAnswer(ctx, &permissions.Answer{
							CommandID: h.ids.Next(),
							TaskID:    r.ID,
							Question:  r.Question.ID,
							Text:      "Drain question answer",
						})
					}
				}
			}
		}

		_ = h.driver.RepairAll(ctx)

		progressedAny := false
		for {
			_, progressed, err := h.driver.StepAny(ctx)
			if err != nil {
				t.Fatalf("seed=%d drain=%d StepAny error: %v", seed, drain, err)
			}
			if !progressed {
				break
			}
			progressedAny = true
		}

		h.drainUserWakes(ctx)

		open, _ := h.store.OpenTasks(ctx)
		cleanup, _ := h.store.CleanupTasks(ctx)
		pendingCmds, _ := h.store.Pending(ctx, "")
		leases, _ := h.backend.ListTaskLeases(ctx, h.user.ID)
		routingPending, _ := h.store.UserRoutingPending(ctx)
		if !progressedAny && len(open) == 0 && len(cleanup) == 0 && len(pendingCmds.Commands) == 0 && len(leases) == 0 && !routingPending {
			break
		}
		if !progressedAny {
			if nextAt, ok := h.driver.NextWakeTime(ctx); ok && nextAt.After(h.clock.Now()) {
				h.clock.Advance(nextAt.Sub(h.clock.Now()) + time.Millisecond)
			} else if routingPending {
				h.clock.Advance(5 * time.Second)
			}
		}
	}

	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("seed=%d final CheckInnerInvariants failed: %v", seed, err)
	}
	if err := h.checker.CheckSafety(ctx, h.userTargets()); err != nil {
		t.Fatalf("seed=%d final CheckSafety failed: %v", seed, err)
	}
	if err := h.checker.CheckQuiescence(ctx, h.userTargets(), h.clock.Now()); err != nil {
		open, _ := h.store.OpenTasks(ctx)
		leases, _ := h.backend.ListTaskLeases(ctx, h.user.ID)
		t.Fatalf("seed=%d final CheckQuiescence failed (open=%+v leases=%+v): %v", seed, open, leases, err)
	}

	return h
}

func TestMultiSeedRandomizedInnerLoop(t *testing.T) {
	seeds := []int64{1, 2, 3, 7, 13, 19, 29, 42, 73, 99, 256, 1024, 2026, 4096, 7777, 12345}
	var totalModelCalls, totalExecuted, totalWrites, totalPubs int
	var totalReconciles int64
	for _, seed := range seeds {
		h := runRandomizedSim(t, seed, 180)
		totalModelCalls += len(h.driver.Model.ModelCalls())
		totalPubs += len(h.driver.Publications())
		totalReconciles += h.driver.Adapter.ReconcileCount()
		for _, ex := range h.driver.Adapter.Executed() {
			totalExecuted++
			if ex.ToolName == inner.ToolNameWrite || ex.ToolName == inner.ToolNameSend {
				totalWrites++
			}
		}
	}
	if totalModelCalls < 50 || totalExecuted < 20 || totalWrites < 3 || totalPubs < 20 {
		t.Fatalf("expected substantial inner-loop coverage across seeds, got modelCalls=%d executed=%d writes=%d pubs=%d reconciles=%d",
			totalModelCalls, totalExecuted, totalWrites, totalPubs, totalReconciles)
	}
	t.Logf("verified %d seeds: modelCalls=%d executed=%d writes=%d pubs=%d reconciles=%d",
		len(seeds), totalModelCalls, totalExecuted, totalWrites, totalPubs, totalReconciles)
}

func TestBitForBitDeterminism(t *testing.T) {
	for _, seed := range []int64{42, 2026, 9001} {
		h1 := runRandomizedSim(t, seed, 120)
		h2 := runRandomizedSim(t, seed, 120)

		d1 := memstore.CombinedDigest(h1.backend, h1.artifacts)
		d2 := memstore.CombinedDigest(h2.backend, h2.artifacts)
		if d1 != d2 {
			t.Fatalf("seed=%d memstore.CombinedDigest mismatch:\n  run1=%s\n  run2=%s", seed, d1, d2)
		}

		a1, a2 := h1.driver.Adapter.Digest(), h2.driver.Adapter.Digest()
		if a1 != a2 {
			t.Fatalf("seed=%d Adapter.Digest mismatch:\n  run1=%s\n  run2=%s", seed, a1, a2)
		}

		m1, m2 := h1.driver.Model.Digest(), h2.driver.Model.Digest()
		if m1 != m2 {
			t.Fatalf("seed=%d Model.Digest mismatch:\n  run1=%s\n  run2=%s", seed, m1, m2)
		}

		ex1, _ := json.Marshal(h1.driver.Adapter.Executed())
		ex2, _ := json.Marshal(h2.driver.Adapter.Executed())
		if string(ex1) != string(ex2) {
			t.Fatalf("seed=%d Executed ledger bytes differ", seed)
		}

		mc1, _ := json.Marshal(h1.driver.Model.ModelCalls())
		mc2, _ := json.Marshal(h2.driver.Model.ModelCalls())
		if string(mc1) != string(mc2) {
			t.Fatalf("seed=%d ModelCalls ledger bytes differ", seed)
		}
	}
}
