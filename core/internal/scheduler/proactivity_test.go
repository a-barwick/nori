package scheduler_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nori/internal/connections"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

type proactivityHarness struct {
	mem   *memstore.Backend
	db    *records.Database
	perms *permissions.Service
	sched *scheduler.Service
	now   time.Time
	prefs connections.Preferences
}

func boolPtr(v bool) *bool { return &v }

func newProactivityHarness(t *testing.T, start time.Time, zone string) *proactivityHarness {
	t.Helper()
	h := &proactivityHarness{
		now: start,
		prefs: connections.Preferences{
			Timezone: zone,
			QuietHours: &connections.QuietHours{
				Enabled: boolPtr(true),
				Start:   "21:00",
				End:     "07:30",
			},
			Proactivity: &connections.ProactivityPreferences{
				PushPolicy:              connections.PushPolicyUrgentAndApprovals,
				MorningBriefTime:        "08:00",
				MorningBriefEnabled:     boolPtr(true),
				DailyProactiveBudgetUSD: 1.0,
			},
		},
	}
	h.mem = memstore.New(memstore.WithClock(func() time.Time { return h.now }))
	t.Cleanup(func() { _ = h.mem.Close() })
	user := users.User{ID: "22222222-2222-4222-8222-222222222222", Email: "proactive@example.com"}
	h.db = records.NewShared(h.mem, user, 1.0)
	h.db.Now = func() time.Time { return h.now }
	t.Cleanup(func() { _ = h.db.Close() })

	h.perms = &permissions.Service{Store: h.db, Now: func() time.Time { return h.now }}
	h.sched = &scheduler.Service{
		Store:                  h.db,
		Tasks:                  h.db,
		Permissions:            h.perms,
		Now:                    func() time.Time { return h.now },
		Timezone:               func(context.Context) string { return h.prefs.Timezone },
		Preferences:            func(context.Context) (connections.Preferences, error) { return h.prefs, nil },
		AppendProactiveMessage: h.db.AppendProactiveMessage,
		EnqueueProactiveEvent:  h.db.EnqueueProactiveTaskEvent,
		ReadPosition:           h.db.ReadPosition,
		Accept: func(ctx context.Context, cmd state.Command) (state.Run, error) {
			receipts, err := h.db.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
			if err != nil {
				return state.Run{}, err
			}
			return receipts[0].Run, nil
		},
	}
	return h
}

func (h *proactivityHarness) completeRun(t *testing.T, runID, result string) {
	t.Helper()
	ctx := context.Background()
	run, err := h.db.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun(%s): %v", runID, err)
	}
	if err := h.db.Incorporate(ctx, state.IncorporateRequest{
		RunID:    runID,
		Revision: run.Revision,
		Position: state.TaskPosition(run),
	}); err != nil {
		t.Fatalf("Incorporate(%s): %v", runID, err)
	}
	if _, err := h.db.Publish(ctx, state.PublishRequest{
		RunID:    runID,
		Revision: run.Revision,
		Status:   state.RunCompleted,
		Text:     result,
	}); err != nil {
		t.Fatalf("Publish completed(%s): %v", runID, err)
	}
}

func (h *proactivityHarness) failRun(t *testing.T, runID, errMsg string) {
	t.Helper()
	ctx := context.Background()
	run, err := h.db.GetRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetRun(%s): %v", runID, err)
	}
	if _, err := h.db.Publish(ctx, state.PublishRequest{
		RunID:    runID,
		Revision: run.Revision,
		Status:   state.RunFailed,
		Text:     errMsg,
	}); err != nil {
		t.Fatalf("Publish failed(%s): %v", runID, err)
	}
}

// 1. Origin isolation (P0 #1)
func TestOriginIsolation(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC) // 08:00 PT (outside quiet hours)
	h := newProactivityHarness(t, start, "America/Los_Angeles")

	runAt := start.Add(time.Minute)
	receipt, err := h.sched.Save(ctx, "", "req-origin", 0, scheduler.Editable{
		Title:          "Background check",
		Instruction:    "Check status",
		Trigger:        scheduler.Trigger{Kind: "once", At: &runAt},
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.InAppDestination},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	var auto scheduler.Automation
	if err := json.Unmarshal(receipt.Resource, &auto); err != nil {
		t.Fatal(err)
	}

	h.now = runAt.Add(time.Second)
	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	occs, err := h.sched.Occurrences(ctx, auto.ID)
	if err != nil || len(occs) != 1 || len(occs[0].TaskIDs) != 1 {
		t.Fatalf("expected 1 running occurrence with task, got %+v (err=%v)", occs, err)
	}
	run, err := h.db.GetRun(ctx, occs[0].TaskIDs[0])
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Origin == nil || run.Origin.ConversationID != scheduler.AutomationConversationID {
		t.Fatalf("run.Origin = %+v, want ConversationID=%q", run.Origin, scheduler.AutomationConversationID)
	}

	// Publish task completion — must NOT wake or enqueue into the outer conversation loop.
	h.completeRun(t, run.ID, "All good")
	ctrl, _, err := h.db.ConversationStatus(ctx)
	if err != nil {
		t.Fatalf("ConversationStatus: %v", err)
	}
	if ctrl.Pending() || ctrl.Received != 0 {
		t.Fatalf("automation run leaked into conversation queue: %+v", ctrl)
	}
}

