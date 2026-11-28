package state

import (
	"regexp"
	"strings"
)

const (
	PartText       = "text"
	PartPage       = "page"
	PartAttachment = "attachment"
	PartArtifact   = "artifact"
	PartImage      = "image"
	PartLink       = "link"
	PartReminder   = "reminder"
	PartBrief      = "brief"
)

type BriefItem struct {
	Title         string `json:"title" firestore:"title"`
	Trailing      string `json:"trailing,omitempty" firestore:"trailing,omitempty"`
	InteractionID string `json:"interaction_id,omitempty" firestore:"interaction_id,omitempty"`
	ActionLabel   string `json:"action_label,omitempty" firestore:"action_label,omitempty"`
}

type Part struct {
	Type        string      `json:"type" firestore:"type"`
	Title       string      `json:"title,omitempty" firestore:"title,omitempty"`
	Text        string      `json:"text,omitempty" firestore:"text,omitempty"`
	Display     string      `json:"display,omitempty" firestore:"display,omitempty"`
	Fallback    string      `json:"fallback,omitempty" firestore:"fallback,omitempty"`
	ID          string      `json:"id,omitempty" firestore:"id,omitempty"`
	Name        string      `json:"name,omitempty" firestore:"name,omitempty"`
	MediaType   string      `json:"media_type,omitempty" firestore:"media_type,omitempty"`
	Size        int64       `json:"size,omitempty" firestore:"size,omitempty"`
	URL         string      `json:"url,omitempty" firestore:"url,omitempty"`
	ScheduledAt string      `json:"scheduled_at,omitempty" firestore:"scheduled_at,omitempty"`
	Greeting    string      `json:"greeting,omitempty" firestore:"greeting,omitempty"`
	Meta        string      `json:"meta,omitempty" firestore:"meta,omitempty"`
	Items       []BriefItem `json:"items,omitempty" firestore:"items,omitempty"`
}

const (
	defaultFallbackNote = "Here's the full write-up."
)

var (
	replyHint           = regexp.MustCompile(`<!--\s*nori:([a-z]+)[\s\S]*?-->`)
	unfinishedComment   = regexp.MustCompile(`<!--[\s\S]*$`)
	cardOpenTagRe       = regexp.MustCompile(`(?i)<card(?:\s+[^>]*?)?>`)
	cardCloseTagRe      = regexp.MustCompile(`(?i)</card\s*>`)
	cardTitleAttrRe     = regexp.MustCompile(`(?i)\btitle\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	thematicBreakRe     = regexp.MustCompile(`^\s*(?:[-*_]\s*){3,}$`)
	headingLineRe       = regexp.MustCompile(`^#{1,6}\s+\S`)
	listItemLineRe      = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+`)
	nestedListLineRe    = regexp.MustCompile(`^(?: {2,}|\t)(?:[-*+]|\d+[.)])\s+`)
	tableSepLineRe      = regexp.MustCompile(`^\s*\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)+\|?\s*$`)
	conversationalStart = regexp.MustCompile(`(?i)^(?:want\b|would you\b|should i\b|do you want\b|let me know\b|also,\s|by the way\b|quick note\b|happy to\b|if you(?:'d| would)? like\b|if you want\b|feel free\b)`)
	sentenceEndRe       = regexp.MustCompile(`^[^\n]*?[.!?](?:\s|$)`)
)

type byteSpan struct{ start, end int }

// markdownCodeSpans returns byte ranges [start, end) covering fenced code
// blocks (``` / ~~~) and inline backtick code spans (`...`, “...“, etc.).
// When allowUnclosedInline is true (used while streaming), an unclosed inline
// backtick run extends to the next blank line (\n\n) or end of input so
// partial inline code like "`<card" is not stripped mid-stream.
func markdownCodeSpans(s string, allowUnclosedInline bool) []byteSpan {
	var spans []byteSpan
	var fenced []byteSpan

	// 1. Collect fenced code blocks.
	pos := 0
	inFence := false
	fenceChar := byte(0)
	fenceLen := 0
	fenceStart := 0
	for pos <= len(s) {
		nl := strings.IndexByte(s[pos:], '\n')
		var line string
		var nextPos int
		if nl < 0 {
			line = s[pos:]
			nextPos = len(s) + 1
		} else {
			line = s[pos : pos+nl]
			nextPos = pos + nl + 1
		}
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
				ch := trimmed[0]
				n := 0
				for n < len(trimmed) && trimmed[n] == ch {
					n++
				}
				if n >= 3 && (ch == '~' || !strings.ContainsRune(trimmed[n:], '`')) {
					inFence = true
					fenceChar = ch
					fenceLen = n
					fenceStart = pos
				}
			}
		} else if len(trimmed) >= fenceLen && trimmed[0] == fenceChar {
			n := 0
			for n < len(trimmed) && trimmed[n] == fenceChar {
				n++
			}
			if n >= fenceLen && strings.TrimSpace(trimmed[n:]) == "" {
				inFence = false
				fenced = append(fenced, byteSpan{start: fenceStart, end: min(nextPos, len(s))})
			}
		}
		pos = nextPos
	}
	if inFence {
		fenced = append(fenced, byteSpan{start: fenceStart, end: len(s)})
	}
	spans = append(spans, fenced...)

	// 2. Collect inline backtick code spans outside fenced code blocks.
	inFenced := func(offset int) (int, bool) {
		for _, sp := range fenced {
			if offset >= sp.start && offset < sp.end {
				return sp.end, true
			}
		}
		return 0, false
	}

	i := 0
	for i < len(s) {
		if end, ok := inFenced(i); ok {
			i = end
			continue
		}
		if s[i] != '`' {
			i++
			continue
		}
		openStart := i
		for i < len(s) && s[i] == '`' {
			i++
		}
		ticks := i - openStart

		// Search for a matching closing run of exact length `ticks` outside fences.
		j := i
		closed := false
		for j < len(s) {
			if end, ok := inFenced(j); ok {
				j = end
				continue
			}
			if s[j] != '`' {
				j++
				continue
			}
			runStart := j
			for j < len(s) && s[j] == '`' {
				j++
			}
			if j-runStart == ticks {
				spans = append(spans, byteSpan{start: openStart, end: j})
				i = j
				closed = true
				break
			}
		}
		if !closed && allowUnclosedInline {
			end := len(s)
			if blank := strings.Index(s[i:], "\n\n"); blank >= 0 {
				end = i + blank
			}
			for _, sp := range fenced {
				if sp.start >= i && sp.start < end {
					end = sp.start
				}
			}
			spans = append(spans, byteSpan{start: openStart, end: end})
			i = end
		}
	}
	return spans
}

