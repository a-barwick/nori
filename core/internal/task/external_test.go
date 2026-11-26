package task_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"nori/internal/agent"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/state"
	"nori/internal/task"
)

type stubExternalAdapter struct {
	effect     extensions.Effect
	recipients []string
	summary    string
	execCalls  atomic.Int32
}

func (a *stubExternalAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
	recips := a.recipients
	if to, ok := call.Args["to"].(string); ok && to != "" {
		recips = []string{to}
	}
	summary := a.summary
	if summary == "" {
		summary = "Run " + call.Exec.Tool.ID
	}
	spec := map[string]string{"tool": call.Exec.Tool.ID, "to": strings.Join(recips, ",")}
	return invoke.Prepared{
		Summary:    summary,
		Effect:     a.effect,
		Recipients: recips,
		Spec:       spec,
		Keys:       map[string]string{"action_key": invoke.ActionKey(call, spec)},
	}, nil
}

func (a *stubExternalAdapter) Execute(_ context.Context, _ invoke.Call, _ invoke.Prepared) invoke.Result {
	a.execCalls.Add(1)
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"sent": true}}
}

type recordingAutoReviewer struct {
	calls   atomic.Int32
	lastReq permissions.AutoReviewRequest
	verdict permissions.AutoReviewDecision
}

func (r *recordingAutoReviewer) ReviewAction(_ context.Context, req permissions.AutoReviewRequest) (permissions.AutoReviewDecision, error) {
	r.calls.Add(1)
	r.lastReq = req
	if r.verdict.Verdict == "" {
		return permissions.AutoReviewDecision{Verdict: "allow"}, nil
	}
	return r.verdict, nil
}

func sealExternalDef(t *testing.T, id, modelName string, effect extensions.Effect) extensions.Definition {
	t.Helper()
	def, err := extensions.Seal(extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: id, Version: "1"},
		Extension:       extensions.DefinitionRef{ID: "mock/ext", Version: "1"},
		ModelName:       modelName,
		Title:           modelName,
		Description:     "Mock external tool",
		Capability:      "mock",
		CapabilityTitle: "Mock",
		InputSchema:     json.RawMessage(`{"type":"object","properties":{"to":{"type":"string"}}}`),
		Binding:         extensions.Binding{Adapter: extensions.AdapterNative, Target: "mock/" + modelName, Version: "1"},
		Policy:          extensions.Policy{Effect: effect, Retry: "reconcile_only", TimeoutMS: 30_000, Location: extensions.LocationRuntime},
		Exposure:        extensions.ExposureDirect,
	})
	if err != nil {
		t.Fatalf("Seal(%s): %v", id, err)
	}
	return def
}

