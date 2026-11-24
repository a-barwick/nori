package outer

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"nori/internal/conversation"
	"nori/internal/sim/env"
	"nori/internal/sim/memstore"
	"nori/internal/state"
)

// Config configures a multi-worker deterministic outer conversation Harness.
type Config struct {
	// UserID is recorded on simulation traces. Defaults to "sim-user".
	UserID string

	// UserFacts is injected into Runner.UserFacts. Defaults to "Simulation user."
	UserFacts string

	// TimeZone configures Runner.Clock.Location. Defaults to time.UTC.
	TimeZone *time.Location

	// Store is shared across all workers, simulating multiple API/worker pods
	// competing for the same user's conversation lease.
	Store state.ConversationStore

	// Optional infrastructure fault injectors paused during CheckOuterInvariants
	// and disabled during DrainConversation.
	StoreFaults *memstore.FaultInjector

	// Deterministic simulation primitives.
	Clock *env.Clock
	IDs   *env.IDGen
	RNG   *rand.Rand
	Trace *env.Trace

	// NumWorkers is the number of competing conversation.Runner instances to
	// construct. Defaults to 2 when <= 0.
	NumWorkers int

	// ReplyFaults overrides DefaultReplyFaultConfig when non-zero (unless
	// DisableFaults is true).
	ReplyFaults ReplyFaultConfig

	// ClassifierFaultProb and SummaryFaultProb configure failure rates for
	// FakeClassifier and FakeSummaryModel when DisableFaults is false.
	// Negative values disable faults; zero uses defaults (0.10 and 0.15).
	ClassifierFaultProb float64
	SummaryFaultProb    float64

	// DisableFaults disables all model faults on startup.
	DisableFaults bool
}

type acceptedInput struct {
	id   string
	text string
}

type publishedMessage struct {
	id        string
	turnID    string
	text      string
	status    string
	createdAt time.Time
}

type outerBaseline struct {
	initialized  bool
	fence        int64
	received     int64
	consumed     int64
	userReceived int64
	userRouted   int64
}

// Harness drives Nori's real conversation.Runner across multiple competing
// workers with deterministic virtual clocks, IDs, zero-sleep timers, and
// outer-loop invariant verification.
type Harness struct {
	UserID      string
	Store       state.ConversationStore
	StoreFaults *memstore.FaultInjector
	Clock       *env.Clock
	IDs         *env.IDGen
	RNG         *rand.Rand
	Trace       *env.Trace

	Workers       []*conversation.Runner
	Tasks         []*conversation.Tasks
	ReplyModels   []*FakeReplyModel
	Classifiers   []*FakeClassifier
	SummaryModels []*FakeSummaryModel
	Plans         *PlanCache

	tickers    []*workerTickerHub
	workerBusy []atomic.Bool

	mu                 sync.Mutex
	step               int
	acceptedInputs     []acceptedInput
	acceptedByID       map[string]acceptedInput
	publishedAssistant map[string]publishedMessage
	baseline           outerBaseline
}