func offsetInSpans(offset int, spans []byteSpan) bool {
	for _, sp := range spans {
		if offset >= sp.start && offset < sp.end {
			return true
		}
	}
	return false
}

func isLineStart(s string, offset int) bool {
	lineStart := strings.LastIndexByte(s[:offset], '\n') + 1
	return strings.TrimSpace(s[lineStart:offset]) == ""
}

func stripCardTagsOutsideCode(s string, allowUnclosedInline bool) string {
	if s == "" || !strings.Contains(strings.ToLower(s), "card") {
		return s
	}
	spans := markdownCodeSpans(s, allowUnclosedInline)
	var b strings.Builder
	b.Grow(len(s))
	cursor := 0
	for cursor < len(s) {
		openLoc := cardOpenTagRe.FindStringIndex(s[cursor:])
		closeLoc := cardCloseTagRe.FindStringIndex(s[cursor:])
		var loc []int
		if openLoc != nil && (closeLoc == nil || openLoc[0] <= closeLoc[0]) {
			loc = openLoc
		} else if closeLoc != nil {
			loc = closeLoc
		}
		if loc == nil {
			b.WriteString(s[cursor:])
			break
		}
		matchStart := cursor + loc[0]
		matchEnd := cursor + loc[1]
		if offsetInSpans(matchStart, spans) {
			b.WriteString(s[cursor:matchEnd])
		} else {
			b.WriteString(s[cursor:matchStart])
		}
		cursor = matchEnd
	}
	return b.String()
}

// StripCardMarkupForStream removes <card ...> / </card> tags outside code
// blocks and inline code spans, and holds back any incomplete trailing card
// tag prefix so raw markup never flashes mid-stream.
func StripCardMarkupForStream(s string) string {
	if s == "" {
		return ""
	}
	cleaned := stripCardTagsOutsideCode(s, true)
	if idx := strings.LastIndexByte(cleaned, '<'); idx >= 0 {
		tail := strings.ToLower(cleaned[idx:])
		if !strings.Contains(tail, ">") && !offsetInSpans(idx, markdownCodeSpans(cleaned, true)) {
			if strings.HasPrefix("<card", tail) ||
				strings.HasPrefix("</card", tail) ||
				strings.HasPrefix(tail, "<card ") ||
				strings.HasPrefix(tail, "<card\t") ||
				strings.HasPrefix(tail, "<card\n") ||
				strings.HasPrefix(tail, "</card") {
				cleaned = cleaned[:idx]
			}
		}
	}
	return strings.TrimRight(cleaned, " \t")
}

