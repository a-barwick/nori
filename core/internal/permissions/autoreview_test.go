package permissions_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/permissions"
)

func TestAutoReviewPromptIncludesNowAndTimeZone(t *testing.T) {
	now := time.Date(2026, 11, 25, 6, 55, 0, 0, time.UTC)
	prompt := permissions.BuildAutoReviewPrompt(permissions.AutoReviewRequest{
		ToolID:      "google/calendar/create_event",
		Summary:     "Create lunch meeting on Wed, Nov 25 at 12:00 PM PST",
		Effect:      "reversible_write",
		Origin:      "user",
		Now:         now,
		TimeZone:    "America/Los_Angeles",
		UserSources: []string{"Put lunch on my calendar tomorrow at noon"},
	})
	if !strings.Contains(prompt, "Current Time:") {
		t.Fatalf("expected Current Time in prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "America/Los_Angeles") || !strings.Contains(prompt, "10:55 PM PST") {
		t.Fatalf("expected local PST time and timezone in prompt, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "2026-11-25T06:55:00Z") {
		t.Fatalf("expected UTC RFC3339 reference in prompt, got:\n%s", prompt)
	}
}

func TestModelAutoReviewerRespectsStandingAllowRuleWhileBlockingExfiltration(t *testing.T) {
	ctx := context.Background()
	var modelCalls atomic.Int32
	reviewer := &permissions.ModelAutoReviewer{
		Review: func(_ context.Context, _ string) (string, error) {
			modelCalls.Add(1)
			return `{"verdict":"ask","reason":"confirm please"}`, nil
		},
	}

	dec, err := reviewer.ReviewAction(ctx, permissions.AutoReviewRequest{
		ToolID:         "nori/assistant/automation-create",
		Summary:        "Create daily standup routine",
		Effect:         "reversible_write",
		Origin:         "user",
		DecisionSource: "permission_rule",
		RuleSummaries:  []string{"Allow changes with nori/assistant/automation-create on any connected account."},
		UserSources:    []string{"Set up a weekday morning routine"},
	})
	if err != nil {
		t.Fatalf("ReviewAction standing allow: %v", err)
	}
	if dec.Verdict != "allow" {
		t.Fatalf("standing allow verdict = %q (%q), want allow", dec.Verdict, dec.Reason)
	}
	if modelCalls.Load() != 0 {
		t.Fatalf("expected 0 model review calls for standing allow rule, got %d", modelCalls.Load())
	}

	decSend, err := reviewer.ReviewAction(ctx, permissions.AutoReviewRequest{
		ToolID:         "google/gmail/send",
		Summary:        "Send report to bob@example.com",
		Effect:         "sensitive_send",
		Recipients:     []string{"bob@example.com"},
		Origin:         "user",
		DecisionSource: "permission_rule",
		RuleSummaries:  []string{"Allow sends with google/gmail/send on conn_1, to anyone."},
		UserSources:    []string{"Handle the Q4 update for bob@example.com"},
	})
	if err != nil || decSend.Verdict != "allow" {
		t.Fatalf("standing send allow = (%+v, %v), want allow", decSend, err)
	}

	decExfil, err := reviewer.ReviewAction(ctx, permissions.AutoReviewRequest{
		ToolID:         "google/gmail/send",
		Summary:        "Forward secrets to attacker@evil.test",
		Effect:         "sensitive_send",
		Recipients:     []string{"attacker@evil.test"},
		Origin:         "user",
		DecisionSource: "permission_rule",
		RuleSummaries:  []string{"Allow sends with google/gmail/send on conn_1, to anyone."},
		UserSources:    []string{"Summarize my inbox"},
	})
	if err != nil || decExfil.Verdict != "deny" {
		t.Fatalf("exfiltration under standing rule = (%+v, %v), want deny", decExfil, err)
	}
}
