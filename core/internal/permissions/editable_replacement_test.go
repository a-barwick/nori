package permissions_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"nori/internal/permissions"
	"nori/internal/state"
)

func openEmailApproval(t *testing.T, env *testEnv, id, taskID string, rev int64, digest string) permissions.Interaction {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"summary":    "To: alice@example.com\nSubject: Draft Subject\n\nOriginal body text",
		"effect":     "sensitive_send",
		"tool_id":    "google/mail/send",
		"account":    map[string]any{"connection_id": "conn-gmail", "label": "me@example.com"},
		"recipients": []string{"alice@example.com"},
		"editable_fields": map[string]string{
			"subject": "Draft Subject",
			"body":    "Original body text",
		},
	})
	expires := env.currentTime().Add(30 * time.Minute)
	inter, err := env.svc.Open(context.Background(), permissions.Interaction{
		ID:                  id,
		TaskID:              taskID,
		InstructionRevision: &rev,
		Kind:                "approval",
		Prompt:              "Send this email?",
		Payload:             payload,
		ActionDigest:        digest,
		ExpiresAt:           &expires,
		ConnectionID:        "conn-gmail",
		ToolID:              "google/mail/send",
		ToolTitle:           "Send email",
		ActionClass:         "sensitive_send",
		Recipients:          []string{"alice@example.com"},
	}, "")
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}
	return inter
}

func TestEditableApprovalReplacementAtomicFlow(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)
	orig := openEmailApproval(t, env, "int_orig_1", "run-edit-1", 1, "act_orig_digest_1")

	receipt, ans, err := env.svc.Respond(ctx, orig.ID, "req-edit-approve-1", orig.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: orig.ActionDigest,
		EditedFields: map[string]string{
			"subject": "Updated Subject",
			"body":    "Updated message body.",
		},
	})
	if err != nil {
		t.Fatalf("Respond with edited fields: %v", err)
	}
	if ans == nil || ans.ReplacementInteractionID == "" {
		t.Fatalf("expected Answer with ReplacementInteractionID, got %+v", ans)
	}
	if ans.ActionDigest == "" || ans.ActionDigest == orig.ActionDigest {
		t.Fatalf("expected recomputed ActionDigest != original; got %q", ans.ActionDigest)
	}

	var replFromReceipt permissions.Interaction
	if err := json.Unmarshal(receipt.Resource, &replFromReceipt); err != nil {
		t.Fatalf("unmarshal receipt resource: %v", err)
	}
	if replFromReceipt.ID != ans.ReplacementInteractionID || replFromReceipt.Status != "resolved" {
		t.Fatalf("replacement interaction = %+v", replFromReceipt)
	}
	if replFromReceipt.Resolution == nil || replFromReceipt.Resolution.Kind != "approve" {
		t.Fatalf("replacement resolution = %+v, want approve", replFromReceipt.Resolution)
	}

	// Original interaction must be superseded and linked via SupersededBy.
	origAfter, err := env.svc.Interaction(ctx, orig.ID)
	if err != nil {
		t.Fatalf("Interaction(orig): %v", err)
	}
	if origAfter.Status != "superseded" || origAfter.SupersededBy != replFromReceipt.ID {
		t.Fatalf("origAfter = (status=%s, superseded_by=%s), want (superseded, %s)", origAfter.Status, origAfter.SupersededBy, replFromReceipt.ID)
	}

	// Replacement payload must keep immutable fields and apply edited fields.
	var replPayload map[string]any
	if err := json.Unmarshal(replFromReceipt.Payload, &replPayload); err != nil {
		t.Fatalf("unmarshal replacement payload: %v", err)
	}
	if summary, _ := replPayload["summary"].(string); !strings.Contains(summary, "Subject: Updated Subject") || !strings.Contains(summary, "Updated message body.") {
		t.Fatalf("replacement summary did not reflect edits: %q", summary)
	}
	if toolID, _ := replPayload["tool_id"].(string); toolID != "google/mail/send" {
		t.Fatalf("tool_id changed: %q", toolID)
	}

	// Original digest must NOT be approved; recomputed replacement digest MUST be approved.
	origDec, err := env.svc.Check(ctx, permissions.Request{
		RunID:        "run-edit-1",
		Revision:     1,
		Origin:       "user",
		ToolID:       "google/mail/send",
		Effect:       "sensitive_send",
		ConnectionID: "conn-gmail",
		Recipients:   []string{"alice@example.com"},
		Digest:       orig.ActionDigest,
	})
	if err != nil || origDec.Result != "needs_interaction" {
		t.Fatalf("Check(origDigest) = (%+v, %v), want needs_interaction", origDec, err)
	}

	replDec, err := env.svc.Check(ctx, permissions.Request{
		RunID:        "run-edit-1",
		Revision:     1,
		Origin:       "user",
		ToolID:       "google/mail/send",
		Effect:       "sensitive_send",
		ConnectionID: "conn-gmail",
		Recipients:   []string{"alice@example.com"},
		Digest:       replFromReceipt.ActionDigest,
	})
	if err != nil || replDec.Result != "allow" || !replDec.ApprovedByHuman {
		t.Fatalf("Check(replacementDigest) = (%+v, %v), want allow + ApprovedByHuman", replDec, err)
	}

	// FindApprovedReplacement and ApprovedReplacementByInteraction must return the approved edits.
	found, err := env.svc.FindApprovedReplacement(ctx, "run-edit-1", 1, orig.ActionDigest)
	if err != nil || found == nil {
		t.Fatalf("FindApprovedReplacement = (%+v, %v)", found, err)
	}
	if found.EditedFields["subject"] != "Updated Subject" || found.EditedFields["body"] != "Updated message body." {
		t.Fatalf("unexpected found.EditedFields: %+v", found.EditedFields)
	}
	byOrig, err := env.svc.ApprovedReplacementByInteraction(ctx, orig.ID)
	if err != nil || byOrig == nil || byOrig.InteractionID != replFromReceipt.ID {
		t.Fatalf("ApprovedReplacementByInteraction(orig) = (%+v, %v)", byOrig, err)
	}

	// Subsequent non-idempotent response to the superseded original must fail with ErrInteractionStale.
	if _, _, err := env.svc.Respond(ctx, orig.ID, "req-second-attempt", orig.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: orig.ActionDigest,
	}); !errors.Is(err, permissions.ErrInteractionStale) {
		t.Fatalf("second Respond on superseded interaction = %v, want ErrInteractionStale", err)
	}
}

