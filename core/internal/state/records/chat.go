package records

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) chats() *docstore.CollectionRef { return d.root().Collection("chats") }
func (d *Database) CreateChat(ctx context.Context, c state.Chat) error {
	c.UpdatedAt = time.Now().UTC()
	_, err := d.chats().Doc(c.ID).Create(ctx, c)
	return err
}

func (d *Database) ChatSummaries(ctx context.Context) ([]state.ChatSummary, error) {
	chats, err := documents[state.ChatSummary](d.chats().Select("id", "title", "mode", "active_turn", "updated_at").Documents(ctx))
	slices.SortStableFunc(chats, func(a, b state.ChatSummary) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return chats, err
}

// The message index and its content share one bounded document. Attention keeps
// unresolved writes discoverable independently of the recent history window.
type chatMessageRecord struct {
	state.ChatMessage
	Attention bool `firestore:"attention"`
}

func readChatHeader(tx *docstore.Transaction, ref *docstore.DocumentRef) (state.Chat, error) {
	doc, err := tx.Get(ref)
	if notFound(err) {
		return state.Chat{}, state.ErrNotFound
	}
	if err != nil {
		return state.Chat{}, err
	}
	var c state.Chat
	err = doc.DataTo(&c)
	return c, err
}

func (d *Database) readChat(tx *docstore.Transaction, id string, attention bool, ids []string) (state.Chat, bool, error) {
	ref := d.chats().Doc(id)
	c, err := readChatHeader(tx, ref)
	if err != nil {
		return c, false, err
	}
	legacy := len(c.Messages) > 0
	if legacy {
		c.MessageCount = int64(len(c.Messages))
		for i := range c.Messages {
			c.Messages[i].Sequence = int64(i + 1)
		}
		return c, true, nil
	}
	docs, err := tx.Documents(ref.Collection("messages").OrderBy("sequence", docstore.Desc).Limit(state.ChatPageSize)).GetAll()
	if err != nil {
		return c, false, err
	}
	if attention {
		pending, e := tx.Documents(ref.Collection("messages").Where("attention", "==", true)).GetAll()
		if e != nil {
			return c, false, e
		}
		docs = append(docs, pending...)
	}
	for _, id := range ids {
		extra, e := tx.Get(ref.Collection("messages").Doc(id))
		if notFound(e) {
			continue
		}
		if e != nil {
			return c, false, e
		}
		docs = append(docs, extra)
	}
	c.Messages = []state.ChatMessage{}
	seen := map[string]bool{}
	for _, doc := range docs {
		var m state.ChatMessage
		if err := doc.DataTo(&m); err != nil {
			return c, false, err
		}
		if !seen[m.ID] {
			c.Messages = append(c.Messages, m)
			seen[m.ID] = true
		}
	}
	slices.SortFunc(c.Messages, func(a, b state.ChatMessage) int { return int(a.Sequence - b.Sequence) })
	c.NextBefore = 0
	if c.MessageCount > state.ChatPageSize {
		c.NextBefore = c.MessageCount - state.ChatPageSize + 1
	}
	return c, false, nil
}

func (d *Database) ActiveChats(ctx context.Context) ([]state.Chat, error) {
	headers, err := documents[state.ChatSummary](d.chats().Where("attention", "==", true).Select("id").Documents(ctx))
	if err != nil {
		return nil, err
	}
	chats := []state.Chat{}
	for _, header := range headers {
		var c state.Chat
		err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
			var e error
			c, _, e = d.readChat(tx, header.ID, true, nil)
			return e
		}, docstore.ReadOnly)
		if err == state.ErrNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		chats = append(chats, c)
	}
	return chats, nil
}

func (d *Database) Chat(ctx context.Context, id string) (c state.Chat, err error) {
	err = d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		var e error
		c, _, e = d.readChat(tx, id, false, nil)
		return e
	}, docstore.ReadOnly)
	return
}

