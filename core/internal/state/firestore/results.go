package firestore

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"nori/internal/state"
)

func (d *Database) results() *firestore.CollectionRef { return d.root().Collection("results") }

func (d *Database) runResults(tx *firestore.Transaction, runID string) ([]state.Result, error) {
	return documents[state.Result](tx.Documents(d.results().Where("run_id", "==", runID)))
}

func (d *Database) readResult(tx *firestore.Transaction, id string) (*state.Result, error) {
	doc, err := tx.Get(d.results().Doc(id))
	if notFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result state.Result
	if err = doc.DataTo(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SaveResult creates a task's result once. A retry finds the saved record.
func (d *Database) SaveResult(ctx context.Context, result state.Result) (state.Result, error) {
	saved := result
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		if _, err := decodeRun(tx.Get(d.runs().Doc(result.RunID))); err != nil {
			return err
		}
		existing, err := d.readResult(tx, result.ID)
		if err != nil {
			return err
		}
		if existing != nil {
			saved = *existing
			return nil
		}
		saved = result
		if err = log.add(state.EventResult, result.ID); err != nil {
			return err
		}
		return tx.Create(d.results().Doc(result.ID), result)
	})
	return saved, err
}

func (d *Database) GetResult(ctx context.Context, id string) (state.Result, error) {
	doc, err := d.results().Doc(id).Get(ctx)
	if notFound(err) {
		return state.Result{}, state.ErrNotFound
	}
	if err != nil {
		return state.Result{}, err
	}
	var result state.Result
	return result, doc.DataTo(&result)
}

// RecentResults lists the newest results first.
func (d *Database) RecentResults(ctx context.Context, limit int) ([]state.Result, error) {
	return documents[state.Result](d.results().OrderBy("created_at", firestore.Desc).Limit(limit).Documents(ctx))
}

// RecentRuns lists the newest tasks first. Tasks from before results existed are only found this way.
func (d *Database) RecentRuns(ctx context.Context, limit int) ([]state.Run, error) {
	return documents[state.Run](d.runs().OrderBy("started_at", firestore.Desc).Limit(limit).Documents(ctx))
}

// MarkDelivered records that a browser showed these results. The first report stands.
func (d *Database) MarkDelivered(ctx context.Context, ids []string) error {
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		log, err := d.openLog(tx)
		if err != nil {
			return err
		}
		results := []*state.Result{}
		for _, id := range ids {
			result, err := d.readResult(tx, id)
			if err != nil {
				return err
			}
			if result != nil && result.DeliveredAt == nil {
				results = append(results, result)
			}
		}
		now := time.Now().UTC()
		for _, result := range results {
			if err = tx.Update(d.results().Doc(result.ID), []firestore.Update{{Path: "delivered_at", Value: now}}); err != nil {
				return err
			}
			if err = log.add(state.EventResult, result.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

// replyResults reads the results a turn's reply reports: those of the tasks whose end it announces, and those the
// outer model chose to show.
func (d *Database) replyResults(tx *firestore.Transaction, t state.ConversationTurn) ([]state.Result, error) {
	seen := map[string]bool{}
	found := []state.Result{}
	for _, input := range t.Inputs {
		if input.Event == nil || (input.Event.Kind != "result" && input.Event.Kind != "failure") {
			continue
		}
		results, err := d.runResults(tx, input.Event.RunID)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			if !seen[result.ID] {
				seen[result.ID] = true
				found = append(found, result)
			}
		}
	}
	for _, id := range t.Attempt.ShowResults {
		if seen[id] {
			continue
		}
		result, err := d.readResult(tx, id)
		if err != nil {
			return nil, err
		}
		if result != nil {
			seen[id] = true
			found = append(found, *result)
		}
	}
	return found, nil
}
