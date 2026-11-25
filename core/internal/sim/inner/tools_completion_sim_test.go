package inner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"nori/internal/agent"
	"nori/internal/agent/responses"
	"nori/internal/extensions"
	"nori/internal/permissions"
	"nori/internal/sim/inner"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/task"
	"nori/internal/transcript"
	"nori/internal/usage"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type noopMeter struct{}

func (noopMeter) RecordUsage(context.Context, state.Usage) error {
	return nil
}

func builtinExecFor(t *testing.T, name string) *extensions.Exec {
	t.Helper()
	for _, d := range agent.BuiltinDefinitions {
		if d.ModelName == name {
			exec := d.Exec()
			return &exec
		}
	}
	t.Fatalf("builtin definition %q not found", name)
	return nil
}

func appendModelCallHistory(t *testing.T, ctx context.Context, store state.TaskStore, runID, parentRef string, call transcript.ToolCall) string {
	t.Helper()
	msg := transcript.Message{
		Role:  transcript.RoleModel,
		Parts: []transcript.Part{{ToolCall: &call}},
	}
	data, err := json.Marshal([]transcript.Message{msg})
	if err != nil {
		t.Fatalf("marshal model message: %v", err)
	}
	ref, err := store.AppendHistory(ctx, runID, parentRef, data)
	if err != nil {
		t.Fatalf("AppendHistory: %v", err)
	}
	return ref
}

func readToolResultOutput(t *testing.T, ctx context.Context, store state.TaskStore, runID, ref, callID string) map[string]any {
	t.Helper()
	blocks, err := store.ReadHistory(ctx, runID, ref)
	if err != nil {
		t.Fatalf("ReadHistory: %v", err)
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		var chunk []transcript.Message
		if err := json.Unmarshal(blocks[i], &chunk); err != nil {
			continue
		}
		for j := len(chunk) - 1; j >= 0; j-- {
			for _, part := range chunk[j].Parts {
				if part.ToolResult != nil && part.ToolResult.CallID == callID {
					return part.ToolResult.Output
				}
			}
		}
	}
	t.Fatalf("tool result for call %q not found in ref %q", callID, ref)
	return nil
}

func TestClearedUncertainWriteCompletes(t *testing.T) {
	runScenario := func(seed int64) (string, string) {
		ctx := context.Background()
		h := newTestHarness(t, seed, harnessOpts{disableFaults: true})

		if _, err := h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{inner.ToolIDWrite},
			ConnectionIDs: []string{},
			ResourceIDs:   []string{"*"},
			ActionClasses: []string{"unknown"},
			Title:         "Allow sim_write for cleared uncertain completion",
		}); err != nil {
			t.Fatalf("SaveRule: %v", err)
		}

		h.driver.Adapter.QueueForcedOutcome(
			inner.ForcedOutcome{Outcome: "uncertain", ReconcileMiss: true},
		)

		turn := 0
		h.driver.Model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return scriptedCall("w1", inner.ToolNameWrite, map[string]any{
					"target":      "invoice-42",
					"value":       "approved",
					"resource_id": "invoice-42",
				}, 0.05), nil, true
			}
			return scriptedCall("c1", "complete", map[string]any{
				"answer":   "Confirmed with user and completed.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.05), nil, true
		})

		run := h.startTask(ctx, "Approve invoice 42")
		h.stepUntilParkedOrDone(ctx, run.ID, 40)

		midRun, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun mid: %v", err)
		}
		if midRun.Question == nil || len(midRun.Question.Clears) != 1 {
			t.Fatalf("expected uncertain-action question with 1 Clears op, got %+v", midRun.Question)
		}

		opID := midRun.Question.Clears[0]
		beforeOp, err := h.store.Operation(ctx, run.ID, opID)
		if err != nil {
			t.Fatalf("Operation before answer: %v", err)
		}
		if beforeOp.Outcome != "uncertain" || beforeOp.Cleared != "" {
			t.Fatalf("expected uncertain uncleared op before answer, got outcome=%q cleared=%q", beforeOp.Outcome, beforeOp.Cleared)
		}

		if err := h.deliverAnswer(ctx, &permissions.Answer{
			CommandID: h.ids.Next(),
			TaskID:    run.ID,
			Question:  midRun.Question.ID,
			Text:      "Yes, I verified invoice 42 was updated.",
		}); err != nil {
			t.Fatalf("deliverAnswer: %v", err)
		}

		h.stepUntilParkedOrDone(ctx, run.ID, 40)

		afterOp, err := h.store.Operation(ctx, run.ID, opID)
		if err != nil {
			t.Fatalf("Operation after answer: %v", err)
		}
		if afterOp.Cleared != state.ClearedAnswered {
			t.Fatalf("expected op.Cleared=%q, got %q", state.ClearedAnswered, afterOp.Cleared)
		}

		finalRun, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun final: %v", err)
		}
		if finalRun.Status != state.RunCompleted || finalRun.Error != "" {
			t.Fatalf("expected RunCompleted after user cleared uncertain write, got status=%s err=%q", finalRun.Status, finalRun.Error)
		}

		if err := h.driver.CheckInnerInvariants(ctx); err != nil {
			t.Fatalf("CheckInnerInvariants: %v", err)
		}
		return h.backend.Digest(), h.driver.Adapter.Digest()
	}

	b1, a1 := runScenario(201)
	b2, a2 := runScenario(201)
	if b1 != b2 || a1 != a2 {
		t.Fatalf("non-deterministic digests: backend %s vs %s, adapter %s vs %s", b1, b2, a1, a2)
	}
}

