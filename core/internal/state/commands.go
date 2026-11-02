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

// Command and its delivery intent are committed in the acceptance transaction.
// ID is supplied by the caller and is stable across HTTP and relay retries.
type CommandKind string

const (
	CommandStart    CommandKind = "start"
	CommandSteer    CommandKind = "steer"
	CommandAnswer   CommandKind = "answer"
	CommandContinue CommandKind = "continue"
	CommandStop     CommandKind = "stop"
	CommandHold     CommandKind = "hold"
	CommandApprove  CommandKind = "approve"
	CommandDecline  CommandKind = "decline"
	// CommandSubtaskResult delivers finished subtasks to the parent that asked for them. Only the transaction that
	// saves a subtask's outcome creates one; it never passes through Prepare, because a pause or the command limit must
	// not lose a result.
	CommandSubtaskResult CommandKind = "subtask_result"
)

type Command struct {
	Interrupt      bool        `json:"interrupt,omitempty" firestore:"interrupt,omitempty"`
	QuestionID     string      `json:"question_id,omitempty" firestore:"question_id,omitempty"`
	SourceInputIDs []string    `json:"source_input_ids,omitempty" firestore:"source_input_ids,omitempty"`
	ID             string      `json:"id" firestore:"id"`
	RunID          string      `json:"run_id" firestore:"run_id"`
	ParentID       string      `json:"parent_id,omitempty" firestore:"parent_id,omitempty"`
	Kind           CommandKind `json:"kind" firestore:"kind"`
	Text           string      `json:"text" firestore:"text"`
	Position       int64       `json:"position" firestore:"position"`
	Revision       int64       `json:"revision" firestore:"revision"`
	Delivered      bool        `json:"delivered" firestore:"delivered"`
	CreatedAt      time.Time   `json:"created_at" firestore:"created_at"`
}

type TaskSnapshot struct {
	Run      Run
	Commands []Command
}

const (
	// MaxCommandBatch bounds the commands one acceptance transaction admits.
	MaxCommandBatch = 32
	// MaxTaskCommands bounds the commands one task accepts, other than a stop.
	MaxTaskCommands = 32
	// MaxTaskHolds and MaxTaskHoldBytes bound the conversation questions waiting on one task.
	MaxTaskHolds     = 32
	MaxTaskHoldBytes = 32 << 10
	// PendingCommandLimit bounds one read of undelivered commands.
	PendingCommandLimit = 100
)

