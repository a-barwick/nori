package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"nori/internal/prompts"
	"nori/internal/state"
)

const taskContextLimit = 12 << 10

type TaskContext struct {
	ActiveTasks  []*TaskContextRecord `json:"active_tasks"`
	EventTasks   []*TaskContextRecord `json:"event_task_records"`
	RecentTasks  []*TaskContextRecord `json:"recent_task_records"`
	SavedResults []SavedResult        `json:"saved_results,omitempty"`
	Truncated    bool                 `json:"truncated"`
	Instructions string               `json:"instructions"`
}

// SavedResult is what the outer loop knows about a saved result. Titles came from task requests and tool calls, so
// they're untrusted data.
type SavedResult struct {
	ID           string     `json:"id"`
	TaskID       string     `json:"task_id"`
	Kind         string     `json:"kind"`
	Title        string     `json:"title"`
	Version      int        `json:"version"`
	SupersededBy string     `json:"superseded_by,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	CheckedAt    *time.Time `json:"checked_at,omitempty"`
	ReportedAt   *time.Time `json:"reported_at,omitempty"`
	DeliveredAt  *time.Time `json:"delivered_at,omitempty"`
}

type TaskContextRecord struct {
	// Kept separately so a shortened question can't be mistaken for the exact action shown in chat.
	QuestionTextTruncated   bool                 `json:"-"`
	Holds                   []state.TaskHold     `json:"held_questions,omitempty"`
	ID                      string               `json:"id"`
	ParentID                string               `json:"parent_id,omitempty"`
	Subtasks                []state.SubtaskLink  `json:"subtasks,omitempty"`
	SubtaskSpent            float64              `json:"subtask_spent,omitempty"`
	Request                 string               `json:"request"`
	Status                  state.RunStatus      `json:"status"`
	Question                *state.Question      `json:"question,omitempty"`
	Waiting                 string               `json:"waiting,omitempty"`
	Progress                string               `json:"progress,omitempty"`
	Revision                int64                `json:"revision"`
	Position                int64                `json:"position"`
	Incorporated            int64                `json:"incorporated"`
	CancelRequested         bool                 `json:"cancel_requested"`
	Spent                   float64              `json:"spent"`
	Budget                  float64              `json:"budget"`
	Error                   string               `json:"error,omitempty"`
	Result                  string               `json:"result,omitempty"`
	Commands                []TaskContextCommand `json:"commands"`
	CommandHistoryTruncated bool                 `json:"command_history_truncated,omitempty"`
	TextTruncated           bool                 `json:"text_truncated,omitempty"`
	Outcome                 *state.TaskOutcome   `json:"outcome,omitempty"`
	OutcomeUnavailable      bool                 `json:"outcome_unavailable,omitempty"`
}

type TaskContextCommand struct {
	ID             string            `json:"id"`
	Kind           state.CommandKind `json:"kind"`
	Text           string            `json:"text,omitempty"`
	QuestionID     string            `json:"question_id,omitempty"`
	Position       int64             `json:"position"`
	Revision       int64             `json:"revision"`
	SourceInputIDs []string          `json:"source_input_ids,omitempty"`
	Accepted       bool              `json:"accepted"`
	Applied        bool              `json:"applied"`
	TextTruncated  bool              `json:"text_truncated,omitempty"`
}

// ContextRecords reads each selected task's accepted commands once. Direct panel
// commands are user instructions even though they create no conversation message.
func (t *Tasks) ContextRecords(ctx context.Context, runs []state.Run, eventRunIDs []string, since time.Time) (TaskContext, error) {
	result := TaskContext{ActiveTasks: []*TaskContextRecord{}, EventTasks: []*TaskContextRecord{}, RecentTasks: []*TaskContextRecord{},
		Instructions: prompts.Runtime("task-commands", nil)}
	seen := map[string]bool{}
	var records []*TaskContextRecord
	add := func(id string, destination *[]*TaskContextRecord) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		record, err := t.contextRecord(ctx, id)
		if err != nil {
			return err
		}
		*destination = append(*destination, record)
		records = append(records, record)
		return nil
	}
	for _, run := range runs {
		if run.Status == state.RunRunning {
			if err := add(run.ID, &result.ActiveTasks); err != nil {
				return result, err
			}
		}
	}
	for _, id := range eventRunIDs {
		if err := add(id, &result.EventTasks); err != nil {
			return result, err
		}
	}
	// A parent's report needs its subtasks' writes and results.
	for _, id := range eventRunIDs {
		snapshot, err := t.Store.GetRun(ctx, id)
		if err != nil {
			return result, err
		}
		for _, link := range snapshot.Subtasks {
			if err := add(link.ID, &result.EventTasks); err != nil {
				return result, err
			}
		}
	}
	// Only the turn reporting a task's end compares its answer with the cited results.
	for _, record := range result.EventTasks {
		if record.Status == state.RunRunning || record.Status == "cancelled" {
			continue
		}
		outcome, err := t.Store.ReadTaskOutcome(ctx, record.ID)
		if err != nil {
			record.OutcomeUnavailable = true
			continue
		}
		if len(outcome.Unmet) > 0 || len(outcome.Evidence) > 0 {
			record.Outcome = &outcome
		}
	}
	for _, run := range runs {
		if seen[run.ID] {
			continue
		}
		if len(result.RecentTasks) >= 10 {
			result.Truncated = true
			continue
		}
		if err := add(run.ID, &result.RecentTasks); err != nil {
			return result, err
		}
	}
	saved, err := t.Store.RecentResults(ctx, state.RecentResultLimit)
	if err != nil {
		return result, err
	}
	for _, item := range saved {
		if item.CreatedAt.Before(since) {
			continue
		}
		result.SavedResults = append(result.SavedResults, SavedResult{
			ID: item.ID, TaskID: item.RunID, Kind: item.Kind, Title: item.Title, Version: item.Version, SupersededBy: item.SupersededBy,
			CreatedAt: item.CreatedAt, CheckedAt: item.CheckedAt, ReportedAt: item.ReportedAt, DeliveredAt: item.DeliveredAt,
		})
	}
	err = fitTaskContext(&result, records)
	return result, err
}

func (t *Tasks) contextRecord(ctx context.Context, id string) (*TaskContextRecord, error) {
	snapshot, err := t.Store.Reconcile(ctx, id)
	if err != nil {
		return nil, err
	}
	run := snapshot.Run
	record := &TaskContextRecord{ID: run.ID, ParentID: run.ParentID, Subtasks: run.Subtasks, SubtaskSpent: run.SubtaskSpent, Request: run.Request, Status: run.Status, Waiting: run.Waiting, Progress: run.Progress,
		Revision: run.Revision, Position: run.Position, Incorporated: run.Incorporated, CancelRequested: run.CancelRequested,
		Holds: append([]state.TaskHold{}, run.Holds...), Spent: run.Spent, Budget: run.Budget, Error: run.Error, Result: run.Result,
		Commands: []TaskContextCommand{}}
	if run.Question != nil {
		question := *run.Question
		record.Question = &question
		// The current question already supplies the waiting text.
		record.Waiting = ""
	}
	for _, command := range snapshot.Commands {
		record.Commands = append(record.Commands, TaskContextCommand{ID: command.ID, Kind: command.Kind, Text: command.Text,
			QuestionID: command.QuestionID, Position: command.Position, Revision: command.Revision,
			SourceInputIDs: command.SourceInputIDs, Accepted: true, Applied: command.Position <= run.Incorporated})
	}
	return record, nil
}

func fitTaskContext(result *TaskContext, records []*TaskContextRecord) error {
	fits := func() bool { data, err := json.Marshal(*result); return err == nil && len(data) <= taskContextLimit }
	if fits() {
		return nil
	}
	result.Truncated = true
	// Prefer the newest commands when the complete histories cannot fit.
	for _, record := range records {
		if len(record.Commands) > 4 {
			record.Commands = record.Commands[len(record.Commands)-4:]
			record.CommandHistoryTruncated = true
		}
	}
	// Cited results are what the report is checked against, so they are shortened last: first other text, then
	// background (recent) tasks, then the cited results themselves.
	for _, limit := range []int{8192, 4096, 2048, 1024, 512, 256, 64, 0} {
		if fits() {
			return nil
		}
		for _, record := range records {
			trimRecordText(record, limit, false)
		}
	}
	for len(result.RecentTasks) > 0 && !fits() {
		result.RecentTasks = result.RecentTasks[:len(result.RecentTasks)-1]
	}
	for _, limit := range []int{4096, 2048, 1024, 512, 256, 64, 0} {
		if fits() {
			return nil
		}
		for _, record := range records {
			trimRecordText(record, limit, true)
		}
	}
	for !fits() {
		var longest *TaskContextRecord
		for _, record := range records {
			if len(record.Commands) > 0 && (longest == nil || len(record.Commands) > len(longest.Commands)) {
				longest = record
			}
		}
		if longest == nil {
			break
		}
		longest.Commands = longest.Commands[1:]
		longest.CommandHistoryTruncated = true
	}
	if !fits() {
		return fmt.Errorf("task identities and current questions exceed the 12 KiB context limit")
	}
	return nil
}

func trimContextText(text *string, limit int, truncated *bool) {
	if len(*text) <= limit {
		return
	}
	for limit > 0 && !utf8.RuneStart((*text)[limit]) {
		limit--
	}
	*text, *truncated = (*text)[:limit], true
}

func trimRecordText(record *TaskContextRecord, limit int, evidence bool) {
	trimContextText(&record.Request, limit, &record.TextTruncated)
	trimContextText(&record.Waiting, limit, &record.TextTruncated)
	trimContextText(&record.Progress, limit, &record.TextTruncated)
	trimContextText(&record.Error, limit, &record.TextTruncated)
	trimContextText(&record.Result, limit, &record.TextTruncated)
	if record.Outcome != nil {
		for i := range record.Outcome.Unmet {
			trimContextText(&record.Outcome.Unmet[i], limit, &record.Outcome.Shortened)
		}
		for i := range record.Outcome.Evidence {
			if evidence {
				trimContextText(&record.Outcome.Evidence[i].Result, limit, &record.Outcome.Shortened)
			}
		}
	}
	if record.Question != nil {
		trimContextText(&record.Question.Text, limit, &record.QuestionTextTruncated)
		record.TextTruncated = record.TextTruncated || record.QuestionTextTruncated
	}
	for i := range record.Holds {
		trimContextText(&record.Holds[i].Question, limit, &record.TextTruncated)
	}
	for i := range record.Commands {
		trimContextText(&record.Commands[i].Text, limit, &record.Commands[i].TextTruncated)
	}
}
