package conversation

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
	"nori/internal/computer"
	"nori/internal/state"
	"nori/internal/task"
)

// Tasks delegates admission, command ordering, delivery and execution to the
// existing task implementation. A dispatch ends at durable acceptance.
type Tasks struct {
	Store    *state.Google
	Client   client.Client
	Computer computer.Config
}

func (t *Tasks) deliver(ctx context.Context) {
	delivery, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := task.Deliver(delivery, t.Store, t.Client); err != nil {
		log.Printf("command accepted; Temporal delivery remains pending: %v", err)
	}
}

func (t *Tasks) AcceptBatch(ctx context.Context, commands []state.Command, fence *state.ConversationFence) ([]state.CommandReceipt, error) {
	receipts, err := t.Store.AcceptCommandBatch(ctx, commands, fence)
	if err != nil {
		return nil, err
	}
	t.deliver(ctx)
	t.repairPausedStops(ctx, receipts)
	return receipts, nil
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
		command.Kind = "steer"
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
		if receipt.Command.Kind == "stop" || receipt.Command.Kind == "cancel" {
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