func TestUnclearedWritesBlockCompletionWhileReadsDoNot(t *testing.T) {
	t.Run("uncleared_failed_write_blocks_task_completion", func(t *testing.T) {
		ctx := context.Background()
		h := newTestHarness(t, 202, harnessOpts{disableFaults: true})

		h.driver.Model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			return scriptedCall("c1", "complete", map[string]any{
				"answer":   "Claimed complete despite failed write.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.05), nil, true
		})

		run := h.startTask(ctx, "Update doc-1")
		resBytes, _ := json.Marshal(map[string]any{"outcome": "failed", "effect_class": "unknown"})
		resRef, err := h.store.AppendHistory(ctx, run.ID, "", resBytes)
		if err != nil {
			t.Fatalf("AppendHistory: %v", err)
		}
		if err := h.store.PutOperation(ctx, run.ID, state.OperationRecord{
			ID:          run.ID + "-op-failed-write",
			CallID:      "w1",
			Name:        inner.ToolNameWrite,
			Revision:    run.Revision,
			EffectClass: "unknown",
			Outcome:     "failed",
			Attempt:     1,
			ResultRef:   resRef,
		}); err != nil {
			t.Fatalf("PutOperation: %v", err)
		}

		h.stepUntilParkedOrDone(ctx, run.ID, 40)

		finalRun, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if finalRun.Status != state.RunFailed || !strings.Contains(finalRun.Error, inner.ToolNameWrite) {
			t.Fatalf("expected RunFailed mentioning unresolved sim_write, got status=%s err=%q", finalRun.Status, finalRun.Error)
		}
		if err := h.driver.CheckInnerInvariants(ctx); err != nil {
			t.Fatalf("CheckInnerInvariants: %v", err)
		}
	})

	t.Run("cleared_did_not_run_failed_write_allows_completion", func(t *testing.T) {
		ctx := context.Background()
		h := newTestHarness(t, 209, harnessOpts{disableFaults: true})

		if _, err := h.perms.SaveRule(ctx, "", h.nextReqID(), 0, permissions.RuleEditable{
			Effect:        "allow",
			ToolIDs:       []string{inner.ToolIDWrite},
			ConnectionIDs: []string{},
			ResourceIDs:   []string{"*"},
			ActionClasses: []string{"unknown"},
			Title:         "Allow sim_write for did-not-run failure check",
		}); err != nil {
			t.Fatalf("SaveRule: %v", err)
		}

		h.driver.Adapter.QueueForcedOutcome(
			inner.ForcedOutcome{Outcome: "failed"},
		)

		turn := 0
		h.driver.Model.SetScript(func(_ context.Context, _ string, _ agent.Request) (agent.Response, error, bool) {
			turn++
			if turn == 1 {
				return scriptedCall("w1", inner.ToolNameWrite, map[string]any{
					"target":      "doc-1",
					"value":       "v2",
					"resource_id": "doc-1",
				}, 0.05), nil, true
			}
			return scriptedCall("c1", "complete", map[string]any{
				"answer":   "Write failed before side effect and was cleared as did_not_run.",
				"evidence": []any{},
				"unmet":    []any{},
			}, 0.05), nil, true
		})

		run := h.startTask(ctx, "Update doc-1 cleanly")
		h.stepUntilParkedOrDone(ctx, run.ID, 40)

		finalRun, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun: %v", err)
		}
		if finalRun.Status != state.RunCompleted || finalRun.Error != "" {
			t.Fatalf("expected RunCompleted for did_not_run cleared failure, got status=%s err=%q", finalRun.Status, finalRun.Error)
		}
		if err := h.driver.CheckInnerInvariants(ctx); err != nil {
			t.Fatalf("CheckInnerInvariants: %v", err)
		}
	})

	t.Run("direct_check_completion_matrix", func(t *testing.T) {
		ctx := context.Background()
		h := newTestHarness(t, 203, harnessOpts{disableFaults: true})
		run := h.startTask(ctx, "Verify CheckCompletion matrix")

		synced, err := h.userRT.Activities.SyncHistory(ctx, task.HistoryInput{
			Step: task.Step{RunID: run.ID, Revision: run.Revision},
		})
		if err != nil {
			t.Fatalf("SyncHistory: %v", err)
		}
		ref := appendModelCallHistory(t, ctx, h.store, run.ID, synced.Ref, transcript.ToolCall{
			ID:   "call-complete",
			Name: "complete",
			Args: map[string]any{"answer": "Done", "evidence": []any{}, "unmet": []any{}},
		})

		saveResultRef := func(payload map[string]any) string {
			b, _ := json.Marshal(payload)
			r, e := h.store.AppendHistory(ctx, run.ID, "", b)
			if e != nil {
				t.Fatalf("AppendHistory result: %v", e)
			}
			return r
		}

		putOp := func(op state.OperationRecord) {
			if err := h.store.PutOperation(ctx, run.ID, op); err != nil {
				t.Fatalf("PutOperation %s: %v", op.ID, err)
			}
		}

		putOp(state.OperationRecord{
			ID: "op-read-pending", CallID: "c-r1", Name: "memory_read", Revision: synced.Revision,
			EffectClass: "read", Outcome: "pending", Attempt: 1,
		})
		putOp(state.OperationRecord{
			ID: "op-read-failed", CallID: "c-r2", Name: "sim_read", Revision: synced.Revision,
			EffectClass: "read", Outcome: "failed", Attempt: 1,
			ResultRef: saveResultRef(map[string]any{"outcome": "failed"}),
		})
		putOp(state.OperationRecord{
			ID: "op-control-failed", CallID: "c-ctrl", Name: "result_link", Revision: synced.Revision,
			EffectClass: "control", Outcome: "failed", Attempt: 1,
			ResultRef: saveResultRef(map[string]any{"outcome": "failed"}),
		})
		putOp(state.OperationRecord{
			ID: "op-work-failed", CallID: "c-work", Name: "request_work", Revision: synced.Revision,
			EffectClass: "unknown", Outcome: "failed", Attempt: 1,
			ResultRef: saveResultRef(map[string]any{"outcome": "failed"}),
		})
		putOp(state.OperationRecord{
			ID: "op-write-cleared", CallID: "c-clr", Name: "sim_write", Revision: synced.Revision,
			EffectClass: "unknown", Outcome: "uncertain", Cleared: state.ClearedAnswered, Attempt: 1,
			ResultRef: saveResultRef(map[string]any{"outcome": "uncertain"}),
		})

		out, err := h.userRT.Activities.CheckCompletion(ctx, task.CompletionInput{
			Step:     task.Step{RunID: run.ID, Revision: synced.Revision},
			Ref:      ref,
			ID:       "chk-1",
			Position: synced.Position,
		})
		if err != nil {
			t.Fatalf("CheckCompletion clean: %v", err)
		}
		if out.Action != "publish" || out.Detail != "" {
			t.Fatalf("expected publish when only reads/controls/request_work/cleared exist, got action=%q detail=%q", out.Action, out.Detail)
		}

		putOp(state.OperationRecord{
			ID: "op-write-uncertain", CallID: "c-unc", Name: "sim_send", Revision: synced.Revision,
			EffectClass: "sensitive_send", Outcome: "uncertain", Attempt: 1,
			ResultRef: saveResultRef(map[string]any{"outcome": "uncertain"}),
		})

		out, err = h.userRT.Activities.CheckCompletion(ctx, task.CompletionInput{
			Step:     task.Step{RunID: run.ID, Revision: synced.Revision},
			Ref:      ref,
			ID:       "chk-2",
			Position: synced.Position,
		})
		if err != nil {
			t.Fatalf("CheckCompletion uncleared uncertain: %v", err)
		}
		if out.Action != "partial" || !strings.Contains(out.Detail, "sim_send") {
			t.Fatalf("expected partial naming sim_send, got action=%q detail=%q", out.Action, out.Detail)
		}
		if strings.Contains(out.Detail, "memory_read") || strings.Contains(out.Detail, "result_link") || strings.Contains(out.Detail, "request_work") {
			t.Fatalf("unexpected non-write op in CheckCompletion detail: %q", out.Detail)
		}
	})
}

