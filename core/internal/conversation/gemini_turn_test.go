package conversation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
	"nori/internal/state"
)

func TestGeminiTaskNotificationRequest(t *testing.T) {
	user := modelMessage(map[string]string{"role": "user", "content": "Check the weather."})
	ack := modelMessage(map[string]string{"role": "assistant", "content": "I'll check."})
	event := inputMessage(state.ConversationInput{Event: &state.TaskEvent{}})
	advice := modelMessage(map[string]string{"role": "system", "content": "Current task context."})
	for _, tc := range []struct {
		name       string
		model      string
		messages   []json.RawMessage
		appendTurn bool
	}{
		{"gateway task notification", "google/gemini-4-neon", []json.RawMessage{user, ack, event, advice}, true},
		{"direct Gemini task notification", "gemini-4-neon", []json.RawMessage{user, ack, event, advice}, true},
		{"system only", "google/gemini-4-neon", []json.RawMessage{event, advice}, true},
		{"user reply", "google/gemini-4-neon", []json.RawMessage{user, ack, user, advice}, false},
		{"other gateway model", "openai/gpt-6.2-sol", []json.RawMessage{user, ack, event, advice}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := NewGatewayModel("http://localhost", "", tc.model)
			body, err := model.requestBody(ReplyRequest{Messages: tc.messages, NoDispatch: true}, 4096, trace.SpanFromContext(context.Background()))
			if err != nil {
				t.Fatal(err)
			}
			var request struct {
				Messages []struct{ Role, Content string }
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			want := len(tc.messages)
			if tc.appendTurn {
				want++
			}
			if len(request.Messages) != want {
				t.Fatalf("got %d messages, want %d", len(request.Messages), want)
			}
			for i, raw := range tc.messages {
				var original struct{ Role, Content string }
				if err := json.Unmarshal(raw, &original); err != nil {
					t.Fatal(err)
				}
				if request.Messages[i] != original {
					t.Fatalf("changed original message %d", i)
				}
			}
			last := request.Messages[want-1]
			if tc.appendTurn && (last.Role != "user" || strings.TrimSpace(last.Content) == "") {
				t.Fatal("missing final input turn")
			}
		})
	}
}
