package inner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"nori/internal/agent"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/permissions"
	"nori/internal/sim/inner"
	"nori/internal/state"
	"nori/internal/task"
	"nori/internal/transcript"
	"nori/internal/users"
)

type composeHookAdapter struct {
	base          invoke.Adapter
	beforeExecute func(ctx context.Context, call invoke.Call, prepared invoke.Prepared)
	afterExecute  func(ctx context.Context, call invoke.Call, prepared invoke.Prepared, res invoke.Result)
}

func (a *composeHookAdapter) Prepare(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	return a.base.Prepare(ctx, call)
}

func (a *composeHookAdapter) Execute(ctx context.Context, call invoke.Call, prepared invoke.Prepared) invoke.Result {
	if a.beforeExecute != nil {
		a.beforeExecute(ctx, call, prepared)
	}
	res := a.base.Execute(ctx, call, prepared)
	if a.afterExecute != nil {
		a.afterExecute(ctx, call, prepared, res)
	}
	return res
}

func (a *composeHookAdapter) Reconcile(ctx context.Context, call invoke.Call, prepared invoke.Prepared) (invoke.Result, bool) {
	if r, ok := a.base.(invoke.Reconciler); ok {
		return r.Reconcile(ctx, call, prepared)
	}
	return invoke.Result{}, false
}

type composeCustomAdapter struct {
	mu      sync.Mutex
	calls   []string
	costUSD float64
	bigBody string
}

func (a *composeCustomAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
	switch call.Exec.Binding.Target {
	case "compose/paid_read", "compose/free_read":
		key := invoke.String(call.Args, "key")
		if key == "" {
			return invoke.Prepared{}, fmt.Errorf("%w: key is required", invoke.ErrInvalidArguments)
		}
		spec := map[string]string{"key": key}
		return invoke.Prepared{
			Summary: fmt.Sprintf("Read %s", key),
			Effect:  extensions.EffectRead,
			Spec:    spec,
			Keys:    map[string]string{"action_key": invoke.ActionKey(call, spec)},
		}, nil
	case "compose/big_utf8":
		spec := map[string]string{"mode": "big"}
		return invoke.Prepared{
			Summary: "Read large UTF-8 payload",
			Effect:  extensions.EffectRead,
			Spec:    spec,
			Keys:    map[string]string{"action_key": invoke.ActionKey(call, spec)},
		}, nil
	default:
		return invoke.Prepared{}, fmt.Errorf("%w: unknown target %q", invoke.ErrInvalidArguments, call.Exec.Binding.Target)
	}
}

func (a *composeCustomAdapter) Execute(_ context.Context, call invoke.Call, prepared invoke.Prepared) invoke.Result {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch call.Exec.Binding.Target {
	case "compose/paid_read":
		key := prepared.Spec["key"]
		a.calls = append(a.calls, key)
		return invoke.Result{
			Outcome: "confirmed",
			CostUSD: a.costUSD,
			Content: map[string]any{"key": key, "value": "paid-" + key},
		}
	case "compose/free_read":
		key := prepared.Spec["key"]
		a.calls = append(a.calls, key)
		return invoke.Result{
			Outcome: "confirmed",
			Content: map[string]any{"key": key, "value": "free-" + key},
		}
	case "compose/big_utf8":
		a.calls = append(a.calls, "big_utf8")
		return invoke.Result{
			Outcome: "confirmed",
			Content: map[string]any{"payload": a.bigBody},
		}
	default:
		return invoke.Result{Outcome: "failed", Err: errors.New("unknown custom target")}
	}
}