func TestBuiltinRuntimeToolsAndMemoryContextFrom(t *testing.T) {
	ctx := context.Background()
	h := newTestHarness(t, 204, harnessOpts{disableFaults: true})

	cutoff := h.clock.Now().UTC().Truncate(time.Microsecond)
	controlPath := "owners/" + h.user.DataOwner() + "/conversation/control"
	if err := h.backend.Apply(ctx, docstore.Mutation{
		Kind:   "set",
		Path:   controlPath,
		Fields: map[string]any{"context_from": cutoff},
	}); err != nil {
		t.Fatalf("set conversation control context_from: %v", err)
	}

	summary := state.ConversationSummary{
		From:      cutoff,
		Through:   cutoff.Add(30 * time.Minute),
		Gist:      "User prefers concise bullet summaries.",
		UpdatedAt: cutoff.Add(31 * time.Minute),
		Items: []state.SummaryItem{
			{Kind: "preference", Quote: "Keep replies concise", InputID: "inp-1"},
		},
	}
	if err := h.store.SaveConversationSummary(ctx, summary, state.ConversationSummary{}); err != nil {
		t.Fatalf("SaveConversationSummary: %v", err)
	}

	uploadID := h.ids.Next()
	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	if _, err := h.store.CreateUpload(ctx, state.Upload{
		Attachment: state.Attachment{
			ID:        uploadID,
			Name:      "chart.png",
			MediaType: "image/png",
			Size:      int64(len(pngBytes)),
		},
		CreatedAt: cutoff,
	}); err != nil {
		t.Fatalf("CreateUpload: %v", err)
	}
	if err := h.store.PutFile(ctx, state.UploadKey(uploadID), "image/png", pngBytes); err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	runID := h.ids.Next()
	cmd := state.Command{
		ID:    h.ids.Next(),
		RunID: runID,
		Kind:  state.CommandStart,
		Text:  "Inspect chart and exercise runtime tools",
		Attachments: []state.Attachment{
			{ID: uploadID, Name: "chart.png", MediaType: "image/png", Size: int64(len(pngBytes))},
		},
	}
	receipts, err := h.store.AcceptCommandBatch(ctx, []state.Command{cmd}, nil)
	if err != nil {
		t.Fatalf("AcceptCommandBatch: %v", err)
	}
	run := receipts[0].Run

	synced, err := h.userRT.Activities.SyncHistory(ctx, task.HistoryInput{
		Step: task.Step{RunID: run.ID, Revision: run.Revision},
	})
	if err != nil {
		t.Fatalf("SyncHistory: %v", err)
	}
	ref := synced.Ref

	runTool := func(callID, opID, name string, args map[string]any) (task.RefToolOutcome, map[string]any) {
		ref = appendModelCallHistory(t, ctx, h.store, run.ID, ref, transcript.ToolCall{
			ID:   callID,
			Name: name,
			Args: args,
		})
		out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
			Step:        task.Step{RunID: run.ID, Revision: synced.Revision},
			Ref:         ref,
			OperationID: opID,
			Index:       0,
			Exec:        builtinExecFor(t, name),
		})
		if err != nil {
			t.Fatalf("ToolRef(%s): %v", name, err)
		}
		ref = out.Ref
		return out, readToolResultOutput(t, ctx, h.store, run.ID, ref, callID)
	}

	memOut, memRes := runTool("call-mem-1", run.ID+"-op-mem-1", "memory_read", map[string]any{})
	if memOut.Outcome != "confirmed" || memOut.EffectClass != "read" {
		t.Fatalf("unexpected memory_read outcome: %+v", memOut)
	}
	convSummary, ok := memRes["conversation_summary"].(map[string]any)
	if !ok || convSummary["model_gist"] != summary.Gist {
		t.Fatalf("expected conversation_summary matching ContextFrom, got %+v", memRes["conversation_summary"])
	}

	if err := h.backend.Apply(ctx, docstore.Mutation{
		Kind:   "set",
		Path:   controlPath,
		Fields: map[string]any{"context_from": cutoff.Add(2 * time.Hour)},
	}); err != nil {
		t.Fatalf("advance context_from: %v", err)
	}
	_, memResAfterReset := runTool("call-mem-2", run.ID+"-op-mem-2", "memory_read", map[string]any{})
	if _, exists := memResAfterReset["conversation_summary"]; exists {
		t.Fatalf("expected stale conversation_summary omitted after ContextFrom advanced, got %+v", memResAfterReset["conversation_summary"])
	}

	linkOpID := run.ID + "-op-link-1"
	linkOut, linkRes := runTool("call-link-1", linkOpID, "result_link", map[string]any{
		"url":   "https://example.com/report",
		"title": "Quarterly Report",
	})
	if linkOut.Outcome != "confirmed" || linkOut.EffectClass != "control" {
		t.Fatalf("unexpected result_link outcome: %+v", linkOut)
	}
	resultID, _ := linkRes["result_id"].(string)
	if resultID == "" {
		t.Fatalf("expected non-empty result_id from result_link, got %+v", linkRes)
	}
	savedResult, err := h.store.GetResult(ctx, resultID)
	if err != nil || savedResult.URL != "https://example.com/report" {
		t.Fatalf("GetResult(%s): %+v err=%v", resultID, savedResult, err)
	}

	opReadOut, opReadRes := runTool("call-opread-1", run.ID+"-op-read-1", "operation_read", map[string]any{
		"operation_id": linkOpID,
	})
	if opReadOut.Outcome != "confirmed" || opReadRes["saved_outcome"] != "confirmed" {
		t.Fatalf("unexpected operation_read output: out=%+v res=%+v", opReadOut, opReadRes)
	}
	nestedOutput, _ := opReadRes["output"].(map[string]any)
	if nestedOutput["result_id"] != resultID {
		t.Fatalf("expected operation_read to return saved result_link output, got %+v", nestedOutput)
	}

	histOut, histRes := runTool("call-hist-1", run.ID+"-op-hist-1", "task_history_read", map[string]any{
		"history_ref": ref,
		"before":      "3",
		"offset":      "0",
	})
	if histOut.Outcome != "confirmed" {
		t.Fatalf("unexpected task_history_read outcome: %+v", histOut)
	}
	fragments, _ := histRes["fragments"].([]any)
	if len(fragments) == 0 {
		t.Fatalf("expected non-empty fragments from task_history_read, got %+v", histRes)
	}

	inspOut, inspRes := runTool("call-insp-1", run.ID+"-op-insp-1", "upload_inspect", map[string]any{
		"upload_id": uploadID,
		"question":  "What does the chart show?",
	})
	if inspOut.Outcome != "confirmed" || inspOut.Cost != 0.01 || inspRes["observation"] != "simulated image inspection" {
		t.Fatalf("unexpected upload_inspect result: out=%+v res=%+v", inspOut, inspRes)
	}
}

