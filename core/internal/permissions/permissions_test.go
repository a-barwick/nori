package permissions_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/permissions"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

type testEnv struct {
	mu    sync.Mutex
	now   time.Time
	store state.ResourceStore
	svc   *permissions.Service
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	e := &testEnv{
		now: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}
	nowFn := func() time.Time {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.now
	}
	backend := memstore.New(memstore.WithClock(nowFn))
	t.Cleanup(func() { _ = backend.Close() })
	u := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "perms@example.com",
		DisplayName: "Perms Test",
		TimeZone:    "UTC",
	}
	db := records.NewShared(backend, u, 5.0)
	db.Now = nowFn
	e.store = db
	e.svc = &permissions.Service{
		Store:            db,
		UnattendedWrites: "confirm_writes",
		UserSends:        "ask",
		Now:              nowFn,
	}
	return e
}

func (e *testEnv) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func (e *testEnv) currentTime() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func mustSaveRule(t *testing.T, svc *permissions.Service, reqID string, rule permissions.RuleEditable) permissions.Rule {
	t.Helper()
	receipt, err := svc.SaveRule(context.Background(), "", reqID, 0, rule)
	if err != nil {
		t.Fatalf("SaveRule(%s): %v", reqID, err)
	}
	var saved permissions.Rule
	if err := json.Unmarshal(receipt.Resource, &saved); err != nil {
		t.Fatalf("unmarshal saved rule: %v", err)
	}
	return saved
}