// 2. Typed NO_OP & finding_keys checkpointing + Failure Throttle (P0 #2)
func TestTypedNoOpAndFindingKeysCheckpoint(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	h := newProactivityHarness(t, start, "America/Los_Angeles")
	h.prefs.Proactivity.DailyProactiveBudgetUSD = 100.0 // allow frequent test watch

	receipt, err := h.sched.Save(ctx, "", "req-watch", 0, scheduler.Editable{
		Title:       "Invoice watch",
		Instruction: "Watch for new invoices",
		Trigger: scheduler.Trigger{
			Kind:               "watch",
			Source:             &scheduler.ResourceRef{ConnectionID: "conn_1", Kind: "gmail", ProviderID: "inbox"},
			PredicateRef:       "new_invoice",
			MinIntervalSeconds: 300,
		},
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.InAppDestination},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	var auto scheduler.Automation
	_ = json.Unmarshal(receipt.Resource, &auto)

	runMonitorOnce := func(result string) scheduler.Occurrence {
		h.now = h.now.Add(6 * time.Minute)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick spawn: %v", err)
		}
		occs, _ := h.sched.Occurrences(ctx, auto.ID)
		occ := occs[0]
		h.completeRun(t, occ.TaskIDs[0], result)
		h.now = h.now.Add(time.Second)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick settle: %v", err)
		}
		updated, err := h.sched.Occurrence(ctx, occ.ID)
		if err != nil {
			t.Fatalf("Occurrence: %v", err)
		}
		return updated
	}

	// Run 1: typed no_op -> settled with reason="no_change", 0 deliveries.
	occ1 := runMonitorOnce(`{"outcome":"no_op","finding_keys":[]}`)
	if occ1.Reason != "no_change" || len(occ1.DeliveryIDs) != 0 {
		t.Fatalf("occ1 = %+v, want reason=no_change and 0 deliveries", occ1)
	}

	// Run 2: typed update with finding_key "inv:101" -> delivered and checkpointed.
	occ2 := runMonitorOnce(`{"outcome":"update","finding_keys":["inv:101"],"summary":"Invoice 101 arrived"}`)
	if occ2.Reason != "" || len(occ2.DeliveryIDs) != 1 {
		t.Fatalf("occ2 = %+v, want 1 delivery", occ2)
	}

	// Run 3: different wording, SAME finding_key "inv:101" -> suppressed as no_change!
	occ3 := runMonitorOnce(`{"outcome":"update","finding_keys":["inv:101"],"summary":"Invoice #101 is still in inbox"}`)
	if occ3.Reason != "no_change" || len(occ3.DeliveryIDs) != 0 {
		t.Fatalf("occ3 = %+v, want suppressed reason=no_change via checkpointed finding_key", occ3)
	}

	// Run 4: new finding_key "inv:102" alongside "inv:101" -> delivered!
	occ4 := runMonitorOnce(`{"outcome":"alert","finding_keys":["inv:101","inv:102"],"summary":"Invoice 102 arrived"}`)
	if occ4.Reason != "" || len(occ4.DeliveryIDs) != 1 {
		t.Fatalf("occ4 = %+v, want 1 delivery for new finding_key", occ4)
	}
}