// parseCardBlocks extracts explicit <card title="...">...</card> blocks outside
// fenced code blocks and inline code spans, preserving surrounding
// conversational text in order. Returns (parts, true) when at least one card
// block was found.
func parseCardBlocks(markdown string) ([]Part, bool) {
	if !cardOpenTagRe.MatchString(markdown) {
		return nil, false
	}

	codeSpans := markdownCodeSpans(markdown, false)
	inCode := func(offset int) bool {
		return offsetInSpans(offset, codeSpans)
	}

	var out []Part
	cursor := 0
	found := false
	for cursor < len(markdown) {
		openStart, openEnd := -1, -1
		searchFrom := cursor
		for searchFrom < len(markdown) {
			loc := cardOpenTagRe.FindStringIndex(markdown[searchFrom:])
			if loc == nil {
				break
			}
			os := searchFrom + loc[0]
			oe := searchFrom + loc[1]
			if !inCode(os) {
				openStart, openEnd = os, oe
				break
			}
			searchFrom = oe
		}
		if openStart < 0 {
			break
		}

		openTag := markdown[openStart:openEnd]
		title := ""
		if m := cardTitleAttrRe.FindStringSubmatch(openTag); len(m) == 3 {
			if m[1] != "" {
				title = strings.TrimSpace(m[1])
			} else {
				title = strings.TrimSpace(m[2])
			}
		}

		// Find matching </card> outside code spans/fences.
		closeStart, closeEnd := -1, -1
		searchClose := openEnd
		for searchClose < len(markdown) {
			cl := cardCloseTagRe.FindStringIndex(markdown[searchClose:])
			if cl == nil {
				break
			}
			cs := searchClose + cl[0]
			ce := searchClose + cl[1]
			if !inCode(cs) {
				closeStart, closeEnd = cs, ce
				break
			}
			searchClose = ce
		}

		// Without a closing </card>, only treat an opening tag as a truncated
		// card if it has a title attribute or starts its own line; ignore
		// stray inline mentions like "the <card> flow".
		if closeStart < 0 && title == "" && !isLineStart(markdown, openStart) {
			break
		}

		if lead := strings.TrimSpace(markdown[cursor:openStart]); lead != "" {
			out = append(out, Part{Type: PartText, Text: lead})
		}

		var body string
		if closeStart >= 0 {
			body = strings.TrimSpace(markdown[openEnd:closeStart])
			cursor = closeEnd
		} else {
			// Gracefully handle truncated replies that omitted </card>.
			body = strings.TrimSpace(markdown[openEnd:])
			cursor = len(markdown)
		}

		if body != "" || title != "" {
			if title == "" {
				title = DocumentTitle(body, "")
				if title == "" {
					title = clipBytes(PlainExcerpt(body, 64), 64)
				}
			}
			out = append(out, Part{Type: PartPage, Display: "page", Title: title, Text: body})
			found = true
		}
	}

	if !found {
		return nil, false
	}
	if tail := strings.TrimSpace(markdown[cursor:]); tail != "" {
		out = append(out, Part{Type: PartText, Text: tail})
	}
	return out, true
}

// TextParts parses a reply's optional <!-- nori:page|bubble --> hint and, when
// the reply contains a structured or long-form document, splits it into ordered
// parts: optional leading conversational text, a PartPage document, and
// optional trailing conversational follow-up text.
func TextParts(text string) []Part {
	display := ""
	stripped := replyHint.ReplaceAllStringFunc(text, func(match string) string {
		if name := replyHint.FindStringSubmatch(match)[1]; name == "page" || name == "bubble" {
			display = name
		}
		return ""
	})
	clean := strings.TrimSpace(unfinishedComment.ReplaceAllString(stripped, ""))
	return segmentTextParts(clean, display)
}

