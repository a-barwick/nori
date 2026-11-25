package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/transcript"
	"nori/internal/users"
)

type fakeClock struct {
	mu  sync.Mutex
	cur time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{cur: start.UTC()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cur
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = c.cur.Add(d)
}

type fakeReplyModel struct {
	mu  sync.Mutex
	seq int
	now func() time.Time
	err error
}

func (*fakeReplyModel) Name() string { return "fake-reply-model" }

func (m *fakeReplyModel) Respond(_ context.Context, req ReplyRequest) (Reply, error) {
	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("usage-reply-%d", m.seq)
	err := m.err
	m.mu.Unlock()
	if err != nil {
		return Reply{Usage: state.Usage{ID: id, Model: "fake-reply-model"}}, err
	}
	text := "Noted: " + req.TelemetryInput
	msg, _ := json.Marshal(map[string]any{"role": "assistant", "content": text})
	return Reply{
		Usage: state.Usage{
			ID:    id,
			Model: "fake-reply-model",
		},
		Message: msg,
		Text:    text,
	}, nil
}

type recordedSummaryCall struct {
	Payload map[string]any
	Tools   []transcript.Tool
}

type fakeSummaryModel struct {
	mu      sync.Mutex
	calls   []recordedSummaryCall
	handler func(ctx context.Context, callNum int, payload map[string]any, req agent.Request) (agent.Response, error)
}

func (m *fakeSummaryModel) Name() string { return "fake-summary-model" }

func (m *fakeSummaryModel) Respond(ctx context.Context, req agent.Request) (agent.Response, error) {
	var payload map[string]any
	if len(req.Messages) > 0 && len(req.Messages[0].Parts) > 0 {
		_ = json.Unmarshal([]byte(req.Messages[0].Parts[0].Text), &payload)
	}
	m.mu.Lock()
	m.calls = append(m.calls, recordedSummaryCall{Payload: payload, Tools: req.Tools})
	callNum := len(m.calls)
	h := m.handler
	m.mu.Unlock()
	if h != nil {
		return h(ctx, callNum, payload, req)
	}
	return toolResponse(flushAndCompactToolName, map[string]any{
		"core_upserts":      []any{},
		"daily_log_entries": []any{},
		"summary_items":     []any{},
		"gist":              "",
	}), nil
}

func (m *fakeSummaryModel) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *fakeSummaryModel) Calls() []recordedSummaryCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]recordedSummaryCall, len(m.calls))
	copy(out, m.calls)
	return out
}

func toolResponse(toolName string, args map[string]any) agent.Response {
	call := transcript.ToolCall{
		ID:   "call-" + toolName,
		Name: toolName,
		Args: args,
	}
	return agent.Response{
		Message: &transcript.Message{
			Role:  transcript.RoleModel,
			Parts: []transcript.Part{{ToolCall: &call}},
		},
		Calls: []transcript.ToolCall{call},
	}
}

type idleTestHarness struct {
	t      *testing.T
	clk    *fakeClock
	store  *records.Database
	runner *Runner
	model  *fakeReplyModel
	sum    *fakeSummaryModel
	seq    int
}

func newIdleTestHarness(t *testing.T, start time.Time) *idleTestHarness {
	t.Helper()
	clk := newFakeClock(start)
	backend := memstore.New(memstore.WithClock(clk.Now))
	u := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "alice@example.com",
		DisplayName: "Alice",
		TimeZone:    "UTC",
	}
	db := records.NewShared(backend, u, 5.0)
	db.Now = clk.Now
	store := state.Compose(db, memstore.NewArtifacts())
	sum := &fakeSummaryModel{}
	model := &fakeReplyModel{now: clk.Now}
	tasks := &Tasks{Store: store, Now: clk.Now}
	r := New(model, nil, sum, tasks)
	r.Now = clk.Now
	r.Clock = Clock{Now: clk.Now, Location: time.UTC}
	r.SyncWork = true
	return &idleTestHarness{
		t:      t,
		clk:    clk,
		store:  db,
		runner: r,
		model:  model,
		sum:    sum,
	}
}

func (h *idleTestHarness) sendTurn(ctx context.Context, text string) (inputID string, turnStartedAt time.Time) {
	h.t.Helper()
	h.seq++
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("idle-test-input-%d", h.seq))).String()
	if _, err := h.runner.HandleInput(ctx, Input{ID: id, Message: text}); err != nil {
		h.t.Fatalf("HandleInput(%q): %v", text, err)
	}
	if err := h.runner.Process(ctx); err != nil {
		h.t.Fatalf("Process(%q): %v", text, err)
	}
	turns, err := h.store.ConversationTurnsAfter(ctx, time.Time{})
	if err != nil || len(turns) == 0 {
		h.t.Fatalf("ConversationTurnsAfter: len=%d err=%v", len(turns), err)
	}
	return id, turns[0].StartedAt
}

func (h *idleTestHarness) sendTaskEventTurn(ctx context.Context, runID, text string) (eventID string, turnStartedAt time.Time) {
	h.t.Helper()
	h.seq++
	cmdID := fmt.Sprintf("cmd-%d", h.seq)
	if _, err := h.store.AcceptCommandBatch(ctx, []state.Command{
		{ID: cmdID, RunID: runID, Kind: state.CommandStart, Text: text},
	}, nil); err != nil {
		h.t.Fatalf("AcceptCommandBatch(%q): %v", runID, err)
	}
	run, err := h.store.GetRun(ctx, runID)
	if err != nil {
		h.t.Fatalf("GetRun(%q): %v", runID, err)
	}
	if err := h.store.Incorporate(ctx, state.IncorporateRequest{
		RunID: runID, Revision: run.Revision, Position: state.TaskPosition(run),
	}); err != nil {
		h.t.Fatalf("Incorporate(%q): %v", runID, err)
	}
	if _, err := h.store.Publish(ctx, state.PublishRequest{
		RunID:    runID,
		Revision: run.Revision,
		Status:   state.RunCompleted,
		Text:     text,
	}); err != nil {
		h.t.Fatalf("Publish(%q): %v", runID, err)
	}
	if err := h.runner.Process(ctx); err != nil {
		h.t.Fatalf("Process task event (%q): %v", text, err)
	}
	turns, err := h.store.ConversationTurnsAfter(ctx, time.Time{})
	if err != nil || len(turns) == 0 {
		h.t.Fatalf("ConversationTurnsAfter after task event: len=%d err=%v", len(turns), err)
	}
	return runID, turns[0].StartedAt
}

type promptTurn struct {
	StartedAt        time.Time           `json:"started_at"`
	AlreadyExtracted bool                `json:"already_extracted"`
	Messages         []map[string]string `json:"messages"`
}

func decodePromptTurns(t *testing.T, payload map[string]any) []promptTurn {
	t.Helper()
	raw, err := json.Marshal(payload["turns"])
	if err != nil {
		t.Fatalf("marshal payload turns: %v", err)
	}
	var turns []promptTurn
	if err := json.Unmarshal(raw, &turns); err != nil {
		t.Fatalf("unmarshal payload turns: %v", err)
	}
	return turns
}