// NewHarness constructs a deterministic multi-worker outer conversation Harness.
func NewHarness(cfg Config) *Harness {
	if cfg.Store == nil {
		panic("sim/outer: Config.Store is required")
	}
	if cfg.UserID == "" {
		cfg.UserID = "sim-user"
	}
	if cfg.UserFacts == "" {
		cfg.UserFacts = "Simulation user."
	}
	if cfg.TimeZone == nil {
		cfg.TimeZone = time.UTC
	}
	if cfg.Clock == nil {
		cfg.Clock = env.NewClock(time.Time{})
	}
	if cfg.RNG == nil {
		cfg.RNG = rand.New(rand.NewPCG(1, 2))
	}
	if cfg.IDs == nil {
		cfg.IDs = env.NewIDGen(rand.New(rand.NewPCG(cfg.RNG.Uint64(), cfg.RNG.Uint64())))
	}
	if cfg.Trace == nil {
		cfg.Trace = env.NewTrace(2048)
	}
	numWorkers := cfg.NumWorkers
	if numWorkers <= 0 {
		numWorkers = 2
	}

	replyFaults := cfg.ReplyFaults
	if !cfg.DisableFaults && replyFaults == (ReplyFaultConfig{}) {
		replyFaults = DefaultReplyFaultConfig()
	}
	classifierProb := cfg.ClassifierFaultProb
	if cfg.DisableFaults || classifierProb < 0 {
		classifierProb = 0
	} else if classifierProb == 0 {
		classifierProb = 0.10
	}
	summaryProb := cfg.SummaryFaultProb
	if cfg.DisableFaults || summaryProb < 0 {
		summaryProb = 0
	} else if summaryProb == 0 {
		summaryProb = 0.15
	}

	deriveRNG := func() *rand.Rand {
		return rand.New(rand.NewPCG(cfg.RNG.Uint64(), cfg.RNG.Uint64()))
	}

	plans := NewPlanCache()
	h := &Harness{
		UserID:             cfg.UserID,
		Store:              cfg.Store,
		StoreFaults:        cfg.StoreFaults,
		Clock:              cfg.Clock,
		IDs:                cfg.IDs,
		RNG:                cfg.RNG,
		Trace:              cfg.Trace,
		Workers:            make([]*conversation.Runner, numWorkers),
		Tasks:              make([]*conversation.Tasks, numWorkers),
		ReplyModels:        make([]*FakeReplyModel, numWorkers),
		Classifiers:        make([]*FakeClassifier, numWorkers),
		SummaryModels:      make([]*FakeSummaryModel, numWorkers),
		Plans:              plans,
		tickers:            make([]*workerTickerHub, numWorkers),
		workerBusy:         make([]atomic.Bool, numWorkers),
		acceptedByID:       make(map[string]acceptedInput),
		publishedAssistant: make(map[string]publishedMessage),
	}

	for i := range numWorkers {
		workerIDs := env.NewIDGen(deriveRNG())
		replyModel := NewFakeReplyModel(ReplyModelConfig{
			Name:          "sim-outer-model",
			WorkerIdx:     i,
			RNG:           deriveRNG(),
			Clock:         cfg.Clock,
			IDs:           env.NewIDGen(deriveRNG()),
			Faults:        replyFaults,
			DisableFaults: cfg.DisableFaults,
			Plans:         plans,
		})
		classifier := NewFakeClassifier(deriveRNG(), cfg.Clock, env.NewIDGen(deriveRNG()), classifierProb)
		if cfg.DisableFaults {
			classifier.SetFaultsEnabled(false)
		}
		summarizer := NewFakeSummaryModel("sim-summary-model", deriveRNG(), cfg.Clock, env.NewIDGen(deriveRNG()), summaryProb)
		if cfg.DisableFaults {
			summarizer.SetFaultsEnabled(false)
		}

		tasks := &conversation.Tasks{
			Store: cfg.Store,
			Now:   cfg.Clock.Now,
		}
		hub := newWorkerTickerHub(cfg.Clock)
		runner := conversation.New(replyModel, classifier, summarizer, tasks)
		runner.UserFacts = cfg.UserFacts
		runner.Clock = conversation.Clock{
			Location: cfg.TimeZone,
			Now:      cfg.Clock.Now,
		}
		runner.Now = hub.now
		runner.NewID = workerIDs.Next
		runner.After = hub.after
		runner.NewTicker = hub.newTicker
		runner.SyncWork = true

		h.Workers[i] = runner
		h.Tasks[i] = tasks
		h.ReplyModels[i] = replyModel
		h.Classifiers[i] = classifier
		h.SummaryModels[i] = summarizer
		h.tickers[i] = hub
	}

	return h
}

// SetStep sets the logical step number recorded on subsequent trace entries.
func (h *Harness) SetStep(step int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.step = step
}

// Record appends a deterministic event to the harness trace.
func (h *Harness) Record(actor, action, detail string) {
	h.mu.Lock()
	step := h.step
	h.mu.Unlock()
	h.Trace.Record(step, h.Clock.Now(), h.UserID, actor, action, detail)
}

// SetModelFaultsEnabled toggles faults on all workers' FakeReplyModel,
// FakeClassifier, and FakeSummaryModel instances.
func (h *Harness) SetModelFaultsEnabled(enabled bool) {
	for _, m := range h.ReplyModels {
		m.SetFaultsEnabled(enabled)
	}
	for _, c := range h.Classifiers {
		c.SetFaultsEnabled(enabled)
	}
	for _, s := range h.SummaryModels {
		s.SetFaultsEnabled(enabled)
	}
}

// SetAllFaultsEnabled toggles faults across all outer models as well as any
// wired memstore.FaultInjector.
func (h *Harness) SetAllFaultsEnabled(enabled bool) {
	h.SetModelFaultsEnabled(enabled)
	if h.StoreFaults != nil {
		h.StoreFaults.SetEnabled(enabled)
	}
}

