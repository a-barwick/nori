package conversation

import (
	"context"
	"log"
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

func (t *Tasks) Snapshot(ctx context.Context) (state.Session, error) {
	return t.Store.ConversationSession(ctx)
}

func (t *Tasks) deliver(ctx context.Context) {
	delivery, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := task.Deliver(delivery, t.Store, t.Client); err != nil {
		log.Printf("command accepted; Temporal delivery remains pending: %v", err)
	}
}

func (t *Tasks) Dispatch(ctx context.Context, id, request string) (Dispatch, error) {
	run, err := t.Store.Accept(ctx, state.Command{ID: id, RunID: id, Kind: "start", Text: request})
	if err != nil {
		return Dispatch{}, err
	}
	t.deliver(ctx)
	return Dispatch{ID: run.ID, Request: request, Status: "accepted", CreatedAt: run.StartedAt}, nil
}

func (t *Tasks) Update(ctx context.Context, id, runID, message string) (state.Run, error) {
	run, err := t.Store.Accept(ctx, state.Command{ID: id, RunID: runID, Kind: state.InputKind(message), Text: message})
	if err != nil {
		return state.Run{}, err
	}
	t.deliver(ctx)
	return run, nil
}

func (t *Tasks) Cancel(ctx context.Context, id string) (state.Run, error) {
	commandID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(id+"-cancel")).String()
	run, err := t.Store.Accept(ctx, state.Command{ID: commandID, RunID: id, Kind: "cancel"})
	if err != nil {
		return state.Run{}, err
	}
	t.deliver(ctx)
	control, err := t.Store.Control(ctx)
	if err == nil {
		if paused := control.Check(); paused != nil {
			repair, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := task.RepairClosed(repair, t.Store, t.Client, t.Computer); err != nil {
				log.Printf("Stop recovery pending: %v", err)
			}
			if latest, err := t.Store.GetRun(repair, id); err == nil {
				run = latest
			}
		}
	}
	return run, nil
}