func TestIdleExtract_TwoTurnsAt2mLeavesVerbatimAndParks(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := newIdleTestHarness(t, start)

	h.sum.handler = func(_ context.Context, _ int, payload map[string]any, _ agent.Request) (agent.Response, error) {
		turns := decodePromptTurns(t, payload)
		var coreUpserts []any
		var logEntries []any
		for _, turn := range turns {
			for _, msg := range turn.Messages {
				if msg["from"] != "user" {
					continue
				}
				coreUpserts = append(coreUpserts, map[string]any{
					"text":     "Durable fact: " + msg["text"],
					"about":    "the user",
					"section":  "Facts",
					"replaces": "",
					"input_id": msg["input_id"],
					"quote":    msg["text"],
				})
				logEntries = append(logEntries, map[string]any{
					"text":     "Episodic note: " + msg["text"],
					"about":    "the user",
					"section":  "Context",
					"input_id": msg["input_id"],
					"run_id":   "",
					"quote":    msg["text"],
				})
			}
		}
		return toolResponse(flushAndCompactToolName, map[string]any{
			"core_upserts":      coreUpserts,
			"daily_log_entries": logEntries,
			"summary_items":     []any{},
			"gist":              "should be ignored in extract_only mode",
		}), nil
	}

	// Send 2 completed user turns (< ExtractTurnThreshold = 3).
	_, _ = h.sendTurn(ctx, "I prefer dark roast espresso.")
	h.clk.Advance(10 * time.Second)
	_, t2 := h.sendTurn(ctx, "My dog's name is Mochi.")

	// NextConversationWake must return latestTurnAt + IdleExtractDelay (2m).
	wakeAt, err := h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake: %v", err)
	}
	wantWake := t2.Add(state.IdleExtractDelay)
	if !wakeAt.Equal(wantWake) {
		t.Fatalf("NextConversationWake = %v, want %v", wakeAt, wantWake)
	}

	// Before 2m elapses, Process/compact must NOT run extraction.
	h.clk.Advance(state.IdleExtractDelay - time.Second)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process before 2m: %v", err)
	}
	if h.sum.CallCount() != 0 {
		t.Fatalf("expected 0 summarizer calls before 2m, got %d", h.sum.CallCount())
	}

	// Advance to exactly +2m -> extractIdleMemory runs.
	h.clk.Advance(time.Second)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process at +2m: %v", err)
	}
	if h.sum.CallCount() != 1 {
		t.Fatalf("expected 1 summarizer call at +2m, got %d", h.sum.CallCount())
	}
	call := h.sum.Calls()[0]
	if extractOnly, _ := call.Payload["extract_only"].(bool); !extractOnly {
		t.Fatalf("expected extract_only=true in payload, got %#v", call.Payload["extract_only"])
	}
	sentTurns := decodePromptTurns(t, call.Payload)
	if len(sentTurns) != 2 {
		t.Fatalf("expected 2 turns in extract payload, got %d", len(sentTurns))
	}
	for i, st := range sentTurns {
		if st.AlreadyExtracted {
			t.Fatalf("turn %d unexpectedly marked already_extracted=true", i)
		}
	}

	// Verify Core Memories and Daily Logs were committed atomically.
	coreMems, err := h.store.Memories(ctx)
	if err != nil {
		t.Fatalf("Memories: %v", err)
	}
	if len(coreMems) != 2 {
		t.Fatalf("expected 2 core memories, got %d: %+v", len(coreMems), coreMems)
	}
	logs, err := h.store.MemoryLogs(ctx, "", 10)
	if err != nil {
		t.Fatalf("MemoryLogs: %v", err)
	}
	if len(logs) != 1 || len(logs[0].Entries) != 2 {
		t.Fatalf("expected 2 daily log entries, got %+v", logs)
	}

	// Verify ConversationSummary watermarks: ExtractedThrough == T2, Through == 0, FlushedThrough == 0.
	sum, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary: %v", err)
	}
	if !sum.ExtractedThrough.Equal(t2) {
		t.Fatalf("ExtractedThrough = %v, want %v", sum.ExtractedThrough, t2)
	}
	if !sum.Through.IsZero() || !sum.FlushedThrough.IsZero() || !sum.RefinementThrough.IsZero() {
		t.Fatalf("expected Through/FlushedThrough/RefinementThrough zero, got through=%v flushed=%v ref=%v", sum.Through, sum.FlushedThrough, sum.RefinementThrough)
	}
	if sum.Turns != 0 || sum.Gist != "" || len(sum.Items) != 0 || len(sum.Excerpts) != 0 {
		t.Fatalf("expected Working Memory untouched, got turns=%d gist=%q items=%d excerpts=%d", sum.Turns, sum.Gist, len(sum.Items), len(sum.Excerpts))
	}

	// All 2 turns must remain verbatim in readAttemptHistory.
	hist := h.runner.readAttemptHistory(ctx, time.Time{})
	if hist.err != nil {
		t.Fatalf("readAttemptHistory: %v", hist.err)
	}
	if hist.view.Summary != nil {
		t.Fatalf("expected nil Summary in readAttemptHistory while Through is zero, got %+v", hist.view.Summary)
	}
	if len(hist.view.Turns) != 2 {
		t.Fatalf("expected all 2 turns verbatim in readAttemptHistory, got %d", len(hist.view.Turns))
	}

	// NextConversationWake must be zero (parks cleanly), and staying idle for hours never triggers compaction.
	wakeAt, err = h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake after extract: %v", err)
	}
	if !wakeAt.IsZero() {
		t.Fatalf("expected zero NextConversationWake after extraction, got %v", wakeAt)
	}
	h.clk.Advance(4 * time.Hour)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process after 4h idle: %v", err)
	}
	if h.sum.CallCount() != 1 {
		t.Fatalf("expected no compaction after 4h idle, got %d summarizer calls", h.sum.CallCount())
	}
}