// SetOnBeforeRespond installs or clears the same mid-flight hook on every
// worker's FakeReplyModel.
func (h *Harness) SetOnBeforeRespond(fn BeforeRespondHook) {
	for _, m := range h.ReplyModels {
		m.SetOnBeforeRespond(fn)
	}
}

// SetOnBeforeCompaction installs or clears the same mid-flight hook on every
// worker's FakeSummaryModel / FakeCompactionModel.
func (h *Harness) SetOnBeforeCompaction(fn CompactionBeforeRespondHook) {
	for _, s := range h.SummaryModels {
		s.SetOnBeforeRespond(fn)
	}
}

// EnsureIntroduction enqueues the initial conversation introduction via Worker 0.
func (h *Harness) EnsureIntroduction(ctx context.Context) error {
	err := h.Workers[0].EnsureIntroduction(ctx)
	h.Record("outer", "ensure_intro", fmt.Sprintf("err=%v", err))
	return err
}

// AcceptInput accepts a new user message with a deterministic UUID from h.IDs.
func (h *Harness) AcceptInput(ctx context.Context, text string) (string, error) {
	id := h.IDs.Next()
	err := h.AcceptInputWithID(ctx, id, text)
	return id, err
}

// AcceptInputWithID submits a user message with a caller-supplied UUID,
// enabling idempotent replay and ErrConversationConflict testing.
func (h *Harness) AcceptInputWithID(ctx context.Context, id, text string) error {
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("message ID must be a UUID")
	}
	if strings.TrimSpace(text) == "" || len(text) > state.MaxMessageBytes {
		return fmt.Errorf("message must contain 1–%d bytes", state.MaxMessageBytes)
	}
	err := h.Store.AcceptConversationInput(ctx, id, text, nil)
	committed := err == nil
	if errors.Is(err, memstore.ErrCommitThenError) {
		committed = h.userMessageExistsFaultFree(ctx, id)
	}
	if committed {
		h.recordAcceptedInput(id, text)
	}
	h.Record("user", "accept_input", fmt.Sprintf("id=%s len=%d err=%v", id, len(text), err))
	return err
}

func (h *Harness) recordAcceptedInput(id, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.acceptedByID[id]; exists {
		return
	}
	item := acceptedInput{id: id, text: text}
	h.acceptedByID[id] = item
	h.acceptedInputs = append(h.acceptedInputs, item)
}

func (h *Harness) userMessageExistsFaultFree(ctx context.Context, inputID string) bool {
	if h.StoreFaults != nil && h.StoreFaults.Enabled() {
		h.StoreFaults.SetEnabled(false)
		defer h.StoreFaults.SetEnabled(true)
	}
	_, err := h.Store.ReadConversationMessage(ctx, inputID)
	return err == nil
}

// ProcessWorker runs one Runner.Process pass on worker workerIdx and waits
// deterministically for that worker's background lease/compaction ticker
// goroutines to exit cleanly before returning.
func (h *Harness) ProcessWorker(ctx context.Context, workerIdx int) error {
	if workerIdx < 0 || workerIdx >= len(h.Workers) {
		return fmt.Errorf("sim/outer: invalid worker index %d (numWorkers=%d)", workerIdx, len(h.Workers))
	}
	owned := h.workerBusy[workerIdx].CompareAndSwap(false, true)
	if owned {
		defer h.workerBusy[workerIdx].Store(false)
	}

	err := h.Workers[workerIdx].Process(ctx)
	if owned {
		h.tickers[workerIdx].waitAll()
	}
	h.Record(fmt.Sprintf("worker-%d", workerIdx), "process", fmt.Sprintf("err=%v", err))
	return err
}

// Cancel cancels an active turn or the head queued input ID.
func (h *Harness) Cancel(ctx context.Context, turnID string) error {
	err := h.Store.CancelConversation(ctx, turnID)
	h.Record("user", "cancel", fmt.Sprintf("turn=%s err=%v", turnID, err))
	return err
}

// Session returns a conversation.Session snapshot via Worker 0.
func (h *Harness) Session(ctx context.Context) (conversation.Session, error) {
	return h.Workers[0].Snapshot(ctx)
}

// TickLeaseWatcher deterministically delivers a single tick to workerIdx's
// currently active lease watcher goroutine (250ms) and waits synchronously
// until that watcher tick completes. When expectCancel is true, it waits until
// the watcher cancels its context and stops.
func (h *Harness) TickLeaseWatcher(ctx context.Context, workerIdx int, expectCancel bool) bool {
	if workerIdx < 0 || workerIdx >= len(h.tickers) || !h.workerBusy[workerIdx].Load() {
		return false
	}
	return h.tickers[workerIdx].tickActive(ctx, expectCancel)
}