func TestFailureThrottleAndAutoPause(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	h := newProactivityHarness(t, start, "America/Los_Angeles")
	h.prefs.Proactivity.DailyProactiveBudgetUSD = 100.0

	receipt, err := h.sched.Save(ctx, "", "req-fail-throttle", 0, scheduler.Editable{
		Title:       "Flaky monitor",
		Instruction: "Run check",
		Trigger: scheduler.Trigger{
			Kind:               "watch",
			Source:             &scheduler.ResourceRef{ConnectionID: "conn_1", Kind: "gmail", ProviderID: "inbox"},
			PredicateRef:       "check",
			MinIntervalSeconds: 300,
		},
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.InAppDestination},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	var auto scheduler.Automation
	_ = json.Unmarshal(receipt.Resource, &auto)

	failOnce := func(errMsg string) scheduler.Occurrence {
		h.now = h.now.Add(6 * time.Minute)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick spawn: %v", err)
		}
		occs, _ := h.sched.Occurrences(ctx, auto.ID)
		occ := occs[0]
		h.failRun(t, occ.TaskIDs[0], errMsg)
		h.now = h.now.Add(time.Second)
		if _, err := h.sched.Tick(ctx); err != nil {
			t.Fatalf("Tick settle: %v", err)
		}
		updated, _ := h.sched.Occurrence(ctx, occ.ID)
		return updated
	}

	// Failure 1: reported.
	occ1 := failOnce("HTTP 400 bad request")
	if len(occ1.DeliveryIDs) != 1 {
		t.Fatalf("1st failure should report, got %+v", occ1)
	}

	// Failure 2 (intermediate): suppressed.
	occ2 := failOnce("HTTP 400 bad request")
	if len(occ2.DeliveryIDs) != 0 || occ2.Reason != "failure_throttled" {
		t.Fatalf("2nd failure should be throttled, got %+v", occ2)
	}

	// Failure 3 (threshold=3): reported + auto-paused.
	occ3 := failOnce("HTTP 400 bad request")
	if len(occ3.DeliveryIDs) != 1 {
		t.Fatalf("3rd failure should report pause, got %+v", occ3)
	}
	got, err := h.sched.Get(ctx, auto.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != scheduler.AutomationPaused || got.PausedReason != "consecutive_failures" {
		t.Fatalf("expected auto-paused with consecutive_failures, got status=%s reason=%s", got.Status, got.PausedReason)
	}
}

// 3. Chat Delivery & PartBrief + AppendProactiveMessage idempotency
func TestAppendProactiveMessageIdempotentAndZeroOuterTurn(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	h := newProactivityHarness(t, start, "America/Los_Angeles")

	msg := state.Message{
		Text: "Good morning",
		Parts: []state.Part{{
			Type:     state.PartBrief,
			Greeting: "Good morning",
			Meta:     "Wednesday, Nov 25",
			Items:    []state.BriefItem{{Title: "Approve wire", InteractionID: "int_1", ActionLabel: "Review"}},
			Fallback: "Good morning — Approve wire",
		}},
	}
	m1, err := h.db.AppendProactiveMessage(ctx, "dlv_brief_1", msg)
	if err != nil {
		t.Fatalf("AppendProactiveMessage 1: %v", err)
	}
	posAfterFirst, _ := h.db.EventPosition(ctx)

	// Second call with same delivery ID must be a no-op (idempotent).
	m2, err := h.db.AppendProactiveMessage(ctx, "dlv_brief_1", msg)
	if err != nil {
		t.Fatalf("AppendProactiveMessage 2: %v", err)
	}
	if m1.ID != m2.ID || m1.ID != records.ProactiveMessageID("dlv_brief_1") {
		t.Fatalf("expected deterministic ID, got %q vs %q", m1.ID, m2.ID)
	}
	posAfterSecond, _ := h.db.EventPosition(ctx)
	if posAfterSecond != posAfterFirst {
		t.Fatalf("idempotent replay emitted extra events: %d -> %d", posAfterFirst, posAfterSecond)
	}

	// Zero outer-loop turn: conversation queue must remain empty.
	ctrl, _, err := h.db.ConversationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ctrl.Pending() || ctrl.Received != 0 {
		t.Fatalf("AppendProactiveMessage must not enqueue outer-loop turn: %+v", ctrl)
	}

	// Verify message persisted in Chat history.
	msgs, _, err := h.db.ConversationMessages(ctx, "")
	if err != nil || len(msgs) != 1 || msgs[0].Parts[0].Type != state.PartBrief {
		t.Fatalf("expected 1 PartBrief message in Chat, got %+v (err=%v)", msgs, err)
	}
}

// 4. System Automation Timezones & Abandoned Accounts
func TestMorningBriefFollowUserTimezoneAndExecutionWindow(t *testing.T) {
	ctx := context.Background()
	// Start at 2026-11-25 06:00 PT = 14:00 UTC.
	start := time.Date(2026, 11, 25, 14, 0, 0, 0, time.UTC)
	h := newProactivityHarness(t, start, "America/Los_Angeles")

	brief, err := h.sched.EnsureMorningBrief(ctx, h.prefs)
	if err != nil {
		t.Fatalf("EnsureMorningBrief: %v", err)
	}
	if brief.ID != scheduler.MorningBriefAutomationID {
		t.Fatalf("ID = %q, want %q", brief.ID, scheduler.MorningBriefAutomationID)
	}
	if !brief.Trigger.FollowUserTimezone {
		t.Fatalf("expected FollowUserTimezone=true")
	}
	if brief.ExecutionWindowSeconds != scheduler.DefaultMorningBriefExecutionWindowSeconds {
		t.Fatalf("ExecutionWindowSeconds = %d, want %d", brief.ExecutionWindowSeconds, scheduler.DefaultMorningBriefExecutionWindowSeconds)
	}
	// 08:00 PT = 16:00 UTC.
	wantPT := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	if brief.NextAt == nil || !brief.NextAt.Equal(wantPT) {
		t.Fatalf("NextAt = %v, want %v (08:00 PT)", brief.NextAt, wantPT)
	}

	// User travels to New York (EST = UTC-5) without editing the automation.
	// 08:00 ET = 13:00 UTC (already passed at 14:00 UTC), so next run becomes tomorrow 08:00 ET = 13:00 UTC!
	h.prefs.Timezone = "America/New_York"
	next, err := h.sched.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick after timezone change: %v", err)
	}
	wantNYTomorrow := time.Date(2026, 11, 26, 13, 0, 0, 0, time.UTC)
	if next == nil || !next.Equal(wantNYTomorrow) {
		t.Fatalf("NextAt after TZ change = %v, want %v (08:00 ET next day)", next, wantNYTomorrow)
	}

	// Simulate missed 08:00 ET brief checked mid-afternoon (15:00 ET = 20:00 UTC, > 2h window).
	h.now = time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC)
	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatalf("Tick mid-afternoon: %v", err)
	}
	occs, _ := h.sched.Occurrences(ctx, scheduler.MorningBriefAutomationID)
	if len(occs) != 1 || occs[0].Status != scheduler.OccurrenceSkipped || occs[0].Reason != "missed_window" {
		t.Fatalf("expected missed 8 AM brief to be skipped with missed_window, got %+v", occs)
	}
}

