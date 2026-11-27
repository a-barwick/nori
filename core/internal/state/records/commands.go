package records

import (
	"context"
	"slices"

	"nori/internal/observability"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) AcceptCommandBatch(ctx context.Context, commands []state.Command, fence *state.ConversationFence) ([]state.CommandReceipt, error) {
	if err := state.ValidateCommandBatch(commands); err != nil {
		return nil, err
	}
	var receipts []state.CommandReceipt
	traceContext := observability.InjectTrace(ctx)
	budget := d.preferredTaskBudget(ctx)
	err := d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		receipts = make([]state.CommandReceipt, 0, len(commands))
		batch, err := d.readCommandBatch(tx, commands)
		if err != nil {
			return err
		}
		before := map[string]int64{}
		runIDs := make([]string, 0, len(batch.Runs))
		for id, run := range batch.Runs {
			before[id] = run.Revision
			runIDs = append(runIDs, id)
		}
		slices.Sort(runIDs)
		if d.PinAccountModelSelection != nil {
			batch.AccountModels = true
			for _, command := range commands {
				if command.Kind == state.CommandStart && batch.Prior[command.ID] == nil {
					batch.AccountModelSelection, err = d.readAccountModelSelection(tx)
					if err != nil {
						return err
					}
					break
				}
			}
		}
		pending, err := d.readPendingInteractions(tx, runIDs)
		if err != nil {
			return err
		}
		control, err := d.admitCommandBatch(tx, commands, batch, fence)
		if err != nil {
			return err
		}
		prepared, err := batch.Prepare(commands, control, d.now(), traceContext, budget)
		receipts = prepared.Receipts
		if err != nil {
			return err
		}
		if err = d.writeCommandBatch(tx, batch, prepared, traceContext); err != nil {
			return err
		}
		return d.supersedeInteractions(tx, pending, before, batch, commands)
	})
	return receipts, err
}

func (d *Database) readAccountModelSelection(tx *docstore.Transaction) (*state.ModelSelection, error) {
	doc, err := tx.Get(d.resources(state.ModelSettingsCollection).Doc(state.ModelSettingsID))
	if notFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	row, err := toResource(state.ModelSettingsCollection, doc)
	if err != nil {
		return nil, err
	}
	var selection state.ModelSelection
	if err := row.Decode(&selection); err != nil {
		return nil, err
	}
	return d.PinAccountModelSelection(selection.TaskOnly()), nil
}

func (d *Database) readCommandBatch(tx *docstore.Transaction, commands []state.Command) (state.CommandBatch, error) {
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

func (d *Database) admitCommandBatch(
	tx *docstore.Transaction, commands []state.Command, batch state.CommandBatch, fence *state.ConversationFence,
) (state.Control, error) {
	if batch.AllDuplicates(commands) {
		return state.Control{}, nil
	}
	if fence != nil {
		conversation, err := d.readConversation(tx)
		if err != nil {
			return state.Control{}, err
		}
		if err = state.CheckFresh(conversation, *fence, d.now()); err != nil {
			return state.Control{}, err
		}
	}
	doc, err := tx.Get(controlDoc(d.db))
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
	tx *docstore.Transaction, batch state.CommandBatch, prepared state.PreparedCommands, traceContext map[string]string,
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
	now := d.now()
	if len(prepared.ReadyEvents) > 0 {
		if err := log.add(state.EventConversation, ""); err != nil {
			return err
		}
		if err := tx.WakeUser(d.user.ID, docstore.LaneConversation, now); err != nil {
			return err
		}
	}
	changedIDs := make([]string, 0, len(prepared.Changed))
	for id := range prepared.Changed {
		changedIDs = append(changedIDs, id)
	}
	slices.Sort(changedIDs)
	for _, id := range changedIDs {
		if err := tx.Set(d.runs().Doc(id), batch.Runs[id]); err != nil {
			return err
		}
		if err := log.add(state.EventTask, id); err != nil {
			return err
		}
	}
	wokenRuns := make(map[string]bool, len(prepared.Accepted))
	for _, cmd := range prepared.Accepted {
		if err := tx.Create(d.commands().Doc(cmd.ID), cmd); err != nil {
			return err
		}
		if cmd.Kind != state.CommandHold && !wokenRuns[cmd.RunID] {
			wokenRuns[cmd.RunID] = true
			if err := tx.WakeTask(d.user.ID, cmd.RunID, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *Database) appendHeldEvents(tx *docstore.Transaction, events []state.TaskEvent, traceContext map[string]string) error {
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
	now := d.now()
	for _, event := range events {
		var input state.ConversationInput
		c, input = state.EnqueueTaskEvent(c, event, now, traceContext)
		if err = tx.Create(d.conversationInputs().Doc(input.ID), input); err != nil {
			return err
		}
	}
	return tx.Set(d.conversation(), c)
}

func (d *Database) appendTaskEvent(tx *docstore.Transaction, run state.Run, event state.TaskEvent, traceContext map[string]string) (bool, error) {
	if !reportsToAssistant(run) {
		return false, nil
	}
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
	now := d.now()
	c, input := state.EnqueueTaskEvent(c, event, now, traceContext)
	if err := tx.Set(d.conversation(), c); err != nil {
		return false, err
	}
	if err := tx.Create(ref, input); err != nil {
		return false, err
	}
	if err := tx.WakeUser(d.user.ID, docstore.LaneConversation, now); err != nil {
		return false, err
	}
	return true, nil
}

func reportsToAssistant(run state.Run) bool {
	if run.Origin == nil {
		return true
	}
	if run.Origin.ConversationID == "automation" {
		return false
	}
	return run.Origin.ConversationID == "" || run.Origin.ConversationID == "assistant"
}

func (d *Database) preferredTaskBudget(ctx context.Context) float64 {
	r, err := d.Resource(ctx, "preferences", "preferences")
	if err != nil {
		return d.taskBudgetUSD
	}
	var prefs struct {
		Budgets struct {
			TaskUSD float64 `json:"task_usd"`
		} `json:"budgets"`
	}
	if r.Decode(&prefs) != nil || prefs.Budgets.TaskUSD <= 0 {
		return d.taskBudgetUSD
	}
	return min(prefs.Budgets.TaskUSD, state.MaxContinuationBudgetUSD)
}
