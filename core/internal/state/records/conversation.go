package records

import (
	"context"
	"fmt"
	"slices"
	"time"

	"nori/internal/observability"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) ConversationStatus(ctx context.Context) (state.ConversationControl, *state.ConversationTurn, error) {
	var c state.ConversationControl
	doc, err := d.conversation().Get(ctx)
	if notFound(err) {
		return c, nil, nil
	}
	if err != nil {
		return c, nil, err
	}
	if err = doc.DataTo(&c); err != nil {
		return c, nil, err
	}
	if c.Active == "" {
		return c, nil, nil
	}
	doc, err = d.conversationTurns().Doc(c.Active).Get(ctx)
	if err != nil {
		return c, nil, err
	}
	var t state.ConversationTurn
	err = doc.DataTo(&t)
	return c, &t, err
}

func (d *Database) UserRoutingPending(ctx context.Context) (bool, error) {
	c, _, err := d.ConversationStatus(ctx)
	return c.RoutingPending(), err
}

func (d *Database) AcceptConversationInput(ctx context.Context, id, text string, attachments []string) error {
	traceContext := observability.InjectTrace(ctx)
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(d.conversationInputs().Doc(id))
		if err == nil {
			var saved state.ConversationInput
			if err = doc.DataTo(&saved); err != nil {
				return err
			}
			return state.ReplayUserInput(saved, text, attachments)
		}
		if !notFound(err) {
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		c, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		attached, err := d.readAttachments(tx, attachments)
		if err != nil {
			return err
		}
		now := d.now()
		c, input, message := state.AdmitUserInput(c, id, text, attached, now, traceContext)
		if err = tx.Set(d.conversation(), c); err != nil {
			return err
		}
		if err = tx.Create(d.conversationInputs().Doc(id), input); err != nil {
			return err
		}
		if err = log.add(state.EventMessage, message.ID); err != nil {
			return err
		}
		if err = log.add(state.EventConversation, ""); err != nil {
			return err
		}
		if err = tx.Create(d.messages().Doc(message.ID), message); err != nil {
			return err
		}
		return tx.WakeUser(d.user.ID, docstore.LaneConversation, now)
	})
}

func (d *Database) inputBatch(tx *docstore.Transaction, after int64) ([]state.ConversationInput, error) {
	it := tx.Documents(d.conversationInputs().Where("position", ">", after).OrderBy("position", docstore.Asc).Limit(state.TurnInputLimit))
	defer it.Stop()
	batch := state.NewInputBatch()
	for {
		doc, err := it.Next()
		if isDone(err) {
			break
		}
		if err != nil {
			return nil, err
		}
		var input state.ConversationInput
		if err = doc.DataTo(&input); err != nil {
			return nil, err
		}
		if !batch.Add(input) {
			break
		}
	}
	return batch.Inputs, nil
}

func (d *Database) unroutedInputs(tx *docstore.Transaction, after, through int64) ([]state.ConversationInput, error) {
	it := tx.Documents(d.conversationInputs().Where("position", ">", after).Where("position", "<=", through).OrderBy("position", docstore.Asc))
	defer it.Stop()
	var routing state.RoutingContext
	for {
		doc, err := it.Next()
		if isDone(err) {
			return routing.Inputs, nil
		}
		if err != nil {
			return nil, err
		}
		var input state.ConversationInput
		if err = doc.DataTo(&input); err != nil {
			return nil, err
		}
		if err = routing.Add(input); err != nil {
			return nil, err
		}
	}
}

func (d *Database) markTurn(tx *docstore.Transaction, turn state.ConversationTurn) error {
	for _, input := range turn.Inputs {
		if !input.IsUser() {
			continue
		}
		update := []docstore.Update{{Path: "turn_id", Value: turn.ID}}
		if err := tx.Update(d.messages().Doc(state.UserMessageID(input.ID)), update); err != nil {
			return err
		}
	}
	return nil
}