// TickCompactionWatcher deterministically delivers a single tick to workerIdx's
// currently active compaction/AutoDream watcher goroutine (500ms) so mid-flush
// UserRoutingPending preemption fires deterministically without wall-clock sleeps.
// When expectCancel is true, it blocks until the watcher cancels watchCtx and exits.
func (h *Harness) TickCompactionWatcher(ctx context.Context, workerIdx int, expectCancel bool) bool {
	if workerIdx < 0 || workerIdx >= len(h.tickers) || !h.workerBusy[workerIdx].Load() {
		return false
	}
	return h.tickers[workerIdx].tickCompaction(ctx, expectCancel)
}

// AcceptedInputs returns a copy of all user input IDs accepted through this
// harness in acceptance order.
func (h *Harness) AcceptedInputs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.acceptedInputs))
	for i, in := range h.acceptedInputs {
		out[i] = in.id
	}
	return out
}

// DrainConversation disables model and infrastructure faults and drives
// Worker 0 until the conversation is completely idle and all accepted user
// inputs have been routed (!c.Pending() && c.Active == "" && !UserRoutingPending).
//
// If the last turn before drain ended in an injected model error (leaving
// !c.Pending() while UserRoutingPending remains true per Nori's replay-on-next-
// user-input contract), DrainConversation submits one deterministic retry user
// message and processes it so all unrouted inputs resolve cleanly.
func (h *Harness) DrainConversation(ctx context.Context) error {
	h.SetAllFaultsEnabled(false)
	h.SetOnBeforeRespond(nil)
	h.SetOnBeforeCompaction(nil)
	for _, s := range h.SummaryModels {
		s.SetFaultMode(CompactionFaultNone)
	}

	const maxIterations = 128
	for iter := range maxIterations {
		c, _, err := h.Store.ConversationStatus(ctx)
		if err != nil {
			return fmt.Errorf("sim/outer: drain read status: %w", err)
		}

		if c.Pending() || c.Active != "" {
			if c.Owner != "" && c.LeaseUntil.After(h.Clock.Now()) {
				h.Clock.Set(c.LeaseUntil.Add(time.Second))
			}
			err := h.ProcessWorker(ctx, 0)
			if err != nil && !errors.Is(err, state.ErrConversationBusy) && !errors.Is(err, state.ErrConversationStale) {
				return fmt.Errorf("sim/outer: drain process (iter=%d): %w", iter, err)
			}
			h.Clock.Advance(time.Millisecond)
			continue
		}

		routingPending, err := h.Store.UserRoutingPending(ctx)
		if err != nil {
			return fmt.Errorf("sim/outer: drain check routing pending: %w", err)
		}
		if routingPending {
			if _, err := h.AcceptInput(ctx, "Continue and resolve my previous message."); err != nil {
				return fmt.Errorf("sim/outer: drain submit retry input: %w", err)
			}
			h.Clock.Advance(time.Millisecond)
			continue
		}

		// One idle pass allows any eligible compaction to finish cleanly.
		if err := h.ProcessWorker(ctx, 0); err != nil {
			return fmt.Errorf("sim/outer: drain idle pass: %w", err)
		}

		c, _, err = h.Store.ConversationStatus(ctx)
		if err != nil {
			return fmt.Errorf("sim/outer: drain final status: %w", err)
		}
		routingPending, err = h.Store.UserRoutingPending(ctx)
		if err != nil {
			return fmt.Errorf("sim/outer: drain final routing check: %w", err)
		}
		if !c.Pending() && c.Active == "" && !routingPending {
			h.Record("drain", "complete", fmt.Sprintf("received=%d consumed=%d user_routed=%d", c.Received, c.Consumed, c.UserRouted))
			return nil
		}
	}

	c, _, _ := h.Store.ConversationStatus(ctx)
	return fmt.Errorf("sim/outer: drain did not converge after %d iterations (received=%d consumed=%d user_received=%d user_routed=%d active=%q)",
		maxIterations, c.Received, c.Consumed, c.UserReceived, c.UserRouted, c.Active)
}

