package state

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// ConversationSummary stands in for the turns that fell out of the outer model's recent window. It changes what the
// model sees, never what is stored: every turn and message stays saved.
type ConversationSummary struct {
	// From is the context start the summary belongs to. When an operator starts a fresh context, a summary of the
	// old one no longer applies.
	From time.Time `json:"from" firestore:"from"`
	// Through is when the newest turn it covers started. Turns after it are replayed word for word.
	Through   time.Time     `json:"through" firestore:"through"`
	Items     []SummaryItem `json:"items" firestore:"items"`
	Gist      string        `json:"gist" firestore:"gist"`
	Turns     int           `json:"turns" firestore:"turns"`
	Model     string        `json:"model" firestore:"model"`
	UpdatedAt time.Time     `json:"updated_at" firestore:"updated_at"`
}

// SummaryItem keeps one thing from an earlier turn in Austin's exact words, with the message it came from.
type SummaryItem struct {
	Kind    string `json:"kind" firestore:"kind"`
	Quote   string `json:"quote" firestore:"quote"`
	InputID string `json:"input_id" firestore:"input_id"`
	Note    string `json:"note,omitempty" firestore:"note,omitempty"`
}

// SummaryKinds are the things a summary keeps word for word.
var SummaryKinds = []string{"constraint", "correction", "preference", "approval", "open_question", "commitment", "decision", "fact"}

// Limits on the outer model's view of the conversation.
const (
	// OuterInputBytes bounds every message one outer reply sends, other than its tool schema. RecentTurnLimit and
	// RecentTurnBytes bound the turns replayed word for word after the summary.
	OuterInputBytes = 40 << 10
	RecentTurnLimit = 8
	RecentTurnBytes = 16 << 10
	// CompactKeep is how many of the newest turns compaction leaves out of the summary. CompactTurnLimit and
	// CompactInputBytes bound the turns one compaction reads.
	CompactKeep       = 4
	CompactTurnLimit  = 24
	CompactInputBytes = 64 << 10
	// SummaryBytes bounds the saved summary. MaxSummaryItems, MaxSummaryQuote, MaxSummaryNote and MaxSummaryGist bound
	// its parts.
	SummaryBytes    = 6 << 10
	MaxSummaryItems = 40
	MaxSummaryQuote = 600
	MaxSummaryNote  = 200
	MaxSummaryGist  = 1500
)

// Applies reports whether a saved summary belongs to the context that starts at from.
func (s ConversationSummary) Applies(from time.Time) bool {
	return !s.Through.IsZero() && s.From.Equal(from)
}

// TurnBytes is how much a turn adds to the outer model's input.
func TurnBytes(t ConversationTurn) int {
	n := 0
	for _, p := range t.Protocol {
		n += len(p)
	}
	return n
}

// CheckSummaryItems keeps only items whose quote is Austin's exact words from the message it names. sources maps input
// IDs to the text of his messages, including the quotes of the summary being replaced. An item whose quote isn't
// exact falls back to the whole message when that fits.
func CheckSummaryItems(items []SummaryItem, sources map[string]string) (kept []SummaryItem, dropped int) {
	kept = []SummaryItem{}
	for i, item := range items {
		if len(kept) == MaxSummaryItems {
			return kept, dropped + len(items) - i
		}
		source, ok := sources[item.InputID]
		if !ok || !validKind(item.Kind) {
			dropped++
			continue
		}
		item.Quote = strings.TrimSpace(item.Quote)
		if item.Quote == "" || !strings.Contains(source, item.Quote) || len(item.Quote) > MaxSummaryQuote {
			if len(source) > MaxSummaryQuote {
				dropped++
				continue
			}
			item.Quote = source
		}
		if len(item.Note) > MaxSummaryNote {
			item.Note = trimText(item.Note, MaxSummaryNote)
		}
		kept = append(kept, item)
	}
	return kept, dropped
}

// FitSummary trims a summary to SummaryBytes: first the gist, then the oldest items, keeping constraints and
// corrections longest.
func FitSummary(s *ConversationSummary) {
	s.Gist = trimText(s.Gist, MaxSummaryGist)
	size := func() int {
		data, _ := json.Marshal(struct {
			Items []SummaryItem `json:"items"`
			Gist  string        `json:"gist"`
		}{s.Items, s.Gist})
		return len(data)
	}
	for size() > SummaryBytes && s.Gist != "" {
		s.Gist = trimText(s.Gist, len(s.Gist)/2)
	}
	for _, protected := range []bool{false, true} {
		for i := 0; i < len(s.Items) && size() > SummaryBytes; {
			kind := s.Items[i].Kind
			if !protected && (kind == "constraint" || kind == "correction") {
				i++
				continue
			}
			s.Items = append(s.Items[:i], s.Items[i+1:]...)
		}
	}
}

func validKind(kind string) bool {
	return slices.Contains(SummaryKinds, kind)
}

func trimText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && (text[limit]&0xC0) == 0x80 {
		limit--
	}
	return text[:limit]
}
