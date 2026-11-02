package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"nori/internal/agent"
	"nori/internal/state"
	"nori/internal/transcript"
)

// failedCommandLimit keeps a long script from filling the question.
const failedCommandLimit = 500

// failureCleared says why a failed call can't have changed anything, or "" when it might have.
func failureCleared(call transcript.ToolCall, output map[string]any) string {
	switch call.Name {
	case "shell":
		if readOnly, _ := call.Args["read_only"].(bool); readOnly {
			return state.ClearedReadOnly
		}
		// 126 and 127 mean bash couldn't start the program. That proves nothing ran only for a single simple command,
		// because anything joined to it might have run first.
		code, _ := output["exit_code"].(float64)
		command, _ := call.Args["command"].(string)
		if (code == 126 || code == 127) && !strings.ContainsAny(command, ";&|<>()$`\n") {
			return state.ClearedDidNotRun
		}
	case "job_start":
		if output["state"] == "failed_to_start" {
			return state.ClearedDidNotRun
		}
	}
	return ""
}

// clearable reports an unresolved operation that Austin's answer can release. A write that needs approval stays
// unresolved, because the provider decides what happened to it, and so does a record with no saved result.
func clearable(op state.OperationRecord) bool {
	return unresolvedWrite(op) && agent.WriteEffect(op.Name) == "" && op.Name != "request_work" && op.ResultRef != "" &&
		(op.Outcome == "failed" || op.Outcome == "uncertain")
}

// nameUnresolved returns the lines that name the task's unresolved operations for a question, and which of them an
// answer releases. The runtime writes these lines because a model's question can leave the command out.
func (a *Activities) nameUnresolved(ctx context.Context, runID string, messages []transcript.Message) (string, []string, error) {
	ops, err := a.Store.Operations(ctx, runID)
	if err != nil {
		return "", nil, err
	}
	commands := map[string]string{}
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.ToolCall != nil {
				commands[part.ToolCall.ID], _ = part.ToolCall.Args["command"].(string)
			}
		}
	}
	var lines []string
	var clears []string
	for _, op := range ops {
		if !unresolvedWrite(op) {
			continue
		}
		name := commands[op.CallID]
		if name == "" {
			name = op.Name
		}
		line := fmt.Sprintf("The command `%s` has no known result and may have changed something.", clip(name, failedCommandLimit))
		if op.Outcome == "failed" {
			line = fmt.Sprintf("The command `%s` failed%s and may have changed something.", clip(name, failedCommandLimit), a.exitDetail(ctx, runID, op))
		}
		lines = append(lines, line)
		if clearable(op) {
			clears = append(clears, op.ID)
		}
	}
	if len(lines) == 0 {
		return "", nil, nil
	}
	if len(clears) > 0 {
		lines = append(lines, "Later commands in this task can't run until you answer.")
	}
	return strings.Join(lines, "\n"), clears, nil
}

func (a *Activities) exitDetail(ctx context.Context, runID string, op state.OperationRecord) string {
	if op.ResultRef == "" {
		return ""
	}
	blocks, err := a.Store.ReadHistory(ctx, runID, op.ResultRef)
	if err != nil || len(blocks) != 1 {
		return ""
	}
	var result struct {
		ExitCode *int `json:"exit_code"`
	}
	if json.Unmarshal(blocks[0], &result) != nil || result.ExitCode == nil {
		return ""
	}
	return fmt.Sprintf(" with exit code %d", *result.ExitCode)
}

// clearAnswered records that Austin answered a question naming these operations. A repeated delivery saves the same
// record again.
func (a *Activities) clearAnswered(ctx context.Context, runID string, q *state.Question) error {
	if len(q.Clears) == 0 {
		return nil
	}
	named := map[string]bool{}
	for _, id := range q.Clears {
		named[id] = true
	}
	ops, err := a.Store.Operations(ctx, runID)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if !named[op.ID] || op.Cleared != "" {
			continue
		}
		op.Cleared = state.ClearedAnswered
		if err := a.Store.PutOperation(ctx, runID, op); err != nil {
			return err
		}
	}
	return nil
}
