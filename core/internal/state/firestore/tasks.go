package firestore

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"nori/internal/observability"
	"nori/internal/state"
)

func (d *Database) Accept(ctx context.Context, cmd state.Command) (state.Run, error) {
	receipts, err := d.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
	if err != nil {
		return state.Run{}, err
	}
	return receipts[0].Run, nil
}

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
	_, err := d.commands().Doc(commandID).Update(ctx, []firestore.Update{{Path: "delivered", Value: true}})
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
	return d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
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
		updates := []firestore.Update{{Path: "incorporated", Value: req.Position}}
		if plan.ClearQuestion {
			updates = append(updates, firestore.Update{Path: "waiting", Value: ""}, firestore.Update{Path: "question", Value: firestore.Delete})
		}
		if err = log.add(state.EventTask, req.RunID); err != nil {
			return err
		}
		return tx.Update(ref, updates)
	})
}

// guard additionally fences new paid work while execution is paused.
func (d *Database) guard(tx *firestore.Transaction, runID string, revision int64) (state.Run, error) {
	run, err := decodeRun(tx.Get(d.runs().Doc(runID)))
	if err != nil {
		return run, err
	}
	if err = state.GuardRun(run, revision); err != nil {
		return run, err
	}
	doc, err := tx.Get(d.control())
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
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var err error
		run, err = d.guard(tx, runID, revision)
		return err
	})
	return run, err
}

func (d *Database) Admit(ctx context.Context, runID string, revision int64, operation string) error {
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, err := d.guard(tx, runID, revision); err != nil {
			return err
		}
		// A second activity attempt cannot silently execute an admitted operation.
		admitted := map[string]any{"id": operation, "revision": revision, "admitted_at": time.Now().UTC()}
		if err := tx.Create(d.runs().Doc(runID).Collection("operations").Doc(operation), admitted); err != nil {
			return err
		}
		return tx.Update(d.runs().Doc(runID), []firestore.Update{
			{Path: "operation_refs", Value: firestore.ArrayUnion(operation)}, {Path: "cleanup_pending", Value: true},
		})
	})
}

func (d *Database) UseApproval(ctx context.Context, runID string, revision int64, operation, commandID string) (state.OperationRecord, error) {
	var used state.OperationRecord
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, err := d.guard(tx, runID, revision); err != nil {
			return err
		}
		ref := d.runs().Doc(runID).Collection("operations").Doc(operation)
		doc, err := tx.Get(ref)
		if err != nil {
			return err
		}
		var op state.OperationRecord
		if err = doc.DataTo(&op); err != nil {
			return err
		}
		command, err := tx.Get(d.commands().Doc(commandID))
		if err != nil {
			return err
		}
		var cmd state.Command
		if err = command.DataTo(&cmd); err != nil {
			return err
		}
		if cmd.RunID != runID {
			return state.ErrQuestionStale
		}
		if used, err = state.UseApproval(op, cmd); err != nil {
			return err
		}
		return tx.Set(ref, map[string]any{"outcome": used.Outcome, "attempt": used.Attempt, "action": used.Action}, firestore.MergeAll)
	})
	return used, err
}

func (d *Database) Project(ctx context.Context, req state.ProjectRequest) error {
	return d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
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
		// A new projection supersedes the last job progress line.
		return tx.Update(d.runs().Doc(req.RunID), []firestore.Update{
			{Path: "waiting", Value: waiting}, {Path: "progress", Value: ""}, {Path: "events", Value: firestore.ArrayUnion(event)},
		})
	})
}

// SetProgress replaces the task's latest progress line. Unlike Project it adds no event, so polling a long job can't
// grow the task's saved record.
func (d *Database) SetProgress(ctx context.Context, runID string, revision int64, text string) error {
	return d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
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
		return tx.Update(d.runs().Doc(runID), []firestore.Update{{Path: "progress", Value: text}})
	})
}

