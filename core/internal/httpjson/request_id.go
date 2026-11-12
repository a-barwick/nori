package httpjson

import "github.com/google/uuid"

// RequestID accepts only canonical UUIDs, so correlation logs cannot carry arbitrary input.
func RequestID(value string) string {
	id, err := uuid.Parse(value)
	if err != nil || id.String() != value {
		return ""
	}
	return value
}