func TestAbandonedAccountAutoPause(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	h := newProactivityHarness(t, start, "America/Los_Angeles")
	h.sched.InactivityPauseDays = 14
	h.sched.LastActivity = func(context.Context) (time.Time, error) {
		return start.Add(-15 * 24 * time.Hour), nil
	}

	brief, err := h.sched.EnsureMorningBrief(ctx, h.prefs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got, err := h.sched.Get(ctx, brief.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != scheduler.AutomationPaused || got.PausedReason != "inactive_account" {
		t.Fatalf("expected inactive_account pause, got status=%s reason=%s", got.Status, got.PausedReason)
	}
}

// 5. Quiet Hours (Midnight wrap, DST spring-forward/fall-back, Holding & Folding, Flush)
func TestInQuietHoursMidnightAndDST(t *testing.T) {
	q := connections.QuietHours{Enabled: boolPtr(true), Start: "21:00", End: "07:30"}

	// Midnight wrap in America/Los_Angeles (PST = UTC-8).
	// 22:30 PST on Nov 25 = 06:30 UTC on Nov 26 -> quiet ends 07:30 PST = 15:30 UTC on Nov 26.
	in, end := scheduler.InQuietHours(time.Date(2026, 11, 26, 6, 30, 0, 0, time.UTC), q, "America/Los_Angeles")
	wantEnd := time.Date(2026, 11, 26, 15, 30, 0, 0, time.UTC)
	if !in || !end.Equal(wantEnd) {
		t.Fatalf("midnight wrap evening: in=%v end=%v, want true, %v", in, end, wantEnd)
	}

	// Early morning 03:00 PST on Nov 26 = 11:00 UTC -> quiet ends 07:30 PST = 15:30 UTC.
	in, end = scheduler.InQuietHours(time.Date(2026, 11, 26, 11, 0, 0, 0, time.UTC), q, "America/Los_Angeles")
	if !in || !end.Equal(wantEnd) {
		t.Fatalf("midnight wrap morning: in=%v end=%v, want true, %v", in, end, wantEnd)
	}

	// Outside quiet hours: 08:00 PST = 16:00 UTC.
	if in, _ := scheduler.InQuietHours(time.Date(2026, 11, 26, 16, 0, 0, 0, time.UTC), q, "America/Los_Angeles"); in {
		t.Fatalf("expected 08:00 PST to be outside quiet hours")
	}

	// DST spring-forward in America/New_York: 2026-03-08 02:00 EST -> 03:00 EDT.
	// 23:00 EST on Mar 7 = 2026-03-08 04:00 UTC -> quiet ends 07:30 EDT = 2026-03-08 11:30 UTC.
	in, end = scheduler.InQuietHours(time.Date(2026, 3, 8, 4, 0, 0, 0, time.UTC), q, "America/New_York")
	wantSpring := time.Date(2026, 3, 8, 11, 30, 0, 0, time.UTC)
	if !in || !end.Equal(wantSpring) {
		t.Fatalf("DST spring-forward: in=%v end=%v, want true, %v", in, end, wantSpring)
	}

	// DST spring-forward nonexistent End wall time (02:30 -> resolves to 03:00 EDT = 07:00 UTC).
	qGap := connections.QuietHours{Enabled: boolPtr(true), Start: "23:00", End: "02:30"}
	in, end = scheduler.InQuietHours(time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), qGap, "America/New_York")
	wantGapEnd := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)
	if !in || !end.Equal(wantGapEnd) {
		t.Fatalf("DST spring-forward gap end: in=%v end=%v, want true, %v", in, end, wantGapEnd)
	}

	// DST fall-back in America/New_York: 2026-11-01 02:00 EDT -> 01:00 EST.
	// 01:30 EDT (first, 05:30 UTC) and 01:30 EST (second, 06:30 UTC) -> both end at 07:30 EST = 12:30 UTC.
	wantFall := time.Date(2026, 11, 1, 12, 30, 0, 0, time.UTC)
	for _, ts := range []time.Time{
		time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC),
	} {
		in, end = scheduler.InQuietHours(ts, q, "America/New_York")
		if !in || !end.Equal(wantFall) {
			t.Fatalf("DST fall-back %v: in=%v end=%v, want true, %v", ts, in, end, wantFall)
		}
	}
}

