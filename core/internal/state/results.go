package state

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ResultAnswer     = "answer"
	ResultFile       = "file"
	ResultScreenshot = "screenshot"
	ResultLink       = "link"
)

const (
	RecentResultLimit = 20
	MaxResultTitle    = 200
)

type Result struct {
	ID           string   `json:"id" firestore:"id"`
	RootID       string   `json:"root_id" firestore:"root_id"`
	Version      int      `json:"version" firestore:"version"`
	Revises      string   `json:"revises,omitempty" firestore:"revises,omitempty"`
	SupersededBy string   `json:"superseded_by,omitempty" firestore:"superseded_by,omitempty"`
	RunID        string   `json:"run_id" firestore:"run_id"`
	Revision     int64    `json:"revision" firestore:"revision"`
	Kind         string   `json:"kind" firestore:"kind"`
	Title        string   `json:"title" firestore:"title"`
	Text         string   `json:"text,omitempty" firestore:"text,omitempty"`
	Name         string   `json:"name,omitempty" firestore:"name,omitempty"`
	MediaType    string   `json:"media_type,omitempty" firestore:"media_type,omitempty"`
	Size         int64    `json:"size,omitempty" firestore:"size,omitempty"`
	ArtifactID   string   `json:"artifact_id,omitempty" firestore:"artifact_id,omitempty"`
	URL          string   `json:"url,omitempty" firestore:"url,omitempty"`
	Evidence     []string `json:"evidence,omitempty" firestore:"evidence,omitempty"`
	Unmet        []string `json:"unmet,omitempty" firestore:"unmet,omitempty"`

	CreatedAt   time.Time  `json:"created_at" firestore:"created_at"`
	CheckedAt   *time.Time `json:"checked_at,omitempty" firestore:"checked_at,omitempty"`
	ReportedAt  *time.Time `json:"reported_at,omitempty" firestore:"reported_at,omitempty"`
	MessageID   string     `json:"message_id,omitempty" firestore:"message_id,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty" firestore:"delivered_at,omitempty"`
}

func ResultKey(id string) string { return "results/" + id }

type ResultDraft struct {
	Title     string
	Checked   bool
	Evidence  []string
	Unmet     []string
	Artifacts []string
	Cited     []string
}

func ResultID(parts ...string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("result:"+strings.Join(parts, ":"))).String()
}

func ResultTitle(request string) string {
	title := strings.TrimSpace(strings.SplitN(strings.TrimSpace(request), "\n", 2)[0])
	return clipBytes(title, 80)
}

// DocumentTitle extracts a clean title from structured document text (Markdown
// heading, "Title: ..." prefix, or standalone bold title line), falling back
// to ResultTitle(fallbackRequest).
func DocumentTitle(text, fallbackRequest string) string {
	if extracted, _ := ExtractDocumentTitle(text); extracted != "" {
		return extracted
	}
	if fallbackRequest == "" {
		return ""
	}
	return ResultTitle(fallbackRequest)
}

// ExtractDocumentTitle returns an explicit title found in the opening lines of
// text along with the remaining body text after that title line.
func ExtractDocumentTitle(text string) (title string, rest string) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	checked := 0
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		checked++
		if checked > 5 {
			break
		}
		var candidate string
		switch {
		case strings.HasPrefix(line, "#"):
			trimmed := strings.TrimLeft(line, "#")
			if len(trimmed) < len(line) && (len(trimmed) == 0 || trimmed[0] == ' ' || trimmed[0] == '\t') {
				candidate = strings.TrimSpace(trimmed)
			}
		case strings.HasPrefix(strings.ToLower(line), "title:"):
			candidate = strings.TrimSpace(line[len("title:"):])
		case strings.HasPrefix(strings.ToLower(line), "**title:**"):
			candidate = strings.TrimSpace(line[len("**title:**"):])
		case strings.HasPrefix(strings.ToLower(line), "**title**:"):
			candidate = strings.TrimSpace(line[len("**title**:"):])
		case checked == 1 && len(line) > 4 && len(line) <= 140 && strings.HasPrefix(line, "**") && strings.HasSuffix(line, "**") && !strings.Contains(line[2:len(line)-2], "**"):
			candidate = strings.TrimSpace(line[2 : len(line)-2])
		}
		if candidate != "" {
			candidate = cleanTitleLine(candidate)
			if candidate != "" {
				remaining := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
				return clipBytes(candidate, 120), remaining
			}
		}
		// Stop scanning if the first non-empty line is normal prose, not a title.
		if checked == 1 {
			break
		}
	}
	return "", strings.TrimSpace(text)
}

func cleanTitleLine(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, "*_`")
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	s = strings.Trim(s, "“”")
	return strings.TrimSpace(s)
}

var (
	mdLinkRe    = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)
	mdHeadingRe = regexp.MustCompile(`(?m)^\s*#{1,6}\s+`)
	mdBulletRe  = regexp.MustCompile(`(?m)^\s*(?:[-*+]|\d+[.)])\s+`)
	mdRuleRe    = regexp.MustCompile(`(?m)^\s*(?:[-*_]\s*){3,}$`)
	mdQuoteRe   = regexp.MustCompile(`(?m)^\s*>\s?`)
	mdSyntaxRe  = regexp.MustCompile(`[*_` + "`" + `~]+`)
)

