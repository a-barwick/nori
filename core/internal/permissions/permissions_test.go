package permissions_test

import (
	"context"
	"encoding/json"
	"errors"
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
