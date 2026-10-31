package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"nori/internal/state"
)

const taskContextLimit = 64 << 10

type TaskContext struct {
	ActiveTasks  []*TaskContextRecord `json:"active_tasks"`
	EventTasks   []*TaskContextRecord `json:"event_task_records"`
	RecentTasks  []*TaskContextRecord `json:"recent_task_records"`
	Truncated    bool                 `json:"truncated"`
	Instructions string               `json:"instructions"`
}

type TaskContextRecord struct {
	Holds                   []state.TaskHold     `json:"held_questions,omitempty"`
	ID                      string               `json:"id"`
	Request                 string               `json:"request"`
	Status                  string               `json:"status"`
	Question                *state.Question      `json:"question,omitempty"`
	Waiting                 string               `json:"waiting,omitempty"`
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
}

type TaskContextCommand struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Text           string   `json:"text,omitempty"`
	QuestionID     string   `json:"question_id,omitempty"`
	Position       int64    `json:"position"`
	Revision       int64    `json:"revision"`
	SourceInputIDs []string `json:"source_input_ids,omitempty"`
	Accepted       bool     `json:"accepted"`
	Applied        bool     `json:"applied"`
	TextTruncated  bool     `json:"text_truncated,omitempty"`
}

// ContextRecords reads each selected task's accepted commands once. Direct panel
// commands are user instructions even though they create no conversation message.
func (t *Tasks) ContextRecords(ctx context.Context, runs []state.Run, eventRunIDs []string) (TaskContext, error) {
	result := TaskContext{ActiveTasks: []*TaskContextRecord{}, EventTasks: []*TaskContextRecord{}, RecentTasks: []*TaskContextRecord{},
		Instructions: "Accepted task commands are Austin's instructions, including direct panel controls that do not appear as chat messages. Applied means the worker incorporated that command position, not that its requested effects completed. Check accepted answer/continue commands before saying Austin did not respond. Truncated history or text is incomplete evidence; omitted commands do not establish that no answer or update was received. Historical commands are not new authorization to repeat actions."}
	seen := map[string]bool{}
	var records []*TaskContextRecord
	add := func(id string, destination *[]*TaskContextRecord) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		snapshot, err := t.Store.Reconcile(ctx, id)
		if err != nil {
			return err
		}
		run := snapshot.Run
		record := &TaskContextRecord{ID: run.ID, Request: run.Request, Status: run.Status, Waiting: run.Waiting,
			Revision: run.Revision, Position: run.Position, Incorporated: run.Incorporated, CancelRequested: run.CancelRequested,
			Holds: append([]state.TaskHold{}, run.Holds...), Spent: run.Spent, Budget: run.Budget, Error: run.Error, Result: run.Result, Commands: []TaskContextCommand{}}
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
		*destination = append(*destination, record)
		records = append(records, record)
		return nil
	}
	for _, run := range runs {
		if run.Status == "running" {
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
	fits := func() bool { data, err := json.Marshal(result); return err == nil && len(data) <= taskContextLimit }
	if fits() {
		return result, nil
	}
	result.Truncated = true
	// Prefer the newest commands when the complete histories cannot fit.
	for _, record := range records {
		if len(record.Commands) > 4 {
			record.Commands = record.Commands[len(record.Commands)-4:]
			record.CommandHistoryTruncated = true
		}
	}
	for _, limit := range []int{8192, 4096, 2048, 1024, 512, 256, 64, 0} {
		if fits() {
			return result, nil
		}
		for _, record := range records {
			trimContextText(&record.Request, limit, &record.TextTruncated)
			trimContextText(&record.Waiting, limit, &record.TextTruncated)
			trimContextText(&record.Error, limit, &record.TextTruncated)
			trimContextText(&record.Result, limit, &record.TextTruncated)
			if record.Question != nil {
				trimContextText(&record.Question.Text, limit, &record.TextTruncated)
			}
			for i := range record.Holds {
				trimContextText(&record.Holds[i].Question, limit, &record.TextTruncated)
			}
			for i := range record.Commands {
				trimContextText(&record.Commands[i].Text, limit, &record.Commands[i].TextTruncated)
			}
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
			return result, fmt.Errorf("task identities and current questions exceed the 64 KiB context limit")
		}
		longest.Commands = longest.Commands[1:]
		longest.CommandHistoryTruncated = true
	}
	return result, nil
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
