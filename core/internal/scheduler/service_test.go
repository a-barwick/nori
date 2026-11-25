package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

func newTestScheduler(t *testing.T, now time.Time, zone string) *scheduler.Service {
	t.Helper()
	mem := memstore.New(memstore.WithClock(func() time.Time { return now }))
	user := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "sched@example.com"}
	db := records.NewShared(mem, user, 1.0)
	db.Now = func() time.Time { return now }
	return &scheduler.Service{
		Store:       db,
		Tasks:       db,
		Permissions: &permissions.Service{Store: db, Now: func() time.Time { return now }},
		Timezone:    func(context.Context) string { return zone },
		Now:         func() time.Time { return now },
	}
}

func TestOptionalMissedRunDefaultsAndPreviewCopy(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 6, 55, 0, 0, time.UTC)
	svc := newTestScheduler(t, now, "America/Los_Angeles")
	at := time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC)

	t.Run("once_reminder_omits_missed_run_and_gets_defaults", func(t *testing.T) {
		editable := scheduler.Editable{
			Title:         "Check supernino updates",
			Instruction:   "Remind me to check out updates to supernino",
			Trigger:       scheduler.Trigger{Kind: "once", At: &at},
			MaxActiveRuns: 1,
		}
		preview, err := svc.Preview(ctx, editable, "confirm_writes")
		if err != nil {
			t.Fatalf("Preview: %v", err)
		}
		if preview.MissedRunExplanation != "" {
			t.Fatalf("Preview.MissedRunExplanation = %q, want empty when omitted", preview.MissedRunExplanation)
		}
		if preview.AuthoritySummary != "" {
			t.Fatalf("Preview.AuthoritySummary = %q, want empty when no connections", preview.AuthoritySummary)
		}
		if len(preview.Warnings) != 0 {
			t.Fatalf("Preview.Warnings = %+v, want none for Nori-only reminder", preview.Warnings)
		}

		receipt, err := svc.Save(ctx, "", "req-once-default", 0, editable)
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		var saved scheduler.Automation
		if err := json.Unmarshal(receipt.Resource, &saved); err != nil {
			t.Fatalf("unmarshal saved: %v", err)
		}
		if saved.MissedRun != "latest" {
			t.Fatalf("saved.MissedRun = %q, want latest", saved.MissedRun)
		}
		if saved.ExecutionWindowSeconds != scheduler.DefaultOnceExecutionWindowSeconds {
			t.Fatalf("saved.ExecutionWindowSeconds = %d, want %d", saved.ExecutionWindowSeconds, scheduler.DefaultOnceExecutionWindowSeconds)
		}
	})

	t.Run("recurring_routine_omits_missed_run_and_uses_second_person_for_connections", func(t *testing.T) {
		editable := scheduler.Editable{
			Title:         "Morning calendar check",
			Instruction:   "Summarize my calendar for today",
			Trigger:       scheduler.Trigger{Kind: "recurring", LocalStart: "2026-11-25T09:00", RRule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"},
			ConnectionIDs: []string{"conn_google_1"},
			MaxActiveRuns: 1,
		}
		preview, err := svc.Preview(ctx, editable, "confirm_writes")
		if err != nil {
			t.Fatalf("Preview: %v", err)
		}
		if preview.MissedRunExplanation != "" {
			t.Fatalf("Preview.MissedRunExplanation = %q, want empty when omitted", preview.MissedRunExplanation)
		}
		if !strings.Contains(preview.AuthoritySummary, "your connected accounts") {
			t.Fatalf("Preview.AuthoritySummary = %q, want second-person account scope", preview.AuthoritySummary)
		}
		if strings.Contains(preview.AuthoritySummary, "the user's") {
			t.Fatalf("Preview.AuthoritySummary = %q, must not contain third-person copy", preview.AuthoritySummary)
		}

		receipt, err := svc.Save(ctx, "", "req-rec-default", 0, editable)
		if err != nil {
			t.Fatalf("Save: %v", err)
		}
		var saved scheduler.Automation
		if err := json.Unmarshal(receipt.Resource, &saved); err != nil {
			t.Fatalf("unmarshal saved: %v", err)
		}
		if saved.MissedRun != "latest" {
			t.Fatalf("saved.MissedRun = %q, want latest", saved.MissedRun)
		}
		if saved.ExecutionWindowSeconds != 0 {
			t.Fatalf("saved.ExecutionWindowSeconds = %d, want 0 for recurring", saved.ExecutionWindowSeconds)
		}
	})

	t.Run("explicit_missed_run_override_is_preserved_and_invalid_rejected", func(t *testing.T) {
		editable := scheduler.Editable{
			Title:         "Explicit skip reminder",
			Instruction:   "Ping me",
			Trigger:       scheduler.Trigger{Kind: "once", At: &at},
			MissedRun:     "skip",
			MaxActiveRuns: 1,
		}
		preview, err := svc.Preview(ctx, editable, "confirm_writes")
		if err != nil {
			t.Fatalf("Preview explicit skip: %v", err)
		}
		if preview.MissedRunExplanation == "" {
			t.Fatalf("expected non-empty MissedRunExplanation when missed_run explicitly overridden")
		}

		bad := editable
		bad.MissedRun = "whatever"
		if _, err := svc.Preview(ctx, bad, "confirm_writes"); !errors.Is(err, state.ErrInvalidResource) {
			t.Fatalf("Preview invalid missed_run err = %v, want ErrInvalidResource", err)
		}
		if _, err := svc.Save(ctx, "", "req-bad-missed", 0, bad); !errors.Is(err, state.ErrInvalidResource) {
			t.Fatalf("Save invalid missed_run err = %v, want ErrInvalidResource", err)
		}
	})
}
