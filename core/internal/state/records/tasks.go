package records

import (
	"context"
	"time"

	"nori/internal/observability"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

// OpenTasks is operational state, independent of the bounded UI projection.
func (d *Database) OpenTasks(ctx context.Context) ([]state.Run, error) {
	return documents[state.Run](d.runs().Where("status", "==", "running").Documents(ctx))
}

func (d *Database) Pending(ctx context.Context) ([]state.Command, error) {
	commands, err := documents[state.Command](d.commands().Where("delivered", "==", false).Limit(state.PendingCommandLimit).Documents(ctx))
	if err != nil {
		return nil, err
	}
	return state.OrderPending(commands), nil
}

func (d *Database) Delivered(ctx context.Context, commandID string) error {
	_, err := d.commands().Doc(commandID).Update(ctx, []docstore.Update{{Path: "delivered", Value: true}})
	return err
}

func (d *Database) Reconcile(ctx context.Context, runID string) (state.TaskSnapshot, error) {
	run, err := d.GetRun(ctx, runID)
	if err != nil {
		return state.TaskSnapshot{}, err
	}
	snapshot := state.TaskSnapshot{Run: run, Commands: []state.Command{}}
	commands, err := documents[state.Command](d.commands().Where("run_id", "==", runID).Documents(ctx))
	if err != nil {
		return snapshot, err
	}
	snapshot.Commands = state.ReconcileCommands(run, commands)
	return snapshot, nil
}

func (d *Database) Incorporate(ctx context.Context, req state.IncorporateRequest) error {
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		ref := d.runs().Doc(req.RunID)
		run, err := decodeRun(tx.Get(ref))
		if err != nil {
			return err
		}
		plan, err := state.PlanIncorporation(run, req.Revision, req.Position)
		if err != nil || plan.Noop {
			// A wake without new commands must not rewrite the task while a user answers its question. The
			// revision and position fence still applies, and the read transaction ends without writing.
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		updates := []docstore.Update{{Path: "incorporated", Value: req.Position}}
		if plan.ClearQuestion {
			updates = append(updates, docstore.Update{Path: "waiting", Value: ""}, docstore.Update{Path: "question", Value: docstore.Delete})
		}
		if err = log.add(state.EventTask, req.RunID); err != nil {
			return err
		}
		return tx.Update(ref, updates)
	})
}

// guard additionally fences new paid work while execution is paused.
func (d *Database) guard(tx *docstore.Transaction, runID string, revision int64) (state.Run, error) {
	run, err := decodeRun(tx.Get(d.runs().Doc(runID)))
	if err != nil {
		return run, err
	}
	if err = state.GuardRun(run, revision); err != nil {
		return run, err
	}
	doc, err := tx.Get(controlDoc(d.db))
	if err != nil {
		return run, err
	}
	var control state.Control
	if err = doc.DataTo(&control); err != nil {
		return run, err
	}
	return run, control.Check()
}

func (d *Database) Guard(ctx context.Context, runID string, revision int64) (state.Run, error) {
	var run state.Run
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		var err error
		run, err = d.guard(tx, runID, revision)
		return err
	})
	return run, err
}

func (d *Database) Admit(ctx context.Context, runID string, revision int64, operation string) error {
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		if _, err := d.guard(tx, runID, revision); err != nil {
			return err
		}
		// A second activity attempt cannot silently execute an admitted operation.
		admitted := map[string]any{"id": operation, "revision": revision, "admitted_at": time.Now().UTC()}
		if err := tx.Create(d.runs().Doc(runID).Collection("operations").Doc(operation), admitted); err != nil {
			return err
		}
		return tx.Update(d.runs().Doc(runID), []docstore.Update{
			{Path: "operation_refs", Value: docstore.ArrayUnion(operation)}, {Path: "cleanup_pending", Value: true},
		})
	})
}

func (d *Database) Project(ctx context.Context, req state.ProjectRequest) error {
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		if _, err := d.guard(tx, req.RunID, req.Revision); err != nil {
			return err
		}
		waiting, event := state.ProjectionEvent(req.Kind, req.Text, time.Now().UTC())
		if err = log.add(state.EventTask, req.RunID); err != nil {
			return err
		}
		return tx.Update(d.runs().Doc(req.RunID), []docstore.Update{
			{Path: "waiting", Value: waiting}, {Path: "progress", Value: ""}, {Path: "events", Value: docstore.ArrayUnion(event)},
		})
	})
}

