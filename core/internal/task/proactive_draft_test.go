package task_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/state"
	"nori/internal/task"
)

type emailCaptureAdapter struct {
	mu           sync.Mutex
	executedArgs []map[string]any
	lastReadOnly bool
}

func (a *emailCaptureAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
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

func (a *emailCaptureAdapter) Execute(_ context.Context, call invoke.Call, _ invoke.Prepared) invoke.Result {
	a.mu.Lock()
	defer a.mu.Unlock()
	cp := map[string]any{}
	for k, v := range call.Args {
		cp[k] = v
	}
	a.executedArgs = append(a.executedArgs, cp)
	a.lastReadOnly = call.ReadOnly
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"sent": true, "subject": call.Args["subject"], "body": call.Args["body"]}}
}

func TestEditedApprovalReplacementExecutesWithApprovedReplacementPayload(t *testing.T) {
	ctx := context.Background()
	sendDef := sealExternalDef(t, "mock/tools/email_send", "email_send", extensions.EffectSensitiveSend)

	f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		switch callNum {
		case 1, 2:
			return toolCallResponse("c-send-"+string(rune('0'+callNum)), "email_send", map[string]any{
				"to":      "alice@example.com",
				"subject": "Original Draft Subject",
				"body":    "Original draft body.",
			}, 0.05), nil
		default:
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Sent.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		}
	})
	adapter := &emailCaptureAdapter{}
	f.extSource.defs = []extensions.Definition{sendDef}
	f.adapters.Register(extensions.AdapterNative, "mock", adapter)

	run := f.startTask(ctx, "run-edit-exec", "Send an email to alice@example.com")
	outcome1, err := f.claimAndRun(ctx, run.ID, "worker-1")
	if err != nil || outcome1 != task.OutcomeParked {
		t.Fatalf("Run #1 = (%s, %v), want (parked, nil)", outcome1, err)
	}

	pending, _, err := f.perms.Interactions(ctx, "pending", run.ID, "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending interactions = (%+v, %v), want 1", pending, err)
	}
	inter := pending[0]

	_, ans, err := f.perms.Respond(ctx, inter.ID, "req-edit-1", inter.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: inter.ActionDigest,
		EditedFields: map[string]string{
			"subject": "Edited Final Subject",
			"body":    "Edited final body.",
		},
	})
	if err != nil || ans == nil {
		t.Fatalf("Respond edited = (%+v, %v)", ans, err)
	}

	if _, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
		ID: ans.CommandID, RunID: ans.TaskID, Kind: state.CommandAnswer, QuestionID: ans.Question, Text: ans.Text,
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}

	outcome2, err := f.claimAndRun(ctx, run.ID, "worker-2")
	if err != nil || outcome2 != task.OutcomeDone {
		t.Fatalf("Run #2 = (%s, %v), want (done, nil)", outcome2, err)
	}
	if len(adapter.executedArgs) != 1 {
		t.Fatalf("executed calls = %d, want 1", len(adapter.executedArgs))
	}
	got := adapter.executedArgs[0]
	if got["to"] != "alice@example.com" || got["subject"] != "Edited Final Subject" || got["body"] != "Edited final body." {
		t.Fatalf("adapter executed with wrong payload: %+v", got)
	}
}