func TestQuietHoursHoldAndMorningBriefFoldAndAntiNag(t *testing.T) {
	ctx := context.Background()
	// 22:00 PT on Nov 25 = 06:00 UTC on Nov 26 (inside quiet hours 21:00–07:30 PT).
	start := time.Date(2026, 11, 26, 6, 0, 0, 0, time.UTC)
	h := newProactivityHarness(t, start, "America/Los_Angeles")
	h.prefs.Proactivity.DailyProactiveBudgetUSD = 10.0

	if _, err := h.sched.EnsureMorningBrief(ctx, h.prefs); err != nil {
		t.Fatal(err)
	}

	// Schedule a one-time alert during quiet hours.
	at := start.Add(time.Minute)
	receipt, err := h.sched.Save(ctx, "", "req-overnight", 0, scheduler.Editable{
		Title:          "Overnight flight alert",
		Instruction:    "Check flight",
		Trigger:        scheduler.Trigger{Kind: "once", At: &at},
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.ChatDestination},
	})
	if err != nil {
		t.Fatal(err)
	}
	var overnight scheduler.Automation
	_ = json.Unmarshal(receipt.Resource, &overnight)

	h.now = at.Add(time.Second)
	_, _ = h.sched.Tick(ctx)
	occs, _ := h.sched.Occurrences(ctx, overnight.ID)
	h.completeRun(t, occs[0].TaskIDs[0], `{"outcome":"update","finding_keys":["flight:ua123"],"summary":"Flight UA123 delayed 30m","interaction_id":"int_rebook"}`)
	h.now = h.now.Add(time.Second)
	_, _ = h.sched.Tick(ctx)

	occAfter, _ := h.sched.Occurrence(ctx, occs[0].ID)
	if len(occAfter.Deliveries) != 1 || occAfter.Deliveries[0].State != scheduler.DeliveryStateHeld {
		t.Fatalf("expected held delivery during quiet hours, got %+v", occAfter.Deliveries)
	}
	heldID := occAfter.Deliveries[0].ID
	wantQuietEnd := time.Date(2026, 11, 26, 15, 30, 0, 0, time.UTC) // 07:30 PT
	if occAfter.Deliveries[0].ScheduledFor == nil || !occAfter.Deliveries[0].ScheduledFor.Equal(wantQuietEnd) {
		t.Fatalf("ScheduledFor = %v, want %v", occAfter.Deliveries[0].ScheduledFor, wantQuietEnd)
	}

	// At quiet_end (07:30 PT), Morning Brief is enabled and coming at 08:00 PT -> still held for folding!
	h.now = wantQuietEnd.Add(time.Minute)
	_, _ = h.sched.Tick(ctx)
	dAtQuietEnd, _ := h.sched.Delivery(ctx, heldID)
	if dAtQuietEnd.State != scheduler.DeliveryStateHeld {
		t.Fatalf("expected delivery to remain held until 8 AM brief, got %s", dAtQuietEnd.State)
	}

	// At 08:00 PT (16:00 UTC), Morning Brief runs and folds the held delivery.
	h.now = time.Date(2026, 11, 26, 16, 0, 1, 0, time.UTC)
	_, _ = h.sched.Tick(ctx)
	briefOccs, _ := h.sched.Occurrences(ctx, scheduler.MorningBriefAutomationID)
	if len(briefOccs) != 1 || len(briefOccs[0].TaskIDs) != 1 {
		t.Fatalf("expected Morning Brief occurrence running, got %+v", briefOccs)
	}
	h.completeRun(t, briefOccs[0].TaskIDs[0], `{"outcome":"update","greeting":"Good morning","items":[{"title":"Standup at 9 AM"}]}`)
	h.now = h.now.Add(time.Second)
	_, _ = h.sched.Tick(ctx)

	// Held delivery is now marked folded with receipt = brief occurrence ID.
	dFolded, _ := h.sched.Delivery(ctx, heldID)
	if dFolded.State != scheduler.DeliveryStateFolded || dFolded.Receipt != briefOccs[0].ID {
		t.Fatalf("expected folded delivery with receipt=%s, got state=%s receipt=%s", briefOccs[0].ID, dFolded.State, dFolded.Receipt)
	}

	// Verify PartBrief was appended to Chat with both brief item and folded item (storing interaction_id + Fallback).
	msgs, _, err := h.db.ConversationMessages(ctx, "")
	if err != nil || len(msgs) != 1 {
		t.Fatalf("expected 1 brief message in Chat, got %d (err=%v)", len(msgs), err)
	}
	part := msgs[0].Parts[0]
	if part.Type != state.PartBrief || len(part.Items) != 2 || part.Items[1].InteractionID != "int_rebook" || !strings.Contains(part.Fallback, "Overnight flight alert") {
		t.Fatalf("unexpected PartBrief: %+v", part)
	}

	// Deliverable 6 (Anti-Nagging): Next night, the SAME unengaged finding_key "flight:ua123"
	// arrives again. Since it already folded into 1 Morning Brief without engagement, it must drop!
	h.now = time.Date(2026, 11, 27, 6, 0, 0, 0, time.UTC) // 22:00 PT next night
	at2 := h.now.Add(time.Minute)
	receipt2, err := h.sched.Save(ctx, "", "req-overnight-2", 0, scheduler.Editable{
		Title:          "Overnight flight alert repeat",
		Instruction:    "Check flight again",
		Trigger:        scheduler.Trigger{Kind: "once", At: &at2},
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.ChatDestination},
	})
	if err != nil {
		t.Fatal(err)
	}
	var overnight2 scheduler.Automation
	_ = json.Unmarshal(receipt2.Resource, &overnight2)

	h.now = at2.Add(time.Second)
	_, _ = h.sched.Tick(ctx)
	occs2, _ := h.sched.Occurrences(ctx, overnight2.ID)
	h.completeRun(t, occs2[0].TaskIDs[0], `{"outcome":"update","finding_keys":["flight:ua123"],"summary":"Flight UA123 still delayed"}`)
	h.now = h.now.Add(time.Second)
	_, _ = h.sched.Tick(ctx)

	occ2After, _ := h.sched.Occurrence(ctx, occs2[0].ID)
	if occ2After.Reason != "anti_nag" || len(occ2After.DeliveryIDs) != 0 {
		t.Fatalf("expected unengaged repeat finding_key to drop with anti_nag, got %+v", occ2After)
	}
}

func TestQuietHoursFlushWhenBriefDisabled(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 26, 6, 0, 0, 0, time.UTC) // 22:00 PT
	h := newProactivityHarness(t, start, "America/Los_Angeles")
	h.prefs.Proactivity.MorningBriefEnabled = boolPtr(false)

	at := start.Add(time.Minute)
	receipt, err := h.sched.Save(ctx, "", "req-flush", 0, scheduler.Editable{
		Title:          "Overnight note",
		Instruction:    "Send note",
		Trigger:        scheduler.Trigger{Kind: "once", At: &at},
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.ChatDestination},
	})
	if err != nil {
		t.Fatal(err)
	}
	var auto scheduler.Automation
	_ = json.Unmarshal(receipt.Resource, &auto)

	h.now = at.Add(time.Second)
	_, _ = h.sched.Tick(ctx)
	occs, _ := h.sched.Occurrences(ctx, auto.ID)
	h.completeRun(t, occs[0].TaskIDs[0], "Held overnight")
	h.now = h.now.Add(time.Second)
	_, _ = h.sched.Tick(ctx)

	occAfter, _ := h.sched.Occurrence(ctx, occs[0].ID)
	heldID := occAfter.Deliveries[0].ID
	if occAfter.Deliveries[0].State != scheduler.DeliveryStateHeld {
		t.Fatalf("expected held state, got %s", occAfter.Deliveries[0].State)
	}

	// Advance to quiet_end (07:30 PT = 15:30 UTC): since brief is disabled, flush immediately!
	h.now = time.Date(2026, 11, 26, 15, 30, 1, 0, time.UTC)
	_, _ = h.sched.Tick(ctx)

	flushed, _ := h.sched.Delivery(ctx, heldID)
	if flushed.State != scheduler.DeliveryStateDelivered {
		t.Fatalf("expected held delivery to flush to delivered at quiet_end, got %s", flushed.State)
	}
	msgs, _, _ := h.db.ConversationMessages(ctx, "")
	if len(msgs) != 1 || !strings.Contains(msgs[0].Text, "Held overnight") {
		t.Fatalf("expected flushed message in Chat, got %+v", msgs)
	}
}