// SameCommand reports whether a retry carries the same intent as a saved command with that ID.
func SameCommand(a, b Command) bool {
	return a.RunID == b.RunID && a.Kind == b.Kind && a.Text == b.Text && a.QuestionID == b.QuestionID &&
		a.Interrupt == b.Interrupt && a.ParentID == b.ParentID && slices.Equal(a.SourceInputIDs, b.SourceInputIDs)
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
	// Prior holds the saved command for each command ID that was already accepted.
	Prior map[string]*Command
	// Runs holds each task named by the batch that exists. Prepare updates it with the batch's effects.
	Runs map[string]Run
	// Exists records, for every task named by the batch, whether its record exists.
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
		case CommandContinue:
			if parent := b.Runs[cmd.RunID].ParentID; parent != "" {
				add(parent)
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

// PreparedCommands is a validated batch ready to write.
type PreparedCommands struct {
	// Accepted are the new commands to create.
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
func (b CommandBatch) Prepare(commands []Command, control Control, now time.Time, traceContext map[string]string) (PreparedCommands, error) {
	prior, runs, exists := b.Prior, b.Runs, b.Exists
	opened := map[string]bool{}
	cascaded := []CommandReceipt{}
	prepared := PreparedCommands{
		Receipts: make([]CommandReceipt, 0, len(commands)), Accepted: []Command{}, ReadyEvents: []TaskEvent{}, Changed: map[string]bool{},
	}
	for _, original := range commands {
		cmd := original
		run := runs[cmd.RunID]
		if run.Question != nil {
			copy := *run.Question
			run.Question = &copy
		}
		if old := prior[cmd.ID]; old != nil {
			if !SameCommand(*old, cmd) {
				return prepared, ErrDuplicate
			}
			prepared.Receipts = append(prepared.Receipts, CommandReceipt{Command: *old, Run: run})
			continue
		}
		if cmd.Interrupt && cmd.Kind != CommandSteer {
			return prepared, fmt.Errorf("%w: only steer can request interruption", ErrCommandInvalid)
		}
		if cmd.Kind == CommandHold {
			if !exists[cmd.RunID] {
				return prepared, ErrNotFound
			}
			holdBytes := len(cmd.Text)
			for _, existing := range run.Holds {
				holdBytes += len(existing.Question)
			}
			if strings.TrimSpace(cmd.Text) == "" || len(run.Holds) >= MaxTaskHolds || holdBytes > MaxTaskHoldBytes {
				return prepared, fmt.Errorf("%w: held question is empty or task hold limit reached", ErrCommandInvalid)
			}
			hold := TaskHold{
				ID: cmd.ID, TaskID: cmd.RunID, Question: cmd.Text, SourceInputIDs: cmd.SourceInputIDs, Status: "pending", CreatedAt: now,
			}
			if run.Status != RunRunning {
				hold.Status = "ready"
				run.EventSequence++
				prepared.ReadyEvents = append(prepared.ReadyEvents, TaskEvent{
					ID: "hold-" + cmd.ID, RunID: run.ID, Kind: "held_ready", Sequence: run.EventSequence, Revision: run.Revision,
					Text: run.Result, Reason: run.Error, Holds: []TaskHold{hold}, CreatedAt: now,
				})
			}
			run.Holds = append(append([]TaskHold{}, run.Holds...), hold)
			cmd.Position, cmd.Revision, cmd.CreatedAt, cmd.Delivered = run.Position, run.Revision, now, true
			runs[run.ID], prepared.Changed[run.ID] = run, true
			prepared.Accepted = append(prepared.Accepted, cmd)
			prepared.Receipts = append(prepared.Receipts, CommandReceipt{Command: cmd, Run: run})
			continue
		}
		if err := control.Check(); err != nil && cmd.Kind != CommandStop {
			return prepared, err
		}
		if exists[cmd.RunID] {
			if run.Status != RunRunning {
				return prepared, ErrBusy
			}
			if cmd.Kind == CommandStart {
				return prepared, ErrDuplicate
			}
			if run.CancelRequested && cmd.Kind != CommandStop {
				return prepared, ErrCancelled
			}
		} else {
			if cmd.Kind != CommandStart {
				return prepared, ErrNotFound
			}
			if strings.TrimSpace(cmd.Text) == "" {
				return prepared, fmt.Errorf("%w: start needs a task request", ErrCommandInvalid)
			}
			run = Run{
				ID: cmd.RunID, Request: cmd.Text, Status: RunRunning, Temporal: true, ProtocolVersion: TaskProtocolVersion,
				WorkflowVersion: TaskWorkflowVersion, StartedAt: now, Budget: TaskBudgetUSD, Events: []Event{}, Artifacts: []Artifact{},
				TraceContext: traceContext, OriginTraceContext: traceContext,
			}
			if cmd.ParentID != "" {
				parent, question, err := linkSubtask(runs[cmd.ParentID], exists[cmd.ParentID], cmd, opened)
				if err != nil {
					return prepared, err
				}
				run.ParentID, run.ReadOnly, run.Budget = parent.ID, question.ReadOnly, parent.Budget
				runs[parent.ID], prepared.Changed[parent.ID] = parent, true
			}
			exists[cmd.RunID] = true
		}
		if run.Position >= MaxTaskCommands && cmd.Kind != CommandStop {
			return prepared, fmt.Errorf("%w: task input limit reached (32 accepted commands)", ErrCommandInvalid)
		}
		switch cmd.Kind {
		case CommandStart, CommandSteer:
			if strings.TrimSpace(cmd.Text) == "" {
				return prepared, fmt.Errorf("%w: task instruction is empty", ErrCommandInvalid)
			}
			run.Revision++
			if cmd.Kind == CommandSteer && cmd.Interrupt {
				run.InterruptRevision = run.Revision
			}
			run.Question, run.Waiting, run.PublicationPending = nil, "", false
		case CommandAnswer, CommandContinue, CommandApprove, CommandDecline:
			kind := "input"
			if run.Question != nil && run.Question.Kind == QuestionWork && cmd.Kind == CommandAnswer {
				kind = QuestionWork
			}
			switch cmd.Kind {
			case CommandContinue:
				kind = "budget"
			case CommandApprove, CommandDecline:
				kind = "approval"
			}
			if run.Question == nil || run.Question.ResolvedBy != "" || cmd.QuestionID == "" || run.Question.ID != cmd.QuestionID ||
				run.Question.Revision != run.Revision || run.Question.Kind != kind {
				return prepared, ErrQuestionStale
			}
			if cmd.Kind == CommandAnswer && strings.TrimSpace(cmd.Text) == "" {
				return prepared, fmt.Errorf("%w: answer is empty", ErrCommandInvalid)
			}
			run.Question.ResolvedBy = cmd.ID
			if cmd.Kind == CommandContinue {
				if run.ParentID == "" {
					run.Budget += TaskBudgetUSD
				} else if owner, known := runs[run.ParentID]; known {
					grantBudget(&owner)
					run.Budget = owner.Budget
					runs[owner.ID], prepared.Changed[owner.ID] = owner, true
				}
			}
		case CommandStop:
			run.Revision++
			run.CancelRequested = true
			run.Question, run.Waiting, run.PublicationPending = nil, "", false
		default:
			return prepared, fmt.Errorf("%w: unknown command kind", ErrCommandInvalid)
		}
		if cmd.Kind == CommandStop || cmd.Kind == CommandSteer {
			for _, id := range RunningSubtasks(run) {
				child, stop, changed := StopSubtask(runs[id], cmd.ID, now)
				if !changed {
					continue
				}
				runs[id], prepared.Changed[id] = child, true
				prepared.Accepted = append(prepared.Accepted, stop)
				cascaded = append(cascaded, CommandReceipt{Command: stop, Run: child})
			}
		}
		run.Position++
		run.TraceContext = PreferTraceContext(run.TraceContext, traceContext)
		cmd.Position, cmd.Revision, cmd.CreatedAt, cmd.Delivered = run.Position, run.Revision, now, false
		runs[run.ID], prepared.Changed[run.ID] = run, true
		prepared.Accepted = append(prepared.Accepted, cmd)
		prepared.Receipts = append(prepared.Receipts, CommandReceipt{Command: cmd, Run: run})
	}
	// A cascaded Stop follows the receipts for the batch itself, which callers match by index.
	prepared.Receipts = append(prepared.Receipts, cascaded...)
	for i := range prepared.Receipts {
		prepared.Receipts[i].Run = runs[prepared.Receipts[i].Command.RunID]
	}
	return prepared, nil
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

// OrderPending sorts undelivered commands oldest first.
func OrderPending(commands []Command) []Command {
	sort.Slice(commands, func(i, j int) bool { return commands[i].CreatedAt.Before(commands[j].CreatedAt) })
	return commands
}
