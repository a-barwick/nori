package conversation

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"nori/internal/state"
)

// commandRequests validates the entire model batch before any command is accepted.
// The turn owner assigns stable command IDs and source inputs after validation.
func commandRequests(calls []ToolCall) ([]state.Command, error) {
	if len(calls) > 16 {
		return nil, fmt.Errorf("a turn can issue at most 16 task commands")
	}
	commands := make([]state.Command, 0, len(calls))
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		if call.Type != "function" || strings.TrimSpace(call.ID) == "" || len(call.ID) > 256 || seen[call.ID] {
			return nil, fmt.Errorf("invalid or duplicate task tool call ID")
		}
		seen[call.ID] = true
		var command state.Command
		switch call.Function.Name {
		case "dispatch":
			var args struct {
				Request string `json:"request"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, err
			}
			command.Kind, command.Text = "start", strings.TrimSpace(args.Request)
		case "steer":
			var args struct {
				RunID     string `json:"run_id"`
				Text      string `json:"text"`
				Interrupt bool   `json:"interrupt,omitempty"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, err
			}
			command.Kind, command.RunID, command.Text = "steer", args.RunID, strings.TrimSpace(args.Text)
			command.Interrupt = args.Interrupt
		case "answer":
			var args struct {
				RunID      string `json:"run_id"`
				QuestionID string `json:"question_id"`
				Text       string `json:"text"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, err
			}
			command.Kind, command.RunID, command.QuestionID, command.Text = "answer", args.RunID, args.QuestionID, strings.TrimSpace(args.Text)
		case "continue":
			var args struct {
				RunID      string `json:"run_id"`
				QuestionID string `json:"question_id"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, err
			}
			command.Kind, command.RunID, command.QuestionID = "continue", args.RunID, args.QuestionID
		case "hold":
			var args struct {
				TaskID   string `json:"task_id"`
				Question string `json:"question"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, err
			}
			command.Kind, command.RunID, command.Text = "hold", args.TaskID, strings.TrimSpace(args.Question)
		case "stop":
			var args struct {
				RunID string `json:"run_id"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, err
			}
			command.Kind, command.RunID = "stop", args.RunID
		default:
			return nil, fmt.Errorf("unknown task command tool")
		}
		if command.Kind != "start" {
			id, err := uuid.Parse(command.RunID)
			if err != nil || id.String() != command.RunID {
				return nil, fmt.Errorf("task command requires a canonical task UUID")
			}
		}
		if command.Kind == "start" || command.Kind == "steer" || command.Kind == "answer" || command.Kind == "hold" {
			if command.Text == "" || len(command.Text) > 8192 {
				return nil, fmt.Errorf("task command text must contain 1 to 8192 UTF-8 bytes")
			}
		}
		if command.Kind == "answer" || command.Kind == "continue" {
			if command.QuestionID == "" || strings.TrimSpace(command.QuestionID) != command.QuestionID || len(command.QuestionID) > 256 {
				return nil, fmt.Errorf("task command requires a question ID within 256 bytes")
			}
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func decodeCommandArguments(arguments string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid task command arguments")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid task command arguments")
	}
	return nil
}
