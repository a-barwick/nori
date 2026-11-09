package records

import (
	"context"
	"strings"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) memories() *docstore.CollectionRef { return d.root().Collection("memories") }
func (d *Database) summary() *docstore.DocumentRef {
	return d.root().Collection("conversation").Doc("summary")
}

// Memories returns every saved memory, oldest first. Their total size is bounded when they are written.
func (d *Database) Memories(ctx context.Context) ([]state.Memory, error) {
	it := d.memories().OrderBy("created_at", docstore.Asc).Documents(ctx)
	defer it.Stop()
	return documents[state.Memory](it)
}

// readMemoryChanges reads the memories a turn's changes touch. It runs before the publish transaction writes anything.
func (d *Database) readMemoryChanges(tx *docstore.Transaction, changes []state.MemoryChange) (map[string]*state.Memory, error) {
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
	tx *docstore.Transaction, t state.ConversationTurn, saved map[string]*state.Memory, now time.Time,
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
func (d *Database) SaveConversationSummary(ctx context.Context, s, expected state.ConversationSummary) error {
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		var saved state.ConversationSummary
		doc, err := tx.Get(d.summary())
		if err != nil && !notFound(err) {
			return err
		}
		if err == nil {
			if err = doc.DataTo(&saved); err != nil {
				return err
			}
		}
		control, err := d.readConversation(tx)
		if err != nil {
			return err
		}
		// A summary is derived from one exact predecessor and context cutoff.
		// A later summarizer or reset cannot be overwritten by stale maintenance.
		if !control.ContextFrom.Equal(s.From) || !saved.From.Equal(expected.From) || !saved.Through.Equal(expected.Through) || !saved.UpdatedAt.Equal(expected.UpdatedAt) {
			return state.ErrConversationStale
		}
		return tx.Set(d.summary(), s)
	})
}

// ConversationTurnsAfter returns up to HistoryTurnsExamined turns that started after a time, newest first. A zero time
// returns the newest turns.
func (d *Database) ConversationTurnsAfter(ctx context.Context, after time.Time) ([]state.ConversationTurn, error) {
	return d.turnsAfter(ctx, after, docstore.Desc)
}

func (d *Database) OldestConversationTurnsAfter(ctx context.Context, after time.Time) ([]state.ConversationTurn, error) {
	return d.turnsAfter(ctx, after, docstore.Asc)
}

func (d *Database) turnsAfter(ctx context.Context, after time.Time, dir docstore.Direction) ([]state.ConversationTurn, error) {
	query := d.conversationTurns().OrderBy("started_at", dir).Limit(state.HistoryTurnsExamined)
	if !after.IsZero() {
		query = query.Where("started_at", ">", after)
	}
	it := query.Documents(ctx)
	defer it.Stop()
	return documents[state.ConversationTurn](it)
}

// ReadConversationMessage accepts visible message IDs and raw summary input IDs.
// The retained-context cutoff is enforced in storage, including for task tools.
func (d *Database) ReadConversationMessage(ctx context.Context, id string) (state.Message, error) {
	if !state.ValidEvidenceID(id) {
		return state.Message{}, state.ErrNotFound
	}
	if !strings.HasSuffix(id, "-user") && !strings.HasSuffix(id, "-assistant") {
		id = state.UserMessageID(id)
	}
	doc, err := d.messages().Doc(id).Get(ctx)
	if notFound(err) {
		return state.Message{}, state.ErrNotFound
	}
	if err != nil {
		return state.Message{}, err
	}
	var message state.Message
	if err = doc.DataTo(&message); err != nil {
		return message, err
	}
	control, _, err := d.ConversationStatus(ctx)
	if err != nil {
		return state.Message{}, err
	}
	if message.ID != id || message.CreatedAt.Before(control.ContextFrom) {
		return state.Message{}, state.ErrNotFound
	}
	return message, nil
}

// EditMemory replaces a memory's text if it has not changed since updatedAt (the client's revision).
func (d *Database) EditMemory(ctx context.Context, id string, expectedUpdated int64, text string) (state.Memory, error) {
	var out state.Memory
	if len(text) == 0 || len(text) > state.MaxMemoryText {
		return out, state.ErrInvalidResource
	}
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(d.memories().Doc(id))
		if notFound(err) {
			return state.ErrNotFound
		}
		if err != nil {
			return err
		}
		var m state.Memory
		if err = doc.DataTo(&m); err != nil {
			return err
		}
		if m.UpdatedAt.UnixMilli() != expectedUpdated {
			return state.ErrRevisionConflict
		}
		m.Text, m.UpdatedAt = text, time.Now().UTC()
		out = m
		return tx.Set(d.memories().Doc(id), m)
	})
	return out, err
}

// ForgetMemory deletes a memory if it has not changed since updatedAt. It removes the memory from future use; it
// does not touch transcripts.
func (d *Database) ForgetMemory(ctx context.Context, id string, expectedUpdated int64) (state.Memory, error) {
	var out state.Memory
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(d.memories().Doc(id))
		if notFound(err) {
			return state.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err = doc.DataTo(&out); err != nil {
			return err
		}
		if out.UpdatedAt.UnixMilli() != expectedUpdated {
			return state.ErrRevisionConflict
		}
		return tx.Delete(d.memories().Doc(id))
	})
	return out, err
}