func (d *Database) ChatMessage(ctx context.Context, id, messageID string) (message state.ChatMessage, err error) {
	ref := d.chats().Doc(id)
	err = d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		c, err := readChatHeader(tx, ref)
		if err != nil {
			return err
		}
		if len(c.Messages) > 0 {
			for i, m := range c.Messages {
				if m.ID == messageID {
					m.Sequence = int64(i + 1)
					message = m
					return nil
				}
			}
			return state.ErrNotFound
		}
		doc, err := tx.Get(ref.Collection("messages").Doc(messageID))
		if notFound(err) {
			return state.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := doc.DataTo(&message); err != nil {
			return err
		}
		if message.ID != messageID {
			return state.ErrNotFound
		}
		return nil
	}, docstore.ReadOnly)
	return message, err
}

func (d *Database) ChatMessages(ctx context.Context, id string, before int64) (page state.ChatMessagePage, err error) {
	page.Messages = []state.ChatMessage{}
	ref := d.chats().Doc(id)
	err = d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		c, e := readChatHeader(tx, ref)
		if e != nil {
			return e
		}
		messages := []state.ChatMessage{}
		if len(c.Messages) > 0 {
			for i, m := range c.Messages {
				m.Sequence = int64(i + 1)
				if before == 0 || m.Sequence < before {
					messages = append(messages, m)
				}
			}
			slices.Reverse(messages)
		} else {
			query := ref.Collection("messages").OrderBy("sequence", docstore.Desc).Limit(state.ChatPageSize + 1)
			if before > 0 {
				query = query.Where("sequence", "<", before)
			}
			docs, e := tx.Documents(query).GetAll()
			if e != nil {
				return e
			}
			for _, doc := range docs {
				var m state.ChatMessage
				if e = doc.DataTo(&m); e != nil {
					return e
				}
				messages = append(messages, m)
			}
		}
		page.NextBefore = 0
		if len(messages) > state.ChatPageSize {
			messages = messages[:state.ChatPageSize]
			page.NextBefore = messages[len(messages)-1].Sequence
		}
		slices.Reverse(messages)
		page.Messages = messages
		return nil
	}, docstore.ReadOnly)
	return
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

func (d *Database) UpdateChat(ctx context.Context, id string, change func(*state.Chat) (string, error), ids ...string) (state.Chat, error) {
	var c state.Chat
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		var err error
		var legacy bool
		c, legacy, err = d.readChat(tx, id, true, ids)
		if err != nil {
			return err
		}
		before := map[string]state.ChatMessage{}
		for _, m := range c.Messages {
			m.Operations = slices.Clone(m.Operations)
			before[m.ID] = m
		}
		header := c.Header()
		kind, err := change(&c)
		if err != nil {
			return err
		}
		if kind == "" {
			return nil
		}
		startsTool := false
		changed := []state.ChatMessage{}
		visible := []state.ChatMessage{}
		for i := range c.Messages {
			m := &c.Messages[i]
			prior, exists := before[m.ID]
			if !exists {
				c.MessageCount++
				m.Sequence = c.MessageCount
			}
			for _, op := range m.Operations {
				wasAttempted := false
				for _, old := range prior.Operations {
					if old.ID == op.ID && old.Outcome == "attempted" && old.Attempt >= op.Attempt {
						wasAttempted = true
					}
				}
				if op.Outcome == "attempted" && !wasAttempted {
					startsTool = true
				}
			}
			if legacy || !reflect.DeepEqual(prior, *m) {
				changed = append(changed, *m)
			}
			oldJSON, _ := json.Marshal(prior)
			newJSON, e := json.Marshal(m)
			if e != nil {
				return e
			}
			if len(newJSON) > state.ChatMessageBytes {
				return state.ErrChatMessageTooLarge
			}
			if string(oldJSON) != string(newJSON) {
				visible = append(visible, *m)
			}
		}
		if startsTool {
			doc, e := tx.Get(controlDoc(d.db))
			if notFound(e) {
				return state.ErrPaused
			}
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
		ref := d.chats().Doc(id)
		for _, m := range changed {
			if err := tx.Set(ref.Collection("messages").Doc(m.ID), chatMessageRecord{ChatMessage: m, Attention: m.NeedsAttention()}); err != nil {
				return err
			}
		}
		c.Attention = c.NeedsAttention()
		// One changed message per event keeps event documents bounded too. The
		// transaction saves the entire update atomically; sequence numbers stay contiguous.
		if len(visible) > 0 || !sameHeader(c.Header(), header) {
			if len(visible) == 0 {
				visible = append(visible, state.ChatMessage{})
			}
			c.UpdatedAt = time.Now().UTC()
			for _, m := range visible {
				c.Position++
				messages := []state.ChatMessage{}
				if m.ID != "" {
					messages = append(messages, m)
				}
				if err := tx.Create(ref.Collection("events").Doc(fmt.Sprintf("%020d", c.Position)), state.ChatEvent{Seq: c.Position, Kind: kind, Conversation: c.Header(), Messages: messages}); err != nil {
					return err
				}
			}
		}
		c.NextBefore = 0
		if c.MessageCount > state.ChatPageSize {
			c.NextBefore = c.MessageCount - state.ChatPageSize + 1
		}
		saved := c
		saved.Messages = nil
		return tx.Set(ref, saved)
	})
	return c, err
}