// SetProgress replaces the task's latest progress line. Unlike Project it adds no event, so polling a long job can't
// grow the task's saved record.
func (d *Database) SetProgress(ctx context.Context, runID string, revision int64, text string) error {
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		run, err := d.guard(tx, runID, revision)
		if err != nil {
			return err
		}
		// A long job is polled often, and an unchanged line is nothing to save or send.
		if run.Progress == text {
			return nil
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		if err = log.add(state.EventTask, runID); err != nil {
			return err
		}
		return tx.Update(d.runs().Doc(runID), []docstore.Update{{Path: "progress", Value: text}})
	})
}

// ProjectQuestion commits the durable wait and its outer inbox event together.
func (d *Database) ProjectQuestion(ctx context.Context, runID string, revision int64, question state.Question) error {
	traceContext := observability.InjectTrace(ctx)
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		run, err := d.guard(tx, runID, revision)
		if err != nil {
			return err
		}
		event, replay, err := state.QuestionEvent(run, revision, question, time.Now().UTC())
		if err != nil || replay {
			return err
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		eventTraceContext := traceContext
		if len(eventTraceContext) == 0 {
			eventTraceContext = run.TraceContext
		}
		inserted, err := d.appendTaskEvent(tx, event, eventTraceContext)
		if err != nil || !inserted {
			return err
		}
		if err = log.add(state.EventConversation, ""); err != nil {
			return err
		}
		if err = log.add(state.EventTask, runID); err != nil {
			return err
		}
		return tx.Set(d.runs().Doc(runID), state.WithQuestion(run, question, event))
	})
}

func (d *Database) Publish(ctx context.Context, req state.PublishRequest) (state.Run, error) {
	var run state.Run
	routingPending := false
	traceContext := observability.InjectTrace(ctx)
	err := d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		routingPending = false
		ref := d.runs().Doc(req.RunID)
		var err error
		run, err = decodeRun(tx.Get(ref))
		if err != nil {
			return err
		}
		admitted, proceed, err := state.AdmitPublication(run, req)
		if err != nil || !proceed {
			return err
		}
		if admitted.GatesOnRouting() {
			conversation, err := d.readConversation(tx)
			if err != nil {
				return err
			}
			if conversation.RoutingPending() {
				routingPending = true
				run = state.DeferPublication(run)
				return tx.Update(ref, []docstore.Update{
					{Path: "publication_pending", Value: true}, {Path: "publication_generation", Value: run.PublicationGeneration},
				})
			}
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		in, err := d.publishReads(tx, req.RunID, run)
		if err != nil {
			return err
		}
		parent := in.parent
		finished, event := state.FinishRun(run, admitted, now)
		published, conflict := state.PublishResults(finished, admitted, in.existing, in.prior, now)
		if conflict != "" {
			finished.Result = conflict + "\n\n" + finished.Result
			event.Text = finished.Result
		}
		announce := run.ParentID == ""
		var result *state.Command
		if run.ParentID != "" {
			var waiting bool
			parent, result, waiting = state.SettleSubtask(parent, run, finished, now)
			// A waiting parent receives this outcome with no model call. Holds still need their answer, and a subtask
			// that outlived its parent's wait may have done something the user has to hear about.
			announce = len(event.Holds) > 0 || (!waiting && len(finished.OperationRefs) > 0)
		}
		if announce {
			eventTraceContext := traceContext
			if len(eventTraceContext) == 0 {
				eventTraceContext = run.TraceContext
			}
			inserted, err := d.appendTaskEvent(tx, event, eventTraceContext)
			if err != nil {
				return err
			}
			if inserted {
				finished.EventSequence = event.Sequence
				if err = log.add(state.EventConversation, ""); err != nil {
					return err
				}
			}
		}
		run = finished
		if err = log.add(state.EventTask, req.RunID); err != nil {
			return err
		}
		for _, result := range published {
			if err = tx.Set(d.results().Doc(result.ID), result); err != nil {
				return err
			}
			if err = log.add(state.EventResult, result.ID); err != nil {
				return err
			}
		}
		if err = d.stopSubtasks(tx, log, in.children, req.RunID, now); err != nil {
			return err
		}
		if run.ParentID != "" {
			if err = tx.Set(d.runs().Doc(run.ParentID), parent); err != nil {
				return err
			}
			if err = log.add(state.EventTask, run.ParentID); err != nil {
				return err
			}
		}
		if result != nil {
			if err = tx.Create(d.commands().Doc(result.ID), *result); err != nil {
				return err
			}
		}
		return tx.Set(ref, run)
	})
	if err == nil && routingPending {
		err = state.ErrRoutingPending
	}
	return run, err
}