func TestRuntimeToolRecoveryAndContextDeadline(t *testing.T) {
	t.Run("context_deadline_used_for_recovery_until", func(t *testing.T) {
		ctx := context.Background()
		h := newTestHarness(t, 205, harnessOpts{disableFaults: true})
		run := h.startTask(ctx, "Check context deadline for receipt recovery")

		synced, err := h.userRT.Activities.SyncHistory(ctx, task.HistoryInput{
			Step: task.Step{RunID: run.ID, Revision: run.Revision},
		})
		if err != nil {
			t.Fatalf("SyncHistory: %v", err)
		}

		expectedDeadline := time.Now().Add(95 * time.Second).UTC().Truncate(time.Microsecond)
		dlCtx, cancel := context.WithDeadline(ctx, expectedDeadline)
		defer cancel()

		opID := run.ID + "-op-deadline"
		ref := appendModelCallHistory(t, ctx, h.store, run.ID, synced.Ref, transcript.ToolCall{
			ID:   "call-dl",
			Name: "memory_read",
			Args: map[string]any{},
		})
		out, err := h.userRT.Activities.ToolRef(dlCtx, task.RefToolInput{
			Step:        task.Step{RunID: run.ID, Revision: synced.Revision},
			Ref:         ref,
			OperationID: opID,
			Index:       0,
			Exec:        builtinExecFor(t, "memory_read"),
		})
		if err != nil || out.Outcome != "confirmed" {
			t.Fatalf("ToolRef(memory_read): out=%+v err=%v", out, err)
		}

		op, err := h.store.Operation(ctx, run.ID, opID)
		if err != nil {
			t.Fatalf("Operation: %v", err)
		}
		if !op.RecoveryUntil.Equal(expectedDeadline) {
			t.Fatalf("expected RecoveryUntil=%v from context deadline, got %v", expectedDeadline, op.RecoveryUntil)
		}
	})

	t.Run("safe_runtime_read_recovery_reruns_cleanly", func(t *testing.T) {
		ctx := context.Background()
		h := newTestHarness(t, 206, harnessOpts{disableFaults: true})
		run := h.startTask(ctx, "Recover safe runtime read")

		synced, err := h.userRT.Activities.SyncHistory(ctx, task.HistoryInput{
			Step: task.Step{RunID: run.ID, Revision: run.Revision},
		})
		if err != nil {
			t.Fatalf("SyncHistory: %v", err)
		}

		opID := run.ID + "-op-mem-recover"
		futureUntil := h.clock.Now().Add(5 * time.Minute).UTC().Truncate(time.Microsecond)
		if err := h.store.Admit(ctx, run.ID, synced.Revision, opID, futureUntil); err != nil {
			t.Fatalf("Admit: %v", err)
		}
		if err := h.store.PutOperation(ctx, run.ID, state.OperationRecord{
			ID:            opID,
			CallID:        "call-mem-rec",
			Name:          "memory_read",
			Revision:      synced.Revision,
			EffectClass:   "read",
			Outcome:       "pending",
			Attempt:       1,
			RecoveryUntil: futureUntil,
		}); err != nil {
			t.Fatalf("PutOperation: %v", err)
		}

		sleepCalls := 0
		h.userRT.Activities.Sleep = func(_ context.Context, d time.Duration) error {
			sleepCalls++
			h.clock.Advance(d)
			return nil
		}

		ref := appendModelCallHistory(t, ctx, h.store, run.ID, synced.Ref, transcript.ToolCall{
			ID:   "call-mem-rec",
			Name: "memory_read",
			Args: map[string]any{},
		})
		out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
			Step:        task.Step{RunID: run.ID, Revision: synced.Revision},
			Ref:         ref,
			OperationID: opID,
			Index:       0,
			Exec:        builtinExecFor(t, "memory_read"),
		})
		if err != nil {
			t.Fatalf("ToolRef(memory_read recovery): %v", err)
		}
		if out.Outcome != "confirmed" {
			t.Fatalf("expected safe runtime read recovery to rerun and confirm, got outcome=%q", out.Outcome)
		}
		if sleepCalls != 0 {
			t.Fatalf("expected rerunnable safe read not to wait for receipt deadline, got %d sleeps", sleepCalls)
		}

		op, err := h.store.Operation(ctx, run.ID, opID)
		if err != nil {
			t.Fatalf("Operation: %v", err)
		}
		if op.Outcome != "confirmed" || op.Attempt != 2 {
			t.Fatalf("expected confirmed op with attempt=2, got outcome=%q attempt=%d", op.Outcome, op.Attempt)
		}
	})

	t.Run("paid_runtime_read_waits_for_receipt_and_settles_failed", func(t *testing.T) {
		ctx := context.Background()
		h := newTestHarness(t, 207, harnessOpts{disableFaults: true})
		run := h.startTask(ctx, "Recover paid runtime read")

		synced, err := h.userRT.Activities.SyncHistory(ctx, task.HistoryInput{
			Step: task.Step{RunID: run.ID, Revision: run.Revision},
		})
		if err != nil {
			t.Fatalf("SyncHistory: %v", err)
		}

		opID := run.ID + "-op-inspect-recover"
		receiptUntil := h.clock.Now().Add(1500 * time.Millisecond).UTC().Truncate(time.Microsecond)
		if err := h.store.Admit(ctx, run.ID, synced.Revision, opID, receiptUntil); err != nil {
			t.Fatalf("Admit: %v", err)
		}
		if err := h.store.PutOperation(ctx, run.ID, state.OperationRecord{
			ID:            opID,
			CallID:        "call-insp-rec",
			Name:          "upload_inspect",
			Revision:      synced.Revision,
			EffectClass:   "read",
			Outcome:       "pending",
			Attempt:       1,
			RecoveryUntil: receiptUntil,
		}); err != nil {
			t.Fatalf("PutOperation: %v", err)
		}

		sleepCalls := 0
		h.userRT.Activities.Sleep = func(_ context.Context, d time.Duration) error {
			sleepCalls++
			h.clock.Advance(d)
			return nil
		}

		ref := appendModelCallHistory(t, ctx, h.store, run.ID, synced.Ref, transcript.ToolCall{
			ID:   "call-insp-rec",
			Name: "upload_inspect",
			Args: map[string]any{"upload_id": h.ids.Next(), "question": "Check image"},
		})
		out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
			Step:        task.Step{RunID: run.ID, Revision: synced.Revision},
			Ref:         ref,
			OperationID: opID,
			Index:       0,
			Exec:        builtinExecFor(t, "upload_inspect"),
		})
		if err != nil {
			t.Fatalf("ToolRef(upload_inspect recovery): %v", err)
		}
		if sleepCalls == 0 || h.clock.Now().Before(receiptUntil) {
			t.Fatalf("expected paid read recovery to wait until %v, clock=%v sleeps=%d", receiptUntil, h.clock.Now(), sleepCalls)
		}
		if out.Outcome != "failed" || !out.CostUnknown {
			t.Fatalf("expected failed with CostUnknown=true, got out=%+v", out)
		}
	})

	t.Run("result_link_control_recovery_settles_failed_without_uncertain_question", func(t *testing.T) {
		ctx := context.Background()
		h := newTestHarness(t, 208, harnessOpts{disableFaults: true})
		run := h.startTask(ctx, "Recover result_link control tool")

		synced, err := h.userRT.Activities.SyncHistory(ctx, task.HistoryInput{
			Step: task.Step{RunID: run.ID, Revision: run.Revision},
		})
		if err != nil {
			t.Fatalf("SyncHistory: %v", err)
		}

		opID := run.ID + "-op-link-recover"
		receiptUntil := h.clock.Now().Add(1 * time.Second).UTC().Truncate(time.Microsecond)
		if err := h.store.Admit(ctx, run.ID, synced.Revision, opID, receiptUntil); err != nil {
			t.Fatalf("Admit: %v", err)
		}
		if err := h.store.PutOperation(ctx, run.ID, state.OperationRecord{
			ID:            opID,
			CallID:        "call-link-rec",
			Name:          "result_link",
			Revision:      synced.Revision,
			EffectClass:   "control",
			Outcome:       "pending",
			Attempt:       1,
			RecoveryUntil: receiptUntil,
		}); err != nil {
			t.Fatalf("PutOperation: %v", err)
		}

		sleepCalls := 0
		h.userRT.Activities.Sleep = func(_ context.Context, d time.Duration) error {
			sleepCalls++
			h.clock.Advance(d)
			return nil
		}

		ref := appendModelCallHistory(t, ctx, h.store, run.ID, synced.Ref, transcript.ToolCall{
			ID:   "call-link-rec",
			Name: "result_link",
			Args: map[string]any{"url": "https://example.com/a", "title": "Link A"},
		})
		out, err := h.userRT.Activities.ToolRef(ctx, task.RefToolInput{
			Step:        task.Step{RunID: run.ID, Revision: synced.Revision},
			Ref:         ref,
			OperationID: opID,
			Index:       0,
			Exec:        builtinExecFor(t, "result_link"),
		})
		if err != nil {
			t.Fatalf("ToolRef(result_link recovery): %v", err)
		}
		if sleepCalls == 0 || h.clock.Now().Before(receiptUntil) {
			t.Fatalf("expected control recovery to wait until %v, clock=%v sleeps=%d", receiptUntil, h.clock.Now(), sleepCalls)
		}
		if out.Outcome != "failed" || out.EffectClass != "control" || out.Unresolved != "" || len(out.Clears) != 0 {
			t.Fatalf("expected clean failed control settlement without uncertain question, got %+v", out)
		}

		completeRef := appendModelCallHistory(t, ctx, h.store, run.ID, out.Ref, transcript.ToolCall{
			ID:   "call-done",
			Name: "complete",
			Args: map[string]any{"answer": "Finished after control recovery", "evidence": []any{}, "unmet": []any{}},
		})
		chk, err := h.userRT.Activities.CheckCompletion(ctx, task.CompletionInput{
			Step:     task.Step{RunID: run.ID, Revision: synced.Revision},
			Ref:      completeRef,
			ID:       "chk-ctrl",
			Position: synced.Position,
		})
		if err != nil || chk.Action != "publish" {
			t.Fatalf("expected CheckCompletion publish after failed control recovery, got chk=%+v err=%v", chk, err)
		}
	})
}