func TestNonBlockingUnattendedDraftCompletionExecutionAndFoldSignals(t *testing.T) {
	ctx := context.Background()
	sendDef := sealExternalDef(t, "mock/tools/email_send", "email_send", extensions.EffectSensitiveSend)

	var sawDraftSaved atomic.Bool
	f := newRunnerFixture(t, func(_ context.Context, callNum int, req agent.Request) (agent.Response, error) {
		if callNum == 1 {
			return toolCallResponse("c-auto-send", "email_send", map[string]any{
				"to":      "alice@example.com",
				"subject": "Overnight digest",
				"body":    "Here is the overnight update.",
			}, 0.05), nil
		}
		for _, m := range req.Messages {
			raw, _ := json.Marshal(m)
			if strings.Contains(string(raw), "draft_saved") {
				sawDraftSaved.Store(true)
			}
		}
		return toolCallResponse("c-done", "complete", map[string]any{
			"answer": "Saved draft for morning review.", "evidence": []any{}, "unmet": []any{},
		}, 0.05), nil
	})
	adapter := &emailCaptureAdapter{}
	f.extSource.defs = []extensions.Definition{sendDef}
	f.adapters.Register(extensions.AdapterNative, "mock", adapter)

	// Start an automation-origin run (ConversationID == "automation").
	receipts, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
		ID:    "cmd-auto-run",
		RunID: "run-auto-draft",
		Kind:  state.CommandStart,
		Text:  "Prepare overnight follow-up email",
		Origin: &state.TaskOrigin{
			ConversationID: task.AutomationConversationID,
			AutomationID:   "auto_overnight_1",
		},
	}}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}

	// Run MUST complete cleanly (OutcomeDone, NOT OutcomeParked) so MaxActiveRuns is never blocked!
	outcome, err := f.claimAndRun(ctx, receipts[0].Run.ID, "worker-auto")
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("automation run = (%s, %v), want (done, nil)", outcome, err)
	}
	if !sawDraftSaved.Load() {
		t.Fatalf("expected model to observe draft_saved tool result")
	}
	if len(adapter.executedArgs) != 0 {
		t.Fatalf("expected 0 direct executions during unattended run, got %d", len(adapter.executedArgs))
	}

	// Pending interaction must exist with TTL = next brief + 24h.
	pending, _, err := f.perms.Interactions(ctx, "pending", receipts[0].Run.ID, "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending interactions = (%+v, %v), want 1", pending, err)
	}
	inter := pending[0]
	wantExpiry := task.ProactiveDraftExpiry(f.clock.Now(), nil)
	if inter.ExpiresAt == nil || !inter.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("ExpiresAt = %v, want %v (next brief + 24h)", inter.ExpiresAt, wantExpiry)
	}

	draftSvc := &task.ProactiveDraftService{
		Store:       f.store,
		Permissions: f.perms,
		Executor:    f.act.Executor,
		Now:         f.clock.Now,
	}

	// Approve with edited subject/body, then execute via ProactiveDraftService.
	_, _, err = f.perms.Respond(ctx, inter.ID, "req-approve-draft", inter.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: inter.ActionDigest,
		EditedFields: map[string]string{
			"subject": "Morning Approved Subject",
			"body":    "Approved body text.",
		},
	})
	if err != nil {
		t.Fatalf("Respond approve edited draft: %v", err)
	}

	var spawnedReq *task.DigestExecutionRequest
	draftSvc.StartRun = func(_ context.Context, req task.DigestExecutionRequest) (string, error) {
		cp := req
		spawnedReq = &cp
		return "run-digest-exec-1", nil
	}

	execRes, err := draftSvc.ExecuteApprovedDraft(ctx, inter.ID)
	if err != nil {
		t.Fatalf("ExecuteApprovedDraft: %v", err)
	}
	if execRes.ExecutionRunID != "run-digest-exec-1" || execRes.Draft.Status != task.DraftStatusExecuted {
		t.Fatalf("execRes = %+v", execRes)
	}
	if spawnedReq == nil || spawnedReq.Args["subject"] != "Morning Approved Subject" || spawnedReq.Args["body"] != "Approved body text." || spawnedReq.Args["to"] != "alice@example.com" {
		t.Fatalf("spawnedReq = %+v", spawnedReq)
	}

	// Verify RunDigestExecution actually executes the adapter with the approved digest & payload.
	invRes, err := draftSvc.RunDigestExecution(ctx, *spawnedReq)
	if err != nil || invRes.Outcome != "confirmed" {
		t.Fatalf("RunDigestExecution = (%+v, %v)", invRes, err)
	}
	if len(adapter.executedArgs) != 1 || adapter.executedArgs[0]["subject"] != "Morning Approved Subject" {
		t.Fatalf("adapter.executedArgs = %+v", adapter.executedArgs)
	}
}

