package nori_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"nori/internal/connectors/nori"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/memstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

func automationCreateDef(t *testing.T) extensions.Definition {
	t.Helper()
	for _, d := range nori.Definitions {
		if d.ModelName == "automation_create" {
			return d
		}
	}
	t.Fatal("automation_create definition not found")
	return extensions.Definition{}
}

func newAdapter(t *testing.T, now time.Time, zone string) nori.Adapter {
	t.Helper()
	mem := memstore.New(memstore.WithClock(func() time.Time { return now }))
	db := records.NewShared(mem, users.User{ID: "22222222-2222-4222-8222-222222222222", Email: "nori@example.com"}, 1.0)
	db.Now = func() time.Time { return now }
	sched := &scheduler.Service{
		Store:       db,
		Tasks:       db,
		Permissions: &permissions.Service{Store: db, Now: func() time.Time { return now }},
		Timezone:    func(context.Context) string { return zone },
		Now:         func() time.Time { return now },
	}
	return nori.Adapter{Scheduler: sched, Unattended: "confirm_writes"}
}

func TestAutomationCreateSchemaMakesMissedRunOptional(t *testing.T) {
	def := automationCreateDef(t)
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(def.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if slices.Contains(schema.Required, "missed_run") {
		t.Fatalf("automation_create required = %v, must not require missed_run", schema.Required)
	}
	if strings.Contains(def.Description, "There is no default") {
		t.Fatalf("automation_create description still instructs model there is no default: %q", def.Description)
	}
}

func TestAutomationCreatePrepareAndExecute(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 6, 55, 0, 0, time.UTC) // Nov 24 10:55 PM PST
	adapter := newAdapter(t, now, "America/Los_Angeles")
	exec := automationCreateDef(t).Exec()

	t.Run("simple_one_time_reminder_skips_autoreview_and_emits_reminder_card", func(t *testing.T) {
		call := invoke.Call{
			RunID:       "run-1",
			Revision:    1,
			OperationID: "op-once-1",
			Exec:        exec,
			TimeZone:    "America/Los_Angeles",
			Args: map[string]any{
				"title":       "Check supernino updates",
				"instruction": "Remind me to check out updates to supernino",
				"trigger": map[string]any{
					"kind": "once",
					"at":   "2026-11-25T20:00:00Z", // Nov 25 12:00 PM PST
				},
			},
		}
		prepared, err := adapter.Prepare(ctx, call)
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if !prepared.SkipAutoReview {
			t.Fatalf("expected SkipAutoReview=true for simple one-time Nori reminder")
		}
		if prepared.RequireConfirmation {
			t.Fatalf("expected RequireConfirmation=false for simple one-time Nori reminder")
		}
		if strings.Contains(prepared.Summary, "+0000 UTC") || strings.Contains(prepared.Summary, "[2026-") {
			t.Fatalf("summary leaked raw Go UTC slice: %q", prepared.Summary)
		}
		if !strings.Contains(prepared.Summary, "12:00 PM PST") {
			t.Fatalf("summary = %q, want local time 12:00 PM PST", prepared.Summary)
		}
		if strings.Contains(prepared.Summary, "connected accounts") || strings.Contains(prepared.Summary, "several runs") {
			t.Fatalf("summary = %q, must omit account scope and missed-run boilerplate", prepared.Summary)
		}

		res := adapter.Execute(ctx, call, prepared)
		if res.Outcome != "confirmed" || res.Err != nil {
			t.Fatalf("Execute = (%s, %v), want confirmed", res.Outcome, res.Err)
		}
		summary, _ := res.Content["summary"].(string)
		if strings.Contains(summary, "+0000 UTC") || !strings.Contains(summary, "12:00 PM PST") {
			t.Fatalf("saved summary = %q, want formatted local time", summary)
		}
		card, ok := res.Content["reminder_card"].(map[string]any)
		if !ok {
			t.Fatalf("expected reminder_card in Execute content, got %+v", res.Content)
		}
		if id, _ := card["id"].(string); !strings.HasPrefix(id, "auto_") {
			t.Fatalf("reminder_card.id = %q, want auto_*", id)
		}
		if card["name"] != "Check supernino updates" {
			t.Fatalf("reminder_card.name = %v", card["name"])
		}
		if card["scheduled_at"] != "2026-11-25T20:00:00Z" {
			t.Fatalf("reminder_card.scheduled_at = %v", card["scheduled_at"])
		}
		if fb, _ := card["fallback"].(string); !strings.HasPrefix(fb, "Reminder set · ") || !strings.Contains(fb, "12:00 PM PST") {
			t.Fatalf("reminder_card.fallback = %q", fb)
		}
	})

	t.Run("recurring_routine_requires_confirmation_with_plain_english_cadence", func(t *testing.T) {
		call := invoke.Call{
			RunID:       "run-2",
			Revision:    1,
			OperationID: "op-rec-1",
			Exec:        exec,
			TimeZone:    "America/Los_Angeles",
			Args: map[string]any{
				"title":          "Morning briefing",
				"instruction":    "Check my calendar and summarize today's schedule",
				"connection_ids": []any{"conn_gcal"},
				"trigger": map[string]any{
					"kind":        "recurring",
					"local_start": "2026-11-25T09:00",
					"timezone":    "America/Los_Angeles",
					"rrule":       "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR",
				},
			},
		}
		prepared, err := adapter.Prepare(ctx, call)
		if err != nil {
			t.Fatalf("Prepare recurring: %v", err)
		}
		if prepared.SkipAutoReview {
			t.Fatalf("recurring routine must not skip AutoReview/confirmation")
		}
		if !prepared.RequireConfirmation {
			t.Fatalf("recurring routine must set RequireConfirmation=true")
		}
		if prepared.PromptTitle != "Create this routine?" {
			t.Fatalf("PromptTitle = %q, want Create this routine?", prepared.PromptTitle)
		}
		if !strings.Contains(prepared.Summary, "Every weekday at 9:00 AM PST") {
			t.Fatalf("summary = %q, want plain-English weekday cadence in local time", prepared.Summary)
		}
		if !strings.Contains(prepared.Summary, "your connected accounts") {
			t.Fatalf("summary = %q, want second-person account scope", prepared.Summary)
		}
		if strings.Contains(prepared.Summary, "the user's") || strings.Contains(prepared.Summary, "+0000 UTC") {
			t.Fatalf("summary = %q, leaked internal copy or raw UTC", prepared.Summary)
		}
	})
}