func TestRuleEvaluationMatrix(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name           string
		userSends      string
		unattended     string
		rules          []permissions.RuleEditable
		req            permissions.Request
		wantResult     string
		wantSource     string
		wantRuleEffect string // optional check that returned RuleIDs matches rule with this effect
	}{
		{
			name: "deny_wins_over_wildcard_allow",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{permissions.Wildcard},
					ConnectionIDs: []string{permissions.Wildcard},
					ResourceIDs:   []string{permissions.Wildcard},
					ActionClasses: []string{"sensitive_send"},
					Recipients:    []string{permissions.Wildcard},
				},
				{
					Effect:        "deny",
					ToolIDs:       []string{"gmail.send"},
					ConnectionIDs: []string{"conn-gmail"},
					ActionClasses: []string{"sensitive_send"},
					Recipients:    []string{"Boss@example.com"},
				},
			},
			req: permissions.Request{
				RunID:        "run-1",
				Origin:       "user",
				ToolID:       "gmail.send",
				Effect:       "sensitive_send",
				ConnectionID: "conn-gmail",
				Recipients:   []string{"boss@example.com"},
			},
			wantResult:     "deny",
			wantRuleEffect: "deny",
		},
		{
			name: "allow_case_insensitive_recipients_all_covered",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{"gmail.send"},
					ConnectionIDs: []string{"conn-gmail"},
					ActionClasses: []string{"sensitive_send"},
					Recipients:    []string{"Boss@Example.COM", "Team@Example.com"},
				},
			},
			req: permissions.Request{
				RunID:        "run-2",
				Origin:       "user",
				ToolID:       "gmail.send",
				Effect:       "sensitive_send",
				ConnectionID: "conn-gmail",
				Recipients:   []string{"boss@example.com", "TEAM@example.com"},
			},
			wantResult:     "allow",
			wantSource:     "permission_rule",
			wantRuleEffect: "allow",
		},
		{
			name: "allow_recipients_partial_coverage_does_not_match",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{"gmail.send"},
					ConnectionIDs: []string{"conn-gmail"},
					ActionClasses: []string{"sensitive_send"},
					Recipients:    []string{"boss@example.com"},
				},
			},
			req: permissions.Request{
				RunID:        "run-3",
				Origin:       "user",
				ToolID:       "gmail.send",
				Effect:       "sensitive_send",
				ConnectionID: "conn-gmail",
				Recipients:   []string{"boss@example.com", "other@example.com"},
			},
			wantResult: "needs_interaction",
		},
		{
			name: "allow_scoped_recipients_rejects_empty_req_recipients",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{"gmail.send"},
					ConnectionIDs: []string{"conn-gmail"},
					ActionClasses: []string{"sensitive_send"},
					Recipients:    []string{"boss@example.com"},
				},
			},
			req: permissions.Request{
				RunID:        "run-4",
				Origin:       "user",
				ToolID:       "gmail.send",
				Effect:       "sensitive_send",
				ConnectionID: "conn-gmail",
			},
			wantResult: "needs_interaction",
		},
		{
			name: "deny_recipients_intersects_case_insensitively",
			rules: []permissions.RuleEditable{
				{
					Effect:        "deny",
					ToolIDs:       []string{"gmail.send"},
					ConnectionIDs: []string{"conn-gmail"},
					ActionClasses: []string{"sensitive_send"},
					Recipients:    []string{"Boss@example.com"},
				},
			},
			req: permissions.Request{
				RunID:        "run-5",
				Origin:       "user",
				ToolID:       "gmail.send",
				Effect:       "sensitive_send",
				ConnectionID: "conn-gmail",
				Recipients:   []string{"safe@example.com", "boss@example.com"},
			},
			wantResult:     "deny",
			wantRuleEffect: "deny",
		},
		{
			name: "deny_recipients_no_intersection_falls_through",
			rules: []permissions.RuleEditable{
				{
					Effect:        "deny",
					ToolIDs:       []string{"gmail.send"},
					ConnectionIDs: []string{"conn-gmail"},
					ActionClasses: []string{"sensitive_send"},
					Recipients:    []string{"Boss@example.com"},
				},
			},
			userSends: "allow",
			req: permissions.Request{
				RunID:        "run-6",
				Origin:       "user",
				ToolID:       "gmail.send",
				Effect:       "sensitive_send",
				ConnectionID: "conn-gmail",
				Recipients:   []string{"colleague@example.com"},
			},
			wantResult: "allow",
			wantSource: "user_request",
		},
		{
			name: "allow_resource_ids_must_cover_all_and_reject_empty",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{"drive.write"},
					ConnectionIDs: []string{"conn-drive"},
					ResourceIDs:   []string{"doc-1", "doc-2"},
					ActionClasses: []string{"reversible_write"},
				},
			},
			req: permissions.Request{
				RunID:        "run-7",
				Origin:       "automation",
				ToolID:       "drive.write",
				Effect:       "reversible_write",
				ConnectionID: "conn-drive",
				ResourceIDs:  []string{"doc-1", "doc-2"},
			},
			wantResult:     "allow",
			wantSource:     "permission_rule",
			wantRuleEffect: "allow",
		},
		{
			name: "allow_resource_ids_missing_one_does_not_match",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{"drive.write"},
					ConnectionIDs: []string{"conn-drive"},
					ResourceIDs:   []string{"doc-1"},
					ActionClasses: []string{"reversible_write"},
				},
			},
			req: permissions.Request{
				RunID:        "run-8",
				Origin:       "automation",
				ToolID:       "drive.write",
				Effect:       "reversible_write",
				ConnectionID: "conn-drive",
				ResourceIDs:  []string{"doc-1", "doc-2"},
			},
			wantResult: "needs_interaction",
		},
		{
			name: "allow_scoped_resource_ids_rejects_empty_req_resources",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{"drive.write"},
					ConnectionIDs: []string{"conn-drive"},
					ResourceIDs:   []string{"doc-1"},
					ActionClasses: []string{"reversible_write"},
				},
			},
			req: permissions.Request{
				RunID:        "run-9",
				Origin:       "automation",
				ToolID:       "drive.write",
				Effect:       "reversible_write",
				ConnectionID: "conn-drive",
			},
			wantResult: "needs_interaction",
		},
		{
			name: "deny_resource_ids_intersects_single_match",
			rules: []permissions.RuleEditable{
				{
					Effect:        "deny",
					ToolIDs:       []string{"drive.write"},
					ConnectionIDs: []string{"conn-drive"},
					ResourceIDs:   []string{"doc-secret"},
					ActionClasses: []string{"reversible_write"},
				},
			},
			req: permissions.Request{
				RunID:        "run-10",
				Origin:       "user",
				ToolID:       "drive.write",
				Effect:       "reversible_write",
				ConnectionID: "conn-drive",
				ResourceIDs:  []string{"doc-public", "doc-secret"},
			},
			wantResult:     "deny",
			wantRuleEffect: "deny",
		},
		{
			name: "deny_resource_ids_disjoint_does_not_block",
			rules: []permissions.RuleEditable{
				{
					Effect:        "deny",
					ToolIDs:       []string{"drive.write"},
					ConnectionIDs: []string{"conn-drive"},
					ResourceIDs:   []string{"doc-secret"},
					ActionClasses: []string{"reversible_write"},
				},
			},
			req: permissions.Request{
				RunID:        "run-11",
				Origin:       "user",
				ToolID:       "drive.write",
				Effect:       "reversible_write",
				ConnectionID: "conn-drive",
				ResourceIDs:  []string{"doc-public"},
			},
			wantResult: "allow",
			wantSource: "user_request",
		},
		{
			name: "empty_req_connection_id_does_not_match_scoped_allow_connection_ids",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{"mcp.send"},
					ConnectionIDs: []string{"conn-1"},
					ActionClasses: []string{"sensitive_send"},
				},
			},
			req: permissions.Request{
				RunID:        "run-12",
				Origin:       "automation",
				ToolID:       "mcp.send",
				Effect:       "sensitive_send",
				ConnectionID: "",
			},
			wantResult: "needs_interaction",
		},
		{
			name: "empty_req_connection_id_does_not_match_scoped_deny_connection_ids",
			rules: []permissions.RuleEditable{
				{
					Effect:        "deny",
					ToolIDs:       []string{"mcp.write"},
					ConnectionIDs: []string{"conn-1"},
					ActionClasses: []string{"reversible_write"},
				},
			},
			req: permissions.Request{
				RunID:        "run-13",
				Origin:       "user",
				ToolID:       "mcp.write",
				Effect:       "reversible_write",
				ConnectionID: "",
			},
			wantResult: "allow",
			wantSource: "user_request",
		},
		{
			name: "empty_req_connection_id_matches_wildcard_connection_ids",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{"mcp.send"},
					ConnectionIDs: []string{permissions.Wildcard},
					ActionClasses: []string{"sensitive_send"},
				},
			},
			req: permissions.Request{
				RunID:        "run-14",
				Origin:       "automation",
				ToolID:       "mcp.send",
				Effect:       "sensitive_send",
				ConnectionID: "",
			},
			wantResult:     "allow",
			wantSource:     "permission_rule",
			wantRuleEffect: "allow",
		},
		{
			name: "empty_req_connection_id_matches_unscoped_empty_connection_ids",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{"mcp.send"},
					ConnectionIDs: nil,
					ActionClasses: []string{"sensitive_send"},
				},
			},
			req: permissions.Request{
				RunID:        "run-15",
				Origin:       "automation",
				ToolID:       "mcp.send",
				Effect:       "sensitive_send",
				ConnectionID: "",
			},
			wantResult:     "allow",
			wantSource:     "permission_rule",
			wantRuleEffect: "allow",
		},
		{
			name: "read_only_task_denies_writes_even_with_allow_rule",
			rules: []permissions.RuleEditable{
				{
					Effect:        "allow",
					ToolIDs:       []string{permissions.Wildcard},
					ConnectionIDs: []string{permissions.Wildcard},
					ActionClasses: []string{"reversible_write"},
				},
			},
			req: permissions.Request{
				RunID:    "run-16",
				Origin:   "user",
				ReadOnly: true,
				ToolID:   "drive.write",
				Effect:   "reversible_write",
			},
			wantResult: "deny",
		},
		{
			name:       "automation_require_rule_denies_without_matching_rule",
			unattended: "require_rule",
			req: permissions.Request{
				RunID:  "run-17",
				Origin: "automation",
				ToolID: "drive.write",
				Effect: "reversible_write",
			},
			wantResult: "deny",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			if tc.userSends != "" {
				env.svc.UserSends = tc.userSends
			}
			if tc.unattended != "" {
				env.svc.UnattendedWrites = tc.unattended
			}
			byID := make(map[string]permissions.Rule, len(tc.rules))
			for i, r := range tc.rules {
				saved := mustSaveRule(t, env.svc, tc.name+"-rule-"+string(rune('a'+i)), r)
				byID[saved.ID] = saved
			}

			got, err := env.svc.Check(ctx, tc.req)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if got.Result != tc.wantResult {
				t.Fatalf("Result = %q, want %q (decision=%+v)", got.Result, tc.wantResult, got)
			}
			if tc.wantSource != "" && got.Source != tc.wantSource {
				t.Fatalf("Source = %q, want %q", got.Source, tc.wantSource)
			}
			if tc.wantRuleEffect != "" {
				if len(got.RuleIDs) != 1 || byID[got.RuleIDs[0]].Effect != tc.wantRuleEffect {
					t.Fatalf("RuleIDs = %v, want 1 %s rule", got.RuleIDs, tc.wantRuleEffect)
				}
			}
		})
	}
}