// CheckOuterInvariants verifies all outer conversation safety, monotonicity,
// publication uniqueness, input tracking, trace, and fence invariants without
// consuming store fault RNG draws.
func (h *Harness) CheckOuterInvariants(ctx context.Context) error {
	if h.StoreFaults != nil && h.StoreFaults.Enabled() {
		h.StoreFaults.SetEnabled(false)
		defer h.StoreFaults.SetEnabled(true)
	}

	c, activeTurn, err := h.Store.ConversationStatus(ctx)
	if err != nil {
		return fmt.Errorf("outer invariant: ConversationStatus: %w", err)
	}

	messages, err := h.readAllMessages(ctx)
	if err != nil {
		return fmt.Errorf("outer invariant: ConversationMessages: %w", err)
	}

	traces, err := h.Store.ConversationTraces(ctx)
	if err != nil {
		return fmt.Errorf("outer invariant: ConversationTraces: %w", err)
	}

	summary, err := h.Store.ConversationSummary(ctx)
	if err != nil {
		return fmt.Errorf("outer invariant: ConversationSummary: %w", err)
	}

	var violations []string
	fail := func(format string, args ...any) {
		violations = append(violations, fmt.Sprintf(format, args...))
	}

	// 1. Control counter bounds and relationships.
	if c.Received < 0 || c.Consumed < 0 || c.UserReceived < 0 || c.UserRouted < 0 || c.Fence < 0 {
		fail("negative control counters: received=%d consumed=%d user_received=%d user_routed=%d fence=%d",
			c.Received, c.Consumed, c.UserReceived, c.UserRouted, c.Fence)
	}
	if c.Consumed > c.Received {
		fail("consumed (%d) exceeds received (%d)", c.Consumed, c.Received)
	}
	if c.UserReceived > c.Received {
		fail("user_received (%d) exceeds received (%d)", c.UserReceived, c.Received)
	}
	if c.UserRouted > c.UserReceived {
		fail("user_routed (%d) exceeds user_received (%d)", c.UserRouted, c.UserReceived)
	}
	if c.UserRouted > c.Consumed {
		fail("user_routed (%d) exceeds consumed (%d)", c.UserRouted, c.Consumed)
	}

	// 2. Monotonic control progression across checks.
	h.mu.Lock()
	if h.baseline.initialized {
		if c.Fence < h.baseline.fence {
			fail("fence regressed: %d < %d", c.Fence, h.baseline.fence)
		}
		if c.Received < h.baseline.received {
			fail("received regressed: %d < %d", c.Received, h.baseline.received)
		}
		if c.Consumed < h.baseline.consumed {
			fail("consumed regressed: %d < %d", c.Consumed, h.baseline.consumed)
		}
		if c.UserReceived < h.baseline.userReceived {
			fail("user_received regressed: %d < %d", c.UserReceived, h.baseline.userReceived)
		}
		if c.UserRouted < h.baseline.userRouted {
			fail("user_routed regressed: %d < %d", c.UserRouted, h.baseline.userRouted)
		}
	}
	h.baseline = outerBaseline{
		initialized:  true,
		fence:        c.Fence,
		received:     c.Received,
		consumed:     c.Consumed,
		userReceived: c.UserReceived,
		userRouted:   c.UserRouted,
	}
	acceptedSnapshot := append([]acceptedInput(nil), h.acceptedInputs...)
	h.mu.Unlock()

	// 3. Active turn & lease fence consistency.
	if c.Active != "" {
		if activeTurn == nil {
			fail("control.Active=%q but active turn document is nil", c.Active)
		} else {
			if activeTurn.ID != c.Active {
				fail("active turn ID mismatch: control=%q turn=%q", c.Active, activeTurn.ID)
			}
			if activeTurn.Status != "running" {
				fail("active turn %q has non-running status %q", activeTurn.ID, activeTurn.Status)
			}
			if activeTurn.Through <= c.Consumed || activeTurn.Through > c.Received {
				fail("active turn %q through=%d out of range (%d, %d]", activeTurn.ID, activeTurn.Through, c.Consumed, c.Received)
			}
			if activeTurn.UserThrough > c.UserReceived {
				fail("active turn %q user_through=%d exceeds user_received=%d", activeTurn.ID, activeTurn.UserThrough, c.UserReceived)
			}
			if activeTurn.Restarts > state.MaxTurnRestarts {
				fail("active turn %q restarts=%d exceeds MaxTurnRestarts=%d", activeTurn.ID, activeTurn.Restarts, state.MaxTurnRestarts)
			}
		}
	} else {
		if c.Owner != "" {
			fail("control.Active is empty but control.Owner=%q", c.Owner)
		}
		if !c.LeaseUntil.IsZero() {
			fail("control.Active is empty but control.LeaseUntil=%s is non-zero", c.LeaseUntil)
		}
	}
	if c.Owner != "" {
		if c.Fence <= 0 {
			fail("control.Owner=%q set with non-positive fence=%d", c.Owner, c.Fence)
		}
		if c.LeaseUntil.IsZero() {
			fail("control.Owner=%q set with zero LeaseUntil", c.Owner)
		}
	}

	// 4. Published assistant messages: monotonic timestamps, unique turn IDs,
	// immutable once published, and never belonging to a still-active turn.
	seenMsgIDs := make(map[string]bool, len(messages))
	seenAssistantTurns := make(map[string]publishedMessage)
	storedUsers := make(map[string]state.Message)
	var lastAssistantAt time.Time
	for _, m := range messages {
		if seenMsgIDs[m.ID] {
			fail("duplicate message ID %q", m.ID)
		}
		seenMsgIDs[m.ID] = true

		if m.Role == "user" {
			storedUsers[m.ID] = m
			continue
		}
		if m.Role == "assistant" && m.TurnID != "" {
			expectedID := state.AssistantMessageID(m.TurnID)
			if m.ID != expectedID {
				fail("assistant message ID %q does not match expected %q for turn %q", m.ID, expectedID, m.TurnID)
			}
			if _, dup := seenAssistantTurns[m.TurnID]; dup {
				fail("duplicate assistant publication for turn %q", m.TurnID)
			}
			if c.Active != "" && m.TurnID == c.Active {
				fail("turn %q is still active but already has a published assistant message %q", c.Active, m.ID)
			}
			if !lastAssistantAt.IsZero() && m.CreatedAt.Before(lastAssistantAt) {
				fail("assistant message %q created_at %s precedes earlier assistant message %s",
					m.ID, m.CreatedAt.Format(time.RFC3339Nano), lastAssistantAt.Format(time.RFC3339Nano))
			}
			lastAssistantAt = m.CreatedAt
			seenAssistantTurns[m.TurnID] = publishedMessage{
				id:        m.ID,
				turnID:    m.TurnID,
				text:      m.Text,
				status:    m.Status,
				createdAt: m.CreatedAt.UTC(),
			}
		}
	}

	h.mu.Lock()
	for turnID, prev := range h.publishedAssistant {
		curr, ok := seenAssistantTurns[turnID]
		if !ok {
			fail("previously published assistant message %q (turn %q) disappeared", prev.id, turnID)
			continue
		}
		if curr.id != prev.id || curr.text != prev.text || curr.status != prev.status || !curr.createdAt.Equal(prev.createdAt) {
			fail("published assistant message for turn %q mutated: was %+v now %+v", turnID, prev, curr)
		}
	}
	for turnID, curr := range seenAssistantTurns {
		h.publishedAssistant[turnID] = curr
	}
	h.mu.Unlock()

	// 5. Accepted user inputs tracked vs stored user messages and consumption.
	for _, in := range acceptedSnapshot {
		msgID := state.UserMessageID(in.id)
		msg, ok := storedUsers[msgID]
		if !ok {
			fail("accepted user input %q missing stored message %q", in.id, msgID)
			continue
		}
		if msg.Text != in.text {
			fail("accepted user input %q text mismatch: expected %q got %q", in.id, in.text, msg.Text)
		}
	}
	if !c.Pending() && c.Active == "" {
		if c.Consumed != c.Received {
			fail("idle conversation has consumed=%d != received=%d", c.Consumed, c.Received)
		}
		if c.Consumed < int64(len(acceptedSnapshot)) {
			fail("idle conversation consumed=%d < acceptedInputs=%d", c.Consumed, len(acceptedSnapshot))
		}
	}

	// 6. Trace / attempt invariants.
	completedAttemptsByTurn := make(map[string]string)
	for _, a := range traces {
		if a.ID == "" || a.TurnID == "" {
			fail("trace attempt missing ID or TurnID: %+v", a)
		}
		if a.Number < 1 {
			fail("trace attempt %q has invalid number %d", a.ID, a.Number)
		}
		switch a.Status {
		case "running":
		case "completed", "error", "superseded", "cancelled":
			if a.FinishedAt == nil {
				fail("terminal trace attempt %q (status=%q) missing finished_at", a.ID, a.Status)
			}
		default:
			fail("trace attempt %q has unknown status %q", a.ID, a.Status)
		}
		if a.Status == "completed" {
			if other, dup := completedAttemptsByTurn[a.TurnID]; dup && other != a.ID {
				fail("turn %q has multiple completed attempts: %q and %q", a.TurnID, other, a.ID)
			}
			completedAttemptsByTurn[a.TurnID] = a.ID
		}
	}

	// 7. Compaction summary consistency.
	if summary.Through.IsZero() {
		if !summary.FlushedThrough.IsZero() || !summary.RefinementThrough.IsZero() {
			fail("summary through is zero while flushed_through=%s refinement_through=%s",
				summary.FlushedThrough.Format(time.RFC3339Nano), summary.RefinementThrough.Format(time.RFC3339Nano))
		}
	} else {
		if summary.Turns < 0 {
			fail("summary has negative turns=%d", summary.Turns)
		}
		if len(summary.Items) > state.MaxSummaryItems {
			fail("summary items (%d) exceed MaxSummaryItems (%d)", len(summary.Items), state.MaxSummaryItems)
		}
		if !summary.FlushedThrough.IsZero() && summary.FlushedThrough.After(summary.Through) {
			fail("summary flushed_through (%s) exceeds through (%s)",
				summary.FlushedThrough.Format(time.RFC3339Nano), summary.Through.Format(time.RFC3339Nano))
		}
		if summary.EffectiveFlushedThrough().After(summary.Through) {
			fail("summary effective_flushed_through (%s) exceeds through (%s)",
				summary.EffectiveFlushedThrough().Format(time.RFC3339Nano), summary.Through.Format(time.RFC3339Nano))
		}
		if !summary.RefinementThrough.IsZero() && summary.RefinementThrough.After(summary.Through) {
			fail("summary refinement_through (%s) exceeds through (%s)",
				summary.RefinementThrough.Format(time.RFC3339Nano), summary.Through.Format(time.RFC3339Nano))
		}
	}
	if summary.EffectiveExtractedThrough().Before(summary.EffectiveFlushedThrough()) {
		fail("summary effective_extracted_through (%s) precedes effective_flushed_through (%s)",
			summary.EffectiveExtractedThrough().Format(time.RFC3339Nano), summary.EffectiveFlushedThrough().Format(time.RFC3339Nano))
	}
	if !summary.ExtractedThrough.IsZero() && summary.ExtractedThrough.Before(summary.EffectiveFlushedThrough()) {
		fail("summary extracted_through (%s) precedes effective_flushed_through (%s)",
			summary.ExtractedThrough.Format(time.RFC3339Nano), summary.EffectiveFlushedThrough().Format(time.RFC3339Nano))
	}

	if len(violations) > 0 {
		return fmt.Errorf("outer invariant violations (%d):\n  - %s", len(violations), strings.Join(violations, "\n  - "))
	}
	return nil
}

