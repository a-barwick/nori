package conversation

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"nori/internal/prompts"
	"nori/internal/state"
)

func TestOptimizationBaselineMetrics(t *testing.T) {
	now := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)

	convPromptBytes := len(prompts.Conversation)
	taskWorkerPromptBytes := len(prompts.TaskWorker)
	convToolsBytes := len(prompts.ConversationTools)

	memories := make([]state.Memory, 0, 25)
	for i := 0; i < 25; i++ {
		memories = append(memories, state.Memory{
			ID:            fmt.Sprintf("00000000-0000-4000-8000-%012d", i),
			Section:       "Preferences",
			About:         "the user",
			Text:          fmt.Sprintf("User standing preference #%d regarding scheduling, communication style, and family routines.", i),
			SourceInputID: fmt.Sprintf("11111111-1111-4000-8000-%012d", i),
			SourceText:    fmt.Sprintf("Original long verbatim user message #%d with extra conversational filler explaining why they like preference #%d.", i, i),
			Provenance:    state.ProvenanceUserExplicit,
			CreatedAt:     now.Add(-time.Duration(25-i) * 24 * time.Hour),
			UpdatedAt:     now.Add(-time.Duration(25-i) * 24 * time.Hour),
		})
	}
	summary := state.ConversationSummary{
		From:    now.Add(-72 * time.Hour),
		Through: now.Add(-1 * time.Hour),
		Gist:    "User has been coordinating Q4 launch tasks, travel itinerary, and calendar holds.",
		Turns:   18,
		Items: []state.SummaryItem{
			{Kind: "decision", Quote: "Move the Thursday sync to 2pm", InputID: "inp-1", Note: "Calendar updated"},
			{Kind: "preference", Quote: "Keep morning blocks meeting-free", InputID: "inp-2"},
		},
		Excerpts: []state.ConversationExcerpt{
			{MessageID: "inp-1", Role: "user", Text: "Move the Thursday sync to 2pm please", TurnStartedAt: now.Add(-2 * time.Hour)},
		},
		FlushedThrough:    now.Add(-1 * time.Hour),
		RefinementThrough: now.Add(-1 * time.Hour),
	}

	memMsg := memoryMessage(memories)
	sumMsg := summaryMessage(summary)

	turns := make([]state.ConversationTurn, 0, 10)
	for i := 0; i < 10; i++ {
		uMsg := string(modelMessage(map[string]string{"role": "user", "content": fmt.Sprintf("Turn %d: Please check my inbox and calendar for updates.", i)}))
		replyText := fmt.Sprintf("Checked your inbox and calendar for turn %d — everything looks on track.", i)
		if i%2 == 0 {
			wrapped, _ := json.Marshal(map[string]any{"actions": []any{}, "text": replyText})
			turns = append(turns, state.ConversationTurn{
				ID:        fmt.Sprintf("turn-%d", i),
				Status:    "completed",
				StartedAt: now.Add(-time.Duration(10-i) * time.Minute),
				Protocol: []string{
					uMsg,
					string(modelMessage(map[string]string{"role": "assistant", "content": string(wrapped)})),
					string(modelMessage(map[string]string{"role": "assistant", "content": replyText})),
				},
			})
		} else {
			turns = append(turns, state.ConversationTurn{
				ID:        fmt.Sprintf("turn-%d", i),
				Status:    "completed",
				StartedAt: now.Add(-time.Duration(10-i) * time.Minute),
				Protocol: []string{
					uMsg,
					string(modelMessage(map[string]string{"role": "assistant", "content": replyText})),
				},
			})
		}
	}

	tc := TaskContext{
		ActiveTasks: []*TaskContextRecord{{
			ID: "run-active-1", Request: "Draft Q4 update memo", Status: state.RunRunning,
			Progress: "Reading calendar", Revision: 1, Position: 1, Incorporated: 1, Budget: 1.0,
			Commands: []TaskContextCommand{{ID: "cmd-1", Kind: state.CommandStart, Text: "Draft Q4 update memo", Position: 1, Revision: 1, Accepted: true, Applied: true}},
		}},
		EventTasks:  []*TaskContextRecord{},
		RecentTasks: make([]*TaskContextRecord, 0, 6),
	}
	for i := 0; i < 6; i++ {
		tc.RecentTasks = append(tc.RecentTasks, &TaskContextRecord{
			ID: fmt.Sprintf("run-recent-%d", i), Request: fmt.Sprintf("Research vendor pricing #%d and summarize key differences.", i),
			Status: state.RunCompleted, Revision: 2, Position: 2, Incorporated: 2, Spent: 0.04, Budget: 1.0,
			Result: strings.Repeat(fmt.Sprintf("Completed summary paragraph for vendor #%d with pricing tiers and renewal dates. ", i), 4),
			Commands: []TaskContextCommand{
				{ID: fmt.Sprintf("c-%d-1", i), Kind: state.CommandStart, Text: fmt.Sprintf("Research vendor #%d", i), Position: 1, Revision: 1, Accepted: true, Applied: true},
				{ID: fmt.Sprintf("c-%d-2", i), Kind: state.CommandSteer, Text: "Focus on annual billing", Position: 2, Revision: 2, Accepted: true, Applied: true},
			},
		})
	}
	_ = fitTaskContext(&tc, append(append([]*TaskContextRecord{}, tc.ActiveTasks...), tc.RecentTasks...))
	tcJSON, _ := json.Marshal(tc)

	curTurn := &state.ConversationTurn{
		ID: "turn-current",
		Inputs: []state.ConversationInput{{
			ID: "inp-cur", Text: "What is on my schedule tomorrow afternoon?", CreatedAt: now,
		}},
	}
	view := historyView{Memories: memories, Summary: &summary, Turns: turns}
	msgs, bps := buildMessages(curTurn, nil, view, "")
	advisoryPayload := map[string]any{"user": map[string]any{}, "task_context": tc, "now": map[string]any{"utc": now.Format(time.RFC3339)}}
	advice, _ := json.Marshal(advisoryPayload)
	msgs = append(msgs, modelMessage(map[string]string{"role": "system", "content": prompts.Runtime("call-advisory-context-for-the-current-inputs", nil) + string(advice)}))

	anth := NewAnthropicModel("test-key", "claude-opus-5-5")
	anth.ReasoningEffort = "low"
	anthBody, _, err := anth.buildRequestBody(ReplyRequest{
		Messages:         msgs,
		CacheBreakpoints: bps,
		TaskIDs:          []string{"run-active-1"},
	}, 4096)
	if err != nil {
		t.Fatalf("buildRequestBody: %v", err)
	}

	slimCoreJSON, _ := json.Marshal(projectMaintenanceMemories(memories, false))

	t.Logf("METRIC outer_prompt_bytes=%d", convPromptBytes)
	t.Logf("METRIC task_worker_prompt_bytes=%d", taskWorkerPromptBytes)
	t.Logf("METRIC conv_tools_bytes=%d", convToolsBytes)
	t.Logf("METRIC task_commands_runtime_bytes=%d", len(tc.Instructions))
	t.Logf("METRIC memory_message_bytes=%d", len(memMsg))
	t.Logf("METRIC summary_message_bytes=%d", len(sumMsg))
	t.Logf("METRIC task_context_bytes=%d", len(tcJSON))
	t.Logf("METRIC anthropic_outer_body_bytes=%d", len(anthBody))
	t.Logf("METRIC anthropic_breakpoints=%v", bps)
	t.Logf("METRIC maintenance_core_memories_bytes=%d", len(slimCoreJSON))
	t.Logf("METRIC idle_extract_delay=%s extract_turn_threshold=%d", state.IdleExtractDelay, state.ExtractTurnThreshold)
}