func TestRuleValidationAndMaxOperationsFallthrough(t *testing.T) {
	ctx := context.Background()
	env := newTestEnv(t)

	// Negative MaxOperations must be rejected by both SaveRule and RuleWrites.
	invalidCases := []struct {
		name string
		rule permissions.RuleEditable
	}{
		{
			name: "negative_max_operations",
			rule: permissions.RuleEditable{
				Effect:        "allow",
				ToolIDs:       []string{"gmail.send"},
				ConnectionIDs: []string{"conn-1"},
				ActionClasses: []string{"sensitive_send"},
				MaxOperations: -1,
			},
		},
		{
			name: "invalid_effect",
			rule: permissions.RuleEditable{
				Effect:        "prompt",
				ToolIDs:       []string{"gmail.send"},
				ActionClasses: []string{"sensitive_send"},
			},
		},
		{
			name: "empty_action_classes",
			rule: permissions.RuleEditable{
				Effect:  "allow",
				ToolIDs: []string{"gmail.send"},
			},
		},
		{
			name: "invalid_action_class",
			rule: permissions.RuleEditable{
				Effect:        "allow",
				ToolIDs:       []string{"gmail.send"},
				ActionClasses: []string{"delete_everything"},
			},
		},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := env.svc.SaveRule(ctx, "", "req-invalid-"+tc.name, 0, tc.rule); !errors.Is(err, state.ErrInvalidResource) {
				t.Fatalf("SaveRule = %v, want ErrInvalidResource", err)
			}
			if _, _, err := env.svc.RuleWrites("rule_invalid", 1, tc.rule); !errors.Is(err, state.ErrInvalidResource) {
				t.Fatalf("RuleWrites = %v, want ErrInvalidResource", err)
			}
		})
	}

	// Create two allow rules with explicit IDs in deterministic sort order:
	// rule_a has MaxOperations=1, rule_b has MaxOperations=2.
	ruleA, _, err := env.svc.RuleWrites("rule_a", 1, permissions.RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{"gmail.send"},
		ConnectionIDs: []string{"conn-1"},
		ActionClasses: []string{"sensitive_send"},
		MaxOperations: 1,
	})
	if err != nil {
		t.Fatalf("RuleWrites rule_a: %v", err)
	}
	if _, err := env.store.PutResource(ctx, ruleA, 0); err != nil {
		t.Fatalf("PutResource rule_a: %v", err)
	}
	ruleB, _, err := env.svc.RuleWrites("rule_b", 1, permissions.RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{"gmail.send"},
		ConnectionIDs: []string{"conn-1"},
		ActionClasses: []string{"sensitive_send"},
		MaxOperations: 2,
	})
	if err != nil {
		t.Fatalf("RuleWrites rule_b: %v", err)
	}
	if _, err := env.store.PutResource(ctx, ruleB, 0); err != nil {
		t.Fatalf("PutResource rule_b: %v", err)
	}

	req := permissions.Request{
		RunID:        "run-quota",
		Origin:       "user",
		ToolID:       "gmail.send",
		Effect:       "sensitive_send",
		ConnectionID: "conn-1",
	}

	// Call 1 consumes rule_a (1/1).
	d1, err := env.svc.Check(ctx, req)
	if err != nil || d1.Result != "allow" || len(d1.RuleIDs) != 1 || d1.RuleIDs[0] != "rule_a" {
		t.Fatalf("call 1 = (%+v, %v), want allow via rule_a", d1, err)
	}
	// Call 2 sees rule_a exhausted and falls through to rule_b (1/2).
	d2, err := env.svc.Check(ctx, req)
	if err != nil || d2.Result != "allow" || len(d2.RuleIDs) != 1 || d2.RuleIDs[0] != "rule_b" {
		t.Fatalf("call 2 = (%+v, %v), want allow fallthrough to rule_b", d2, err)
	}
	// Call 3 consumes rule_b (2/2).
	d3, err := env.svc.Check(ctx, req)
	if err != nil || d3.Result != "allow" || len(d3.RuleIDs) != 1 || d3.RuleIDs[0] != "rule_b" {
		t.Fatalf("call 3 = (%+v, %v), want allow via rule_b", d3, err)
	}
	// Call 4: both rules exhausted -> falls through to needs_interaction (UserSends="ask").
	d4, err := env.svc.Check(ctx, req)
	if err != nil || d4.Result != "needs_interaction" {
		t.Fatalf("call 4 = (%+v, %v), want needs_interaction after all rules exhausted", d4, err)
	}

	// Verify persisted Used counts match limits.
	gotA, err := env.svc.Rule(ctx, "rule_a")
	if err != nil || gotA.Used != 1 {
		t.Fatalf("rule_a after exhaustion = (%+v, %v), want Used=1", gotA, err)
	}
	gotB, err := env.svc.Rule(ctx, "rule_b")
	if err != nil || gotB.Used != 2 {
		t.Fatalf("rule_b after exhaustion = (%+v, %v), want Used=2", gotB, err)
	}
}

