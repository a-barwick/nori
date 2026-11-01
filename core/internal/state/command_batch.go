package state

import (
	"context"
	"fmt"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"nori/internal/observability"
)

type commandBatch struct {
	prior  map[string]*Command
	runs   map[string]Run
	exists map[string]bool
}

// readCommandBatch completes all document reads before the transaction writes.
func (s *Google) readCommandBatch(tx *firestore.Transaction, commands []Command) (commandBatch, error) {
	batch := commandBatch{prior: map[string]*Command{}, runs: map[string]Run{}, exists: map[string]bool{}}
	seen := map[string]bool{}
	// Firestore requires all reads before writes, including repeated task IDs.
	for _, cmd := range commands {
		if cmd.ID == "" || cmd.RunID == "" || seen[cmd.ID] || len(cmd.Text) > MaxMessageBytes {
			return batch, fmt.Errorf("%w: invalid or repeated command ID, task ID, or text", ErrCommandInvalid)
		}
		seen[cmd.ID] = true
		doc, err := tx.Get(s.commands().Doc(cmd.ID))
		if err == nil {
			var old Command
			if err = doc.DataTo(&old); err != nil {
				return batch, err
			}
			batch.prior[cmd.ID] = &old
		} else if status.Code(err) != codes.NotFound {
			return batch, err
		}
		if _, read := batch.exists[cmd.RunID]; !read {
			doc, err = tx.Get(s.runs().Doc(cmd.RunID))
			if err == nil {
				r, err := decodeRun(doc, nil)
				if err != nil {
					return batch, err
				}
				batch.runs[cmd.RunID], batch.exists[cmd.RunID] = r, true
			} else if status.Code(err) == codes.NotFound {
				batch.exists[cmd.RunID] = false
			} else {
				return batch, err
			}
		}
	}
	return batch, nil
}

func (s *Google) admitCommandBatch(tx *firestore.Transaction, commands []Command, batch commandBatch, fence *ConversationFence) (Control, error) {
	allDuplicate := true
	for _, cmd := range commands {
		allDuplicate = allDuplicate && batch.prior[cmd.ID] != nil
	}
	if !allDuplicate {
		if err := s.conversationAdmission(tx, fence); err != nil {
			return Control{}, err
		}
	}
	var control Control
	if !allDuplicate {
		controlDoc, err := tx.Get(s.db.Collection("system").Doc("control"))
		if err != nil {
			return Control{}, err
		}
		if err = controlDoc.DataTo(&control); err != nil {
			return Control{}, err
		}
	}
	return control, nil
}

