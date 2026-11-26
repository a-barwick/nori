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

type CommandPage struct {
	Commands []Command
	Next     string
}

type CommandKind string

const (
	CommandStart         CommandKind = "start"
	CommandSteer         CommandKind = "steer"
	CommandAnswer        CommandKind = "answer"
	CommandStop          CommandKind = "stop"
	CommandHold          CommandKind = "hold"
	CommandSubtaskResult CommandKind = "subtask_result"
)

type Command struct {
	Interrupt      bool            `json:"interrupt,omitempty" firestore:"interrupt,omitempty"`
	QuestionID     string          `json:"question_id,omitempty" firestore:"question_id,omitempty"`
	SourceInputIDs []string        `json:"source_input_ids,omitempty" firestore:"source_input_ids,omitempty"`
	Attachments    []Attachment    `json:"attachments,omitempty" firestore:"attachments,omitempty"`
	Revises        string          `json:"revises,omitempty" firestore:"revises,omitempty"`
	Origin         *TaskOrigin     `json:"origin,omitempty" firestore:"origin,omitempty"`
	BudgetUSD      float64         `json:"budget_usd,omitempty" firestore:"budget_usd,omitempty"`
	ModelSelection *ModelSelection `json:"model_selection,omitempty" firestore:"model_selection,omitempty"`
	ID             string          `json:"id" firestore:"id"`
	RunID          string          `json:"run_id" firestore:"run_id"`
	ParentID       string          `json:"parent_id,omitempty" firestore:"parent_id,omitempty"`
	Kind           CommandKind     `json:"kind" firestore:"kind"`
	Text           string          `json:"text" firestore:"text"`
	Position       int64           `json:"position" firestore:"position"`
	Revision       int64           `json:"revision" firestore:"revision"`
	Delivered      bool            `json:"delivered" firestore:"delivered"`
	CreatedAt      time.Time       `json:"created_at" firestore:"created_at"`
}

type TaskSnapshot struct {
	Run      Run
	Commands []Command
}

const MaxContinuationBudgetUSD = 50.0

const (
	MaxCommandBatch     = 32
	MaxTaskCommands     = 32
	MaxTaskHolds        = 32
	MaxTaskHoldBytes    = 32 << 10
	PendingCommandLimit = 100
)

func SameCommand(a, b Command) bool {
	return a.RunID == b.RunID && a.Kind == b.Kind && a.Text == b.Text && a.QuestionID == b.QuestionID &&
		a.Interrupt == b.Interrupt && a.ParentID == b.ParentID && slices.Equal(a.SourceInputIDs, b.SourceInputIDs) &&
		slices.Equal(a.Attachments, b.Attachments) && a.Revises == b.Revises && sameOrigin(a.Origin, b.Origin) &&
		a.BudgetUSD == b.BudgetUSD && ModelSelectionEqual(a.ModelSelection, b.ModelSelection)
}

func sameOrigin(a, b *TaskOrigin) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

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

type CommandBatch struct {
	Prior                 map[string]*Command
	Runs                  map[string]Run
	Exists                map[string]bool
	AccountModels         bool
	AccountModelSelection *ModelSelection
}

func NewCommandBatch() CommandBatch {
	return CommandBatch{Prior: map[string]*Command{}, Runs: map[string]Run{}, Exists: map[string]bool{}}
}

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

func (b CommandBatch) AllDuplicates(commands []Command) bool {
	for _, cmd := range commands {
		if b.Prior[cmd.ID] == nil {
			return false
		}
	}
	return true
}

type PreparedCommands struct {
	Accepted    []Command
	ReadyEvents []TaskEvent
	Changed     map[string]bool
	Receipts    []CommandReceipt
}

func (b CommandBatch) Prepare(commands []Command, control Control, now time.Time, traceContext map[string]string, taskBudgetUSD float64) (PreparedCommands, error) {
	if taskBudgetUSD <= 0 {
		taskBudgetUSD = TaskBudgetUSD
	}
	p := commandPrep{
		taskBudgetUSD: taskBudgetUSD, accountModels: b.AccountModels, accountModelSelection: b.AccountModelSelection,
		runs: b.Runs, exists: b.Exists, opened: map[string]bool{}, now: now, traceContext: traceContext,
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
	p.prepared.Receipts = append(p.prepared.Receipts, p.cascaded...)
	for i := range p.prepared.Receipts {
		p.prepared.Receipts[i].Run = p.runs[p.prepared.Receipts[i].Command.RunID]
	}
	return p.prepared, nil
}

type commandPrep struct {
	accountModels         bool
	accountModelSelection *ModelSelection
	taskBudgetUSD         float64
	runs                  map[string]Run
	exists                map[string]bool
	opened                map[string]bool
	now                   time.Time
	traceContext          map[string]string
	prepared              PreparedCommands
	cascaded              []CommandReceipt
}

func (p *commandPrep) change(run Run) {
	p.runs[run.ID], p.prepared.Changed[run.ID] = run, true
}

func (p *commandPrep) accept(run Run, cmd Command) {
	p.change(run)
	p.prepared.Accepted = append(p.prepared.Accepted, cmd)
	p.prepared.Receipts = append(p.prepared.Receipts, CommandReceipt{Command: cmd, Run: run})
}

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
		ModelSelection: cmd.ModelSelection.Clone(),
	}
	if p.accountModels {
		run.ModelSelection = nil
	}
	if cmd.BudgetUSD > 0 && cmd.BudgetUSD < run.Budget {
		run.Budget = cmd.BudgetUSD
	}
	if cmd.Origin != nil && cmd.Origin.ContinuesTaskID != "" && cmd.BudgetUSD > run.Budget {
		run.Budget = min(cmd.BudgetUSD, MaxContinuationBudgetUSD)
	}
	if cmd.ParentID != "" {
		parent, question, err := linkSubtask(p.runs[cmd.ParentID], p.exists[cmd.ParentID], cmd, p.opened)
		if err != nil {
			return Run{}, err
		}
		run.ParentID, run.ReadOnly, run.Budget = parent.ID, question.ReadOnly, parent.Budget
		if run.ModelSelection.IsEmpty() && !parent.ModelSelection.IsEmpty() {
			run.ModelSelection = parent.ModelSelection.TaskOnly()
		}
		p.change(parent)
	}
	if p.accountModels && run.ModelSelection.IsEmpty() {
		run.ModelSelection = p.accountModelSelection.Clone()
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
