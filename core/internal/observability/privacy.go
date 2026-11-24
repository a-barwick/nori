package observability

import (
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
)

var (
	privacyMu          sync.RWMutex
	sessionEnvironment = "development"
	captureContent     bool
	credentials        []string
)

func setPrivacyConfig(environment string, capture bool, creds []string) {
	privacyMu.Lock()
	defer privacyMu.Unlock()
	sessionEnvironment = environment
	captureContent = capture
	next := make([]string, 0, len(creds))
	for _, s := range creds {
		if len(s) >= 8 && !slices.Contains(next, s) {
			next = append(next, s)
		}
	}
	credentials = next
}

func RegisterCredentials(secrets ...string) {
	privacyMu.Lock()
	defer privacyMu.Unlock()
	next := append([]string(nil), credentials...)
	for _, s := range secrets {
		if len(s) >= 8 && !slices.Contains(next, s) {
			next = append(next, s)
		}
	}
	credentials = next
}

func SessionID() string {
	privacyMu.RLock()
	env := sessionEnvironment
	privacyMu.RUnlock()
	return "nori-" + env + "-conversation"
}

var contentPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(?:https?|postgres(?:ql)?|rediss?|amqps?)://[^\s<>"']+`),
	regexp.MustCompile(`(?i)\b(?:cookies?|set-cookie)["']?\s*[:=]\s*[^\r\n]+`),
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)\b(?:authorization|password|passphrase|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|secret)["']?\s*[:=]\s*["']?[^\s"',;]+`),
	regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`),
	regexp.MustCompile(`\b(?:sk-(?:proj-)?[A-Za-z0-9_-]{16,}|AIza[A-Za-z0-9_-]{20,})\b`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,})\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b`),
	regexp.MustCompile(`\b[A-Za-z0-9_+/=-]{128,}\b`),
}

func ContentAttribute(key, value string) (attribute.KeyValue, bool) {
	privacyMu.RLock()
	enabled := captureContent
	privacyMu.RUnlock()
	if !enabled || (key != "input.value" && key != "output.value") {
		return attribute.KeyValue{}, false
	}
	return attribute.String(key, redactText(value, 2048)), true
}

func RedactText(value string, limit int) string {
	return redactText(value, limit)
}

func redactText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "")
	privacyMu.RLock()
	creds := credentials
	privacyMu.RUnlock()
	for _, secret := range creds {
		if len(secret) >= 8 {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	for _, pattern := range contentPatterns {
		value = pattern.ReplaceAllString(value, "[redacted]")
	}
	maxLen := 8192
	if limit > 0 && limit < maxLen {
		maxLen = limit
	}
	if len(value) > maxLen {
		value = value[:maxLen]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
		value += "…"
	}
	return value
}