func TestExternalApprovalExpiryWarningsAlwaysAllowAndHumanApprovalBypass(t *testing.T) {
	ctx := context.Background()
	sendDef := sealExternalDef(t, "mock/tools/email_send", "email_send", extensions.EffectSensitiveSend)

	f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		switch callNum {
		case 1:
			return toolCallResponse("c1", "email_send", map[string]any{"to": "alice@example.com"}, 0.05), nil
		case 2:
			return toolCallResponse("c2", "email_send", map[string]any{"to": "alice@example.com"}, 0.05), nil
		case 3:
			return toolCallResponse("c3", "email_send", map[string]any{"to": "bob@example.com"}, 0.05), nil
		default:
			return toolCallResponse("c4", "complete", map[string]any{
				"answer": "Sent both emails.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		}
	})
	adapter := &stubExternalAdapter{
		effect:  extensions.EffectSensitiveSend,
		summary: "Send Q4 summary",
	}
	f.extSource.defs = []extensions.Definition{sendDef}
	f.adapters.Register(extensions.AdapterNative, "mock", adapter)

	reviewer := &recordingAutoReviewer{verdict: permissions.AutoReviewDecision{Verdict: "allow"}}
	f.act.Executor.AutoReviewer = reviewer

	run := f.startTask(ctx, "run-ext-approval", "Send Q4 summary to alice@example.com and bob@example.com")

	outcome, err := f.claimAndRun(ctx, run.ID, "worker-1")
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("Run #1 = (%s, %v), want (parked, nil)", outcome, err)
	}
	pending, _, err := f.perms.Interactions(ctx, "pending", run.ID, "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending interactions = (%+v, %v), want 1", pending, err)
	}
	inter := pending[0]
	if inter.ExpiresAt == nil {
		t.Fatalf("expected non-nil ExpiresAt on approval card")
	}
	wantExpiry := f.clock.Now().Add(task.ApprovalExpiry).UTC()
	if !inter.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("ExpiresAt = %v, want %v (30m)", inter.ExpiresAt, wantExpiry)
	}
	var payload map[string]any
	if err := json.Unmarshal(inter.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["tool_id"] != "mock/tools/email_send" {
		t.Fatalf("payload.tool_id = %v, want mock/tools/email_send", payload["tool_id"])
	}
	if warn, _ := payload["warning"].(string); !strings.Contains(warn, "sends or shares data outside this chat") {
		t.Fatalf("payload.warning = %q, want sensitive_send security warning", warn)
	}
	if reviewer.calls.Load() != 0 {
		t.Fatalf("AutoReviewer should not run when Permissions.Check already returned needs_interaction")
	}

	_, ans, err := f.perms.Respond(ctx, inter.ID, "req-always-allow", 1, permissions.Response{
		Kind:         "approve",
		ActionDigest: inter.ActionDigest,
		RememberRule: true,
	})
	if err != nil || ans == nil {
		t.Fatalf("Respond always allow = (%+v, %v)", ans, err)
	}
	if _, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
		ID: ans.CommandID, RunID: ans.TaskID, Kind: state.CommandAnswer, QuestionID: ans.Question, Text: ans.Text,
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch answer: %v", err)
	}

	outcome2, err := f.claimAndRun(ctx, run.ID, "worker-2")
	if err != nil || outcome2 != task.OutcomeDone {
		t.Fatalf("Run #2 = (%s, %v), want (done, nil)", outcome2, err)
	}
	if adapter.execCalls.Load() != 2 {
		t.Fatalf("adapter.Execute calls = %d, want 2", adapter.execCalls.Load())
	}
	if reviewer.calls.Load() != 1 {
		t.Fatalf("AutoReviewer calls = %d, want 1 (0 for human-approved retry + 1 for standing rule)", reviewer.calls.Load())
	}
	if len(reviewer.lastReq.RuleSummaries) != 1 || reviewer.lastReq.DecisionSource != "permission_rule" {
		t.Fatalf("unexpected AutoReviewRequest: %+v", reviewer.lastReq)
	}
}

