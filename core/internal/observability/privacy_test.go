package observability

import (
	"strings"
	"testing"
)

func TestRedactText_VercelAndAnthropicKeys(t *testing.T) {
	vck := "vck_0123456789abcdef_XYZ"
	ant := "sk-ant-api03-abcdef0123456789_XYZ"
	got := RedactText("vercel="+vck+" anthropic="+ant, 0)
	if strings.Contains(got, vck) {
		t.Fatalf("expected Vercel AI Gateway key to be redacted, got %q", got)
	}
	if strings.Contains(got, ant) {
		t.Fatalf("expected Anthropic key to be redacted, got %q", got)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("expected [redacted] marker, got %q", got)
	}
}