func TestIdleExtract_NoPrematureIdleCompactionAndSoftThresholdPreventsDoubleExtraction(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := newIdleTestHarness(t, start)

	// Adversarial model: ALWAYS attempts to emit core_upserts and daily_log_entries for EVERY turn
	// in the batch (even turns marked already_extracted=true!), plus summary_items and gist.
	h.sum.handler = func(_ context.Context, _ int, payload map[string]any, _ agent.Request) (agent.Response, error) {
		turns := decodePromptTurns(t, payload)
		var coreUpserts []any
		var logEntries []any
		var summaryItems []any
		var gistParts []string
		for _, turn := range turns {
			for _, msg := range turn.Messages {
				if msg["from"] != "user" {
					continue
				}
				// Deliberately emit a slightly different text on later calls to catch any duplicate insertion.
				tag := "extract"
				if extractOnly, _ := payload["extract_only"].(bool); !extractOnly {
					tag = "re-emitted-during-compact"
				}
				coreUpserts = append(coreUpserts, map[string]any{
					"text":     tag + ": " + msg["text"],
					"about":    "the user",
					"section":  "Facts",
					"replaces": "",
					"input_id": msg["input_id"],
					"quote":    msg["text"],
				})
				logEntries = append(logEntries, map[string]any{
					"text":     tag + " log: " + msg["text"],
					"about":    "the user",
					"section":  "Context",
					"input_id": msg["input_id"],
					"run_id":   "",
					"quote":    msg["text"],
				})
				summaryItems = append(summaryItems, map[string]any{
					"kind":     "fact",
					"quote":    msg["text"],
					"input_id": msg["input_id"],
					"note":     "compacted",
				})
				gistParts = append(gistParts, msg["text"])
			}
		}
		return toolResponse(flushAndCompactToolName, map[string]any{
			"core_upserts":      coreUpserts,
			"daily_log_entries": logEntries,
			"summary_items":     summaryItems,
			"gist":              "Discussed: " + strings.Join(gistParts, "; "),
		}), nil
	}

	// Send 15 user turns (> CompactKeep=14, < SoftCompactUserTurns=16) across two sessions.
	// Every ExtractTurnThreshold (6) turns triggers immediate decoupled extraction (2 calls + 1 idle drain), NEVER compaction.
	var turnIDs []string
	var turnTimes []time.Time
	for i := 1; i <= 15; i++ {
		if i == 4 {
			h.clk.Advance(10 * time.Hour)
		} else if i > 1 {
			h.clk.Advance(10 * time.Second)
		}
		id, ts := h.sendTurn(ctx, fmt.Sprintf("Turn %d: unique user fact #%d.", i, i))
		turnIDs = append(turnIDs, id)
		turnTimes = append(turnTimes, ts)
	}
	// Drain remaining unextracted turns (13..15) after IdleExtractDelay.
	h.clk.Advance(state.IdleExtractDelay + time.Second)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process idle drain: %v", err)
	}

	if want := 3; h.sum.CallCount() != want {
		t.Fatalf("expected %d extract_only calls across 15 user turns (threshold=%d + idle drain), got %d", want, state.ExtractTurnThreshold, h.sum.CallCount())
	}
	for idx, c := range h.sum.Calls() {
		if extractOnly, _ := c.Payload["extract_only"].(bool); !extractOnly {
			t.Fatalf("call %d unexpectedly ran compaction before SoftCompactUserTurns", idx)
		}
	}

	// Idle for 2m, 10m, and 6h -> NO premature idle compaction!
	// Note: 15 user-sourced log entries exist, so temporarily pin LastDreamAt to isolate compaction behavior.
	sumPreIdle, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary before idle: %v", err)
	}
	if !sumPreIdle.Through.IsZero() || !sumPreIdle.FlushedThrough.IsZero() || !sumPreIdle.ExtractedThrough.Equal(turnTimes[14]) {
		t.Fatalf("expected Through=0 FlushedThrough=0 ExtractedThrough=T15, got %+v", sumPreIdle)
	}
	sumPinnedDream := sumPreIdle
	sumPinnedDream.LastDreamAt = h.clk.Now().UTC()
	sumPinnedDream.DreamThrough = h.clk.Now().UTC()
	sumPinnedDream.UpdatedAt = h.clk.Now().UTC().Add(time.Second)
	if err := h.store.SaveConversationSummary(ctx, sumPinnedDream, sumPreIdle); err != nil {
		t.Fatalf("SaveConversationSummary dream bookmark: %v", err)
	}

	wakeIdle, err := h.runner.NextConversationWake(ctx)
	if err != nil || !wakeIdle.IsZero() {
		t.Fatalf("expected zero NextConversationWake at 15 user turns (< SoftCompactUserTurns=16), got %v err=%v", wakeIdle, err)
	}
	for _, d := range []time.Duration{2 * time.Minute, 10 * time.Minute, 6 * time.Hour} {
		h.clk.Advance(d)
		if err := h.runner.Process(ctx); err != nil {
			t.Fatalf("Process after idle %v: %v", d, err)
		}
	}
	if h.sum.CallCount() != 3 {
		t.Fatalf("expected still 3 calls (zero premature idle compaction), got %d", h.sum.CallCount())
	}
	histBefore16 := h.runner.readAttemptHistory(ctx, time.Time{})
	if histBefore16.err != nil || histBefore16.view.Summary != nil || len(histBefore16.view.Turns) != 15 {
		t.Fatalf("expected all 15 turns verbatim before turn 16, got summary=%v turns=%d err=%v", histBefore16.view.Summary, len(histBefore16.view.Turns), histBefore16.err)
	}

	// Turn 16 (== SoftCompactUserTurns=16) triggers flushAndCompact immediately!
	// Keeps CompactKeep=14 turns verbatim (Turns 3..16) and compacts Turns 1..2.
	h.clk.Advance(10 * time.Second)
	_, t16 := h.sendTurn(ctx, "Turn 16: unique user fact #16.")
	if h.sum.CallCount() != 4 {
		t.Fatalf("expected 4 summarizer calls (3 extracts + 1 compact at turn 16), got %d", h.sum.CallCount())
	}
	compactCall := h.sum.Calls()[3]
	if _, hasExtractOnly := compactCall.Payload["extract_only"]; hasExtractOnly {
		t.Fatalf("expected coupled flushAndCompact not to set extract_only, got %#v", compactCall.Payload["extract_only"])
	}
	compactTurns := decodePromptTurns(t, compactCall.Payload)
	if len(compactTurns) != 2 || !compactTurns[0].AlreadyExtracted || !compactTurns[1].AlreadyExtracted {
		t.Fatalf("expected 2 already_extracted=true turns (Turns 1..2) in compact payload, got %+v", compactTurns)
	}

	// Verify Turns 1..2 were summarized into Working Memory, FlushedThrough=Through=T2, and ExtractedThrough stayed T15!
	sum, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after compact: %v", err)
	}
	if !sum.FlushedThrough.Equal(turnTimes[1]) || !sum.Through.Equal(turnTimes[1]) {
		t.Fatalf("expected FlushedThrough=Through=%v (T2), got flushed=%v through=%v", turnTimes[1], sum.FlushedThrough, sum.Through)
	}
	if !sum.ExtractedThrough.Equal(turnTimes[14]) {
		t.Fatalf("expected ExtractedThrough preserved at %v (T15), got %v", turnTimes[14], sum.ExtractedThrough)
	}
	if len(sum.Items) != 2 || sum.Items[0].InputID != turnIDs[0] || sum.Items[1].InputID != turnIDs[1] {
		t.Fatalf("expected Turns 1..2 summarized in Summary.Items, got %+v", sum.Items)
	}
	if !strings.Contains(sum.Gist, "unique user fact #1") || !strings.Contains(sum.Gist, "unique user fact #2") {
		t.Fatalf("expected Turns 1..2 in Gist, got %q", sum.Gist)
	}

	// Verify NO duplicate Core memories or Daily Log entries were created even though the adversarial model emitted them!
	coreAfterCompact, err := h.store.Memories(ctx)
	if err != nil {
		t.Fatalf("Memories after compact: %v", err)
	}
	if len(coreAfterCompact) != 15 {
		t.Fatalf("expected still 15 core memories (no double-extraction of Turns 1..2), got %d", len(coreAfterCompact))
	}
	for _, m := range coreAfterCompact {
		if strings.HasPrefix(m.Text, "re-emitted-during-compact:") {
			t.Fatalf("adversarial double-extracted core memory leaked: %+v", m)
		}
	}
	logsAfterCompact, err := h.store.MemoryLogs(ctx, "", 10)
	if err != nil {
		t.Fatalf("MemoryLogs after compact: %v", err)
	}
	totalLogs := 0
	for _, doc := range logsAfterCompact {
		for _, e := range doc.Entries {
			totalLogs++
			if strings.HasPrefix(e.Text, "re-emitted-during-compact") {
				t.Fatalf("adversarial double-extracted daily log entry leaked: %+v", e)
			}
		}
	}
	if totalLogs != 15 {
		t.Fatalf("expected still 15 daily log entries (no duplicate for Turns 1..2), got %d", totalLogs)
	}

	// Verify readAttemptHistory now sees Summary (Through=T2) + 14 verbatim turns (Turns 3..16).
	hist := h.runner.readAttemptHistory(ctx, time.Time{})
	if hist.err != nil {
		t.Fatalf("readAttemptHistory: %v", hist.err)
	}
	if hist.view.Summary == nil || !hist.view.Summary.Through.Equal(turnTimes[1]) {
		t.Fatalf("expected Summary Through=%v in readAttemptHistory, got %+v", turnTimes[1], hist.view.Summary)
	}
	if len(hist.view.Turns) != state.CompactKeep {
		t.Fatalf("expected %d verbatim turns (Turns 3..16), got %d", state.CompactKeep, len(hist.view.Turns))
	}

	// NextConversationWake must schedule T16 + IdleExtractDelay (2m) for extracting Turn 16.
	wakeAt, err := h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake after turn 16 compact: %v", err)
	}
	wantExtractWake := t16.Add(state.IdleExtractDelay)
	if !wakeAt.Equal(wantExtractWake) {
		t.Fatalf("NextConversationWake = %v, want %v (T16 + 2m)", wakeAt, wantExtractWake)
	}

	// Advance +2m -> extracts ONLY Turn 16, leaving Through=FlushedThrough=T2 unchanged.
	h.clk.Advance(state.IdleExtractDelay)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process idle extract for Turn 16: %v", err)
	}
	if h.sum.CallCount() != 5 {
		t.Fatalf("expected 5 summarizer calls total, got %d", h.sum.CallCount())
	}
	extractTurns := decodePromptTurns(t, h.sum.Calls()[4].Payload)
	if len(extractTurns) != 1 {
		t.Fatalf("expected only Turn 16 (1 turn) in extract call, got %d", len(extractTurns))
	}
	sum2, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after Turn 16 extract: %v", err)
	}
	if !sum2.ExtractedThrough.Equal(t16) {
		t.Fatalf("ExtractedThrough = %v, want %v", sum2.ExtractedThrough, t16)
	}
	if !sum2.FlushedThrough.Equal(turnTimes[1]) || !sum2.Through.Equal(turnTimes[1]) {
		t.Fatalf("expected Through/FlushedThrough unchanged at %v, got flushed=%v through=%v", turnTimes[1], sum2.FlushedThrough, sum2.Through)
	}
	coreFinal, err := h.store.Memories(ctx)
	if err != nil || len(coreFinal) != 16 {
		t.Fatalf("expected 16 unique core memories (Turns 1..16), got %d err=%v", len(coreFinal), err)
	}
	wakeFinal, err := h.runner.NextConversationWake(ctx)
	if err != nil || !wakeFinal.IsZero() {
		t.Fatalf("expected zero NextConversationWake after extracting all turns, got %v err=%v", wakeFinal, err)
	}
}

