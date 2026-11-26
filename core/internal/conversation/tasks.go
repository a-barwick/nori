package conversation

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/observability"
	"nori/internal/state"
)

type Tasks struct {
	Store state.ConversationStore
	Now   func() time.Time
}

func (t *Tasks) now() time.Time {
	if t != nil && t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *Tasks) AcceptBatch(ctx context.Context, commands []state.Command, fence *state.ConversationFence) ([]state.CommandReceipt, error) {
	ctx, span := conversationTracer.Start(ctx, "conversation.command_batch", trace.WithAttributes(
		attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID()),
		attribute.Int("nori.command.count", len(commands))))
	defer span.End()
	started := t.now()
	receipts, err := t.Store.AcceptCommandBatch(ctx, commands, fence)
	for _, cmd := range commands {
		name := safeCommandName(cmd.Kind)
		_, commandSpan := conversationTracer.Start(ctx, "conversation."+name, trace.WithTimestamp(started), trace.WithAttributes(
			attribute.String("openinference.span.kind", "TOOL"), attribute.String("session.id", observability.SessionID()),
			attribute.String("tool.name", name), attribute.String("nori.task.id", cmd.RunID), attribute.String("nori.command.id", cmd.ID)))
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
			observability.Event(ctx, "conversation.correction.accepted", attribute.String("nori.task.id", cmd.RunID),
				attribute.String("nori.command.id", cmd.ID), attribute.Bool("nori.command.interrupt", cmd.Interrupt))
		}
		commandSpan.End()
	}
	if err != nil {
		if !commandRejected(err) && !errors.Is(err, state.ErrConversationStale) && !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "conversation.command", err)
		}
		return nil, err
	}
	return receipts, nil
}

func safeCommandName(kind state.CommandKind) string {
	switch kind {
	case "start":
		return "dispatch"
	case "steer", "answer", "stop", "hold":
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
		return receipt.Run, nil
	}
	current, err := t.Store.GetRun(ctx, runID)
	if err != nil {
		return state.Run{}, err
	}
	command := state.Command{ID: id, RunID: runID, Kind: state.CommandSteer, Text: message}
	if question := current.Question; question != nil && question.ResolvedBy == "" && question.Revision == current.Revision &&
		(question.Kind == "input" || question.Kind == state.QuestionWork) {
		command.Kind, command.QuestionID = state.CommandAnswer, question.ID
	}
	receipts, err := t.AcceptBatch(ctx, []state.Command{command}, nil)
	if err != nil {
		return state.Run{}, err
	}
	return receipts[0].Run, nil
}

func (t *Tasks) Cancel(ctx context.Context, id string) (state.Run, error) {
	commandID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(id+"-cancel")).String()
	receipts, err := t.AcceptBatch(ctx, []state.Command{{ID: commandID, RunID: id, Kind: "stop"}}, nil)
	if err != nil {
		return state.Run{}, err
	}
	return receipts[0].Run, nil
}