func TestRespondIdempotencyRevisionConflictAndExpiry(t *testing.T) {
	ctx := context.Background()

	t.Run("approval_idempotency_conflict_and_check_allow", func(t *testing.T) {
		env := newTestEnv(t)
		rev := int64(3)
		expires := env.currentTime().Add(10 * time.Minute)
		opened, err := env.svc.Open(ctx, permissions.Interaction{
			ID:                  "int_approve_1",
			TaskID:              "run-approve-1",
			InstructionRevision: &rev,
			Kind:                "approval",
			Prompt:              "Send invoice to client",
			ActionDigest:        "digest-abc",
			ExpiresAt:           &expires,
		}, "email")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if opened.Revision != 1 || opened.Status != "pending" {
			t.Fatalf("opened = %+v, want rev=1 status=pending", opened)
		}

		// Wrong revision must return ErrRevisionConflict without resolving.
		if _, _, err := env.svc.Respond(ctx, opened.ID, "req-bad-rev", 2, permissions.Response{
			Kind:         "approve",
			ActionDigest: "digest-abc",
		}); !errors.Is(err, state.ErrRevisionConflict) {
			t.Fatalf("wrong revision err = %v, want ErrRevisionConflict", err)
		}

		// Wrong action digest must return ErrInteractionStale without resolving.
		if _, _, err := env.svc.Respond(ctx, opened.ID, "req-bad-digest", 1, permissions.Response{
			Kind:         "approve",
			ActionDigest: "digest-wrong",
		}); !errors.Is(err, permissions.ErrInteractionStale) {
			t.Fatalf("wrong digest err = %v, want ErrInteractionStale", err)
		}

		// First valid response resolves the interaction and records the approval.
		resp := permissions.Response{Kind: "approve", ActionDigest: "digest-abc"}
		receipt1, ans1, err := env.svc.Respond(ctx, opened.ID, "req-ok-1", 1, resp)
		if err != nil {
			t.Fatalf("Respond #1: %v", err)
		}
		if receipt1.Status != "applied" || ans1 == nil || ans1.TaskID != "run-approve-1" || ans1.CommandID == "" {
			t.Fatalf("Respond #1 = (%+v, %+v), want applied receipt + answer", receipt1, ans1)
		}

		// Idempotent retry with identical requestID/payload/expected returns the saved receipt and same Answer before delivery.
		receipt2, ans2, err := env.svc.Respond(ctx, opened.ID, "req-ok-1", 1, resp)
		if err != nil {
			t.Fatalf("Respond idempotent retry: %v", err)
		}
		if receipt2.Status != "applied" || string(receipt2.Resource) != string(receipt1.Resource) {
			t.Fatalf("idempotent retry receipt mismatch: got=%+v want=%+v", receipt2, receipt1)
		}
		if ans2 == nil || ans2.CommandID != ans1.CommandID || ans2.Text != ans1.Text {
			t.Fatalf("idempotent retry answer mismatch: got=%+v want=%+v", ans2, ans1)
		}

		// Same requestID with a DIFFERENT payload must return ErrIdempotencyConflict.
		if _, _, err := env.svc.Respond(ctx, opened.ID, "req-ok-1", 1, permissions.Response{
			Kind:         "decline",
			ActionDigest: "digest-abc",
		}); !errors.Is(err, state.ErrIdempotencyConflict) {
			t.Fatalf("conflicting idempotency retry err = %v, want ErrIdempotencyConflict", err)
		}

		// After MarkDelivered, idempotent retry returns receipt with nil Answer.
		env.svc.MarkDelivered(ctx, opened.ID)
		_, ans3, err := env.svc.Respond(ctx, opened.ID, "req-ok-1", 1, resp)
		if err != nil || ans3 != nil {
			t.Fatalf("retry after MarkDelivered = (ans=%+v, err=%v), want (nil, nil)", ans3, err)
		}

		// New requestID on already-resolved interaction returns ErrInteractionStale.
		if _, _, err := env.svc.Respond(ctx, opened.ID, "req-second", 2, resp); !errors.Is(err, permissions.ErrInteractionStale) {
			t.Fatalf("respond on resolved interaction err = %v, want ErrInteractionStale", err)
		}

		// Recorded approval authorizes the exact (RunID, Revision, Digest) via Check.
		dec, err := env.svc.Check(ctx, permissions.Request{
			RunID:    "run-approve-1",
			Revision: 3,
			Origin:   "user",
			ToolID:   "gmail.send",
			Effect:   "sensitive_send",
			Digest:   "digest-abc",
		})
		if err != nil || dec.Result != "allow" || dec.Source != "approval" {
			t.Fatalf("Check after approval = (%+v, %v), want allow/approval", dec, err)
		}
	})

	t.Run("decline_on_decline_callback_runs_once_even_on_retry", func(t *testing.T) {
		env := newTestEnv(t)
		declineCalls := 0
		env.svc.OnDecline = func(_ context.Context, _ permissions.Interaction) string {
			declineCalls++
			return " Draft removed."
		}
		rev := int64(1)
		opened, err := env.svc.Open(ctx, permissions.Interaction{
			ID:                  "int_decline_1",
			TaskID:              "run-decline-1",
			InstructionRevision: &rev,
			Kind:                "approval",
			Prompt:              "Send draft",
			ActionDigest:        "digest-dec",
		}, "email")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		resp := permissions.Response{Kind: "decline", ActionDigest: "digest-dec"}
		_, ans1, err := env.svc.Respond(ctx, opened.ID, "req-dec-1", 1, resp)
		if err != nil || ans1 == nil {
			t.Fatalf("Respond decline = (%+v, %v)", ans1, err)
		}
		_, ans2, err := env.svc.Respond(ctx, opened.ID, "req-dec-1", 1, resp)
		if err != nil || ans2 == nil {
			t.Fatalf("Respond decline retry = (%+v, %v)", ans2, err)
		}
		if declineCalls != 1 {
			t.Fatalf("OnDecline calls = %d, want 1", declineCalls)
		}
		if ans1.Text != ans2.Text {
			t.Fatalf("retry answer text drifted: %q vs %q", ans1.Text, ans2.Text)
		}
	})

	t.Run("decline_user_note_forwarding_validation_and_legacy_fallback", func(t *testing.T) {
		env := newTestEnv(t)
		env.svc.OnDecline = func(_ context.Context, _ permissions.Interaction) string {
			return " Draft removed."
		}
		rev := int64(2)
		opened, err := env.svc.Open(ctx, permissions.Interaction{
			ID:                  "int_decline_note",
			TaskID:              "run-decline-note",
			InstructionRevision: &rev,
			Kind:                "approval",
			Prompt:              "Cancel contract with Apex",
			ActionDigest:        "digest-note",
		}, "email")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		// Oversized decline text returns ErrInvalidResource without resolving.
		if _, _, err := env.svc.Respond(ctx, opened.ID, "req-too-big", 1, permissions.Response{
			Kind:         "decline",
			ActionDigest: "digest-note",
			Text:         strings.Repeat("x", state.MaxMessageBytes+1),
		}); !errors.Is(err, state.ErrInvalidResource) {
			t.Fatalf("oversized decline text err = %v, want ErrInvalidResource", err)
		}

		// Non-empty trimmed note is stored on Resolution.Text and appended after prefix before OnDecline.
		resp := permissions.Response{
			Kind:         "decline",
			ActionDigest: "digest-note",
			Text:         "  Wait until CFO approves Q2 bridge batch.  ",
		}
		_, ans1, err := env.svc.Respond(ctx, opened.ID, "req-note-ok", 1, resp)
		if err != nil || ans1 == nil {
			t.Fatalf("Respond decline with note = (%+v, %v)", ans1, err)
		}
		wantPrefix := "The user declined this action. Do not attempt it; report what was not done: Cancel contract with Apex User note: Wait until CFO approves Q2 bridge batch. Draft removed."
		if ans1.Text != wantPrefix {
			t.Fatalf("ans1.Text = %q, want %q", ans1.Text, wantPrefix)
		}
		gotInter, err := env.svc.Interaction(ctx, opened.ID)
		if err != nil || gotInter.Resolution == nil || gotInter.Resolution.Text != "Wait until CFO approves Q2 bridge batch." {
			t.Fatalf("Interaction resolution = (%+v, %v)", gotInter.Resolution, err)
		}
		// Idempotent retry returns identical text.
		_, ans2, err := env.svc.Respond(ctx, opened.ID, "req-note-ok", 1, resp)
		if err != nil || ans2 == nil || ans2.Text != ans1.Text {
			t.Fatalf("retry answer = (%+v, %v), want Text=%q", ans2, err, ans1.Text)
		}

		// Whitespace-only text preserves exact legacy answer text without " User note:".
		openedWS, err := env.svc.Open(ctx, permissions.Interaction{
			ID:                  "int_decline_ws",
			TaskID:              "run-decline-ws",
			InstructionRevision: &rev,
			Kind:                "approval",
			Prompt:              "Send email",
			ActionDigest:        "digest-ws",
		}, "email")
		if err != nil {
			t.Fatalf("Open ws: %v", err)
		}
		_, ansWS, err := env.svc.Respond(ctx, openedWS.ID, "req-ws", 1, permissions.Response{
			Kind:         "decline",
			ActionDigest: "digest-ws",
			Text:         "   \t\n  ",
		})
		if err != nil || ansWS == nil {
			t.Fatalf("Respond decline ws = (%+v, %v)", ansWS, err)
		}
		wantWS := "The user declined this action. Do not attempt it; report what was not done: Send email Draft removed."
		if ansWS.Text != wantWS {
			t.Fatalf("ansWS.Text = %q, want %q", ansWS.Text, wantWS)
		}
		env.svc.MarkDelivered(ctx, opened.ID)
		env.svc.MarkDelivered(ctx, openedWS.ID)

		// Legacy fallback in UndeliveredAnswers (when answer_text is empty) preserves User note.
		legacyBody := map[string]any{
			"id":            "int_decline_legacy",
			"task_id":       "run-decline-legacy",
			"kind":          "approval",
			"status":        "resolved",
			"prompt":        "Delete archive",
			"action_digest": "digest-legacy",
			"resolution": map[string]any{
				"kind": "decline",
				"text": "Keep for audit",
				"at":   env.currentTime().Format(time.RFC3339Nano),
			},
		}
		legacyRes, err := state.NewResource(permissions.CollectionInteractions, "int_decline_legacy", 0, legacyBody, map[string]string{"status": "resolved", "task_id": "run-decline-legacy"}, env.currentTime())
		if err != nil {
			t.Fatalf("NewResource legacy: %v", err)
		}
		if _, err := env.store.PutResource(ctx, legacyRes, 0); err != nil {
			t.Fatalf("PutResource legacy: %v", err)
		}
		undeliv, ids, err := env.svc.UndeliveredAnswers(ctx)
		if err != nil || len(undeliv) != 1 || len(ids) != 1 || ids[0] != "int_decline_legacy" {
			t.Fatalf("UndeliveredAnswers = (%+v, %+v, %v), want 1 legacy answer", undeliv, ids, err)
		}
		wantLegacy := "The user declined this action. Do not attempt it; report what was not done: Delete archive User note: Keep for audit"
		if undeliv[0].Text != wantLegacy {
			t.Fatalf("UndeliveredAnswers[0].Text = %q, want %q", undeliv[0].Text, wantLegacy)
		}
	})

	t.Run("expiry_blocks_respond_and_updates_status_queries", func(t *testing.T) {
		env := newTestEnv(t)
		expires := env.currentTime().Add(5 * time.Minute)
		opened, err := env.svc.Open(ctx, permissions.Interaction{
			ID:        "int_exp_1",
			TaskID:    "run-exp-1",
			Kind:      "question",
			Prompt:    "Which folder?",
			ExpiresAt: &expires,
		}, "")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		// Before expiry, listed under pending and not expired.
		pending, _, err := env.svc.Interactions(ctx, "pending", "", "", 20)
		if err != nil || len(pending) != 1 || pending[0].ID != opened.ID {
			t.Fatalf("pending before expiry = (%+v, %v), want [%s]", pending, err, opened.ID)
		}

		// Advance clock past ExpiresAt.
		env.advance(5*time.Minute + time.Second)

		got, err := env.svc.Interaction(ctx, opened.ID)
		if err != nil || got.Status != "expired" {
			t.Fatalf("Interaction after expiry = (%+v, %v), want status=expired", got, err)
		}
		pending, _, err = env.svc.Interactions(ctx, "pending", "", "", 20)
		if err != nil || len(pending) != 0 {
			t.Fatalf("pending after expiry = (%+v, %v), want empty", pending, err)
		}
		expired, _, err := env.svc.Interactions(ctx, "expired", "", "", 20)
		if err != nil || len(expired) != 1 || expired[0].ID != opened.ID {
			t.Fatalf("expired list = (%+v, %v), want [%s]", expired, err, opened.ID)
		}

		if _, _, err := env.svc.Respond(ctx, opened.ID, "req-exp-1", 1, permissions.Response{
			Kind: "answer",
			Text: "Archive",
		}); !errors.Is(err, permissions.ErrInteractionStale) {
			t.Fatalf("Respond on expired interaction = %v, want ErrInteractionStale", err)
		}
	})
}