func (m Message) WithParts() Message {
	if len(m.Parts) == 0 {
		if m.Role == "user" {
			m.Parts = []Part{{Type: PartText, Text: m.Text}}
		} else {
			m.Parts = TextParts(m.Text)
		}
		return m
	}
	if m.Role != "assistant" || m.Status == MessageStatusFailed {
		return m
	}
	// Upgrade legacy assistant messages that were stored as a single raw text
	// part (possibly with trailing non-text parts) into engine-segmented parts.
	hasPage := false
	textIndexes := []int{}
	for i, p := range m.Parts {
		if p.Type == PartPage {
			hasPage = true
			break
		}
		if p.Type == PartText {
			textIndexes = append(textIndexes, i)
		}
	}
	if hasPage || len(textIndexes) != 1 {
		return m
	}
	idx := textIndexes[0]
	only := m.Parts[idx]
	if only.Display == "bubble" {
		return m
	}
	segmented := segmentTextParts(only.Text, only.Display)
	if len(segmented) == 1 && segmented[0].Type == PartText {
		return m
	}
	upgraded := make([]Part, 0, len(m.Parts)-1+len(segmented))
	upgraded = append(upgraded, m.Parts[:idx]...)
	upgraded = append(upgraded, segmented...)
	upgraded = append(upgraded, m.Parts[idx+1:]...)
	m.Parts = upgraded
	return m
}

type mdBlockKind int

const (
	blockParagraph mdBlockKind = iota
	blockHeading
	blockList
	blockCode
	blockTable
	blockHR
)

type mdBlock struct {
	kind  mdBlockKind
	text  string
	lines []string
}

func splitMarkdownBlocks(markdown string) []mdBlock {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	var blocks []mdBlock
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		trimmed := strings.TrimSpace(lines[i])
		// Fenced code block
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence := trimmed[:3]
			start := i
			i++
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence) {
				i++
			}
			if i < len(lines) {
				i++
			}
			chunk := lines[start:i]
			blocks = append(blocks, mdBlock{kind: blockCode, text: strings.Join(chunk, "\n"), lines: chunk})
			continue
		}
		// Thematic break (--- / *** / ___)
		if thematicBreakRe.MatchString(lines[i]) {
			blocks = append(blocks, mdBlock{kind: blockHR, text: trimmed, lines: lines[i : i+1]})
			i++
			continue
		}
		// Heading
		if headingLineRe.MatchString(trimmed) {
			blocks = append(blocks, mdBlock{kind: blockHeading, text: trimmed, lines: lines[i : i+1]})
			i++
			continue
		}
		// Table (header + separator row)
		if i+1 < len(lines) && strings.Contains(lines[i], "|") && tableSepLineRe.MatchString(lines[i+1]) {
			start := i
			i += 2
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" && strings.Contains(lines[i], "|") {
				i++
			}
			chunk := lines[start:i]
			blocks = append(blocks, mdBlock{kind: blockTable, text: strings.Join(chunk, "\n"), lines: chunk})
			continue
		}
		// List block
		if listItemLineRe.MatchString(trimmed) {
			start := i
			i++
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if t == "" {
					break
				}
				if listItemLineRe.MatchString(t) || nestedListLineRe.MatchString(lines[i]) || strings.HasPrefix(lines[i], "  ") || strings.HasPrefix(lines[i], "\t") {
					i++
					continue
				}
				break
			}
			chunk := lines[start:i]
			blocks = append(blocks, mdBlock{kind: blockList, text: strings.Join(chunk, "\n"), lines: chunk})
			continue
		}
		// Paragraph (stop at blank line, fence, HR, heading, list, or table)
		start := i
		i++
		for i < len(lines) {
			t := strings.TrimSpace(lines[i])
			if t == "" || strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") ||
				thematicBreakRe.MatchString(lines[i]) || headingLineRe.MatchString(t) || listItemLineRe.MatchString(t) {
				break
			}
			if i+1 < len(lines) && strings.Contains(lines[i], "|") && tableSepLineRe.MatchString(lines[i+1]) {
				break
			}
			i++
		}
		chunk := lines[start:i]
		blocks = append(blocks, mdBlock{kind: blockParagraph, text: strings.TrimSpace(strings.Join(chunk, "\n")), lines: chunk})
	}
	return blocks
}

func countWords(s string) int {
	n := 0
	for _, field := range strings.Fields(s) {
		for _, r := range field {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r > 127 {
				n++
				break
			}
		}
	}
	return n
}

func looksLikeDocStart(b mdBlock) bool {
	if b.kind == blockHeading {
		return true
	}
	if b.kind == blockParagraph && len(b.lines) == 1 {
		t := strings.TrimSpace(b.text)
		if len(t) > 4 && len(t) <= 140 && strings.HasPrefix(t, "**") && strings.HasSuffix(t, "**") && !strings.Contains(t[2:len(t)-2], "**") {
			return true
		}
		if strings.HasPrefix(strings.ToLower(t), "title:") {
			return true
		}
	}
	return false
}

