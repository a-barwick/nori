package conversation

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.temporal.io/sdk/client"
	"nori/internal/computer"
	"nori/internal/observability"
	"nori/internal/state"
	"nori/internal/task"
	"nori/internal/workerwake"
)

// Tasks delegates admission, command ordering, delivery and execution to the
// existing task implementation. A dispatch ends at durable acceptance.
type Tasks struct {
	Store    state.ConversationStore
	Wakes    *workerwake.Cloud
	Client   client.Client
	Computer computer.Config
}

func (t *Tasks) deliver(ctx context.Context) {
	delivery, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := task.Deliver(delivery, t.Store, t.Client, t.Wakes); err != nil {
		log.Printf("command accepted; Temporal delivery remains pending: %v", err)
	}
}

func (t *Tasks) AcceptBatch(ctx context.Context, commands []state.Command, fence *state.ConversationFence) ([]state.CommandReceipt, error) {
	ctx, span := conversationTracer.Start(ctx, "conversation.command_batch", trace.WithAttributes(attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID()), attribute.Int("nori.command.count", len(commands))))
	defer span.End()
	started := time.Now()
	receipts, err := t.Store.AcceptCommandBatch(ctx, commands, fence)
	for _, cmd := range commands {
		name := safeCommandName(cmd.Kind)
		_, commandSpan := conversationTracer.Start(ctx, "conversation."+name, trace.WithTimestamp(started), trace.WithAttributes(attribute.String("openinference.span.kind", "TOOL"), attribute.String("session.id", observability.SessionID()), attribute.String("tool.name", name), attribute.String("nori.task.id", cmd.RunID), attribute.String("nori.command.id", cmd.ID)))
		outcome := "accepted"
		if err != nil {
			outcome = "rejected"
		}
		commandSpan.SetAttributes(attribute.String("nori.tool.outcome", outcome), attribute.Bool("nori.command.interrupt", cmd.Interrupt))
		if cmd.Kind == state.CommandHold {
			commandSpan.SetAttributes(attribute.String("nori.hold.id", cmd.ID))
		}
		if err == nil && cmd.Kind == state.CommandHold {
			observability.Event(ctx, "conversation.hold.accepted", attribute.String("nori.hold.id", cmd.ID), attribute.String("nori.task.id", cmd.RunID))
		}
		if err == nil && cmd.Kind == state.CommandSteer {
			observability.Event(ctx, "conversation.correction.accepted", attribute.String("nori.task.id", cmd.RunID), attribute.String("nori.command.id", cmd.ID), attribute.Bool("nori.command.interrupt", cmd.Interrupt))
		}
		commandSpan.End()
	}
	if err != nil {
		if !commandRejected(err) && !errors.Is(err, state.ErrConversationStale) && !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "conversation.command", err)
		}
		return nil, err
	}
	for _, receipt := range receipts {
		if receipt.Command.Kind != state.CommandHold {
			t.deliver(ctx)
			break
		}
	}
	t.repairPausedStops(ctx, receipts)
	return receipts, nil
}

func safeCommandName(kind state.CommandKind) string {
	switch kind {
	case "start":
		return "dispatch"
	case "steer", "answer", "continue", "stop", "input", "cancel", "hold":
		return string(kind)
	default:
		return "unknown"
	}
}

func (t *Tasks) Update(ctx context.Context, id, runID, message string) (state.Run, error) {
	if receipt, err := t.Store.ConversationCommand(ctx, id); err != nil {
		return state.Run{}, err
	} else if receipt != nil {
		if receipt.Command.RunID != runID || receipt.Command.Text != message {
			return state.Run{}, state.ErrDuplicate
		}
		t.deliver(ctx)
		return receipt.Run, nil
	}
	current, err := t.Store.GetRun(ctx, runID)
	if err != nil {
		return state.Run{}, err
	}
	command := state.Command{ID: id, RunID: runID, Kind: state.InputKind(message), Text: message}
	if current.ProtocolVersion >= state.TaskProtocolVersion {
		command.Kind = state.CommandSteer
		if question := current.Question; question != nil && question.ResolvedBy == "" && question.Revision == current.Revision {
			switch {
			case question.Kind == "input":
				command.Kind, command.QuestionID = "answer", question.ID
			case question.Kind == "budget" && strings.EqualFold(strings.TrimSpace(message), "continue"):
				command.Kind, command.QuestionID = "continue", question.ID
			}
		}
	}
	run, err := t.Store.Accept(ctx, command)
	if err != nil {
		return state.Run{}, err
	}
	t.deliver(ctx)
	return run, nil
}

func (t *Tasks) Cancel(ctx context.Context, id string) (state.Run, error) {
	commandID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(id+"-cancel")).String()
	receipts, err := t.AcceptBatch(ctx, []state.Command{{ID: commandID, RunID: id, Kind: "stop"}}, nil)
	if err != nil {
		return state.Run{}, err
	}
	return receipts[0].Run, nil
}

// Stop can finish a never-started task while its worker remains paused. This
// only repairs terminal state and attaches for cleanup; it never starts compute.
func (t *Tasks) repairPausedStops(ctx context.Context, receipts []state.CommandReceipt) {
	stops := map[string]bool{}
	for _, receipt := range receipts {
		if receipt.Command.Kind == state.CommandStop || receipt.Command.Kind == state.CommandCancel {
			stops[receipt.Command.RunID] = true
		}
	}
	if len(stops) == 0 {
		return
	}
	repair, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	control, err := t.Store.Control(repair)
	if err != nil || !control.Paused {
		return
	}
	for id := range stops {
		if _, err = task.StopPaused(repair, t.Store, t.Computer, id); err != nil {
			log.Printf("Stop recovery pending for task %s: %v", id, err)
		}
	}
	for i := range receipts {
		if stops[receipts[i].Command.RunID] {
			if latest, err := t.Store.GetRun(repair, receipts[i].Command.RunID); err == nil {
				receipts[i].Run = latest
			}
		}
	}
}