func TestIdleExtract_PreemptionByUserMessageLeavesExtractedThroughUntouched(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := newIdleTestHarness(t, start)

	tickCh := make(chan time.Time, 4)
	h.runner.NewTicker = func(time.Duration) (<-chan time.Time, func()) {
		return tickCh, func() {}
	}

	_, _ = h.sendTurn(ctx, "Turn 1: Planning a marathon.")
	h.clk.Advance(10 * time.Second)
	_, _ = h.sendTurn(ctx, "Turn 2: Target race is in October.")

	// Advance +20m so idle extraction triggers.
	h.clk.Advance(state.IdleExtractDelay)

	started := make(chan struct{})
	preempted := make(chan bool, 1)
	h.sum.handler = func(callCtx context.Context, _ int, _ map[string]any, _ agent.Request) (agent.Response, error) {
		close(started)
		<-callCtx.Done()
		preempted <- true
		return agent.Response{}, callCtx.Err()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.runner.compact(ctx)
	}()

	<-started
	// User sends a new message mid-extraction -> UserRoutingPending becomes true.
	urgentID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("preempt-msg")).String()
	if _, err := h.runner.HandleInput(ctx, Input{ID: urgentID, Message: "Wait, quick question!"}); err != nil {
		t.Fatalf("HandleInput during extract: %v", err)
	}
	// Fire the 500ms watcher tick deterministically.
	tickCh <- h.clk.Now()

	select {
	case <-preempted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for idle extraction preemption")
	}
	<-done

	// Verify ExtractedThrough and ExtractFailures are completely untouched.
	sum, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary: %v", err)
	}
	if !sum.ExtractedThrough.IsZero() {
		t.Fatalf("expected ExtractedThrough zero after preemption, got %v", sum.ExtractedThrough)
	}
	if sum.ExtractFailures != 0 || !sum.LastExtractFailureThrough.IsZero() {
		t.Fatalf("expected ExtractFailures=0 after preemption, got failures=%d through=%v", sum.ExtractFailures, sum.LastExtractFailureThrough)
	}
	if !h.runner.compactAfter.IsZero() {
		t.Fatalf("expected no failure backoff after user preemption, got compactAfter=%v", h.runner.compactAfter)
	}
	mems, err := h.store.Memories(ctx)
	if err != nil || len(mems) != 0 {
		t.Fatalf("expected 0 memories committed on preemption, got %d err=%v", len(mems), err)
	}
}

func TestIdleExtract_ThreeStrikeFailureTriggersLosslessFallback(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := newIdleTestHarness(t, start)

	h.sum.handler = func(_ context.Context, _ int, _ map[string]any, _ agent.Request) (agent.Response, error) {
		return agent.Response{}, errors.New("upstream provider 503")
	}

	_, _ = h.sendTurn(ctx, "Remember my locker code is 42-18-09.")
	h.clk.Advance(10 * time.Second)
	_, t2 := h.sendTurn(ctx, "My flight departs at 06:30.")

	// Advance +20m -> Strike 1 (2m backoff).
	h.clk.Advance(state.IdleExtractDelay)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process strike 1: %v", err)
	}
	sum1, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after strike 1: %v", err)
	}
	if sum1.ExtractFailures != 1 || !sum1.LastExtractFailureThrough.Equal(t2) || !sum1.ExtractedThrough.IsZero() {
		t.Fatalf("unexpected summary after strike 1: %+v", sum1)
	}
	wake1, err := h.runner.NextConversationWake(ctx)
	if err != nil || !wake1.Equal(h.clk.Now().Add(2*time.Minute)) {
		t.Fatalf("expected +2m backoff wake after strike 1, got %v (now=%v) err=%v", wake1, h.clk.Now(), err)
	}

	// Advance +2m -> Strike 2 (5m backoff).
	h.clk.Advance(2 * time.Minute)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process strike 2: %v", err)
	}
	sum2, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after strike 2: %v", err)
	}
	if sum2.ExtractFailures != 2 || !sum2.LastExtractFailureThrough.Equal(t2) || !sum2.ExtractedThrough.IsZero() {
		t.Fatalf("unexpected summary after strike 2: %+v", sum2)
	}
	wake2, err := h.runner.NextConversationWake(ctx)
	if err != nil || !wake2.Equal(h.clk.Now().Add(5*time.Minute)) {
		t.Fatalf("expected +5m backoff wake after strike 2, got %v (now=%v) err=%v", wake2, h.clk.Now(), err)
	}

	// Advance +5m -> Strike 3 -> lossless raw-log fallback commits Daily Log entries and advances ExtractedThrough!
	h.clk.Advance(5 * time.Minute)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process strike 3: %v", err)
	}
	sum3, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after strike 3: %v", err)
	}
	if !sum3.ExtractedThrough.Equal(t2) {
		t.Fatalf("expected ExtractedThrough=%v after 3-strike fallback, got %v", t2, sum3.ExtractedThrough)
	}
	if sum3.ExtractFailures != 0 || !sum3.LastExtractFailureThrough.IsZero() {
		t.Fatalf("expected ExtractFailures reset after fallback, got failures=%d through=%v", sum3.ExtractFailures, sum3.LastExtractFailureThrough)
	}
	if !sum3.Through.IsZero() || !sum3.FlushedThrough.IsZero() {
		t.Fatalf("expected Through/FlushedThrough still zero after idle-extract fallback, got through=%v flushed=%v", sum3.Through, sum3.FlushedThrough)
	}

	logs, err := h.store.MemoryLogs(ctx, "", 10)
	if err != nil {
		t.Fatalf("MemoryLogs after fallback: %v", err)
	}
	if len(logs) != 1 || len(logs[0].Entries) != 2 {
		t.Fatalf("expected 2 lossless raw fallback log entries, got %+v", logs)
	}
	joined := logs[0].Entries[0].Text + "\n" + logs[0].Entries[1].Text
	if !strings.Contains(joined, "42-18-09") || !strings.Contains(joined, "06:30") {
		t.Fatalf("expected raw user messages preserved in fallback logs, got %q", joined)
	}

	// NextConversationWake must now be zero (parks cleanly).
	wake3, err := h.runner.NextConversationWake(ctx)
	if err != nil || !wake3.IsZero() {
		t.Fatalf("expected zero NextConversationWake after 3-strike fallback, got %v err=%v", wake3, err)
	}
}

