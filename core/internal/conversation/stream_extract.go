package conversation

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// actionFreeStructuredPartial extracts the partial decoded "text" string from a
// streaming nori_response JSON object ONLY AFTER "actions" has been observed as
// a complete empty array ([]). If "actions" is missing, incomplete, or non-empty,
// it returns ("", false) so replies that might trigger commands or rejections
// never stream prematurely and raw JSON never leaks to clients.
func actionFreeStructuredPartial(raw string) (string, bool) {
	actionsDone, actionsEmpty := inspectTopLevelActions(raw)
	if !actionsDone || !actionsEmpty {
		return "", false
	}
	text, ok := extractTopLevelPartialString(raw, "text")
	if !ok {
		return "", false
	}
	return text, true
}

// inspectTopLevelActions scans a partial JSON object for top-level `"actions": [...]`.
// It returns (true, true) iff `"actions"` has been completely closed as an empty array `[]`,
// (true, false) if `"actions"` has at least one item, and (false, false) if not yet decided.
func inspectTopLevelActions(raw string) (decided bool, empty bool) {
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case '"':
			if depth == 1 && strings.HasPrefix(raw[i:], `"actions"`) {
				rest := strings.TrimSpace(raw[i+len(`"actions"`):])
				if !strings.HasPrefix(rest, ":") {
					return false, false
				}
				rest = strings.TrimSpace(rest[1:])
				if rest == "" {
					return false, false
				}
				if rest[0] != '[' {
					// Invalid/non-array actions -> never stream.
					return true, false
				}
				inner := strings.TrimSpace(rest[1:])
				if inner == "" {
					return false, false
				}
				if inner[0] == ']' {
					return true, true
				}
				// At least one element started inside actions -> do not stream.
				return true, false
			}
			inString = true
		}
	}
	return false, false
}

// extractTopLevelPartialString locates a top-level string field (e.g. "text") in a
// partial JSON object and decodes the longest valid UTF-8 prefix received so far.
func extractTopLevelPartialString(raw, key string) (string, bool) {
	quotedKey := `"` + key + `"`
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case '"':
			if depth == 1 && strings.HasPrefix(raw[i:], quotedKey) {
				rest := strings.TrimSpace(raw[i+len(quotedKey):])
				if !strings.HasPrefix(rest, ":") {
					return "", false
				}
				rest = strings.TrimSpace(rest[1:])
				if !strings.HasPrefix(rest, `"`) {
					return "", false
				}
				return decodePartialJSONString(rest)
			}
			inString = true
		}
	}
	return "", false
}

// decodePartialJSONString decodes a JSON string starting with '"' that may still be open.
func decodePartialJSONString(s string) (string, bool) {
	if len(s) < 2 || s[0] != '"' {
		return "", false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		ch := s[i]
		if ch == '"' {
			return b.String(), true
		}
		if ch == '\\' {
			if i+1 >= len(s) {
				break
			}
			next := s[i+1]
			switch next {
			case '"', '\\', '/':
				b.WriteByte(next)
				i++
			case 'b':
				b.WriteByte('\b')
				i++
			case 'f':
				b.WriteByte('\f')
				i++
			case 'n':
				b.WriteByte('\n')
				i++
			case 'r':
				b.WriteByte('\r')
				i++
			case 't':
				b.WriteByte('\t')
				i++
			case 'u':
				if i+6 > len(s) {
					return b.String(), b.Len() > 0
				}
				var decoded string
				if json.Unmarshal([]byte(`"`+s[i:i+6]+`"`), &decoded) == nil {
					b.WriteString(decoded)
					i += 5
				} else {
					return b.String(), b.Len() > 0
				}
			default:
				return b.String(), b.Len() > 0
			}
			continue
		}
		if ch < utf8.RuneSelf {
			b.WriteByte(ch)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			// Incomplete trailing multibyte UTF-8 sequence — wait for next chunk.
			break
		}
		b.WriteRune(r)
		i += size - 1
	}
	return b.String(), b.Len() > 0
}
