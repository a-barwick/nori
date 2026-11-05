package firestore

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/firestore"
	"nori/internal/state"
)

func (d *Database) chats() *firestore.CollectionRef { return d.root().Collection("chats") }
func (d *Database) CreateChat(ctx context.Context, c state.Chat) error {
	c.UpdatedAt = time.Now().UTC()
	_, err := d.chats().Doc(c.ID).Create(ctx, c)
	return err
}

// ChatSummaries reads only the list fields, newest first.
func (d *Database) ChatSummaries(ctx context.Context) ([]state.ChatSummary, error) {
	chats, err := documents[state.ChatSummary](d.chats().Select("id", "title", "mode", "active_turn", "updated_at").Documents(ctx))
	slices.SortStableFunc(chats, func(a, b state.ChatSummary) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return chats, err
}

func (d *Database) ActiveChats(ctx context.Context) ([]state.Chat, error) {
	chats, err := documents[state.Chat](d.chats().Where("attention", "==", true).Documents(ctx))
	for i := range chats {
		if chats[i].Messages == nil {
			chats[i].Messages = []state.ChatMessage{}
		}
	}
	return chats, err
}

func (d *Database) Chat(ctx context.Context, id string) (state.Chat, error) {
	doc, err := d.chats().Doc(id).Get(ctx)
	if notFound(err) {
		return state.Chat{}, state.ErrNotFound
	}
	if err != nil {
		return state.Chat{}, err
	}
	var c state.Chat
	err = doc.DataTo(&c)
	if c.Messages == nil {
		c.Messages = []state.ChatMessage{}
	}
	return c, err
}

// changedMessages returns the messages an update added or changed, compared by their public form.
func changedMessages(before, after []state.ChatMessage) []state.ChatMessage {
	prior := map[string]string{}
	for _, m := range before {
		encoded, _ := json.Marshal(m)
		prior[m.ID] = string(encoded)
	}
	changed := []state.ChatMessage{}
	for _, m := range after {
		encoded, _ := json.Marshal(m)
		if prior[m.ID] != string(encoded) {
			changed = append(changed, m)
		}
	}
	return changed
}

func sameHeader(a, b state.ChatHeader) bool {
	turn := func(h state.ChatHeader) string {
		if h.ActiveTurn == nil {
			return ""
		}
		return *h.ActiveTurn
	}
	sameTurn := turn(a) == turn(b)
	a.ActiveTurn, b.ActiveTurn = nil, nil
	return sameTurn && a == b
}

func (d *Database) UpdateChat(ctx context.Context, id string, change func(*state.Chat) (string, error)) (state.Chat, error) {
	var c state.Chat
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		ref := d.chats().Doc(id)
		doc, err := tx.Get(ref)
		if notFound(err) {
			return state.ErrNotFound
		}
		if err != nil {
			return err
		}
		c = state.Chat{}
		if err = doc.DataTo(&c); err != nil {
			return err
		}
		if c.Messages == nil {
			c.Messages = []state.ChatMessage{}
		}
		before := slices.Clone(c.Messages)
		header := c.Header()
		prior := map[string]string{}
		for _, m := range c.Messages {
			for _, op := range m.Operations {
				prior[op.ID] = op.Outcome
			}
		}
		kind, err := change(&c)
		if err != nil {
			return err
		}
		if kind == "" {
			return nil
		}
		// Tool admission shares the global pause fence with background tasks.
		startsTool := false
		for _, m := range c.Messages {
			for _, op := range m.Operations {
				if op.Outcome == "attempted" && prior[op.ID] != "attempted" {
					startsTool = true
				}
			}
		}
		if startsTool {
			doc, e := tx.Get(d.control())
			if e != nil {
				return e
			}
			var control state.Control
			if e = doc.DataTo(&control); e != nil {
				return e
			}
			if e = control.Check(); e != nil {
				return e
			}
		}
		encoded, _ := json.Marshal(c)
		if len(encoded) > 512<<10 {
			return state.ErrChatFull
		}
		messages := changedMessages(before, c.Messages)
		c.Attention = c.NeedsAttention()
		// A lease renewal changes nothing a client sees, so it saves without an event.
		if len(messages) == 0 && sameHeader(c.Header(), header) {
			return tx.Set(ref, c)
		}
		c.Position++
		c.UpdatedAt = time.Now().UTC()
		if err = tx.Set(ref, c); err != nil {
			return err
		}
		return tx.Create(ref.Collection("events").Doc(fmt.Sprintf("%020d", c.Position)), state.ChatEvent{Seq: c.Position, Kind: kind, Conversation: c.Header(), Messages: messages})
	})
	return c, err
}