func (d *Database) WatchChatEvents(ctx context.Context, id string, after int64, deliver func([]state.ChatEvent) error) error {
	return d.chats().Doc(id).Collection("events").Where("seq", ">", after).OrderBy("seq", docstore.Asc).Watch(ctx, func(docs []*docstore.DocumentSnapshot) error {
		batch := make([]state.ChatEvent, 0, len(docs))
		for _, doc := range docs {
			var event state.ChatEvent
			if err := doc.DataTo(&event); err != nil {
				return err
			}
			batch = append(batch, event)
		}
		slices.SortFunc(batch, func(a, b state.ChatEvent) int { return int(a.Seq - b.Seq) })
		return deliver(batch)
	})
}

// DeleteChat first removes the chat document in a transaction that refuses a busy
// chat, so no turn can start on it, then removes its event and upload records.
func (d *Database) DeleteChat(ctx context.Context, id string) ([]string, error) {
	ref := d.chats().Doc(id)
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
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
		if c.Attention || c.NeedsAttention() {
			return state.ErrChatBusy
		}
		return tx.Delete(ref)
	})
	if err != nil {
		return nil, err
	}
	uploads := []string{}
	for _, collection := range []string{"events", "uploads", "messages"} {
		for {
			docs, err := ref.Collection(collection).Select().Limit(200).Documents(ctx).GetAll()
			if err != nil {
				return uploads, err
			}
			if len(docs) == 0 {
				break
			}
			paths := make([]string, len(docs))
			for i, doc := range docs {
				paths[i] = doc.Ref.Path
				if collection == "uploads" {
					uploads = append(uploads, doc.Ref.ID)
				}
			}
			if err = d.db.DeleteBatch(ctx, paths); err != nil {
				return uploads, err
			}
		}
	}

	return uploads, nil
}

func (d *Database) CreateChatUpload(ctx context.Context, id string, u state.Upload) (state.Upload, error) {
	saved := u
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
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

// ChatMessagesAfter reads a contiguous oldest-first page for context maintenance.
func (d *Database) ChatMessagesAfter(ctx context.Context, id string, after int64) (messages []state.ChatMessage, err error) {
	ref := d.chats().Doc(id)
	err = d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		c, e := readChatHeader(tx, ref)
		if e != nil {
			return e
		}
		messages = []state.ChatMessage{}
		if len(c.Messages) > 0 {
			for i, m := range c.Messages {
				m.Sequence = int64(i + 1)
				if m.Sequence > after {
					messages = append(messages, m)
					if len(messages) == state.ChatPageSize {
						break
					}
				}
			}
			return nil
		}
		docs, e := tx.Documents(ref.Collection("messages").Where("sequence", ">", after).OrderBy("sequence", docstore.Asc).Limit(state.ChatPageSize)).GetAll()
		if e != nil {
			return e
		}
		for _, doc := range docs {
			var m state.ChatMessage
			if e = doc.DataTo(&m); e != nil {
				return e
			}
			messages = append(messages, m)
		}
		return nil
	}, docstore.ReadOnly)
	return
}