func TestIdleExtract_AutoDreamPreservesExtractedThrough(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := newIdleTestHarness(t, start)

	h.sum.handler = func(_ context.Context, _ int, _ map[string]any, req agent.Request) (agent.Response, error) {
		for _, tool := range req.Tools {
			if tool.Name == dreamConsolidateToolName {
				return toolResponse(dreamConsolidateToolName, map[string]any{
					"core_upserts":      []any{},
					"core_deletes":      []any{},
					"daily_log_entries": []any{},
				}), nil
			}
		}
		return toolResponse(flushAndCompactToolName, map[string]any{
			"core_upserts":      []any{},
			"daily_log_entries": []any{},
			"summary_items":     []any{},
			"gist":              "",
		}), nil
	}

	// Complete ExtractTurnThreshold turns -> extracts immediately, so ExtractedThrough == lastTurn > FlushedThrough (0).
	var t3 time.Time
	for i := 1; i <= state.ExtractTurnThreshold; i++ {
		if i > 1 {
			h.clk.Advance(10 * time.Second)
		}
		_, t3 = h.sendTurn(ctx, fmt.Sprintf("Turn %d", i))
	}

	// Seed 10 user-sourced Daily Log entries so AutoDream triple-gate is satisfied.
	curSum, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary: %v", err)
	}
	if !curSum.ExtractedThrough.Equal(t3) || !curSum.FlushedThrough.IsZero() {
		t.Fatalf("unexpected pre-dream summary: %+v", curSum)
	}
	seedEntries := make([]state.MemoryLogEntry, 0, state.DreamMinLogEntries)
	seedNow := h.clk.Now().UTC()
	for i := range state.DreamMinLogEntries {
		seedEntries = append(seedEntries, state.MemoryLogEntry{
			ID:            fmt.Sprintf("seed-log-%d", i),
			Section:       "Context",
			Text:          fmt.Sprintf("User log fact #%d", i),
			About:         "the user",
			Provenance:    state.ProvenancePreCompactFlush,
			SourceInputID: fmt.Sprintf("in-seed-%d", i),
			SourceText:    fmt.Sprintf("User log fact #%d", i),
			CreatedAt:     seedNow,
			UpdatedAt:     seedNow,
		})
	}
	nextSeed := curSum
	nextSeed.UpdatedAt = seedNow.Add(time.Second)
	if _, err := h.store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
		ExpectedSummary: curSum,
		NextSummary:     nextSeed,
		DailyLogDate:    seedNow.Format("2006-01-02"),
		DailyLogEntries: seedEntries,
	}); err != nil {
		t.Fatalf("seed Daily Logs: %v", err)
	}

	// Advance past IdleDreamDelay (15m) and run idle AutoDream via Process -> maybeDream.
	h.clk.Advance(state.IdleDreamDelay)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process idle dream: %v", err)
	}

	afterDream, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after dream: %v", err)
	}
	if afterDream.LastDreamAt.IsZero() || afterDream.DreamThrough.IsZero() {
		t.Fatalf("expected AutoDream to update LastDreamAt/DreamThrough, got %+v", afterDream)
	}
	if !afterDream.ExtractedThrough.Equal(t3) {
		t.Fatalf("AutoDream wiped or altered ExtractedThrough: got %v, want %v", afterDream.ExtractedThrough, t3)
	}
	if !afterDream.FlushedThrough.IsZero() || !afterDream.Through.IsZero() {
		t.Fatalf("AutoDream unexpectedly modified FlushedThrough/Through: flushed=%v through=%v", afterDream.FlushedThrough, afterDream.Through)
	}

	// Also verify ConsolidateNow when ExtractedThrough > FlushedThrough > 0 preserves ExtractedThrough.
	flushedSummary := afterDream
	flushedSummary.FlushedThrough = t3.Add(-10 * time.Second)
	flushedSummary.Through = flushedSummary.FlushedThrough
	flushedSummary.UpdatedAt = h.clk.Now().UTC().Add(time.Second)
	if err := h.store.SaveConversationSummary(ctx, flushedSummary, afterDream); err != nil {
		t.Fatalf("SaveConversationSummary: %v", err)
	}
	h.clk.Advance(time.Minute)
	consolidated, err := h.runner.ConsolidateNow(ctx)
	if err != nil {
		t.Fatalf("ConsolidateNow: %v", err)
	}
	if !consolidated.ExtractedThrough.Equal(t3) {
		t.Fatalf("ConsolidateNow wiped ExtractedThrough: got %v, want %v", consolidated.ExtractedThrough, t3)
	}
	if !consolidated.FlushedThrough.Equal(flushedSummary.FlushedThrough) {
		t.Fatalf("ConsolidateNow altered FlushedThrough: got %v, want %v", consolidated.FlushedThrough, flushedSummary.FlushedThrough)
	}
}

func TestIdleExtract_PinnedProtectionAndAdvanceHotWindowPreservesExtractedThrough(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := newIdleTestHarness(t, start)

	// Send Turn 1 at T1.
	in1, t1 := h.sendTurn(ctx, "I prefer tabs over spaces.")

	// User manually pins a Core memory AFTER T1 via UI (UpdatedAt = T1 + 5m).
	h.clk.Advance(5 * time.Minute)
	pinned := true
	if _, err := h.store.MutateMemory(ctx, state.MemoryMutationRequest{
		RequestID: "req-pin-1",
		Kind:      state.MemoryMutateCreateCore,
		TargetID:  "mem-pinned-style",
		Text:      "Always use 2 spaces for indentation.",
		About:     "the user",
		Section:   "Preferences",
		Pinned:    &pinned,
	}); err != nil {
		t.Fatalf("MutateMemory pin: %v", err)
	}

	// Idle extraction attempts to overwrite the newer pinned memory citing older Turn 1.
	h.sum.handler = func(_ context.Context, _ int, _ map[string]any, _ agent.Request) (agent.Response, error) {
		return toolResponse(flushAndCompactToolName, map[string]any{
			"core_upserts": []any{
				map[string]any{
					"text":     "Prefers tabs over spaces.",
					"about":    "the user",
					"section":  "Preferences",
					"replaces": "mem-pinned-style",
					"input_id": in1,
					"quote":    "I prefer tabs over spaces.",
				},
			},
			"daily_log_entries": []any{},
			"summary_items":     []any{},
			"gist":              "",
		}), nil
	}

	h.clk.Advance(state.IdleExtractDelay)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process idle extract with pinned protection: %v", err)
	}

	mems, err := h.store.Memories(ctx)
	if err != nil {
		t.Fatalf("Memories: %v", err)
	}
	if len(mems) != 1 || mems[0].ID != "mem-pinned-style" || mems[0].Text != "Always use 2 spaces for indentation." || !mems[0].Pinned {
		t.Fatalf("expected newer pinned memory protected from older turn idle extraction, got %+v", mems)
	}
	sum, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary: %v", err)
	}
	if !sum.ExtractedThrough.Equal(t1) || !sum.Through.IsZero() {
		t.Fatalf("expected ExtractedThrough=%v and Through=0, got extracted=%v through=%v", t1, sum.ExtractedThrough, sum.Through)
	}

	// Now simulate a rapid burst of 22 turns (> RecentTurnLimit=20 before final readAttemptHistory)
	// so advanceHotWindow runs while Through is still zero, and verify advanceHotWindow preserves ExtractedThrough!
	h.runner.summarizer = nil // disable out-of-band flush so only advanceHotWindow runs
	for i := range state.RecentTurnLimit + 1 {
		h.clk.Advance(time.Second)
		_, _ = h.sendTurn(ctx, fmt.Sprintf("Burst turn %d", i+2))
	}
	sumAfterBurst, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after burst: %v", err)
	}
	if sumAfterBurst.Through.IsZero() {
		t.Fatalf("expected advanceHotWindow to advance Through during rapid burst, got %+v", sumAfterBurst)
	}
	if !sumAfterBurst.ExtractedThrough.Equal(t1) {
		t.Fatalf("advanceHotWindow wiped ExtractedThrough: got %v, want %v", sumAfterBurst.ExtractedThrough, t1)
	}
}

