package state

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type CommandReceipt struct {
	Command Command `json:"command"`
	Run     Run     `json:"run"`
}

// CommandPage scans the delivery outbox in stable ID order. Next survives
// acknowledgements deleting records from the pending set during the scan.
type CommandPage struct {
	Commands []Command
	Next     string
}

// Command and its delivery intent are committed in the acceptance transaction.
// ID is supplied by the caller and is stable across HTTP and relay retries.
type CommandKind string

const (
	CommandStart  CommandKind = "start"
	CommandSteer  CommandKind = "steer"
	CommandAnswer CommandKind = "answer"
	CommandStop   CommandKind = "stop"
	CommandHold   CommandKind = "hold"
	// CommandSubtaskResult delivers finished subtasks to the parent that asked for them. Only the transaction that
	// saves a subtask's outcome creates one; it never passes through Prepare, because a pause or the command limit must
	// not lose a result.
	CommandSubtaskResult CommandKind = "subtask_result"
)

type Command struct {
	Interrupt      bool         `json:"interrupt,omitempty" firestore:"interrupt,omitempty"`
	QuestionID     string       `json:"question_id,omitempty" firestore:"question_id,omitempty"`
	SourceInputIDs []string     `json:"source_input_ids,omitempty" firestore:"source_input_ids,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty" firestore:"attachments,omitempty"`
	Revises        string       `json:"revises,omitempty" firestore:"revises,omitempty"`
	// Origin is recorded on the task a start command creates.
	Origin *TaskOrigin `json:"origin,omitempty" firestore:"origin,omitempty"`
	// BudgetUSD lowers a started task's spending ceiling, for example to an automation's budget. It never raises
	// the deployment's ceiling.
	BudgetUSD float64     `json:"budget_usd,omitempty" firestore:"budget_usd,omitempty"`
	ID        string      `json:"id" firestore:"id"`
	RunID     string      `json:"run_id" firestore:"run_id"`
	ParentID  string      `json:"parent_id,omitempty" firestore:"parent_id,omitempty"`
	Kind      CommandKind `json:"kind" firestore:"kind"`
	Text      string      `json:"text" firestore:"text"`
	Position  int64       `json:"position" firestore:"position"`
	Revision  int64       `json:"revision" firestore:"revision"`
	Delivered bool        `json:"delivered" firestore:"delivered"`
	CreatedAt time.Time   `json:"created_at" firestore:"created_at"`
}

type TaskSnapshot struct {
	Run      Run
	Commands []Command
}

// MaxContinuationBudgetUSD bounds the budget a user can give an explicit continuation.
const MaxContinuationBudgetUSD = 50.0

const (
	MaxCommandBatch = 32
	// MaxTaskCommands bounds the commands one task accepts, other than a stop.
	MaxTaskCommands     = 32
	MaxTaskHolds        = 32
	MaxTaskHoldBytes    = 32 << 10
	PendingCommandLimit = 100
)

func SameCommand(a, b Command) bool {
	return a.RunID == b.RunID && a.Kind == b.Kind && a.Text == b.Text && a.QuestionID == b.QuestionID &&
		a.Interrupt == b.Interrupt && a.ParentID == b.ParentID && slices.Equal(a.SourceInputIDs, b.SourceInputIDs) &&
		slices.Equal(a.Attachments, b.Attachments) && a.Revises == b.Revises && sameOrigin(a.Origin, b.Origin) && a.BudgetUSD == b.BudgetUSD
}

func sameOrigin(a, b *TaskOrigin) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ValidateCommandBatch checks a batch before anything is read. A rejected batch never partly commits.
func ValidateCommandBatch(commands []Command) error {
	if len(commands) == 0 || len(commands) > MaxCommandBatch {
		return fmt.Errorf("%w: a command batch needs 1–32 commands", ErrCommandInvalid)
	}
	seen := map[string]bool{}
	for _, cmd := range commands {
		if cmd.ID == "" || cmd.RunID == "" || seen[cmd.ID] || len(cmd.Text) > MaxMessageBytes {
			return fmt.Errorf("%w: invalid or repeated command ID, task ID, or text", ErrCommandInvalid)
		}
		seen[cmd.ID] = true
	}
	return nil
}

