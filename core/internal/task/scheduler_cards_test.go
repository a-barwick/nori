package task_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/connectors/nori"
	"nori/internal/extensions"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/state"
	"nori/internal/task"
)

func attachNoriScheduler(t *testing.T, f *runnerFixture, zone string) *scheduler.Service {
	t.Helper()
	sched := &scheduler.Service{
		Store:       f.store,
		Tasks:       f.store,
		Permissions: f.perms,
		Timezone:    func(context.Context) string { return zone },
		Now:         f.clock.Now,
	}
	f.extSource.defs = append(f.extSource.defs, nori.Definitions...)
	nori.Register(f.adapters, nori.Adapter{Scheduler: sched, Unattended: "confirm_writes"})
	return sched
}

func TestStructuredRequestInputOpensCardWithoutWakingOuterLoop(t *testing.T) {
	ctx := context.Background()
	f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		if callNum == 1 {
			return toolCallResponse("c-ask", "request_input", map[string]any{
				"question": "Which calendar should I check?",
				"input":    "choice",
				"options": []any{
					map[string]any{"id": "work", "label": "Work"},
					map[string]any{"id": "personal", "label": "Personal"},
				},
			}, 0.02), nil
		}
		return toolCallResponse("c-done", "complete", map[string]any{
			"answer": "Checked your Work calendar.", "evidence": []any{}, "unmet": []any{},
		}, 0.02), nil
	})

	run := f.startTask(ctx, "run-structured-ask", "Check my calendar tomorrow")
	outcome, err := f.claimAndRun(ctx, run.ID, "worker-ask-1")
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("claimAndRun = (%s, %v), want (parked, nil)", outcome, err)
	}

	// Verify structured QuestionPayload on the pending Interaction card.
	pending, _, err := f.perms.Interactions(ctx, "pending", run.ID, "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending interactions = (%+v, %v), want 1", pending, err)
	}
	var qPayload struct {
		Input   string `json:"input"`
		Options []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"options"`
	}
	if err := json.Unmarshal(pending[0].Payload, &qPayload); err != nil {
		t.Fatalf("unmarshal question payload: %v", err)
	}
	if qPayload.Input != "choice" || len(qPayload.Options) != 2 || qPayload.Options[0].ID != "work" || qPayload.Options[1].Label != "Personal" {
		t.Fatalf("unexpected QuestionPayload: %+v", qPayload)
	}

	// Verify the outer conversation loop was NOT woken to emit a duplicate assistant chat bubble!
	_, turn, err := f.store.AcquireConversation(ctx, "outer-worker", "turn-check")
	if err != nil {
		t.Fatalf("AcquireConversation: %v", err)
	}
	if turn != nil {
		t.Fatalf("expected NO outer conversation wake for interactive question card, got turn with inputs %+v", turn.Inputs)
	}

	// Confirm the task itself still holds the question for SSE / panel / chat context.
	gotRun, err := f.store.GetRun(ctx, run.ID)
	if err != nil || gotRun.Question == nil || !gotRun.Question.Interactive {
		t.Fatalf("expected run.Question.Interactive=true, got %+v (err=%v)", gotRun.Question, err)
	}
}

func TestOneTimeNoriReminderExecutesImmediatelyAndEmitsReminderPart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 6, 55, 0, 0, time.UTC) // Nov 24 10:55 PM PST

	f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		if callNum == 1 {
			return toolCallResponse("c-rem", "automation_create", map[string]any{
				"title":       "Check supernino updates",
				"instruction": "Remind me to check out updates to supernino",
				"trigger": map[string]any{
					"kind": "once",
					"at":   "2026-11-25T20:00:00Z", // Nov 25 12:00 PM PST
				},
			}, 0.02), nil
		}
		return toolCallResponse("c-done", "complete", map[string]any{
			"answer": "Set a reminder for tomorrow at 12:00 PM PST to check supernino updates.", "evidence": []any{}, "unmet": []any{},
		}, 0.02), nil
	})
	f.clock.Advance(now.Sub(f.clock.Now()))
	sched := attachNoriScheduler(t, f, "America/Los_Angeles")

	// Even if AutoReviewer would conservatively return "ask", a simple one-time Nori-only reminder must bypass it!
	reviewer := &recordingAutoReviewer{verdict: permissions.AutoReviewDecision{Verdict: "ask", Reason: "should not be called"}}
	f.act.Executor.AutoReviewer = reviewer

	run := f.startTask(ctx, "run-once-reminder", "Remind me tomorrow at noon to check out updates to supernino")
	outcome, err := f.claimAndRun(ctx, run.ID, "worker-rem-1")
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("claimAndRun = (%s, %v), want (done, nil)", outcome, err)
	}
	if reviewer.calls.Load() != 0 {
		t.Fatalf("expected 0 AutoReviewer calls for simple one-time Nori reminder, got %d", reviewer.calls.Load())
	}
	pending, _, err := f.perms.Interactions(ctx, "pending", run.ID, "", 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("expected 0 pending interactions, got %+v (err=%v)", pending, err)
	}

	autos, err := sched.List(ctx, "enabled")
	if err != nil || len(autos) != 1 {
		t.Fatalf("expected 1 saved automation, got %+v (err=%v)", autos, err)
	}
	if autos[0].MissedRun != "latest" || autos[0].ExecutionWindowSeconds != scheduler.DefaultOnceExecutionWindowSeconds {
		t.Fatalf("unexpected automation defaults: missed_run=%q window=%d", autos[0].MissedRun, autos[0].ExecutionWindowSeconds)
	}

	// Publish the outer conversation turn for the terminal task event and verify PartReminder lands on the assistant message!
	ctrl, turn, err := f.store.AcquireConversation(ctx, "outer-worker", "turn-rem-confirm")
	if err != nil || turn == nil {
		t.Fatalf("AcquireConversation = (%+v, %v), want terminal task event turn", turn, err)
	}
	fence := state.ConversationFence{Owner: ctrl.Owner, Fence: ctrl.Fence, TurnID: turn.ID, Through: turn.Through, UserThrough: turn.UserThrough, Frozen: turn.Frozen}
	turn.Attempt = state.ConversationAttempt{ID: "att-rem", TurnID: turn.ID, Number: 1, Status: "completed"}
	if err := f.store.PublishConversation(ctx, fence, *turn, "You're all set."); err != nil {
		t.Fatalf("PublishConversation: %v", err)
	}
	msg, err := f.store.ConversationMessage(ctx, state.AssistantMessageID(turn.ID))
	if err != nil {
		t.Fatalf("ConversationMessage: %v", err)
	}
	var remPart *state.Part
	for i := range msg.Parts {
		if msg.Parts[i].Type == state.PartReminder {
			remPart = &msg.Parts[i]
			break
		}
	}
	if remPart == nil {
		t.Fatalf("expected PartReminder in assistant message parts, got %+v", msg.Parts)
	}
	if remPart.ID != autos[0].ID || remPart.Name != "Check supernino updates" || remPart.ScheduledAt != "2026-11-25T20:00:00Z" {
		t.Fatalf("unexpected PartReminder fields: %+v", *remPart)
	}
	if !strings.Contains(remPart.Text, "12:00 PM PST") || !strings.Contains(remPart.Fallback, "Reminder set ·") {
		t.Fatalf("unexpected PartReminder text/fallback: %+v", *remPart)
	}
}

func TestRecurringRoutineOpensPlainEnglishCardWithoutDuplicateBubbleAndRespectsAlwaysAllow(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 6, 55, 0, 0, time.UTC)

	f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		switch callNum {
		case 1, 2:
			return toolCallResponse("c-rec-"+string(rune('0'+callNum)), "automation_create", map[string]any{
				"title":       "Daily standup prep",
				"instruction": "Remind me to jot down standup notes",
				"trigger": map[string]any{
					"kind":        "recurring",
					"local_start": "2026-11-25T09:00",
					"timezone":    "America/Los_Angeles",
					"rrule":       "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR",
				},
			}, 0.02), nil
		default:
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Created the weekday routine.", "evidence": []any{}, "unmet": []any{},
			}, 0.02), nil
		}
	})
	f.clock.Advance(now.Sub(f.clock.Now()))
	sched := attachNoriScheduler(t, f, "America/Los_Angeles")

	run := f.startTask(ctx, "run-rec-routine", "Every weekday at 9am remind me to jot down standup notes")
	outcome, err := f.claimAndRun(ctx, run.ID, "worker-rec-1")
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("claimAndRun #1 = (%s, %v), want (parked, nil)", outcome, err)
	}

	// No outer loop wake (no duplicate chat bubble)!
	_, turn, err := f.store.AcquireConversation(ctx, "outer-worker", "turn-rec-check")
	if err != nil {
		t.Fatalf("AcquireConversation: %v", err)
	}
	if turn != nil {
		t.Fatalf("expected NO outer conversation wake when approval card opened, got %+v", turn.Inputs)
	}

	pending, _, err := f.perms.Interactions(ctx, "pending", run.ID, "", 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending interactions = (%+v, %v), want 1", pending, err)
	}
	card := pending[0]
	if card.Prompt != "Create this routine?" {
		t.Fatalf("card.Prompt = %q, want Create this routine?", card.Prompt)
	}
	var payload struct {
		Summary string `json:"summary"`
		Warning string `json:"warning"`
		Account struct {
			ConnectionID string `json:"connection_id"`
			Label        string `json:"label"`
		} `json:"account"`
	}
	if err := json.Unmarshal(card.Payload, &payload); err != nil {
		t.Fatalf("unmarshal approval payload: %v", err)
	}
	if payload.Warning != "" {
		t.Fatalf("payload.Warning = %q, want empty for internal Nori schedule", payload.Warning)
	}
	if payload.Account.Label != "" {
		t.Fatalf("payload.Account.Label = %q, want empty when no external account", payload.Account.Label)
	}
	if !strings.Contains(payload.Summary, "Every weekday at 9:00 AM PST") {
		t.Fatalf("payload.Summary = %q, want plain-English local cadence", payload.Summary)
	}
	if strings.Contains(payload.Summary, "the user's") || strings.Contains(payload.Summary, "+0000 UTC") {
		t.Fatalf("payload.Summary = %q, leaked internal phrasing or raw UTC", payload.Summary)
	}

	// Approve with RememberRule=true ("Always allow") -> completes and saves standing allow rule.
	_, ans, err := f.perms.Respond(ctx, card.ID, "req-approve-routine", card.Revision, permissions.Response{
		Kind:         "approve",
		ActionDigest: card.ActionDigest,
		RememberRule: true,
	})
	if err != nil || ans == nil {
		t.Fatalf("Respond approve: (%+v, %v)", ans, err)
	}
	if _, err := f.store.AcceptCommandBatch(ctx, []state.Command{{
		ID: ans.CommandID, RunID: ans.TaskID, Kind: state.CommandAnswer, QuestionID: ans.Question, Text: ans.Text,
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch answer: %v", err)
	}

	outcome2, err := f.claimAndRun(ctx, run.ID, "worker-rec-2")
	if err != nil || outcome2 != task.OutcomeDone {
		t.Fatalf("claimAndRun #2 = (%s, %v), want (done, nil)", outcome2, err)
	}
	autos, err := sched.List(ctx, "enabled")
	if err != nil || len(autos) != 1 {
		t.Fatalf("expected 1 saved recurring automation, got %+v (err=%v)", autos, err)
	}

	// Second recurring routine in a new task MUST honor the saved "Always allow" rule without re-prompting!
	run2 := f.startTask(ctx, "run-rec-routine-2", "Every Friday at 4pm remind me to send weekly notes")
	f.model.fn = func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		if callNum == 1 {
			return toolCallResponse("c-rec-second", "automation_create", map[string]any{
				"title":       "Weekly wrap-up",
				"instruction": "Remind me to send weekly notes",
				"trigger": map[string]any{
					"kind":        "recurring",
					"local_start": "2026-11-27T16:00",
					"timezone":    "America/Los_Angeles",
					"rrule":       "FREQ=WEEKLY;BYDAY=FR",
				},
			}, 0.02), nil
		}
		return toolCallResponse("c-done-2", "complete", map[string]any{
			"answer": "Created the Friday routine.", "evidence": []any{}, "unmet": []any{},
		}, 0.02), nil
	}
	f.model.calls = 0
	outcome3, err := f.claimAndRun(ctx, run2.ID, "worker-rec-3")
	if err != nil || outcome3 != task.OutcomeDone {
		t.Fatalf("claimAndRun #3 (Always allow rule) = (%s, %v), want (done, nil)", outcome3, err)
	}
	autos2, err := sched.List(ctx, "enabled")
	if err != nil || len(autos2) != 2 {
		t.Fatalf("expected 2 saved recurring automations after Always allow, got %+v (err=%v)", autos2, err)
	}
}

func TestAutoReviewerReceivesNowAndTimeZone(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 6, 55, 0, 0, time.UTC)
	calDef := sealExternalDef(t, "mock/tools/cal_create", "cal_create", extensions.EffectReversibleWrite)

	f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		if callNum == 1 {
			return toolCallResponse("c-cal", "cal_create", map[string]any{}, 0.05), nil
		}
		return toolCallResponse("c-done", "complete", map[string]any{
			"answer": "Booked.", "evidence": []any{}, "unmet": []any{},
		}, 0.05), nil
	})
	f.clock.Advance(now.Sub(f.clock.Now()))
	f.extSource.defs = []extensions.Definition{calDef}
	f.adapters.Register(extensions.AdapterNative, "mock", &stubExternalAdapter{effect: extensions.EffectReversibleWrite, summary: "Book event"})
	reviewer := &recordingAutoReviewer{verdict: permissions.AutoReviewDecision{Verdict: "allow"}}
	f.act.Executor.AutoReviewer = reviewer

	run := f.startTask(ctx, "run-tz-review", "Book lunch tomorrow at noon")
	outcome, err := f.claimAndRun(ctx, run.ID, "worker-tz")
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("claimAndRun = (%s, %v), want (done, nil)", outcome, err)
	}
	if reviewer.calls.Load() != 1 {
		t.Fatalf("AutoReviewer calls = %d, want 1", reviewer.calls.Load())
	}
	if !reviewer.lastReq.Now.Equal(now) {
		t.Fatalf("AutoReviewRequest.Now = %v, want %v", reviewer.lastReq.Now, now)
	}
}
