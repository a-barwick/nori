package state

import "strings"

// WorkVersionKind identifies the category of pending work represented by a WorkVersion token.
type WorkVersionKind string

const (
	WorkVersionAssistant   WorkVersionKind = "assistant"
	WorkVersionFinishing   WorkVersionKind = "finishing"
	WorkVersionStop        WorkVersionKind = "stop"
	WorkVersionCommand     WorkVersionKind = "command"
	WorkVersionPublication WorkVersionKind = "publication"
	WorkVersionCleanup     WorkVersionKind = "cleanup"
)

// FormatWorkVersion encodes a structured WorkVersion token with its kind prefix.
func FormatWorkVersion(kind WorkVersionKind, parts ...string) string {
	if len(parts) == 0 {
		return string(kind)
	}
	return string(kind) + ":" + strings.Join(parts, ":")
}

// ParseWorkVersion extracts the typed WorkVersionKind from a WorkVersion token.
func ParseWorkVersion(version string) (WorkVersionKind, bool) {
	prefix, _, ok := strings.Cut(version, ":")
	if !ok {
		return "", false
	}
	switch kind := WorkVersionKind(prefix); kind {
	case WorkVersionAssistant, WorkVersionFinishing, WorkVersionStop, WorkVersionCommand, WorkVersionPublication, WorkVersionCleanup:
		return kind, true
	default:
		return "", false
	}
}

// IsTaskOnlyWorkVersion reports whether version represents task-lane-only pending work
// that does not require waking the conversation lane.
func IsTaskOnlyWorkVersion(version string) bool {
	kind, ok := ParseWorkVersion(version)
	return ok && kind != WorkVersionAssistant
}