func (a *composeCustomAdapter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func composeMustSeal(t *testing.T, def extensions.Definition) extensions.Definition {
	t.Helper()
	sealed, err := extensions.Seal(def)
	if err != nil {
		t.Fatalf("Seal %s: %v", def.Ref.ID, err)
	}
	return sealed
}

func composeRegisterCustomTools(t *testing.T, h *testHarness, adapter *composeCustomAdapter, costUSD float64) {
	t.Helper()
	paidDef := composeMustSeal(t, extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: "compose/tools/paid_read", Version: "1"},
		Extension:       extensions.DefinitionRef{ID: inner.ExtensionID, Version: "1"},
		ModelName:       "sim_paid_read",
		Title:           "Paid Read",
		Description:     "Paid read tool for budget tests.",
		Capability:      "sim",
		CapabilityTitle: "Simulated tools",
		InputSchema:     json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","minLength":1}},"required":["key"],"additionalProperties":false}`),
		Binding:         extensions.Binding{Adapter: extensions.AdapterNative, Target: "compose/paid_read", Version: "1"},
		Policy:          extensions.Policy{Effect: extensions.EffectRead, Retry: "safe", TimeoutMS: 30_000, Location: extensions.LocationRuntime, InterruptedCostUSD: costUSD},
		Exposure:        extensions.ExposureDirect,
	})
	freeDef := composeMustSeal(t, extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: "compose/tools/free_read", Version: "1"},
		Extension:       extensions.DefinitionRef{ID: inner.ExtensionID, Version: "1"},
		ModelName:       "sim_free_read",
		Title:           "Free Read",
		Description:     "Zero-cost read tool for budget tests.",
		Capability:      "sim",
		CapabilityTitle: "Simulated tools",
		InputSchema:     json.RawMessage(`{"type":"object","properties":{"key":{"type":"string","minLength":1}},"required":["key"],"additionalProperties":false}`),
		Binding:         extensions.Binding{Adapter: extensions.AdapterNative, Target: "compose/free_read", Version: "1"},
		Policy:          extensions.Policy{Effect: extensions.EffectRead, Retry: "safe", TimeoutMS: 30_000, Location: extensions.LocationRuntime},
		Exposure:        extensions.ExposureDirect,
	})
	bigDef := composeMustSeal(t, extensions.Definition{
		Ref:             extensions.DefinitionRef{ID: "compose/tools/big_utf8", Version: "1"},
		Extension:       extensions.DefinitionRef{ID: inner.ExtensionID, Version: "1"},
		ModelName:       "sim_big_utf8",
		Title:           "Big UTF-8 Read",
		Description:     "Returns a large multi-byte UTF-8 payload.",
		Capability:      "sim",
		CapabilityTitle: "Simulated tools",
		InputSchema:     json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`),
		Binding:         extensions.Binding{Adapter: extensions.AdapterNative, Target: "compose/big_utf8", Version: "1"},
		Policy:          extensions.Policy{Effect: extensions.EffectRead, Retry: "safe", TimeoutMS: 30_000, Location: extensions.LocationRuntime},
		Exposure:        extensions.ExposureDirect,
	})
	h.userRT.Extensions.Definitions = append(h.userRT.Extensions.Definitions, paidDef, freeDef, bigDef)
	h.userRT.Adapters.Register(extensions.AdapterNative, "compose", adapter)
}

func composeAllowWrites(t *testing.T, ctx context.Context, h *testHarness) {
	t.Helper()
	if _, err := h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{inner.ToolIDWrite},
		ConnectionIDs: []string{},
		ResourceIDs:   []string{"*"},
		ActionClasses: []string{"unknown"},
		Title:         "Allow sim_write",
	}); err != nil {
		t.Fatalf("SaveRule write: %v", err)
	}
	if _, err := h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{inner.ToolIDSend},
		ConnectionIDs: []string{},
		ResourceIDs:   []string{},
		Recipients:    []string{"*"},
		ActionClasses: []string{"sensitive_send"},
		Title:         "Allow sim_send",
	}); err != nil {
		t.Fatalf("SaveRule send: %v", err)
	}
}

func composeExecPtr(t *testing.T, ctx context.Context, h *testHarness) *extensions.Exec {
	t.Helper()
	snapshot, _ := h.userRT.Agent.Tools(ctx)
	def, ok := snapshot.Lookup("compose")
	if !ok {
		t.Fatalf("compose definition not found in tool snapshot")
	}
	exec := def.Exec()
	return &exec
}

func composeAppendCall(t *testing.T, ctx context.Context, h *testHarness, runID, parentRef, callID string, script any) string {
	t.Helper()
	rawScript, err := json.Marshal(script)
	if err != nil {
		t.Fatalf("marshal compose script: %v", err)
	}
	call := transcript.ToolCall{
		ID:   callID,
		Name: "compose",
		Args: map[string]any{"script": string(rawScript)},
	}
	msg := transcript.Message{
		Role:  transcript.RoleModel,
		Parts: []transcript.Part{{ToolCall: &call}},
	}
	data, err := json.Marshal([]transcript.Message{msg})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}
	ref, err := h.store.AppendHistory(ctx, runID, parentRef, data)
	if err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	return ref
}

func composeReadLastToolResult(t *testing.T, ctx context.Context, h *testHarness, runID, ref string) map[string]any {
	t.Helper()
	blocks, err := h.store.ReadHistory(ctx, runID, ref)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		var msgs []transcript.Message
		if err := json.Unmarshal(blocks[i], &msgs); err != nil {
			t.Fatalf("unmarshal history block: %v", err)
		}
		for j := len(msgs) - 1; j >= 0; j-- {
			for k := len(msgs[j].Parts) - 1; k >= 0; k-- {
				if msgs[j].Parts[k].ToolResult != nil {
					return msgs[j].Parts[k].ToolResult.Output
				}
			}
		}
	}
	t.Fatalf("no tool result found in history ref %s", ref)
	return nil
}

func composeSyncTask(t *testing.T, ctx context.Context, h *testHarness, run state.Run) (string, int64) {
	t.Helper()
	synced, err := h.userRT.Activities.SyncHistory(ctx, task.HistoryInput{
		Step: task.Step{RunID: run.ID, Revision: run.Revision},
	})
	if err != nil {
		t.Fatalf("SyncHistory(%s): %v", run.ID, err)
	}
	return synced.Ref, synced.Revision
}

func composeBoundaryKind(err error) string {
	switch {
	case errors.Is(err, state.ErrCancelled):
		return "cancelled"
	case errors.Is(err, state.ErrPaused):
		return "paused"
	case errors.Is(err, state.ErrSuperseded):
		return "superseded"
	default:
		return ""
	}
}

func TestComposeParallelReadsBindDownstream(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 201, harnessOpts{disableFaults: true})
	ctx = users.WithID(ctx, h.user.ID)
	composeAllowWrites(t, ctx, h)

	run := h.startTask(ctx, "Seed and compose parallel reads")
	baseRef, rev := composeSyncTask(t, ctx, h, run)
	composeExec := composeExecPtr(t, ctx, h)

	seedScript := map[string]any{
		"version":     1,
		"concurrency": 4,
		"steps": []map[string]any{
			{
				"id":       "seed",
				"tool":     inner.ToolNameWrite,
				"for_each": []any{map[string]any{"k": "k1", "v": "v1"}, map[string]any{"k": "k2", "v": "skip"}, map[string]any{"k": "k3", "v": "v3"}, map[string]any{"k": "k4", "v": "v4"}},
				"as":       "pair",
				"args":     map[string]any{"target": "$pair.k", "value": "$pair.v", "resource_id": "$pair.k"},
			},
		},
	}
	ref := composeAppendCall(t, ctx, h, run.ID, baseRef, "call-seed", seedScript)
	seedOut, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
		Step:        task.Step{RunID: run.ID, Revision: rev},
		Ref:         ref,
		OperationID: run.ID + "-op-seed",
		Index:       0,
		Exec:        composeExec,
	})
	if err != nil {
		t.Fatalf("seed ToolRef: %v", err)
	}
	if seedOut.Outcome != "confirmed" {
		t.Fatalf("seed outcome = %q, want confirmed", seedOut.Outcome)
	}

	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	ready := make(chan struct{})
	var closeOnce sync.Once
	h.userRT.Adapters.Register(extensions.AdapterNative, "sim", &composeHookAdapter{
		base: h.driver.Adapter,
		beforeExecute: func(_ context.Context, _ invoke.Call, prepared invoke.Prepared) {
			if prepared.Effect != extensions.EffectRead {
				return
			}
			cur := inFlight.Add(1)
			for {
				prev := maxInFlight.Load()
				if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
					break
				}
			}
			if cur >= 2 {
				closeOnce.Do(func() { close(ready) })
			} else {
				select {
				case <-ready:
				case <-time.After(200 * time.Millisecond):
				}
			}
		},
		afterExecute: func(_ context.Context, _ invoke.Call, prepared invoke.Prepared, _ invoke.Result) {
			if prepared.Effect == extensions.EffectRead {
				inFlight.Add(-1)
			}
		},
	})

	script := map[string]any{
		"version":     1,
		"concurrency": 4,
		"steps": []map[string]any{
			{
				"id":       "reads",
				"tool":     inner.ToolNameRead,
				"for_each": []any{"k1", "k2", "k3", "k4"},
				"as":       "k",
				"args":     map[string]any{"key": "$k"},
			},
			{
				"id":       "copied",
				"tool":     inner.ToolNameWrite,
				"for_each": "$reads",
				"as":       "item",
				"where":    map[string]any{"path": "$item.value", "not_equals": "skip"},
				"args": map[string]any{
					"target":      "out_{{ $item.key }}",
					"value":       "bound:{{ $item.value }}",
					"resource_id": "out_{{ $item.key }}",
				},
			},
		},
		"output": map[string]any{
			"values":        "$reads[*].value",
			"copied_values": "$copied[*].value",
			"copied_count":  "$copied|count",
		},
	}
	ref2 := composeAppendCall(t, ctx, h, run.ID, seedOut.Ref, "call-par", script)
	out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
		Step:        task.Step{RunID: run.ID, Revision: run.Revision},
		Ref:         ref2,
		OperationID: run.ID + "-op-par",
		Index:       0,
		Exec:        composeExec,
	})
	if err != nil {
		t.Fatalf("parallel compose ToolRef: %v", err)
	}
	if out.Outcome != "confirmed" {
		t.Fatalf("outcome = %q, want confirmed", out.Outcome)
	}
	if maxInFlight.Load() < 2 {
		t.Fatalf("expected parallel for_each reads with concurrency 4, maxInFlight=%d", maxInFlight.Load())
	}

	result := composeReadLastToolResult(t, ctx, h, run.ID, out.Ref)
	payload, _ := result["output"].(map[string]any)
	rawVals, _ := json.Marshal(payload["values"])
	if string(rawVals) != `["v1","skip","v3","v4"]` {
		t.Fatalf("read values = %s, want script-order [\"v1\",\"skip\",\"v3\",\"v4\"]", rawVals)
	}
	rawCopied, _ := json.Marshal(payload["copied_values"])
	if string(rawCopied) != `["bound:v1","bound:v3","bound:v4"]` {
		t.Fatalf("copied values = %s, want [\"bound:v1\",\"bound:v3\",\"bound:v4\"]", rawCopied)
	}
	if count, _ := payload["copied_count"].(float64); int(count) != 3 {
		t.Fatalf("copied_count = %v, want 3", payload["copied_count"])
	}

	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("CheckInnerInvariants: %v", err)
	}
}

func TestComposeForEachWritesSerializeNoSelfBlock(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 202, harnessOpts{disableFaults: true})
	ctx = users.WithID(ctx, h.user.ID)
	composeAllowWrites(t, ctx, h)

	var inFlightWrites atomic.Int32
	var maxInFlightWrites atomic.Int32
	h.userRT.Adapters.Register(extensions.AdapterNative, "sim", &composeHookAdapter{
		base: h.driver.Adapter,
		beforeExecute: func(_ context.Context, _ invoke.Call, prepared invoke.Prepared) {
			if prepared.Effect == extensions.EffectRead {
				return
			}
			cur := inFlightWrites.Add(1)
			for {
				prev := maxInFlightWrites.Load()
				if cur <= prev || maxInFlightWrites.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
		},
		afterExecute: func(_ context.Context, _ invoke.Call, prepared invoke.Prepared, _ invoke.Result) {
			if prepared.Effect != extensions.EffectRead {
				inFlightWrites.Add(-1)
			}
		},
	})

	run := h.startTask(ctx, "Batch writes in compose")
	baseRef, rev := composeSyncTask(t, ctx, h, run)
	composeExec := composeExecPtr(t, ctx, h)

	items := []any{"t0", "t1", "t2", "t3", "t4", "t5"}
	script := map[string]any{
		"version":     1,
		"concurrency": 6,
		"steps": []map[string]any{
			{
				"id":       "writes",
				"tool":     inner.ToolNameWrite,
				"for_each": items,
				"as":       "k",
				"args":     map[string]any{"target": "$k", "value": "val_{{ $k }}", "resource_id": "$k"},
			},
			{
				"id":       "verify",
				"tool":     inner.ToolNameRead,
				"for_each": items,
				"as":       "k",
				"args":     map[string]any{"key": "$k"},
			},
		},
		"output": map[string]any{
			"write_outcomes": "$writes[*].outcome",
			"read_values":    "$verify[*].value",
		},
	}

	ref := composeAppendCall(t, ctx, h, run.ID, baseRef, "call-writes", script)
	opID := run.ID + "-op-writes"
	out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
		Step:        task.Step{RunID: run.ID, Revision: rev},
		Ref:         ref,
		OperationID: opID,
		Index:       0,
		Exec:        composeExec,
	})
	if err != nil {
		t.Fatalf("ToolRef: %v", err)
	}
	if out.Outcome != "confirmed" {
		t.Fatalf("compose outcome = %q, want confirmed", out.Outcome)
	}
	if maxInFlightWrites.Load() != 1 {
		t.Fatalf("maxInFlightWrites = %d, want 1 (non-read calls must serialize)", maxInFlightWrites.Load())
	}

	result := composeReadLastToolResult(t, ctx, h, run.ID, out.Ref)
	payload, _ := result["output"].(map[string]any)
	rawOutcomes, _ := json.Marshal(payload["write_outcomes"])
	wantOutcomes := `["confirmed","confirmed","confirmed","confirmed","confirmed","confirmed"]`
	if string(rawOutcomes) != wantOutcomes {
		t.Fatalf("write_outcomes = %s, want %s", rawOutcomes, wantOutcomes)
	}
	rawReadVals, _ := json.Marshal(payload["read_values"])
	wantReadVals := `["val_t0","val_t1","val_t2","val_t3","val_t4","val_t5"]`
	if string(rawReadVals) != wantReadVals {
		t.Fatalf("read_values = %s, want %s", rawReadVals, wantReadVals)
	}

	writeOps := map[string]int{}
	var writeTargets []string
	for _, execCall := range h.driver.Adapter.Executed() {
		if execCall.ToolName == inner.ToolNameWrite {
			writeOps[execCall.OperationID]++
			writeTargets = append(writeTargets, fmt.Sprint(execCall.Args["target"]))
		}
	}
	if len(writeOps) != 6 {
		t.Fatalf("distinct write ops = %d, want 6", len(writeOps))
	}
	for id, count := range writeOps {
		if count != 1 {
			t.Fatalf("write op %s executed %d times, want 1", id, count)
		}
	}
	if strings.Join(writeTargets, ",") != "t0,t1,t2,t3,t4,t5" {
		t.Fatalf("write execution order = %v, want [t0 t1 t2 t3 t4 t5]", writeTargets)
	}

	ops, err := h.store.Operations(ctx, run.ID)
	if err != nil {
		t.Fatalf("Operations: %v", err)
	}
	for _, op := range ops {
		if op.Outcome == "uncertain" {
			t.Fatalf("unexpected uncertain operation %s (%s)", op.ID, op.Name)
		}
	}
	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("CheckInnerInvariants: %v", err)
	}
}

func TestComposeMidScriptLifecyclePropagation(t *testing.T) {
	modes := []struct {
		name         string
		wantKind     string
		parallelRead bool
		trigger      func(ctx context.Context, h *testHarness, runID string) error
	}{
		{
			name:     "cancel_during_writes",
			wantKind: "cancelled",
			trigger: func(ctx context.Context, h *testHarness, runID string) error {
				return h.stopTask(ctx, runID)
			},
		},
		{
			name:     "pause_during_writes",
			wantKind: "paused",
			trigger: func(ctx context.Context, h *testHarness, runID string) error {
				return h.setPaused(ctx, true)
			},
		},
		{
			name:     "supersede_during_writes",
			wantKind: "superseded",
			trigger: func(ctx context.Context, h *testHarness, runID string) error {
				return h.steerTask(ctx, runID, "change direction")
			},
		},
		{
			name:         "cancel_during_parallel_reads",
			wantKind:     "cancelled",
			parallelRead: true,
			trigger: func(ctx context.Context, h *testHarness, runID string) error {
				return h.stopTask(ctx, runID)
			},
		},
		{
			name:         "pause_during_parallel_reads",
			wantKind:     "paused",
			parallelRead: true,
			trigger: func(ctx context.Context, h *testHarness, runID string) error {
				return h.setPaused(ctx, true)
			},
		},
		{
			name:         "supersede_during_parallel_reads",
			wantKind:     "superseded",
			parallelRead: true,
			trigger: func(ctx context.Context, h *testHarness, runID string) error {
				return h.steerTask(ctx, runID, "steer parallel read")
			},
		},
	}

	for idx, tc := range modes {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := newTestHarness(t, int64(210+idx), harnessOpts{disableFaults: true})
			ctx = users.WithID(ctx, h.user.ID)
			composeAllowWrites(t, ctx, h)

			run := h.startTask(ctx, "Lifecycle test "+tc.name)
			baseRef, rev := composeSyncTask(t, ctx, h, run)
			composeExec := composeExecPtr(t, ctx, h)

			var fired atomic.Bool
			h.userRT.Adapters.Register(extensions.AdapterNative, "sim", &composeHookAdapter{
				base: h.driver.Adapter,
				afterExecute: func(_ context.Context, _ invoke.Call, _ invoke.Prepared, _ invoke.Result) {
					if fired.CompareAndSwap(false, true) {
						if err := tc.trigger(context.Background(), h, run.ID); err != nil {
							t.Errorf("trigger %s: %v", tc.name, err)
						}
					}
				},
			})

			var script map[string]any
			if tc.parallelRead {
				script = map[string]any{
					"version":     1,
					"concurrency": 4,
					"steps": []map[string]any{
						{
							"id":   "r0",
							"tool": inner.ToolNameRead,
							"args": map[string]any{"key": "first"},
						},
						{
							"id":       "r1",
							"tool":     inner.ToolNameRead,
							"for_each": []any{"a", "b", "c", "d"},
							"as":       "k",
							"args":     map[string]any{"key": "$k"},
						},
					},
				}
			} else {
				script = map[string]any{
					"version":     1,
					"concurrency": 3,
					"steps": []map[string]any{
						{
							"id":       "w",
							"tool":     inner.ToolNameWrite,
							"for_each": []any{"w0", "w1", "w2"},
							"as":       "k",
							"args":     map[string]any{"target": "$k", "value": "v", "resource_id": "$k"},
						},
					},
				}
			}

			ref := composeAppendCall(t, ctx, h, run.ID, baseRef, "call-lc", script)
			opID := run.ID + "-op-lc"
			_, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
				Step:        task.Step{RunID: run.ID, Revision: rev},
				Ref:         ref,
				OperationID: opID,
				Index:       0,
				Exec:        composeExec,
			})
			if err == nil {
				t.Fatalf("expected %s boundary error, got nil", tc.wantKind)
			}
			if got := composeBoundaryKind(err); got != tc.wantKind {
				t.Fatalf("error kind = %q (err=%v), want %q", got, err, tc.wantKind)
			}

			op, err := h.store.Operation(ctx, run.ID, opID)
			if err != nil {
				t.Fatalf("Operation(%s): %v", opID, err)
			}
			if op.Outcome == "uncertain" || op.ResultRef != "" {
				t.Fatalf("compose op was poisoned: outcome=%q result_ref=%q", op.Outcome, op.ResultRef)
			}

			if tc.wantKind == "paused" && !tc.parallelRead {
				if err := h.setPaused(ctx, false); err != nil {
					t.Fatalf("unpause: %v", err)
				}
				resumed, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
					Step:        task.Step{RunID: run.ID, Revision: run.Revision},
					Ref:         ref,
					OperationID: opID,
					Index:       0,
					Exec:        composeExec,
				})
				if err != nil {
					t.Fatalf("resumed ToolRef: %v", err)
				}
				if resumed.Outcome != "confirmed" {
					t.Fatalf("resumed outcome = %q, want confirmed", resumed.Outcome)
				}
				counts := map[string]int{}
				for _, c := range h.driver.Adapter.Executed() {
					if c.ToolName == inner.ToolNameWrite {
						counts[c.OperationID]++
					}
				}
				if len(counts) != 3 {
					t.Fatalf("resumed distinct write ops = %d, want 3", len(counts))
				}
				for id, n := range counts {
					if n != 1 {
						t.Fatalf("after pause resume, write op %s executed %d times, want 1", id, n)
					}
				}
			}

			if err := h.driver.CheckInnerInvariants(ctx); err != nil {
				t.Fatalf("CheckInnerInvariants: %v", err)
			}
		})
	}
}

func TestComposeReadOnlySubtaskNeedsInput(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 203, harnessOpts{disableFaults: true})
	ctx = users.WithID(ctx, h.user.ID)
	composeAllowWrites(t, ctx, h)

	h.driver.Adapter.QueueForcedOutcome(inner.ForcedOutcome{Outcome: "uncertain", ReconcileMiss: true})

	parent := h.startTask(ctx, "Parent with unresolved write delegating read-only subtask")
	parentTurn := 0
	childTurn := 0
	var childID string

	readOnlyScript := map[string]any{
		"version":     1,
		"concurrency": 2,
		"steps": []map[string]any{
			{
				"id":   "r1",
				"tool": inner.ToolNameRead,
				"args": map[string]any{"key": "status"},
			},
			{
				"id":   "w1",
				"tool": inner.ToolNameWrite,
				"args": map[string]any{"target": "status", "value": "mutated", "resource_id": "status"},
			},
		},
	}
	rawReadOnlyScript, err := json.Marshal(readOnlyScript)
	if err != nil {
		t.Fatalf("marshal script: %v", err)
	}

	h.driver.Model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
		if runID == parent.ID {
			parentTurn++
			switch parentTurn {
			case 1:
				return scriptedCall("p1", inner.ToolNameWrite, map[string]any{"target": "seed", "value": "u1", "resource_id": "seed"}, 0.05), nil, true
			case 2:
				return scriptedCall("p2", "request_work", map[string]any{"request": "inspect via compose", "why": "check state"}, 0.05), nil, true
			}
		}
		if runID == childID {
			childTurn++
			if childTurn == 1 {
				return scriptedCall("ch-compose", "compose", map[string]any{"script": string(rawReadOnlyScript)}, 0.05), nil, true
			}
		}
		return agent.Response{}, nil, false
	})

	h.stepUntilParkedOrDone(ctx, parent.ID, 20)
	if err := h.steerTask(ctx, parent.ID, "delegate read-only inspection"); err != nil {
		t.Fatalf("steerTask parent: %v", err)
	}
	h.stepUntilParkedOrDone(ctx, parent.ID, 20)

	children, err := h.driver.SpawnSubtask(ctx, parent.ID, "Read-only compose subtask direct", "Read-only compose subtask driver")
	if err != nil {
		t.Fatalf("SpawnSubtask: %v", err)
	}
	childDirect, childDriver := children[0], children[1]
	childID = childDriver.ID
	if !childDirect.ReadOnly || !childDriver.ReadOnly {
		t.Fatalf("expected spawned children to be ReadOnly=true")
	}

	directBaseRef, directRev := composeSyncTask(t, ctx, h, childDirect)
	composeExec := composeExecPtr(t, ctx, h)
	directRef := composeAppendCall(t, ctx, h, childDirect.ID, directBaseRef, "direct-ro-compose", readOnlyScript)
	directOpID := childDirect.ID + "-direct-ro"
	directOut, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
		Step:        task.Step{RunID: childDirect.ID, Revision: directRev},
		Ref:         directRef,
		OperationID: directOpID,
		Index:       0,
		ReadOnly:    true,
		Exec:        composeExec,
	})
	if err != nil {
		t.Fatalf("direct ToolRef on read-only subtask: %v", err)
	}
	if directOut.Outcome != "needs_input" {
		t.Fatalf("direct ToolRef outcome = %q, want needs_input", directOut.Outcome)
	}
	directOp, err := h.store.Operation(ctx, childDirect.ID, directOpID)
	if err != nil {
		t.Fatalf("Operation(%s): %v", directOpID, err)
	}
	if directOp.Outcome == "confirmed" || directOp.ResultRef != "" {
		t.Fatalf("stored compose op should not settle as confirmed: outcome=%q result_ref=%q", directOp.Outcome, directOp.ResultRef)
	}

	h.stepUntilParkedOrDone(ctx, childDriver.ID, 25)
	gotChild, err := h.store.GetRun(ctx, childDriver.ID)
	if err != nil {
		t.Fatalf("GetRun child: %v", err)
	}
	if gotChild.Question == nil {
		t.Fatalf("expected read-only subtask to pause with Question != nil after compose write step")
	}
	if gotChild.Question.Kind != "input" {
		t.Fatalf("child question kind = %q, want input", gotChild.Question.Kind)
	}

	for _, execCall := range h.driver.Adapter.Executed() {
		if (execCall.RunID == childDirect.ID || execCall.RunID == childDriver.ID) && execCall.Effect != string(extensions.EffectRead) {
			t.Fatalf("read-only subtask executed non-read tool: %+v", execCall)
		}
	}
	if err := h.checker.CheckSafety(ctx, h.userTargets()); err != nil {
		t.Fatalf("CheckSafety: %v", err)
	}
	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("CheckInnerInvariants: %v", err)
	}
}

func TestComposeReplayDivergenceHaltsWithoutRepeatingWrites(t *testing.T) {
	t.Run("definition_change_on_replay", func(t *testing.T) {
		ctx := context.Background()
		h := newTestHarness(t, 204, harnessOpts{disableFaults: true})
		ctx = users.WithID(ctx, h.user.ID)
		composeAllowWrites(t, ctx, h)

		run := h.startTask(ctx, "Update alpha/beta and send hello to alice@example.com for replay divergence")
		baseRef, rev := composeSyncTask(t, ctx, h, run)
		composeExec := composeExecPtr(t, ctx, h)

		var callsSeen atomic.Int32
		h.userRT.Adapters.Register(extensions.AdapterNative, "sim", &composeHookAdapter{
			base: h.driver.Adapter,
			afterExecute: func(_ context.Context, _ invoke.Call, _ invoke.Prepared, _ invoke.Result) {
				if callsSeen.Add(1) == 2 {
					if err := h.setPaused(context.Background(), true); err != nil {
						t.Errorf("setPaused: %v", err)
					}
				}
			},
		})

		script := map[string]any{
			"version":     1,
			"concurrency": 2,
			"steps": []map[string]any{
				{
					"id":   "w1",
					"tool": inner.ToolNameWrite,
					"args": map[string]any{"target": "alpha", "value": "1", "resource_id": "alpha"},
				},
				{
					"id":   "s1",
					"tool": inner.ToolNameSend,
					"args": map[string]any{"to": "alice@example.com", "body": "hello"},
				},
				{
					"id":   "w2",
					"tool": inner.ToolNameWrite,
					"args": map[string]any{"target": "beta", "value": "2", "resource_id": "beta"},
				},
			},
		}

		ref := composeAppendCall(t, ctx, h, run.ID, baseRef, "call-div-def", script)
		opID := run.ID + "-op-div-def"
		_, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
			Step:        task.Step{RunID: run.ID, Revision: rev},
			Ref:         ref,
			OperationID: opID,
			Index:       0,
			Exec:        composeExec,
		})
		if composeBoundaryKind(err) != "paused" {
			t.Fatalf("attempt 1 err = %v, want paused", err)
		}

		if err := h.setPaused(ctx, false); err != nil {
			t.Fatalf("unpause: %v", err)
		}
		bumpedSend := inner.SimSendDef
		bumpedSend.Ref.Version = "2"
		bumpedSend.Ref.Digest = ""
		bumpedSend = composeMustSeal(t, bumpedSend)
		h.userRT.Extensions.Definitions = []extensions.Definition{inner.SimReadDef, inner.SimWriteDef, bumpedSend}

		out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
			Step:        task.Step{RunID: run.ID, Revision: run.Revision},
			Ref:         ref,
			OperationID: opID,
			Index:       0,
			Exec:        composeExec,
		})
		if err != nil {
			t.Fatalf("attempt 2 ToolRef: %v", err)
		}
		if out.Outcome != "failed" {
			t.Fatalf("attempt 2 outcome = %q, want failed", out.Outcome)
		}

		result := composeReadLastToolResult(t, ctx, h, run.ID, out.Ref)
		rawCalls, _ := json.Marshal(result["calls"])
		var callList []map[string]any
		if err := json.Unmarshal(rawCalls, &callList); err != nil {
			t.Fatalf("unmarshal calls: %v", err)
		}
		if len(callList) != 2 {
			t.Fatalf("calls count = %d (%s), want 2 (reused w1 + diverged s1)", len(callList), rawCalls)
		}
		if callList[0]["outcome"] != "confirmed" || callList[0]["reused"] != true {
			t.Fatalf("call[0] = %+v, want confirmed with reused=true", callList[0])
		}
		if callList[1]["outcome"] != "diverged" {
			t.Fatalf("call[1] = %+v, want diverged", callList[1])
		}

		executed := h.driver.Adapter.Executed()
		if len(executed) != 2 {
			t.Fatalf("total external executions = %d, want 2 (w1 once, s1 once, w2 never)", len(executed))
		}
		if err := h.driver.CheckInnerInvariants(ctx); err != nil {
			t.Fatalf("CheckInnerInvariants: %v", err)
		}
	})

	t.Run("changed_args_on_replay", func(t *testing.T) {
		ctx := context.Background()
		h := newTestHarness(t, 205, harnessOpts{disableFaults: true})
		ctx = users.WithID(ctx, h.user.ID)
		composeAllowWrites(t, ctx, h)

		run := h.startTask(ctx, "Replay divergence on changed args")
		baseRef, rev := composeSyncTask(t, ctx, h, run)
		composeExec := composeExecPtr(t, ctx, h)

		var callsSeen atomic.Int32
		h.userRT.Adapters.Register(extensions.AdapterNative, "sim", &composeHookAdapter{
			base: h.driver.Adapter,
			afterExecute: func(_ context.Context, _ invoke.Call, _ invoke.Prepared, _ invoke.Result) {
				if callsSeen.Add(1) == 2 {
					if err := h.setPaused(context.Background(), true); err != nil {
						t.Errorf("setPaused: %v", err)
					}
				}
			},
		})

		script1 := map[string]any{
			"version":     1,
			"concurrency": 2,
			"steps": []map[string]any{
				{
					"id":   "w1",
					"tool": inner.ToolNameWrite,
					"args": map[string]any{"target": "k1", "value": "v1", "resource_id": "k1"},
				},
				{
					"id":   "w2",
					"tool": inner.ToolNameWrite,
					"args": map[string]any{"target": "k2", "value": "v2", "resource_id": "k2"},
				},
				{
					"id":   "w3",
					"tool": inner.ToolNameWrite,
					"args": map[string]any{"target": "k3", "value": "v3", "resource_id": "k3"},
				},
			},
		}

		opID := run.ID + "-op-div-args"
		ref1 := composeAppendCall(t, ctx, h, run.ID, baseRef, "call-div-args", script1)
		_, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
			Step:        task.Step{RunID: run.ID, Revision: rev},
			Ref:         ref1,
			OperationID: opID,
			Index:       0,
			Exec:        composeExec,
		})
		if composeBoundaryKind(err) != "paused" {
			t.Fatalf("attempt 1 err = %v, want paused", err)
		}

		if err := h.setPaused(ctx, false); err != nil {
			t.Fatalf("unpause: %v", err)
		}

		script2 := map[string]any{
			"version":     1,
			"concurrency": 2,
			"steps": []map[string]any{
				{
					"id":   "w1",
					"tool": inner.ToolNameWrite,
					"args": map[string]any{"target": "k1", "value": "v1", "resource_id": "k1"},
				},
				{
					"id":   "w2",
					"tool": inner.ToolNameWrite,
					"args": map[string]any{"target": "k2", "value": "DIFFERENT", "resource_id": "k2"},
				},
				{
					"id":   "w3",
					"tool": inner.ToolNameWrite,
					"args": map[string]any{"target": "k3", "value": "v3", "resource_id": "k3"},
				},
			},
		}
		ref2 := composeAppendCall(t, ctx, h, run.ID, baseRef, "call-div-args", script2)
		out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
			Step:        task.Step{RunID: run.ID, Revision: rev},
			Ref:         ref2,
			OperationID: opID,
			Index:       0,
			Exec:        composeExec,
		})
		if err != nil {
			t.Fatalf("attempt 2 ToolRef: %v", err)
		}
		if out.Outcome != "failed" {
			t.Fatalf("attempt 2 outcome = %q, want failed", out.Outcome)
		}

		result := composeReadLastToolResult(t, ctx, h, run.ID, out.Ref)
		rawCalls, _ := json.Marshal(result["calls"])
		var callList []map[string]any
		if err := json.Unmarshal(rawCalls, &callList); err != nil {
			t.Fatalf("unmarshal calls: %v", err)
		}
		if len(callList) != 2 {
			t.Fatalf("calls count = %d (%s), want 2", len(callList), rawCalls)
		}
		if callList[0]["outcome"] != "confirmed" || callList[0]["reused"] != true {
			t.Fatalf("call[0] = %+v, want confirmed with reused=true", callList[0])
		}
		if callList[1]["outcome"] != "diverged" {
			t.Fatalf("call[1] = %+v, want diverged", callList[1])
		}
		if len(h.driver.Adapter.Executed()) != 2 {
			t.Fatalf("external executions = %d, want 2 (no repeated writes, w3 not run)", len(h.driver.Adapter.Executed()))
		}
		if err := h.driver.CheckInnerInvariants(ctx); err != nil {
			t.Fatalf("CheckInnerInvariants: %v", err)
		}
	})
}

func TestComposeSubtaskFamilyBudgetEnforcement(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 206, harnessOpts{disableFaults: true, budgetUSD: 1.00})
	ctx = users.WithID(ctx, h.user.ID)

	custom := &composeCustomAdapter{costUSD: 0.05}
	composeRegisterCustomTools(t, h, custom, 0.05)

	parent := h.startTask(ctx, "Parent sharing family budget")
	parentTurn := 0
	h.driver.Model.SetScript(func(_ context.Context, runID string, _ agent.Request) (agent.Response, error, bool) {
		if runID == parent.ID {
			parentTurn++
			if parentTurn == 1 {
				return scriptedCall("p-work", "request_work", map[string]any{"request": "delegate work", "why": "parallel subtasks"}, 0.60), nil, true
			}
		}
		return agent.Response{}, nil, false
	})

	h.stepUntilParkedOrDone(ctx, parent.ID, 20)
	children, err := h.driver.SpawnSubtask(ctx, parent.ID, "Sibling spender", "Compose worker")
	if err != nil {
		t.Fatalf("SpawnSubtask: %v", err)
	}
	sibling, worker := children[0], children[1]

	if err := h.store.Spend(ctx, state.SpendUpdate{RunID: sibling.ID, Spent: 0.28, Budget: 1.00}); err != nil {
		t.Fatalf("Spend sibling: %v", err)
	}
	parentAfter, err := h.store.GetRun(ctx, parent.ID)
	if err != nil {
		t.Fatalf("GetRun parent: %v", err)
	}
	if parentAfter.Spent+parentAfter.SubtaskSpent < 0.879 || parentAfter.Spent+parentAfter.SubtaskSpent > 0.881 {
		t.Fatalf("family spend before compose = %.4f, want 0.88", parentAfter.Spent+parentAfter.SubtaskSpent)
	}

	workerBaseRef, workerRev := composeSyncTask(t, ctx, h, worker)
	composeExec := composeExecPtr(t, ctx, h)
	paidScript := map[string]any{
		"version":     1,
		"concurrency": 4,
		"steps": []map[string]any{
			{
				"id":       "paid",
				"tool":     "sim_paid_read",
				"for_each": []any{"p1", "p2", "p3", "p4", "p5"},
				"as":       "k",
				"args":     map[string]any{"key": "$k"},
			},
		},
	}
	ref := composeAppendCall(t, ctx, h, worker.ID, workerBaseRef, "call-budget", paidScript)
	out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
		Step:        task.Step{RunID: worker.ID, Revision: workerRev},
		Ref:         ref,
		OperationID: worker.ID + "-op-budget",
		Index:       0,
		Exec:        composeExec,
	})
	if err != nil {
		t.Fatalf("ToolRef subtask compose: %v", err)
	}
	if out.Outcome != "confirmed" {
		t.Fatalf("outcome = %q, want confirmed", out.Outcome)
	}
	if custom.count() != 2 {
		t.Fatalf("paid tool executed %d times, want 2 (remaining family budget 0.12 / 0.05 = 2)", custom.count())
	}
	if out.Cost < 0.099 || out.Cost > 0.101 {
		t.Fatalf("compose cost = %.4f, want 0.10", out.Cost)
	}
	if parentAfter.Spent+parentAfter.SubtaskSpent+out.Cost > 1.00 {
		t.Fatalf("family spend exceeded budget: %.4f > 1.00", parentAfter.Spent+parentAfter.SubtaskSpent+out.Cost)
	}

	result := composeReadLastToolResult(t, ctx, h, worker.ID, out.Ref)
	rawCalls, _ := json.Marshal(result["calls"])
	var callList []map[string]any
	if err := json.Unmarshal(rawCalls, &callList); err != nil {
		t.Fatalf("unmarshal calls: %v", err)
	}
	confirmedCount, skippedCount := 0, 0
	for _, c := range callList {
		switch c["outcome"] {
		case "confirmed":
			confirmedCount++
		case "skipped":
			skippedCount++
		}
	}
	if confirmedCount != 2 || skippedCount != 3 {
		t.Fatalf("confirmed=%d skipped=%d, want confirmed=2 skipped=3 (calls=%s)", confirmedCount, skippedCount, rawCalls)
	}

	if err := h.store.Spend(ctx, state.SpendUpdate{RunID: worker.ID, Spent: 0.12, Budget: 1.00}); err != nil {
		t.Fatalf("Spend worker to ceiling: %v", err)
	}
	freeScript := map[string]any{
		"version":     1,
		"concurrency": 1,
		"steps": []map[string]any{
			{
				"id":   "free",
				"tool": "sim_free_read",
				"args": map[string]any{"key": "should_skip"},
			},
		},
	}
	refExhausted := composeAppendCall(t, ctx, h, worker.ID, out.Ref, "call-exhausted", freeScript)
	outExhausted, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
		Step:        task.Step{RunID: worker.ID, Revision: worker.Revision},
		Ref:         refExhausted,
		OperationID: worker.ID + "-op-exhausted",
		Index:       0,
		Exec:        composeExec,
	})
	if err != nil {
		t.Fatalf("ToolRef exhausted budget: %v", err)
	}
	if custom.count() != 2 {
		t.Fatalf("expected zero additional calls at exhausted family ceiling, got total %d", custom.count())
	}
	resExhausted := composeReadLastToolResult(t, ctx, h, worker.ID, outExhausted.Ref)
	rawExhausted, _ := json.Marshal(resExhausted["calls"])
	if !strings.Contains(string(rawExhausted), `"outcome":"skipped"`) {
		t.Fatalf("expected skipped call at exhausted ceiling, got %s", rawExhausted)
	}
}

func TestComposeUTF8SafeOutputClipping(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 207, harnessOpts{disableFaults: true})
	ctx = users.WithID(ctx, h.user.ID)

	bigPayload := strings.Repeat("🔥日本語a", 4000)
	custom := &composeCustomAdapter{bigBody: bigPayload}
	composeRegisterCustomTools(t, h, custom, 0.05)

	run := h.startTask(ctx, "UTF-8 output clipping")
	baseRef, rev := composeSyncTask(t, ctx, h, run)
	composeExec := composeExecPtr(t, ctx, h)

	script := map[string]any{
		"version":     1,
		"concurrency": 1,
		"steps": []map[string]any{
			{
				"id":   "big",
				"tool": "sim_big_utf8",
				"args": map[string]any{},
			},
		},
		"output": "$big.payload",
	}
	ref := composeAppendCall(t, ctx, h, run.ID, baseRef, "call-utf8", script)
	out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
		Step:        task.Step{RunID: run.ID, Revision: rev},
		Ref:         ref,
		OperationID: run.ID + "-op-utf8",
		Index:       0,
		Exec:        composeExec,
	})
	if err != nil {
		t.Fatalf("ToolRef: %v", err)
	}
	if out.Outcome != "confirmed" {
		t.Fatalf("outcome = %q, want confirmed", out.Outcome)
	}

	result := composeReadLastToolResult(t, ctx, h, run.ID, out.Ref)
	if truncatedFlag, _ := result["output_truncated"].(bool); !truncatedFlag {
		t.Fatalf("expected output_truncated=true")
	}
	truncated, ok := result["output"].(string)
	if !ok {
		t.Fatalf("expected truncated string output, got %T", result["output"])
	}
	if !utf8.ValidString(truncated) {
		t.Fatalf("truncated compose output is invalid UTF-8")
	}
	if !strings.HasSuffix(truncated, "…") {
		t.Fatalf("truncated compose output missing ellipsis suffix")
	}
	body := strings.TrimSuffix(truncated, "…")
	if len(body) > 32<<10 {
		t.Fatalf("truncated body len = %d, exceeds 32 KiB", len(body))
	}
}

func runDeterministicComposeWorkload(t *testing.T, seed int64) (backendDigest, adapterDigest, resultJSON string) {
	t.Helper()
	ctx := context.Background()
	h := newTestHarness(t, seed, harnessOpts{disableFaults: true, budgetUSD: 1.00})
	ctx = users.WithID(ctx, h.user.ID)
	composeAllowWrites(t, ctx, h)

	custom := &composeCustomAdapter{costUSD: 0.04}
	composeRegisterCustomTools(t, h, custom, 0.04)

	run := h.startTask(ctx, fmt.Sprintf("Deterministic compose workload seed=%d", seed))
	baseRef, rev := composeSyncTask(t, ctx, h, run)
	composeExec := composeExecPtr(t, ctx, h)

	script := map[string]any{
		"version":     1,
		"concurrency": 1,
		"steps": []map[string]any{
			{
				"id":       "writes",
				"tool":     inner.ToolNameWrite,
				"for_each": []any{"a", "b", "c"},
				"as":       "k",
				"args":     map[string]any{"target": "$k", "value": "v_{{ $k }}", "resource_id": "$k"},
			},
			{
				"id":       "reads",
				"tool":     inner.ToolNameRead,
				"for_each": []any{"a", "b", "c"},
				"as":       "k",
				"args":     map[string]any{"key": "$k"},
			},
			{
				"id":       "paid",
				"tool":     "sim_paid_read",
				"for_each": []any{"x", "y"},
				"as":       "k",
				"args":     map[string]any{"key": "$k"},
			},
		},
		"output": map[string]any{
			"written": "$writes[*].target",
			"read":    "$reads[*].value",
			"paid":    "$paid[*].value",
		},
	}

	ref := composeAppendCall(t, ctx, h, run.ID, baseRef, "call-det", script)
	opID := run.ID + "-op-det"
	out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
		Step:        task.Step{RunID: run.ID, Revision: rev},
		Ref:         ref,
		OperationID: opID,
		Index:       0,
		Exec:        composeExec,
	})
	if err != nil {
		t.Fatalf("seed %d ToolRef: %v", seed, err)
	}
	if out.Outcome != "confirmed" {
		t.Fatalf("seed %d outcome = %q, want confirmed", seed, out.Outcome)
	}

	replayOut, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
		Step:        task.Step{RunID: run.ID, Revision: run.Revision},
		Ref:         ref,
		OperationID: opID,
		Index:       0,
		Exec:        composeExec,
	})
	if err != nil {
		t.Fatalf("seed %d replay ToolRef: %v", seed, err)
	}
	if replayOut.Outcome != "confirmed" {
		t.Fatalf("seed %d replay outcome = %q, want confirmed", seed, replayOut.Outcome)
	}

	if err := h.driver.CheckInnerInvariants(ctx); err != nil {
		t.Fatalf("seed %d CheckInnerInvariants: %v", seed, err)
	}

	res := composeReadLastToolResult(t, ctx, h, run.ID, out.Ref)
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return h.backend.Digest(), h.driver.Adapter.Digest(), string(raw)
}

func TestComposeDeterminismAcrossSeeds(t *testing.T) {
	for _, seed := range []int64{401, 402, 403} {
		b1, a1, r1 := runDeterministicComposeWorkload(t, seed)
		b2, a2, r2 := runDeterministicComposeWorkload(t, seed)
		if b1 != b2 {
			t.Fatalf("seed %d backend digest mismatch: %s != %s", seed, b1, b2)
		}
		if a1 != a2 {
			t.Fatalf("seed %d adapter digest mismatch: %s != %s", seed, a1, a2)
		}
		if r1 != r2 {
			t.Fatalf("seed %d result JSON mismatch:\n%s\n!=\n%s", seed, r1, r2)
		}
	}
}
