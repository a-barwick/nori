package task_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"nori/internal/agent"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/llmguard"
	"nori/internal/permissions"
	"nori/internal/task"
)

type visionPreparingAdapter struct {
	execCalls atomic.Int32
}

func (a *visionPreparingAdapter) Prepare(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	if call.InspectImage != nil {
		_, _ = call.InspectImage(ctx, "approval_screenshot", "inspect", "what is shown?", []byte("fake-png"), "image/png")
	}
	spec := map[string]string{"tool": call.Exec.Tool.ID}
	return invoke.Prepared{
		Summary: "Submit payment form",
		Effect:  extensions.EffectReversibleWrite,
		Spec:    spec,
		Keys:    map[string]string{"action_key": invoke.ActionKey(call, spec)},
	}, nil
}

func (a *visionPreparingAdapter) Execute(context.Context, invoke.Call, invoke.Prepared) invoke.Result {
	a.execCalls.Add(1)
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"ok": true}}
}

type costedAutoReviewer struct {
	verdict permissions.AutoReviewDecision
	costUSD float64
}

func (r *costedAutoReviewer) ReviewAction(ctx context.Context, _ permissions.AutoReviewRequest) (permissions.AutoReviewDecision, error) {
	llmguard.ObserveCallCost(ctx, r.costUSD, true, true)
	return r.verdict, nil
}

type stubRoundTrip func(*http.Request) (*http.Response, error)

func (f stubRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestComposeCapsPaidExaToolCallsInRunner(t *testing.T) {
	ctx := context.Background()
	var exaCalls atomic.Int32

	queries := make([]string, 25)
	for i := range queries {
		queries[i] = fmt.Sprintf("query-%d", i)
	}
	scriptBytes, _ := json.Marshal(map[string]any{
		"version":     1,
		"concurrency": 1,
		"steps": []map[string]any{
			{
				"id":       "searches",
				"tool":     "web_search",
				"for_each": queries,
				"as":       "q",
				"args":     map[string]any{"query": "$q"},
			},
		},
	})

	f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		switch callNum {
		case 1:
			return toolCallResponse("c-compose", "compose", map[string]any{"script": string(scriptBytes)}, 0.01), nil
		case 2:
			// A subsequent web_search call should also be skipped because PaidTools reached defaultMaxPaidTools (20).
			return toolCallResponse("c-extra-search", "web_search", map[string]any{"query": "one-more"}, 0.01), nil
		default:
			return toolCallResponse("c-done", "complete", map[string]any{
				"answer": "Done.", "evidence": []any{}, "unmet": []any{},
			}, 0.01), nil
		}
	})
	f.act.Agent.Capabilities.ExaSearchKey = "exa-test-key"
	f.act.Agent.HTTP = &http.Client{Transport: stubRoundTrip(func(r *http.Request) (*http.Response, error) {
		exaCalls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"results":[{"title":"R","url":"https://example.com"}],"costDollars":{"total":0.01}}`)),
		}, nil
	})}
	agent.ConfigureSearch(f.act.Agent, agent.SearchOptions{
		Guard: llmguard.New(llmguard.Config{
			DuplicateMax: 100,
			Scopes: map[string]llmguard.ScopePolicy{
				llmguard.ScopeSearch: {MaxPerMinute: 100, MaxPerHour: 1000, MaxInFlight: 16},
			},
		}),
	})

	run := f.startTask(ctx, "run-compose-cap", "Run 25 web searches")
	outcome, err := f.claimAndRun(ctx, run.ID, "worker-1")
	if err != nil || outcome != task.OutcomeDone {
		t.Fatalf("claimAndRun = (%v, %v), want OutcomeDone", outcome, err)
	}
	if exaCalls.Load() != 20 {
		t.Fatalf("expected compose + task paid tool cap to limit Exa calls to 20, got %d", exaCalls.Load())
	}
	after, err := f.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	// 3 model calls ($0.03) + 20 Exa calls ($0.20) = $0.23
	if after.Spent < 0.229 || after.Spent > 0.231 {
		t.Fatalf("expected run.Spent = 0.23, got %v", after.Spent)
	}
}

type inspectCostModel struct {
	agent.Model
	inspectCost float64
}

func (m inspectCostModel) Inspect(context.Context, agent.InspectRequest) (agent.Inspection, error) {
	return agent.Inspection{Sent: true, Cost: m.inspectCost, CostKnown: true, Observation: "Payment confirmation screen"}, nil
}

func TestAutoReviewerAndApprovalVisionChargeRunSpent(t *testing.T) {
	ctx := context.Background()
	writeDef := sealExternalDef(t, "mock/tools/form_submit", "form_submit", extensions.EffectReversibleWrite)

	f := newRunnerFixture(t, func(_ context.Context, callNum int, _ agent.Request) (agent.Response, error) {
		switch callNum {
		case 1:
			return toolCallResponse("c1", "form_submit", map[string]any{}, 0.05), nil
		default:
			return toolCallResponse("c2", "complete", map[string]any{
				"answer": "Submitted.", "evidence": []any{}, "unmet": []any{},
			}, 0.05), nil
		}
	})
	f.act.Agent.Model = inspectCostModel{Model: f.act.Agent.Model, inspectCost: 0.03}
	adapter := &visionPreparingAdapter{}
	f.extSource.defs = []extensions.Definition{writeDef}
	f.adapters.Register(extensions.AdapterNative, "mock", adapter)
	f.act.Executor.AutoReviewer = &costedAutoReviewer{
		verdict: permissions.AutoReviewDecision{Verdict: "ask", Reason: "confirm submit"},
		costUSD: 0.02,
	}

	run := f.startTask(ctx, "run-extra-spend", "Submit the form")
	outcome, err := f.claimAndRun(ctx, run.ID, "worker-1")
	if err != nil || outcome != task.OutcomeParked {
		t.Fatalf("expected OutcomeParked awaiting approval, got (%v, %v)", outcome, err)
	}
	afterAsk, err := f.store.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	// Model call ($0.05) + Prepare approval vision ($0.03) + AutoReviewer ($0.02) = $0.10
	if afterAsk.Spent < 0.099 || afterAsk.Spent > 0.101 {
		t.Fatalf("expected run.Spent = 0.10 after Prepare vision + AutoReviewer ask, got %v", afterAsk.Spent)
	}
}
