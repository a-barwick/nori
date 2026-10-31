package state

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// appendTaskEvent is called after the task transaction's other reads and before
// its writes. The state transition and outer inbox insertion commit together.
func (s *Google) appendTaskEvent(tx *firestore.Transaction, event TaskEvent) (bool, error) {
	ref := s.conversationInputs().Doc("event-" + event.ID)
	doc, err := tx.Get(ref)
	if err == nil {
		var prior ConversationInput
		if err := doc.DataTo(&prior); err != nil {
			return false, err
		}
		if prior.Event == nil {
			return false, ErrDuplicate
		}
		old := *prior.Event
		old.CreatedAt, event.CreatedAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(old, event) {
			return false, ErrDuplicate
		}
		return false, nil
	}
	if status.Code(err) != codes.NotFound {
		return false, err
	}
	c, err := readConversation(tx, s.conversation())
	if err != nil {
		return false, err
	}
	c.Received++
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	in := ConversationInput{ID: "event-" + event.ID, Position: c.Received, CreatedAt: event.CreatedAt, Event: &event}
	if err := tx.Set(s.conversation(), c); err != nil {
		return false, err
	}
	return true, tx.Create(ref, in)
}

func (s *Google) conversationRoutingPending(tx *firestore.Transaction) (bool, error) {
	c, err := readConversation(tx, s.conversation())
	return c.UserReceived > c.UserRouted, err
}

func (s *Google) UserRoutingPending(ctx context.Context) (bool, error) {
	c, _, err := s.ConversationStatus(ctx)
	return c.UserReceived > c.UserRouted, err
}

func (s *Google) unroutedInputs(tx *firestore.Transaction, after, through int64) ([]ConversationInput, error) {
	it := tx.Documents(s.conversationInputs().Where("position", ">", after).Where("position", "<=", through).OrderBy("position", firestore.Asc))
	defer it.Stop()
	var inputs []ConversationInput
	bytes := 0
	for {
		doc, err := it.Next()
		if err == iterator.Done {
			return inputs, nil
		}
		if err != nil {
			return nil, err
		}
		var in ConversationInput
		if err := doc.DataTo(&in); err != nil {
			return nil, err
		}
		if !in.IsUser() {
			continue
		}
		bytes += len(in.Text)
		if bytes > 256<<10 {
			return nil, fmt.Errorf("unresolved conversation inputs exceed the routing context limit")
		}
		inputs = append(inputs, in)
	}
}

// ConversationCommand recovers an accepted action after a response/checkpoint
// failure. The command is the immutable receipt; Run is the current projection.
func (s *Google) ConversationCommand(ctx context.Context, id string) (*CommandReceipt, error) {
	doc, err := s.commands().Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var command Command
	if err := doc.DataTo(&command); err != nil {
		return nil, err
	}
	run, err := s.GetRun(ctx, command.RunID)
	if err != nil {
		return nil, err
	}
	return &CommandReceipt{Command: command, Run: run}, nil
}
