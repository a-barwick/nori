package conversation

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"nori/internal/llmguard"
	"nori/internal/state"
)

// TestLivePromptCacheHits sends consecutive outer-model turns through the real request builder and OpenAI, and checks
// that each turn reads the previous turn's prefix from the prompt cache, including after a new Core Memory (and so a
// new memory ID) appears. Opt-in: NORI_LIVE_CACHE_TEST=1 and OPENAI_API_KEY. Costs a few cents.
func TestLivePromptCacheHits(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if os.Getenv("NORI_LIVE_CACHE_TEST") != "1" || key == "" {
		t.Skip("set NORI_LIVE_CACHE_TEST=1 and OPENAI_API_KEY to run against OpenAI")
	}
	model := NewOpenAIModel(key, openAIModelName)
	model.Guard = llmguard.New(llmguard.Config{AllowLiveInTests: true})
	memories := []state.Memory{}
	for i := 0; i < 25; i++ {
		memories = append(memories, state.Memory{
			ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), Section: "Context", About: "the user",
			Text: fmt.Sprintf("The user's standing preference number %d about scheduling, family logistics and work projects.", i),
		})
	}
	turn := func(i int) state.ConversationTurn {
		user := fmt.Sprintf("Turn %d: ", i) + strings.Repeat("Please check my calendar and inbox for anything that moved this week. ", 10)
		reply := fmt.Sprintf("Reply %d: ", i) + strings.Repeat("Here is what changed, with dates and owners. ", 10)
		return state.ConversationTurn{Protocol: []string{
			string(modelMessage(map[string]string{"role": "user", "content": user})),
			string(modelMessage(map[string]string{"role": "assistant", "content": reply})),
		}}
	}
	history := []state.ConversationTurn{}
	for i := 0; i < 10; i++ {
		history = append(history, turn(i))
	}
	send := func(label string) (cached, written, input int64) {
		view := historyView{Memories: memories, Turns: history}
		msgs, bps := buildMessages(&state.ConversationTurn{}, nil, view, "")
		msgs = append(msgs,
			modelMessage(map[string]string{"role": "user", "content": "Quick question: anything new for Thursday?"}),
			modelMessage(map[string]string{"role": "system", "content": "Advisory context: now=" + time.Now().UTC().Format(time.RFC3339Nano)}),
		)
		memoryIDs := []string{}
		for _, m := range memories {
			memoryIDs = append(memoryIDs, m.ID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		reply, err := model.Respond(ctx, ReplyRequest{Messages: msgs, CacheBreakpoints: bps, MemoryIDs: memoryIDs, TaskIDs: []string{}})
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		u := reply.Usage
		if u.InputTokens == nil || u.CachedTokens == nil || u.CacheWriteTokens == nil {
			t.Fatalf("%s: usage missing cache fields: %+v", label, u)
		}
		cost := "?"
		if u.CostUSD != nil {
			cost = fmt.Sprintf("$%.5f", *u.CostUSD)
		}
		t.Logf("%-34s input=%d cached=%d written=%d cost=%s", label, *u.InputTokens, *u.CachedTokens, *u.CacheWriteTokens, cost)
		return *u.CachedTokens, *u.CacheWriteTokens, *u.InputTokens
	}

	send("turn 1 (cold)")
	history = append(history, turn(10))
	cached2, _, input2 := send("turn 2 (history grew)")
	if cached2 < input2/2 {
		t.Errorf("turn 2 should read most of its prefix from cache: cached=%d of %d", cached2, input2)
	}
	memories = append(memories, state.Memory{ID: "00000000-0000-4000-8000-999999999999", Section: "Context", About: "the user", Text: "A brand-new memory."})
	history = append(history, turn(11))
	cached3, _, _ := send("turn 3 (new memory + ID)")
	if cached3 < 1024 {
		t.Errorf("turn 3 should still read the static system prompt from cache after a memory change: cached=%d", cached3)
	}
	history = append(history, turn(12))
	cached4, _, input4 := send("turn 4 (history grew)")
	if cached4 < input4/2 {
		t.Errorf("turn 4 should read most of its prefix from cache: cached=%d of %d", cached4, input4)
	}
}
