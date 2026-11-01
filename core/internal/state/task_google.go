package state

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"nori/internal/observability"
)

func (s *Google) commands() *firestore.CollectionRef { return s.root().Collection("task_commands") }

func (s *Google) Accept(ctx context.Context, cmd Command) (Run, error) {
	receipts, err := s.AcceptCommandBatch(ctx, []Command{cmd}, nil)
	if err != nil {
		return Run{}, err
	}
	return receipts[0].Run, nil
}

func sameCommand(a, b Command) bool {
	return a.RunID == b.RunID && a.Kind == b.Kind && a.Text == b.Text && a.QuestionID == b.QuestionID && a.Interrupt == b.Interrupt && slices.Equal(a.SourceInputIDs, b.SourceInputIDs)
}

// AcceptCommandBatch validates every command before writing any of them. The
// caller's IDs are the durable delivery intents, including on HTTP retry.
func (s *Google) AcceptCommandBatch(ctx context.Context, commands []Command, fence *ConversationFence) ([]CommandReceipt, error) {
	if len(commands) == 0 || len(commands) > 32 {
		return nil, fmt.Errorf("%w: a command batch needs 1–32 commands", ErrCommandInvalid)
	}
	var receipts []CommandReceipt
	traceContext := observability.InjectTrace(ctx)
	err := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		receipts = make([]CommandReceipt, 0, len(commands))
		batch, err := s.readCommandBatch(tx, commands)
		if err != nil {
			return err
		}
		control, err := s.admitCommandBatch(tx, commands, batch, fence)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		prepared, err := batch.prepare(commands, control, now, traceContext)
		receipts = prepared.receipts
		if err != nil {
			return err
		}
		return s.writeCommandBatch(tx, batch, prepared, now, traceContext, fence)
	})
	return receipts, err
}

// OpenTasks is operational state, independent of the bounded UI projection.
func (s *Google) OpenTasks(ctx context.Context) ([]Run, error) {
	iter := s.runs().Where("status", "==", "running").Documents(ctx)
	defer iter.Stop()
	runs := []Run{}
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		run, err := decodeRun(doc, nil)
		if err != nil {
			return nil, err
		}
		if run.Temporal {
			runs = append(runs, run)
		}
	}
	return runs, nil
}

func (s *Google) Pending(ctx context.Context) ([]Command, error) {
	iter := s.commands().Where("delivered", "==", false).Limit(100).Documents(ctx)
	defer iter.Stop()
	commands := []Command{}
	for {
		d, e := iter.Next()
		if e == iterator.Done {
			break
		}
		if e != nil {
			return nil, e
		}
		var c Command
		if e = d.DataTo(&c); e != nil {
			return nil, e
		}
		commands = append(commands, c)
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].CreatedAt.Before(commands[j].CreatedAt) })
	return commands, nil
}
func (s *Google) Delivered(ctx context.Context, id string) error {
	_, e := s.commands().Doc(id).Update(ctx, []firestore.Update{{Path: "delivered", Value: true}})
	return e
}
func (s *Google) Reconcile(ctx context.Context, id string) (TaskSnapshot, error) {
	r, e := s.GetRun(ctx, id)
	if e != nil {
		return TaskSnapshot{}, e
	}
	snap := TaskSnapshot{Run: r, Commands: []Command{}}
	iter := s.commands().Where("run_id", "==", id).Documents(ctx)
	defer iter.Stop()
	for {
		d, e := iter.Next()
		if e == iterator.Done {
			break
		}
		if e != nil {
			return snap, e
		}
		var c Command
		if e = d.DataTo(&c); e != nil {
			return snap, e
		}
		if c.Kind != "hold" && c.Position <= taskPosition(r) {
			snap.Commands = append(snap.Commands, c)
		}
	}
	sort.Slice(snap.Commands, func(i, j int) bool { return snap.Commands[i].Position < snap.Commands[j].Position })
	return snap, nil
}
func (s *Google) Incorporate(ctx context.Context, id string, revision, position int64) error {
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		d, e := tx.Get(s.runs().Doc(id))
		r, e := decodeRun(d, e)
		if e != nil {
			return e
		}
		if r.Revision != revision {
			return ErrSuperseded
		}
		if position != taskPosition(r) || position < r.Incorporated {
			return ErrSuperseded
		}
		// A wake without new commands must not rewrite the task while a user
		// answers its question. Keep the revision/position fence, but release
		// the read transaction without contending for an unchanged record.
		if position == r.Incorporated {
			return nil
		}
		updates := []firestore.Update{{Path: "incorporated", Value: position}}
		if position > r.Incorporated {
			if r.Question == nil || r.Question.ResolvedBy != "" || r.Question.Revision != revision {
				updates = append(updates, firestore.Update{Path: "waiting", Value: ""}, firestore.Update{Path: "question", Value: firestore.Delete})
			}
		}
		return tx.Update(s.runs().Doc(id), updates)
	})
}