func (h *Harness) readAllMessages(ctx context.Context) ([]state.Message, error) {
	var pages [][]state.Message
	cursor := ""
	seenCursors := make(map[string]bool)
	for {
		page, next, err := h.Store.ConversationMessages(ctx, cursor)
		if err != nil {
			return nil, err
		}
		pages = append(pages, page)
		if next == "" {
			break
		}
		if seenCursors[next] {
			return nil, fmt.Errorf("pagination cursor loop at %q", next)
		}
		seenCursors[next] = true
		cursor = next
	}
	// Pages are fetched newest-page-first, with each page already in chronological order.
	slices.Reverse(pages)
	var all []state.Message
	for _, p := range pages {
		all = append(all, p...)
	}
	return all, nil
}

// --- Deterministic per-worker timer & ticker hub ---

type simTicker struct {
	id        int64
	interval  time.Duration
	ch        chan time.Time
	stopped   chan struct{}
	isStopped bool
	lastRenew time.Time
	stopOnce  sync.Once
}

type workerTickerHub struct {
	clock    *env.Clock
	mu       sync.Mutex
	cond     *sync.Cond
	nextID   int64
	active   []*simTicker
	nowCalls int64
}

func newWorkerTickerHub(clock *env.Clock) *workerTickerHub {
	h := &workerTickerHub{clock: clock}
	h.cond = sync.NewCond(&h.mu)
	return h
}