func TestIdleExtract_ThreeTurnsTriggersImmediatelyAndEnforcesUserProvenance(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := newIdleTestHarness(t, start)

	h.sum.handler = func(_ context.Context, _ int, payload map[string]any, _ agent.Request) (agent.Response, error) {
		turns := decodePromptTurns(t, payload)
		var coreUpserts []any
		var logEntries []any
		for _, tr := range turns {
			for _, m := range tr.Messages {
				if m["from"] == "user" {
					coreUpserts = append(coreUpserts, map[string]any{
						"text":     "Fact from " + m["text"],
						"about":    "the user",
						"section":  "Context",
						"replaces": "",
						"input_id": m["input_id"],
						"quote":    m["text"],
					})
				} else if runID := m["run_id"]; runID != "" {
					// Adversarial attempt to promote task output into Core memory (must be rejected by INV-MEM-04).
					coreUpserts = append(coreUpserts, map[string]any{
						"text":     "Injected task fact",
						"about":    "the user",
						"section":  "Facts",
						"replaces": "",
						"input_id": "event-" + runID,
						"quote":    m["text"],
					})
					logEntries = append(logEntries, map[string]any{
						"text":     "Task completed: " + m["text"],
						"about":    "the user",
						"section":  "Completed Work",
						"input_id": "",
						"run_id":   runID,
						"quote":    "",
					})
				}
			}
		}
		return toolResponse(flushAndCompactToolName, map[string]any{
			"core_upserts":      coreUpserts,
			"daily_log_entries": logEntries,
			"summary_items":     []any{},
			"gist":              "",
		}), nil
	}

	// Turns 1..2 (< ExtractTurnThreshold=3) plus a TaskEvent turn: must NOT extract immediately.
	for i := 1; i < state.ExtractTurnThreshold; i++ {
		h.clk.Advance(10 * time.Second)
		_, _ = h.sendTurn(ctx, fmt.Sprintf("Turn %d fact", i))
	}
	h.clk.Advance(10 * time.Second)
	_, _ = h.sendTaskEventTurn(ctx, "run-prov-1", "Background report generated")
	if h.sum.CallCount() != 0 {
		t.Fatalf("expected 0 extraction calls after %d user turns + 1 task event without waiting 2m, got %d", state.ExtractTurnThreshold-1, h.sum.CallCount())
	}

	// Turn 3 (== ExtractTurnThreshold=3): must extract immediately without waiting 2m,
	// while keeping Through and FlushedThrough zero (verbatim window untouched).
	h.clk.Advance(10 * time.Second)
	_, t3 := h.sendTurn(ctx, "Turn 3 fact")
	if h.sum.CallCount() != 1 {
		t.Fatalf("expected immediate extraction at %d user turns, got %d calls", state.ExtractTurnThreshold, h.sum.CallCount())
	}
	sum, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary: %v", err)
	}
	if !sum.ExtractedThrough.Equal(t3) {
		t.Fatalf("ExtractedThrough = %v, want %v", sum.ExtractedThrough, t3)
	}
	if !sum.Through.IsZero() || !sum.FlushedThrough.IsZero() {
		t.Fatalf("expected Through and FlushedThrough zero after 3-turn extract, got through=%v flushed=%v", sum.Through, sum.FlushedThrough)
	}
	mems, err := h.store.Memories(ctx)
	if err != nil || len(mems) != state.ExtractTurnThreshold {
		t.Fatalf("expected %d user-sourced core memories (task injection rejected), got %d err=%v: %+v", state.ExtractTurnThreshold, len(mems), err, mems)
	}
	for _, m := range mems {
		if strings.Contains(m.Text, "Injected task fact") {
			t.Fatalf("INV-MEM-04 violation: task event promoted to Core memory: %+v", m)
		}
	}
	logs, err := h.store.MemoryLogs(ctx, "", 10)
	if err != nil || len(logs) != 1 || len(logs[0].Entries) == 0 {
		t.Fatalf("expected task_outcome daily log entries, got %+v err=%v", logs, err)
	}
	foundExtractedTaskLog := false
	for _, entry := range logs[0].Entries {
		if entry.Provenance != state.ProvenanceTaskOutcome {
			t.Fatalf("expected only task_outcome daily log entries, got %+v", entry)
		}
		if strings.Contains(entry.Text, "Task completed: Background report generated") {
			foundExtractedTaskLog = true
		}
	}
	if !foundExtractedTaskLog {
		t.Fatalf("expected extracted task_outcome daily log entry, got %+v", logs[0].Entries)
	}
	wakeAt, err := h.runner.NextConversationWake(ctx)
	if err != nil || !wakeAt.IsZero() {
		t.Fatalf("expected zero NextConversationWake after 3-turn extract, got %v err=%v", wakeAt, err)
	}
}

func TestCompaction_CompactKeepUserTurnsWithInterleavedTaskEvents(t *testing.T) {
	ctx := context.Background()

	t.Run("planFlush_SoftCompactTurns_preserves_last_10_user_turns", func(t *testing.T) {
		start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
		h := newIdleTestHarness(t, start)

		var compactBatches [][]promptTurn
		h.sum.handler = func(_ context.Context, _ int, payload map[string]any, _ agent.Request) (agent.Response, error) {
			turns := decodePromptTurns(t, payload)
			if extractOnly, _ := payload["extract_only"].(bool); !extractOnly {
				compactBatches = append(compactBatches, turns)
			}
			var items []any
			for _, tr := range turns {
				for _, m := range tr.Messages {
					if m["from"] == "user" {
						items = append(items, map[string]any{
							"kind":     "fact",
							"quote":    m["text"],
							"input_id": m["input_id"],
							"note":     "compacted",
						})
					}
				}
			}
			return toolResponse(flushAndCompactToolName, map[string]any{
				"core_upserts":      []any{},
				"daily_log_entries": []any{},
				"summary_items":     items,
				"gist":              "compacted older turns",
			}), nil
		}

		// Build 28 total turns (SoftCompactTurns=28) with 12 user turns + 16 TaskEvent turns:
		// Oldest 4: U1, E1, U2, E2
		// Middle 10: U3..U9 (7 user turns) + E3..E5 (3 task events)
		// Newest 14: E6..E16 (11 task events) + U10..U12 (3 user turns)
		// Only 3 of the newest 14 turns are user turns, so naive CompactKeep=14 would evict U3..U9!
		// With CompactKeepUserTurns=10, U3..U12 MUST all remain verbatim!
		userInputIDs := make([]string, 0, 12)
		sendUser := func(n int) {
			h.clk.Advance(10 * time.Second)
			id, _ := h.sendTurn(ctx, fmt.Sprintf("User exchange #%d", n))
			userInputIDs = append(userInputIDs, id)
		}
		sendEvt := func(n int) {
			h.clk.Advance(10 * time.Second)
			_, _ = h.sendTaskEventTurn(ctx, fmt.Sprintf("run-%d", n), fmt.Sprintf("Task event #%d done", n))
		}

		// Oldest 4
		sendUser(1)
		sendEvt(1)
		sendUser(2)
		sendEvt(2)
		// Middle 10 (7 user turns: U3..U9, 3 events: E3..E5)
		sendUser(3)
		sendUser(4)
		sendEvt(3)
		sendUser(5)
		sendUser(6)
		sendEvt(4)
		sendUser(7)
		sendUser(8)
		sendEvt(5)
		sendUser(9)
		// Newest 14 (11 events: E6..E16, 3 user turns: U10..U12)
		for e := 6; e <= 10; e++ {
			sendEvt(e)
		}
		sendUser(10)
		for e := 11; e <= 14; e++ {
			sendEvt(e)
		}
		sendUser(11)
		sendEvt(15)
		sendEvt(16)
		sendUser(12)

		if len(compactBatches) != 1 {
			t.Fatalf("expected 1 flushAndCompact call at SoftCompactTurns=%d, got %d", state.SoftCompactTurns, len(compactBatches))
		}
		if len(compactBatches[0]) != 4 {
			t.Fatalf("expected oldest 4 turns (U1,E1,U2,E2) compacted, got %d turns", len(compactBatches[0]))
		}

		// Verify all 10 newest user turns (U3..U12) survive verbatim in readAttemptHistory!
		hist := h.runner.readAttemptHistory(ctx, time.Time{})
		if hist.err != nil {
			t.Fatalf("readAttemptHistory: %v", hist.err)
		}
		if len(hist.view.Turns) != 24 {
			t.Fatalf("expected 24 verbatim turns kept (holding all 10 newest user turns), got %d", len(hist.view.Turns))
		}
		presentUsers := map[string]bool{}
		for _, tr := range hist.view.Turns {
			for _, in := range tr.Inputs {
				if in.IsUser() {
					presentUsers[in.ID] = true
				}
			}
		}
		for i := 2; i < 12; i++ {
			if !presentUsers[userInputIDs[i]] {
				t.Fatalf("user exchange #%d (id=%s) was evicted by TaskEvent turns!", i+1, userInputIDs[i])
			}
		}
		if presentUsers[userInputIDs[0]] || presentUsers[userInputIDs[1]] {
			t.Fatalf("expected oldest 2 user exchanges U1..U2 to be compacted")
		}
	})

	t.Run("advanceHotWindow_preserves_last_10_user_turns_bounded_by_RecentTurnBytes", func(t *testing.T) {
		start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
		h := newIdleTestHarness(t, start)
		h.runner.summarizer = nil // force hot-window path only

		// Drive 38 total turns (> RecentTurnLimit=36):
		// Oldest 10: U1, U2, E1..E8
		// Next 8: U3..U8 (6 user turns), E9..E10
		// Newest 20: E11..E26 (16 task events) + U9..U12 (4 user turns)
		// Keeping U3..U12 requires 28 turns (<= RecentTurnLimit=36, <= RecentTurnBytes=64KiB).
		userInputIDs := make([]string, 0, 12)
		sendUser := func(n int) {
			h.clk.Advance(time.Second)
			id, _ := h.sendTurn(ctx, fmt.Sprintf("HotWindow user #%d", n))
			userInputIDs = append(userInputIDs, id)
		}
		sendEvt := func(n int) {
			h.clk.Advance(time.Second)
			_, _ = h.sendTaskEventTurn(ctx, fmt.Sprintf("hot-run-%d", n), fmt.Sprintf("Hot task event #%d", n))
		}

		sendUser(1)
		sendUser(2)
		for e := 1; e <= 8; e++ {
			sendEvt(e)
		}
		for u := 3; u <= 8; u++ {
			sendUser(u)
		}
		sendEvt(9)
		sendEvt(10)
		for e := 11; e <= 22; e++ {
			sendEvt(e)
		}
		sendUser(9)
		sendUser(10)
		for e := 23; e <= 26; e++ {
			sendEvt(e)
		}
		sendUser(11)
		sendUser(12)

		hist := h.runner.readAttemptHistory(ctx, time.Time{})
		if hist.err != nil {
			t.Fatalf("readAttemptHistory after hot window: %v", hist.err)
		}
		if hist.view.Summary == nil || hist.view.Summary.Through.IsZero() {
			t.Fatalf("expected advanceHotWindow to advance Through, got %+v", hist.view.Summary)
		}
		if hist.view.Truncated {
			t.Fatalf("expected history untruncated after advanceHotWindow")
		}
		presentUsers := map[string]bool{}
		for _, tr := range hist.view.Turns {
			for _, in := range tr.Inputs {
				if in.IsUser() {
					presentUsers[in.ID] = true
				}
			}
		}
		for i := 2; i < 12; i++ {
			if !presentUsers[userInputIDs[i]] {
				t.Fatalf("advanceHotWindow evicted user exchange #%d (id=%s) due to TaskEvent turns", i+1, userInputIDs[i])
			}
		}
	})

	t.Run("verbatimKeepCount_respects_RecentTurnBytes_and_SoftCompactBytes", func(t *testing.T) {
		base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
		// 12 user turns of 8 KiB each = 96 KiB (> RecentTurnBytes=64 KiB).
		// Even though CompactKeepUserTurns=10 and CompactKeep=14, RecentTurnBytes bounds keep to 8 turns (64 KiB),
		// and SoftCompactBytes (48 KiB) bounds keep to 5 turns (40 KiB < 48 KiB).
		big := strings.Repeat("x", 8<<10)
		turns := make([]state.ConversationTurn, 12)
		for i := range turns {
			turns[i] = state.ConversationTurn{
				ID:        fmt.Sprintf("big-%d", i),
				Status:    "completed",
				StartedAt: base.Add(time.Duration(12-i) * time.Second),
				Inputs:    []state.ConversationInput{{ID: fmt.Sprintf("in-%d", i), Text: "msg"}},
				Protocol:  []string{big},
			}
		}
		if got := verbatimKeepCount(turns, time.Time{}, 0); got != 8 {
			t.Fatalf("verbatimKeepCount (RecentTurnBytes=64KiB) = %d, want 8", got)
		}
		if got := verbatimKeepCount(turns, time.Time{}, state.SoftCompactBytes); got != 5 {
			t.Fatalf("verbatimKeepCount (SoftCompactBytes=48KiB) = %d, want 5", got)
		}
	})
}

