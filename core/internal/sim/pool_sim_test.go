package sim_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/app"
	"nori/internal/conversation"
	"nori/internal/platform"
	"nori/internal/scheduler"
	"nori/internal/settings"
	"nori/internal/sim/env"
	"nori/internal/sim/invariants"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/transcript"
	"nori/internal/users"
	"nori/internal/worker"
)

type simPoolOuterModel struct {
	seq atomic.Int64
	now func() time.Time
}

func (m *simPoolOuterModel) Name() string { return "sim-pool-outer" }

func (m *simPoolOuterModel) Respond(ctx context.Context, req conversation.ReplyRequest) (conversation.Reply, error) {
	if err := ctx.Err(); err != nil {
		return conversation.Reply{}, err
	}
	id := m.seq.Add(1)
	now := time.Now().UTC()
	if m.now != nil {
		now = m.now().UTC()
	}
	usage := state.Usage{
		ID:            uuid.NewString(),
		Provider:      "outer",
		Model:         m.Name(),
		CostStatus:    "unknown",
		RequestStatus: "pending",
		CreatedAt:     now,
	}
	if req.RecordUsage != nil {
		if err := req.RecordUsage(ctx, usage); err != nil {
			usage.RequestStatus = "not_sent"
			return conversation.Reply{Usage: usage}, err
		}
	}

	var calls []conversation.ToolCall
	if !req.NoDispatch && strings.Contains(req.TelemetryInput, "DISPATCH_TASK:") {
		tc := conversation.ToolCall{
			ID:   fmt.Sprintf("call_disp_%d", id),
			Type: "function",
		}
		tc.Function.Name = "dispatch"
		args, _ := json.Marshal(map[string]any{
			"request": "Handle dispatched background work: " + req.TelemetryInput,
		})
		tc.Function.Arguments = string(args)
		calls = append(calls, tc)
	}

	text := fmt.Sprintf("Acknowledged turn %d.", id)
	if len(calls) > 0 {
		text = fmt.Sprintf("Started background task for turn %d.", id)
	}
	inTok, outTok, totTok := int64(32), int64(16), int64(48)
	cost := 0.0001
	usage.InputTokens = &inTok
	usage.OutputTokens = &outTok
	usage.TotalTokens = &totTok
	usage.CostUSD = &cost
	usage.CostStatus = "calculated"
	usage.RequestStatus = "received"
	if req.RecordUsage != nil {
		_ = req.RecordUsage(ctx, usage)
	}
	if req.Stream != nil && text != "" {
		if err := req.Stream(text); err != nil {
			return conversation.Reply{Usage: usage}, err
		}
	}
	msgPayload := map[string]any{"role": "assistant", "content": text}
	if len(calls) > 0 {
		msgPayload["tool_calls"] = calls
	}
	msg, _ := json.Marshal(msgPayload)
	return conversation.Reply{
		Usage:   usage,
		Message: msg,
		Text:    text,
		Calls:   calls,
	}, nil
}

type simPoolInnerModel struct {
	seq atomic.Int64
}

func (m *simPoolInnerModel) Provider() string { return "sim" }
func (m *simPoolInnerModel) Name() string     { return "sim-pool-inner" }

func (m *simPoolInnerModel) Respond(ctx context.Context, req agent.Request) (agent.Response, error) {
	if err := ctx.Err(); err != nil {
		return agent.Response{NotSent: true}, err
	}
	n := m.seq.Add(1)

	// Detect whether this task asks a question once before completing.
	askMode := false
	alreadyAnswered := false
	for _, msg := range req.Messages {
		for _, p := range msg.Parts {
			if strings.Contains(p.Text, "ASK_ONCE:") {
				askMode = true
			}
			if strings.Contains(p.Text, "ANSWER_PROVIDED") || (p.ToolResult != nil && p.ToolResult.Name == "request_input") {
				alreadyAnswered = true
			}
		}
	}

	if askMode && !alreadyAnswered {
		call := transcript.ToolCall{
			ID:   fmt.Sprintf("ask_%d", n),
			Name: "request_input",
			Args: map[string]any{"question": "Please confirm parameters for multi-replica test."},
		}
		return agent.Response{
			Message:   &transcript.Message{Role: transcript.RoleModel, Parts: []transcript.Part{{ToolCall: &call}}},
			Calls:     []transcript.ToolCall{call},
			Cost:      0.01,
			CostKnown: true,
		}, nil
	}

	call := transcript.ToolCall{
		ID:   fmt.Sprintf("done_%d", n),
		Name: "complete",
		Args: map[string]any{
			"answer":   fmt.Sprintf("Completed background task step %d cleanly.", n),
			"evidence": []any{},
			"unmet":    []any{},
		},
	}
	return agent.Response{
		Message:   &transcript.Message{Role: transcript.RoleModel, Parts: []transcript.Part{{ToolCall: &call}}},
		Calls:     []transcript.ToolCall{call},
		Cost:      0.01,
		CostKnown: true,
	}, nil
}