func (h *workerTickerHub) now() time.Time {
	t := h.clock.Now()
	h.mu.Lock()
	h.nowCalls++
	h.cond.Broadcast()
	h.mu.Unlock()
	return t
}

// after advances the virtual clock immediately and returns a pre-filled channel
// so Runner.wait (restart quiet-window backoff) never sleeps on wall-clock time.
func (h *workerTickerHub) after(d time.Duration) <-chan time.Time {
	if d > 0 {
		h.clock.Advance(d)
	}
	ch := make(chan time.Time, 1)
	ch <- h.clock.Now()
	return ch
}

func (h *workerTickerHub) newTicker(interval time.Duration) (<-chan time.Time, func()) {
	h.mu.Lock()
	h.nextID++
	t := &simTicker{
		id:        h.nextID,
		interval:  interval,
		ch:        make(chan time.Time),
		stopped:   make(chan struct{}),
		lastRenew: h.clock.Now(),
	}
	h.active = append(h.active, t)
	h.cond.Broadcast()
	h.mu.Unlock()

	stop := func() {
		t.stopOnce.Do(func() {
			h.mu.Lock()
			t.isStopped = true
			for i, cand := range h.active {
				if cand.id == t.id {
					h.active = append(h.active[:i], h.active[i+1:]...)
					break
				}
			}
			close(t.stopped)
			h.cond.Broadcast()
			h.mu.Unlock()
		})
	}
	return t.ch, stop
}

