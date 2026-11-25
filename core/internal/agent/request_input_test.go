package agent_test

import (
	"testing"

	"nori/internal/agent"
	"nori/internal/transcript"
)

func TestValidateCallsStructuredRequestInput(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{
			name: "legacy_question_only",
			args: map[string]any{"question": "Which date works best?"},
		},
		{
			name: "confirm_input",
			args: map[string]any{"question": "Send this draft now?", "input": "confirm", "options": []any{}},
		},
		{
			name: "choice_with_options",
			args: map[string]any{
				"question": "Which calendar should I use?",
				"input":    "choice",
				"options": []any{
					map[string]any{"id": "work", "label": "Work"},
					map[string]any{"id": "personal", "label": "Personal"},
				},
			},
		},
		{
			name: "multi_choice_with_options",
			args: map[string]any{
				"question": "Which topics should the briefing cover?",
				"input":    "multi_choice",
				"options": []any{
					map[string]any{"id": "cal", "label": "Calendar"},
					map[string]any{"id": "mail", "label": "Unread email"},
				},
			},
		},
		{
			name:    "choice_missing_options",
			args:    map[string]any{"question": "Pick one", "input": "choice", "options": []any{}},
			wantErr: true,
		},
		{
			name:    "invalid_input_type",
			args:    map[string]any{"question": "Pick one", "input": "slider", "options": []any{}},
			wantErr: true,
		},
		{
			name: "duplicate_option_ids",
			args: map[string]any{
				"question": "Pick one",
				"input":    "choice",
				"options": []any{
					map[string]any{"id": "dup", "label": "A"},
					map[string]any{"id": "dup", "label": "B"},
				},
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := agent.ValidateCalls([]transcript.ToolCall{{
				ID:   "call-1",
				Name: "request_input",
				Args: tc.args,
			}}, true, true, nil)
			if tc.wantErr && err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}
