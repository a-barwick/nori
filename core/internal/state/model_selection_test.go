package state

import (
	"testing"
	"time"
)

func TestInputBatchSplitsOnModelSelectionBoundary(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	selA := &ModelSelection{ChatProfileID: "local-inkling-2-mini", ChatProfileVersion: "v1"}
	selB := &ModelSelection{ChatProfileID: "cloud-claude-haiku-5-5", ChatProfileVersion: "v1", ChatReasoning: "low"}

	b := NewInputBatch()
	if !b.Add(ConversationInput{ID: "in-1", Text: "first", Position: 1, CreatedAt: now, ModelSelection: selA}) {
		t.Fatalf("expected first input to be accepted")
	}
	if !b.Add(ConversationInput{ID: "in-2", Text: "second same model", Position: 2, CreatedAt: now, ModelSelection: selA.Clone()}) {
		t.Fatalf("expected same-selection input to batch together")
	}
	if b.Add(ConversationInput{ID: "in-3", Text: "third different model", Position: 3, CreatedAt: now, ModelSelection: selB}) {
		t.Fatalf("expected different model selection to split the batch")
	}
	if len(b.Inputs) != 2 {
		t.Fatalf("expected 2 inputs in batch, got %d", len(b.Inputs))
	}

	// Default (nil) vs explicit selection also splits.
	b2 := NewInputBatch()
	if !b2.Add(ConversationInput{ID: "in-4", Text: "default", Position: 4, CreatedAt: now}) {
		t.Fatalf("expected default input to be accepted")
	}
	if b2.Add(ConversationInput{ID: "in-5", Text: "explicit", Position: 5, CreatedAt: now, ModelSelection: selB}) {
		t.Fatalf("expected explicit selection after default to split the batch")
	}

	// NewTurn copies the batch's selection and freezes when newer input remains.
	ctrl := ConversationControl{Received: 3, UserReceived: 3}
	turn := NewTurn("turn-1", b.Inputs, nil, ctrl, now)
	if !ModelSelectionEqual(turn.ModelSelection, selA) {
		t.Fatalf("expected turn.ModelSelection=%+v, got %+v", selA, turn.ModelSelection)
	}
	if !turn.Frozen {
		t.Fatalf("expected turn to be frozen because input 3 remains queued")
	}
}

func TestReplayUserInputChecksModelSelection(t *testing.T) {
	selA := &ModelSelection{ChatProfileID: "local-inkling-2-mini", ChatProfileVersion: "v1"}
	selB := &ModelSelection{ChatProfileID: "cloud-claude-haiku-5-5", ChatProfileVersion: "v1"}
	saved := ConversationInput{ID: "in-1", Text: "hello", ModelSelection: selA}

	if err := ReplayUserInputWithModel(saved, "hello", nil, selA.Clone()); err != nil {
		t.Fatalf("expected identical replay to succeed, got %v", err)
	}
	if err := ReplayUserInputWithModel(saved, "hello", nil, selB); err != ErrConversationConflict {
		t.Fatalf("expected ErrConversationConflict on different selection, got %v", err)
	}
}

func TestCommandStartPinsAndInheritsSubtaskModelSelection(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	taskSel := &ModelSelection{TaskProfileID: "cloud-gemini-4-neon", TaskProfileVersion: "v1", TaskReasoning: "high"}

	batch := NewCommandBatch()
	startCmd := Command{
		ID: "cmd-1", RunID: "run-parent", Kind: CommandStart, Text: "Parent task",
		ModelSelection: taskSel,
	}
	prep, err := batch.Prepare([]Command{startCmd}, Control{}, now, nil, 1.0)
	if err != nil {
		t.Fatalf("Prepare parent start: %v", err)
	}
	parent := batch.Runs["run-parent"]
	if !ModelSelectionEqual(parent.ModelSelection, taskSel) {
		t.Fatalf("expected parent run to pin %+v, got %+v", taskSel, parent.ModelSelection)
	}
	if len(prep.Receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(prep.Receipts))
	}

	// Park parent on a work question and dispatch a subtask without explicit selection -> inherits parent's TaskOnly selection.
	parent.Question = &Question{ID: "q-1", Kind: QuestionWork, Text: "Do child work", Revision: parent.Revision}
	batch2 := NewCommandBatch()
	batch2.Runs["run-parent"] = parent
	batch2.Exists["run-parent"] = true
	childCmd := Command{
		ID: "cmd-2", RunID: "run-child", ParentID: "run-parent", QuestionID: "q-1",
		Kind: CommandStart, Text: "Child task",
	}
	if _, err := batch2.Prepare([]Command{childCmd}, Control{}, now, nil, 1.0); err != nil {
		t.Fatalf("Prepare child start: %v", err)
	}
	child := batch2.Runs["run-child"]
	if !ModelSelectionEqual(child.ModelSelection, taskSel.TaskOnly()) {
		t.Fatalf("expected child subtask to inherit %+v, got %+v", taskSel.TaskOnly(), child.ModelSelection)
	}
}