func TestThreeStatePrecedenceDenyOverAskOverAllow(t *testing.T) {
	ctx := context.Background()

	t.Run("summarize_ask_rule", func(t *testing.T) {
		summary := permissions.Summarize(permissions.RuleEditable{
			Effect:        "ask",
			ToolIDs:       []string{"gmail.send"},
			ConnectionIDs: []string{"conn-gmail"},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{"ceo@example.com"},
		})
		if !strings.HasPrefix(summary, "Ask before sends with gmail.send on conn-gmail") || !strings.Contains(summary, "ceo@example.com") {
			t.Fatalf("unexpected ask summary: %q", summary)
		}
	})

	t.Run("ask_overrides_allow_and_user_reversible_write_and_approval_lets_retry_succeed", func(t *testing.T) {
		env := newTestEnv(t)
		env.svc.UserSends = "allow" // Even with UserSends="allow", ask rule must require approval.

		allowRule := mustSaveRule(t, env.svc, "req-allow-all", permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{permissions.Wildcard},
			ConnectionIDs: []string{permissions.Wildcard},
			ResourceIDs:   []string{permissions.Wildcard},
			ActionClasses: []string{"sensitive_send", "reversible_write"},
			Recipients:    []string{permissions.Wildcard},
		})
		askRule := mustSaveRule(t, env.svc, "req-ask-pi", permissions.RuleEditable{
			Effect:        "ask",
			ToolIDs:       []string{"gmail.send"},
			ConnectionIDs: []string{permissions.Wildcard},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{"a.vance@ucl.sim.local"},
		})
		_ = allowRule

		req := permissions.Request{
			RunID:        "run-ask-1",
			Revision:     2,
			Origin:       "user",
			ToolID:       "gmail.send",
			Effect:       "sensitive_send",
			ConnectionID: "conn-1",
			Recipients:   []string{"colleague@example.com", "A.VANCE@ucl.sim.local"},
			Digest:       "digest-ask-1",
		}
		// 1. Ask beats Allow automatically.
		d1, err := env.svc.Check(ctx, req)
		if err != nil || d1.Result != "needs_interaction" || len(d1.RuleIDs) != 1 || d1.RuleIDs[0] != askRule.ID {
			t.Fatalf("Check with ask+allow = (%+v, %v), want needs_interaction via %s", d1, err, askRule.ID)
		}
		if !strings.Contains(d1.Reason, "A custom rule requires your approval:") {
			t.Fatalf("unexpected ask reason: %q", d1.Reason)
		}

		// 2. Non-matching recipient falls through ask to allow rule.
		otherReq := req
		otherReq.Recipients = []string{"colleague@example.com"}
		dOther, err := env.svc.Check(ctx, otherReq)
		if err != nil || dOther.Result != "allow" || dOther.Source != "permission_rule" {
			t.Fatalf("Check non-matching ask recipient = (%+v, %v), want allow via permission_rule", dOther, err)
		}

		// 3. Approving the exact-digest interaction lets the retried call succeed with ApprovedByHuman=true!
		rev := int64(2)
		opened, err := env.svc.Open(ctx, permissions.Interaction{
			ID:                  "int_ask_1",
			TaskID:              "run-ask-1",
			InstructionRevision: &rev,
			Kind:                "approval",
			Prompt:              "Send to Dr. Vance",
			ActionDigest:        "digest-ask-1",
			ToolID:              "gmail.send",
			ActionClass:         "sensitive_send",
			ConnectionID:        "conn-1",
			Recipients:          req.Recipients,
		}, "")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, _, err := env.svc.Respond(ctx, opened.ID, "req-approve-ask-1", 1, permissions.Response{
			Kind:         "approve",
			ActionDigest: "digest-ask-1",
		}); err != nil {
			t.Fatalf("Respond approve: %v", err)
		}
		d2, err := env.svc.Check(ctx, req)
		if err != nil || d2.Result != "allow" || d2.Source != "approval" || !d2.ApprovedByHuman {
			t.Fatalf("Check after approving ask card = (%+v, %v), want allow/approval/ApprovedByHuman=true", d2, err)
		}

		// 4. Adding a Deny rule still beats both Ask and the recorded human approval!
		denyRule := mustSaveRule(t, env.svc, "req-deny-pi", permissions.RuleEditable{
			Effect:        "deny",
			ToolIDs:       []string{"gmail.send"},
			ConnectionIDs: []string{permissions.Wildcard},
			ActionClasses: []string{"sensitive_send"},
			Recipients:    []string{"a.vance@ucl.sim.local"},
		})
		d3, err := env.svc.Check(ctx, req)
		if err != nil || d3.Result != "deny" || len(d3.RuleIDs) != 1 || d3.RuleIDs[0] != denyRule.ID {
			t.Fatalf("Check with deny+ask+approval = (%+v, %v), want deny via %s", d3, err, denyRule.ID)
		}
	})

	t.Run("default_policy_asks_on_sensitive_send_and_unknown_allows_user_reversible_write", func(t *testing.T) {
		env := newTestEnv(t)
		env.svc.UserSends = "" // Unset default must behave as "ask".

		dSend, err := env.svc.Check(ctx, permissions.Request{
			RunID: "run-def", Origin: "user", ToolID: "gmail.send", Effect: "sensitive_send",
		})
		if err != nil || dSend.Result != "needs_interaction" {
			t.Fatalf("default sensitive_send = (%+v, %v), want needs_interaction", dSend, err)
		}
		dUnknown, err := env.svc.Check(ctx, permissions.Request{
			RunID: "run-def", Origin: "user", ToolID: "mcp.tool", Effect: "unknown",
		})
		if err != nil || dUnknown.Result != "needs_interaction" {
			t.Fatalf("default unknown = (%+v, %v), want needs_interaction", dUnknown, err)
		}
		dWriteUser, err := env.svc.Check(ctx, permissions.Request{
			RunID: "run-def", Origin: "user", ToolID: "calendar.create", Effect: "reversible_write",
		})
		if err != nil || dWriteUser.Result != "allow" || dWriteUser.Source != "user_request" {
			t.Fatalf("user reversible_write = (%+v, %v), want allow/user_request", dWriteUser, err)
		}
		dWriteAuto, err := env.svc.Check(ctx, permissions.Request{
			RunID: "run-def", Origin: "automation", ToolID: "calendar.create", Effect: "reversible_write",
		})
		if err != nil || dWriteAuto.Result != "needs_interaction" {
			t.Fatalf("automation reversible_write = (%+v, %v), want needs_interaction", dWriteAuto, err)
		}
	})
}

