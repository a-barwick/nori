package firestore

import (
	"context"
	"encoding/json"
	"fmt"

	"cloud.google.com/go/firestore"
	"nori/internal/state"
)

func (d *Database) chats() *firestore.CollectionRef { return d.root().Collection("chats") }
func (d *Database) CreateChat(ctx context.Context, c state.Chat) error {
	_, err := d.chats().Doc(c.ID).Create(ctx, c)
	return err
}
func (d *Database) Chats(ctx context.Context) ([]state.Chat, error) {
	chats, err := documents[state.Chat](d.chats().Documents(ctx))
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
			return fmt.Errorf("chat record reached its storage limit")
		}
		c.Position++
		if err = tx.Set(ref, c); err != nil {
			return err
		}
		// Internal provider and reservation state never enters a public replay snapshot.
		snapshot := c
		snapshot.Started = ""
		snapshot.LeaseUntil = state.Chat{}.LeaseUntil
		snapshot.Usage = nil
		return tx.Create(ref.Collection("events").Doc(fmt.Sprintf("%020d", c.Position)), state.ChatEvent{Seq: c.Position, Kind: kind, Conversation: snapshot})
	})
	return c, err
}
func (d *Database) ChatEvents(ctx context.Context, id string, after int64) ([]state.ChatEvent, error) {
	if _, err := d.Chat(ctx, id); err != nil {
		return nil, err
	}
	return documents[state.ChatEvent](d.chats().Doc(id).Collection("events").Where("seq", ">", after).OrderBy("seq", firestore.Asc).Limit(100).Documents(ctx))
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
