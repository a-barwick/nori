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

func hasUntrustedTaskOutput(t *state.ConversationTurn, tasks TaskContext) bool {
	for _, in := range t.Inputs {
		if in.Event != nil {
			return true
		}
	}
	return len(tasks.EventTasks) > 0
}

func sideEffectingCall(call ToolCall) bool {
	return call.Function.Name != "show_result"
}

func isMemoryToolCall(call ToolCall) bool {
	return call.Function.Name == state.MemoryRemember || call.Function.Name == state.MemoryForget
}

func (r *Runner) planUserCommands(ctx context.Context, t *state.ConversationTurn, reply Reply, request ReplyRequest,
	messages []json.RawMessage, inputID string,
) (Reply, error) {
	plan, err := r.model.Respond(ctx, ReplyRequest{
		Messages: messages, CommandPlan: true, TaskIDs: request.TaskIDs, MemoryIDs: request.MemoryIDs,
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
	planCmdCalls, planMemCalls, err := splitMemory(plan.Calls)
	if err != nil {
		return reply, err
	}
	commands, err := commandRequests(planCmdCalls)
	if err != nil {
		return reply, err
	}
	for i, command := range commands {
		if !plannedCommand(planCmdCalls[i]) || (command.Kind == state.CommandSteer && !slices.Contains(request.TaskIDs, command.RunID)) {
			return reply, fmt.Errorf("independent command plan returned an unsupported command")
		}
	}
	for _, mc := range planMemCalls {
		if mc.change.Kind == state.MemoryForget || (mc.change.Kind == state.MemoryRemember && mc.change.Replaces) {
			if !slices.Contains(request.MemoryIDs, mc.change.ID) {
				return reply, fmt.Errorf("independent command plan named an unknown memory ID")
			}
		}
	}
	planMemToolCalls := make([]ToolCall, 0, len(planMemCalls))
	for _, call := range plan.Calls {
		if isMemoryToolCall(call) {
			planMemToolCalls = append(planMemToolCalls, call)
		}
	}

	proposed := []ToolCall{}
	for _, call := range reply.Calls {
		if plannedCommand(call) {
			proposed = append(proposed, call)
		}
	}
	_, proposedMemCalls, err := splitMemory(reply.Calls)
	if err != nil {
		return reply, err
	}
	original, err := commandRequests(proposed)
	if err != nil {
		return reply, err
	}
	matched := len(original) == len(commands)
	for i := 0; matched && i < len(original); i++ {
		matched = original[i].Kind == commands[i].Kind && original[i].RunID == commands[i].RunID
	}
	if matched && len(planMemCalls) > 0 {
		used := make([]bool, len(proposedMemCalls))
		for _, plannedMem := range planMemCalls {
			found := false
			for j, propMem := range proposedMemCalls {
				if used[j] || propMem.change.Kind != plannedMem.change.Kind {
					continue
				}
				if plannedMem.change.Kind == state.MemoryForget && propMem.change.ID != plannedMem.change.ID {
					continue
				}
				used[j] = true
				found = true
				break
			}
			if !found {
				matched = false
				break
			}
		}
	}
	if !matched {
		reply.Calls = nil
		reply.Text = prompts.Runtime("request-clarify", nil)
		if len(plan.Calls) == 0 && strings.TrimSpace(plan.Text) != "" {
			reply.Text = plan.Text
		}
	} else {
		verified := make([]ToolCall, 0, len(planCmdCalls)+len(planMemToolCalls))
		for _, planned := range planCmdCalls {
			planned.ID = fmt.Sprintf("action_%d", len(verified))
			verified = append(verified, planned)
		}
		for _, planned := range planMemToolCalls {
			planned.ID = fmt.Sprintf("action_%d", len(verified))
			verified = append(verified, planned)
		}
		reply.Calls = verified
		reply.Text = plan.Text
		if len(reply.Calls) == 0 && strings.TrimSpace(reply.Text) == "" {
			reply.Text = prompts.Runtime("request-clarify", nil)
		}
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
	type planCommand struct {
		Kind           state.CommandKind `json:"kind"`
		SourceInputIDs []string          `json:"source_input_ids"`
	}
	type planTask struct {
		ID               string          `json:"id"`
		ParentID         string          `json:"parent_id"`
		Status           state.RunStatus `json:"status"`
		CancelRequested  bool            `json:"cancel_requested"`
		QuestionKind     string          `json:"question_kind"`
		UserSources      []userMessage   `json:"user_sources"`
		AcceptedCommands []planCommand   `json:"accepted_commands"`
	}
	buildTaskGroup := func(records []*TaskContextRecord) ([]planTask, error) {
		group := make([]planTask, 0, len(records))
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
			accepted := []planCommand{}
			for _, cmd := range task.Commands {
				accepted = append(accepted, planCommand{Kind: cmd.Kind, SourceInputIDs: cmd.SourceInputIDs})
			}
			questionKind := ""
			if q := task.Question; q != nil && q.ResolvedBy == "" && q.Revision == task.Revision {
				switch q.Kind {
				case "input", state.QuestionWork:
					questionKind = q.Kind
				}
			}
			group = append(group, planTask{
				ID: task.ID, ParentID: task.ParentID, Status: task.Status,
				CancelRequested: task.CancelRequested, QuestionKind: questionKind,
				UserSources: users, AcceptedCommands: accepted,
			})
		}
		return group, nil
	}
	activeTasks, err := buildTaskGroup(tasks.ActiveTasks)
	if err != nil {
		return nil, err
	}
	eventTasks, err := buildTaskGroup(tasks.EventTasks)
	if err != nil {
		return nil, err
	}
	recentTasks, err := buildTaskGroup(tasks.RecentTasks)
	if err != nil {
		return nil, err
	}
	results := []map[string]string{}
	for _, result := range tasks.SavedResults {
		results = append(results, map[string]string{"id": result.ID, "task_id": result.TaskID, "superseded_by": result.SupersededBy})
	}
	type planMemoryItem struct {
		ID      string `json:"id"`
		Text    string `json:"text"`
		About   string `json:"about,omitempty"`
		Section string `json:"section,omitempty"`
	}
	savedMemories := make([]planMemoryItem, 0, len(view.Memories))
	for _, m := range view.Memories {
		if m.EffectiveProvenance() == state.ProvenanceTaskOutcome {
			continue
		}
		savedMemories = append(savedMemories, planMemoryItem{
			ID:      m.ID,
			Text:    m.Text,
			About:   m.About,
			Section: m.Section,
		})
	}
	systemMsg := modelMessage(map[string]string{"role": "system", "content": prompts.CommandPlan})
	currentMsg := modelMessage(map[string]string{"role": "user", "content": prompts.Runtime("command-plan-current-user-messages-in-acceptance-order", nil) + string(modelMessage(current))})
	contextPrefix := prompts.Runtime("command-plan-historical-user-messages-and-runtime-task-facts-for-ref", nil)
	buildContext := func() (json.RawMessage, int) {
		allTasks := make([]planTask, 0, len(activeTasks)+len(eventTasks)+len(recentTasks))
		allTasks = append(allTasks, activeTasks...)
		allTasks = append(allTasks, eventTasks...)
		allTasks = append(allTasks, recentTasks...)
		ctxMsg := modelMessage(map[string]string{"role": "system", "content": contextPrefix + string(modelMessage(map[string]any{
			"history":        history,
			"tasks":          allTasks,
			"saved_results":  results,
			"saved_memories": savedMemories,
		}))})
		return ctxMsg, len(systemMsg) + len(ctxMsg) + len(currentMsg)
	}
	ctxMsg, size := buildContext()
	for size > state.OuterInputBytes && len(history) > 0 {
		history = history[1:]
		ctxMsg, size = buildContext()
	}
	for size > state.OuterInputBytes && len(results) > 0 {
		results = results[:len(results)-1]
		ctxMsg, size = buildContext()
	}
	for size > state.OuterInputBytes && len(recentTasks) > 0 {
		recentTasks = recentTasks[:len(recentTasks)-1]
		ctxMsg, size = buildContext()
	}
	for size > state.OuterInputBytes && len(eventTasks) > 0 {
		eventTasks = eventTasks[:len(eventTasks)-1]
		ctxMsg, size = buildContext()
	}
	for size > state.OuterInputBytes && len(savedMemories) > 0 {
		savedMemories = savedMemories[:len(savedMemories)-1]
		ctxMsg, size = buildContext()
	}
	for size > state.OuterInputBytes {
		target := -1
		for i := range activeTasks {
			if len(activeTasks[i].UserSources) > 0 && (target < 0 || len(activeTasks[i].UserSources) > len(activeTasks[target].UserSources)) {
				target = i
			}
		}
		if target < 0 {
			break
		}
		activeTasks[target].UserSources = activeTasks[target].UserSources[1:]
		ctxMsg, size = buildContext()
	}
	for size > state.OuterInputBytes {
		target := -1
		for i := range activeTasks {
			if len(activeTasks[i].AcceptedCommands) > 0 && (target < 0 || len(activeTasks[i].AcceptedCommands) > len(activeTasks[target].AcceptedCommands)) {
				target = i
			}
		}
		if target < 0 {
			break
		}
		activeTasks[target].AcceptedCommands = activeTasks[target].AcceptedCommands[1:]
		ctxMsg, size = buildContext()
	}
	if size > state.OuterInputBytes {
		return nil, fmt.Errorf("user command context exceeds its size limit; no commands accepted")
	}
	return []json.RawMessage{systemMsg, ctxMsg, currentMsg}, nil
}
