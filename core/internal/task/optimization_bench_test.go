package task

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/transcript"
	"nori/internal/users"
)

type captureModel struct {
	lastReq agent.Request
}

func (m *captureModel) Name() string     { return "claude-haiku-5-5" }
func (m *captureModel) Provider() string { return "anthropic" }
func (m *captureModel) Respond(_ context.Context, req agent.Request) (agent.Response, error) {
	m.lastReq = req
	return agent.Response{Answer: "ok", CostKnown: true}, nil
}
func (m *captureModel) Inspect(_ context.Context, _ agent.InspectRequest) (agent.Inspection, error) {
	return agent.Inspection{CostKnown: true}, nil
}

// TestInnerOptimizationBaselineMetrics prints deterministic inner-loop payload & timing metrics
// so Before/After comparisons are directly measurable.
func TestInnerOptimizationBaselineMetrics(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 18, 0, 0, 0, time.UTC)

	backend := memstore.New()
	u := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "bench@example.com"}
	store := state.Compose(records.NewShared(backend, u, 5.0), memstore.NewArtifacts())

	// Seed 10 completed prior tasks with ~3 KiB each of conversation messages (~30 KiB total)
	for i := 0; i < 10; i++ {
		runID := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		cmd := state.Command{
			ID:        fmt.Sprintf("10000000-0000-4000-8000-%012d", i+1),
			RunID:     runID,
			Kind:      state.CommandStart,
			Text:      fmt.Sprintf("Prior completed task %d: analyze quarterly metrics and produce report", i+1),
			CreatedAt: now.Add(-time.Duration(10-i) * time.Hour),
		}
		if _, err := store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil); err != nil {
			t.Fatalf("AcceptCommandBatch: %v", err)
		}
		if err := store.Incorporate(ctx, state.IncorporateRequest{RunID: runID, Revision: 1, Position: 1}); err != nil {
			t.Fatalf("Incorporate: %v", err)
		}
		longResult := strings.Repeat(fmt.Sprintf("Detailed completed report #%d with findings, metrics, and recommendations. ", i+1), 40)
		if _, err := store.Publish(ctx, state.PublishRequest{
			RunID:         runID,
			Revision:      1,
			Status:        state.RunCompleted,
			Detail:        "Done",
			Text:          longResult,
			BypassRouting: true,
		}); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}

	capModel := &captureModel{}
	svc := &agent.Service{Model: capModel, Store: store, Now: func() time.Time { return now }}
	act := &Activities{Agent: svc, Store: store, Now: func() time.Time { return now }}
	bgMsgs, err := act.Context(ctx, "00000000-0000-4000-8000-999999999999")
	if err != nil {
		t.Fatalf("Activities.Context: %v", err)
	}
	bgBytes := 0
	for _, m := range bgMsgs {
		raw, _ := json.Marshal(m)
		bgBytes += len(raw)
	}

	// Capture real Round-1 agent.Request (instructions + tools + reasoning)
	_, err = svc.Decide(ctx, append(bgMsgs, transcript.UserText("Check my calendar for tomorrow")), true, true)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	toolsJSON, _ := json.Marshal(capModel.lastReq.Tools)

	// Measure memory_read output size with 25 memories including verbose SourceText
	mems := make([]state.Memory, 0, 25)
	for i := 0; i < 25; i++ {
		mems = append(mems, state.Memory{
			ID:            fmt.Sprintf("20000000-0000-4000-8000-%012d", i),
			Section:       "Preferences",
			About:         "the user",
			Text:          fmt.Sprintf("Standing preference #%d for inner worker recall.", i),
			SourceInputID: fmt.Sprintf("30000000-0000-4000-8000-%012d", i),
			SourceText:    strings.Repeat("Verbatim original user message excerpt that inflates memory_read payload. ", 4),
			Provenance:    state.ProvenanceUserExplicit,
			CreatedAt:     now.Add(-time.Duration(25-i) * time.Hour),
			UpdatedAt:     now.Add(-time.Duration(25-i) * time.Hour),
		})
	}
	if _, err := store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
		ExpectedSummary: state.ConversationSummary{},
		NextSummary: state.ConversationSummary{
			Through:   now.Add(-10 * time.Minute),
			Gist:      "Active project milestones",
			Turns:     12,
			Excerpts:  []state.ConversationExcerpt{{MessageID: "e1", Role: "user", Text: strings.Repeat("Long verbatim excerpt ", 20), TurnStartedAt: now.Add(-20 * time.Minute)}},
			UpdatedAt: now,
		},
		CoreUpserts: mems,
	}); err != nil {
		t.Fatalf("CommitFlushAndCompact: %v", err)
	}
	memOut, err := svc.CallRuntimeTool(ctx, "00000000-0000-4000-8000-999999999999", "memory_read", map[string]any{})
	if err != nil {
		t.Fatalf("CallRuntimeTool(memory_read): %v", err)
	}
	memOutJSON, _ := json.Marshal(memOut)

	t.Logf("METRIC inner_activities_context_bytes=%d", bgBytes)
	t.Logf("METRIC inner_r1_instructions_bytes=%d", len(capModel.lastReq.Instructions))
	t.Logf("METRIC inner_r1_tools_bytes=%d count=%d", len(toolsJSON), len(capModel.lastReq.Tools))
	t.Logf("METRIC inner_r1_reasoning=%q", capModel.lastReq.Reasoning)
	t.Logf("METRIC inner_memory_read_output_bytes=%d", len(memOutJSON))
}
