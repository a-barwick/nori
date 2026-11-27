package task

import (
	"testing"

	"nori/internal/agent"
	"nori/internal/transcript"
)

func TestProposalFiltersNoopUnmetSentinels(t *testing.T) {
	call := transcript.ToolCall{
		ID:   "c1",
		Name: "complete",
		Args: map[string]any{
			"answer":   "All done.",
			"evidence": []any{"", "  op_1  "},
			"unmet":    []any{"", "  ", "None", "none.", "N/A", "Nothing", "All requirements met."},
		},
	}
	got := proposal(call)
	if len(got.Unmet) != 0 {
		t.Fatalf("Unmet = %#v, want empty for sentinel values", got.Unmet)
	}
	if len(got.Evidence) != 1 || got.Evidence[0] != "op_1" {
		t.Fatalf("Evidence = %#v, want [op_1]", got.Evidence)
	}

	real := proposal(transcript.ToolCall{
		ID:   "c2",
		Name: "complete",
		Args: map[string]any{
			"answer": "Partial answer.",
			"unmet":  []any{"None", "Could not export PDF"},
		},
	})
	if len(real.Unmet) != 1 || real.Unmet[0] != "Could not export PDF" {
		t.Fatalf("real Unmet = %#v, want [Could not export PDF]", real.Unmet)
	}
}

func TestValidateCompleteAllowsOptionalEvidenceAndUnmet(t *testing.T) {
	calls := []transcript.ToolCall{{
		ID:   "c1",
		Name: "complete",
		Args: map[string]any{"answer": "Done."},
	}}
	if err := agent.ValidateCalls(calls, false, false, nil); err != nil {
		t.Fatalf("ValidateCalls with omitted evidence/unmet returned error: %v", err)
	}
}