// 7. Cadence & Cost Governor (Create/Change/Resume needs_confirmation + Runtime advance budget_exhausted)
func TestGovernorOverBudgetNeedsRealUserInteractionAndRuntimeCap(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	h := newProactivityHarness(t, start, "America/Los_Angeles")
	h.prefs.Proactivity.DailyProactiveBudgetUSD = 1.0

	expensiveWatch := scheduler.Editable{
		Title:       "High-freq watch",
		Instruction: "Poll every 5 min",
		Trigger: scheduler.Trigger{
			Kind:               "watch",
			Source:             &scheduler.ResourceRef{ConnectionID: "conn_1", Kind: "gmail", ProviderID: "inbox"},
			PredicateRef:       "urgent",
			MinIntervalSeconds: 300, // 288 runs/day * $0.05 = $14.40/day > $1.00/day
		},
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.ChatDestination},
	}

	// Step 1: Create returns needs_confirmation backed by a real permissions.Interaction.
	rec1, err := h.sched.Save(ctx, "", "req-expensive-1", 0, expensiveWatch)
	if err != nil {
		t.Fatalf("Save over-budget: %v", err)
	}
	if rec1.Status != scheduler.StatusNeedsConfirmation {
		t.Fatalf("Status = %q, want %q", rec1.Status, scheduler.StatusNeedsConfirmation)
	}
	var conf scheduler.BudgetConfirmation
	if err := json.Unmarshal(rec1.Resource, &conf); err != nil {
		t.Fatalf("unmarshal confirmation: %v", err)
	}
	if conf.InteractionID == "" || conf.ActionDigest == "" {
		t.Fatalf("expected InteractionID and ActionDigest, got %+v", conf)
	}
	inter, err := h.perms.Interaction(ctx, conf.InteractionID)
	if err != nil || inter.Status != "pending" || inter.Kind != "approval" {
		t.Fatalf("expected real pending approval interaction, got %+v (err=%v)", inter, err)
	}

	// Step 2: Retrying without user approval still returns needs_confirmation (no self-confirmation).
	expensiveWatch.ConfirmationInteractionID = conf.InteractionID
	rec2, err := h.sched.Save(ctx, "", "req-expensive-2", 0, expensiveWatch)
	if err != nil || rec2.Status != scheduler.StatusNeedsConfirmation {
		t.Fatalf("unapproved retry must stay needs_confirmation, got %+v (err=%v)", rec2, err)
	}

	// Step 3: Real user approves the interaction via permissions.Service.Respond.
	if _, _, err := h.perms.Respond(ctx, inter.ID, "resp-approve-budget", inter.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: conf.ActionDigest,
	}); err != nil {
		t.Fatalf("Respond approve: %v", err)
	}

	// Step 4: Now Save succeeds!
	rec3, err := h.sched.Save(ctx, "", "req-expensive-1", 0, expensiveWatch)
	if err != nil || rec3.Status != "applied" {
		t.Fatalf("approved Save should succeed, got %+v (err=%v)", rec3, err)
	}
	var saved scheduler.Automation
	_ = json.Unmarshal(rec3.Resource, &saved)

	// Step 5: Runtime check in advance() compares actual daily spend (Task + OuterLoop + ComputerWake) against cap.
	h.sched.DailyProactiveSpend = func(context.Context, time.Time) (scheduler.ProactiveSpend, error) {
		return scheduler.ProactiveSpend{
			TaskLLMUSD:      0.60,
			OuterLoopUSD:    0.25,
			ComputerWakeUSD: 0.20, // total $1.05 >= $1.00 cap
		}, nil
	}
	h.now = h.now.Add(6 * time.Minute)
	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatalf("Tick runtime budget: %v", err)
	}
	occs, _ := h.sched.Occurrences(ctx, saved.ID)
	if len(occs) != 1 || occs[0].Status != scheduler.OccurrenceSkipped || occs[0].Reason != "budget_exhausted" {
		t.Fatalf("expected runtime budget_exhausted skip, got %+v", occs)
	}

	// Step 6: Pause then Resume also enforces governor!
	latest, err := h.sched.Get(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Get before pause: %v", err)
	}
	if _, err := h.sched.Act(ctx, latest.ID, "req-pause", latest.Revision, scheduler.Action{Action: "pause"}); err != nil {
		t.Fatalf("Act pause: %v", err)
	}
	paused, _ := h.sched.Get(ctx, saved.ID)
	resumeRec, err := h.sched.Act(ctx, saved.ID, "req-resume", paused.Revision, scheduler.Action{Action: "resume"})
	if err != nil {
		t.Fatalf("Act resume: %v", err)
	}
	if resumeRec.Status != scheduler.StatusNeedsConfirmation {
		t.Fatalf("over-budget Resume must return needs_confirmation, got %+v", resumeRec)
	}
}

