package firestore

import (
	"context"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/firestore"
	"nori/internal/state"
)

const logAttempts = 20

func (d *Database) events() *firestore.CollectionRef { return d.root().Collection("events") }
func (d *Database) eventHead() *firestore.DocumentRef {
	return d.root().Collection("event_head").Doc("head")
}

// eventLog adds entries to the log inside one transaction. Open it once the transaction knows it will write, so a replay
// or an idle check doesn't conflict with anything, and before its first write, because Firestore requires reads first.
type eventLog struct {
	d     *Database
	tx    *firestore.Transaction
	seq   int64
	start int64
	added map[[2]string]bool
}

// logged runs a transaction that adds to the log. Every one of them writes the same head, so they conflict with each
// other, and more attempts let a burst of task and conversation changes finish.
func (d *Database) logged(ctx context.Context, f func(context.Context, *firestore.Transaction) error) error {
	return d.db.RunTransaction(ctx, f, firestore.MaxAttempts(logAttempts))
}

func (d *Database) openLog(tx *firestore.Transaction) (*eventLog, error) {
	seq, err := readHead(tx.Get(d.eventHead()))
	if err != nil {
		return nil, err
	}
	return &eventLog{d: d, tx: tx, seq: seq, start: seq, added: map[[2]string]bool{}}, nil
}

func readHead(doc *firestore.DocumentSnapshot, err error) (int64, error) {
	if notFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	seq, _ := doc.Data()["seq"].(int64)
	return seq, nil
}

// add records one change. Repeats of the same record in a transaction are dropped.
func (l *eventLog) add(kind, ref string) error {
	key := [2]string{kind, ref}
	if l.added[key] {
		return nil
	}
	l.added[key] = true
	l.seq++
	event := state.LogEvent{Seq: l.seq, Kind: kind, Ref: ref, CreatedAt: time.Now().UTC()}
	if err := l.tx.Create(l.d.events().Doc(fmt.Sprintf("%020d", l.seq)), event); err != nil {
		return err
	}
	return l.tx.Set(l.d.eventHead(), map[string]any{"seq": l.seq})
}

// EventPosition is the sequence of the newest event, or 0 when there are none yet.
func (d *Database) EventPosition(ctx context.Context) (int64, error) {
	return readHead(d.eventHead().Get(ctx))
}

// WatchEvents calls deliver with each batch of events after the position, in order, until ctx ends. Sequence numbers
// follow commit order and each batch is a consistent read, so a later event can't arrive ahead of an earlier one.
func (d *Database) WatchEvents(ctx context.Context, after int64, deliver func([]state.LogEvent) error) error {
	snapshots := d.events().Where("seq", ">", after).OrderBy("seq", firestore.Asc).Snapshots(ctx)
	defer snapshots.Stop()
	for {
		snapshot, err := snapshots.Next()
		if err != nil {
			return err
		}
		batch := []state.LogEvent{}
		for _, change := range snapshot.Changes {
			if change.Kind != firestore.DocumentAdded {
				continue
			}
			var event state.LogEvent
			if err := change.Doc.DataTo(&event); err != nil {
				return err
			}
			batch = append(batch, event)
		}
		slices.SortFunc(batch, func(a, b state.LogEvent) int { return int(a.Seq - b.Seq) })
		if len(batch) == 0 {
			continue
		}
		if err := deliver(batch); err != nil {
			return err
		}
	}
}

// ConversationMessage reads one message for the stream.
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