func TestOpenAITariffsAndResponsesParser(t *testing.T) {
	int64Ptr := func(v int64) *int64 { return &v }
	almostEqual := func(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

	t.Run("openai_tariff_exact_and_dated_snapshots", func(t *testing.T) {
		for _, model := range []string{"gpt-6.1-sol", "gpt-6.1-sol-2026-04-01"} {
			cost, err := usage.SolCost(model, "default", int64Ptr(100_000), int64Ptr(1_000), int64Ptr(101_000), 20_000)
			if err != nil {
				t.Fatalf("SolCost(%s): %v", model, err)
			}
			expected := float64(80_000*2000+20_000*100+1_000*10000) / 1e9
			if !almostEqual(cost, expected) {
				t.Fatalf("SolCost(%s): got %.12f want %.12f", model, cost, expected)
			}
		}
		if _, err := usage.SolCost("gpt-6.1-sol-", "default", int64Ptr(100), int64Ptr(10), int64Ptr(110), 0); err == nil {
			t.Fatalf("expected SolCost to reject empty snapshot suffix")
		}
		if _, err := usage.SolCost("gpt-6.1-terra-2026-04-01", "default", int64Ptr(100), int64Ptr(10), int64Ptr(110), 0); err == nil {
			t.Fatalf("expected SolCost to reject terra model")
		}

		for _, model := range []string{"gpt-6.1-terra", "gpt-6.1-terra-2026-04-01"} {
			stdCost, err := usage.TerraCost(model, "default", int64Ptr(200_000), int64Ptr(5_000), int64Ptr(205_000), 50_000)
			if err != nil {
				t.Fatalf("TerraCost standard(%s): %v", model, err)
			}
			expectedStd := float64(150_000*1000+50_000*50+5_000*5000) / 1e9
			if !almostEqual(stdCost, expectedStd) {
				t.Fatalf("TerraCost standard(%s): got %.12f want %.12f", model, stdCost, expectedStd)
			}

			longCost, err := usage.TerraCost(model, "default", int64Ptr(300_000), int64Ptr(10_000), int64Ptr(310_000), 100_000)
			if err != nil {
				t.Fatalf("TerraCost long(%s): %v", model, err)
			}
			expectedLong := float64(200_000*2000+100_000*100+10_000*7500) / 1e9
			if !almostEqual(longCost, expectedLong) {
				t.Fatalf("TerraCost long(%s): got %.12f want %.12f", model, longCost, expectedLong)
			}
		}
		if _, err := usage.TerraCost("gpt-6.1-terra-", "default", int64Ptr(100), int64Ptr(10), int64Ptr(110), 0); err == nil {
			t.Fatalf("expected TerraCost to reject empty snapshot suffix")
		}
	})

	t.Run("responses_dated_snapshot_and_non_negative_output_tokens", func(t *testing.T) {
		ctx := context.Background()

		directClient := responses.New("test-key", "gpt-6.1-terra", noopMeter{})
		directClient.HTTP = &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				payload := `{
					"status": "completed",
					"model": "gpt-6.1-terra-2026-04-01",
					"service_tier": "default",
					"usage": {
						"input_tokens": 100000,
						"output_tokens": 2000,
						"total_tokens": 102000,
						"input_tokens_details": {"cached_tokens": 25000},
						"output_tokens_details": {"reasoning_tokens": 500}
					},
					"output": [
						{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Snapshot ok"}]}
					]
				}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewBufferString(payload)),
				}, nil
			}),
		}

		resp, err := directClient.Respond(ctx, agent.Request{
			Instructions:    "test",
			Messages:        []transcript.Message{transcript.UserText("hi")},
			MaxOutputTokens: 256,
		})
		if err != nil {
			t.Fatalf("direct Respond: %v", err)
		}
		expectedCost := float64(75_000*1000+25_000*50+2_000*5000) / 1e9
		if !resp.CostKnown || !almostEqual(resp.Cost, expectedCost) {
			t.Fatalf("expected calculated dated snapshot cost %.12f, got known=%v cost=%.12f", expectedCost, resp.CostKnown, resp.Cost)
		}
		if resp.Message == nil || resp.Answer != "Snapshot ok" {
			t.Fatalf("expected message and answer, got msg=%v answer=%q", resp.Message, resp.Answer)
		}
		if resp.Usage == nil || resp.Usage.OutputTokens != 1500 || resp.Usage.ReasoningTokens != 500 {
			t.Fatalf("unexpected usage: %+v", resp.Usage)
		}

		gatewayClient := responses.NewGateway("https://gateway.local", "gw-key", "custom/model", noopMeter{})
		gatewayClient.HTTP = &http.Client{
			Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				payload := `{
					"status": "completed",
					"model": "custom/model",
					"usage": {
						"input_tokens": 500,
						"output_tokens": 40,
						"total_tokens": 660,
						"cost": 0.012,
						"output_tokens_details": {"reasoning_tokens": 120}
					},
					"output": [
						{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Gateway ok"}]}
					]
				}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(bytes.NewBufferString(payload)),
				}, nil
			}),
		}

		gwResp, err := gatewayClient.Respond(ctx, agent.Request{
			Instructions:    "test",
			Messages:        []transcript.Message{transcript.UserText("hi")},
			MaxOutputTokens: 256,
		})
		if err != nil {
			t.Fatalf("gateway Respond: %v", err)
		}
		if gwResp.Usage == nil || gwResp.Usage.OutputTokens != 0 || gwResp.Usage.ReasoningTokens != 120 {
			t.Fatalf("expected clamped OutputTokens=0 and ReasoningTokens=120, got %+v", gwResp.Usage)
		}
	})

	t.Run("responses_parse_failure_does_not_leak_partial_message", func(t *testing.T) {
		ctx := context.Background()

		cases := []struct {
			name    string
			payload string
		}{
			{
				name: "malformed_second_function_call",
				payload: `{
					"status": "completed",
					"model": "gpt-6.1-terra-2026-04-01",
					"service_tier": "default",
					"usage": {"input_tokens": 1000, "output_tokens": 100, "total_tokens": 1100},
					"output": [
						{"type": "function_call", "call_id": "c1", "name": "memory_read", "arguments": "{}"},
						{"type": "function_call", "call_id": "c2", "name": "memory_read", "arguments": "{not-json"}
					]
				}`,
			},
			{
				name: "malformed_item_json_type",
				payload: `{
					"status": "completed",
					"model": "gpt-6.1-terra-2026-04-01",
					"service_tier": "default",
					"usage": {"input_tokens": 1000, "output_tokens": 100, "total_tokens": 1100},
					"output": [
						{"type": "function_call", "call_id": "c1", "name": "memory_read", "arguments": "{}"},
						"not-an-object"
					]
				}`,
			},
			{
				name: "empty_output_no_answer",
				payload: `{
					"status": "completed",
					"model": "gpt-6.1-terra-2026-04-01",
					"service_tier": "default",
					"usage": {"input_tokens": 1000, "output_tokens": 100, "total_tokens": 1100},
					"output": []
				}`,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				client := responses.New("test-key", "gpt-6.1-terra", noopMeter{})
				client.HTTP = &http.Client{
					Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": []string{"application/json"}},
							Body:       io.NopCloser(bytes.NewBufferString(tc.payload)),
						}, nil
					}),
				}
				resp, err := client.Respond(ctx, agent.Request{
					Instructions:    "test",
					Messages:        []transcript.Message{transcript.UserText("hi")},
					MaxOutputTokens: 256,
				})
				if err == nil {
					t.Fatalf("expected parse error for %s, got nil", tc.name)
				}
				if resp.Message != nil {
					t.Fatalf("expected nil Message on parse failure for %s, got %+v", tc.name, resp.Message)
				}
				if !resp.CostKnown || resp.Cost <= 0 {
					t.Fatalf("expected billed cost preserved on parse error for %s, got known=%v cost=%v", tc.name, resp.CostKnown, resp.Cost)
				}
			})
		}
	})
}