// CommandBatch holds what an acceptance transaction read for a batch. Backends read every document before they write,
// including when several commands name the same task.
type CommandBatch struct {
	Prior map[string]*Command
	// Runs holds each task named by the batch that exists. Prepare updates it with the batch's effects.
	Runs   map[string]Run
	Exists map[string]bool
}

func NewCommandBatch() CommandBatch {
	return CommandBatch{Prior: map[string]*Command{}, Runs: map[string]Run{}, Exists: map[string]bool{}}
}

// Related names the other tasks a batch's commands change, which the transaction also has to read: a subtask's parent,
// and the running subtasks a Stop or correction ends.
func (b CommandBatch) Related(commands []Command) []string {
	related := []string{}
	add := func(id string) {
		if _, read := b.Exists[id]; !read && !slices.Contains(related, id) {
			related = append(related, id)
		}
	}
	for _, cmd := range commands {
		switch cmd.Kind {
		case CommandStart:
			if cmd.ParentID != "" {
				add(cmd.ParentID)
			}
		case CommandStop, CommandSteer:
			for _, id := range RunningSubtasks(b.Runs[cmd.RunID]) {
				add(id)
			}
		}
	}
	return related
}

// AllDuplicates reports whether every command was already accepted. A replay needs no admission checks.
func (b CommandBatch) AllDuplicates(commands []Command) bool {
	for _, cmd := range commands {
		if b.Prior[cmd.ID] == nil {
			return false
		}
	}
	return true
}

type PreparedCommands struct {
	Accepted []Command
	// ReadyEvents are inbox events for holds registered on tasks that already ended.
	ReadyEvents []TaskEvent
	// Changed names the tasks whose records changed; their new state is in CommandBatch.Runs.
	Changed map[string]bool
	// Receipts has one entry per command, in order, including replays.
	Receipts []CommandReceipt
}

// Prepare validates the whole batch in order against its evolving in-memory runs. It writes nothing, so a later
// rejection cannot partly accept a batch.
func (b CommandBatch) Prepare(commands []Command, control Control, now time.Time, traceContext map[string]string, taskBudgetUSD float64) (PreparedCommands, error) {
	if taskBudgetUSD <= 0 {
		taskBudgetUSD = TaskBudgetUSD
	}
	p := commandPrep{
		taskBudgetUSD: taskBudgetUSD,
		runs:          b.Runs, exists: b.Exists, opened: map[string]bool{}, now: now, traceContext: traceContext,
		prepared: PreparedCommands{
			Receipts: make([]CommandReceipt, 0, len(commands)), Accepted: []Command{}, ReadyEvents: []TaskEvent{}, Changed: map[string]bool{},
		},
	}
	for _, cmd := range commands {
		run := p.runs[cmd.RunID]
		if run.Question != nil {
			copy := *run.Question
			run.Question = &copy
		}
		if old := b.Prior[cmd.ID]; old != nil {
			if !SameCommand(*old, cmd) {
				return p.prepared, ErrDuplicate
			}
			p.prepared.Receipts = append(p.prepared.Receipts, CommandReceipt{Command: *old, Run: run})
			continue
		}
		if cmd.Interrupt && cmd.Kind != CommandSteer {
			return p.prepared, fmt.Errorf("%w: only steer can request interruption", ErrCommandInvalid)
		}
		if cmd.Kind == CommandHold {
			if err := p.hold(cmd, run); err != nil {
				return p.prepared, err
			}
			continue
		}
		if err := control.Check(); err != nil && cmd.Kind != CommandStop {
			return p.prepared, err
		}
		if p.exists[cmd.RunID] {
			if run.Status != RunRunning {
				return p.prepared, ErrBusy
			}
			if run.Finishing != nil && cmd.Kind != CommandStop {
				return p.prepared, ErrBusy
			}
			if cmd.Kind == CommandStart {
				return p.prepared, ErrDuplicate
			}
			if run.CancelRequested && cmd.Kind != CommandStop {
				return p.prepared, ErrCancelled
			}
		} else {
			var err error
			if run, err = p.start(cmd); err != nil {
				return p.prepared, err
			}
		}
		if run.Position >= MaxTaskCommands && cmd.Kind != CommandStop {
			return p.prepared, fmt.Errorf("%w: task input limit reached (32 accepted commands)", ErrCommandInvalid)
		}
		run.Uploads = MergeAttachments(run.Uploads, cmd.Attachments)
		if err := p.apply(&run, cmd); err != nil {
			return p.prepared, err
		}
		if cmd.Kind == CommandStop || cmd.Kind == CommandSteer {
			p.stopSubtasks(run, cmd.ID)
		}
		run.Position++
		run.TraceContext = PreferTraceContext(run.TraceContext, traceContext)
		cmd.Position, cmd.Revision, cmd.CreatedAt, cmd.Delivered = run.Position, run.Revision, now, false
		p.accept(run, cmd)
	}
	// A cascaded Stop follows the receipts for the batch itself, which callers match by index.
	p.prepared.Receipts = append(p.prepared.Receipts, p.cascaded...)
	for i := range p.prepared.Receipts {
		p.prepared.Receipts[i].Run = p.runs[p.prepared.Receipts[i].Command.RunID]
	}
	return p.prepared, nil
}

