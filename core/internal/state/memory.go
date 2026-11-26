package state

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"nori/internal/prompts"
)

type MemoryProvenance string

const (
	ProvenanceUserExplicit    MemoryProvenance = "user_explicit"
	ProvenancePreCompactFlush MemoryProvenance = "pre_compact_flush"
	ProvenanceSummaryEvicted  MemoryProvenance = "summary_evicted"
	ProvenanceTaskOutcome     MemoryProvenance = "task_outcome"
	ProvenanceDreamCurated    MemoryProvenance = "dream_curated"
)

func ValidMemoryProvenance(p MemoryProvenance) bool {
	switch p {
	case ProvenanceUserExplicit, ProvenancePreCompactFlush, ProvenanceSummaryEvicted, ProvenanceTaskOutcome, ProvenanceDreamCurated:
		return true
	default:
		return false
	}
}

type Memory struct {
	ID            string           `json:"id" firestore:"id"`
	Text          string           `json:"text" firestore:"text"`
	About         string           `json:"about" firestore:"about"`
	Section       string           `json:"section,omitempty" firestore:"section,omitempty"`
	Pinned        bool             `json:"pinned,omitempty" firestore:"pinned,omitempty"`
	Provenance    MemoryProvenance `json:"provenance,omitempty" firestore:"provenance,omitempty"`
	SourceInputID string           `json:"source_input_id,omitempty" firestore:"source_input_id,omitempty"`
	SourceText    string           `json:"source_text,omitempty" firestore:"source_text,omitempty"`
	SupersedesID  string           `json:"supersedes_id,omitempty" firestore:"supersedes_id,omitempty"`
	CreatedAt     time.Time        `json:"created_at" firestore:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at" firestore:"updated_at"`
}

func (m Memory) EffectiveProvenance() MemoryProvenance {
	if m.Provenance == "" {
		return ProvenanceUserExplicit
	}
	return m.Provenance
}

type MemoryLog struct {
	Date      string           `json:"date" firestore:"date"`
	Shard     int              `json:"shard,omitempty" firestore:"shard,omitempty"`
	Entries   []MemoryLogEntry `json:"entries" firestore:"entries"`
	UpdatedAt time.Time        `json:"updated_at" firestore:"updated_at"`
}

type MemoryLogEntry struct {
	ID            string           `json:"id" firestore:"id"`
	Section       string           `json:"section,omitempty" firestore:"section,omitempty"`
	Text          string           `json:"text" firestore:"text"`
	About         string           `json:"about,omitempty" firestore:"about,omitempty"`
	Provenance    MemoryProvenance `json:"provenance" firestore:"provenance"`
	SourceInputID string           `json:"source_input_id,omitempty" firestore:"source_input_id,omitempty"`
	SourceText    string           `json:"source_text,omitempty" firestore:"source_text,omitempty"`
	RunID         string           `json:"run_id,omitempty" firestore:"run_id,omitempty"`
	CreatedAt     time.Time        `json:"created_at" firestore:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at" firestore:"updated_at"`
}

type MemoryChange struct {
	Kind     string `json:"kind" firestore:"kind"`
	ID       string `json:"id" firestore:"id"`
	Text     string `json:"text,omitempty" firestore:"text,omitempty"`
	About    string `json:"about,omitempty" firestore:"about,omitempty"`
	Section  string `json:"section,omitempty" firestore:"section,omitempty"`
	Replaces bool   `json:"replaces,omitempty" firestore:"replaces,omitempty"`
	Error    string `json:"error,omitempty" firestore:"error,omitempty"`
}

const (
	MemoryRemember = "remember"
	MemoryForget   = "forget"

	CoreMemoryBytes     = 24 << 10
	MemoryBytes         = CoreMemoryBytes
	MaxMemoryText       = 2000
	MaxMemoryAbout      = 80
	MaxMemorySection    = 64
	MemorySourceBytes   = 1 << 10
	MaxDailyLogBytes    = 64 << 10
	MaxDailyLogEntries  = 128
	MaxTaskOutcomeBytes = 600
	TailRecallBytes     = 2560
	TailRecallTopK      = 5

	MemoryMutateCreateCore     = "create_core"
	MemoryMutateEditCore       = "edit_core"
	MemoryMutateForgetCore     = "forget_core"
	MemoryMutatePinCore        = "pin_core"
	MemoryMutateUnpinCore      = "unpin_core"
	MemoryMutateDemoteCore     = "demote_core"
	MemoryMutatePromoteLog     = "promote_log"
	MemoryMutateForgetLog      = "forget_log"
	MemoryMutatePromoteWorking = "promote_working"
	MemoryMutateDismissWorking = "dismiss_working"
)

type FlushAndCompactRequest struct {
	ExpectedSummary ConversationSummary
	NextSummary     ConversationSummary
	CoreUpserts     []Memory
	CoreDeletes     []string
	DailyLogDate    string
	DailyLogEntries []MemoryLogEntry
	CheckPending    bool
}

type DreamConsolidateRequest struct {
	ExpectedSummary ConversationSummary
	NextSummary     ConversationSummary
	CoreUpserts     []Memory
	CoreDeletes     []string
	DailyLogDate    string
	DailyLogEntries []MemoryLogEntry
	CheckPending    bool
}

type MemoryMutationRequest struct {
	RequestID        string
	ExpectedRevision int64
	Kind             string
	TargetID         string
	ReplacesID       string
	Date             string
	Text             string
	About            string
	Section          string
	SourceText       string
	Binding          string
	Pinned           *bool
	FormatResource   func(result MemoryMutationResult) (json.RawMessage, error)
}

