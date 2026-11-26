package state

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	TaskBudgetUSD = 1.0
	MaxSubtasks   = 4
	QuestionWork  = "work"
)

type SubtaskLink struct {
	ID         string    `json:"id" firestore:"id"`
	QuestionID string    `json:"question_id" firestore:"question_id"`
	Status     RunStatus `json:"status" firestore:"status"`
}

func RunningSubtasks(run Run) []string {
	ids := []string{}
	for _, link := range run.Subtasks {
		if link.Status == RunRunning {
			ids = append(ids, link.ID)
		}
	}
	return ids
}

func linkSubtask(parent Run, exists bool, cmd Command, opened map[string]bool) (Run, Question, error) {
	if !exists {
		return parent, Question{}, ErrNotFound
	}
	if parent.Status != RunRunning || parent.Finishing != nil {
		return parent, Question{}, ErrBusy
	}
	if parent.CancelRequested {
		return parent, Question{}, ErrCancelled
	}
	if parent.ParentID != "" {
		return parent, Question{}, fmt.Errorf("%w: a subtask can't have its own subtasks", ErrCommandInvalid)
	}
	question := parent.Question
	if question == nil || question.Kind != QuestionWork || question.ID != cmd.QuestionID || question.Revision != parent.Revision ||
		(question.ResolvedBy != "" && !opened[question.ID]) {
		return parent, Question{}, ErrQuestionStale
	}
	count := 0
	for _, link := range parent.Subtasks {
		if link.QuestionID == question.ID {
			count++
		}
	}
	if count >= MaxSubtasks {
		return parent, Question{}, fmt.Errorf("%w: a work request can start at most %d subtasks", ErrCommandInvalid, MaxSubtasks)
	}
	opened[question.ID] = true
	resolved := *question
	if resolved.ResolvedBy == "" {
		resolved.ResolvedBy = cmd.ID
	}
	parent.Question = &resolved
	parent.Subtasks = append(append([]SubtaskLink{}, parent.Subtasks...), SubtaskLink{ID: cmd.RunID, QuestionID: question.ID, Status: RunRunning})
	return parent, resolved, nil
}

func StopSubtask(child Run, cause string, now time.Time) (Run, Command, bool) {
	if child.Status != RunRunning || child.CancelRequested || child.Finishing != nil {
		return child, Command{}, false
	}
	child.Revision++
	child.Position++
	child.CancelRequested = true
	child.Question, child.Waiting, child.PublicationPending = nil, "", false
	return child, Command{
		ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(cause+":"+child.ID)).String(), RunID: child.ID, Kind: CommandStop,
		Position: child.Position, Revision: child.Revision, CreatedAt: now,
	}, true
}

func SettleSubtask(parent, before, finished Run, now time.Time) (settled Run, result *Command, waiting bool) {
	parent = addSubtaskCost(parent, before, finished.Spent, finished.CostUnknown)
	parent.Subtasks = append([]SubtaskLink{}, parent.Subtasks...)
	questionID := ""
	for i := range parent.Subtasks {
		if parent.Subtasks[i].ID == finished.ID {
			parent.Subtasks[i].Status = finished.Status
			questionID = parent.Subtasks[i].QuestionID
		}
	}
	question := parent.Question
	if parent.Status != RunRunning || parent.Finishing != nil || parent.CancelRequested || question == nil || question.ID != questionID || question.Revision != parent.Revision {
		return parent, nil, false
	}
	for _, link := range parent.Subtasks {
		if link.QuestionID == questionID && link.Status == RunRunning {
			return parent, nil, true
		}
	}
	parent.Position++
	return parent, &Command{
		ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(questionID+":subtask-result")).String(), RunID: parent.ID, Kind: CommandSubtaskResult,
		QuestionID: questionID, Position: parent.Position, Revision: parent.Revision, CreatedAt: now,
	}, true
}

func addSubtaskCost(parent, before Run, spent float64, costUnknown bool) Run {
	if spent > before.Spent {
		parent.SubtaskSpent += spent - before.Spent
	}
	parent.CostUnknown = parent.CostUnknown || costUnknown
	return parent
}

func RollUpSpend(parent, child Run, update SpendUpdate) Run {
	spent, _, unknown := MergeSpend(child, update)
	return addSubtaskCost(parent, child, spent, unknown)
}

func FamilyCost(run Run, parent *Run) (others, budget float64, costUnknown bool) {
	if parent == nil {
		return run.SubtaskSpent, run.Budget, run.CostUnknown
	}
	return parent.Spent + parent.SubtaskSpent - run.Spent, parent.Budget, run.CostUnknown || parent.CostUnknown
}