func TestProactiveDraftDeclineAndExpiryFoldSignals(t *testing.T) {
	ctx := context.Background()
	sendDef := sealExternalDef(t, "mock/tools/email_send", "email_send", extensions.EffectSensitiveSend)

	f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		if callNum == 1 {
			return toolCallResponse("c-1", "email_send", map[string]any{
				"to": "alice@example.com", "subject": "S1", "body": "B1",
			}, 0.05), nil
		}
		return toolCallResponse("c-done", "complete", map[string]any{
			"answer": "Draft saved.", "evidence": []any{}, "unmet": []any{},
		}, 0.05), nil
	})
	f.extSource.defs = []extensions.Definition{sendDef}
	f.adapters.Register(extensions.AdapterNative, "mock", &emailCaptureAdapter{})

	// Run 1 -> declined
	r1, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
		ID: "cmd-decl", RunID: "run-decl", Kind: state.CommandStart, Text: "Draft 1",
		Origin: &state.TaskOrigin{ConversationID: task.AutomationConversationID, AutomationID: "auto_1"},
	}}, nil)
	if err != nil {
		t.Fatalf("start r1: %v", err)
	}
	if out, err := f.claimAndRun(ctx, r1[0].Run.ID, "w1"); err != nil || out != task.OutcomeDone {
		t.Fatalf("run 1 = (%s, %v)", out, err)
	}
	p1, _, _ := f.perms.Interactions(ctx, "pending", r1[0].Run.ID, "", 10)
	if len(p1) != 1 {
		t.Fatalf("expected 1 pending interaction for r1, got %d", len(p1))
	}

	draftSvc := &task.ProactiveDraftService{
		Store:       f.store,
		Permissions: f.perms,
		Executor:    f.act.Executor,
		Now:         f.clock.Now,
	}

	// Decline -> records single fold-into-brief signal.
	declDraft, sig, err := draftSvc.HandleDeclineOrSkip(ctx, p1[0].ID, "decline")
	if err != nil || declDraft.Status != task.DraftStatusDeclined || sig.Reason != "declined" {
		t.Fatalf("HandleDeclineOrSkip = (%+v, %+v, %v)", declDraft, sig, err)
	}

	// Run 2 -> expires after TTL
	f2 := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		if callNum == 1 {
			return toolCallResponse("c-2", "email_send", map[string]any{
				"to": "bob@example.com", "subject": "S2", "body": "B2",
			}, 0.05), nil
		}
		return toolCallResponse("c-done-2", "complete", map[string]any{
			"answer": "Draft saved.", "evidence": []any{}, "unmet": []any{},
		}, 0.05), nil
	})
	f2.extSource.defs = []extensions.Definition{sendDef}
	f2.adapters.Register(extensions.AdapterNative, "mock", &emailCaptureAdapter{})
	r2, _ := f2.store.AcceptCommandBatch(ctx, []state.Command{{
		ID: "cmd-exp", RunID: "run-exp", Kind: state.CommandStart, Text: "Draft 2",
		Origin: &state.TaskOrigin{ConversationID: task.AutomationConversationID, AutomationID: "auto_2"},
	}}, nil)
	if out, err := f2.claimAndRun(ctx, r2[0].Run.ID, "w2"); err != nil || out != task.OutcomeDone {
		t.Fatalf("run 2 = (%s, %v)", out, err)
	}
	draftSvc2 := &task.ProactiveDraftService{Store: f2.store, Permissions: f2.perms, Now: f2.clock.Now}

	// Advance past next brief + 24h (48h is guaranteed past TTL).
	f2.clock.Advance(48 * time.Hour)
	expiredSigs, err := draftSvc2.ExpirePendingDrafts(ctx, f2.clock.Now())
	if err != nil || len(expiredSigs) != 1 || expiredSigs[0].Reason != "expired" {
		t.Fatalf("ExpirePendingDrafts = (%+v, %v), want 1 expired signal", expiredSigs, err)
	}

	// PendingFoldSignals returns the signal once; after MarkFoldSignalConsumed it is dropped.
	pendingSigs, err := draftSvc2.PendingFoldSignals(ctx)
	if err != nil || len(pendingSigs) != 1 {
		t.Fatalf("PendingFoldSignals = (%+v, %v), want 1", pendingSigs, err)
	}
	if err := draftSvc2.MarkFoldSignalConsumed(ctx, pendingSigs[0].ID); err != nil {
		t.Fatalf("MarkFoldSignalConsumed: %v", err)
	}
	afterConsume, err := draftSvc2.PendingFoldSignals(ctx)
	if err != nil || len(afterConsume) != 0 {
		t.Fatalf("PendingFoldSignals after consume = (%+v, %v), want 0", afterConsume, err)
	}
}

func TestProactiveDraftExpiryTimezoneCalculation(t *testing.T) {
	// 2026-06-01 07:00 America/Los_Angeles (14:00 UTC) -> next brief is same day 08:00 PDT (15:00 UTC) -> +24h = 2026-06-02 15:00 UTC.
	now := time.Date(2026, 6, 1, 14, 0, 0, 0, time.UTC)
	prefs := &connections.Preferences{
		Timezone: "America/Los_Angeles",
		Proactivity: &connections.ProactivityPreferences{
			MorningBriefTime: "08:00",
		},
	}
	got := task.ProactiveDraftExpiry(now, prefs)
	want := time.Date(2026, 6, 2, 15, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("ProactiveDraftExpiry = %v, want %v", got, want)
	}
}

type computerStubAdapter struct {
	effect       extensions.Effect
	sawReadOnly  atomic.Bool
	execCalls    atomic.Int32
	needTakeover bool
}

