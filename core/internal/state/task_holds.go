package state

import (
	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"time"
)

// TaskHold is a conversation question waiting for one task's outcome. It does
// not change the task's instructions, budget, or open worker question.
type TaskHold struct {
	ID             string    `json:"id" firestore:"id"`
	TaskID         string    `json:"task_id" firestore:"task_id"`
	Question       string    `json:"question" firestore:"question"`
	SourceInputIDs []string  `json:"source_input_ids,omitempty" firestore:"source_input_ids,omitempty"`
	Status         string    `json:"status" firestore:"status"`
	CreatedAt      time.Time `json:"created_at" firestore:"created_at"`
	AnsweredTurnID string    `json:"answered_turn_id,omitempty" firestore:"answered_turn_id,omitempty"`
}

func readyTaskHolds(holds []TaskHold) []TaskHold {
	result := append([]TaskHold{}, holds...)
	for i := range result {
		if result[i].Status == "pending" {
			result[i].Status = "ready"
		}
	}
	return result
}
func pendingTaskHolds(holds []TaskHold) []TaskHold {
	result := []TaskHold{}
	for _, hold := range holds {
		if hold.Status == "pending" || hold.Status == "ready" {
			result = append(result, hold)
		}
	}
	return result
}

// appendHeldEvents handles registration after a task finished. Reading the run
// in the registration transaction serializes it against terminal publication.
// All event reads precede writes, including batches with several terminal tasks.
func (s *Google) appendHeldEvents(tx *firestore.Transaction, events []TaskEvent, traceContext map[string]string) error {
	if len(events) == 0 {
		return nil
	}
	for _, event := range events {
		_, err := tx.Get(s.conversationInputs().Doc("event-" + event.ID))
		if err == nil {
			return ErrDuplicate
		}
		if status.Code(err) != codes.NotFound {
			return err
		}
	}
	c, err := readConversation(tx, s.conversation())
	if err != nil {
		return err
	}
	for _, event := range events {
		c.Received++
		in := ConversationInput{ID: "event-" + event.ID, Position: c.Received, CreatedAt: event.CreatedAt, Event: &event, TraceContext: traceContext}
		if err = tx.Create(s.conversationInputs().Doc(in.ID), in); err != nil {
			return err
		}
	}
	return tx.Set(s.conversation(), c)
}

// holdAcknowledgements reads before PublishConversation writes anything. The
// held answer and its acknowledgement then commit in the same transaction.
func (s *Google) holdAcknowledgements(tx *firestore.Transaction, t ConversationTurn, text string) (map[string]Run, error) {
	wanted := map[string]map[string]bool{}
	for _, input := range t.Inputs {
		if input.Event == nil {
			continue
		}
		for _, hold := range input.Event.Holds {
			if wanted[hold.TaskID] == nil {
				wanted[hold.TaskID] = map[string]bool{}
			}
			wanted[hold.TaskID][hold.ID] = true
		}
	}
	runs := map[string]Run{}
	for id, ids := range wanted {
		run, err := decodeRun(tx.Get(s.runs().Doc(id)))
		if err != nil {
			return nil, err
		}
		run.Holds = append([]TaskHold{}, run.Holds...)
		for i := range run.Holds {
			hold := &run.Holds[i]
			if !ids[hold.ID] || hold.AnsweredTurnID != "" {
				continue
			}
			hold.Status = "answered"
			if t.Attempt.Status == "error" || text != t.Attempt.Text {
				hold.Status = "failed"
			}
			hold.AnsweredTurnID = t.ID
		}
		runs[id] = run
	}
	return runs, nil
}