// prepare validates the whole batch in order against its evolving in-memory runs.
// It performs no transaction writes, so a later rejection cannot partly accept a batch.
func (batch commandBatch) prepare(commands []Command, control Control, now time.Time, traceContext map[string]string) (preparedCommands, error) {
	prior, runs, exists := batch.prior, batch.runs, batch.exists
	prepared := preparedCommands{receipts: make([]CommandReceipt, 0, len(commands)), accepted: []Command{}, readyEvents: []TaskEvent{}, changed: map[string]bool{}}
	for _, original := range commands {
		cmd := original
		run := runs[cmd.RunID]
		if run.Question != nil {
			copy := *run.Question
			run.Question = &copy
		}
		// Old pinned workers retain their original command vocabulary/counters.
		if exists[cmd.RunID] && run.ProtocolVersion < TaskProtocolVersion {
			switch cmd.Kind {
			case "steer", "answer":
				cmd.Kind = InputKind(cmd.Text)
				// Keep a receipt accepted before this translation fix stable.
				// New commands use the pinned v1 worker's continue vocabulary.
				if old := prior[cmd.ID]; old != nil && old.Kind == CommandInput && cmd.Kind == CommandContinue {
					cmd.Kind = CommandInput
				}
			case "stop":
				cmd.Kind = CommandCancel
			}
		}
		if old := prior[cmd.ID]; old != nil {
			if !sameCommand(*old, cmd) {
				return prepared, ErrDuplicate
			}
			prepared.receipts = append(prepared.receipts, CommandReceipt{Command: *old, Run: run})
			continue
		}
		if cmd.Interrupt && cmd.Kind != CommandSteer {
			return prepared, fmt.Errorf("%w: only steer can request interruption", ErrCommandInvalid)
		}
		if cmd.Interrupt && run.WorkflowVersion < TaskWorkflowVersion {
			return prepared, fmt.Errorf("%w: this task worker cannot safely interrupt active work", ErrCommandInvalid)
		}
		if cmd.Kind == CommandHold {
			if !exists[cmd.RunID] {
				return prepared, ErrNotFound
			}
			if run.Status == RunRunning && run.WorkflowVersion < TaskWorkflowVersion {
				return prepared, fmt.Errorf("%w: this task worker cannot deliver held results", ErrCommandInvalid)
			}
			holdBytes := len(cmd.Text)
			for _, existing := range run.Holds {
				holdBytes += len(existing.Question)
			}
			if strings.TrimSpace(cmd.Text) == "" || len(run.Holds) >= 32 || holdBytes > 32<<10 {
				return prepared, fmt.Errorf("%w: held question is empty or task hold limit reached", ErrCommandInvalid)
			}
			hold := TaskHold{ID: cmd.ID, TaskID: cmd.RunID, Question: cmd.Text, SourceInputIDs: cmd.SourceInputIDs, Status: "pending", CreatedAt: now}
			if run.Status != RunRunning {
				hold.Status = "ready"
				run.EventSequence++
				prepared.readyEvents = append(prepared.readyEvents, TaskEvent{ID: "hold-" + cmd.ID, RunID: run.ID, Kind: "held_ready", Sequence: run.EventSequence, Revision: run.Revision, Text: run.Result, Reason: run.Error, Holds: []TaskHold{hold}, CreatedAt: now})
			}
			run.Holds = append(append([]TaskHold{}, run.Holds...), hold)
			cmd.Position, cmd.Revision, cmd.CreatedAt, cmd.Delivered = run.Position, run.Revision, now, true
			runs[run.ID], prepared.changed[run.ID] = run, true
			prepared.accepted = append(prepared.accepted, cmd)
			prepared.receipts = append(prepared.receipts, CommandReceipt{Command: cmd, Run: run})
			continue
		}
		if err := control.Check(); err != nil && cmd.Kind != CommandStop && cmd.Kind != CommandCancel {
			return prepared, err
		}
		if exists[cmd.RunID] {
			if !run.Temporal || run.Status != RunRunning {
				return prepared, ErrBusy
			}
			if cmd.Kind == CommandStart {
				return prepared, ErrDuplicate
			}
			if run.CancelRequested && cmd.Kind != CommandStop && cmd.Kind != CommandCancel {
				return prepared, ErrCancelled
			}
		} else {
			if cmd.Kind != CommandStart {
				return prepared, ErrNotFound
			}
			if strings.TrimSpace(cmd.Text) == "" {
				return prepared, fmt.Errorf("%w: start needs a task request", ErrCommandInvalid)
			}
			run = Run{ID: cmd.RunID, Request: cmd.Text, Status: "running", Temporal: true, ProtocolVersion: TaskProtocolVersion, WorkflowVersion: TaskWorkflowVersion, StartedAt: now, Budget: 1, Events: []Event{}, Artifacts: []Artifact{}, TraceContext: traceContext, OriginTraceContext: traceContext}
			exists[cmd.RunID] = true
		}
		if run.ProtocolVersion < TaskProtocolVersion {
			if cmd.Kind != CommandInput && cmd.Kind != CommandContinue && cmd.Kind != CommandCancel {
				return prepared, fmt.Errorf("%w: unsupported legacy task command", ErrCommandInvalid)
			}
			if run.Revision >= 32 && cmd.Kind != CommandCancel {
				return prepared, fmt.Errorf("%w: task input limit reached (32 accepted commands)", ErrCommandInvalid)
			}
			run.Revision++
			run.Position = run.Revision
			if cmd.Kind == CommandCancel {
				run.CancelRequested = true
			}
		} else {
			if run.Position >= 32 && cmd.Kind != CommandStop {
				return prepared, fmt.Errorf("%w: task input limit reached (32 accepted commands)", ErrCommandInvalid)
			}
			switch cmd.Kind {
			case "start", "steer":
				if strings.TrimSpace(cmd.Text) == "" {
					return prepared, fmt.Errorf("%w: task instruction is empty", ErrCommandInvalid)
				}
				run.Revision++
				if cmd.Kind == CommandSteer && cmd.Interrupt {
					run.InterruptRevision = run.Revision
				}
				run.Question, run.Waiting, run.PublicationPending = nil, "", false
			case "answer", "continue":
				kind := "input"
				if cmd.Kind == CommandContinue {
					kind = "budget"
				}
				if run.Question == nil || run.Question.ResolvedBy != "" || cmd.QuestionID == "" || run.Question.ID != cmd.QuestionID || run.Question.Revision != run.Revision || run.Question.Kind != kind {
					return prepared, ErrQuestionStale
				}
				if cmd.Kind == CommandAnswer && strings.TrimSpace(cmd.Text) == "" {
					return prepared, fmt.Errorf("%w: answer is empty", ErrCommandInvalid)
				}
				run.Question.ResolvedBy = cmd.ID
			case "stop":
				run.Revision++
				run.CancelRequested = true
				run.Question, run.Waiting, run.PublicationPending = nil, "", false
			default:
				return prepared, fmt.Errorf("%w: unknown command kind", ErrCommandInvalid)
			}
			run.Position++
		}
		// Prefer the latest sampled command; an unsampled correction must not
		// discard a sampled parent needed by later activity spans.
		if len(traceContext) != 0 {
			current := trace.SpanContextFromContext(observability.ExtractTrace(context.Background(), run.TraceContext))
			incoming := trace.SpanContextFromContext(observability.ExtractTrace(context.Background(), traceContext))
			if incoming.IsValid() && (!current.IsValid() || !current.IsSampled() || incoming.IsSampled()) {
				run.TraceContext = traceContext
			}
		}
		cmd.Position, cmd.Revision, cmd.CreatedAt, cmd.Delivered = run.Position, run.Revision, now, false
		runs[run.ID], prepared.changed[run.ID] = run, true
		prepared.accepted = append(prepared.accepted, cmd)
		prepared.receipts = append(prepared.receipts, CommandReceipt{Command: cmd, Run: run})
	}
	for i := range prepared.receipts {
		prepared.receipts[i].Run = runs[prepared.receipts[i].Command.RunID]
	}
	return prepared, nil
}

func (s *Google) writeCommandBatch(tx *firestore.Transaction, batch commandBatch, prepared preparedCommands, now time.Time, traceContext map[string]string, fence *ConversationFence) error {
	runs := batch.runs
	if err := s.appendHeldEvents(tx, prepared.readyEvents, traceContext); err != nil {
		return err
	}
	for id := range prepared.changed {
		if err := tx.Set(s.runs().Doc(id), runs[id]); err != nil {
			return err
		}
	}
	for _, cmd := range prepared.accepted {
		if err := tx.Create(s.commands().Doc(cmd.ID), cmd); err != nil {
			return err
		}
		if runs[cmd.RunID].ProtocolVersion < TaskProtocolVersion && cmd.Kind != CommandCancel {
			message := Message{ID: cmd.ID + "-user", RunID: cmd.RunID, Role: "user", Text: cmd.Text, CreatedAt: now}
			if fence != nil {
				message.TurnID = fence.TurnID
			}
			if err := tx.Create(s.messages().Doc(message.ID), message); err != nil {
				return err
			}
		}
	}
	return nil
}

type preparedCommands struct {
	accepted    []Command
	readyEvents []TaskEvent
	changed     map[string]bool
	receipts    []CommandReceipt
}
