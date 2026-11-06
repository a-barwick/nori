package firestore

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"nori/internal/observability"
	"nori/internal/state"
)

// AcceptCommandBatch validates every command before writing any of them. The caller's IDs are the durable delivery
// intents, including on HTTP retry.
func (d *Database) AcceptCommandBatch(ctx context.Context, commands []state.Command, fence *state.ConversationFence) ([]state.CommandReceipt, error) {
	if err := state.ValidateCommandBatch(commands); err != nil {
		return nil, err
	}
	var receipts []state.CommandReceipt
	traceContext := observability.InjectTrace(ctx)
	err := d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		receipts = make([]state.CommandReceipt, 0, len(commands))
		batch, err := d.readCommandBatch(tx, commands)
		if err != nil {
			return err
		}
		control, err := d.admitCommandBatch(tx, commands, batch, fence)
		if err != nil {
			return err
		}
		prepared, err := batch.Prepare(commands, control, time.Now().UTC(), traceContext, d.taskBudgetUSD)
		receipts = prepared.Receipts
		if err != nil {
			return err
		}
		return d.writeCommandBatch(tx, batch, prepared, traceContext)
	})
	return receipts, err
}

// readCommandBatch completes all document reads before the transaction writes, including for repeated task IDs.
func (d *Database) readCommandBatch(tx *firestore.Transaction, commands []state.Command) (state.CommandBatch, error) {
	batch := state.NewCommandBatch()
	for _, cmd := range commands {
		doc, err := tx.Get(d.commands().Doc(cmd.ID))
		if err == nil {
			var saved state.Command
			if err = doc.DataTo(&saved); err != nil {
				return batch, err
			}
			batch.Prior[cmd.ID] = &saved
		} else if !notFound(err) {
			return batch, err
		}
		if _, read := batch.Exists[cmd.RunID]; read {
			continue
		}
		doc, err = tx.Get(d.runs().Doc(cmd.RunID))
		switch {
		case err == nil:
			run, err := decodeRun(doc, nil)
			if err != nil {
				return batch, err
			}
			batch.Runs[cmd.RunID], batch.Exists[cmd.RunID] = run, true
		case notFound(err):
			batch.Exists[cmd.RunID] = false
		default:
			return batch, err
		}
	}
	for _, id := range batch.Related(commands) {
		doc, err := tx.Get(d.runs().Doc(id))
		switch {
		case err == nil:
			run, err := decodeRun(doc, nil)
			if err != nil {
				return batch, err
			}
			batch.Runs[id], batch.Exists[id] = run, true
		case notFound(err):
			batch.Exists[id] = false
		default:
			return batch, err
		}
	}
	return batch, nil
}

// admitCommandBatch is part of the acceptance transaction: input acceptance and new task admission serialize on the
// same conversation control document. A batch of replays is not admitted again.
func (d *Database) admitCommandBatch(
	tx *firestore.Transaction, commands []state.Command, batch state.CommandBatch, fence *state.ConversationFence,
) (state.Control, error) {
	if batch.AllDuplicates(commands) {
		return state.Control{}, nil
	}
	if fence != nil {
		conversation, err := d.readConversation(tx)
		if err != nil {
			return state.Control{}, err
		}
		if err = state.CheckFresh(conversation, *fence, time.Now()); err != nil {
			return state.Control{}, err
		}
	}
	doc, err := tx.Get(d.control())
	if err != nil {
		return state.Control{}, err
	}
	var control state.Control
	if err = doc.DataTo(&control); err != nil {
		return state.Control{}, err
	}
	return control, nil
}

func (d *Database) writeCommandBatch(
	tx *firestore.Transaction, batch state.CommandBatch, prepared state.PreparedCommands, traceContext map[string]string,
) error {
	if len(prepared.Changed) == 0 && len(prepared.Accepted) == 0 && len(prepared.ReadyEvents) == 0 {
		return nil
	}
	log, err := d.openLog(tx)
	if err != nil {
		return err
	}
	if err := d.appendHeldEvents(tx, prepared.ReadyEvents, traceContext); err != nil {
		return err
	}
	if len(prepared.ReadyEvents) > 0 {
		if err := log.add(state.EventConversation, ""); err != nil {
			return err
		}
	}
	for id := range prepared.Changed {
		if err := tx.Set(d.runs().Doc(id), batch.Runs[id]); err != nil {
			return err
		}
		if err := log.add(state.EventTask, id); err != nil {
			return err
		}
	}
	for _, cmd := range prepared.Accepted {
		if err := tx.Create(d.commands().Doc(cmd.ID), cmd); err != nil {
			return err
		}
	}
	return nil
}

// appendHeldEvents handles registration after a task finished. Reading the run in the registration transaction
// serializes it against terminal publication. All event reads precede writes, including batches with several
// terminal tasks.
func (d *Database) appendHeldEvents(tx *firestore.Transaction, events []state.TaskEvent, traceContext map[string]string) error {
	if len(events) == 0 {
		return nil
	}
	for _, event := range events {
		_, err := tx.Get(d.conversationInputs().Doc(state.TaskEventInputID(event.ID)))
		if err == nil {
			return state.ErrDuplicate
		}
		if !notFound(err) {
			return err
		}
	}
	c, err := d.readConversation(tx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, event := range events {
		var input state.ConversationInput
		c, input = state.EnqueueTaskEvent(c, event, now, traceContext)
		if err = tx.Create(d.conversationInputs().Doc(input.ID), input); err != nil {
			return err
		}
	}
	return tx.Set(d.conversation(), c)
}

// appendTaskEvent is called after the task transaction's other reads and before its writes. The state transition and
// outer inbox insertion commit together. It reports false when the same event was already in the inbox.
func (d *Database) appendTaskEvent(tx *firestore.Transaction, event state.TaskEvent, traceContext map[string]string) (bool, error) {
	ref := d.conversationInputs().Doc(state.TaskEventInputID(event.ID))
	doc, err := tx.Get(ref)
	if err == nil {
		var prior state.ConversationInput
		if err := doc.DataTo(&prior); err != nil {
			return false, err
		}
		return false, state.ClassifyEventReplay(prior, event)
	}
	if !notFound(err) {
		return false, err
	}
	c, err := d.readConversation(tx)
	if err != nil {
		return false, err
	}
	c, input := state.EnqueueTaskEvent(c, event, time.Now().UTC(), traceContext)
	if err := tx.Set(d.conversation(), c); err != nil {
		return false, err
	}
	return true, tx.Create(ref, input)
}
