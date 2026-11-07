package conversation

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"nori/internal/prompts"
	"nori/internal/state"
)

// memoryMessage shows the saved memories. It comes right after the instructions, so it is part of the stable prefix
// of every request.
func memoryMessage(memories []state.Memory) json.RawMessage {
	content := prompts.Runtime("memories-none", nil)
	if len(memories) > 0 {
		type shown struct {
			ID    string `json:"id"`
			Text  string `json:"text"`
			About string `json:"about"`
			Saved string `json:"saved"`
		}
		list := make([]shown, 0, len(memories))
		for _, m := range memories {
			list = append(list, shown{ID: m.ID, Text: m.Text, About: m.About, Saved: m.UpdatedAt.Format("2006-01-02")})
		}
		data, _ := json.Marshal(list)
		content = prompts.Runtime("memories", map[string]any{"Memories": string(data)})
	}
	return modelMessage(map[string]string{"role": "system", "content": content})
}

// summaryMessage shows the summary of the turns before the recent ones.
func summaryMessage(s state.ConversationSummary) json.RawMessage {
	// The keys say whose words each field is.
	type shown struct {
		Kind      string `json:"kind"`
		UserWords string `json:"user_words"`
		InputID   string `json:"input_id"`
		ModelNote string `json:"model_note,omitempty"`
	}
	items := make([]shown, 0, len(s.Items))
	for _, item := range s.Items {
		items = append(items, shown{item.Kind, item.Quote, item.InputID, item.Note})
	}
	data, _ := json.Marshal(map[string]any{"items": items, "model_gist": s.Gist, "turns_summarized": s.Turns})
	return modelMessage(map[string]string{"role": "system", "content": prompts.Runtime("summary", map[string]any{"Summary": string(data)})})
}

// splitMemory separates remember and forget actions, which the runtime applies itself, from task commands.
func splitMemory(calls []ToolCall) ([]ToolCall, []memoryCall, error) {
	commands := make([]ToolCall, 0, len(calls))
	changes := []memoryCall{}
	for _, call := range calls {
		switch call.Function.Name {
		case state.MemoryRemember:
			var args struct {
				Text     string  `json:"text"`
				About    string  `json:"about"`
				Replaces *string `json:"replaces"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, nil, err
			}
			change := state.MemoryChange{Kind: state.MemoryRemember, Text: strings.TrimSpace(args.Text), About: strings.TrimSpace(args.About)}
			if args.Replaces != nil && *args.Replaces != "" {
				if !canonicalID(*args.Replaces) {
					return nil, nil, fmt.Errorf("remember names an invalid memory ID")
				}
				change.ID, change.Replaces = *args.Replaces, true
			}
			changes = append(changes, memoryCall{call.ID, change})
		case state.MemoryForget:
			var args struct {
				MemoryID string `json:"memory_id"`
			}
			if err := decodeCommandArguments(call.Function.Arguments, &args); err != nil {
				return nil, nil, err
			}
			if !canonicalID(args.MemoryID) {
				return nil, nil, fmt.Errorf("forget names an invalid memory ID")
			}
			changes = append(changes, memoryCall{call.ID, state.MemoryChange{Kind: state.MemoryForget, ID: args.MemoryID}})
		default:
			commands = append(commands, call)
		}
	}
	return commands, changes, nil
}

// canonicalID reports whether a memory ID is a canonical UUID, which is also a safe Firestore document ID.
func canonicalID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

type memoryCall struct {
	callID string
	change state.MemoryChange
}

// planMemory names new memories and checks every change against the saved ones. A new memory's ID comes from the
// attempt and call, so a repeated attempt names it the same way.
func planMemory(attemptID string, calls []memoryCall, saved []state.Memory) ([]state.MemoryChange, bool) {
	changes := make([]state.MemoryChange, 0, len(calls))
	for _, call := range calls {
		change := call.change
		if change.Kind == state.MemoryRemember && !change.Replaces {
			change.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(attemptID+":memory:"+call.callID)).String()
		}
		changes = append(changes, change)
	}
	return changes, state.PlanMemoryChanges(saved, changes)
}

// memoryFailureText replaces the reply when a memory change was refused, because the reply was written before the
// model knew.
func memoryFailureText(changes []state.MemoryChange) string {
	for _, c := range changes {
		if c.Error != "" {
			return prompts.Runtime("memory-change-failed", map[string]any{"Error": c.Error})
		}
	}
	return ""
}

// memoryText acknowledges applied memory changes when the model wrote no reply text.
func memoryText(changes []state.MemoryChange) string {
	if len(changes) == 0 {
		return ""
	}
	return prompts.Runtime("memory-changed", nil)
}
