package task

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"nori/internal/agent"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/transcript"
)

// attachmentNote lists the files a command gives the task. Names came from the user's device, so they're quoted as data.
func attachmentNote(attachments []state.Attachment) string {
	if len(attachments) == 0 {
		return ""
	}
	data, _ := json.Marshal(attachments)
	return prompts.Runtime("attached-files", map[string]any{"Files": string(data)})
}

// citedOutputs finds the screenshots and saved results an answer cites, whether by their ID or by the operation that
// made them.
func citedOutputs(ops []state.OperationRecord, evidence []string) (artifacts, results []string) {
	for _, op := range ops {
		for _, id := range op.ArtifactIDs {
			if !slices.Contains(evidence, op.ID) && !slices.Contains(evidence, id) {
				continue
			}
			if op.Name == "result_save" || op.Name == "result_link" {
				results = append(results, id)
			} else {
				artifacts = append(artifacts, id)
			}
		}
	}
	return artifacts, results
}

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

// clearable reports an unresolved operation that the user's answer can release. A prepared write stays
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
		line := prompts.Runtime("command-unknown", map[string]any{"Command": clip(name, failedCommandLimit)})
		if op.Outcome == "failed" {
			line = prompts.Runtime("command-failed", map[string]any{"Command": clip(name, failedCommandLimit), "ExitDetail": a.exitDetail(ctx, runID, op)})
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
		lines = append(lines, prompts.Runtime("commands-await-answer", nil))
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
	return prompts.Runtime("command-exit", map[string]any{"Code": *result.ExitCode})
}

// clearAnswered records that the user answered a question naming these operations. A repeated delivery saves the same
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
