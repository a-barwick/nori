package state

import (
	"regexp"
	"strings"
)

const (
	PartText       = "text"
	PartAttachment = "attachment"
	PartArtifact   = "artifact"
	PartImage      = "image"
	PartLink       = "link"
	PartReminder   = "reminder"
)

type Part struct {
	Type        string `json:"type" firestore:"type"`
	Text        string `json:"text,omitempty" firestore:"text,omitempty"`
	Display     string `json:"display,omitempty" firestore:"display,omitempty"`
	Fallback    string `json:"fallback,omitempty" firestore:"fallback,omitempty"`
	ID          string `json:"id,omitempty" firestore:"id,omitempty"`
	Name        string `json:"name,omitempty" firestore:"name,omitempty"`
	MediaType   string `json:"media_type,omitempty" firestore:"media_type,omitempty"`
	Size        int64  `json:"size,omitempty" firestore:"size,omitempty"`
	URL         string `json:"url,omitempty" firestore:"url,omitempty"`
	ScheduledAt string `json:"scheduled_at,omitempty" firestore:"scheduled_at,omitempty"`
}

var (
	replyHint         = regexp.MustCompile(`<!--\s*nori:([a-z]+)[\s\S]*?-->`)
	unfinishedComment = regexp.MustCompile(`<!--[\s\S]*$`)
)

func TextParts(text string) []Part {
	part := Part{Type: PartText}
	stripped := replyHint.ReplaceAllStringFunc(text, func(match string) string {
		if name := replyHint.FindStringSubmatch(match)[1]; name == "page" || name == "bubble" {
			part.Display = name
		}
		return ""
	})
	part.Text = strings.TrimSpace(unfinishedComment.ReplaceAllString(stripped, ""))
	return []Part{part}
}

func (m Message) WithParts() Message {
	if len(m.Parts) == 0 {
		m.Parts = TextParts(m.Text)
	}
	return m
}
