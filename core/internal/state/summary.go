package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"
)

// ConversationSummary stands in for the turns that fell out of the outer model's recent window. It changes what the
// model sees, never what is stored: every turn and message stays saved.
type ConversationSummary struct {
	// A summary applies only to the retained context cutoff it covers.
	From time.Time `json:"from" firestore:"from"`
	// ExtractedThrough is the durable session-idle memory extraction watermark
	// (FlushedThrough <= ExtractedThrough). Turns in (FlushedThrough, ExtractedThrough]
	// have already extracted their durable Core/Daily-Log facts while remaining
	// verbatim in the recent chat window, so later compaction summarizes them
	// into Working Memory without double-extracting memories.
	ExtractedThrough time.Time `json:"extracted_through,omitempty" firestore:"extracted_through,omitempty"`
	// FlushedThrough is the authoritative durable watermark: every completed turn <= FlushedThrough
	// has committed its extracted memories (Core/Log) AND semantic summary in the same DB transaction.
	FlushedThrough time.Time `json:"flushed_through,omitempty" firestore:"flushed_through,omitempty"`
	// Legacy RefinementThrough read-compatibly migrates to FlushedThrough when FlushedThrough.IsZero().
	RefinementThrough time.Time `json:"refinement_through,omitempty" firestore:"refinement_through,omitempty"`
	// Through is the hot-path verbatim window cutoff (FlushedThrough <= Through).
	// Any turn in (FlushedThrough, Through] MUST remain represented in Excerpts until FlushedThrough catches up.
	Through                   time.Time             `json:"through" firestore:"through"`
	Excerpts                  []ConversationExcerpt `json:"excerpts,omitempty" firestore:"excerpts,omitempty"`
	Items                     []SummaryItem         `json:"items" firestore:"items"`
	DismissedItemIDs          []string              `json:"dismissed_item_ids,omitempty" firestore:"dismissed_item_ids,omitempty"`
	Gist                      string                `json:"gist" firestore:"gist"`
	Turns                     int                   `json:"turns" firestore:"turns"`
	Model                     string                `json:"model" firestore:"model"`
	FlushFailures             int                   `json:"flush_failures,omitempty" firestore:"flush_failures,omitempty"`
	LastFailureThrough        time.Time             `json:"last_failure_through,omitempty" firestore:"last_failure_through,omitempty"`
	ExtractFailures           int                   `json:"extract_failures,omitempty" firestore:"extract_failures,omitempty"`
	LastExtractFailureThrough time.Time             `json:"last_extract_failure_through,omitempty" firestore:"last_extract_failure_through,omitempty"`
	DreamThrough              time.Time             `json:"dream_through,omitempty" firestore:"dream_through,omitempty"`
	LastDreamAt               time.Time             `json:"last_dream_at,omitempty" firestore:"last_dream_at,omitempty"`
	UpdatedAt                 time.Time             `json:"updated_at" firestore:"updated_at"`
}

// EffectiveFlushedThrough returns the durable flush watermark, read-compatibly migrating
// legacy RefinementThrough / semantic summaries when FlushedThrough is zero.
func (s ConversationSummary) EffectiveFlushedThrough() time.Time {
	if !s.FlushedThrough.IsZero() {
		return s.FlushedThrough
	}
	if !s.RefinementThrough.IsZero() {
		return s.RefinementThrough
	}
	if !s.Through.IsZero() && s.Model != "" && s.Model != "extractive" {
		return s.Through
	}
	return s.From
}

// EffectiveExtractedThrough returns the durable memory-extraction watermark
// (always >= EffectiveFlushedThrough()).
func (s ConversationSummary) EffectiveExtractedThrough() time.Time {
	flushed := s.EffectiveFlushedThrough()
	if s.ExtractedThrough.After(flushed) {
		return s.ExtractedThrough
	}
	return flushed
}

// ConversationExcerpt is a lossless source excerpt for turns in (FlushedThrough, Through].
type ConversationExcerpt struct {
	MessageID     string    `json:"message_id" firestore:"message_id"`
	Role          string    `json:"role" firestore:"role"`
	Text          string    `json:"text" firestore:"text"`
	TurnStartedAt time.Time `json:"turn_started_at,omitempty" firestore:"turn_started_at,omitempty"`
}

// FitConversationExcerpts bounds temporary unflushed excerpts well inside OuterInputBytes.
func FitConversationExcerpts(s *ConversationSummary) {
	const maxExcerptBytes = 32 << 10
	for len(s.Excerpts) > 0 {
		data, _ := json.Marshal(s.Excerpts)
		if len(data) <= maxExcerptBytes {
			return
		}
		s.Excerpts = s.Excerpts[1:]
	}
}