func TestQuietHoursFlushWhenBriefFails(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 26, 6, 0, 0, 0, time.UTC) // 22:00 PT
	h := newProactivityHarness(t, start, "America/Los_Angeles")
	h.prefs.Proactivity.DailyProactiveBudgetUSD = 10.0

	if _, err := h.sched.EnsureMorningBrief(ctx, h.prefs); err != nil {
		t.Fatal(err)
	}
	at := start.Add(time.Minute)
	rec, err := h.sched.Save(ctx, "", "req-brief-fail-held", 0, scheduler.Editable{
		Title:          "Overnight alert",
		Instruction:    "Check",
		Trigger:        scheduler.Trigger{Kind: "once", At: &at},
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.ChatDestination},
	})
	if err != nil {
		t.Fatal(err)
	}
	var auto scheduler.Automation
	_ = json.Unmarshal(rec.Resource, &auto)

	h.now = at.Add(time.Second)
	_, _ = h.sched.Tick(ctx)
	occs, _ := h.sched.Occurrences(ctx, auto.ID)
	h.completeRun(t, occs[0].TaskIDs[0], "Held for brief")
	h.now = h.now.Add(time.Second)
	_, _ = h.sched.Tick(ctx)

	occAfter, _ := h.sched.Occurrence(ctx, occs[0].ID)
	heldID := occAfter.Deliveries[0].ID
	if occAfter.Deliveries[0].State != scheduler.DeliveryStateHeld {
		t.Fatalf("expected held state, got %s", occAfter.Deliveries[0].State)
	}

	// Morning Brief runs at 08:00 PT (16:00 UTC) and FAILS -> held delivery flushes immediately!
	h.now = time.Date(2026, 11, 26, 16, 0, 1, 0, time.UTC)
	_, _ = h.sched.Tick(ctx)
	briefOccs, _ := h.sched.Occurrences(ctx, scheduler.MorningBriefAutomationID)
	h.failRun(t, briefOccs[0].TaskIDs[0], "HTTP 400 upstream error")
	h.now = h.now.Add(time.Second)
	_, _ = h.sched.Tick(ctx)

	flushed, _ := h.sched.Delivery(ctx, heldID)
	if flushed.State != scheduler.DeliveryStateDelivered {
		t.Fatalf("expected held delivery flushed to delivered on brief failure, got %s", flushed.State)
	}
}

func TestAntiNaggingEngagementPreventsDropAndWatchAlertEnqueuesEvent(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	h := newProactivityHarness(t, start, "America/Los_Angeles")
	h.prefs.Proactivity.DailyProactiveBudgetUSD = 10.0

	// Record user engagement via LastUserTurnAt after surfacing.
	var lastTurn time.Time
	h.sched.LastUserTurnAt = func(context.Context) (time.Time, error) { return lastTurn, nil }

	at := start.Add(time.Minute)
	rec, err := h.sched.Save(ctx, "", "req-alert-event", 0, scheduler.Editable{
		Title:          "Urgent server alert",
		Instruction:    "Check server",
		Trigger:        scheduler.Trigger{Kind: "once", At: &at},
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.ChatDestination},
	})
	if err != nil {
		t.Fatal(err)
	}
	var auto scheduler.Automation
	_ = json.Unmarshal(rec.Resource, &auto)

	h.now = at.Add(time.Second)
	_, _ = h.sched.Tick(ctx)
	occs, _ := h.sched.Occurrences(ctx, auto.ID)
	h.completeRun(t, occs[0].TaskIDs[0], `{"outcome":"alert","finding_keys":["srv:down"],"summary":"Primary DB unreachable"}`)
	h.now = h.now.Add(time.Second)
	_, _ = h.sched.Tick(ctx)

	// Surfaced alert on dest_chat with EnqueueProactiveEvent wired enqueues into conversation queue.
	ctrl, _, err := h.db.ConversationStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ctrl.Pending() || ctrl.Received != 1 {
		t.Fatalf("expected surfaced watch alert to enqueue 1 task event, got %+v", ctrl)
	}

	// User engages after surfacing -> explicit engagement recorded.
	lastTurn = h.now.Add(time.Minute)
	if err := h.sched.RecordFindingEngagement(ctx, "srv:down", "user_turn"); err != nil {
		t.Fatal(err)
	}

	// Re-surfacing same finding_key is NOT dropped because user engaged.
	at2 := h.now.Add(2 * time.Minute)
	rec2, _ := h.sched.Save(ctx, "", "req-alert-event-2", 0, scheduler.Editable{
		Title:          "Urgent server alert 2",
		Instruction:    "Check server",
		Trigger:        scheduler.Trigger{Kind: "once", At: &at2},
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.ChatDestination},
	})
	var auto2 scheduler.Automation
	_ = json.Unmarshal(rec2.Resource, &auto2)
	h.now = at2.Add(time.Second)
	_, _ = h.sched.Tick(ctx)
	occs2, _ := h.sched.Occurrences(ctx, auto2.ID)
	h.completeRun(t, occs2[0].TaskIDs[0], `{"outcome":"alert","finding_keys":["srv:down"],"summary":"Primary DB still flapping"}`)
	h.now = h.now.Add(time.Second)
	_, _ = h.sched.Tick(ctx)

	occ2After, _ := h.sched.Occurrence(ctx, occs2[0].ID)
	if occ2After.Reason == "anti_nag" || len(occ2After.DeliveryIDs) != 1 {
		t.Fatalf("engaged finding_key should not be dropped by anti_nag, got %+v", occ2After)
	}
}