func (d *Database) AcquireConversation(ctx context.Context, owner, turnID string) (state.ConversationControl, *state.ConversationTurn, error) {
	var c state.ConversationControl
	var turn *state.ConversationTurn
	err := d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		var err error
		c, err = d.readConversation(tx)
		if err != nil {
			return err
		}
		now := d.now()
		if err = state.CanAcquire(c, now); err != nil {
			return err
		}
		if state.Idle(c) {
			return nil
		}
		var t state.ConversationTurn
		if c.Active != "" {
			doc, err := tx.Get(d.conversationTurns().Doc(c.Active))
			if err != nil {
				return err
			}
			if err = doc.DataTo(&t); err != nil {
				return err
			}
		} else {
			inputs, err := d.inputBatch(tx, c.Consumed)
			if err != nil {
				return err
			}
			if len(inputs) == 0 {
				return nil
			}
			var unresolved []state.ConversationInput
			if state.NeedsUnrouted(c, inputs) {
				if unresolved, err = d.unroutedInputs(tx, c.UserRouted, c.Consumed); err != nil {
					return err
				}
			}
			t = state.NewTurn(turnID, inputs, unresolved, c, now)
			c.Active = t.ID
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		c = state.Lease(c, owner, now)
		turn = &t
		if err = tx.Set(d.conversation(), c); err != nil {
			return err
		}
		if err = d.markTurn(tx, t); err != nil {
			return err
		}
		if err = log.add(state.EventConversation, ""); err != nil {
			return err
		}
		return tx.Set(d.conversationTurns().Doc(t.ID), t)
	})
	return c, turn, err
}

func (d *Database) CheckConversation(ctx context.Context, f state.ConversationFence) (bool, error) {
	doc, err := d.conversation().Get(ctx)
	if err != nil {
		return false, err
	}
	var c state.ConversationControl
	if err = doc.DataTo(&c); err != nil {
		return false, err
	}
	if err = state.CheckOwned(c, f, d.now()); err != nil {
		return false, err
	}
	return state.StaleFor(c, f), nil
}

func (d *Database) RenewConversation(ctx context.Context, f state.ConversationFence) (bool, error) {
	stale := false
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		c, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		c, stale, err = state.Renew(c, f, d.now())
		if err != nil {
			return err
		}
		return tx.Set(d.conversation(), c)
	})
	return stale, err
}

func (d *Database) ReleaseConversation(ctx context.Context, f state.ConversationFence) error {
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		c, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		now := d.now()
		if c, err = state.Release(c, f, now); err != nil {
			return err
		}
		if err = log.add(state.EventConversation, ""); err != nil {
			return err
		}
		if err = tx.Set(d.conversation(), c); err != nil {
			return err
		}
		if c.Pending() || c.Active != "" {
			if err = tx.WakeUser(d.user.ID, docstore.LaneConversation, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// SaveConversation fences every checkpoint, including model replies and failed attempts.
func (d *Database) SaveConversation(ctx context.Context, f state.ConversationFence, t state.ConversationTurn) error {
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		c, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		if err = state.CheckOwned(c, f, d.now()); err != nil {
			return err
		}
		if err = log.add(state.EventConversation, ""); err != nil {
			return err
		}
		if err = tx.Set(d.conversationTurns().Doc(t.ID), t); err != nil {
			return err
		}
		if t.Attempt.ID != "" {
			return tx.Set(d.conversationAttempts().Doc(t.Attempt.ID), t.Attempt)
		}
		return nil
	})
}

// RestartConversation incorporates the full waiting batch only after one quiet second. The fourth attempt freezes
// its coverage; later input remains queued for the next turn.
func (d *Database) RestartConversation(
	ctx context.Context, f state.ConversationFence, t state.ConversationTurn,
) (state.ConversationTurn, time.Duration, error) {
	var wait time.Duration
	original := t
	err := d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		t = original
		c, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		if err = state.CheckOwned(c, f, d.now()); err != nil {
			return err
		}
		var restart bool
		restart, wait = state.RestartWait(c, t, d.now())
		if !restart {
			return nil
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		inputs, err := d.inputBatch(tx, c.Consumed)
		if err != nil {
			return err
		}
		if c.UserRouted < c.Consumed {
			unresolved, err := d.unroutedInputs(tx, c.UserRouted, c.Consumed)
			if err != nil {
				return err
			}
			inputs = append(unresolved, inputs...)
		}
		result, err := state.RestartTurn(c, t, inputs, d.now())
		if err != nil {
			return err
		}
		t = result.Turn
		if result.Superseded != nil {
			if err = tx.Set(d.conversationAttempts().Doc(result.Superseded.ID), *result.Superseded); err != nil {
				return err
			}
		}
		if result.Replaced {
			if err = d.markTurn(tx, t); err != nil {
				return err
			}
		}
		if err = log.add(state.EventConversation, ""); err != nil {
			return err
		}
		return tx.Set(d.conversationTurns().Doc(t.ID), t)
	})
	return t, wait, err
}