type MemoryMutationResult struct {
	Memory      *Memory              `json:"memory,omitempty"`
	Demoted     []Memory             `json:"demoted,omitempty"`
	LogEntry    *MemoryLogEntry      `json:"log_entry,omitempty"`
	Summary     *ConversationSummary `json:"summary,omitempty"`
	CoreBytes   int                  `json:"core_bytes,omitempty"`
	ContextFrom time.Time            `json:"context_from,omitempty"`
	Note        string               `json:"note,omitempty"`
}

func ValidMemoryDate(date string) bool {
	if len(date) != 10 {
		return false
	}
	parsed, err := time.Parse("2006-01-02", date)
	return err == nil && parsed.Format("2006-01-02") == date
}

func MemoryLogDocID(date string, shard int) string {
	if shard <= 1 {
		return date
	}
	return fmt.Sprintf("%s#%d", date, shard)
}

func SortCoreMemories(memories []Memory) {
	slices.SortFunc(memories, func(a, b Memory) int {
		if a.Pinned != b.Pinned {
			if a.Pinned {
				return -1
			}
			return 1
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			if a.CreatedAt.Before(b.CreatedAt) {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

func CoreMemorySize(memories []Memory) int {
	total := 0
	for _, m := range memories {
		total += len(m.Text)
	}
	return total
}

func DemoteCoreOverflow(memories []Memory, limit int) (kept []Memory, demoted []Memory, err error) {
	if limit <= 0 {
		limit = CoreMemoryBytes
	}
	var pinned, unpinned []Memory
	pinnedBytes := 0
	for _, m := range memories {
		if m.Pinned {
			pinned = append(pinned, m)
			pinnedBytes += len(m.Text)
		} else {
			unpinned = append(unpinned, m)
		}
	}
	if pinnedBytes > limit {
		return nil, nil, ErrInvalidResource
	}
	slices.SortFunc(unpinned, func(a, b Memory) int {
		if !a.CreatedAt.Equal(b.CreatedAt) {
			if a.CreatedAt.Before(b.CreatedAt) {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.ID, b.ID)
	})
	total := pinnedBytes
	for _, m := range unpinned {
		total += len(m.Text)
	}
	cut := 0
	for cut < len(unpinned) && total > limit {
		total -= len(unpinned[cut].Text)
		demoted = append(demoted, unpinned[cut])
		cut++
	}
	kept = make([]Memory, 0, len(pinned)+len(unpinned)-cut)
	kept = append(kept, pinned...)
	kept = append(kept, unpinned[cut:]...)
	SortCoreMemories(kept)
	return kept, demoted, nil
}

func PlanMemoryChanges(saved []Memory, changes []MemoryChange) bool {
	byID := map[string]Memory{}
	for _, m := range saved {
		byID[m.ID] = m
	}
	touched := map[string]bool{}
	ok := true
	for i := range changes {
		c := &changes[i]
		old, exists := byID[c.ID]
		switch {
		case c.Kind == MemoryForget && !exists:
			c.Error = "there is no saved memory with that ID"
		case c.Kind == MemoryForget:
			delete(byID, c.ID)
			delete(touched, c.ID)
		case c.Kind != MemoryRemember:
			c.Error = "unknown memory change"
		case c.Replaces && !exists:
			c.Error = "there is no saved memory with that ID to correct"
		case strings.TrimSpace(c.Text) == "" || len(c.Text) > MaxMemoryText || !utf8.ValidString(c.Text):
			c.Error = fmt.Sprintf("a memory needs 1 to %d bytes of text", MaxMemoryText)
		case len(c.About) > MaxMemoryAbout || !utf8.ValidString(c.About):
			c.Error = fmt.Sprintf("who a memory is about can be at most %d bytes", MaxMemoryAbout)
		case len(c.Section) > MaxMemorySection || !utf8.ValidString(c.Section):
			c.Error = fmt.Sprintf("a memory section can be at most %d bytes", MaxMemorySection)
		default:
			pinned := exists && old.Pinned
			requiredBytes := len(c.Text)
			for id, m := range byID {
				if id == c.ID {
					continue
				}
				if m.Pinned || touched[id] {
					requiredBytes += len(m.Text)
				}
			}
			if requiredBytes > CoreMemoryBytes {
				c.Error = prompts.Runtime("memory-full", nil)
			} else {
				byID[c.ID] = Memory{ID: c.ID, Text: c.Text, About: c.About, Section: c.Section, Pinned: pinned}
				touched[c.ID] = true
			}
		}
		ok = ok && c.Error == ""
	}
	return ok
}

func ApplyMemoryChange(c MemoryChange, existing *Memory, source ConversationInput, now time.Time) Memory {
	m := Memory{
		ID:            c.ID,
		Text:          c.Text,
		About:         c.About,
		Section:       c.Section,
		Provenance:    ProvenanceUserExplicit,
		SourceInputID: source.ID,
		SourceText:    BoundedSource(source.Text),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if m.About == "" {
		m.About = "the user"
	}
	if existing != nil {
		m.CreatedAt = existing.CreatedAt
		m.Pinned = existing.Pinned
		if m.Section == "" {
			m.Section = existing.Section
		}
	}
	return m
}

func BoundedSource(text string) string {
	if len(text) <= MemorySourceBytes {
		return text
	}
	limit := MemorySourceBytes
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func SourceInput(inputs []ConversationInput) ConversationInput {
	var source ConversationInput
	for _, in := range inputs {
		if in.IsUser() {
			source = in
		}
	}
	return source
}