// guardTask checks task ownership without execution admission. Publishing an
// already-paid answer may proceed during pause; stale revisions may not.
func taskPosition(r Run) int64 {
	if r.ProtocolVersion < TaskProtocolVersion {
		return r.Revision
	}
	return r.Position
}
func guardTask(r Run, revision int64) error {
	if r.CancelRequested {
		return ErrCancelled
	}
	if r.Revision != revision || r.Incorporated != taskPosition(r) {
		return ErrSuperseded
	}
	if r.Status != "running" {
		return ErrBusy
	}
	return nil
}

// guardTx additionally fences new paid work while execution is paused.
func (s *Google) guardTx(tx *firestore.Transaction, id string, revision int64) (Run, error) {
	d, e := tx.Get(s.runs().Doc(id))
	r, e := decodeRun(d, e)
	if e != nil {
		return r, e
	}
	if e = guardTask(r, revision); e != nil {
		return r, e
	}
	d, e = tx.Get(s.db.Collection("system").Doc("control"))
	if e != nil {
		return r, e
	}
	var c Control
	if e = d.DataTo(&c); e != nil {
		return r, e
	}
	return r, c.Check()
}
func (s *Google) Guard(ctx context.Context, id string, revision int64) (Run, error) {
	var r Run
	e := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var e error
		r, e = s.guardTx(tx, id, revision)
		return e
	})
	return r, e
}
func (s *Google) Admit(ctx context.Context, id string, revision int64, operation string) error {
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, e := s.guardTx(tx, id, revision); e != nil {
			return e
		}
		// A second activity attempt cannot silently execute an admitted operation.
		if e := tx.Create(s.runs().Doc(id).Collection("operations").Doc(operation), map[string]any{"id": operation, "revision": revision, "admitted_at": time.Now().UTC()}); e != nil {
			return e
		}
		return tx.Update(s.runs().Doc(id), []firestore.Update{{Path: "operation_refs", Value: firestore.ArrayUnion(operation)}, {Path: "cleanup_pending", Value: true}})
	})
}
func (s *Google) Project(ctx context.Context, id string, revision int64, kind, text string) error {
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, e := s.guardTx(tx, id, revision); e != nil {
			return e
		}
		waiting := ""
		if kind == "waiting" {
			waiting = text
		}
		return tx.Update(s.runs().Doc(id), []firestore.Update{{Path: "waiting", Value: waiting}, {Path: "progress", Value: ""}, {Path: "events", Value: firestore.ArrayUnion(Event{Kind: kind, Text: text, CreatedAt: time.Now().UTC()})}})
	})
}

// SetProgress replaces the task's latest progress line. Unlike Project it adds no
// event, so polling a long job can't grow the task's saved record.
func (s *Google) SetProgress(ctx context.Context, id string, revision int64, text string) error {
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, e := s.guardTx(tx, id, revision); e != nil {
			return e
		}
		return tx.Update(s.runs().Doc(id), []firestore.Update{{Path: "progress", Value: text}})
	})
}

// ProjectQuestion commits the durable wait and its outer inbox event together.
func (s *Google) ProjectQuestion(ctx context.Context, id string, revision int64, question Question) error {
	traceContext := observability.InjectTrace(ctx)
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		run, err := s.guardTx(tx, id, revision)
		if err != nil {
			return err
		}
		if question.ID == "" || question.Text == "" || question.Revision != revision || (question.Kind != "input" && question.Kind != "budget") {
			return fmt.Errorf("invalid task question")
		}
		if run.Question != nil && run.Question.ID == question.ID {
			return nil
		}
		event := TaskEvent{ID: question.ID, RunID: id, Kind: "needs_input", Sequence: run.EventSequence + 1, Revision: revision, Text: question.Text, QuestionID: question.ID, Reason: question.Kind, OperationRefs: run.OperationRefs, CreatedAt: time.Now().UTC()}
		eventTraceContext := traceContext
		if len(eventTraceContext) == 0 {
			eventTraceContext = run.TraceContext
		}
		inserted, err := s.appendTaskEvent(tx, event, eventTraceContext)
		if err != nil {
			return err
		}
		if !inserted {
			return nil
		}
		run.Question, run.Waiting, run.EventSequence = &question, question.Text, event.Sequence
		return tx.Set(s.runs().Doc(id), run)
	})
}

