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
}

func (*fakeReplyModel) Name() string { return "fake-reply-model" }

func (m *fakeReplyModel) Respond(_ context.Context, req ReplyRequest) (Reply, error) {
	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("usage-reply-%d", m.seq)
	m.mu.Unlock()
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
	tasks := &Tasks{Store: store, Now: clk.Now}
	r := New(&fakeReplyModel{now: clk.Now}, nil, sum, tasks)
	r.Now = clk.Now
	r.Clock = Clock{Now: clk.Now, Location: time.UTC}
	r.SyncWork = true
	return &idleTestHarness{
		t:      t,
		clk:    clk,
		store:  db,
		runner: r,
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

func TestIdleExtract_ThreeTurnsAt20mLeavesVerbatimAndParks(t *testing.T) {
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

	// Send 3 completed turns (<= CompactKeep = 5).
	_, _ = h.sendTurn(ctx, "I prefer dark roast espresso.")
	h.clk.Advance(10 * time.Second)
	_, _ = h.sendTurn(ctx, "My dog's name is Mochi.")
	h.clk.Advance(10 * time.Second)
	_, t3 := h.sendTurn(ctx, "I live in Portland.")

	// NextConversationWake must return latestTurnAt + IdleExtractDelay (20m).
	wakeAt, err := h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake: %v", err)
	}
	wantWake := t3.Add(state.IdleExtractDelay)
	if !wakeAt.Equal(wantWake) {
		t.Fatalf("NextConversationWake = %v, want %v", wakeAt, wantWake)
	}

	// Before 20m elapses, Process/compact must NOT run extraction.
	h.clk.Advance(state.IdleExtractDelay - time.Second)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process before 20m: %v", err)
	}
	if h.sum.CallCount() != 0 {
		t.Fatalf("expected 0 summarizer calls before 20m, got %d", h.sum.CallCount())
	}

	// Advance to exactly +20m -> extractIdleMemory runs.
	h.clk.Advance(time.Second)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process at +20m: %v", err)
	}
	if h.sum.CallCount() != 1 {
		t.Fatalf("expected 1 summarizer call at +20m, got %d", h.sum.CallCount())
	}
	call := h.sum.Calls()[0]
	if extractOnly, _ := call.Payload["extract_only"].(bool); !extractOnly {
		t.Fatalf("expected extract_only=true in payload, got %#v", call.Payload["extract_only"])
	}
	sentTurns := decodePromptTurns(t, call.Payload)
	if len(sentTurns) != 3 {
		t.Fatalf("expected 3 turns in extract payload, got %d", len(sentTurns))
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
	if len(coreMems) != 3 {
		t.Fatalf("expected 3 core memories, got %d: %+v", len(coreMems), coreMems)
	}
	logs, err := h.store.MemoryLogs(ctx, "", 10)
	if err != nil {
		t.Fatalf("MemoryLogs: %v", err)
	}
	if len(logs) != 1 || len(logs[0].Entries) != 3 {
		t.Fatalf("expected 3 daily log entries, got %+v", logs)
	}

	// Verify ConversationSummary watermarks: ExtractedThrough == T3, Through == 0, FlushedThrough == 0.
	sum, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary: %v", err)
	}
	if !sum.ExtractedThrough.Equal(t3) {
		t.Fatalf("ExtractedThrough = %v, want %v", sum.ExtractedThrough, t3)
	}
	if !sum.Through.IsZero() || !sum.FlushedThrough.IsZero() || !sum.RefinementThrough.IsZero() {
		t.Fatalf("expected Through/FlushedThrough/RefinementThrough zero, got through=%v flushed=%v ref=%v", sum.Through, sum.FlushedThrough, sum.RefinementThrough)
	}
	if sum.Turns != 0 || sum.Gist != "" || len(sum.Items) != 0 || len(sum.Excerpts) != 0 {
		t.Fatalf("expected Working Memory untouched, got turns=%d gist=%q items=%d excerpts=%d", sum.Turns, sum.Gist, len(sum.Items), len(sum.Excerpts))
	}

	// All 3 turns must remain verbatim in readAttemptHistory.
	hist := h.runner.readAttemptHistory(ctx, time.Time{})
	if hist.err != nil {
		t.Fatalf("readAttemptHistory: %v", hist.err)
	}
	if hist.view.Summary != nil {
		t.Fatalf("expected nil Summary in readAttemptHistory while Through is zero, got %+v", hist.view.Summary)
	}
	if len(hist.view.Turns) != 3 {
		t.Fatalf("expected all 3 turns verbatim in readAttemptHistory, got %d", len(hist.view.Turns))
	}

	// NextConversationWake must be zero (parks cleanly).
	wakeAt, err = h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake after extract: %v", err)
	}
	if !wakeAt.IsZero() {
		t.Fatalf("expected zero NextConversationWake after extraction, got %v", wakeAt)
	}
}