// SummaryItem keeps one thing from an earlier turn in the user's exact words, with the message it came from.
type SummaryItem struct {
	ID      string `json:"id,omitempty" firestore:"id,omitempty"`
	Kind    string `json:"kind" firestore:"kind"`
	Quote   string `json:"quote" firestore:"quote"`
	InputID string `json:"input_id" firestore:"input_id"`
	Note    string `json:"note,omitempty" firestore:"note,omitempty"`
}

// SummaryItemID returns a stable deterministic identifier for a SummaryItem.
func SummaryItemID(item SummaryItem) string {
	if item.ID != "" {
		return item.ID
	}
	h := sha256.Sum256([]byte(item.InputID + "\x00" + item.Kind + "\x00" + strings.TrimSpace(item.Quote)))
	return "si_" + hex.EncodeToString(h[:8])
}

var SummaryKinds = []string{"constraint", "correction", "preference", "approval", "open_question", "commitment", "decision", "fact"}

const (
	// OuterInputBytes bounds every message one outer reply sends, other than its tool schema.
	// RecentTurnLimit and RecentTurnBytes bound the turns replayed word for word after the summary.
	OuterInputBytes = 128 << 10
	RecentTurnLimit = 20
	RecentTurnBytes = 48 << 10
	// SoftCompactTurns and SoftCompactBytes trigger proactive out-of-band flush+compact well before
	// the hard verbatim window cap. CompactKeep leaves the newest turns verbatim.
	SoftCompactTurns  = 12
	SoftCompactBytes  = 32 << 10
	CompactKeep       = 5
	CompactTurnLimit  = 24
	CompactInputBytes = 64 << 10
	// SummaryBytes bounds the saved Working Memory summary.
	SummaryBytes    = 16 << 10
	MaxSummaryItems = 80
	MaxSummaryQuote = 600
	MaxSummaryNote  = 200
	MaxSummaryGist  = 4000
	// Coupled compaction, session-idle extraction, & AutoDream scheduling parameters.
	MaxFlushRetries    = 3
	IdleFlushDelay     = 2 * time.Minute
	IdleExtractDelay   = 20 * time.Minute
	IdleDreamDelay     = 15 * time.Minute
	DreamMinInterval   = 12 * time.Hour
	DreamMinLogEntries = 10
)

func (s ConversationSummary) Applies(from time.Time) bool {
	return !s.Through.IsZero() && s.From.Equal(from)
}

func TurnBytes(t ConversationTurn) int {
	n := 0
	for _, p := range t.Protocol {
		n += len(p)
	}
	return n
}

// CheckSummaryItems keeps only items whose quote is the user's exact words from the message it names. sources maps input
// IDs to the text of their messages, including the quotes of the summary being replaced. An item whose quote isn't
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
		item.ID = SummaryItemID(item)
		kept = append(kept, item)
	}
	return kept, dropped
}

// FitSummary trims a summary to SummaryBytes without returning archived entries.
// Callers participating in coupled flush+compact MUST use FitSummaryWithArchive so
// evicted items are preserved in Daily Logs (INV-MEM-02).
func FitSummary(s *ConversationSummary) {
	_ = FitSummaryWithArchive(s, time.Time{})
}

// FitSummaryWithArchive trims a summary to SummaryBytes and MaxSummaryItems: first the gist,
// then the oldest items (keeping constraints and corrections longest). Every evicted item is
// returned as a ProvenanceSummaryEvicted MemoryLogEntry so nothing is silently lost.
func FitSummaryWithArchive(s *ConversationSummary, now time.Time) []MemoryLogEntry {
	s.Gist = trimText(s.Gist, MaxSummaryGist)
	for i := range s.Items {
		if s.Items[i].ID == "" {
			s.Items[i].ID = SummaryItemID(s.Items[i])
		}
	}
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
	var archived []MemoryLogEntry
	ts := now.UTC()
	for _, protected := range []bool{false, true} {
		for i := 0; i < len(s.Items) && (size() > SummaryBytes || len(s.Items) > MaxSummaryItems); {
			item := s.Items[i]
			if !protected && (item.Kind == "constraint" || item.Kind == "correction") {
				i++
				continue
			}
			s.Items = append(s.Items[:i], s.Items[i+1:]...)
			text := item.Quote
			if note := strings.TrimSpace(item.Note); note != "" {
				text = trimText(text+" ("+note+")", MaxMemoryText)
			} else {
				text = trimText(text, MaxMemoryText)
			}
			archived = append(archived, MemoryLogEntry{
				ID:            "evict_" + SummaryItemID(item),
				Section:       "Archived",
				Text:          text,
				About:         "the user",
				Provenance:    ProvenanceSummaryEvicted,
				SourceInputID: item.InputID,
				SourceText:    BoundedSource(item.Quote),
				CreatedAt:     ts,
				UpdatedAt:     ts,
			})
		}
	}
	return archived
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
