package conversation

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"nori/internal/prompts"
	"nori/internal/state"
)

// commandRequests validates the entire model batch before any command is accepted.
// splitShows separates show_result actions, which start no task, from task commands.
func splitShows(calls []ToolCall) ([]ToolCall, []string, error) {
	commands := make([]ToolCall, 0, len(calls))
	shows := []string{}
	for _, call := range calls {
		if call.Function.Name != "show_result" {
			commands = append(commands, call)
			continue
		}
		var args struct {
			ResultID string `json:"result_id"`
		}
		if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
			return nil, nil, err
		}
		if !state.ValidUploadID(args.ResultID) {
			return nil, nil, fmt.Errorf("show_result needs a saved result's ID")
		}
		shows = append(shows, args.ResultID)
	}
	return commands, shows, nil
}

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
				Request string  `json:"request"`
				For     *string `json:"for"`
				// The question it settles, so a late dispatch can't attach to a newer request.
				QuestionID *string `json:"question_id"`
				// Earlier files the task also needs. The current message's files go with it anyway.
				Attachments []string `json:"attachments"`
				// A saved result this task changes. The new result links to it.
				Revises *string `json:"revises"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, err
			}
			command.Kind, command.Text = "start", strings.TrimSpace(args.Request)
			if len(args.Attachments) > state.MaxAttachments {
				return nil, fmt.Errorf("a dispatch can name at most %d files", state.MaxAttachments)
			}
			for _, id := range args.Attachments {
				if !state.ValidUploadID(id) {
					return nil, fmt.Errorf("a dispatch names an invalid file ID")
				}
				command.Attachments = append(command.Attachments, state.Attachment{ID: id})
			}
			if args.Revises != nil && *args.Revises != "" {
				if !state.ValidUploadID(*args.Revises) {
					return nil, fmt.Errorf("a dispatch names an invalid result ID")
				}
				command.Revises = *args.Revises
			}
			if args.For != nil && *args.For != "" {
				id, err := uuid.Parse(*args.For)
				if err != nil || id.String() != *args.For {
					return nil, fmt.Errorf("task command requires a canonical task UUID")
				}
				if args.QuestionID == nil || *args.QuestionID == "" || len(*args.QuestionID) > 256 {
					return nil, fmt.Errorf("a subtask needs the question ID it settles")
				}
				command.ParentID, command.QuestionID = *args.For, *args.QuestionID
			}
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
		if command.Kind != state.CommandStart {
			id, err := uuid.Parse(command.RunID)
			if err != nil || id.String() != command.RunID {
				return nil, fmt.Errorf("task command requires a canonical task UUID")
			}
		}
		if command.Kind == state.CommandStart || command.Kind == state.CommandSteer || command.Kind == state.CommandAnswer || command.Kind == state.CommandHold {
			if command.Text == "" || len(command.Text) > state.MaxMessageBytes {
				return nil, fmt.Errorf("task command text must contain 1 to 8192 UTF-8 bytes")
			}
		}
		if command.Kind == state.CommandAnswer {
			if command.QuestionID == "" || strings.TrimSpace(command.QuestionID) != command.QuestionID || len(command.QuestionID) > 256 {
				return nil, fmt.Errorf("task command requires a question ID within 256 bytes")
			}
		}
		commands = append(commands, command)
	}
	return commands, nil
}

// A whole reply can assent; a word inside an unrelated request, quote, negation or
// correction cannot. The model doesn't get to extract an approving fragment.
func workRequests(inputs []state.ConversationInput) []WorkRequest {
	requests := []WorkRequest{}
	for _, input := range inputs {
		if input.IsUser() {
			return nil
		}
		if input.Event != nil && input.Event.Kind == "needs_input" && input.Event.Reason == state.QuestionWork {
			requests = append(requests, WorkRequest{TaskID: input.Event.RunID, QuestionID: input.Event.QuestionID})
		}
	}
	return requests
}

// workCommandsAllowed keeps a notification turn to settling its own requests. It can't stop or steer.
func workCommandsAllowed(commands []state.Command, requests []WorkRequest) bool {
	for _, command := range commands {
		allowed := false
		for _, request := range requests {
			switch command.Kind {
			case state.CommandStart:
				allowed = allowed || (command.ParentID == request.TaskID && command.QuestionID == request.QuestionID)
			case state.CommandAnswer:
				allowed = allowed || (command.RunID == request.TaskID && command.QuestionID == request.QuestionID)
			}
		}
		if !allowed {
			return false
		}
	}
	return true
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

// Tool-only model messages are valid. Acknowledge only durable receipts, after
// admission (or recovery), without another model call or a claim of completion.
func acceptedCommandText(actions []state.ConversationAction) string {
	counts := map[string]int{}
	for _, action := range actions {
		if action.Status == "accepted" {
			kind := string(action.Command.Kind)
			if action.Command.ParentID != "" {
				kind = "subtask"
			}
			counts[kind]++
		}
	}
	parts := []string{}
	if n := counts["start"]; n == 1 {
		parts = append(parts, prompts.Runtime("task-accepted", nil))
	} else if n > 1 {
		parts = append(parts, prompts.Runtime("tasks-accepted", map[string]any{"Count": n}))
	}
	for _, item := range []struct{ kind, text string }{
		{"subtask", prompts.Runtime("subtask-accepted", nil)},
		{"steer", prompts.Runtime("steer-accepted", nil)},
		{"answer", prompts.Runtime("answer-accepted", nil)},
		{"hold", prompts.Runtime("hold-accepted", nil)},
		{"stop", prompts.Runtime("stop-accepted", nil)},
	} {
		if counts[item.kind] > 0 {
			parts = append(parts, item.text)
		}
	}
	return strings.Join(parts, " ")
}
