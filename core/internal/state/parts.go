package state

import (
	"regexp"
	"strings"
)

// Part types. A client that doesn't know a type shows the part's Fallback.
const (
	PartText       = "text"
	PartAttachment = "attachment"
	PartArtifact   = "artifact"
	PartImage      = "image"
	PartLink       = "link"
	// PartApprovalText is an exact saved approval question, shown without Markdown.
	PartApprovalText = "approval_text"
	// PartOffer is a proactive offer's Accept and Dismiss card. ID names the offer.
	PartOffer = "offer"
)

// Part is one piece of a message. Clients read parts and never parse model output.
type Part struct {
	Type string `json:"type" firestore:"type"`
	// Text and Display belong to a text part. Display is "page" or "bubble" when the model asked for one.
	Text    string `json:"text,omitempty" firestore:"text,omitempty"`
	Display string `json:"display,omitempty" firestore:"display,omitempty"`
	// Fallback is what a client shows for a type it doesn't know. Every non-text part has one.
	Fallback string `json:"fallback,omitempty" firestore:"fallback,omitempty"`
	// ID names the upload or result behind an attachment, artifact or image part.
	ID        string `json:"id,omitempty" firestore:"id,omitempty"`
	Name      string `json:"name,omitempty" firestore:"name,omitempty"`
	MediaType string `json:"media_type,omitempty" firestore:"media_type,omitempty"`
	Size      int64  `json:"size,omitempty" firestore:"size,omitempty"`
	URL       string `json:"url,omitempty" firestore:"url,omitempty"`
	// Status is an offer card's state: open, accepted or dismissed.
	Status string `json:"status,omitempty" firestore:"status,omitempty"`
}

var (
	replyHint         = regexp.MustCompile(`<!--\s*nori:([a-z]+)[\s\S]*?-->`)
	unfinishedComment = regexp.MustCompile(`<!--[\s\S]*$`)
)

// TextParts turns model text into parts. The model's layout hints leave the text here, so no client sees them.
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

// WithParts returns the message with its parts. A message saved before parts existed gets them from its text.
func (m Message) WithParts() Message {
	if len(m.Parts) == 0 {
		m.Parts = TextParts(m.Text)
	}
	return m
}