func TestDST_InnerContext128KiBAndMemoryRecallTools(t *testing.T) {
	runScenario := func(seed int64) (string, string) {
		ctx := context.Background()
		h := newTestHarness(t, seed, harnessOpts{
			disableFaults: true,
			budgetUSD:     5.0,
			costPerCall:   0.01,
		})

		// 1. Seed pre-ContextFrom conversation message and Daily Log entry on virtual clock t0.
		t0 := h.clock.Now().UTC().Truncate(time.Microsecond)
		todayDate := t0.Format("2006-01-02")
		preMsgID := h.ids.Next()
		if err := h.store.AcceptConversationInput(ctx, preMsgID, "aurora pre-cutoff secret aurora_secret_pre_cutoff do not leak old chat", nil); err != nil {
			t.Fatalf("AcceptConversationInput pre-cutoff: %v", err)
		}
		if _, err := h.store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			NextSummary: state.ConversationSummary{
				From:           time.Time{},
				FlushedThrough: t0,
				Through:        t0,
				Gist:           "Pre-cutoff gist should not appear after ContextFrom",
				UpdatedAt:      t0,
			},
			DailyLogDate: todayDate,
			DailyLogEntries: []state.MemoryLogEntry{{
				ID:            "pre-log-1",
				Section:       "Decisions",
				Text:          "aurora pre-cutoff decision aurora_secret_pre_cutoff log",
				About:         "the user",
				Provenance:    state.ProvenancePreCompactFlush,
				SourceInputID: preMsgID,
				CreatedAt:     t0,
				UpdatedAt:     t0,
			}},
		}); err != nil {
			t.Fatalf("CommitFlushAndCompact pre-cutoff: %v", err)
		}

		// 2. Advance virtual clock +2h and set ContextFrom cutoff.
		h.clock.Advance(2 * time.Hour)
		cutoff := h.clock.Now().UTC().Truncate(time.Microsecond)
		controlPath := "owners/" + h.user.DataOwner() + "/conversation/control"
		if err := h.backend.Apply(ctx, docstore.Mutation{
			Kind:   "set",
			Path:   controlPath,
			Fields: map[string]any{"context_from": cutoff},
		}); err != nil {
			t.Fatalf("set context_from: %v", err)
		}

		// 3. Advance virtual clock +10m and seed post-ContextFrom message, Core memories, Daily Log, and Working Memory.
		h.clock.Advance(10 * time.Minute)
		postTime := h.clock.Now().UTC().Truncate(time.Microsecond)
		postMsgID := h.ids.Next()
		if err := h.store.AcceptConversationInput(ctx, postMsgID, "aurora post-cutoff aurora_post_cutoff discuss milestone alpha", nil); err != nil {
			t.Fatalf("AcceptConversationInput post-cutoff: %v", err)
		}
		// Mark outer routing clear so the inner task publishes without holding for UserRoutingPending.
		convCtrl, _, err := h.store.ConversationStatus(ctx)
		if err != nil {
			t.Fatalf("ConversationStatus: %v", err)
		}
		if err := h.backend.Apply(ctx, docstore.Mutation{
			Kind: "set",
			Path: controlPath,
			Fields: map[string]any{
				"context_from":  cutoff,
				"received":      convCtrl.Received,
				"consumed":      convCtrl.Received,
				"user_received": convCtrl.UserReceived,
				"user_routed":   convCtrl.UserReceived,
			},
		}); err != nil {
			t.Fatalf("clear user routing pending: %v", err)
		}

		// Create 6 Core memories (~1800B each = ~10.8 KiB total) so memory_get("core") output is large enough
		// to trigger fitContext (>128 KiB) omission on older tool outputs cleanly.
		pinnedTrue := true
		for i := range 6 {
			padding := strings.Repeat(fmt.Sprintf("aurora_post_cutoff_core_%d ", i), 65)
			text := fmt.Sprintf("Core note #%d for aurora aurora_post_cutoff: %s", i, padding)
			if len(text) > 1800 {
				text = text[:1800]
			}
			var pinned *bool
			if i == 0 {
				pinned = &pinnedTrue
			}
			if _, err := h.store.MutateMemory(ctx, state.MemoryMutationRequest{
				RequestID: h.nextReqID(),
				Kind:      state.MemoryMutateCreateCore,
				Text:      text,
				About:     "the user",
				Section:   "Projects",
				Pinned:    pinned,
			}); err != nil {
				t.Fatalf("MutateMemory create core %d: %v", i, err)
			}
		}

		curSummary, err := h.store.ConversationSummary(ctx)
		if err != nil {
			t.Fatalf("ConversationSummary before post-cutoff flush: %v", err)
		}
		if _, err := h.store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			ExpectedSummary: curSummary,
			NextSummary: state.ConversationSummary{
				From:           cutoff,
				FlushedThrough: postTime,
				Through:        postTime,
				Gist:           "Working on aurora_post_cutoff milestone alpha",
				Items: []state.SummaryItem{
					{Kind: "decision", Quote: "aurora_post_cutoff discuss milestone alpha", InputID: postMsgID, Note: "Active milestone"},
				},
				UpdatedAt: postTime,
			},
			DailyLogDate: todayDate,
			DailyLogEntries: []state.MemoryLogEntry{{
				ID:            "post-log-1",
				Section:       "Decisions",
				Text:          "Decided to ship aurora aurora_post_cutoff milestone alpha on Friday",
				About:         "the user",
				Provenance:    state.ProvenancePreCompactFlush,
				SourceInputID: postMsgID,
				CreatedAt:     postTime,
				UpdatedAt:     postTime,
			}},
		}); err != nil {
			t.Fatalf("CommitFlushAndCompact post-cutoff: %v", err)
		}

		coreBefore, err := h.store.Memories(ctx)
		if err != nil {
			t.Fatalf("Memories before task: %v", err)
		}
		if len(coreBefore) != 6 {
			t.Fatalf("expected 6 Core memories before task, got %d", len(coreBefore))
		}

		// 4. Drive a real task.Runner execution > 128 KiB exercising fitContext -> projectContext and all 7 recall tools.
		var (
			phase                 = "grow_fit"
			growFitCalls          = 0
			growProjectCalls      = 0
			recallStep            = 0
			sawFitContextOmission bool
			sawProjectContext     bool
			omittedOpID           string
			archiveHistoryRef     string
			archiveBefore         string
			verifiedTools         = map[string]bool{}
		)

		assertNoPreCutoffLeak := func(toolName string, output map[string]any) {
			raw, _ := json.Marshal(output)
			if strings.Contains(string(raw), "aurora_secret_pre_cutoff") || strings.Contains(string(raw), preMsgID) || strings.Contains(string(raw), "pre-log-1") {
				t.Fatalf("%s leaked pre-ContextFrom data: %s", toolName, string(raw))
			}
		}

		h.driver.Model.SetScript(func(_ context.Context, _ string, req agent.Request) (agent.Response, error, bool) {
			// Scan incoming projected/fitted request messages for fitContext omissions, projectContext archive notices,
			// and recall tool results from the previous turn.
			for _, msg := range req.Messages {
				for _, p := range msg.Parts {
					if p.ToolResult != nil {
						if omitted, _ := p.ToolResult.Output["omitted"].(bool); omitted {
							if opID, _ := p.ToolResult.Output["operation_id"].(string); opID != "" {
								sawFitContextOmission = true
								if omittedOpID == "" {
									omittedOpID = opID
								}
							}
						}
						switch p.ToolResult.CallID {
						case "recall-memory-read":
							if verifiedTools["memory_read"] {
								continue
							}
							assertNoPreCutoffLeak("memory_read", p.ToolResult.Output)
							mems, _ := p.ToolResult.Output["memories"].([]any)
							cs, _ := p.ToolResult.Output["conversation_summary"].(map[string]any)
							if len(mems) != 6 || cs == nil || cs["model_gist"] != "Working on aurora_post_cutoff milestone alpha" {
								t.Fatalf("unexpected memory_read output: %+v", p.ToolResult.Output)
							}
							verifiedTools["memory_read"] = true

						case "recall-memory-search":
							if verifiedTools["memory_search"] {
								continue
							}
							assertNoPreCutoffLeak("memory_search", p.ToolResult.Output)
							results, _ := p.ToolResult.Output["results"].([]any)
							var sawCore, sawLog bool
							for _, raw := range results {
								row, _ := raw.(map[string]any)
								if row["layer"] == docstore.MemoryLayerCore {
									sawCore = true
								}
								if row["layer"] == docstore.MemoryLayerLog && row["entry_id"] == "post-log-1" {
									sawLog = true
								}
							}
							if !sawCore || !sawLog {
								t.Fatalf("expected memory_search to return both core and post-cutoff log hits, got %+v", results)
							}
							verifiedTools["memory_search"] = true

						case "recall-memory-get-core":
							if verifiedTools["memory_get_core"] {
								continue
							}
							assertNoPreCutoffLeak("memory_get(core)", p.ToolResult.Output)
							mems, _ := p.ToolResult.Output["memories"].([]any)
							if p.ToolResult.Output["target"] != "core" || len(mems) != 6 {
								t.Fatalf("unexpected memory_get(core) output: %+v", p.ToolResult.Output)
							}
							verifiedTools["memory_get_core"] = true

						case "recall-memory-get-log":
							if verifiedTools["memory_get_log"] {
								continue
							}
							assertNoPreCutoffLeak("memory_get(log)", p.ToolResult.Output)
							entries, _ := p.ToolResult.Output["entries"].([]any)
							if p.ToolResult.Output["target"] != "log" || len(entries) != 1 {
								t.Fatalf("expected exactly 1 post-cutoff log entry from memory_get(log), got %+v", p.ToolResult.Output)
							}
							entry0, _ := entries[0].(map[string]any)
							if entry0["entry_id"] != "post-log-1" {
								t.Fatalf("expected post-log-1 from memory_get(log), got %+v", entry0)
							}
							verifiedTools["memory_get_log"] = true

						case "recall-conv-find":
							if verifiedTools["conversation_find"] {
								continue
							}
							assertNoPreCutoffLeak("conversation_find", p.ToolResult.Output)
							foundMsgs, _ := p.ToolResult.Output["messages"].([]any)
							if len(foundMsgs) != 1 {
								t.Fatalf("expected exactly 1 post-cutoff message from conversation_find, got %+v", foundMsgs)
							}
							row0, _ := foundMsgs[0].(map[string]any)
							if row0["message_id"] != postMsgID+"-user" {
								t.Fatalf("expected message_id=%s from conversation_find, got %+v", postMsgID+"-user", row0)
							}
							verifiedTools["conversation_find"] = true

						case "recall-conv-read":
							if verifiedTools["conversation_read"] {
								continue
							}
							assertNoPreCutoffLeak("conversation_read", p.ToolResult.Output)
							msgObj, _ := p.ToolResult.Output["message"].(map[string]any)
							if msgObj["id"] != postMsgID+"-user" || !strings.Contains(fmt.Sprint(msgObj["text"]), "aurora_post_cutoff") {
								t.Fatalf("unexpected conversation_read output: %+v", p.ToolResult.Output)
							}
							verifiedTools["conversation_read"] = true

						case "recall-op-read":
							if verifiedTools["operation_read"] {
								continue
							}
							if p.ToolResult.Output["read_operation_id"] != omittedOpID || p.ToolResult.Output["saved_outcome"] != "confirmed" {
								t.Fatalf("unexpected operation_read output: %+v", p.ToolResult.Output)
							}
							nested, _ := p.ToolResult.Output["output"].(map[string]any)
							if nested["target"] != "core" {
								t.Fatalf("expected operation_read to restore omitted memory_get(core) output, got %+v", nested)
							}
							verifiedTools["operation_read"] = true

						case "recall-task-history":
							if verifiedTools["task_history_read"] {
								continue
							}
							frags, _ := p.ToolResult.Output["fragments"].([]any)
							if len(frags) == 0 {
								t.Fatalf("expected non-empty fragments from task_history_read, got %+v", p.ToolResult.Output)
							}
							verifiedTools["task_history_read"] = true
						}
					}
					if msg.Role == transcript.RoleUser && p.Text != "" {
						if idx := strings.Index(p.Text, `{"before":`); idx >= 0 {
							var notice struct {
								HistoryRef string `json:"history_ref"`
								Before     string `json:"before"`
							}
							if json.Unmarshal([]byte(p.Text[idx:]), &notice) == nil && state.ValidHistoryRef(notice.HistoryRef) && notice.Before != "" {
								sawProjectContext = true
								archiveHistoryRef = notice.HistoryRef
								archiveBefore = notice.Before
							}
						}
					}
				}
			}

			switch phase {
			case "grow_fit":
				if sawFitContextOmission {
					phase = "grow_project"
				} else {
					growFitCalls++
					if growFitCalls > 20 {
						t.Fatalf("failed to trigger fitContext omission within 20 large memory_get calls")
					}
					return scriptedCall(fmt.Sprintf("fit-core-%d", growFitCalls), "memory_get", map[string]any{"target": "core"}, 0.01), nil, true
				}
				fallthrough

			case "grow_project":
				if sawProjectContext {
					phase = "recall"
				} else {
					growProjectCalls++
					if growProjectCalls > 16 {
						t.Fatalf("failed to trigger projectContext within 16 large model turns")
					}
					call := transcript.ToolCall{
						ID:   fmt.Sprintf("proj-read-%d", growProjectCalls),
						Name: inner.ToolNameRead,
						Args: map[string]any{"key": fmt.Sprintf("k-%d", growProjectCalls)},
					}
					// Attach ~16 KiB of non-omittable model text so fitted transcript exceeds 128 KiB and triggers projectContext.
					largeNote := fmt.Sprintf("Large model analysis block %d: ", growProjectCalls) + strings.Repeat("context_growth_segment_128k ", 600)
					msg := transcript.Message{
						Role: transcript.RoleModel,
						Parts: []transcript.Part{
							{Text: largeNote},
							{ToolCall: &call},
						},
					}
					return agent.Response{
						Message:   &msg,
						Calls:     []transcript.ToolCall{call},
						Cost:      0.01,
						CostKnown: true,
					}, nil, true
				}
				fallthrough

			default:
				recallStep++
				switch recallStep {
				case 1:
					return scriptedCall("recall-memory-read", "memory_read", map[string]any{}, 0.01), nil, true
				case 2:
					return scriptedCall("recall-memory-search", "memory_search", map[string]any{"query": "aurora", "layer": "", "limit": "10"}, 0.01), nil, true
				case 3:
					return scriptedCall("recall-memory-get-core", "memory_get", map[string]any{"target": "core"}, 0.01), nil, true
				case 4:
					return scriptedCall("recall-memory-get-log", "memory_get", map[string]any{"target": "log", "date": todayDate}, 0.01), nil, true
				case 5:
					return scriptedCall("recall-conv-find", "conversation_find", map[string]any{"query": "aurora"}, 0.01), nil, true
				case 6:
					return scriptedCall("recall-conv-read", "conversation_read", map[string]any{"message_id": postMsgID}, 0.01), nil, true
				case 7:
					return scriptedCall("recall-op-read", "operation_read", map[string]any{"operation_id": omittedOpID}, 0.01), nil, true
				case 8:
					return scriptedCall("recall-task-history", "task_history_read", map[string]any{
						"history_ref": archiveHistoryRef,
						"before":      archiveBefore,
						"offset":      "0",
					}, 0.01), nil, true
				default:
					// Emit a >600B answer to prove Publish bounds task_outcome Daily Log entries to <= 600 bytes.
					longAnswer := "Completed aurora_post_cutoff recall audit successfully: " + strings.Repeat("verified_milestone_alpha_outcome ", 28)
					return scriptedCall("recall-complete", "complete", map[string]any{
						"answer":   longAnswer,
						"evidence": []any{},
						"unmet":    []any{},
					}, 0.01), nil, true
				}
			}
		})

		run := h.startTask(ctx, "Audit aurora_post_cutoff memory, logs, and >128KiB transcript recall")
		h.stepUntilParkedOrDone(ctx, run.ID, 120)

		finalRun, err := h.store.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun final: %v", err)
		}
		if finalRun.Status != state.RunCompleted || finalRun.Error != "" {
			t.Fatalf("expected RunCompleted, got status=%s err=%q", finalRun.Status, finalRun.Error)
		}
		if !sawFitContextOmission {
			t.Fatalf("expected fitContext omission to trigger on >128 KiB transcript")
		}
		if !sawProjectContext {
			t.Fatalf("expected projectContext archive projection to trigger on >128 KiB transcript")
		}
		for _, tool := range []string{
			"memory_read", "memory_search", "memory_get_core", "memory_get_log",
			"conversation_find", "conversation_read", "operation_read", "task_history_read",
		} {
			if !verifiedTools[tool] {
				t.Fatalf("expected recall tool %q to be verified during task execution", tool)
			}
		}

		// 5. Verify Publish did NOT mutate Core Memory (INV-MEM-04).
		coreAfter, err := h.store.Memories(ctx)
		if err != nil {
			t.Fatalf("Memories after task: %v", err)
		}
		if len(coreAfter) != len(coreBefore) {
			t.Fatalf("task completion must not mutate Core Memory: before=%d after=%d", len(coreBefore), len(coreAfter))
		}
		for i := range coreBefore {
			if coreAfter[i].ID != coreBefore[i].ID || coreAfter[i].Text != coreBefore[i].Text {
				t.Fatalf("Core Memory[%d] mutated by task completion: before=%+v after=%+v", i, coreBefore[i], coreAfter[i])
			}
			if coreAfter[i].EffectiveProvenance() == state.ProvenanceTaskOutcome {
				t.Fatalf("INV-MEM-04 violation: task_outcome found in Core Memory: %+v", coreAfter[i])
			}
		}

		// 6. Verify Publish appended a bounded (<= 600 B) task_outcome entry to today's Daily Log & MemoryIndex.
		logDoc, err := h.store.MemoryLog(ctx, todayDate)
		if err != nil {
			t.Fatalf("MemoryLog(%s): %v", todayDate, err)
		}
		var taskEntry *state.MemoryLogEntry
		for i := range logDoc.Entries {
			e := &logDoc.Entries[i]
			if e.RunID == run.ID && e.Provenance == state.ProvenanceTaskOutcome {
				taskEntry = e
				break
			}
		}
		if taskEntry == nil {
			t.Fatalf("expected task_outcome entry for run %s in Daily Log %s, got %+v", run.ID, todayDate, logDoc.Entries)
		}
		if taskEntry.Section != "Completed Work" {
			t.Fatalf("expected task_outcome section 'Completed Work', got %q", taskEntry.Section)
		}
		if len(taskEntry.Text) == 0 || len(taskEntry.Text) > state.MaxTaskOutcomeBytes || !utf8.ValidString(taskEntry.Text) {
			t.Fatalf("expected valid UTF-8 task_outcome text <= %d bytes, got len=%d valid=%v text=%q",
				state.MaxTaskOutcomeBytes, len(taskEntry.Text), utf8.ValidString(taskEntry.Text), taskEntry.Text)
		}

		// Verify the new task_outcome entry is indexed in MemoryIndex.
		hits, err := h.store.SearchMemory(ctx, docstore.MemorySearchQuery{
			Query:        "verified_milestone_alpha_outcome",
			Layer:        docstore.MemoryLayerLog,
			CreatedAfter: cutoff,
			Limit:        5,
			Now:          h.clock.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("SearchMemory task_outcome: %v", err)
		}
		foundTaskOutcome := false
		for _, hit := range hits {
			if hit.Chunk.RunID == run.ID && hit.Chunk.Provenance == string(state.ProvenanceTaskOutcome) {
				foundTaskOutcome = true
				break
			}
		}
		if !foundTaskOutcome {
			t.Fatalf("expected task_outcome entry indexed in MemoryIndex, got hits=%+v", hits)
		}

		if err := h.driver.CheckInnerInvariants(ctx); err != nil {
			t.Fatalf("CheckInnerInvariants: %v", err)
		}
		return h.backend.Digest(), h.driver.Model.Digest()
	}

	b1, m1 := runScenario(707)
	b2, m2 := runScenario(707)
	if b1 != b2 || m1 != m2 {
		t.Fatalf("non-deterministic digests: backend %s vs %s, model %s vs %s", b1, b2, m1, m2)
	}
}
