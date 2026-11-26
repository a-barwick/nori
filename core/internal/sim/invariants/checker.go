package invariants

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/users"
)

type Violation struct {
	Invariant string
	UserID    string
	EntityID  string
	Message   string
}

func (v Violation) Error() string {
	var b strings.Builder
	b.WriteString(v.Invariant)
	if v.UserID != "" {
		b.WriteString(" [user=")
		b.WriteString(v.UserID)
		b.WriteString("]")
	}
	if v.EntityID != "" {
		b.WriteString(" [entity=")
		b.WriteString(v.EntityID)
		b.WriteString("]")
	}
	b.WriteString(": ")
	b.WriteString(v.Message)
	return b.String()
}

type Violations []Violation

func (vs Violations) Error() string {
	if len(vs) == 0 {
		return ""
	}
	if len(vs) == 1 {
		return "invariant violation: " + vs[0].Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d invariant violations:", len(vs))
	for _, v := range vs {
		b.WriteString("\n  - ")
		b.WriteString(v.Error())
	}
	return b.String()
}

type UserTarget struct {
	User        users.User
	Store       state.ConversationStore
	Permissions *permissions.Service
	Scheduler   *scheduler.Service
}

type Config struct {
	Backend *memstore.Backend

	Faults *memstore.FaultInjector

	Now func() time.Time

	CheckLeases bool
}

type Stats struct {
	SafetyChecks          int
	QuiescenceChecks      int
	TrackedRuns           int
	TrackedOperations     int
	TrackedCommands       int
	TrackedAutomations    int
	PausedStartViolations int
}

type userEntityKey struct {
	userID   string
	entityID string
}

type userRunOpKey struct {
	userID string
	runID  string
	opID   string
}

type runBaseline struct {
	revision     int64
	position     int64
	incorporated int64
	spent        float64
	subtaskSpent float64
	terminal     state.RunStatus
}

type opBaseline struct {
	evidenceRecorded bool
	id               string
	callID           string
	name             string
	effectClass      string
	outcome          string
	resultRef        string
	revision         int64
	attempt          int
	cleared          string
	actionDigest     string
}

type cmdBaseline struct {
	runID     string
	commandID string
	kind      state.CommandKind
	position  int64
	revision  int64
	delivered bool
}

type convBaseline struct {
	received     int64
	consumed     int64
	userReceived int64
	userRouted   int64
	fence        int64
}

type automationBaseline struct {
	generation int64
	cursor     *time.Time
}

type taskControlBaseline struct {
	fence         int64
	models        int
	tools         int
	checks        int
	position      int64
	revision      int64
	ownersByFence map[int64]string
}

type pausedStartEvent struct {
	userID string
	runID  string
	at     time.Time
}

type Checker struct {
	mu          sync.Mutex
	backend     *memstore.Backend
	faults      *memstore.FaultInjector
	nowFn       func() time.Time
	checkLeases bool

	safetyChecks     int
	quiescenceChecks int

	runs           map[userEntityKey]runBaseline
	taskControls   map[userEntityKey]taskControlBaseline
	eventPositions map[string]int64
	readMarks      map[string]state.ReadMark
	conversations  map[string]convBaseline
	operations     map[userRunOpKey]opBaseline
	commands       map[userEntityKey]cmdBaseline
	automations    map[userEntityKey]automationBaseline

	runOwners         map[string]string
	interactionOwners map[string]string
	automationOwners  map[string]string
	occurrenceOwners  map[string]string
	inputOwners       map[string]string

	memoryBaselines map[string]memoryBaseline

	pausedStarts     []pausedStartEvent
	stickyViolations []Violation
}

func New(cfg Config) *Checker {
	return &Checker{
		backend:           cfg.Backend,
		faults:            cfg.Faults,
		nowFn:             cfg.Now,
		checkLeases:       cfg.CheckLeases,
		runs:              map[userEntityKey]runBaseline{},
		taskControls:      map[userEntityKey]taskControlBaseline{},
		eventPositions:    map[string]int64{},
		readMarks:         map[string]state.ReadMark{},
		conversations:     map[string]convBaseline{},
		operations:        map[userRunOpKey]opBaseline{},
		commands:          map[userEntityKey]cmdBaseline{},
		automations:       map[userEntityKey]automationBaseline{},
		runOwners:         map[string]string{},
		interactionOwners: map[string]string{},
		automationOwners:  map[string]string{},
		occurrenceOwners:  map[string]string{},
		inputOwners:       map[string]string{},
		memoryBaselines:   map[string]memoryBaseline{},
	}
}

func (c *Checker) now() time.Time {
	if c.nowFn == nil {
		return time.Now().UTC()
	}
	return c.nowFn().UTC()
}

func (c *Checker) withoutFaults(fn func() error) error {
	if c.faults == nil {
		return fn()
	}
	wasEnabled := c.faults.Enabled()
	if wasEnabled {
		c.faults.SetEnabled(false)
		defer c.faults.SetEnabled(true)
	}
	return fn()
}

func (c *Checker) RecordCommandReceipt(userID string, receipt state.CommandReceipt) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.recordCommandReceiptLocked(userID, receipt, false); !ok {
		c.stickyViolations = append(c.stickyViolations, v)
		return Violations{v}
	}
	return nil
}

