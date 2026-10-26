package state

import (
	"context"
	"fmt"
	"sort"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Google) commands() *firestore.CollectionRef { return s.root().Collection("task_commands") }

func (s *Google) Accept(ctx context.Context, cmd Command) (Run, error) {
	var run Run
	err := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		prior, e := tx.Get(s.commands().Doc(cmd.ID))
		if e == nil {
			var old Command
			if e = prior.DataTo(&old); e != nil {
				return e
			}
			if old.RunID != cmd.RunID || old.Kind != cmd.Kind || old.Text != cmd.Text {
				return ErrDuplicate
			}
			d, e := tx.Get(s.runs().Doc(cmd.RunID))
			run, e = decodeRun(d, e)
			return e
		}
		if status.Code(e) != codes.NotFound {
			return e
		}
		d, e := tx.Get(s.runs().Doc(cmd.RunID))
		exists := e == nil
		if e != nil && status.Code(e) != codes.NotFound {
			return e
		}
		if exists {
			run, e = decodeRun(d, nil)
			if e != nil {
				return e
			}
			if !run.Temporal || run.Status != "running" {
				return ErrBusy
			}
			if cmd.Kind == "start" {
				return ErrDuplicate
			}
		} else if cmd.Kind != "start" {
			return ErrNotFound
		}
		cdoc, e := tx.Get(s.db.Collection("system").Doc("control"))
		if e != nil {
			return e
		}
		var c Control
		if e = cdoc.DataTo(&c); e != nil {
			return e
		}
		// Stop remains admissible while execution is paused.
		if e = c.Check(); e != nil && cmd.Kind != "cancel" {
			return e
		}
		now := time.Now().UTC()
		if !exists {
			root, e := tx.Get(s.root())
			if e != nil && status.Code(e) != codes.NotFound {
				return e
			}
			if e == nil {
				active, _ := root.Data()["active_run"].(string)
				if active != "" {
					d, e := tx.Get(s.runs().Doc(active))
					if e != nil {
						return e
					}
					previous, e := decodeRun(d, nil)
					if e != nil {
						return e
					}
					if previous.Status == "running" {
						return ErrBusy
					}
				}
			}
			// Durable tasks have no session expiry; pause only fences execution.
			run = Run{ID: cmd.RunID, Status: "running", Temporal: true, StartedAt: now, Events: []Event{}, Artifacts: []Artifact{}}
		}
		if cmd.Kind != "start" && cmd.Kind != "input" && cmd.Kind != "continue" && cmd.Kind != "cancel" {
			return fmt.Errorf("unknown command kind")
		}
		if run.Revision >= 32 && cmd.Kind != "cancel" {
			return fmt.Errorf("task input limit reached (32 accepted commands)")
		}
		run.Revision++
		if cmd.Kind == "cancel" {
			run.CancelRequested = true
		}
		cmd.Position = run.Revision
		cmd.Revision = run.Revision
		cmd.CreatedAt = now
		cmd.Delivered = false
		if e = tx.Set(s.root(), map[string]any{"active_run": run.ID}, firestore.MergeAll); e != nil {
			return e
		}
		if e = tx.Set(s.runs().Doc(run.ID), run); e != nil {
			return e
		}
		if e = tx.Create(s.commands().Doc(cmd.ID), cmd); e != nil {
			return e
		}
		if cmd.Kind == "cancel" {
			return nil
		}
		return tx.Create(s.messages().Doc(cmd.ID+"-user"), Message{ID: cmd.ID + "-user", RunID: run.ID, Role: "user", Text: cmd.Text, CreatedAt: now})
	})
	return run, err
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
		if c.Position <= r.Revision {
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
		if position > r.Revision || position < r.Incorporated {
			return ErrSuperseded
		}
		updates := []firestore.Update{{Path: "incorporated", Value: position}}
		if position > r.Incorporated {
			updates = append(updates, firestore.Update{Path: "waiting", Value: ""})
		}
		return tx.Update(s.runs().Doc(id), updates)
	})
}

// guardTask checks task ownership without execution admission. Publishing an
// already-paid answer may proceed during pause; stale revisions may not.
func guardTask(r Run, revision int64) error {
	if r.CancelRequested {
		return ErrCancelled
	}
	if r.Revision != revision || r.Incorporated != revision {
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
		return tx.Create(s.runs().Doc(id).Collection("operations").Doc(operation), map[string]any{"id": operation, "revision": revision, "admitted_at": time.Now().UTC()})
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
		return tx.Update(s.runs().Doc(id), []firestore.Update{{Path: "waiting", Value: waiting}, {Path: "events", Value: firestore.ArrayUnion(Event{Kind: kind, Text: text, CreatedAt: time.Now().UTC()})}})
	})
}
func (s *Google) Publish(ctx context.Context, id string, revision int64, runStatus, detail, text string, costUnknown bool) (Run, error) {
	var run Run
	err := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		d, e := tx.Get(s.runs().Doc(id))
		run, e = decodeRun(d, e)
		if e != nil {
			return e
		}
		if run.Status != "running" {
			return nil
		}
		if runStatus == "completed" {
			if e = guardTask(run, revision); e != nil {
				return e
			}
		}
		if run.CancelRequested && runStatus != "failed" {
			runStatus = "cancelled"
			detail = "Cancelled by Austin."
			text = ""
		}
		now := time.Now().UTC()
		run.CostUnknown = run.CostUnknown || costUnknown
		run.Status = runStatus
		run.Error = detail
		run.Waiting = ""
		run.FinishedAt = &now
		if e = tx.Set(s.runs().Doc(id), run); e != nil {
			return e
		}
		if text == "" {
			return nil
		}
		return tx.Create(s.messages().Doc(id+"-assistant"), Message{ID: id + "-assistant", RunID: id, Role: "assistant", Text: text, CreatedAt: now})
	})
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