func (a *computerStubAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
	a.sawReadOnly.Store(call.ReadOnly)
	return invoke.Prepared{
		Summary: "Run computer command",
		Effect:  a.effect,
		Spec:    map[string]string{"cmd": invoke.String(call.Args, "command")},
	}, nil
}

func (a *computerStubAdapter) Execute(_ context.Context, call invoke.Call, _ invoke.Prepared) invoke.Result {
	a.sawReadOnly.Store(call.ReadOnly)
	a.execCalls.Add(1)
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"ok": true}}
}

func (a *computerStubAdapter) TakeoverNeeded(_ context.Context, _ invoke.Call) (*invoke.TakeoverRequest, error) {
	if !a.needTakeover {
		return nil, nil
	}
	return &invoke.TakeoverRequest{Prompt: "Sign in", Reason: "login", ComputerID: "comp-1"}, nil
}

func TestUnattendedOriginLimitsComputerReadOnlyAndForbiddenTools(t *testing.T) {
	ctx := context.Background()

	t.Run("proactive_computer_enforces_readonly_and_blocks_writes", func(t *testing.T) {
		compWriteDef, err := extensions.Seal(extensions.Definition{
			Ref:             extensions.DefinitionRef{ID: "nori/computer/computer-shell", Version: "1"},
			Extension:       extensions.DefinitionRef{ID: "nori/computer", Version: "1"},
			ModelName:       "computer_shell",
			Title:           "Computer shell",
			Description:     "Run shell",
			Capability:      "personal_computer",
			CapabilityTitle: "Personal computer",
			InputSchema:     json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
			Binding:         extensions.Binding{Adapter: extensions.AdapterNative, Target: "nori.computer/shell", Version: "1"},
			Policy:          extensions.Policy{Effect: extensions.EffectReversibleWrite, Retry: "reconcile_only", TimeoutMS: 30_000, Location: extensions.LocationRuntime},
			Exposure:        extensions.ExposureDirect,
		})
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}

		f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
			if callNum == 1 {
				return toolCallResponse("c-comp", "computer_shell", map[string]any{"command": "rm -rf /tmp/foo"}, 0.05), nil
			}
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Blocked because read-only.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		compAdapter := &computerStubAdapter{effect: extensions.EffectReversibleWrite}
		f.extSource.defs = []extensions.Definition{compWriteDef}
		f.adapters.Register(extensions.AdapterNative, "nori.computer", compAdapter)

		receipts, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
			ID: "cmd-comp-ro", RunID: "run-comp-ro", Kind: state.CommandStart, Text: "Check computer",
			Origin: &state.TaskOrigin{ConversationID: task.AutomationConversationID, AutomationID: "auto_comp"},
		}}, nil)
		if err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}
		out, err := f.claimAndRun(ctx, receipts[0].Run.ID, "w-comp")
		if err != nil || out != task.OutcomeDone {
			t.Fatalf("proactive computer run = (%s, %v), want (done, nil)", out, err)
		}
		if !compAdapter.sawReadOnly.Load() {
			t.Fatalf("expected call.ReadOnly=true on proactive Personal Agent Computer invocation")
		}
		if compAdapter.execCalls.Load() != 0 {
			t.Fatalf("expected 0 executions for write on proactive computer run, got %d", compAdapter.execCalls.Load())
		}
	})

	t.Run("automation_origin_strips_automation_create", func(t *testing.T) {
		createDef, err := extensions.Seal(extensions.Definition{
			Ref:             extensions.DefinitionRef{ID: "nori/assistant/automation-create", Version: "1"},
			Extension:       extensions.DefinitionRef{ID: "nori/assistant", Version: "1"},
			ModelName:       "automation_create",
			Title:           "Create automation",
			Description:     "Create",
			Capability:      "nori_automations",
			CapabilityTitle: "Automations",
			InputSchema:     json.RawMessage(`{"type":"object"}`),
			Binding:         extensions.Binding{Adapter: extensions.AdapterNative, Target: "nori.automations/create", Version: "1"},
			Policy:          extensions.Policy{Effect: extensions.EffectReversibleWrite, Retry: "provider_key", TimeoutMS: 20_000, Location: extensions.LocationRuntime},
			Exposure:        extensions.ExposureDirect,
		})
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}

		var sawOffered atomic.Bool
		var sawBlocked atomic.Bool
		f := newRunnerFixture(t, func(_ context.Context, callNum int, req agent.Request) (agent.Response, error) {
			if callNum == 1 {
				for _, tool := range req.Tools {
					if tool.Name == "automation_create" {
						sawOffered.Store(true)
					}
				}
				return toolCallResponse("c-create", "automation_create", map[string]any{}, 0.05), nil
			}
			for _, m := range req.Messages {
				raw, _ := json.Marshal(m)
				s := string(raw)
				if strings.Contains(s, `"outcome":"denied"`) || strings.Contains(s, `\"outcome\":\"denied\"`) ||
					strings.Contains(s, `"outcome":"failed"`) || strings.Contains(s, `\"outcome\":\"failed\"`) ||
					strings.Contains(s, "not available") || strings.Contains(s, "unknown") {
					sawBlocked.Store(true)
				}
			}
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Denied.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		stub := &stubExternalAdapter{effect: extensions.EffectReversibleWrite}
		f.extSource.defs = []extensions.Definition{createDef}
		f.adapters.Register(extensions.AdapterNative, "nori.automations", stub)

		receipts, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
			ID: "cmd-forb", RunID: "run-forb", Kind: state.CommandStart, Text: "Spawn another automation",
			Origin: &state.TaskOrigin{ConversationID: task.AutomationConversationID, AutomationID: "auto_self"},
		}}, nil)
		if err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}
		if out, err := f.claimAndRun(ctx, receipts[0].Run.ID, "w-forb"); err != nil || out != task.OutcomeDone {
			t.Fatalf("forbidden tool run = (%s, %v)", out, err)
		}
		if sawOffered.Load() {
			t.Fatalf("automation_create was offered in model tools for automation-origin run; expected upfront filtering")
		}
		if stub.execCalls.Load() != 0 || !sawBlocked.Load() {
			t.Fatalf("expected automation_create to be blocked (execs=%d, sawBlocked=%v)", stub.execCalls.Load(), sawBlocked.Load())
		}
	})

	t.Run("standing_rule_revoked_between_draft_and_execution_blocks_draft", func(t *testing.T) {
		sendDef := sealExternalDef(t, "mock/tools/email_send", "email_send", extensions.EffectSensitiveSend)
		f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
			if callNum == 1 {
				return toolCallResponse("c-1", "email_send", map[string]any{
					"to": "alice@example.com", "subject": "Hi", "body": "Body",
				}, 0.05), nil
			}
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Saved.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		adapter := &emailCaptureAdapter{}
		f.extSource.defs = []extensions.Definition{sendDef}
		f.adapters.Register(extensions.AdapterNative, "mock", adapter)

		receipts, _ := f.store.AcceptCommandBatch(ctx, []state.Command{{
			ID: "cmd-deny-exec", RunID: "run-deny-exec", Kind: state.CommandStart, Text: "Draft",
			Origin: &state.TaskOrigin{ConversationID: task.AutomationConversationID, AutomationID: "auto_deny"},
		}}, nil)
		if out, err := f.claimAndRun(ctx, receipts[0].Run.ID, "w-deny"); err != nil || out != task.OutcomeDone {
			t.Fatalf("run = (%s, %v)", out, err)
		}
		pending, _, _ := f.perms.Interactions(ctx, "pending", receipts[0].Run.ID, "", 10)
		inter := pending[0]
		_, _, err := f.perms.Respond(ctx, inter.ID, "req-app", inter.Revision, permissions.Response{
			Kind: "approve", ActionDigest: inter.ActionDigest,
		})
		if err != nil {
			t.Fatalf("Respond: %v", err)
		}
		// Add a deny rule right before execution!
		if _, err := f.perms.SaveRule(ctx, "", "req-new-deny", 0, permissions.RuleEditable{
			Effect:        "deny",
			ToolIDs:       []string{"mock/tools/email_send"},
			ConnectionIDs: []string{permissions.Wildcard},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{"alice@example.com"},
			Title:         "Never email alice",
		}); err != nil {
			t.Fatalf("SaveRule deny: %v", err)
		}

		draftSvc := &task.ProactiveDraftService{
			Store:       f.store,
			Permissions: f.perms,
			Executor:    f.act.Executor,
			Now:         f.clock.Now,
		}
		_, err = draftSvc.ExecuteApprovedDraft(ctx, inter.ID)
		if err == nil || !strings.Contains(err.Error(), "standing rule") {
			t.Fatalf("ExecuteApprovedDraft after deny rule = %v, want standing rule block", err)
		}
		if len(adapter.executedArgs) != 0 {
			t.Fatalf("expected 0 executions after deny rule, got %d", len(adapter.executedArgs))
		}
	})
}
