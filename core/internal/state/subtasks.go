package state

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	TaskBudgetUSD = 1.0
	MaxSubtasks   = 4
	// QuestionWork is the question a task parks on after request_work.
	QuestionWork = "work"
)

// SubtaskLink is what a parent records about one subtask. A task has at most one parent, and a subtask has none of its
// own, so dependency loops can't form.
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

// linkSubtask checks that parent is waiting on an open work request, and links the new subtask to it. Every subtask for
// one request is accepted in one batch, so the parent's result can't be delivered before a sibling is linked. opened
// holds the requests this batch has already resolved.
func linkSubtask(parent Run, exists bool, cmd Command, opened map[string]bool) (Run, Question, error) {
	if !exists {
		return parent, Question{}, ErrNotFound
	}
	if parent.Status != RunRunning {
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

// StopSubtask requests Stop on a running subtask because its parent stopped, failed or changed. cause makes the command
// ID stable when a transaction retries. It reports false for a subtask that has already ended or been stopped.
func StopSubtask(child Run, cause string, now time.Time) (Run, Command, bool) {
	if child.Status != RunRunning || child.CancelRequested {
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

// SettleSubtask records a subtask's end on its parent, and returns the subtask_result command when it was the last one
// the parent's open request was waiting on. before is the subtask as last saved, so a repeat adds no cost. waiting is
// false when the parent no longer waits on this request, because a correction or Stop closed it.
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
	if parent.Status != RunRunning || parent.CancelRequested || question == nil || question.ID != questionID || question.Revision != parent.Revision {
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

// addSubtaskCost moves a subtask's newly recorded cost onto its parent. It works from the subtask's saved total, so a
// retry or a repeated projection adds nothing, and concurrent subtasks serialize on the parent's document.
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

// FamilyCost returns what the rest of a task's family has spent, and the budget and unknown-cost state they share.
// parent is nil for a top-level task.
func FamilyCost(run Run, parent *Run) (others, budget float64, costUnknown bool) {
	if parent == nil {
		return run.SubtaskSpent, run.Budget, run.CostUnknown
	}
	return parent.Spent + parent.SubtaskSpent - run.Spent, parent.Budget, run.CostUnknown || parent.CostUnknown
}