type commandPrep struct {
	taskBudgetUSD float64
	runs          map[string]Run
	exists        map[string]bool
	opened        map[string]bool
	now           time.Time
	traceContext  map[string]string
	prepared      PreparedCommands
	cascaded      []CommandReceipt
}

func (p *commandPrep) change(run Run) {
	p.runs[run.ID], p.prepared.Changed[run.ID] = run, true
}

func (p *commandPrep) accept(run Run, cmd Command) {
	p.change(run)
	p.prepared.Accepted = append(p.prepared.Accepted, cmd)
	p.prepared.Receipts = append(p.prepared.Receipts, CommandReceipt{Command: cmd, Run: run})
}

// hold registers a question to raise when the task ends. On a task that already ended it is ready at once.
func (p *commandPrep) hold(cmd Command, run Run) error {
	if !p.exists[cmd.RunID] {
		return ErrNotFound
	}
	holdBytes := len(cmd.Text)
	for _, existing := range run.Holds {
		holdBytes += len(existing.Question)
	}
	if strings.TrimSpace(cmd.Text) == "" || len(run.Holds) >= MaxTaskHolds || holdBytes > MaxTaskHoldBytes {
		return fmt.Errorf("%w: held question is empty or task hold limit reached", ErrCommandInvalid)
	}
	hold := TaskHold{
		ID: cmd.ID, TaskID: cmd.RunID, Question: cmd.Text, SourceInputIDs: cmd.SourceInputIDs, Status: "pending", CreatedAt: p.now,
	}
	if run.Status != RunRunning {
		hold.Status = "ready"
		run.EventSequence++
		p.prepared.ReadyEvents = append(p.prepared.ReadyEvents, TaskEvent{
			ID: "hold-" + cmd.ID, RunID: run.ID, Kind: "held_ready", Sequence: run.EventSequence, Revision: run.Revision,
			Text: run.Result, Reason: run.Error, Holds: []TaskHold{hold}, CreatedAt: p.now,
		})
	}
	run.Holds = append(append([]TaskHold{}, run.Holds...), hold)
	cmd.Position, cmd.Revision, cmd.CreatedAt, cmd.Delivered = run.Position, run.Revision, p.now, true
	p.accept(run, cmd)
	return nil
}

