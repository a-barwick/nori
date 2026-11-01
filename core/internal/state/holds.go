package state

import "time"

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

// HoldTargets names, per task, the holds that a conversation turn's inputs asked it to answer.
func HoldTargets(turn ConversationTurn) map[string]map[string]bool {
	wanted := map[string]map[string]bool{}
	for _, input := range turn.Inputs {
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
	return wanted
}

// AcknowledgeHolds marks the named holds on a task as answered by the turn, or failed when the turn errored or
// published something other than the attempt's own reply. Holds that were answered before stay as they are.
func AcknowledgeHolds(run Run, ids map[string]bool, turn ConversationTurn, text string) Run {
	run.Holds = append([]TaskHold{}, run.Holds...)
	for i := range run.Holds {
		hold := &run.Holds[i]
		if !ids[hold.ID] || hold.AnsweredTurnID != "" {
			continue
		}
		hold.Status = "answered"
		if turn.Attempt.Status == "error" || text != turn.Attempt.Text {
			hold.Status = "failed"
		}
		hold.AnsweredTurnID = turn.ID
	}
	return run
}