// ProjectQuestion commits the durable wait and its outer inbox event together.
func (d *Database) ProjectQuestion(ctx context.Context, runID string, revision int64, question state.Question) error {
	traceContext := observability.InjectTrace(ctx)
	return d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
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
		// An offer's prep task waits quietly: the offer is how Austin hears about it.
		if run.Offer == "" {
			inserted, err := d.appendTaskEvent(tx, event, eventTraceContext)
			if err != nil || !inserted {
				return err
			}
			if err = log.add(state.EventConversation, ""); err != nil {
				return err
			}
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
	err := d.logged(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
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
				return tx.Update(ref, []firestore.Update{
					{Path: "publication_pending", Value: true}, {Path: "publication_generation", Value: run.PublicationGeneration},
				})
			}
		}
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		existing, err := d.runResults(tx, req.RunID)
		if err != nil {
			return err
		}
		var prior *state.Result
		if run.Revises != "" {
			if prior, err = d.readResult(tx, run.Revises); err != nil {
				return err
			}
		}
		var parent state.Run
		if run.ParentID != "" {
			if parent, err = decodeRun(tx.Get(d.runs().Doc(run.ParentID))); err != nil {
				return err
			}
		}
		// A parent that ends for any reason ends its subtasks, so this covers failure and recovery as well as Stop.
		children := map[string]state.Run{}
		for _, id := range state.RunningSubtasks(run) {
			if children[id], err = decodeRun(tx.Get(d.runs().Doc(id))); err != nil {
				return err
			}
		}
		var schedule *state.Schedule
		if run.Schedule != "" {
			if schedule, err = d.readSchedule(tx, run.Schedule); err != nil {
				return err
			}
		}
		finished, event := state.FinishRun(run, admitted, now)
		published, conflict := state.PublishResults(finished, admitted, existing, prior, now)
		if conflict != "" {
			finished.Result = conflict + "\n\n" + finished.Result
			event.Text = finished.Result
		}
		// An offer's prep task reports to its offer, not the conversation.
		announce := run.ParentID == "" && run.Offer == ""
		// A monitor check that found nothing new says nothing. One that failed or was stopped still reports.
		if run.Quiet && finished.Status == state.RunCompleted {
			alert, _ := state.MonitorOutcome(finished.Result)
			announce = announce && alert
			if schedule != nil {
				updated := state.ApplyMonitorResult(*schedule, finished.Result, now)
				schedule = &updated
			}
		} else {
			schedule = nil
		}
		var result *state.Command
		if run.ParentID != "" {
			var waiting bool
			parent, result, waiting = state.SettleSubtask(parent, run, finished, now)
			// A waiting parent receives this outcome with no model call. Holds still need their answer, and a subtask
			// that outlived its parent's wait may have done something Austin has to hear about.
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
		for id, child := range children {
			stopped, stop, changed := state.StopSubtask(child, req.RunID+":ended", now)
			if !changed {
				continue
			}
			if err = tx.Set(d.runs().Doc(id), stopped); err != nil {
				return err
			}
			if err = log.add(state.EventTask, id); err != nil {
				return err
			}
			if err = tx.Create(d.commands().Doc(stop.ID), stop); err != nil {
				return err
			}
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
		if schedule != nil {
			if err = tx.Set(d.schedules().Doc(schedule.ID), *schedule); err != nil {
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
	_, err := d.runs().Doc(runID).Update(ctx, []firestore.Update{{Path: "cleanup_pending", Value: false}})
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
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		run, err := decodeRun(tx.Get(d.runs().Doc(runID)))
		if err != nil {
			return err
		}
		if !state.ShouldClearPublication(run, generation) {
			return nil
		}
		return tx.Update(d.runs().Doc(runID), []firestore.Update{{Path: "publication_pending", Value: false}})
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
		d.runs().Doc(runID).Collection("steps").OrderBy("started_at", firestore.Desc).Limit(state.TaskStepLimit).Documents(ctx))
	if err != nil {
		return nil, err
	}
	return state.Chronological(steps), nil
}