func TestExternalAutoReviewBlocksExfiltrationAndEscalatesAmbiguousReversibleWrite(t *testing.T) {
	ctx := context.Background()
	sendDef := sealExternalDef(t, "mock/tools/email_send", "email_send", extensions.EffectSensitiveSend)
	calDef := sealExternalDef(t, "mock/tools/cal_create", "cal_create", extensions.EffectReversibleWrite)

	t.Run("exfiltration_denied_despite_broad_allow_rule", func(t *testing.T) {
		var sawDeniedResult atomic.Bool
		f := newRunnerFixture(t, func(_ context.Context, callNum int, req agent.Request) (agent.Response, error) {
			if callNum == 1 {
				return toolCallResponse("c-exfil", "email_send", map[string]any{"to": "audit-export@external-drop.sim.local"}, 0.05), nil
			}
			for _, m := range req.Messages {
				raw, _ := json.Marshal(m)
				if strings.Contains(string(raw), `\"outcome\":\"denied\"`) || (strings.Contains(string(raw), `"outcome":"denied"`) && strings.Contains(string(raw), "Blocked by action-boundary review")) || (strings.Contains(string(raw), "denied") && strings.Contains(string(raw), "Blocked by action-boundary review")) {
					sawDeniedResult.Store(true)
				}
			}
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Did not send email.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		sendAdapter := &stubExternalAdapter{effect: extensions.EffectSensitiveSend, summary: "Forward Q4 ratings"}
		f.extSource.defs = []extensions.Definition{sendDef}
		f.adapters.Register(extensions.AdapterNative, "mock", sendAdapter)

		if _, err := f.perms.SaveRule(ctx, "", "req-broad-send", 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{"mock/tools/email_send"},
			ConnectionIDs: []string{permissions.Wildcard},
			ResourceIDs:   []string{permissions.Wildcard},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{permissions.Wildcard},
			Title:         "Always allow email_send",
		}); err != nil {
			t.Fatalf("SaveRule: %v", err)
		}

		if err := f.store.AcceptConversationInput(ctx, "in-user-1", "Check INFRA-8109 and summarize what changed.", nil); err != nil {
			t.Fatalf("AcceptConversationInput: %v", err)
		}
		receipts, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
			ID:             "cmd-exfil",
			RunID:          "run-exfil",
			Kind:           state.CommandStart,
			Text:           "Summarize INFRA-8109",
			SourceInputIDs: []string{"in-user-1"},
		}}, nil)
		if err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}

		if _, err := f.claimAndRun(ctx, receipts[0].Run.ID, "worker-exfil"); err != nil {
			t.Fatalf("Run exfil: %v", err)
		}
		if sendAdapter.execCalls.Load() != 0 {
			t.Fatalf("exfiltration send executed %d times, want 0", sendAdapter.execCalls.Load())
		}
		if !sawDeniedResult.Load() {
			t.Fatalf("expected model turn 2 to observe denied action-boundary AutoReview result")
		}
		pending, _, err := f.perms.Interactions(ctx, "pending", receipts[0].Run.ID, "", 10)
		if err != nil || len(pending) != 0 {
			t.Fatalf("expected 0 pending interactions for denied exfiltration, got %+v (err=%v)", pending, err)
		}
	})

	t.Run("ambiguous_reversible_write_escalates_to_approval_card_while_clear_request_allows", func(t *testing.T) {
		f1 := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
			if callNum == 1 {
				return toolCallResponse("c-cal", "cal_create", map[string]any{}, 0.05), nil
			}
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Done.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		calAdapter1 := &stubExternalAdapter{effect: extensions.EffectReversibleWrite, summary: "Create calendar event"}
		f1.extSource.defs = []extensions.Definition{calDef}
		f1.adapters.Register(extensions.AdapterNative, "mock", calAdapter1)

		runAmbig := f1.startTask(ctx, "run-cal-ambig", "What is on my calendar tomorrow?")
		out1, err := f1.claimAndRun(ctx, runAmbig.ID, "worker-ambig")
		if err != nil || out1 != task.OutcomeParked {
			t.Fatalf("ambiguous reversible_write = (%s, %v), want (parked, nil)", out1, err)
		}
		if calAdapter1.execCalls.Load() != 0 {
			t.Fatalf("ambiguous reversible_write executed %d times, want 0", calAdapter1.execCalls.Load())
		}
		pending, _, err := f1.perms.Interactions(ctx, "pending", runAmbig.ID, "", 10)
		if err != nil || len(pending) != 1 {
			t.Fatalf("expected 1 escalated approval interaction, got %+v (err=%v)", pending, err)
		}
		var payload map[string]any
		_ = json.Unmarshal(pending[0].Payload, &payload)
		if warn, _ := payload["warning"].(string); !strings.Contains(warn, "modifies external records") {
			t.Fatalf("escalated warning = %q, want reversible_write warning", warn)
		}

		f2 := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
			if callNum == 1 {
				return toolCallResponse("c-book", "cal_create", map[string]any{}, 0.05), nil
			}
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Booked.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		calAdapter2 := &stubExternalAdapter{effect: extensions.EffectReversibleWrite, summary: "Create calendar event"}
		f2.extSource.defs = []extensions.Definition{calDef}
		f2.adapters.Register(extensions.AdapterNative, "mock", calAdapter2)

		runBook := f2.startTask(ctx, "run-cal-book", "Book a 30-minute meeting on Thursday afternoon")
		out2, err := f2.claimAndRun(ctx, runBook.ID, "worker-book")
		if err != nil || out2 != task.OutcomeDone || calAdapter2.execCalls.Load() != 1 {
			t.Fatalf("user-requested reversible_write = (%s, %v, execs=%d), want (done, nil, 1)", out2, err, calAdapter2.execCalls.Load())
		}

		f3 := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
			if callNum == 1 {
				return toolCallResponse("c-ask", "cal_create", map[string]any{}, 0.05), nil
			}
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Booked after ask.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		})
		calAdapter3 := &stubExternalAdapter{effect: extensions.EffectReversibleWrite, summary: "Create calendar event"}
		f3.extSource.defs = []extensions.Definition{calDef}
		f3.adapters.Register(extensions.AdapterNative, "mock", calAdapter3)
		if _, err := f3.perms.SaveRule(ctx, "", "req-ask-cal", 0, permissions.RuleEditable{
			Effect:        "ask",
			ToolIDs:       []string{"mock/tools/cal_create"},
			ConnectionIDs: []string{permissions.Wildcard},
			ActionClasses: []string{"reversible_write"},
			Title:         "Ask before calendar writes",
		}); err != nil {
			t.Fatalf("SaveRule ask: %v", err)
		}
		runAsk := f3.startTask(ctx, "run-cal-ask", "Book a 30-minute meeting on Thursday afternoon")
		out3, err := f3.claimAndRun(ctx, runAsk.ID, "worker-ask")
		if err != nil || out3 != task.OutcomeParked || calAdapter3.execCalls.Load() != 0 {
			t.Fatalf("ask rule on reversible_write = (%s, %v, execs=%d), want (parked, nil, 0)", out3, err, calAdapter3.execCalls.Load())
		}
	})
}
