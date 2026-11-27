package state

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxTaskTitleLen = 72

var (
	parenMetaRe      = regexp.MustCompile(`(?i)\s*\([^)]*(?:user['’]?s?\s+words?|thanksgiving|today\s+is|pacific|e\.g\.)[^)]*\)`)
	userAppositiveRe = regexp.MustCompile(`(?i),\s+the\s+user['’]s\s+[^.!?\n]+`)
	multiSpaceRe     = regexp.MustCompile(`\s+`)
)

// TaskTitle returns a concise, human-readable label for a run, preferring an
// explicit run.Title and falling back to a cleaned first sentence of run.Request.
func TaskTitle(run Run) string {
	if t := strings.Join(strings.Fields(strings.TrimSpace(run.Title)), " "); t != "" {
		return clipTitle(t, maxTaskTitleLen)
	}
	return TitleFromRequest(run.Request)
}

// ShortTaskTitle derives a concise plain-English task title from a raw delegation
// request string when no explicit title was provided.
func ShortTaskTitle(request string) string {
	return TitleFromRequest(request)
}

// TitleFromRequest derives a short display title from an internal task request,
// stripping multi-sentence instructions and third-person prompt phrasing.
func TitleFromRequest(request string) string {
	s := strings.TrimSpace(request)
	if s == "" {
		return "Task"
	}
	if nl := strings.IndexAny(s, "\r\n"); nl >= 0 {
		s = strings.TrimSpace(s[:nl])
	}
	s = parenMetaRe.ReplaceAllString(s, "")
	s = firstSentence(s)
	s = userAppositiveRe.ReplaceAllString(s, "")

	replacements := []struct {
		old string
		new string
	}{
		{"Set a reminder for the user to ", "Remind to "},
		{"set a reminder for the user to ", "Remind to "},
		{"Create a reminder for the user to ", "Remind to "},
		{"for the user to ", "to "},
		{"email the user ", "email "},
		{"Email the user ", "Email "},
		{"remind the user to ", "Remind to "},
		{"Remind the user to ", "Remind to "},
		{"the user's ", ""},
		{"The user's ", ""},
		{"the user ", ""},
		{` "later"`, ""},
		{` 'later'`, ""},
	}
	for _, r := range replacements {
		s = strings.ReplaceAll(s, r.old, r.new)
	}
	s = multiSpaceRe.ReplaceAllString(strings.TrimSpace(s), " ")
	s = strings.TrimRight(s, " .,:;-")
	if s == "" {
		return "Task"
	}
	r, size := utf8.DecodeRuneInString(s)
	if r != utf8.RuneError && unicode.IsLower(r) {
		s = string(unicode.ToUpper(r)) + s[size:]
	}
	return clipTitle(s, maxTaskTitleLen)
}

func firstSentence(s string) string {
	for i := 0; i < len(s)-1; i++ {
		if (s[i] == '.' || s[i] == '?' || s[i] == '!') && s[i+1] == ' ' {
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

func clipTitle(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if sp := strings.LastIndexByte(s[:cut], ' '); sp > limit/2 {
		cut = sp
	}
	return strings.TrimRight(s[:cut], " .,:;-") + "…"
}
