package state

import (
	"errors"
	"fmt"
	"net/url"
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
		answer := Result{
			ID: ResultID(run.ID, "answer"), RunID: run.ID, Revision: req.Revision, Kind: ResultAnswer, Title: ResultTitle(run.Request),
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