// PlainExcerpt converts Markdown text into a concise, single-line plain-text
// preview clipped to maxRunes runes.
func PlainExcerpt(text string, maxRunes int) string {
	if text == "" || maxRunes <= 0 {
		return ""
	}
	s := strings.ReplaceAll(text, "\r\n", "\n")
	s = replyHint.ReplaceAllString(s, "")
	s = cardOpenTagRe.ReplaceAllString(s, "")
	s = cardCloseTagRe.ReplaceAllString(s, "")
	s = mdRuleRe.ReplaceAllString(s, " ")
	s = mdLinkRe.ReplaceAllString(s, "$1")
	s = mdHeadingRe.ReplaceAllString(s, "")
	s = mdBulletRe.ReplaceAllString(s, "")
	s = mdQuoteRe.ReplaceAllString(s, "")
	s = mdSyntaxRe.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	cut := strings.TrimSpace(string(runes[:maxRunes]))
	if idx := strings.LastIndex(cut, " "); idx > maxRunes/2 {
		cut = cut[:idx]
	}
	return strings.TrimRight(cut, ".,;:-") + "…"
}

func clipBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && text[limit]&0xC0 == 0x80 {
		limit--
	}
	return text[:limit] + "…"
}

func (r Result) Summary() Result {
	r.Text = clipBytes(r.Text, 500)
	return r
}

func ValidLink(address string) error {
	parsed, err := url.Parse(address)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || len(address) > 2048 {
		return errors.New("result_link needs an http or https URL")
	}
	return nil
}

func PublishResults(run Run, req PublishRequest, existing []Result, prior *Result, now time.Time) ([]Result, string) {
	results := append([]Result{}, existing...)
	sort.SliceStable(results, func(i, j int) bool { return results[i].CreatedAt.Before(results[j].CreatedAt) })
	checked := req.Answer != nil && req.Answer.Checked && run.Status == RunCompleted
	if req.Answer != nil && run.Result != "" {
		title := strings.TrimSpace(req.Answer.Title)
		if title != "" {
			title = clipBytes(cleanTitleLine(title), 120)
		}
		if title == "" {
			title = DocumentTitle(run.Result, run.Request)
		}
		answer := Result{
			ID: ResultID(run.ID, "answer"), RunID: run.ID, Revision: req.Revision, Kind: ResultAnswer, Title: title,
			Text: run.Result, Evidence: req.Answer.Evidence, Unmet: req.Answer.Unmet, CreatedAt: now, Version: 1,
		}
		answer.RootID = answer.ID
		results = append(results, answer)
		for _, artifact := range req.Answer.Artifacts {
			id := ResultID(run.ID, artifact)
			results = append(results, Result{
				ID: id, RootID: id, Version: 1, RunID: run.ID, Revision: req.Revision, Kind: ResultScreenshot, ArtifactID: artifact,
				Title: "Screenshot: " + ResultTitle(run.Request), CreatedAt: now,
			})
		}
	}
	for i := range results {
		cited := results[i].Kind == ResultAnswer || results[i].Kind == ResultScreenshot || (req.Answer != nil && slices.Contains(req.Answer.Cited, results[i].ID))
		if checked && cited && results[i].CheckedAt == nil {
			results[i].CheckedAt = &now
		}
	}
	if prior == nil || len(results) == 0 || !checked {
		return results, ""
	}
	main := &results[0]
	for i := range results {
		if results[i].Kind == ResultFile {
			main = &results[i]
			break
		}
		if results[i].Kind == ResultAnswer {
			main = &results[i]
		}
	}
	if prior.SupersededBy != "" && prior.SupersededBy != main.ID {
		notice := fmt.Sprintf("Revision conflict: saved result %s was already replaced by %s. This task's output is saved separately; it did not replace either result.", prior.ID, prior.SupersededBy)
		for i := range results {
			if results[i].ID == main.ID || results[i].Kind == ResultAnswer {
				results[i].Unmet = append(append([]string{}, results[i].Unmet...), notice)
			}
			if results[i].Kind == ResultAnswer {
				results[i].Text = notice + "\n\n" + results[i].Text
			}
		}
		return results, notice
	}
	main.Revises, main.RootID, main.Version = prior.ID, prior.RootID, prior.Version+1
	superseded := *prior
	superseded.SupersededBy = main.ID
	return append(results, superseded), ""
}

func ReportResults(results []Result, shown []string, messageID string, now time.Time) ([]Result, []Part) {
	parts := []Part{}
	marked := make([]Result, 0, len(results))
	for _, result := range results {
		if result.ReportedAt == nil {
			result.ReportedAt, result.MessageID = &now, messageID
		}
		marked = append(marked, result)
		if part, ok := ResultPart(result); ok {
			parts = append(parts, part)
		} else if slices.Contains(shown, result.ID) {
			parts = append(parts, Part{Type: PartText, Text: result.Text})
		}
	}
	return marked, parts
}

func ResultPart(result Result) (Part, bool) {
	switch result.Kind {
	case ResultFile:
		kind := PartArtifact
		if strings.HasPrefix(result.MediaType, "image/") {
			kind = PartImage
		}
		return Part{
			Type: kind, ID: result.ID, Name: result.Name, MediaType: result.MediaType, Size: result.Size,
			Fallback: fmt.Sprintf("Saved file: %s (%s). Open Nori's web app to get it.", result.Title, result.Name),
		}, true
	case ResultScreenshot:
		return Part{
			Type: PartImage, ID: result.ID, Name: result.Title, MediaType: "image/png",
			Fallback: fmt.Sprintf("Saved screenshot: %s. Open Nori's web app to see it.", result.Title),
		}, true
	case ResultLink:
		return Part{Type: PartLink, ID: result.ID, Name: result.Title, URL: result.URL, Fallback: fmt.Sprintf("Saved link: %s %s", result.Title, result.URL)}, true
	}
	return Part{}, false
}