func TestRespondAlwaysAllowCreatesAtomicRule(t *testing.T) {
	ctx := context.Background()

	t.Run("approve_with_remember_rule_creates_scoped_allow_rule_atomically", func(t *testing.T) {
		env := newTestEnv(t)
		rev := int64(1)
		opened, err := env.svc.Open(ctx, permissions.Interaction{
			ID:                  "int_always_1",
			TaskID:              "run-always-1",
			InstructionRevision: &rev,
			Kind:                "approval",
			Prompt:              "Send status update to alice@example.com",
			ActionDigest:        "digest-1",
			ConnectionID:        "conn-gmail-work",
			ToolID:              "google/gmail.send",
			ToolTitle:           "Send email",
			ActionClass:         "sensitive_send",
			Recipients:          []string{"alice@example.com"},
		}, "")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		// Ensure private fields are never projected in wire JSON.
		raw, _ := json.Marshal(opened)
		for _, secretKey := range []string{"private_tool_id", "private_action_class", "private_connection_id", "google/gmail.send"} {
			if strings.Contains(string(raw), secretKey) {
				t.Fatalf("projected Interaction JSON leaked private metadata %q: %s", secretKey, string(raw))
			}
		}

		resp := permissions.Response{Kind: "approve", ActionDigest: "digest-1", RememberRule: true}
		receipt1, ans1, err := env.svc.Respond(ctx, opened.ID, "req-always-1", 1, resp)
		if err != nil || receipt1.Status != "applied" || ans1 == nil {
			t.Fatalf("Respond always allow = (%+v, %+v, %v)", receipt1, ans1, err)
		}

		// Idempotent retry must not fail or duplicate the created rule.
		if _, _, err := env.svc.Respond(ctx, opened.ID, "req-always-1", 1, resp); err != nil {
			t.Fatalf("idempotent retry: %v", err)
		}

		rules, err := env.svc.Rules(ctx)
		if err != nil || len(rules) != 1 {
			t.Fatalf("Rules after Always allow = (%+v, %v), want 1 rule", rules, err)
		}
		r := rules[0]
		if r.Effect != "allow" || r.Title != "Always allow Send email" || r.Summary == "" {
			t.Fatalf("unexpected created rule: %+v", r)
		}
		if len(r.ToolIDs) != 1 || r.ToolIDs[0] != "google/gmail.send" {
			t.Fatalf("ToolIDs = %v, want [google/gmail.send]", r.ToolIDs)
		}
		if len(r.ConnectionIDs) != 1 || r.ConnectionIDs[0] != "conn-gmail-work" {
			t.Fatalf("ConnectionIDs = %v, want [conn-gmail-work]", r.ConnectionIDs)
		}
		if len(r.ResourceIDs) != 1 || r.ResourceIDs[0] != permissions.Wildcard {
			t.Fatalf("ResourceIDs = %v, want [*]", r.ResourceIDs)
		}
		if len(r.Recipients) != 1 || r.Recipients[0] != permissions.Wildcard {
			t.Fatalf("Recipients = %v, want [*]", r.Recipients)
		}

		// Subsequent call to same tool/account with a new digest and recipient auto-allows via permission_rule.
		dNext, err := env.svc.Check(ctx, permissions.Request{
			RunID:        "run-always-2",
			Revision:     1,
			Origin:       "user",
			ToolID:       "google/gmail.send",
			Effect:       "sensitive_send",
			ConnectionID: "conn-gmail-work",
			Recipients:   []string{"bob@example.com"},
			Digest:       "digest-2",
		})
		if err != nil || dNext.Result != "allow" || dNext.Source != "permission_rule" || len(dNext.RuleIDs) != 1 || dNext.RuleIDs[0] != r.ID {
			t.Fatalf("Check on subsequent call = (%+v, %v), want allow via %s", dNext, err, r.ID)
		}

		// Different account is NOT covered by the connection-scoped rule.
		dOtherConn, err := env.svc.Check(ctx, permissions.Request{
			RunID:        "run-always-2",
			Revision:     1,
			Origin:       "user",
			ToolID:       "google/gmail.send",
			Effect:       "sensitive_send",
			ConnectionID: "conn-gmail-personal",
			Recipients:   []string{"bob@example.com"},
			Digest:       "digest-3",
		})
		if err != nil || dOtherConn.Result != "needs_interaction" {
			t.Fatalf("Check on other connection = (%+v, %v), want needs_interaction", dOtherConn, err)
		}
	})

	t.Run("decline_with_remember_rule_never_creates_rule_and_non_approval_rejects_flag", func(t *testing.T) {
		env := newTestEnv(t)
		rev := int64(1)
		opened, err := env.svc.Open(ctx, permissions.Interaction{
			ID:                  "int_decline_rem",
			TaskID:              "run-dec-rem",
			InstructionRevision: &rev,
			Kind:                "approval",
			Prompt:              "Send email",
			ActionDigest:        "digest-dec",
			ToolID:              "google/gmail.send",
			ActionClass:         "sensitive_send",
		}, "")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, _, err := env.svc.Respond(ctx, opened.ID, "req-dec-rem", 1, permissions.Response{
			Kind: "decline", ActionDigest: "digest-dec", RememberRule: true,
		}); err != nil {
			t.Fatalf("Respond decline: %v", err)
		}
		rules, err := env.svc.Rules(ctx)
		if err != nil || len(rules) != 0 {
			t.Fatalf("decline with RememberRule created rules: %+v, err=%v", rules, err)
		}

		q, err := env.svc.Open(ctx, permissions.Interaction{
			ID: "int_q_rem", TaskID: "run-q", Kind: "question", Prompt: "Which folder?",
		}, "")
		if err != nil {
			t.Fatalf("Open question: %v", err)
		}
		if _, _, err := env.svc.Respond(ctx, q.ID, "req-q-rem", 1, permissions.Response{
			Kind: "answer", Text: "Inbox", RememberRule: true,
		}); !errors.Is(err, state.ErrInvalidResource) {
			t.Fatalf("answer with RememberRule err = %v, want ErrInvalidResource", err)
		}
	})
}