// publishInputs is what publishing a task reads. A Firestore transaction must finish its reads before it writes.
type publishInputs struct {
	existing []state.Result
	prior    *state.Result
	parent   state.Run
	children map[string]state.Run
}

func (d *Database) publishReads(tx *docstore.Transaction, runID string, run state.Run) (publishInputs, error) {
	var in publishInputs
	var err error
	if in.existing, err = d.runResults(tx, runID); err != nil {
		return in, err
	}
	if run.Revises != "" {
		if in.prior, err = d.readResult(tx, run.Revises); err != nil {
			return in, err
		}
	}
	if run.ParentID != "" {
		if in.parent, err = decodeRun(tx.Get(d.runs().Doc(run.ParentID))); err != nil {
			return in, err
		}
	}
	// A parent that ends for any reason ends its subtasks, so this covers failure and recovery as well as Stop.
	in.children = map[string]state.Run{}
	for _, id := range state.RunningSubtasks(run) {
		if in.children[id], err = decodeRun(tx.Get(d.runs().Doc(id))); err != nil {
			return in, err
		}
	}
	return in, nil
}

func (d *Database) stopSubtasks(tx *docstore.Transaction, log *eventLog, children map[string]state.Run, runID string, now time.Time) error {
	for id, child := range children {
		stopped, stop, changed := state.StopSubtask(child, runID+":ended", now)
		if !changed {
			continue
		}
		if err := tx.Set(d.runs().Doc(id), stopped); err != nil {
			return err
		}
		if err := log.add(state.EventTask, id); err != nil {
			return err
		}
		if err := tx.Create(d.commands().Doc(stop.ID), stop); err != nil {
			return err
		}
	}
	return nil
}

// OperationRevisions identifies only scopes that were durably admitted, including attempts whose worker died before
// their RPC outcome reached Temporal.
func (d *Database) OperationRevisions(ctx context.Context, runID string) ([]int64, error) {
	iter := d.runs().Doc(runID).Collection("operations").Documents(ctx)
	defer iter.Stop()
	revisions := []int64{}
	for {
		doc, err := iter.Next()
		if isDone(err) {
			break
		}
		if err != nil {
			return nil, err
		}
		revision, _ := doc.Data()["revision"].(int64)
		revisions = append(revisions, revision)
	}
	return state.UniqueRevisions(revisions), nil
}

func (d *Database) CleanupTasks(ctx context.Context) ([]state.Run, error) {
	return documents[state.Run](d.runs().Where("cleanup_pending", "==", true).Documents(ctx))
}

func (d *Database) FinishCleanup(ctx context.Context, runID string) error {
	_, err := d.runs().Doc(runID).Update(ctx, []docstore.Update{{Path: "cleanup_pending", Value: false}})
	return err
}

func (d *Database) PublicationTasks(ctx context.Context) ([]state.Run, error) {
	runs, err := documents[state.Run](d.runs().Where("publication_pending", "==", true).Documents(ctx))
	if err != nil {
		return nil, err
	}
	return state.OpenOnly(runs), nil
}

func (d *Database) PublicationWoken(ctx context.Context, runID string, generation int64) error {
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		run, err := decodeRun(tx.Get(d.runs().Doc(runID)))
		if err != nil {
			return err
		}
		if !state.ShouldClearPublication(run, generation) {
			return nil
		}
		return tx.Update(d.runs().Doc(runID), []docstore.Update{{Path: "publication_pending", Value: false}})
	})
}

func (d *Database) RecordTaskStep(ctx context.Context, runID string, step state.TaskStep) error {
	_, err := d.runs().Doc(runID).Collection("steps").Doc(step.ID).Set(ctx, step)
	return err
}

// TaskSteps is fetched only when a trace is opened, not with every session poll.
func (d *Database) TaskSteps(ctx context.Context, runID string) ([]state.TaskStep, error) {
	if _, err := d.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	steps, err := documents[state.TaskStep](
		d.runs().Doc(runID).Collection("steps").OrderBy("started_at", docstore.Desc).Limit(state.TaskStepLimit).Documents(ctx))
	if err != nil {
		return nil, err
	}
	return state.Chronological(steps), nil
}
