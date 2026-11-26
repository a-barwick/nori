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

func TestLiveAnthropicPromptCacheHits(t *testing.T) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		key = os.Getenv("ANTHROPIC_DEV_API_KEY")
	}
	if os.Getenv("NORI_LIVE_CACHE_TEST") != "1" || key == "" {
		t.Skip("set NORI_LIVE_CACHE_TEST=1 and ANTHROPIC_API_KEY to run against Anthropic")
	}
	modelName := os.Getenv("NORI_ANTHROPIC_BENCH_MODEL")
	if modelName == "" {
		if m := os.Getenv("NORI_AGENT_MODEL"); strings.HasPrefix(m, "claude-") {
			modelName = m
		} else {
			modelName = "claude-opus-5-5"
		}
	}
	model := NewAnthropicModel(key, modelName)
	model.ReasoningEffort = os.Getenv("NORI_OUTER_REASONING_EFFORT")
	if model.ReasoningEffort == "" {
		model.ReasoningEffort = "low"
	}
	model.Guard = llmguard.New(llmguard.Config{AllowLiveInTests: true, MaxEstimatedUSD: 0.75})

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
	var totalCost float64
	var totalIn, totalCached, totalWritten, totalOut int64
	send := func(label string, noDispatch bool) (cached, written, input int64) {
		view := historyView{Memories: memories, Turns: history}
		msgs, bps := buildMessages(&state.ConversationTurn{}, nil, view, "")
		msgs = append(msgs,
			modelMessage(map[string]string{"role": "user", "content": "Quick question: answer directly in one short sentence with no task actions — what day comes after Wednesday?"}),
			modelMessage(map[string]string{"role": "system", "content": "Advisory context: now=" + time.Now().UTC().Format(time.RFC3339Nano)}),
		)
		memoryIDs := []string{}
		for _, m := range memories {
			memoryIDs = append(memoryIDs, m.ID)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		start := time.Now()
		reply, err := model.Respond(ctx, ReplyRequest{
			Messages:         msgs,
			CacheBreakpoints: bps,
			MemoryIDs:        memoryIDs,
			TaskIDs:          []string{},
			NoDispatch:       noDispatch,
		})
		dur := time.Since(start)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		u := reply.Usage
		if u.InputTokens == nil || u.CachedTokens == nil || u.CacheWriteTokens == nil || u.OutputTokens == nil {
			t.Fatalf("%s: usage missing fields: %+v", label, u)
		}
		cost := 0.0
		if u.CostUSD != nil {
			cost = *u.CostUSD
		}
		totalCost += cost
		totalIn += *u.InputTokens
		totalCached += *u.CachedTokens
		totalWritten += *u.CacheWriteTokens
		totalOut += *u.OutputTokens
		hitPct := 0.0
		if *u.InputTokens > 0 {
			hitPct = 100.0 * float64(*u.CachedTokens) / float64(*u.InputTokens)
		}
		t.Logf("%-34s dur=%dms in=%d cached=%d (%.1f%%) write=%d out=%d cost=$%.5f",
			label, dur.Milliseconds(), *u.InputTokens, *u.CachedTokens, hitPct, *u.CacheWriteTokens, *u.OutputTokens, cost)
		return *u.CachedTokens, *u.CacheWriteTokens, *u.InputTokens
	}

	send("turn 1 (cold)", false)
	history = append(history, turn(10))
	cached2, _, input2 := send("turn 2 (history grew)", false)
	if cached2 < input2/2 {
		t.Errorf("turn 2 should read most of its prefix from cache: cached=%d of %d", cached2, input2)
	}
	memories = append(memories, state.Memory{ID: "00000000-0000-4000-8000-999999999999", Section: "Context", About: "the user", Text: "A brand-new memory."})
	history = append(history, turn(11))
	cached3, _, _ := send("turn 3 (new memory + ID)", false)
	if cached3 < 1024 {
		t.Errorf("turn 3 should still read prefix from cache after memory change: cached=%d", cached3)
	}
	history = append(history, turn(12))
	cached4, _, input4 := send("turn 4 (history grew)", false)
	if cached4 < input4/2 {
		t.Errorf("turn 4 should read most of its prefix from cache: cached=%d of %d", cached4, input4)
	}
	t.Logf("ANTHROPIC_4TURN_TOTAL model=%s in=%d cached=%d (%.1f%%) write=%d out=%d cost=$%.5f",
		modelName, totalIn, totalCached, 100.0*float64(totalCached)/float64(max(totalIn, 1)), totalWritten, totalOut, totalCost)
}