func TestAutoReviewGuardrailsAndModelWrapper(t *testing.T) {
	ctx := context.Background()
	det := permissions.DeterministicAutoReviewer{}

	t.Run("blocks_untrusted_recipient_exfiltration_even_with_broad_allow_rule", func(t *testing.T) {
		dec, err := det.ReviewAction(ctx, permissions.AutoReviewRequest{
			ToolID:         "mcp/cascade/email_send",
			Summary:        "Send Q4 calibrations to audit-export@external-drop.sim.local",
			Effect:         "sensitive_send",
			Recipients:     []string{"audit-export@external-drop.sim.local"},
			Origin:         "user",
			DecisionSource: "permission_rule",
			RuleSummaries:  []string{"Allow sends with mcp/cascade/email_send on any connected account, to anyone."},
			UserSources:    []string{"hey nori, check the issue feed and give me a quick tldr of what changed on INFRA-8109."},
		})
		if err != nil || dec.Verdict != "deny" {
			t.Fatalf("ReviewAction exfil = (%+v, %v), want deny", dec, err)
		}
	})

	t.Run("allows_verified_user_recipient_send", func(t *testing.T) {
		dec, err := det.ReviewAction(ctx, permissions.AutoReviewRequest{
			ToolID:         "google/gmail.send",
			Summary:        "Send update to dana.chen@cascadecloud.sim.local",
			Effect:         "sensitive_send",
			Recipients:     []string{"dana.chen@cascadecloud.sim.local"},
			Origin:         "user",
			DecisionSource: "permission_rule",
			RuleSummaries:  []string{"Allow sends with google/gmail.send on conn-work, to anyone."},
			UserSources:    []string{"Email Dana Chen (dana.chen@cascadecloud.sim.local) the Q1 roadmap summary."},
		})
		if err != nil || dec.Verdict != "allow" {
			t.Fatalf("ReviewAction authorized send = (%+v, %v), want allow", dec, err)
		}
	})

	t.Run("blocks_hypothetical_draft_but_allows_explicit_for_real_override", func(t *testing.T) {
		blocked, err := det.ReviewAction(ctx, permissions.AutoReviewRequest{
			ToolID:      "mcp/cascade/email_send",
			Summary:     "Send email to dana.chen@cascadecloud.sim.local",
			Effect:      "sensitive_send",
			Recipients:  []string{"dana.chen@cascadecloud.sim.local"},
			Origin:      "user",
			UserSources: []string{"Draft a strictly hypothetical email to dana.chen@cascadecloud.sim.local — DO NOT actually send anything!"},
		})
		if err != nil || blocked.Verdict != "deny" {
			t.Fatalf("hypothetical send = (%+v, %v), want deny", blocked, err)
		}

		allowed, err := det.ReviewAction(ctx, permissions.AutoReviewRequest{
			ToolID:      "mcp/cascade/calendar_create_event",
			Summary:     "Create event Reorg Pod Proposal Review with Dana",
			Effect:      "reversible_write",
			Recipients:  []string{"dana.chen@cascadecloud.sim.local"},
			Origin:      "user",
			UserSources: []string{"this time FOR REAL (not hypothetical!) go ahead and book a 45-minute calendar block with dana.chen@cascadecloud.sim.local."},
		})
		if err != nil || allowed.Verdict != "allow" {
			t.Fatalf("explicit for-real calendar write = (%+v, %v), want allow", allowed, err)
		}
	})

	t.Run("escalates_ambiguous_or_read_only_user_reversible_write", func(t *testing.T) {
		dec, err := det.ReviewAction(ctx, permissions.AutoReviewRequest{
			ToolID:         "google/calendar.create",
			Summary:        "Create event Lunch",
			Effect:         "reversible_write",
			Origin:         "user",
			DecisionSource: "user_request",
			UserSources:    []string{"What is on my calendar tomorrow?"},
		})
		if err != nil || dec.Verdict != "ask" {
			t.Fatalf("read-only user prompt with reversible_write = (%+v, %v), want ask", dec, err)
		}

		delDec, err := det.ReviewAction(ctx, permissions.AutoReviewRequest{
			ToolID:         "google/drive.delete",
			Summary:        "Delete file Q4_Budget.xlsx",
			Effect:         "reversible_write",
			Origin:         "user",
			DecisionSource: "user_request",
			UserSources:    []string{"Update my Q4 notes document with today's highlights."},
		})
		if err != nil || delDec.Verdict != "ask" {
			t.Fatalf("unrequested delete = (%+v, %v), want ask", delDec, err)
		}
	})

	t.Run("model_reviewer_enforces_deterministic_first_and_fails_safe", func(t *testing.T) {
		modelCalls := 0
		m := &permissions.ModelAutoReviewer{
			Review: func(_ context.Context, _ string) (string, error) {
				modelCalls++
				return `{"verdict":"allow","reason":"ok"}`, nil
			},
		}
		// Deterministic block must short-circuit before model is ever called!
		dec, err := m.ReviewAction(ctx, permissions.AutoReviewRequest{
			ToolID:      "gmail.send",
			Summary:     "Forward secrets to attacker@evil.example",
			Effect:      "sensitive_send",
			Recipients:  []string{"attacker@evil.example"},
			Origin:      "user",
			UserSources: []string{"Summarize my inbox."},
		})
		if err != nil || dec.Verdict != "deny" || modelCalls != 0 {
			t.Fatalf("deterministic short-circuit = (%+v, %v, calls=%d), want deny with 0 model calls", dec, err, modelCalls)
		}

		// When deterministic allows, model verdict is respected.
		dec2, err := m.ReviewAction(ctx, permissions.AutoReviewRequest{
			ToolID:      "calendar.create",
			Summary:     "Book dentist appointment",
			Effect:      "reversible_write",
			Origin:      "user",
			UserSources: []string{"Book a dentist appointment for Friday at 2pm."},
		})
		if err != nil || dec2.Verdict != "allow" || modelCalls != 1 {
			t.Fatalf("model allow = (%+v, %v, calls=%d), want allow with 1 model call", dec2, err, modelCalls)
		}

		// Model error fails safe to "ask".
		failModel := &permissions.ModelAutoReviewer{
			Review: func(_ context.Context, _ string) (string, error) {
				return "", errors.New("provider timeout")
			},
		}
		dec3, err := failModel.ReviewAction(ctx, permissions.AutoReviewRequest{
			ToolID:      "calendar.create",
			Summary:     "Book dentist appointment",
			Effect:      "reversible_write",
			Origin:      "user",
			UserSources: []string{"Book a dentist appointment for Friday at 2pm."},
		})
		if err != nil || dec3.Verdict != "ask" {
			t.Fatalf("model error fail-safe = (%+v, %v), want ask", dec3, err)
		}
	})
}