func (m *simPoolInnerModel) Inspect(context.Context, agent.InspectRequest) (agent.Inspection, error) {
	return agent.Inspection{Observation: "ok", CostKnown: true}, nil
}

// TestMultiReplicaWorkerPoolSim drives 3 competing real worker.Pool replicas over a shared
// memstore.Backend with live storage + lease fault injection, dropped pg_notify + resync,
// global pause + StopPaused, missing/orphan lease repair via real app.App.RepairLeases,
// real task.Runner, real conversation.Runner, real scheduler.Service, and mid-flight replica
// graceful drain — verifying full Safety + Quiescence (with CheckLeases: true) at convergence.
func TestMultiReplicaWorkerPoolSim(t *testing.T) {
	silenceStdLog(t)
	ctx := context.Background()

	clock := env.NewClock(time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC))
	rng := rand.New(rand.NewPCG(20261122, 42))
	faultCfg := memstore.FaultConfig{
		GetErrProb:        0.04,
		QueryErrProb:      0.04,
		ApplyErrProb:      0.04,
		TxConflictProb:    0.06,
		CommitThenErrProb: 0.03,
		LeaseErrProb:      0.08,
	}
	inj := memstore.NewFaultInjector(faultCfg, rng)
	inj.SetEnabled(false) // Enable after initial user & service bootstrap.

	backend := memstore.New(
		memstore.WithClock(clock.Now),
		memstore.WithFaultInjector(inj),
	)
	artifactsByUser := map[string]*memstore.Artifacts{}
	var artMu sync.Mutex
	storage := platform.NewStorage(backend, func(u users.User) state.ArtifactStore {
		artMu.Lock()
		defer artMu.Unlock()
		a, ok := artifactsByUser[u.ID]
		if !ok {
			a = memstore.NewArtifacts(memstore.WithArtifactsFaultInjector(inj))
			artifactsByUser[u.ID] = a
		}
		return a
	})
	storage.Now = clock.Now

	cfg := settings.Settings{
		Cloud:         "local",
		Role:          "all",
		TaskBudgetUSD: 2.0,
		Features:      settings.Features{DisableJev: true},
		Models: settings.Models{
			OpenAIKey:     "sim-openai",
			OpenRouterKey: "sim-openrouter",
			Outer:         "sim-pool-outer",
			Inner:         "sim-pool-inner",
		},
	}

	outerModel := &simPoolOuterModel{now: clock.Now}
	innerModel := &simPoolInnerModel{}

	svc := app.OpenServiceWithStorage(storage, nil, cfg)
	svc.Now = clock.Now
	svc.ConfigureApp = func(_ users.User, a *app.App) {
		if a.Scheduler != nil {
			a.Scheduler.Now = clock.Now
		}
		if a.Assistant != nil && a.Assistant.Permissions != nil {
			a.Assistant.Permissions.Now = clock.Now
		}
		if a.Tasks != nil {
			runner := conversation.New(outerModel, nil, innerModel, a.Tasks)
			runner.Clock = conversation.Clock{Now: clock.Now, Location: time.UTC}
			a.Runner = runner
		}
		if a.TaskActivities != nil {
			a.TaskActivities.Agent = agent.New(a.Store, innerModel)
			a.TaskActivities.Agent.Now = clock.Now
		}
	}
	t.Cleanup(svc.Close)

	// Exercise BOTH users.LegacyID ("owner", where ID != DataOwner()) AND a UUID user!
	userList := []users.User{
		{ID: users.LegacyID, Email: "legacy@example.com", DisplayName: "Legacy User", TimeZone: "UTC"},
		{ID: uuid.NewString(), Email: "second@example.com", DisplayName: "Second User", TimeZone: "UTC"},
	}
	for _, u := range userList {
		if err := svc.Users.Create(ctx, u); err != nil {
			t.Fatalf("create user %s: %v", u.ID, err)
		}
	}

	// Acquire Apps upfront for workload injection & verification (kept alive for test duration).
	userApps := make([]*app.App, len(userList))
	for i, u := range userList {
		a, rel, err := svc.Acquire(ctx, u)
		if err != nil {
			t.Fatalf("Acquire %s: %v", u.ID, err)
		}
		t.Cleanup(rel)
		userApps[i] = a
	}

	// Spin up 3 competing real worker.Pool replicas (ZERO callback stubs!).
	const numReplicas = 3
	pools := make([]*worker.Pool, numReplicas)
	cancels := make([]context.CancelFunc, numReplicas)
	dones := make([]chan error, numReplicas)

	for i := range numReplicas {
		p, err := worker.New(svc, worker.Options{
			Owner:                   fmt.Sprintf("sim-replica-%d", i),
			TaskConcurrency:         4,
			ConversationConcurrency: 2,
			SchedulerConcurrency:    2,
			PollInterval:            25 * time.Millisecond,
			RepairInterval:          75 * time.Millisecond,
			RenewInterval:           150 * time.Millisecond,
			DrainTimeout:            3 * time.Second,
			CancelGrace:             500 * time.Millisecond,
			Now:                     clock.Now,
		})
		if err != nil {
			t.Fatalf("worker.New replica %d: %v", i, err)
		}
		pools[i] = p
		rCtx, rCancel := context.WithCancel(ctx)
		cancels[i] = rCancel
		dones[i] = make(chan error, 1)
		go func(idx int) {
			dones[idx] <- pools[idx].Run(rCtx)
		}(i)
	}
	t.Cleanup(func() {
		for i := range numReplicas {
			if cancels[i] != nil {
				cancels[i]()
			}
		}
		for i := range numReplicas {
			if dones[i] != nil {
				<-dones[i]
			}
		}
	})

	if err := backend.WaitForWakeSubscribers(ctx, numReplicas); err != nil {
		t.Fatalf("WaitForWakeSubscribers(%d): %v", numReplicas, err)
	}

	// Helper retrying client-side workload operations through transient injected faults.
	retryOp := func(label string, fn func() error) {
		t.Helper()
		for range 40 {
			if err := fn(); err == nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		inj.SetEnabled(false)
		err := fn()
		inj.SetEnabled(true)
		if err != nil {
			t.Fatalf("%s failed even without faults: %v", label, err)
		}
	}

	// Enable full storage + lease fault injection across all 3 competing replicas!
	inj.SetEnabled(true)

	// 1. Submit natural chat inputs (some plain, some dispatching background tasks) for both users.
	for idx, uApp := range userApps {
		plainID := uuid.NewString()
		retryOp("chat plain", func() error {
			return uApp.Store.AcceptConversationInput(ctx, plainID, fmt.Sprintf("Hello from user %d", idx), nil)
		})
		dispID := uuid.NewString()
		retryOp("chat dispatch", func() error {
			return uApp.Store.AcceptConversationInput(ctx, dispID, fmt.Sprintf("DISPATCH_TASK: research topic %d", idx), nil)
		})
	}

	// 2. Start direct tasks: normal, ask-once question task, a task to stop mid-flight, and a task to stop while paused.
	askTaskIDs := make([]string, len(userApps))
	stopTaskIDs := make([]string, len(userApps))
	missingLeaseTaskIDs := make([]string, len(userApps))
	for idx, uApp := range userApps {
		askID, askCmdID := uuid.NewString(), uuid.NewString()
		askTaskIDs[idx] = askID
		retryOp("start ask task", func() error {
			_, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
				ID: askCmdID, RunID: askID, Kind: state.CommandStart, Text: "ASK_ONCE: verify user preference",
			}}, nil)
			return err
		})

		stopID, stopStartCmdID, stopCmdID := uuid.NewString(), uuid.NewString(), uuid.NewString()
		stopTaskIDs[idx] = stopID
		retryOp("start stop task", func() error {
			_, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
				ID: stopStartCmdID, RunID: stopID, Kind: state.CommandStart, Text: "Task to cancel",
			}}, nil)
			return err
		})
		retryOp("stop task", func() error {
			_, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
				ID: stopCmdID, RunID: stopID, Kind: state.CommandStop,
			}}, nil)
			return err
		})

		// Create a task and deliberately delete its lease so real app.RepairLeases must restore it under faults!
		missID, missCmdID := uuid.NewString(), uuid.NewString()
		missingLeaseTaskIDs[idx] = missID
		retryOp("start missing-lease task", func() error {
			_, err := uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
				ID: missCmdID, RunID: missID, Kind: state.CommandStart, Text: "Task repaired by RepairLeases",
			}}, nil)
			return err
		})
		_ = func() error {
			_, err := backend.DeleteTaskLease(ctx, userList[idx].ID, missID, 0)
			return err
		}()
		// Inject an orphan lease for a non-existent run so RepairLeases must delete it under faults!
		_ = backend.WakeTask(ctx, userList[idx].ID, "orphan-run-"+uuid.NewString()[:8], time.Time{})
	}

	// Start a task before pausing so we can test CommandStop + StopPaused while globally paused!
	pausedStopID, pausedStartCmdID := uuid.NewString(), uuid.NewString()
	retryOp("start paused-stop task", func() error {
		_, err := userApps[0].Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: pausedStartCmdID, RunID: pausedStopID, Kind: state.CommandStart, Text: "ASK_ONCE: task stopped while globally paused",
		}}, nil)
		return err
	})

	// 3. Create a "once" automation for each user and fire run_now (testing run_now + Publish -> LaneScheduler + Inbox delivery under faults!).
	for idx, uApp := range userApps {
		farFuture := clock.Now().Add(48 * time.Hour)
		var auto scheduler.Automation
		reqID := uuid.NewString()
		retryOp("save automation", func() error {
			rcpt, err := uApp.Scheduler.Save(ctx, "", reqID, 0, scheduler.Editable{
				Title:          fmt.Sprintf("Multi-replica once report %d", idx),
				Instruction:    "Generate report cleanly.",
				Trigger:        scheduler.Trigger{Kind: "once", At: &farFuture},
				MissedRun:      "skip",
				MaxActiveRuns:  1,
				BudgetUSD:      0.5,
				DestinationIDs: []string{scheduler.InAppDestination},
			})
			if err != nil {
				if existing, gerr := uApp.Scheduler.Created(ctx, reqID); gerr == nil {
					auto = existing
					return nil
				}
				return err
			}
			return json.Unmarshal(rcpt.Resource, &auto)
		})
		actReqID := uuid.NewString()
		retryOp("run_now automation", func() error {
			cur, gerr := uApp.Scheduler.Get(ctx, auto.ID)
			if gerr != nil {
				return gerr
			}
			if cur.LastOccurrenceID != "" {
				return nil
			}
			_, err := uApp.Scheduler.Act(ctx, auto.ID, actReqID, cur.Revision, scheduler.Action{Action: "run_now"})
			return err
		})
	}

	// 4. Chaos wave: drop pg_notify events temporarily, toggle global pause + StopPaused, resync, and gracefully drain replica-0!
	backend.SetDropWakeEvents(true)
	time.Sleep(60 * time.Millisecond)

	// Toggle global pause, then issue CommandStop on pausedStopID -> exercise StopPaused under RepairLeases + faults!
	retryOp("pause system", func() error {
		return backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "system/control",
			Fields: map[string]any{"paused": true},
		})
	})
	pausedStopCmdID := uuid.NewString()
	retryOp("stop paused task", func() error {
		_, err := userApps[0].Store.AcceptCommandBatch(ctx, []state.Command{{
			ID: pausedStopCmdID, RunID: pausedStopID, Kind: state.CommandStop,
		}}, nil)
		return err
	})
	_ = pools[1].RepairOnce(ctx)
	time.Sleep(60 * time.Millisecond)

	// Gracefully drain replica-0 mid-flight while faults are active!
	cancels[0]()
	if err := <-dones[0]; err != nil {
		t.Fatalf("replica-0 graceful drain error: %v", err)
	}
	cancels[0] = nil
	dones[0] = nil

	// Unpause and emit resync, restoring wake delivery on surviving replicas 1 and 2.
	retryOp("unpause system", func() error {
		return backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   "system/control",
			Fields: map[string]any{"paused": false},
		})
	})
	backend.SetDropWakeEvents(false)
	backend.EmitResync()
	time.Sleep(80 * time.Millisecond)

	// 5. Disable faults for convergence drain, advance virtual clock past any fault-leaked 30s
	// TaskLease / ConversationLease / TaskControl leases, and let surviving replicas converge!
	inj.SetEnabled(false)
	if err := backend.Apply(ctx, docstore.Mutation{
		Kind:   "set",
		Path:   "system/control",
		Fields: map[string]any{"paused": false},
	}); err != nil {
		t.Fatalf("final unpause: %v", err)
	}
	clock.Advance(state.TaskLease + state.ConversationLease + 5*time.Second)
	_ = pools[1].RepairOnce(ctx)
	backend.EmitResync()

	// Wait for all ask-once tasks to be answered, all tasks/conversations/occurrences/wakes to settle!
	deadline := time.Now().Add(15 * time.Second)
	for round := 0; time.Now().Before(deadline); round++ {
		if round > 0 && round%5 == 0 {
			// Advance virtual clock past any pending TaskLease backoff or RecoveryUntil (240s)
			// or unexpired lease so the pool immediately completes D-053 recovery.
			now := clock.Now()
			target := now.Add(5 * time.Second)
			if leases, lerr := backend.ListTaskLeases(ctx, ""); lerr == nil {
				for _, l := range leases {
					if l.Runnable && l.WakeAt.After(target) {
						target = l.WakeAt.Add(time.Second)
					}
					if l.LeaseUntil != nil && l.LeaseUntil.After(target) {
						target = l.LeaseUntil.Add(time.Second)
					}
				}
			}
			clock.Advance(target.Sub(now))
			_ = pools[1].RepairOnce(ctx)
			backend.EmitResync()
		}
		// Answer any open unanswered questions so ASK_ONCE tasks complete, and if a
		// chaos-phase outer turn failed leaving RoutingPending=true (with no pending
		// inputs), send one follow-up user turn so WaitRoutingPending tasks wake cleanly!
		for _, uApp := range userApps {
			if conv, _, cerr := uApp.Store.ConversationStatus(ctx); cerr == nil && conv.RoutingPending() && !conv.Pending() && conv.Active == "" {
				_ = uApp.Store.AcceptConversationInput(ctx, uuid.NewString(), "Follow-up to complete routing after fault recovery", nil)
			}
			open, err := uApp.Store.OpenTasks(ctx)
			if err != nil {
				continue
			}
			for _, r := range open {
				if r.Question != nil && r.Question.ResolvedBy == "" && r.Question.Revision == r.Revision {
					_, _ = uApp.Store.AcceptCommandBatch(ctx, []state.Command{{
						ID:         uuid.NewString(),
						RunID:      r.ID,
						Kind:       state.CommandAnswer,
						QuestionID: r.Question.ID,
						Text:       "ANSWER_PROVIDED: confirmed",
					}}, nil)
				}
			}
		}

		// Check full convergence across tasks, conversations, inbox delivery, leases, and user wakes.
		allSettled := true
		for i, uApp := range userApps {
			open, err := uApp.Store.OpenTasks(ctx)
			if err != nil || len(open) > 0 {
				allSettled = false
				break
			}
			cleanup, err := uApp.Store.CleanupTasks(ctx)
			if err != nil || len(cleanup) > 0 {
				allSettled = false
				break
			}
			conv, _, err := uApp.Store.ConversationStatus(ctx)
			if err != nil || conv.Pending() || conv.Active != "" {
				allSettled = false
				break
			}
			inbox, err := uApp.Scheduler.Inbox(ctx, false)
			if err != nil || len(inbox) < 1 {
				allSettled = false
				break
			}
			leases, err := backend.ListTaskLeases(ctx, userList[i].ID)
			if err != nil || len(leases) > 0 {
				allSettled = false
				break
			}
			wakes, err := backend.ListUserWakes(ctx, userList[i].ID)
			if err != nil {
				allSettled = false
				break
			}
			now := clock.Now()
			for _, w := range wakes {
				if w.Owner != "" || (w.Runnable && !w.WakeAt.After(now)) {
					allSettled = false
					break
				}
			}
			if !allSettled {
				break
			}
		}
		if allSettled {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Gracefully shut down surviving replicas before running full Safety + Quiescence audit.
	for i := 1; i < numReplicas; i++ {
		cancels[i]()
		if err := <-dones[i]; err != nil {
			t.Fatalf("replica-%d graceful drain error: %v", i, err)
		}
		cancels[i] = nil
		dones[i] = nil
	}

	// Build UserTargets and run full Safety + Quiescence (CheckLeases: true)!
	targets := make([]invariants.UserTarget, len(userList))
	for i, u := range userList {
		targets[i] = invariants.UserTarget{
			User:        u,
			Store:       userApps[i].Store,
			Permissions: userApps[i].Assistant.Permissions,
		}
	}
	checker := invariants.New(invariants.Config{
		Backend:     backend,
		CheckLeases: true,
	})
	now := clock.Now()
	if err := checker.CheckSafety(ctx, targets); err != nil {
		t.Fatalf("multi-replica CheckSafety failed:\n%v", err)
	}
	if err := checker.CheckQuiescence(ctx, targets, now); err != nil {
		t.Fatalf("multi-replica CheckQuiescence failed:\n%v", err)
	}

	// Verify meaningful fault injection + functional completion coverage.
	stats := backend.FaultStats()
	if stats.LeaseErrors == 0 || stats.Total() == 0 {
		t.Fatalf("expected injected storage + lease faults to fire, got %+v", stats)
	}
	for i, uApp := range userApps {
		inbox, err := uApp.Scheduler.Inbox(ctx, false)
		if err != nil || len(inbox) < 1 {
			autos, _ := uApp.Scheduler.List(ctx, "")
			var occs []scheduler.Occurrence
			var runs []state.Run
			for _, a := range autos {
				o, _ := uApp.Scheduler.Occurrences(ctx, a.ID)
				occs = append(occs, o...)
				for _, oo := range o {
					for _, tid := range oo.TaskIDs {
						r, rerr := uApp.Store.GetRun(ctx, tid)
						t.Logf("occ %s task %s: status=%s err=%v run=%+v", oo.ID, tid, r.Status, rerr, r)
						runs = append(runs, r)
					}
				}
			}
			leases, _ := backend.ListTaskLeases(ctx, userList[i].ID)
			wakes, _ := backend.ListUserWakes(ctx, userList[i].ID)
			t.Fatalf("user %d (%s): expected delivered automation InboxItem, got %d (err=%v now=%s leases=%+v wakes=%+v occs=%+v runs=%d)", i, userList[i].ID, len(inbox), err, clock.Now(), leases, wakes, occs, len(runs))
		}
		askRun, err := uApp.Store.GetRun(ctx, askTaskIDs[i])
		if err != nil || (askRun.Status != state.RunCompleted && askRun.Status != state.RunFailed) || askRun.CleanupPending {
			t.Fatalf("user %d: expected ask-once task terminal & clean, got %+v (err=%v)", i, askRun, err)
		}
		repairedRun, err := uApp.Store.GetRun(ctx, missingLeaseTaskIDs[i])
		if err != nil || (repairedRun.Status != state.RunCompleted && repairedRun.Status != state.RunFailed) || repairedRun.CleanupPending {
			t.Fatalf("user %d: expected repaired missing-lease task terminal & clean, got %+v (err=%v)", i, repairedRun, err)
		}
	}
	pausedRun, err := userApps[0].Store.GetRun(ctx, pausedStopID)
	if err != nil || pausedRun.Status != state.RunCancelled || pausedRun.CleanupPending {
		t.Fatalf("expected paused-stop task cleanly cancelled, got %+v (err=%v)", pausedRun, err)
	}
	t.Logf("multi-replica worker.Pool sim passed (replicas=%d users=%d leaseFaults=%d totalFaults=%d)",
		numReplicas, len(userList), stats.LeaseErrors, stats.Total())
}
