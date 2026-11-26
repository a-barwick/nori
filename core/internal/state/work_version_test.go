package state

import "testing"

func TestWorkVersionRoundTripAndClassification(t *testing.T) {
	taskOnly := []struct {
		kind  WorkVersionKind
		parts []string
		want  string
	}{
		{WorkVersionFinishing, []string{"run-1"}, "finishing:run-1"},
		{WorkVersionStop, []string{"run-1"}, "stop:run-1"},
		{WorkVersionCommand, []string{"cmd-1"}, "command:cmd-1"},
		{WorkVersionPublication, []string{"pub-1"}, "publication:pub-1"},
		{WorkVersionCleanup, []string{"run-1"}, "cleanup:run-1"},
	}
	for _, tc := range taskOnly {
		got := FormatWorkVersion(tc.kind, tc.parts...)
		if got != tc.want {
			t.Fatalf("FormatWorkVersion(%q, %v) = %q, want %q", tc.kind, tc.parts, got, tc.want)
		}
		kind, ok := ParseWorkVersion(got)
		if !ok || kind != tc.kind {
			t.Fatalf("ParseWorkVersion(%q) = (%q, %v), want (%q, true)", got, kind, ok, tc.kind)
		}
		if !IsTaskOnlyWorkVersion(got) {
			t.Fatalf("IsTaskOnlyWorkVersion(%q) = false, want true", got)
		}
	}

	assistant := FormatWorkVersion(WorkVersionAssistant, "turn-1", "1")
	if assistant != "assistant:turn-1:1" {
		t.Fatalf("FormatWorkVersion assistant = %q", assistant)
	}
	if IsTaskOnlyWorkVersion(assistant) {
		t.Fatalf("IsTaskOnlyWorkVersion(%q) = true, want false", assistant)
	}
	if _, ok := ParseWorkVersion("unknown:123"); ok {
		t.Fatalf("expected unknown prefix not to parse")
	}
	if IsTaskOnlyWorkVersion("no-colon") {
		t.Fatalf("expected malformed version not to be task-only")
	}
}