func (d *Database) PublishConversation(ctx context.Context, f state.ConversationFence, t state.ConversationTurn, text string) error {
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		c, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		if err = state.CheckFresh(c, f, d.now()); err != nil {
			return err
		}
		heldRuns, err := d.holdAcknowledgements(tx, t, text)
		if err != nil {
			return err
		}
		results, err := d.replyResults(tx, t)
		if err != nil {
			return err
		}
		memories, err := d.readMemoryChanges(tx, t.Attempt.Memory)
		if err != nil {
			return err
		}
		now := d.now()
		if err = d.writeMemoryChanges(tx, t, memories, now); err != nil {
			return err
		}
		wasRoutingPending := c.RoutingPending()
		c, completed, message := state.CompleteTurn(c, t, text, now)
		marked, parts := state.ReportResults(results, t.Attempt.ShowResults, message.ID, now)
		message.Parts = append(message.Parts, parts...)
		seenParts := map[[2]string]bool{}
		for _, p := range message.Parts {
			if p.ID != "" {
				seenParts[[2]string{p.Type, p.ID}] = true
			}
		}
		for _, in := range t.Inputs {
			if in.Event == nil || len(in.Event.Parts) == 0 {
				continue
			}
			for _, p := range in.Event.Parts {
				key := [2]string{p.Type, p.ID}
				if p.ID != "" && seenParts[key] {
					continue
				}
				if p.ID != "" {
					seenParts[key] = true
				}
				message.Parts = append(message.Parts, p)
			}
		}
		for _, result := range marked {
			message.ResultIDs = append(message.ResultIDs, result.ID)
			if err = tx.Set(d.results().Doc(result.ID), result); err != nil {
				return err
			}
			if err = log.add(state.EventResult, result.ID); err != nil {
				return err
			}
		}
		heldIDs := make([]string, 0, len(heldRuns))
		for id := range heldRuns {
			heldIDs = append(heldIDs, id)
		}
		slices.Sort(heldIDs)
		for _, id := range heldIDs {
			run := heldRuns[id]
			if err = tx.Set(d.runs().Doc(id), run); err != nil {
				return err
			}
			if err = log.add(state.EventTask, id); err != nil {
				return err
			}
		}
		if err = log.add(state.EventMessage, message.ID); err != nil {
			return err
		}
		if err = log.add(state.EventConversation, ""); err != nil {
			return err
		}
		if err = tx.Set(d.conversation(), c); err != nil {
			return err
		}
		if err = tx.Set(d.conversationTurns().Doc(completed.ID), completed); err != nil {
			return err
		}
		if err = tx.Set(d.conversationAttempts().Doc(completed.Attempt.ID), completed.Attempt); err != nil {
			return err
		}
		if err = tx.Set(d.messages().Doc(message.ID), message); err != nil {
			return err
		}
		if wasRoutingPending && !c.RoutingPending() {
			if err = tx.WakeWaitingTasks(d.user.ID, docstore.WaitRoutingPending); err != nil {
				return err
			}
		}
		if c.Pending() {
			if err = tx.WakeUser(d.user.ID, docstore.LaneConversation, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// holdAcknowledgements reads before PublishConversation writes anything. The held answer and its acknowledgement
// then commit in the same transaction.
func (d *Database) holdAcknowledgements(tx *docstore.Transaction, t state.ConversationTurn, text string) (map[string]state.Run, error) {
	runs := map[string]state.Run{}
	targets := state.HoldTargets(t)
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		run, err := decodeRun(tx.Get(d.runs().Doc(id)))
		if err != nil {
			return nil, err
		}
		runs[id] = state.AcknowledgeHolds(run, targets[id], t, text)
	}
	return runs, nil
}

func (d *Database) ConversationTraces(ctx context.Context) ([]state.ConversationAttempt, error) {
	return documents[state.ConversationAttempt](
		d.conversationAttempts().OrderBy("started_at", docstore.Desc).Limit(state.TraceLimit).Documents(ctx))
}

func (d *Database) ConversationMessages(ctx context.Context, before string) ([]state.Message, string, error) {
	q := d.messages().OrderBy("created_at", docstore.Desc).OrderBy(docstore.DocumentID, docstore.Desc)
	if before != "" {
		doc, err := d.messages().Doc(before).Get(ctx)
		if err != nil {
			return nil, "", fmt.Errorf("invalid history cursor: %w", err)
		}
		q = q.StartAfter(doc)
	}
	messages, err := documents[state.Message](q.Limit(state.MessagePageSize + 1).Documents(ctx))
	if err != nil {
		return nil, "", err
	}
	page, next := state.MessagePage(messages)
	return page, next, nil
}

// ConversationCommand recovers an accepted action after a response/checkpoint failure. The command is the immutable
// receipt; Run is the current projection.
func (d *Database) ConversationCommand(ctx context.Context, id string) (*state.CommandReceipt, error) {
	doc, err := d.commands().Doc(id).Get(ctx)
	if notFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var command state.Command
	if err := doc.DataTo(&command); err != nil {
		return nil, err
	}
	run, err := d.GetRun(ctx, command.RunID)
	if err != nil {
		return nil, err
	}
	return &state.CommandReceipt{Command: command, Run: run}, nil
}

// EnsureConversationIntroduction shares admission ordering with real messages. Concurrent first loads enqueue one
// introduction; existing history is untouched.
func (d *Database) EnsureConversationIntroduction(ctx context.Context, prompt string) error {
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		c, err := d.readConversation(tx)
		if err != nil || !state.NeedsIntroduction(c) {
			return err
		}
		messages := tx.Documents(d.messages().Limit(1))
		defer messages.Stop()
		if _, err = messages.Next(); err == nil {
			return nil
		} else if !isDone(err) {
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		now := d.now()
		c, input := state.Introduce(c, prompt, now)
		if err = tx.Set(d.conversation(), c); err != nil {
			return err
		}
		if err = log.add(state.EventConversation, ""); err != nil {
			return err
		}
		if err = tx.Create(d.conversationInputs().Doc(input.ID), input); err != nil {
			return err
		}
		return tx.WakeUser(d.user.ID, docstore.LaneConversation, now)
	})
}

// CancelConversation fences only the named reply. Accepted task commands remain
// durable and inputs after this turn's coverage remain queued. Retrying a stop
// cannot cancel a later turn. An input ID can stop its queued batch before acquire.
func (d *Database) CancelConversation(ctx context.Context, turnID string) error {
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		c, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		doc, err := tx.Get(d.conversationTurns().Doc(turnID))
		var t state.ConversationTurn
		if err == nil {
			if err = doc.DataTo(&t); err != nil {
				return err
			}
			if t.Status == "cancelled" || t.Status == "completed" {
				return nil
			}
			if c.Active != turnID {
				return state.ErrConversationStale
			}
		} else if notFound(err) {
			if c.Active != "" {
				return state.ErrConversationStale
			}
			inputs, err := d.inputBatch(tx, c.Consumed)
			if err != nil {
				return err
			}
			// Only the first queued input names this batch. Newer inputs are not consumed.
			if len(inputs) == 0 || inputs[0].ID != turnID {
				return state.ErrConversationStale
			}
			t = state.NewTurn(turnID, inputs[:1], nil, c, d.now())
		} else {
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		now := d.now()
		wasRoutingPending := c.RoutingPending()
		t.Attempt.Status = "cancelled"
		c, t, message := state.CompleteTurn(c, t, "Reply stopped.", now)
		c.Fence++
		t.Status, message.Status = "cancelled", "cancelled"
		if t.Attempt.ID != "" {
			t.Attempt.Status, t.Attempt.FinishedAt = "cancelled", &now
			t.Attempt.DurationMS = now.Sub(t.Attempt.StartedAt).Milliseconds()
			if err = tx.Set(d.conversationAttempts().Doc(t.Attempt.ID), t.Attempt); err != nil {
				return err
			}
		}
		for _, write := range []func() error{
			func() error { return tx.Set(d.conversation(), c) },
			func() error { return tx.Set(d.conversationTurns().Doc(t.ID), t) },
			func() error { return tx.Set(d.messages().Doc(message.ID), message) },
			func() error { return log.add(state.EventMessage, message.ID) },
			func() error { return log.add(state.EventConversation, "") },
		} {
			if err = write(); err != nil {
				return err
			}
		}
		if wasRoutingPending && !c.RoutingPending() {
			if err = tx.WakeWaitingTasks(d.user.ID, docstore.WaitRoutingPending); err != nil {
				return err
			}
		}
		if c.Pending() {
			if err = tx.WakeUser(d.user.ID, docstore.LaneConversation, now); err != nil {
				return err
			}
		}
		return nil
	})
}