func (s *Google) Publish(ctx context.Context, id string, revision int64, runStatus RunStatus, detail, text string, costUnknown bool, bypassRouting bool) (Run, error) {
	var run Run
	routingPending := false
	traceContext := observability.InjectTrace(ctx)
	err := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		routingPending = false
		var err error
		run, err = decodeRun(tx.Get(s.runs().Doc(id)))
		if err != nil {
			return err
		}
		if run.Status != RunRunning {
			return nil
		}
		if runStatus == RunCancelled && run.CancelRequested {
			revision = run.Revision
		}
		if runStatus == RunCompleted {
			if err = guardTask(run, revision); err != nil {
				return err
			}
		} else if run.Revision != revision {
			return ErrSuperseded
		}
		bypass := runStatus == RunCancelled || costUnknown || bypassRouting
		if run.ProtocolVersion >= TaskProtocolVersion && !bypass {
			pending, err := s.conversationRoutingPending(tx)
			if err != nil {
				return err
			}
			if pending {
				routingPending = true
				run.PublicationPending = true
				run.PublicationGeneration++
				return tx.Update(s.runs().Doc(id), []firestore.Update{{Path: "publication_pending", Value: true}, {Path: "publication_generation", Value: run.PublicationGeneration}})
			}
		}
		if run.CancelRequested && runStatus != RunFailed {
			runStatus, detail, text = "cancelled", "Stopped by Austin.", ""
		}
		now := time.Now().UTC()
		if run.ProtocolVersion >= TaskProtocolVersion {
			kind := "failure"
			switch runStatus {
			case "completed":
				kind = "result"
			case "cancelled":
				kind = "stopped"
			}
			run.Holds = readyTaskHolds(run.Holds)
			event := TaskEvent{Holds: pendingTaskHolds(run.Holds), ID: id + "-terminal", RunID: id, Kind: kind, Sequence: run.EventSequence + 1, Revision: revision, Text: text, Reason: detail, OperationRefs: run.OperationRefs, CreatedAt: now}
			eventTraceContext := traceContext
			if len(eventTraceContext) == 0 {
				eventTraceContext = run.TraceContext
			}
			inserted, err := s.appendTaskEvent(tx, event, eventTraceContext)
			if err != nil {
				return err
			}
			if inserted {
				run.EventSequence = event.Sequence
			}
		}
		run.PublicationPending = false
		run.CostUnknown = run.CostUnknown || costUnknown
		run.Status, run.Error, run.Result, run.Waiting, run.Question, run.FinishedAt = runStatus, detail, text, "", nil, &now
		if err = tx.Set(s.runs().Doc(id), run); err != nil {
			return err
		}
		if run.ProtocolVersion < TaskProtocolVersion && text != "" {
			return tx.Create(s.messages().Doc(id+"-assistant"), Message{ID: id + "-assistant", RunID: id, Role: "assistant", Text: text, CreatedAt: now})
		}
		return nil
	})
	if err == nil && routingPending {
		err = ErrRoutingPending
	}
	return run, err
}

// OperationRevisions identifies only scopes that were durably admitted, including
// attempts whose worker died before their RPC outcome reached Temporal.
func (s *Google) OperationRevisions(ctx context.Context, id string) ([]int64, error) {
	iter := s.runs().Doc(id).Collection("operations").Documents(ctx)
	defer iter.Stop()
	seen := map[int64]bool{}
	revisions := []int64{}
	for {
		d, e := iter.Next()
		if e == iterator.Done {
			break
		}
		if e != nil {
			return nil, e
		}
		v, _ := d.Data()["revision"].(int64)
		if !seen[v] {
			seen[v] = true
			revisions = append(revisions, v)
		}
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i] < revisions[j] })
	return revisions, nil
}

func (s *Google) CleanupTasks(ctx context.Context) ([]Run, error) {
	iter := s.runs().Where("cleanup_pending", "==", true).Documents(ctx)
	defer iter.Stop()
	runs := []Run{}
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		run, err := decodeRun(doc, nil)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, nil
}
func (s *Google) FinishCleanup(ctx context.Context, id string) error {
	_, err := s.runs().Doc(id).Update(ctx, []firestore.Update{{Path: "cleanup_pending", Value: false}})
	return err
}

func (s *Google) PublicationTasks(ctx context.Context) ([]Run, error) {
	iter := s.runs().Where("publication_pending", "==", true).Documents(ctx)
	defer iter.Stop()
	runs := []Run{}
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		run, err := decodeRun(doc, nil)
		if err != nil {
			return nil, err
		}
		if run.Status == RunRunning {
			runs = append(runs, run)
		}
	}
	return runs, nil
}
func (s *Google) PublicationWoken(ctx context.Context, id string, generation int64) error {
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		run, err := decodeRun(tx.Get(s.runs().Doc(id)))
		if err != nil {
			return err
		}
		if run.PublicationGeneration != generation || !run.PublicationPending {
			return nil
		}
		return tx.Update(s.runs().Doc(id), []firestore.Update{{Path: "publication_pending", Value: false}})
	})
}