func TestCompaction_SoftCompactBytesAndCatchUpFlush(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := newIdleTestHarness(t, start)

	// 1. SoftCompactBytes (48 KiB) triggers compaction even with fewer than 16 turns.
	// Note: fakeReplyModel echoes the input, so each ~3.8 KiB user message creates ~7.7 KiB protocol bytes.
	payloadText := strings.Repeat("B", 3800)
	for i := range 7 {
		h.clk.Advance(10 * time.Second)
		_, _ = h.sendTurn(ctx, fmt.Sprintf("Big %d: %s", i+1, payloadText))
	}
	sumBytes, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after byte pressure: %v", err)
	}
	if sumBytes.Through.IsZero() || sumBytes.FlushedThrough.IsZero() {
		t.Fatalf("expected SoftCompactBytes (48 KiB) to trigger flush+compact, got %+v", sumBytes)
	}

	// 2. Catch-up flush when EffectiveFlushedThrough() < Through after an advanceHotWindow burst.
	h2 := newIdleTestHarness(t, start)
	h2.runner.summarizer = nil
	for i := range state.RecentTurnLimit + 2 {
		h2.clk.Advance(time.Second)
		_, _ = h2.sendTurn(ctx, fmt.Sprintf("Burst msg %d", i+1))
	}
	// Ensure advanceHotWindow has moved Through ahead of FlushedThrough (0).
	_ = h2.runner.readAttemptHistory(ctx, time.Time{})
	sumBurst, err := h2.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after burst: %v", err)
	}
	if sumBurst.Through.IsZero() || !sumBurst.EffectiveFlushedThrough().Before(sumBurst.Through) {
		t.Fatalf("expected catch-up state (FlushedThrough < Through), got %+v", sumBurst)
	}

	// Re-attach summarizer: NextConversationWake must report immediate catch-up wake (== now).
	h2.runner.summarizer = h2.sum
	wakeCatchUp, err := h2.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake during catchUp: %v", err)
	}
	if !wakeCatchUp.Equal(h2.clk.Now()) {
		t.Fatalf("expected immediate catchUp wake == now (%v), got %v", h2.clk.Now(), wakeCatchUp)
	}
	// Drain catch-up passes until FlushedThrough catches up to Through.
	for range 4 {
		if err := h2.runner.Process(ctx); err != nil {
			t.Fatalf("Process catchUp: %v", err)
		}
	}
	sumCaughtUp, err := h2.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after catchUp: %v", err)
	}
	if sumCaughtUp.EffectiveFlushedThrough().Before(sumCaughtUp.Through) {
		t.Fatalf("expected FlushedThrough >= Through after catch-up, got flushed=%v through=%v", sumCaughtUp.EffectiveFlushedThrough(), sumCaughtUp.Through)
	}
}

