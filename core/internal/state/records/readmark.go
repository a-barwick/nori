package records

import (
	"context"

	"nori/internal/state"
	"nori/internal/state/docstore"
)

// The saved document keeps its original path.
func (d *Database) readMark() *docstore.DocumentRef {
	return d.root().Collection("conversation").Doc("seen")
}

func (d *Database) ReadPosition(ctx context.Context) (state.ReadMark, error) {
	doc, err := d.readMark().Get(ctx)
	if notFound(err) {
		return state.ReadMark{}, nil
	}
	if err != nil {
		return state.ReadMark{}, err
	}
	var mark state.ReadMark
	err = doc.DataTo(&mark)
	return mark, err
}

// MarkSeen records the newest reply shown in a client's viewport. The mark only moves forward, and its event is
// committed with the mark so other clients can resume it after being offline.
func (d *Database) MarkSeen(ctx context.Context, messageID string) error {
	return d.logged(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		doc, err := tx.Get(d.messages().Doc(messageID))
		if notFound(err) {
			return state.ErrNotFound
		}
		if err != nil {
			return err
		}
		var m state.Message
		if err = doc.DataTo(&m); err != nil {
			return err
		}
		if m.Role != "assistant" {
			return state.ErrCommandInvalid
		}
		var mark state.ReadMark
		doc, err = tx.Get(d.readMark())
		if err == nil {
			if err = doc.DataTo(&mark); err != nil {
				return err
			}
		} else if !notFound(err) {
			return err
		}
		if !m.CreatedAt.After(mark.Through) {
			return nil
		}
		log, err := d.openLog(ctx, tx)
		if err != nil {
			return err
		}
		return log.addWithSeq(state.EventReadPosition, "", func(seq int64) error {
			return tx.Set(d.readMark(), state.ReadMark{MessageID: m.ID, Through: m.CreatedAt, Position: seq})
		})
	})
}
