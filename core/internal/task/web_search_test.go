package task

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"nori/internal/agent"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/transcript"
	"nori/internal/users"
)

type nativeSearchStubModel struct {
	response agent.Response
}

func (m *nativeSearchStubModel) Provider() string              { return "openai" }
func (m *nativeSearchStubModel) Name() string                  { return "gpt-6.1-terra" }
func (m *nativeSearchStubModel) SupportsNativeWebSearch() bool { return true }
func (m *nativeSearchStubModel) Respond(_ context.Context, _ agent.Request) (agent.Response, error) {
	return m.response, nil
}
func (m *nativeSearchStubModel) Inspect(_ context.Context, _ agent.InspectRequest) (agent.Inspection, error) {
	return agent.Inspection{}, nil
}

func TestModelRefRecordsNativeWebSearchOperations(t *testing.T) {
	ctx := context.Background()
	fixedNow := time.Date(2026, 11, 25, 6, 49, 10, 0, time.UTC)
	backend := memstore.New()
	u := users.User{ID: "11111111-1111-4111-8111-111111111111", Email: "a@example.com", DisplayName: "Alice", TimeZone: "America/Los_Angeles"}
	store := state.Compose(records.NewShared(backend, u, 5.0), memstore.NewArtifacts())

	runID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := store.AcceptCommandBatch(ctx, []state.Command{{
		ID: runID, RunID: runID, Kind: state.CommandStart, Text: "What is the weather in SF Thursday?", CreatedAt: fixedNow,
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}
	if err := store.Incorporate(ctx, state.IncorporateRequest{RunID: runID, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("Incorporate: %v", err)
	}

	completeCall := transcript.ToolCall{
		ID:   "call_complete_1",
		Name: "complete",
		Args: map[string]any{"answer": "Sunny, 68F on Thursday.", "evidence": []any{}, "unmet": []any{}},
	}
	model := &nativeSearchStubModel{
		response: agent.Response{
			Message: &transcript.Message{
				Role:  transcript.RoleModel,
				Parts: []transcript.Part{{ToolCall: &completeCall}},
			},
			Calls: []transcript.ToolCall{completeCall},
			WebSearches: []agent.WebSearchCall{
				{
					ID:      "ws_1",
					Status:  "completed",
					Action:  "search",
					Query:   "San Francisco weather Thursday forecast",
					Sources: []string{"https://weather.example.com/sf"},
				},
				{
					ID:     "ws_2",
					Status: "completed",
					Action: "open_page",
					URL:    "https://weather.example.com/sf",
				},
				{
					ID:     "ws_3",
					Status: "failed",
					Action: "search",
					Query:  "unreachable weather feed",
				},
			},
			Citations: []agent.URLCitation{{URL: "https://weather.example.com/sf", Title: "SF Weather"}},
			Cost:      0.025,
			CostKnown: true,
		},
	}

	svc := agent.New(store, model)
	svc.Now = func() time.Time { return fixedNow }
	act := &Activities{
		Agent: svc,
		Store: store,
		Now:   func() time.Time { return fixedNow },
	}

	ref, err := act.appendMessages(ctx, runID, "", []transcript.Message{transcript.UserText("What is the weather in SF Thursday?")})
	if err != nil {
		t.Fatalf("appendMessages: %v", err)
	}

	outcome, err := act.ModelRef(ctx, RefModelInput{
		Step:    Step{RunID: runID, Revision: 1},
		ModelID: "m1",
		Ref:     ref,
	})
	if err != nil {
		t.Fatalf("ModelRef: %v", err)
	}
	if outcome.Failure != "" || len(outcome.Calls) != 1 || outcome.Calls[0].Name != "complete" {
		t.Fatalf("unexpected ModelRef outcome: %+v", outcome)
	}

	ops, err := store.Operations(ctx, runID)
	if err != nil {
		t.Fatalf("Operations: %v", err)
	}
	if len(ops) != 3 {
		t.Fatalf("len(ops) = %d, want 3: %+v", len(ops), ops)
	}

	want := []struct {
		callID  string
		summary string
		outcome string
	}{
		{"ws_1", "Searched: San Francisco weather Thursday forecast", "confirmed"},
		{"ws_2", "Read page: https://weather.example.com/sf", "confirmed"},
		{"ws_3", "Searched: unreachable weather feed", "failed"},
	}
	for i, tc := range want {
		op := ops[i]
		if op.Name != "web_search" {
			t.Fatalf("op[%d].Name = %q, want web_search", i, op.Name)
		}
		if op.CallID != tc.callID {
			t.Fatalf("op[%d].CallID = %q, want %q", i, op.CallID, tc.callID)
		}
		if op.EffectClass != "read" {
			t.Fatalf("op[%d].EffectClass = %q, want read", i, op.EffectClass)
		}
		if op.Outcome != tc.outcome {
			t.Fatalf("op[%d].Outcome = %q, want %q", i, op.Outcome, tc.outcome)
		}
		if op.AuthorityRef != "" || op.ConnectionID != "" {
			t.Fatalf("op[%d] should have empty AuthorityRef/ConnectionID, got auth=%q conn=%q", i, op.AuthorityRef, op.ConnectionID)
		}
		if op.Action == nil || op.Action.Text != tc.summary || op.Action.Digest == "" {
			t.Fatalf("op[%d].Action = %+v, want text=%q", i, op.Action, tc.summary)
		}
		if op.CreatedAt == nil || !op.CreatedAt.Equal(fixedNow) {
			t.Fatalf("op[%d].CreatedAt = %v, want %v", i, op.CreatedAt, fixedNow)
		}
		if op.ResultRef == "" {
			t.Fatalf("op[%d].ResultRef should be populated", i)
		}
	}

	readOut, err := act.operationRead(ctx, runID, ops[0].ID)
	if err != nil {
		t.Fatalf("operationRead: %v", err)
	}
	savedOutput, _ := readOut["output"].(map[string]any)
	if savedOutput["query"] != "San Francisco weather Thursday forecast" || savedOutput["native"] != true {
		t.Fatalf("unexpected saved native search output: %#v", savedOutput)
	}
	rawCitations, _ := json.Marshal(savedOutput["citations"])
	if string(rawCitations) == "null" || len(rawCitations) < 10 {
		t.Fatalf("expected citations in saved native search output, got %s", string(rawCitations))
	}

	steps, err := store.TaskSteps(ctx, runID)
	if err != nil {
		t.Fatalf("TaskSteps: %v", err)
	}
	if len(steps) != 1 || len(steps[0].Calls) != 4 {
		t.Fatalf("expected 1 model step with 4 trace calls (3 web_search + 1 complete), got %+v", steps)
	}
	if steps[0].Calls[0].Name != "web_search" {
		t.Fatalf("expected first trace call to be web_search, got %+v", steps[0].Calls[0])
	}
}

func TestDurableToolRecordsFallbackWebSearchSummaryAndCreatedAt(t *testing.T) {
	ctx := context.Background()
	fixedNow := time.Date(2026, 11, 25, 7, 0, 0, 0, time.UTC)
	backend := memstore.New()
	u := users.User{ID: "22222222-2222-4222-8222-222222222222", Email: "b@example.com", DisplayName: "Bob", TimeZone: "UTC"}
	store := state.Compose(records.NewShared(backend, u, 5.0), memstore.NewArtifacts())

	runID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	if _, err := store.AcceptCommandBatch(ctx, []state.Command{{
		ID: runID, RunID: runID, Kind: state.CommandStart, Text: "Search fallback test", CreatedAt: fixedNow,
	}}, nil); err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}
	if err := store.Incorporate(ctx, state.IncorporateRequest{RunID: runID, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("Incorporate: %v", err)
	}

	svc := agent.New(store, nil)
	svc.Now = func() time.Time { return fixedNow }
	svc.HTTP = &http.Client{}
	act := &Activities{
		Agent: svc,
		Store: store,
		Now:   func() time.Time { return fixedNow },
	}

	_, err := act.durableTool(ctx, ToolInput{
		Step:        Step{RunID: runID, Revision: 1},
		OperationID: runID + "-m1-t1",
		Call: transcript.ToolCall{
			ID:   "call_ws_1",
			Name: "web_search",
			Args: map[string]any{"query": "golang 1.25 release date"},
		},
	})
	if err != nil {
		t.Fatalf("durableTool: %v", err)
	}

	ops, err := store.Operations(ctx, runID)
	if err != nil {
		t.Fatalf("Operations: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("len(ops) = %d, want 1", len(ops))
	}
	op := ops[0]
	if op.Name != "web_search" || op.EffectClass != "read" || op.Outcome != "failed" {
		t.Fatalf("unexpected fallback web_search op: %+v", op)
	}
	if op.Action == nil || op.Action.Text != "Searched: golang 1.25 release date" {
		t.Fatalf("expected Action.Text with query summary, got %+v", op.Action)
	}
	if op.CreatedAt == nil || !op.CreatedAt.Equal(fixedNow) {
		t.Fatalf("expected CreatedAt=%v, got %v", fixedNow, op.CreatedAt)
	}
	if op.AuthorityRef != "" {
		t.Fatalf("expected empty AuthorityRef, got %q", op.AuthorityRef)
	}
}