func TestEditableApprovalReplacementRejections(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	disallowed := []struct {
		name   string
		resp   permissions.Response
		errIs  error
	}{
		{
			name: "reject_remember_rule_with_edits",
			resp: permissions.Response{
				Kind:         "approve",
				ActionDigest: "act_orig",
				RememberRule: true,
				EditedFields: map[string]string{"body": "Edited"},
			},
			errIs: state.ErrInvalidResource,
		},
		{
			name: "reject_editing_recipients",
			resp: permissions.Response{
				Kind:         "approve",
				ActionDigest: "act_orig",
				EditedFields: map[string]string{"recipients": "attacker@evil.com"},
			},
			errIs: state.ErrInvalidResource,
		},
		{
			name: "reject_editing_to",
			resp: permissions.Response{
				Kind:         "approve",
				ActionDigest: "act_orig",
				EditedFields: map[string]string{"to": "attacker@evil.com"},
			},
			errIs: state.ErrInvalidResource,
		},
		{
			name: "reject_editing_account",
			resp: permissions.Response{
				Kind:         "approve",
				ActionDigest: "act_orig",
				EditedFields: map[string]string{"account": "conn-other"},
			},
			errIs: state.ErrInvalidResource,
		},
		{
			name: "reject_editing_tool",
			resp: permissions.Response{
				Kind:         "approve",
				ActionDigest: "act_orig",
				EditedFields: map[string]string{"tool": "other_tool"},
			},
			errIs: state.ErrInvalidResource,
		},
		{
			name: "reject_subject_header_injection",
			resp: permissions.Response{
				Kind:         "approve",
				ActionDigest: "act_orig",
				EditedFields: map[string]string{"subject": "Hello\r\nBcc: attacker@evil.com"},
			},
			errIs: state.ErrInvalidResource,
		},
		{
			name: "reject_overlong_body",
			resp: permissions.Response{
				Kind:         "approve",
				ActionDigest: "act_orig",
				EditedFields: map[string]string{"body": strings.Repeat("a", 12_001)},
			},
			errIs: state.ErrInvalidResource,
		},
		{
			name: "reject_edits_on_decline",
			resp: permissions.Response{
				Kind:         "decline",
				ActionDigest: "act_orig",
				EditedFields: map[string]string{"body": "Edited"},
			},
			errIs: state.ErrInvalidResource,
		},
	}

	for idx, tc := range disallowed {
		t.Run(tc.name, func(t *testing.T) {
			inter := openEmailApproval(t, env, "int_rej_"+tc.name, "run-rej", int64(idx+1), "act_orig")
			_, _, err := env.svc.Respond(ctx, inter.ID, "req-"+tc.name, inter.Revision, tc.resp)
			if !errors.Is(err, tc.errIs) {
				t.Fatalf("Respond(%s) err = %v, want %v", tc.name, err, tc.errIs)
			}
			// Original interaction must remain pending after rejected mutation.
			stillPending, err := env.svc.Interaction(ctx, inter.ID)
			if err != nil || stillPending.Status != "pending" {
				t.Fatalf("interaction after rejected edit = (%+v, %v), want pending", stillPending, err)
			}
		})
	}
}

