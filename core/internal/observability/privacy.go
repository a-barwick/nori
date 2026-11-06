package observability

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
)

// Init sets these from the process's settings.
var (
	sessionEnvironment = "development"
	captureContent     bool
	credentials        []string
)

// SessionID groups this single-owner application's AI spans without an email.
func SessionID() string {
	return "nori-" + sessionEnvironment + "-conversation"
}

var contentPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)https?://[^\s<>"']+`),
	regexp.MustCompile(`(?i)\b(?:cookies?|set-cookie)["']?\s*[:=]\s*[^\r\n]+`),
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)\b(?:authorization|password|passphrase|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|secret)["']?\s*[:=]\s*["']?[^\s"',;]+`),
	regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`),
	regexp.MustCompile(`\b(?:sk-(?:proj-)?[A-Za-z0-9_-]{16,}|AIza[A-Za-z0-9_-]{20,})\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
	regexp.MustCompile(`\b[A-Za-z0-9_+/=-]{128,}\b`),
}

// ContentAttribute is limited to explicitly approved model prompt and reply
// text. Redaction is best effort, not a guarantee that all personal data is
// removed; callers must never supply tool arguments or screenshots here.
func ContentAttribute(key, value string) (attribute.KeyValue, bool) {
	if !captureContent || (key != "input.value" && key != "output.value") {
		return attribute.KeyValue{}, false
	}
	return attribute.String(key, redactText(value, 2048)), true
}

func redactText(value string, limit int) string {
	// Cap work before running patterns and avoid retaining a split UTF-8 rune.
	if len(value) > 8192 {
		value = value[:8192]
	}
	value = strings.ToValidUTF8(value, "")
	for _, secret := range credentials {
		if len(secret) >= 8 {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	for _, pattern := range contentPatterns {
		value = pattern.ReplaceAllString(value, "[redacted]")
	}
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		value += "…"
	}
	return value
}
