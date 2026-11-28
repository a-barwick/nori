package records

import (
	"context"
	"fmt"
	"slices"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (d *Database) events() *docstore.CollectionRef { return d.root().Collection("events") }
func (d *Database) eventHead() *docstore.DocumentRef {
	return d.root().Collection("event_head").Doc("head")
}

type eventLog struct {
	d     *Database
	tx    *docstore.Transaction
	seq   int64
	start int64
	added map[[2]string]bool
	items []logEntry
}

type logEntry struct {
	kind string
	ref  string
	at   time.Time
	hook func(seq int64) error
}

func (d *Database) logged(ctx context.Context, f func(context.Context, *docstore.Transaction) error) error {
	var active *eventLog
	txCtx := context.WithValue(ctx, logCtxKey{}, &active)
	return d.db.RunTransaction(txCtx, func(ctx context.Context, tx *docstore.Transaction) error {
		active = nil
		if err := f(ctx, tx); err != nil {
			return err
		}
		if active == nil {
			return nil
		}
		return active.flush()
	}, docstore.MaxAttempts(docstore.ContendedWriteAttempts))
}

type logCtxKey struct{}

func (d *Database) openLog(tx *docstore.Transaction) (*eventLog, error) {
	log := &eventLog{d: d, tx: tx, added: map[[2]string]bool{}}
	if !tx.HasSequencer() {
		seq, err := readHead(tx.Get(d.eventHead()))
		if err != nil {
			return nil, err
		}
		log.seq, log.start = seq, seq
	}
	if slot, ok := tx.Context().Value(logCtxKey{}).(**eventLog); ok {
		*slot = log
	}
	return log, nil
}

func readHead(doc *docstore.DocumentSnapshot, err error) (int64, error) {
	if notFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	seq, _ := doc.Data()["seq"].(int64)
	return seq, nil
}

func (l *eventLog) add(kind, ref string) error {
	key := [2]string{kind, ref}
	if l.added[key] {
		return nil
	}
	l.added[key] = true
	l.items = append(l.items, logEntry{kind: kind, ref: ref, at: l.d.now()})
	return nil
}

func (l *eventLog) addWithSeq(kind, ref string, hook func(seq int64) error) error {
	key := [2]string{kind, ref}
	if l.added[key] {
		return nil
	}
	l.added[key] = true
	l.items = append(l.items, logEntry{kind: kind, ref: ref, at: l.d.now(), hook: hook})
	return nil
}

func (l *eventLog) flush() error {
	if len(l.items) == 0 {
		return nil
	}
	start, sequenced, err := l.tx.NextSequence(l.d.eventHead(), len(l.items))
	if err != nil {
		return err
	}
	if sequenced {
		l.seq = start - 1
	}
	for _, item := range l.items {
		l.seq++
		event := state.LogEvent{Seq: l.seq, Kind: item.kind, Ref: item.ref, CreatedAt: item.at}
		if err := l.tx.Create(l.d.events().Doc(fmt.Sprintf("%020d", l.seq)), event); err != nil {
			return err
		}
		if item.hook != nil {
			if err := item.hook(l.seq); err != nil {
				return err
			}
		}
	}
	if sequenced {
		return nil
	}
	return l.tx.Set(l.d.eventHead(), map[string]any{"seq": l.seq})
}

func (d *Database) EventPosition(ctx context.Context) (int64, error) {
	if head, ok, err := d.db.CurrentSequence(ctx, d.eventHead()); ok {
		return head, err
	}
	return readHead(d.eventHead().Get(ctx))
}

func (d *Database) WatchEvents(ctx context.Context, after int64, deliver func([]state.LogEvent) error) error {
	return d.events().Where("seq", ">", after).OrderBy("seq", docstore.Asc).Watch(ctx, func(docs []*docstore.DocumentSnapshot) error {
		batch := make([]state.LogEvent, 0, len(docs))
		for _, doc := range docs {
			var event state.LogEvent
			if err := doc.DataTo(&event); err != nil {
				return err
			}
			batch = append(batch, event)
		}
		slices.SortFunc(batch, func(a, b state.LogEvent) int { return int(a.Seq - b.Seq) })
		return deliver(batch)
	})
}

func (d *Database) ConversationMessage(ctx context.Context, id string) (state.Message, error) {
	doc, err := d.messages().Doc(id).Get(ctx)
	if notFound(err) {
		return state.Message{}, state.ErrNotFound
	}
	if err != nil {
		return state.Message{}, err
	}
	var message state.Message
	return message, doc.DataTo(&message)
}
