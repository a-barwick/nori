package state

import (
	"context"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

// EnsureConversationIntroduction shares admission ordering with real messages.
// Concurrent first loads enqueue one introduction; existing history is untouched.
func (s *Google) EnsureConversationIntroduction(ctx context.Context, prompt string) error {
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		c, err := readConversation(tx, s.conversation())
		if err != nil || c.Received != 0 {
			return err
		}
		messages := tx.Documents(s.messages().Limit(1))
		defer messages.Stop()
		if _, err = messages.Next(); err == nil {
			return nil
		} else if err != iterator.Done {
			return err
		}
		now := time.Now().UTC()
		c.Received = 1
		c.Introducing = true
		c.LatestAt = now
		input := ConversationInput{ID: "nori-introduction", Text: prompt, Position: 1, CreatedAt: now, Introduction: true}
		if err = tx.Set(s.conversation(), c); err != nil {
			return err
		}
		return tx.Create(s.conversationInputs().Doc(input.ID), input)
	})
}