func TestAutoDream_UncompactedThreadMultiDayConsolidationAnd12hGate(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	h := newIdleTestHarness(t, start)

	dreamCalls := 0
	var lastDreamLogCount int
	h.sum.handler = func(_ context.Context, _ int, payload map[string]any, req agent.Request) (agent.Response, error) {
		for _, tool := range req.Tools {
			if tool.Name == dreamConsolidateToolName {
				dreamCalls++
				if rawLogs, ok := payload["daily_logs"].([]any); ok {
					lastDreamLogCount = len(rawLogs)
				}
				return toolResponse(dreamConsolidateToolName, map[string]any{
					"core_upserts":      []any{},
					"core_deletes":      []any{},
					"daily_log_entries": []any{},
				}), nil
			}
		}
		turns := decodePromptTurns(t, payload)
		var logEntries []any
		for _, tr := range turns {
			for _, m := range tr.Messages {
				if m["from"] == "user" {
					logEntries = append(logEntries, map[string]any{
						"text":     "Daily note: " + m["text"],
						"about":    "the user",
						"section":  "Context",
						"input_id": m["input_id"],
						"run_id":   "",
						"quote":    m["text"],
					})
				} else if runID := m["run_id"]; runID != "" {
					logEntries = append(logEntries, map[string]any{
						"text":     "Task outcome: " + m["text"],
						"about":    "the user",
						"section":  "Completed Work",
						"input_id": "",
						"run_id":   runID,
						"quote":    "",
					})
				}
			}
		}
		return toolResponse(flushAndCompactToolName, map[string]any{
			"core_upserts":      []any{},
			"daily_log_entries": logEntries,
			"summary_items":     []any{},
			"gist":              "",
		}), nil
	}

	// Accumulate 10 user turns across 2 days (< SoftCompactUserTurns=16 so Through stays zero!),
	// plus 2 task event turns whose task_outcome logs must NOT count toward AutoDream.
	for i := 1; i <= 3; i++ {
		h.clk.Advance(10 * time.Second)
		_, _ = h.sendTurn(ctx, fmt.Sprintf("Day 1 morning note %d", i))
	}
	h.clk.Advance(8 * time.Hour)
	for i := 4; i <= 6; i++ {
		h.clk.Advance(10 * time.Second)
		_, _ = h.sendTurn(ctx, fmt.Sprintf("Day 1 evening note %d", i))
	}
	h.clk.Advance(14 * time.Hour)
	for i := 7; i <= 9; i++ {
		h.clk.Advance(10 * time.Second)
		_, _ = h.sendTurn(ctx, fmt.Sprintf("Day 2 morning note %d", i))
	}
	h.clk.Advance(time.Hour)
	_, _ = h.sendTaskEventTurn(ctx, "run-dream-1", "Background sync finished")
	h.clk.Advance(10 * time.Second)
	_, t10 := h.sendTurn(ctx, "Day 2 afternoon note 10")

	// Extract turn 10 at +2m so 10 user-sourced Daily Log entries exist while Through.IsZero().
	h.clk.Advance(state.IdleExtractDelay)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process idle extract #4: %v", err)
	}
	if dreamCalls != 0 {
		t.Fatalf("expected AutoDream to wait for +15m quiet delay, got %d dream calls", dreamCalls)
	}

	// NextConversationWake must schedule AutoDream at t10 + IdleDreamDelay (15m).
	wakeDream, err := h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake before dream: %v", err)
	}
	wantDreamWake := t10.Add(state.IdleDreamDelay)
	if !wakeDream.Equal(wantDreamWake) {
		t.Fatalf("NextConversationWake = %v, want %v (t10 + 15m)", wakeDream, wantDreamWake)
	}

	// Advance to t10 + 15m -> AutoDream runs even though Through.IsZero()!
	h.clk.Advance(state.IdleDreamDelay - state.IdleExtractDelay)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process idle AutoDream on uncompacted thread: %v", err)
	}
	if dreamCalls != 1 {
		t.Fatalf("expected 1 AutoDream call on uncompacted thread, got %d", dreamCalls)
	}
	if lastDreamLogCount != 10 {
		t.Fatalf("expected 10 user-sourced daily log entries passed to AutoDream (excluding task_outcome), got %d", lastDreamLogCount)
	}
	sumDream1, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after dream #1: %v", err)
	}
	if !sumDream1.Through.IsZero() || !sumDream1.FlushedThrough.IsZero() {
		t.Fatalf("expected Through/FlushedThrough still zero after uncompacted AutoDream, got through=%v flushed=%v", sumDream1.Through, sumDream1.FlushedThrough)
	}
	if !sumDream1.ExtractedThrough.Equal(t10) || sumDream1.LastDreamAt.IsZero() || sumDream1.DreamThrough.IsZero() {
		t.Fatalf("unexpected watermarks after dream #1: %+v", sumDream1)
	}

	// Verify 12h DreamMinInterval gate: seed 10 new user-sourced entries 1h later.
	h.clk.Advance(time.Hour)
	seedNow := h.clk.Now().UTC()
	seedEntries := make([]state.MemoryLogEntry, 0, state.DreamMinLogEntries)
	for i := range state.DreamMinLogEntries {
		seedEntries = append(seedEntries, state.MemoryLogEntry{
			ID:            fmt.Sprintf("second-wave-%d", i),
			Section:       "Context",
			Text:          fmt.Sprintf("Second wave user fact #%d", i),
			About:         "the user",
			Provenance:    state.ProvenancePreCompactFlush,
			SourceInputID: fmt.Sprintf("in-wave2-%d", i),
			SourceText:    fmt.Sprintf("Second wave user fact #%d", i),
			CreatedAt:     seedNow,
			UpdatedAt:     seedNow,
		})
	}
	nextSeed := sumDream1
	nextSeed.UpdatedAt = seedNow.Add(time.Second)
	if _, err := h.store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
		ExpectedSummary: sumDream1,
		NextSummary:     nextSeed,
		DailyLogDate:    seedNow.Format("2006-01-02"),
		DailyLogEntries: seedEntries,
	}); err != nil {
		t.Fatalf("seed second wave logs: %v", err)
	}

	// Before 12h since LastDreamAt: Process must NOT dream, and NextConversationWake schedules LastDreamAt + 12h.
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process before 12h gate: %v", err)
	}
	if dreamCalls != 1 {
		t.Fatalf("expected AutoDream blocked before 12h DreamMinInterval, got %d calls", dreamCalls)
	}
	wakeDream2, err := h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake during 12h cooldown: %v", err)
	}
	wantDream2Wake := sumDream1.LastDreamAt.Add(state.DreamMinInterval)
	if !wakeDream2.Equal(wantDream2Wake) {
		t.Fatalf("NextConversationWake = %v, want %v (LastDreamAt + 12h)", wakeDream2, wantDream2Wake)
	}

	// Advance to LastDreamAt + 12h -> second AutoDream runs cleanly!
	h.clk.Advance(wantDream2Wake.Sub(h.clk.Now()))
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process at LastDreamAt + 12h: %v", err)
	}
	if dreamCalls != 2 {
		t.Fatalf("expected second AutoDream call after 12h interval, got %d", dreamCalls)
	}
}

func TestCompaction_ParkedWhenUserRoutingPendingAfterErrorTurn(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	h := newIdleTestHarness(t, start)

	// Build two normal turns so idle extraction is eligible after IdleExtractDelay.
	_, _ = h.sendTurn(ctx, "Remember my favorite coffee roaster is Heart.")
	h.clk.Advance(10 * time.Second)
	_, _ = h.sendTurn(ctx, "My flight is next Tuesday at 08:00.")

	// Simulate an outer model failure on turn 3: CompleteTurn consumes the inbox
	// (Pending() == false, Active == "") while intentionally leaving UserRouted < UserReceived
	// (UserRoutingPending() == true) per Nori's replay-on-next-user-input contract.
	h.clk.Advance(10 * time.Second)
	h.model.err = errors.New("task command requires a canonical task UUID")
	failID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("failed-turn-routing-pending")).String()
	if _, err := h.runner.HandleInput(ctx, Input{ID: failID, Message: "Update my task"}); err != nil {
		t.Fatalf("HandleInput failed turn: %v", err)
	}
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process failed turn: %v", err)
	}
	h.model.err = nil

	control, _, err := h.store.ConversationStatus(ctx)
	if err != nil {
		t.Fatalf("ConversationStatus: %v", err)
	}
	if control.Pending() || control.Active != "" {
		t.Fatalf("expected consumed idle control after failed turn, got pending=%v active=%q", control.Pending(), control.Active)
	}
	routingPending, err := h.store.UserRoutingPending(ctx)
	if err != nil || !routingPending {
		t.Fatalf("expected UserRoutingPending=true after error turn, got pending=%v err=%v", routingPending, err)
	}

	// Advance past IdleExtractDelay so extraction would otherwise be due immediately.
	h.clk.Advance(state.IdleExtractDelay + time.Minute)

	sumCalls := 0
	h.sum.handler = func(_ context.Context, _ int, _ map[string]any, _ agent.Request) (agent.Response, error) {
		sumCalls++
		return agent.Response{}, errors.New("summarizer must not be called while UserRoutingPending is true")
	}

	// NextConversationWake MUST park (return zero), never spin the worker lane.
	wakeAt, err := h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake: %v", err)
	}
	if !wakeAt.IsZero() {
		t.Fatalf("expected zero NextConversationWake while UserRoutingPending=true, got %v", wakeAt)
	}

	// Even if Process or compact or maybeDream is called directly, ZERO model calls may fire.
	for i := range 5 {
		if err := h.runner.Process(ctx); err != nil {
			t.Fatalf("Process iter %d: %v", i, err)
		}
		h.runner.compact(ctx)
		h.runner.maybeDream(ctx)
	}
	if sumCalls != 0 {
		t.Fatalf("expected 0 summarizer calls while UserRoutingPending=true, got %d", sumCalls)
	}

	// Once a subsequent user turn succeeds, UserRoutingPending clears and Process immediately
	// runs the queued idle extraction cleanly.
	h.sum.handler = func(_ context.Context, _ int, _ map[string]any, _ agent.Request) (agent.Response, error) {
		sumCalls++
		return agent.Response{Calls: []transcript.ToolCall{{
			Name: "flush_and_compact",
			Args: map[string]any{"core_upserts": []any{}, "daily_log_entries": []any{}, "summary_items": []any{}, "gist": ""},
		}}}, nil
	}
	h.clk.Advance(10 * time.Second)
	_, _ = h.sendTurn(ctx, "All good, carry on.")
	h.clk.Advance(state.IdleExtractDelay + time.Second)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process idle extract after recovery: %v", err)
	}
	if sumCalls != 1 {
		t.Fatalf("expected 1 extraction call after UserRoutingPending cleared, got %d", sumCalls)
	}
	wakeAfter, err := h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake after recovery: %v", err)
	}
	if !wakeAfter.IsZero() {
		t.Fatalf("expected zero NextConversationWake once extraction completed, got %v", wakeAfter)
	}
}