func (d *Database) WatchChatEvents(ctx context.Context, id string, after int64, deliver func([]state.ChatEvent) error) error {
	snapshots := d.chats().Doc(id).Collection("events").Where("seq", ">", after).OrderBy("seq", firestore.Asc).Snapshots(ctx)
	defer snapshots.Stop()
	for {
		snapshot, err := snapshots.Next()
		if err != nil {
			return err
		}
		batch := []state.ChatEvent{}
		for _, change := range snapshot.Changes {
			if change.Kind != firestore.DocumentAdded {
				continue
			}
			var event state.ChatEvent
			if err := change.Doc.DataTo(&event); err != nil {
				return err
			}
			batch = append(batch, event)
		}
		if len(batch) == 0 {
			continue
		}
		slices.SortFunc(batch, func(a, b state.ChatEvent) int { return int(a.Seq - b.Seq) })
		if err := deliver(batch); err != nil {
			return err
		}
	}
}

// DeleteChat first removes the chat document in a transaction that refuses a busy
// chat, so no turn can start on it, then removes its event and upload records.
func (d *Database) DeleteChat(ctx context.Context, id string) ([]string, error) {
	ref := d.chats().Doc(id)
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		doc, err := tx.Get(ref)
		if notFound(err) {
			return state.ErrNotFound
		}
		if err != nil {
			return err
		}
		var c state.Chat
		if err = doc.DataTo(&c); err != nil {
			return err
		}
		if c.NeedsAttention() {
			return state.ErrChatBusy
		}
		return tx.Delete(ref)
	})
	if err != nil {
		return nil, err
	}
	uploads := []string{}
	writer := d.db.BulkWriter(ctx)
	for _, collection := range []string{"events", "uploads"} {
		docs, err := ref.Collection(collection).DocumentRefs(ctx).GetAll()
		if err != nil {
			writer.End()
			return uploads, err
		}
		for _, doc := range docs {
			if collection == "uploads" {
				uploads = append(uploads, doc.ID)
			}
			if _, err = writer.Delete(doc); err != nil {
				writer.End()
				return uploads, err
			}
		}
	}
	writer.End()
	return uploads, nil
}

func (d *Database) CreateChatUpload(ctx context.Context, id string, u state.Upload) (state.Upload, error) {
	saved := u
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		if _, err := tx.Get(d.chats().Doc(id)); notFound(err) {
			return state.ErrNotFound
		} else if err != nil {
			return err
		}
		ref := d.chats().Doc(id).Collection("uploads").Doc(u.ID)
		doc, err := tx.Get(ref)
		if err == nil {
			if err = doc.DataTo(&saved); err != nil {
				return err
			}
			if saved.SHA256 != u.SHA256 || saved.Name != u.Name || saved.MediaType != u.MediaType {
				return state.ErrDuplicate
			}
			return nil
		}
		if !notFound(err) {
			return err
		}
		return tx.Create(ref, u)
	})
	return saved, err
}
func (d *Database) ChatUpload(ctx context.Context, id, upload string) (state.Upload, error) {
	doc, err := d.chats().Doc(id).Collection("uploads").Doc(upload).Get(ctx)
	if notFound(err) {
		return state.Upload{}, state.ErrNotFound
	}
	if err != nil {
		return state.Upload{}, err
	}
	var u state.Upload
	err = doc.DataTo(&u)
	return u, err
}
