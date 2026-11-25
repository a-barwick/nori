package conversation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"nori/internal/state"
	"nori/internal/usage"
)

// The reply schema is part of the cached prompt prefix, so a direct reply call's schema must not change with the
// conversation's tasks or memories.
func TestStableReplySchemaIgnoresIDs(t *testing.T) {
	a, _ := json.Marshal(responseFormat(nil, nil, nil, false, false, true))
	b, _ := json.Marshal(responseFormat([]string{"run-1", "run-2"}, nil, []string{"mem-1"}, false, false, true))
	if string(a) != string(b) {
		t.Fatal("stable reply schema changed with task/memory IDs")
	}
	for _, tool := range []string{"steer", "answer", "stop", "hold", "forget", "remember", "dispatch", "show_result"} {
		if !strings.Contains(string(a), `"enum":["`+tool+`"]`) {
			t.Fatalf("stable schema is missing the %s action", tool)
		}
	}
	if strings.Contains(string(b), "run-1") || strings.Contains(string(b), "mem-1") {
		t.Fatal("stable schema leaked a per-turn ID")
	}
	// Command-plan and work-request turns keep their enum-restricted schemas.
	plan, _ := json.Marshal(responseFormat(nil, nil, []string{"mem-1"}, true, false, true))
	if !strings.Contains(string(plan), "mem-1") {
		t.Fatal("command-plan schema lost its memory ID enum")
	}
	work, _ := json.Marshal(responseFormat([]string{"run-1"}, []WorkRequest{{TaskID: "run-1", QuestionID: "q-1"}}, nil, false, false, true))
	if !strings.Contains(string(work), "q-1") {
		t.Fatal("work-request schema lost its question enum")
	}
}

func TestValidateReplyIDs(t *testing.T) {
	call := func(name, args string) ToolCall {
		c := ToolCall{ID: "c", Type: "function"}
		c.Function.Name, c.Function.Arguments = name, args
		return c
	}
	tasks, memories := []string{"run-1"}, []string{"mem-1"}
	for _, test := range []struct {
		name string
		call ToolCall
		ok   bool
	}{
		{"known steer", call("steer", `{"run_id":"run-1","text":"x","interrupt":false}`), true},
		{"unknown steer", call("steer", `{"run_id":"run-9","text":"x","interrupt":false}`), false},
		{"unknown answer", call("answer", `{"run_id":"run-9","question_id":"q","text":"x"}`), false},
		{"unknown stop", call("stop", `{"run_id":"run-9"}`), false},
		{"unknown hold", call("hold", `{"task_id":"run-9","question":"x"}`), false},
		{"dispatch without for", call("dispatch", `{"request":"x","for":null,"question_id":null,"attachments":[],"revises":null}`), true},
		{"dispatch for known", call("dispatch", `{"request":"x","for":"run-1","question_id":null,"attachments":[],"revises":null}`), true},
		{"dispatch for unknown", call("dispatch", `{"request":"x","for":"run-9","question_id":null,"attachments":[],"revises":null}`), false},
		{"forget known", call(state.MemoryForget, `{"memory_id":"mem-1"}`), true},
		{"forget unknown", call(state.MemoryForget, `{"memory_id":"mem-9"}`), false},
		{"new memory", call(state.MemoryRemember, `{"text":"x","about":"u","section":"s","replaces":null}`), true},
		{"correct unknown memory", call(state.MemoryRemember, `{"text":"x","about":"u","section":"s","replaces":"mem-9"}`), false},
		{"show result is not task-scoped", call("show_result", `{"result_id":"anything"}`), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateReplyIDs([]ToolCall{test.call}, tasks, memories)
			if (err == nil) != test.ok {
				t.Fatalf("validateReplyIDs ok=%v, err=%v", test.ok, err)
			}
		})
	}
}

func TestOpenAIConversationMarksBreakpoints(t *testing.T) {
	msgs := []json.RawMessage{
		modelMessage(map[string]string{"role": "system", "content": "static"}),
		modelMessage(map[string]string{"role": "user", "content": "hello"}),
		modelMessage(map[string]string{"role": "assistant", "content": "hi"}),
	}
	out, err := openAIConversation(msgs, []int{0, 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{{"type": "text", "text": "static", "prompt_cache_breakpoint": map[string]string{"mode": "explicit"}}}
	if !reflect.DeepEqual(out[0]["content"], want) {
		t.Fatalf("breakpoint message = %#v", out[0]["content"])
	}
	if out[1]["content"] != "hello" {
		t.Fatalf("unmarked message should stay a plain string, got %#v", out[1]["content"])
	}
	if _, ok := out[2]["content"].([]map[string]any); !ok {
		t.Fatal("assistant breakpoint not marked")
	}
}

func TestBuildMessagesBreakpoints(t *testing.T) {
	turn := func(text string) state.ConversationTurn {
		return state.ConversationTurn{Protocol: []string{
			string(modelMessage(map[string]string{"role": "user", "content": text})),
			string(modelMessage(map[string]string{"role": "assistant", "content": "ok " + text})),
		}}
	}
	view := historyView{Turns: []state.ConversationTurn{turn("a"), turn("b"), turn("c")}}
	msgs, bps := buildMessages(&state.ConversationTurn{}, nil, view, "")
	// system(0), memory(1), then a(2,3) b(4,5) c(6,7): last two turn ends are 5 and 7.
	if want := []int{0, 1, 5, 7}; !reflect.DeepEqual(bps, want) {
		t.Fatalf("breakpoints = %v, want %v (messages=%d)", bps, want, len(msgs))
	}
	// Next turn: this turn's newest history end (7) is the next request's second-to-last turn end.
	view.Turns = append(view.Turns, turn("d"))
	_, next := buildMessages(&state.ConversationTurn{}, nil, view, "")
	if next[2] != bps[3] {
		t.Fatalf("next turn does not re-mark the previous write position: %v vs %v", next, bps)
	}
}

func TestSolCostWithWrites(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	// 10k input: 6k read from cache, 3k written, 1k ordinary; 100 output.
	got, err := usage.SolCostWithWrites("gpt-6.1-sol", "default", i(10_000), i(100), i(10_100), 6_000, 3_000)
	if err != nil {
		t.Fatal(err)
	}
	want := (1_000*2.0 + 6_000*0.10 + 3_000*2.50 + 100*10.0) / 1e6
	if diff := got - want; diff > 1e-12 || diff < -1e-12 {
		t.Fatalf("cost = %.9f, want %.9f", got, want)
	}
	if _, err := usage.SolCostWithWrites("gpt-6.1-sol", "default", i(10), i(1), i(11), 6, 6); err == nil {
		t.Fatal("reads plus writes above input must be rejected")
	}
}