func TestIdleExtract_Return10hLaterAnd6TurnFastFlushPreventsDoubleExtraction(t *testing.T) {
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

	// Session 1: Turns 1..3.
	in1, t1 := h.sendTurn(ctx, "Turn 1: I am allergic to peanuts.")
	h.clk.Advance(10 * time.Second)
	_, _ = h.sendTurn(ctx, "Turn 2: My sister lives in Tokyo.")
	h.clk.Advance(10 * time.Second)
	_, t3 := h.sendTurn(ctx, "Turn 3: I use Neovim for Go.")

	// Idle +20m -> extracts Turns 1..3.
	h.clk.Advance(state.IdleExtractDelay)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process idle extract #1: %v", err)
	}
	coreBefore, err := h.store.Memories(ctx)
	if err != nil || len(coreBefore) != 3 {
		t.Fatalf("expected 3 core memories after session 1 extract, got %d err=%v", len(coreBefore), err)
	}
	logsBefore, err := h.store.MemoryLogs(ctx, "", 10)
	if err != nil || len(logsBefore) != 1 || len(logsBefore[0].Entries) != 3 {
		t.Fatalf("expected 3 daily log entries after session 1 extract, got %+v err=%v", logsBefore, err)
	}

	// Return +10h later and send Turns 4..6 (6 total turns > CompactKeep=5).
	h.clk.Advance(10 * time.Hour)
	_, _ = h.sendTurn(ctx, "Turn 4: Let's plan a trip to Kyoto.")
	h.clk.Advance(10 * time.Second)
	_, _ = h.sendTurn(ctx, "Turn 5: Budget is $3000.")
	h.clk.Advance(10 * time.Second)
	_, t6 := h.sendTurn(ctx, "Turn 6: Prefer boutique hotels.")

	// NextConversationWake must schedule fast idle flush at T6 + 2m (NOT +20m).
	wakeAt, err := h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake after 6 turns: %v", err)
	}
	wantFlushWake := t6.Add(state.IdleFlushDelay)
	if !wakeAt.Equal(wantFlushWake) {
		t.Fatalf("NextConversationWake = %v, want %v (T6 + 2m)", wakeAt, wantFlushWake)
	}

	// Advance +2m -> flushAndCompact compacts Turn 1 into Summary.Items + Gist.
	h.clk.Advance(state.IdleFlushDelay)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process fast idle flush at +2m: %v", err)
	}
	if h.sum.CallCount() != 2 {
		t.Fatalf("expected 2 summarizer calls (extract #1 + compact Turn 1), got %d", h.sum.CallCount())
	}
	compactCall := h.sum.Calls()[1]
	if _, hasExtractOnly := compactCall.Payload["extract_only"]; hasExtractOnly {
		t.Fatalf("expected coupled flushAndCompact not to set extract_only, got %#v", compactCall.Payload["extract_only"])
	}
	compactTurns := decodePromptTurns(t, compactCall.Payload)
	if len(compactTurns) != 1 || !compactTurns[0].AlreadyExtracted {
		t.Fatalf("expected 1 turn with already_extracted=true in compact payload, got %+v", compactTurns)
	}

	// Verify Turn 1 WAS summarized into Working Memory, FlushedThrough=Through=T1, and ExtractedThrough stayed T3!
	sum, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after compact: %v", err)
	}
	if !sum.FlushedThrough.Equal(t1) || !sum.Through.Equal(t1) {
		t.Fatalf("expected FlushedThrough=Through=%v, got flushed=%v through=%v", t1, sum.FlushedThrough, sum.Through)
	}
	if !sum.ExtractedThrough.Equal(t3) {
		t.Fatalf("expected ExtractedThrough preserved at %v, got %v", t3, sum.ExtractedThrough)
	}
	if len(sum.Items) != 1 || sum.Items[0].InputID != in1 || !strings.Contains(sum.Items[0].Quote, "allergic to peanuts") {
		t.Fatalf("expected Turn 1 summarized in Summary.Items, got %+v", sum.Items)
	}
	if !strings.Contains(sum.Gist, "allergic to peanuts") {
		t.Fatalf("expected Turn 1 in Gist, got %q", sum.Gist)
	}

	// Verify NO duplicate Core memories or Daily Log entries were created even though the adversarial model emitted them!
	coreAfterCompact, err := h.store.Memories(ctx)
	if err != nil {
		t.Fatalf("Memories after compact: %v", err)
	}
	if len(coreAfterCompact) != 3 {
		t.Fatalf("expected still 3 core memories (no double-extraction of Turn 1), got %d: %+v", len(coreAfterCompact), coreAfterCompact)
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
	if totalLogs != 3 {
		t.Fatalf("expected still 3 daily log entries (no duplicate for Turn 1), got %d", totalLogs)
	}

	// Verify readAttemptHistory now sees Summary (Through=T1) + 5 verbatim turns (Turns 2..6).
	hist := h.runner.readAttemptHistory(ctx, time.Time{})
	if hist.err != nil {
		t.Fatalf("readAttemptHistory: %v", hist.err)
	}
	if hist.view.Summary == nil || !hist.view.Summary.Through.Equal(t1) {
		t.Fatalf("expected Summary Through=%v in readAttemptHistory, got %+v", t1, hist.view.Summary)
	}
	if len(hist.view.Turns) != 5 {
		t.Fatalf("expected 5 verbatim turns (Turns 2..6), got %d", len(hist.view.Turns))
	}

	// NextConversationWake must now schedule T6 + 20m for extracting Turns 4..6!
	wakeAt, err = h.runner.NextConversationWake(ctx)
	if err != nil {
		t.Fatalf("NextConversationWake after Turn 1 compact: %v", err)
	}
	wantExtract2Wake := t6.Add(state.IdleExtractDelay)
	if !wakeAt.Equal(wantExtract2Wake) {
		t.Fatalf("NextConversationWake = %v, want %v (T6 + 20m)", wakeAt, wantExtract2Wake)
	}

	// Advance remaining 18m (to T6 + 20m) -> extracts ONLY Turns 4..6, leaving Through=FlushedThrough=T1 unchanged.
	h.clk.Advance(state.IdleExtractDelay - state.IdleFlushDelay)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process idle extract #2: %v", err)
	}
	if h.sum.CallCount() != 3 {
		t.Fatalf("expected 3 summarizer calls total, got %d", h.sum.CallCount())
	}
	extract2Turns := decodePromptTurns(t, h.sum.Calls()[2].Payload)
	if len(extract2Turns) != 3 {
		t.Fatalf("expected only Turns 4..6 (3 turns) in extract #2, got %d", len(extract2Turns))
	}
	sum2, err := h.store.ConversationSummary(ctx)
	if err != nil {
		t.Fatalf("ConversationSummary after extract #2: %v", err)
	}
	if !sum2.ExtractedThrough.Equal(t6) {
		t.Fatalf("ExtractedThrough = %v, want %v", sum2.ExtractedThrough, t6)
	}
	if !sum2.FlushedThrough.Equal(t1) || !sum2.Through.Equal(t1) {
		t.Fatalf("expected Through/FlushedThrough unchanged at %v, got flushed=%v through=%v", t1, sum2.FlushedThrough, sum2.Through)
	}
	if len(sum2.Items) != 1 || sum2.Items[0].InputID != in1 {
		t.Fatalf("expected Summary.Items preserved across extract #2, got %+v", sum2.Items)
	}
	coreFinal, err := h.store.Memories(ctx)
	if err != nil || len(coreFinal) != 6 {
		t.Fatalf("expected 6 unique core memories (Turns 1..6), got %d err=%v", len(coreFinal), err)
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

	// Complete 3 turns and idle-extract them at +20m so ExtractedThrough == T3 > FlushedThrough (0).
	_, _ = h.sendTurn(ctx, "Turn 1")
	h.clk.Advance(10 * time.Second)
	_, _ = h.sendTurn(ctx, "Turn 2")
	h.clk.Advance(10 * time.Second)
	_, t3 := h.sendTurn(ctx, "Turn 3")

	h.clk.Advance(state.IdleExtractDelay)
	if err := h.runner.Process(ctx); err != nil {
		t.Fatalf("Process idle extract: %v", err)
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

	// Advance clock slightly and run idle AutoDream via Process -> maybeDream.
	h.clk.Advance(time.Minute)
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