func TestMCPServerConnectionIDRuleMatching(t *testing.T) {
	ctx := context.Background()
	svc := newTestEnv(t).svc

	// Standing "Always allow" rule created via approval card (connection_ids: ["*"], action_classes: ["unknown"]).
	if _, err := svc.SaveRule(ctx, "", "req-allow-mcp", 0, permissions.RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{"mcp/mcp_srv1/email_send"},
		ConnectionIDs: []string{"*"},
		ResourceIDs:   []string{"*"},
		ActionClasses: []string{"unknown"},
		Title:         "Always allow email_send",
	}); err != nil {
		t.Fatalf("SaveRule allow: %v", err)
	}

	// Verify Always allow works on mcp_srv1.
	dec, err := svc.Check(ctx, permissions.Request{
		RunID: "run-1", Origin: "user", ToolID: "mcp/mcp_srv1/email_send", Effect: "unknown",
	})
	if err != nil || dec.Result != "allow" {
		t.Fatalf("Check before ask rule = (%+v, %v), want allow", dec, err)
	}

	// Add Settings "Ask first" rule targeting mcp_srv1 via connection_ids: ["mcp_srv1"] with tool_ids: ["*"].
	if _, err := svc.SaveRule(ctx, "", "req-ask-mcp", 0, permissions.RuleEditable{
		Effect:        "ask",
		ToolIDs:       []string{"*"},
		ConnectionIDs: []string{"mcp_srv1"},
		ResourceIDs:   []string{"*"},
		ActionClasses: []string{"unknown"},
		Title:         "Ask before qa-workspace MCP actions",
	}); err != nil {
		t.Fatalf("SaveRule ask: %v", err)
	}

	// Ask rule targeting mcp_srv1 must override the Always allow rule!
	dec2, err := svc.Check(ctx, permissions.Request{
		RunID: "run-2", Origin: "user", ToolID: "mcp/mcp_srv1/email_send", Effect: "unknown",
	})
	if err != nil || dec2.Result != "needs_interaction" {
		t.Fatalf("Check with mcp_srv1 ask rule = (%+v, %v), want needs_interaction", dec2, err)
	}

	// Ask rule for mcp_srv1 must NOT match a different MCP server (mcp_srv2).
	if _, err := svc.SaveRule(ctx, "", "req-allow-mcp2", 0, permissions.RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{"mcp/mcp_srv2/email_send"},
		ConnectionIDs: []string{},
		ResourceIDs:   []string{},
		ActionClasses: []string{"unknown"},
	}); err != nil {
		t.Fatalf("SaveRule allow mcp_srv2: %v", err)
	}
	dec3, err := svc.Check(ctx, permissions.Request{
		RunID: "run-3", Origin: "user", ToolID: "mcp/mcp_srv2/email_send", Effect: "unknown",
	})
	if err != nil || dec3.Result != "allow" {
		t.Fatalf("Check on mcp_srv2 (empty connection_ids allow rule) = (%+v, %v), want allow", dec3, err)
	}
}