func isConversationalTailBlock(b mdBlock) bool {
	if b.kind != blockParagraph {
		return false
	}
	t := strings.TrimSpace(b.text)
	if t == "" || countWords(t) > 55 {
		return false
	}
	return strings.HasSuffix(t, "?") || conversationalStart.MatchString(t)
}

func isGistParagraph(text string) bool {
	if countWords(text) > 45 {
		return false
	}
	sentences := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '.' || text[i] == '!' || text[i] == '?' {
			if i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\n' {
				sentences++
			}
		}
	}
	return sentences <= 2
}

func joinBlocks(blocks []mdBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, b.text)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func segmentTextParts(markdown, forced string) []Part {
	if forced == "bubble" {
		return []Part{{Type: PartText, Text: markdown, Display: "bubble"}}
	}
	if cardParts, ok := parseCardBlocks(markdown); ok {
		return cardParts
	}
	if forced != "page" {
		return []Part{{Type: PartText, Text: markdown}}
	}
	blocks := splitMarkdownBlocks(markdown)
	if len(blocks) == 0 {
		return []Part{{Type: PartText, Text: markdown}}
	}

	startIdx := 0
	endIdx := len(blocks) - 1
	cover := ""
	tail := ""

	// 1. Peel trailing conversational follow-up after a closing thematic break (---)
	for i := endIdx - 1; i >= max(1, endIdx-3); i-- {
		if blocks[i].kind == blockHR {
			after := blocks[i+1 : endIdx+1]
			allPara := len(after) > 0
			words := 0
			for _, b := range after {
				if b.kind != blockParagraph {
					allPara = false
					break
				}
				words += countWords(b.text)
			}
			if allPara && words <= 100 {
				tail = joinBlocks(after)
				endIdx = i - 1
			}
			break
		}
	}

	// 2. Even without a thematic break, peel 1-2 short conversational closing paragraphs
	if tail == "" && endIdx >= 1 {
		tailStart := endIdx + 1
		peeled := 0
		for tailStart-1 > startIdx && peeled < 2 && isConversationalTailBlock(blocks[tailStart-1]) {
			tailStart--
			peeled++
		}
		if peeled > 0 && tailStart <= endIdx {
			tail = joinBlocks(blocks[tailStart : endIdx+1])
			endIdx = tailStart - 1
		}
	}

	// 3. Peel leading preamble/caveats before an opening thematic break (---) followed by a document title
	for i := startIdx + 1; i <= min(endIdx-1, startIdx+4); i++ {
		if blocks[i].kind == blockHR && looksLikeDocStart(blocks[i+1]) {
			before := blocks[startIdx:i]
			ok := len(before) > 0
			words := 0
			for _, b := range before {
				if b.kind != blockParagraph && b.kind != blockList {
					ok = false
					break
				}
				words += countWords(b.text)
			}
			if ok && words <= 120 {
				cover = joinBlocks(before)
				startIdx = i + 1
			}
			break
		}
	}

	remaining := blocks[startIdx : endIdx+1]
	body := joinBlocks(remaining)

	// 4. If no cover yet and the first remaining block is a brief intro paragraph, peel it
	if cover == "" && len(remaining) > 0 && remaining[0].kind == blockParagraph && !looksLikeDocStart(remaining[0]) {
		first := remaining[0].text
		if len(remaining) > 1 && isGistParagraph(first) {
			cover = strings.TrimSpace(first)
			body = joinBlocks(remaining[1:])
		} else if m := sentenceEndRe.FindString(first); m != "" && countWords(m) <= 30 {
			restFirst := strings.TrimSpace(first[len(m):])
			if restFirst != "" || len(remaining) > 1 {
				cover = strings.TrimSpace(m)
				if restFirst != "" {
					if len(remaining) > 1 {
						body = restFirst + "\n\n" + joinBlocks(remaining[1:])
					} else {
						body = restFirst
					}
				} else {
					body = joinBlocks(remaining[1:])
				}
			}
		} else {
			cover = defaultFallbackNote
		}
	}

	title := DocumentTitle(body, "")
	if title == "" {
		title = clipBytes(PlainExcerpt(body, 64), 64)
	}

	out := make([]Part, 0, 3)
	if cover != "" {
		out = append(out, Part{Type: PartText, Text: cover})
	}
	out = append(out, Part{Type: PartPage, Display: "page", Title: title, Text: body})
	if tail != "" {
		out = append(out, Part{Type: PartText, Text: tail})
	}
	return out
}