func (p *commandPrep) start(cmd Command) (Run, error) {
	if cmd.Kind != CommandStart {
		return Run{}, ErrNotFound
	}
	if strings.TrimSpace(cmd.Text) == "" {
		return Run{}, fmt.Errorf("%w: start needs a task request", ErrCommandInvalid)
	}
	run := Run{
		ID: cmd.RunID, Request: cmd.Text, Status: RunRunning,
		StartedAt: p.now, Budget: p.taskBudgetUSD, Events: []Event{}, Artifacts: []Artifact{},
		TraceContext: p.traceContext, OriginTraceContext: p.traceContext, Revises: cmd.Revises, Origin: cmd.Origin,
	}
	if cmd.BudgetUSD > 0 && cmd.BudgetUSD < run.Budget {
		run.Budget = cmd.BudgetUSD
	}
	// An explicit continuation is the user's own decision to spend more on the job; it may raise the ceiling up to
	// MaxContinuationBudgetUSD.
	if cmd.Origin != nil && cmd.Origin.ContinuesTaskID != "" && cmd.BudgetUSD > run.Budget {
		run.Budget = min(cmd.BudgetUSD, MaxContinuationBudgetUSD)
	}
	if cmd.ParentID != "" {
		parent, question, err := linkSubtask(p.runs[cmd.ParentID], p.exists[cmd.ParentID], cmd, p.opened)
		if err != nil {
			return Run{}, err
		}
		run.ParentID, run.ReadOnly, run.Budget = parent.ID, question.ReadOnly, parent.Budget
		p.change(parent)
	}
	p.exists[cmd.RunID] = true
	return run, nil
}

func (p *commandPrep) apply(run *Run, cmd Command) error {
	switch cmd.Kind {
	case CommandStart, CommandSteer:
		if strings.TrimSpace(cmd.Text) == "" {
			return fmt.Errorf("%w: task instruction is empty", ErrCommandInvalid)
		}
		run.Revision++
		if cmd.Kind == CommandSteer && cmd.Interrupt {
			run.InterruptRevision = run.Revision
		}
		run.Question, run.Waiting, run.PublicationPending = nil, "", false
	case CommandAnswer:
		return p.resolve(run, cmd)
	case CommandStop:
		run.Revision++
		run.CancelRequested = true
		run.Question, run.Waiting, run.PublicationPending = nil, "", false
	default:
		return fmt.Errorf("%w: unknown command kind", ErrCommandInvalid)
	}
	return nil
}

func (p *commandPrep) resolve(run *Run, cmd Command) error {
	kind := "input"
	if run.Question != nil && run.Question.Kind == QuestionWork && cmd.Kind == CommandAnswer {
		kind = QuestionWork
	}
	if run.Question == nil || run.Question.ResolvedBy != "" || cmd.QuestionID == "" || run.Question.ID != cmd.QuestionID ||
		run.Question.Revision != run.Revision || run.Question.Kind != kind {
		return ErrQuestionStale
	}
	if cmd.Kind == CommandAnswer && strings.TrimSpace(cmd.Text) == "" {
		return fmt.Errorf("%w: answer is empty", ErrCommandInvalid)
	}
	run.Question.ResolvedBy = cmd.ID
	return nil
}

func (p *commandPrep) stopSubtasks(run Run, commandID string) {
	for _, id := range RunningSubtasks(run) {
		child, stop, changed := StopSubtask(p.runs[id], commandID, p.now)
		if !changed {
			continue
		}
		p.change(child)
		p.prepared.Accepted = append(p.prepared.Accepted, stop)
		p.cascaded = append(p.cascaded, CommandReceipt{Command: stop, Run: child})
	}
}

// PreferTraceContext keeps the trace parent that later activity spans should hang from. It prefers the latest sampled
// command; an unsampled correction must not discard a sampled parent. Only the W3C traceparent is read.
func PreferTraceContext(current, incoming map[string]string) map[string]string {
	if len(incoming) == 0 {
		return current
	}
	now, next := traceParent(current), traceParent(incoming)
	if next.IsValid() && (!now.IsValid() || !now.IsSampled() || next.IsSampled()) {
		return incoming
	}
	return current
}

func traceParent(headers map[string]string) trace.SpanContext {
	ctx := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": headers["traceparent"]})
	return trace.SpanContextFromContext(ctx)
}

// ReconcileCommands selects the commands a task has accepted, in order. Holds are conversation questions, not
// instructions, so they are not part of the task's input.
func ReconcileCommands(run Run, commands []Command) []Command {
	selected := []Command{}
	for _, c := range commands {
		if c.Kind != CommandHold && c.Position <= TaskPosition(run) {
			selected = append(selected, c)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Position < selected[j].Position })
	return selected
}