func (h *workerTickerHub) waitAll() {
	h.mu.Lock()
	for len(h.active) > 0 {
		h.cond.Wait()
	}
	h.mu.Unlock()
}

func (h *workerTickerHub) latestTickerByIntervalLocked(interval time.Duration) *simTicker {
	for i := len(h.active) - 1; i >= 0; i-- {
		if h.active[i].interval == interval {
			return h.active[i]
		}
	}
	return nil
}

func (h *workerTickerHub) latestLeaseTickerLocked() *simTicker {
	return h.latestTickerByIntervalLocked(250 * time.Millisecond)
}

func (h *workerTickerHub) latestCompactionTickerLocked() *simTicker {
	return h.latestTickerByIntervalLocked(500 * time.Millisecond)
}

// tickCompaction synchronously delivers one virtual tick to the most recently
// registered active compaction/AutoDream watcher ticker (500ms interval).
// When expectCancel is true, it waits until UserRoutingPending causes the
// watcher to cancel watchCtx and stop cleanly.
func (h *workerTickerHub) tickCompaction(ctx context.Context, expectCancel bool) bool {
	stopCancel := context.AfterFunc(ctx, func() {
		h.mu.Lock()
		h.cond.Broadcast()
		h.mu.Unlock()
	})
	defer stopCancel()

	// Wait for compact/dreamConsolidate watcher goroutine to reach NewTicker(500ms).
	h.mu.Lock()
	t := h.latestCompactionTickerLocked()
	for t == nil {
		if ctx.Err() != nil {
			h.mu.Unlock()
			return false
		}
		h.cond.Wait()
		t = h.latestCompactionTickerLocked()
	}
	now := h.clock.Now()
	h.mu.Unlock()

	select {
	case t.ch <- now:
	case <-t.stopped:
		return expectCancel
	case <-ctx.Done():
		return false
	}

	if expectCancel {
		h.mu.Lock()
		defer h.mu.Unlock()
		for !t.isStopped && ctx.Err() == nil {
			h.cond.Wait()
		}
		return t.isStopped
	}
	return true
}

// tickActive synchronously delivers one virtual tick to the most recently
// registered active lease watcher ticker (250ms interval) and waits for that
// tick iteration to finish (or for the ticker to stop when expectCancel is true).
func (h *workerTickerHub) tickActive(ctx context.Context, expectCancel bool) bool {
	stopCancel := context.AfterFunc(ctx, func() {
		h.mu.Lock()
		h.cond.Broadcast()
		h.mu.Unlock()
	})
	defer stopCancel()

	// Wait for watchLease goroutine to reach NewTicker(250ms) if it was just spawned.
	h.mu.Lock()
	t := h.latestLeaseTickerLocked()
	for t == nil {
		if ctx.Err() != nil {
			h.mu.Unlock()
			return false
		}
		h.cond.Wait()
		t = h.latestLeaseTickerLocked()
	}
	beforeNow := h.nowCalls
	now := h.clock.Now()
	willRenew := now.Sub(t.lastRenew) >= 10*time.Second
	h.mu.Unlock()

	select {
	case t.ch <- now:
	case <-t.stopped:
		return false
	case <-ctx.Done():
		return false
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if expectCancel {
		for !t.isStopped && ctx.Err() == nil {
			h.cond.Wait()
		}
		return t.isStopped
	}
	expectedDelta := int64(1)
	if willRenew {
		expectedDelta = 2
	}
	target := beforeNow + expectedDelta
	for !t.isStopped && h.nowCalls < target && ctx.Err() == nil {
		h.cond.Wait()
	}
	if !t.isStopped && willRenew {
		t.lastRenew = now
	}
	return true
}