func (c *Checker) RecordCommandReceipts(userID string, receipts []state.CommandReceipt) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var vs Violations
	for _, receipt := range receipts {
		if v, ok := c.recordCommandReceiptLocked(userID, receipt, false); !ok {
			c.stickyViolations = append(c.stickyViolations, v)
			vs = append(vs, v)
		}
	}
	if len(vs) > 0 {
		return vs
	}
	return nil
}

func (c *Checker) recordCommandReceiptLocked(userID string, receipt state.CommandReceipt, checkDelivered bool) (Violation, bool) {
	cmd := receipt.Command
	if cmd.ID == "" {
		return Violation{
			Invariant: "command.receipt_valid",
			UserID:    userID,
			Message:   "command receipt has empty command ID",
		}, false
	}
	if receipt.Run.ID != "" && cmd.RunID != "" && receipt.Run.ID != cmd.RunID {
		return Violation{
			Invariant: "command.receipt_consistent",
			UserID:    userID,
			EntityID:  cmd.ID,
			Message:   fmt.Sprintf("receipt Run.ID %q does not match Command.RunID %q", receipt.Run.ID, cmd.RunID),
		}, false
	}
	runID := cmd.RunID
	if runID == "" {
		runID = receipt.Run.ID
	}
	key := userEntityKey{userID: userID, entityID: cmd.ID}
	if prev, exists := c.commands[key]; exists {
		if prev.runID != runID || prev.commandID != cmd.ID || prev.kind != cmd.Kind ||
			prev.position != cmd.Position || prev.revision != cmd.Revision {
			return Violation{
				Invariant: "command.receipt_idempotent",
				UserID:    userID,
				EntityID:  cmd.ID,
				Message: fmt.Sprintf(
					"retried command receipt changed: prior=(run=%s kind=%s pos=%d rev=%d) got=(run=%s kind=%s pos=%d rev=%d)",
					prev.runID, prev.kind, prev.position, prev.revision,
					runID, cmd.Kind, cmd.Position, cmd.Revision,
				),
			}, false
		}
		if checkDelivered {
			if prev.delivered && !cmd.Delivered {
				return Violation{
					Invariant: "command.delivered_monotonic",
					UserID:    userID,
					EntityID:  cmd.ID,
					Message:   "command delivered flag reverted from true to false",
				}, false
			}
			if cmd.Delivered {
				prev.delivered = true
				c.commands[key] = prev
			}
		}
		return Violation{}, true
	}
	delivered := false
	if checkDelivered {
		delivered = cmd.Delivered
	}
	c.commands[key] = cmdBaseline{
		runID:     runID,
		commandID: cmd.ID,
		kind:      cmd.Kind,
		position:  cmd.Position,
		revision:  cmd.Revision,
		delivered: delivered,
	}
	return Violation{}, true
}

func (c *Checker) RecordExecutionStart(userID, runID string, paused bool) {
	if !paused {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pausedStarts = append(c.pausedStarts, pausedStartEvent{
		userID: userID,
		runID:  runID,
		at:     c.now(),
	})
}

func (c *Checker) OnExecutionStart(userID, runID string) {
	if c.backend == nil {
		return
	}
	snap := c.backend.Snapshot()
	ctrl, ctrlVs := decodeControlFromSnapshot(snap)
	if len(ctrlVs) > 0 {
		c.mu.Lock()
		c.stickyViolations = append(c.stickyViolations, ctrlVs...)
		c.mu.Unlock()
		return
	}
	c.RecordExecutionStart(userID, runID, ctrl.Paused)
}

func (c *Checker) PausedStartViolations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pausedStarts)
}

func (c *Checker) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		SafetyChecks:          c.safetyChecks,
		QuiescenceChecks:      c.quiescenceChecks,
		TrackedRuns:           len(c.runs),
		TrackedOperations:     len(c.operations),
		TrackedCommands:       len(c.commands),
		TrackedAutomations:    len(c.automations),
		PausedStartViolations: len(c.pausedStarts),
	}
}

func sortUsers(in []UserTarget) []UserTarget {
	out := slices.Clone(in)
	slices.SortFunc(out, func(a, b UserTarget) int {
		return strings.Compare(a.User.ID, b.User.ID)
	})
	return out
}