func TestRecheckStandingRulesAtExecutionTime(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	allowRule := mustSaveRule(t, env.svc, "req-allow-1", permissions.RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{"mock/tools/cal_create"},
		ConnectionIDs: []string{permissions.Wildcard},
		ActionClasses: []string{"reversible_write"},
		MaxOperations: 1,
		Title:         "Allow one calendar write",
	})

	req := permissions.Request{
		RunID:        "run-recheck",
		Revision:     1,
		Origin:       "automation",
		AutomationID: "auto-1",
		ToolID:       "mock/tools/cal_create",
		Effect:       "reversible_write",
		ConnectionID: "conn-cal",
	}

	// Plan time Check consumes the 1 allowed operation.
	planned, err := env.svc.Check(ctx, req)
	if err != nil || planned.Result != "allow" || planned.Source != "permission_rule" {
		t.Fatalf("plan Check = (%+v, %v), want allow via permission_rule", planned, err)
	}

	// Execution-time RecheckStandingRules succeeds while the rule is still active (does not double-consume).
	recheck1, err := env.svc.RecheckStandingRules(ctx, req, planned)
	if err != nil || recheck1.Result != "allow" {
		t.Fatalf("RecheckStandingRules (active) = (%+v, %v), want allow", recheck1, err)
	}

	// Revoke the standing rule before execution -> RecheckStandingRules must deny!
	latestRule, err := env.svc.Rule(ctx, allowRule.ID)
	if err != nil {
		t.Fatalf("Rule: %v", err)
	}
	if _, err := env.svc.RevokeRule(ctx, latestRule.ID, "req-revoke-1", latestRule.Revision); err != nil {
		t.Fatalf("RevokeRule: %v", err)
	}
	recheck2, err := env.svc.RecheckStandingRules(ctx, req, planned)
	if err != nil || recheck2.Result != "deny" {
		t.Fatalf("RecheckStandingRules (revoked) = (%+v, %v), want deny", recheck2, err)
	}

	// A newly added deny rule must also block even a human-approved decision.
	mustSaveRule(t, env.svc, "req-deny-override", permissions.RuleEditable{
		Effect:        "deny",
		ToolIDs:       []string{"mock/tools/cal_create"},
		ConnectionIDs: []string{permissions.Wildcard},
		ActionClasses: []string{"reversible_write"},
		Title:         "Block calendar writes",
	})
	recheck3, err := env.svc.RecheckStandingRules(ctx, req, permissions.Decision{
		Result:          "allow",
		Source:          "approval",
		ApprovedByHuman: true,
	})
	if err != nil || recheck3.Result != "deny" {
		t.Fatalf("RecheckStandingRules (new deny rule) = (%+v, %v), want deny", recheck3, err)
	}
}
