package firestore

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"nori/internal/state"
)

func (d *Database) memories() *firestore.CollectionRef { return d.root().Collection("memories") }
func (d *Database) summary() *firestore.DocumentRef {
	return d.root().Collection("conversation").Doc("summary")
}

// Memories returns every saved memory, oldest first. Their total size is bounded when they are written.
func (d *Database) Memories(ctx context.Context) ([]state.Memory, error) {
	it := d.memories().OrderBy("created_at", firestore.Asc).Documents(ctx)
	defer it.Stop()
	return documents[state.Memory](it)
}

// readMemoryChanges reads the memories a turn's changes touch. It runs before the publish transaction writes anything.
func (d *Database) readMemoryChanges(tx *firestore.Transaction, changes []state.MemoryChange) (map[string]*state.Memory, error) {
	saved := map[string]*state.Memory{}
	for _, c := range changes {
		doc, err := tx.Get(d.memories().Doc(c.ID))
		if notFound(err) {
			saved[c.ID] = nil
			continue
		}
		if err != nil {
			return nil, err
		}
		var m state.Memory
		if err = doc.DataTo(&m); err != nil {
			return nil, err
		}
		saved[c.ID] = &m
	}
	return saved, nil
}

// writeMemoryChanges applies a turn's changes inside its publish transaction. A refused change means none apply.
func (d *Database) writeMemoryChanges(
	tx *firestore.Transaction, t state.ConversationTurn, saved map[string]*state.Memory, now time.Time,
) error {
	for _, c := range t.Attempt.Memory {
		if c.Error != "" {
			return nil
		}
	}
	source := state.SourceInput(t.Inputs)
	for _, c := range t.Attempt.Memory {
		var err error
		if c.Kind == state.MemoryForget {
			err = tx.Delete(d.memories().Doc(c.ID))
		} else {
			err = tx.Set(d.memories().Doc(c.ID), state.ApplyMemoryChange(c, saved[c.ID], source, now))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// ConversationSummary returns the saved summary, or a zero one.
func (d *Database) ConversationSummary(ctx context.Context) (state.ConversationSummary, error) {
	var s state.ConversationSummary
	doc, err := d.summary().Get(ctx)
	if notFound(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = doc.DataTo(&s)
	return s, err
}

// SaveConversationSummary replaces the summary unless a newer one is already saved: one for a later context, or one
// that covers more of this context.
func (d *Database) SaveConversationSummary(ctx context.Context, s state.ConversationSummary) error {
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		var saved state.ConversationSummary
		doc, err := tx.Get(d.summary())
		if err != nil && !notFound(err) {
			return err
		}
		if err == nil {
			if err = doc.DataTo(&saved); err != nil {
				return err
			}
			if saved.From.After(s.From) || (saved.From.Equal(s.From) && !s.Through.After(saved.Through)) {
				return nil
			}
		}
		return tx.Set(d.summary(), s)
	})
}

// ConversationTurnsAfter returns up to HistoryTurnsExamined turns that started after a time, newest first. A zero time
// returns the newest turns.
func (d *Database) ConversationTurnsAfter(ctx context.Context, after time.Time) ([]state.ConversationTurn, error) {
	query := d.conversationTurns().OrderBy("started_at", firestore.Desc).Limit(state.HistoryTurnsExamined)
	if !after.IsZero() {
		query = query.Where("started_at", ">", after)
	}
	it := query.Documents(ctx)
	defer it.Stop()
	return documents[state.ConversationTurn](it)
}
