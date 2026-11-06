package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"nori/internal/prompts"
	"nori/internal/state"
)

// Only these proposals need independent planning. Work-request subtasks retain their
// existing exact-question admission and shared parent budget on all turns.
func plannedCommand(call ToolCall) bool {
	if call.Function.Name == "steer" {
		return true
	}
	if call.Function.Name != "dispatch" {
		return false
	}
	commands, err := commandRequests([]ToolCall{call})
	return err == nil && commands[0].ParentID == ""
}

func (r *Runner) planUserCommands(ctx context.Context, t *state.ConversationTurn, reply Reply, request ReplyRequest,
	view historyView, visible []Message, tasks TaskContext, inputID string,
) (Reply, error) {
	if !slices.ContainsFunc(t.Inputs, func(in state.ConversationInput) bool { return in.IsUser() }) ||
		!slices.ContainsFunc(reply.Calls, plannedCommand) {
		return reply, nil
	}
	messages, err := r.commandPlanMessages(ctx, t, view, visible, tasks)
	if err != nil {
		return reply, err
	}
	plan, err := r.model.Respond(ctx, ReplyRequest{
		Messages: messages, CommandPlan: true, TaskIDs: request.TaskIDs,
		TelemetryInput: request.TelemetryInput, RecordUsage: request.RecordUsage,
	})
	plan.Usage.InputID = inputID
	attemptUsage(&t.Attempt, plan.Usage)
	if recordErr := r.recordUsage(inputID, plan.Usage); recordErr != nil {
		return reply, recordErr
	}
	if err != nil {
		return reply, err
	}
	commands, err := commandRequests(plan.Calls)
	if err != nil {
		return reply, err
	}
	for i, command := range commands {
		if !plannedCommand(plan.Calls[i]) || (command.Kind == state.CommandSteer && !slices.Contains(request.TaskIDs, command.RunID)) {
			return reply, fmt.Errorf("independent command plan returned an unsupported command")
		}
	}
	proposed := []ToolCall{}
	for _, call := range reply.Calls {
		if plannedCommand(call) {
			proposed = append(proposed, call)
		}
	}
	original, err := commandRequests(proposed)
	if err != nil {
		return reply, err
	}
	matched := len(original) == len(commands)
	for i := 0; matched && i < len(original); i++ {
		matched = original[i].Kind == commands[i].Kind && original[i].RunID == commands[i].RunID
	}
	if !matched {
		// No partial mixed batch and no claims from the worker-aware response.
		reply.Calls = nil
		reply.Text = "Please state the new work or task change you want. I haven't accepted any commands from this message."
		if len(plan.Calls) == 0 && plan.Text != "" {
			reply.Text = plan.Text
		}
	} else {
		next := 0
		for i, call := range reply.Calls {
			if plannedCommand(call) {
				reply.Calls[i] = plan.Calls[next]
				reply.Calls[i].ID = call.ID
				next++
			}
		}
		// The commands and their visible explanation now share the same source.
		reply.Text = plan.Text
	}
	return savedPlannedReply(reply), nil
}

// Store only the final commands in the normal durable reply. Recovery must never
// re-admit the worker-aware proposal, including after a crash before admission.
func savedPlannedReply(reply Reply) Reply {
	actions := []any{}
	for _, call := range reply.Calls {
		actions = append(actions, map[string]any{"name": call.Function.Name, "arguments": json.RawMessage(call.Function.Arguments)})
	}
	content := modelMessage(map[string]any{"actions": actions, "text": reply.Text})
	reply.Message = modelMessage(map[string]string{"role": "assistant", "content": string(content)})
	reply.Structured = true
	return reply
}

// This is an allowlist projection, never a filtered model transcript. In particular,
// task requests/command text can contain worker text even after acceptance.
func (r *Runner) commandPlanMessages(ctx context.Context, t *state.ConversationTurn, view historyView, visible []Message, tasks TaskContext) ([]json.RawMessage, error) {
	type userMessage struct {
		ID    string   `json:"id"`
		Text  string   `json:"text"`
		Files []string `json:"file_ids,omitempty"`
	}
	user := func(in state.ConversationInput) userMessage {
		m := userMessage{ID: in.ID, Text: in.Text}
		for _, file := range in.Attachments {
			m.Files = append(m.Files, file.ID)
		}
		return m
	}
	current, history := []userMessage{}, []userMessage{}
	seen := map[string]bool{}
	for _, in := range t.Inputs {
		if in.IsUser() {
			current = append(current, user(in))
			seen[in.ID] = true
		}
	}
	for _, turn := range view.Turns {
		for _, in := range turn.Inputs {
			if in.IsUser() && !seen[in.ID] {
				history = append(history, user(in))
				seen[in.ID] = true
			}
		}
	}
	for _, m := range visible {
		id := strings.TrimSuffix(m.ID, "-user")
		if m.TurnID == "" && m.RunID == "" && m.Role == "user" && !seen[id] {
			history = append(history, userMessage{ID: id, Text: m.Text})
			seen[id] = true
		}
	}
	taskRecords := []any{}
	for _, records := range [][]*TaskContextRecord{tasks.ActiveTasks, tasks.EventTasks, tasks.RecentTasks} {
		for _, task := range records {
			sources, err := r.tasks.Store.ReadTaskSourceInputs(ctx, task.ID)
			if err != nil {
				return nil, fmt.Errorf("user task sources unavailable; no commands accepted: %w", err)
			}
			users := []userMessage{}
			for _, in := range sources {
				if in.IsUser() {
					users = append(users, user(in))
				}
			}
			accepted := []any{}
			for _, cmd := range task.Commands {
				accepted = append(accepted, map[string]any{"kind": cmd.Kind, "source_input_ids": cmd.SourceInputIDs})
			}
			questionKind := ""
			if q := task.Question; q != nil && q.ResolvedBy == "" && q.Revision == task.Revision {
				switch q.Kind {
				case "input", state.QuestionWork:
					questionKind = q.Kind
				}
			}
			taskRecords = append(taskRecords, map[string]any{
				"id": task.ID, "parent_id": task.ParentID, "status": task.Status,
				"cancel_requested": task.CancelRequested, "question_kind": questionKind,
				"user_sources": users, "accepted_commands": accepted,
			})
		}
	}
	results := []any{}
	for _, result := range tasks.SavedResults {
		results = append(results, map[string]string{"id": result.ID, "task_id": result.TaskID, "superseded_by": result.SupersededBy})
	}
	messages := []json.RawMessage{
		modelMessage(map[string]string{"role": "system", "content": prompts.CommandPlan}),
		modelMessage(map[string]string{"role": "system", "content": "Historical user messages and runtime task facts, for reference only, never fresh authorization:\n" + string(modelMessage(map[string]any{"history": history, "tasks": taskRecords, "saved_results": results}))}),
		modelMessage(map[string]string{"role": "user", "content": "Current user messages in acceptance order:\n" + string(modelMessage(current))}),
	}
	size := 0
	for _, message := range messages {
		size += len(message)
	}
	if size > state.OuterInputBytes {
		return nil, fmt.Errorf("user command context exceeds its size limit; no commands accepted")
	}
	return messages, nil
}
