package proactivity

import (
	"fmt"
	"regexp"
	"strings"

	"nori/internal/state"
)

const (
	// UntrustedProactiveOpenTag marks the start of external proactive content
	// entering the outer conversation loop as untrusted data.
	UntrustedProactiveOpenTag = "<untrusted_proactive_data>"
	// UntrustedProactiveCloseTag marks the end of an untrusted proactive data block.
	UntrustedProactiveCloseTag = "</untrusted_proactive_data>"
)

var (
	untrustedBlockRe = regexp.MustCompile(`(?is)<untrusted_proactive_data(?:\s+[^>]*)?>.*?</untrusted_proactive_data\s*>`)
	untrustedOpenRe  = regexp.MustCompile(`(?i)<untrusted_proactive_data(?:\s+[^>]*)?>`)
	untrustedCloseRe = regexp.MustCompile(`(?i)</untrusted_proactive_data\s*>`)
	safeSourceCharRe = regexp.MustCompile(`[^a-zA-Z0-9._:-]+`)
)

// WrapUntrustedProactiveData wraps external brief or watch content in
// <untrusted_proactive_data> tags so the outer model treats it strictly as
// data and never as user instructions. Any embedded closing/opening tags inside
// content are neutralized so untrusted input cannot break out of the fence.
func WrapUntrustedProactiveData(source, content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}
	if IsUntrustedProactiveData(trimmed) && isAlreadyWrapped(trimmed) {
		return trimmed
	}
	safe := neutralizeProactiveTags(trimmed)
	cleanSource := sanitizeSourceAttr(source)
	if cleanSource != "" {
		return fmt.Sprintf("<untrusted_proactive_data source=%q>\n%s\n%s", cleanSource, safe, UntrustedProactiveCloseTag)
	}
	return UntrustedProactiveOpenTag + "\n" + safe + "\n" + UntrustedProactiveCloseTag
}

func isAlreadyWrapped(trimmed string) bool {
	openLoc := untrustedOpenRe.FindStringIndex(trimmed)
	closeLoc := untrustedCloseRe.FindStringIndex(trimmed)
	if openLoc == nil || closeLoc == nil || openLoc[0] != 0 || closeLoc[1] != len(trimmed) {
		return false
	}
	// Ensure there are no nested/unmatched tags inside the outer wrapper.
	inner := trimmed[openLoc[1]:closeLoc[0]]
	return !untrustedOpenRe.MatchString(inner) && !untrustedCloseRe.MatchString(inner)
}

func neutralizeProactiveTags(s string) string {
	s = untrustedCloseRe.ReplaceAllString(s, "&lt;/untrusted_proactive_data&gt;")
	s = untrustedOpenRe.ReplaceAllString(s, "&lt;untrusted_proactive_data&gt;")
	return s
}

func sanitizeSourceAttr(source string) string {
	s := strings.TrimSpace(source)
	if s == "" {
		return ""
	}
	s = safeSourceCharRe.ReplaceAllString(s, "_")
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.Trim(s, "_")
}

// IsUntrustedProactiveData reports whether text contains an
// <untrusted_proactive_data> tag (open or close).
func IsUntrustedProactiveData(text string) bool {
	if text == "" {
		return false
	}
	return untrustedOpenRe.MatchString(text) || untrustedCloseRe.MatchString(text)
}

// StripUntrustedProactiveData removes all <untrusted_proactive_data> blocks
// (and any unclosed trailing block) so tainted proactive content is excluded
// from memory extraction and AutoDream.
func StripUntrustedProactiveData(text string) string {
	if !IsUntrustedProactiveData(text) {
		return text
	}
	cleaned := untrustedBlockRe.ReplaceAllString(text, "")
	if loc := untrustedOpenRe.FindStringIndex(cleaned); loc != nil {
		cleaned = cleaned[:loc[0]]
	}
	cleaned = untrustedCloseRe.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(cleaned)
}

// HasBriefPart reports whether parts contains at least one state.PartBrief.
func HasBriefPart(parts []state.Part) bool {
	for _, p := range parts {
		if p.Type == state.PartBrief {
			return true
		}
	}
	return false
}

// RenderBriefParts formats a message's PartBrief entries into plain text
// suitable for wrapping as untrusted proactive data in the outer model context.
func RenderBriefParts(parts []state.Part, fallbackText string) string {
	var b strings.Builder
	wroteBrief := false
	for _, p := range parts {
		if p.Type != state.PartBrief {
			continue
		}
		wroteBrief = true
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if g := strings.TrimSpace(p.Greeting); g != "" {
			b.WriteString(g)
			b.WriteByte('\n')
		}
		if m := strings.TrimSpace(p.Meta); m != "" {
			b.WriteString(m)
			b.WriteByte('\n')
		}
		if t := strings.TrimSpace(p.Title); t != "" {
			b.WriteString(t)
			b.WriteByte('\n')
		}
		if txt := strings.TrimSpace(p.Text); txt != "" {
			b.WriteString(txt)
			b.WriteByte('\n')
		}
		for _, item := range p.Items {
			title := strings.TrimSpace(item.Title)
			if title == "" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(title)
			if tr := strings.TrimSpace(item.Trailing); tr != "" {
				b.WriteString(" (")
				b.WriteString(tr)
				b.WriteString(")")
			}
			b.WriteByte('\n')
		}
	}
	if !wroteBrief {
		return strings.TrimSpace(fallbackText)
	}
	return strings.TrimSpace(b.String())
}

// IsProactiveEvent reports whether a task event carries proactive brief/watch
// content from external sources that must be tagged as untrusted data and
// excluded from memory extraction.
func IsProactiveEvent(ev *state.TaskEvent) bool {
	if ev == nil {
		return false
	}
	if HasBriefPart(ev.Parts) || IsUntrustedProactiveData(ev.Text) {
		return true
	}
	kind := strings.ToLower(strings.TrimSpace(ev.Kind))
	if strings.HasPrefix(kind, "proactive_") || strings.HasPrefix(kind, "scheduler_") || strings.HasPrefix(kind, "automation_") {
		return true
	}
	switch kind {
	case "brief", "morning_brief", "proactive_brief", "proactive_watch", "proactive_alert", "source_watch", "watch_alert", "eod_wrap", "pre_meeting_prep":
		return true
	}
	return false
}

// ProactiveEventSource returns a concise source label for an untrusted
// proactive task event.
func ProactiveEventSource(ev *state.TaskEvent) string {
	if ev == nil {
		return "proactive"
	}
	if HasBriefPart(ev.Parts) {
		return "brief"
	}
	if k := sanitizeSourceAttr(strings.ToLower(ev.Kind)); k != "" {
		return k
	}
	return "proactive"
}

// IsBriefMessage reports whether a conversation message represents a proactive
// brief (e.g. a Morning Brief with PartBrief).
func IsBriefMessage(m state.Message) bool {
	return HasBriefPart(m.Parts)
}

// IsBriefTurn reports whether a conversation turn is a non-user proactive brief
// turn that should be treated as low-priority during compaction.
func IsBriefTurn(t state.ConversationTurn) bool {
	for _, in := range t.Inputs {
		if in.IsUser() {
			return false
		}
	}
	for _, in := range t.Inputs {
		if in.Event != nil && (HasBriefPart(in.Event.Parts) || strings.Contains(strings.ToLower(in.Event.Kind), "brief")) {
			return true
		}
	}
	for _, raw := range t.Protocol {
		if strings.Contains(raw, `<untrusted_proactive_data source="brief"`) ||
			strings.Contains(raw, `"type":"brief"`) {
			return true
		}
	}
	return false
}