func TestGovernorSkipsExhaustedAndOtherDayOnceRemindersAndEmitsValidApprovalPayload(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC) // 08:00 PT
	h := newProactivityHarness(t, start, "America/Los_Angeles")
	h.prefs.Proactivity.DailyProactiveBudgetUSD = 1.0

	// Enable Morning Brief ($0.15/day).
	if _, err := h.sched.EnsureMorningBrief(ctx, h.prefs); err != nil {
		t.Fatalf("EnsureMorningBrief: %v", err)
	}

	// Create and fire two historical one-time reminders with $1.00 budget each
	// (simulating legacy reminders already stored with status=enabled, Created=1, NextAt=nil).
	h.prefs.Proactivity.DailyProactiveBudgetUSD = 10.0
	for i, offset := range []time.Duration{time.Minute, 2 * time.Minute} {
		at := start.Add(offset)
		rec, err := h.sched.Save(ctx, "", "req-past-once-"+string(rune('1'+i)), 0, scheduler.Editable{
			Title:          "Old reminder",
			Instruction:    "Remind me",
			Trigger:        scheduler.Trigger{Kind: "once", At: &at},
			BudgetUSD:      1.0,
			MaxActiveRuns:  1,
			DestinationIDs: []string{scheduler.ChatDestination},
		})
		if err != nil || rec.Status != "applied" {
			t.Fatalf("save old reminder %d: status=%q err=%v", i, rec.Status, err)
		}
	}
	// Fire both past reminders so they remain status=enabled with NextAt=nil and Created=1.
	h.now = start.Add(5 * time.Minute)
	if _, err := h.sched.Tick(ctx); err != nil {
		t.Fatalf("Tick past reminders: %v", err)
	}

	// Also schedule a future one-time reminder on a DIFFERENT day (2 days out).
	twoDaysOut := start.Add(48 * time.Hour)
	if rec, err := h.sched.Save(ctx, "", "req-other-day-once", 0, scheduler.Editable{
		Title:          "Later reminder",
		Instruction:    "Remind me later",
		Trigger:        scheduler.Trigger{Kind: "once", At: &twoDaysOut},
		BudgetUSD:      0.50,
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.ChatDestination},
	}); err != nil || rec.Status != "applied" {
		t.Fatalf("save other-day reminder: status=%q err=%v", rec.Status, err)
	}

	// Restore default $1.00/day cap. Creating a one-time reminder for tomorrow at 10am
	// must NOT count the two already-fired $1.00 reminders or the different-day reminder.
	h.prefs.Proactivity.DailyProactiveBudgetUSD = 1.0
	tomorrow10am := time.Date(2026, 11, 26, 18, 0, 0, 0, time.UTC) // 10:00 AM PST
	rec, err := h.sched.Save(ctx, "", "req-tomorrow-10am", 0, scheduler.Editable{
		Title:          "Check personal computer billing error and API credits",
		Instruction:    "Remind me to check on that",
		Trigger:        scheduler.Trigger{Kind: "once", At: &tomorrow10am},
		BudgetUSD:      0.15,
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.ChatDestination},
	})
	if err != nil {
		t.Fatalf("Save tomorrow reminder: %v", err)
	}
	if rec.Status != "applied" {
		t.Fatalf("tomorrow one-time reminder status = %q, want applied (resource=%s)", rec.Status, string(rec.Resource))
	}

	// Verify over-budget watch emits an OpenAPI-compliant ApprovalPayload with task context and expiry.
	rev := int64(3)
	taskCtx := scheduler.WithBudgetInteractionContext(ctx, scheduler.BudgetInteractionContext{
		TaskID:              "task-watch-1",
		InstructionRevision: &rev,
		ConversationID:      "assistant",
	})
	expensive := scheduler.Editable{
		Title:       "Every 5 min inbox watch",
		Instruction: "Watch inbox",
		Trigger: scheduler.Trigger{
			Kind:               "watch",
			Source:             &scheduler.ResourceRef{ConnectionID: "conn_1", Kind: "gmail", ProviderID: "inbox"},
			PredicateRef:       "urgent",
			MinIntervalSeconds: 300,
		},
		BudgetUSD:      0.05,
		MaxActiveRuns:  1,
		DestinationIDs: []string{scheduler.ChatDestination},
	}
	confRec, err := h.sched.Save(taskCtx, "", "req-watch-payload-1", 0, expensive)
	if err != nil || confRec.Status != scheduler.StatusNeedsConfirmation {
		t.Fatalf("over-budget Save = (%q, %v), want needs_confirmation", confRec.Status, err)
	}
	var conf scheduler.BudgetConfirmation
	if err := json.Unmarshal(confRec.Resource, &conf); err != nil {
		t.Fatalf("unmarshal conf: %v", err)
	}
	inter, err := h.perms.Interaction(ctx, conf.InteractionID)
	if err != nil {
		t.Fatalf("Interaction: %v", err)
	}
	if inter.Kind != "approval" || inter.Prompt != "Approve proactive spend?" {
		t.Fatalf("unexpected interaction header: kind=%q prompt=%q", inter.Kind, inter.Prompt)
	}
	if inter.TaskID != "task-watch-1" || inter.InstructionRevision == nil || *inter.InstructionRevision != 3 {
		t.Fatalf("expected task metadata on budget interaction, got task=%q rev=%v", inter.TaskID, inter.InstructionRevision)
	}
	if inter.ExpiresAt == nil || !inter.ExpiresAt.After(h.now) {
		t.Fatalf("expected future expires_at on budget interaction, got %v", inter.ExpiresAt)
	}
	var payload map[string]any
	if err := json.Unmarshal(inter.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	for _, key := range []string{"summary", "tool_id", "warning", "effect", "account", "resources", "approval_once"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("budget ApprovalPayload missing required OpenAPI field %q: %v", key, payload)
		}
	}
	if payload["tool_id"] != scheduler.BudgetApprovalToolID {
		t.Fatalf("tool_id = %v, want %q", payload["tool_id"], scheduler.BudgetApprovalToolID)
	}

	// Approving the interaction allows a retry with a DIFFERENT requestID (as happens on task resume).
	if _, _, err := h.perms.Respond(ctx, inter.ID, "resp-approve-watch", inter.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: inter.ActionDigest,
	}); err != nil {
		t.Fatalf("Respond approve: %v", err)
	}
	savedRec, err := h.sched.Save(taskCtx, "", "req-watch-payload-retry-new-id", 0, expensive)
	if err != nil || savedRec.Status != "applied" {
		t.Fatalf("approved retry with new requestID = (%q, %v), want applied", savedRec.Status, err)
	}
}
