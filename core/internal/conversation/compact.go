package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"nori/internal/agent"
	"nori/internal/proactivity"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/transcript"
	"nori/internal/usage"
)

const (
	flushAndCompactToolName = "flush_and_compact"
	legacySummaryToolName   = "save_summary"
)

type slimCoreMemory struct {
	ID            string                 `json:"id"`
	Text          string                 `json:"text"`
	About         string                 `json:"about,omitempty"`
	Section       string                 `json:"section,omitempty"`
	Pinned        bool                   `json:"pinned,omitempty"`
	Provenance    state.MemoryProvenance `json:"provenance,omitempty"`
	SourceInputID string                 `json:"source_input_id,omitempty"`
}

func projectMaintenanceMemories(memories []state.Memory, includeSourceID bool) []slimCoreMemory {
	out := make([]slimCoreMemory, 0, len(memories))
	for _, m := range memories {
		about := m.About
		if about == "the user" {
			about = ""
		}
		prov := m.EffectiveProvenance()
		if prov == state.ProvenanceUserExplicit {
			prov = ""
		}
		item := slimCoreMemory{
			ID:         m.ID,
			Text:       m.Text,
			About:      about,
			Section:    m.Section,
			Pinned:     m.Pinned,
			Provenance: prov,
		}
		if includeSourceID {
			item.SourceInputID = m.SourceInputID
		}
		out = append(out, item)
	}
	return out
}

var extractOnlyTool = transcript.Tool{
	Name:        flushAndCompactToolName,
	Description: prompts.Runtime("flush-and-compact", nil),
	Parameters: &transcript.Schema{
		Type:     transcript.TypeObject,
		Required: []string{"core_upserts", "daily_log_entries"},
		Properties: map[string]*transcript.Schema{
			"core_upserts":      flushAndCompactTool.Parameters.Properties["core_upserts"],
			"daily_log_entries": flushAndCompactTool.Parameters.Properties["daily_log_entries"],
		},
	},
}

var summaryOnlyTool = transcript.Tool{
	Name:        flushAndCompactToolName,
	Description: prompts.Runtime("flush-and-compact", nil),
	Parameters: &transcript.Schema{
		Type:     transcript.TypeObject,
		Required: []string{"summary_items", "gist"},
		Properties: map[string]*transcript.Schema{
			"summary_items": flushAndCompactTool.Parameters.Properties["summary_items"],
			"gist":          flushAndCompactTool.Parameters.Properties["gist"],
		},
	},
}

var flushAndCompactTool = transcript.Tool{
	Name:        flushAndCompactToolName,
	Description: prompts.Runtime("flush-and-compact", nil),
	Parameters: &transcript.Schema{
		Type:     transcript.TypeObject,
		Required: []string{"core_upserts", "daily_log_entries", "summary_items", "gist"},
		Properties: map[string]*transcript.Schema{
			"core_upserts": {
				Type: transcript.TypeArray,
				Items: &transcript.Schema{
					Type:     transcript.TypeObject,
					Required: []string{"text", "about", "section", "replaces", "input_id", "quote"},
					Properties: map[string]*transcript.Schema{
						"text":     {Type: transcript.TypeString, Description: "Durable third-person fact, preference, person, or project context (1-2000 bytes)."},
						"about":    {Type: transcript.TypeString, Description: "Who this describes: 'the user' or the person named."},
						"section":  {Type: transcript.TypeString, Description: "Optional category such as Preferences, People, Projects, or Context."},
						"replaces": {Type: transcript.TypeString, Description: "Existing Core memory ID to update/supersede, or empty string for a new memory."},
						"input_id": {Type: transcript.TypeString, Description: "Exact input_id of the user message in this batch that states this fact."},
						"quote":    {Type: transcript.TypeString, Description: "Exact verbatim quote from that user message supporting this fact."},
					},
				},
			},
			"daily_log_entries": {
				Type: transcript.TypeArray,
				Items: &transcript.Schema{
					Type:     transcript.TypeObject,
					Required: []string{"text", "about", "section", "input_id", "run_id", "quote"},
					Properties: map[string]*transcript.Schema{
						"text":     {Type: transcript.TypeString, Description: "Episodic decision, context note, or completed-work takeaway (1-2000 bytes)."},
						"about":    {Type: transcript.TypeString, Description: "Who this describes (default 'the user')."},
						"section":  {Type: transcript.TypeString, Description: "Category: Decisions, Context, Completed Work, or Archived."},
						"input_id": {Type: transcript.TypeString, Description: "Exact user input_id from this batch (empty string for task-sourced entries)."},
						"run_id":   {Type: transcript.TypeString, Description: "Exact task run_id from a task event in this batch (empty string for user-sourced entries)."},
						"quote":    {Type: transcript.TypeString, Description: "Exact verbatim quote from the cited user message (empty string for task-sourced entries)."},
					},
				},
			},
			"summary_items": {
				Type: transcript.TypeArray,
				Items: &transcript.Schema{
					Type:     transcript.TypeObject,
					Required: []string{"kind", "quote", "input_id", "note"},
					Properties: map[string]*transcript.Schema{
						"kind":     {Type: transcript.TypeString, Enum: state.SummaryKinds},
						"quote":    {Type: transcript.TypeString, Description: prompts.Runtime("summary-quote", nil)},
						"input_id": {Type: transcript.TypeString, Description: prompts.Runtime("summary-input", nil)},
						"note":     {Type: transcript.TypeString, Description: prompts.Runtime("summary-note", nil)},
					},
				},
			},
			"gist": {Type: transcript.TypeString},
		},
	},
}

type SummaryModel interface {
	Name() string
	Respond(ctx context.Context, req agent.Request) (agent.Response, error)
}

const compactionPause = 10 * time.Minute

func flushBackoff(failures int) time.Duration {
	switch failures {
	case 1:
		return 2 * time.Minute
	case 2:
		return 5 * time.Minute
	default:
		return 10 * time.Minute
	}
}

func (r *Runner) userLocation(ctx context.Context) *time.Location {
	loc := r.Clock.Location
	if r.Profile != nil {
		_, profileLoc := r.Profile(ctx)
		if profileLoc != nil {
			loc = profileLoc
		}
	}
	if loc == nil {
		return time.UTC
	}
	return loc
}

func (r *Runner) localDate(ctx context.Context, t time.Time) string {
	return t.In(r.userLocation(ctx)).Format("2006-01-02")
}

func (r *Runner) contextStart(ctx context.Context) (time.Time, error) {
	control, _, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return control.ContextFrom, nil
}

func (r *Runner) compact(ctx context.Context) {
	if r.summarizer == nil {
		return
	}
	control, _, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return
	}
	if blocked, err := r.backgroundIdleBlocked(ctx, control); err != nil || blocked {
		return
	}
	now := r.now()
	canCompact := !now.Before(r.getCompactAfter())
	canExtract := !now.Before(r.getExtractAfter())
	if !canCompact && !canExtract {
		return
	}
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go r.runPeriodic(watchCtx, done, 500*time.Millisecond, func() bool {
		if pending, err := r.tasks.Store.UserRoutingPending(watchCtx); err == nil && pending {
			cancel()
			return false
		}
		return true
	})
	if canCompact {
		if err := r.flushAndCompact(watchCtx); err != nil {
			if watchCtx.Err() == nil && !errors.Is(err, state.ErrConversationStale) {
				slog.WarnContext(ctx, "conversation flush and compaction failed", "err", err)
			}
			return
		}
	}
	if watchCtx.Err() != nil || r.now().Before(r.getExtractAfter()) {
		return
	}
	if err := r.extractIdleMemory(watchCtx); err != nil && watchCtx.Err() == nil && !errors.Is(err, state.ErrConversationStale) {
		slog.WarnContext(ctx, "conversation idle memory extraction failed", "err", err)
	}
}

func (r *Runner) advanceHotWindow(ctx context.Context) error {
	from, err := r.contextStart(ctx)
	if err != nil {
		return err
	}
	summary, err := r.tasks.Store.ConversationSummary(ctx)
	if err != nil {
		return err
	}
	expected := summary
	after := from
	var flushedThrough time.Time
	if summary.Applies(from) {
		after = summary.Through
		flushedThrough = maxTime(from, summary.EffectiveFlushedThrough())
	} else {
		prev := summary
		summary = state.ConversationSummary{From: from}
		if prev.From.Equal(from) {
			if prev.ExtractedThrough.After(from) {
				summary.ExtractedThrough = prev.ExtractedThrough
			}
			summary.ExtractFailures = prev.ExtractFailures
			summary.LastExtractFailureThrough = prev.LastExtractFailureThrough
			summary.DismissedItemIDs = slices.Clone(prev.DismissedItemIDs)
			summary.FlushFailures = prev.FlushFailures
			summary.LastFailureThrough = prev.LastFailureThrough
			summary.DreamThrough = prev.DreamThrough
			summary.LastDreamAt = prev.LastDreamAt
		}
		flushedThrough = from
	}
	if summary.Items == nil {
		summary.Items = []state.SummaryItem{}
	}
	turns, err := r.tasks.Store.ConversationTurnsAfter(ctx, after)
	if err != nil {
		return err
	}
	pageFull := len(turns) == state.HistoryTurnsExamined
	turns = slices.DeleteFunc(turns, func(t state.ConversationTurn) bool { return t.Status != state.TurnCompleted })
	size := 0
	for _, t := range turns {
		size += state.TurnBytes(t)
	}
	if len(turns) <= state.RecentTurnLimit && size <= state.RecentTurnBytes {
		return nil
	}
	keep := verbatimKeepCount(turns, time.Time{}, 0)
	older := turns[keep:]
	if pageFull && len(older) > 0 {
		newest := older[0].StartedAt
		if older, err = r.tasks.Store.OldestConversationTurnsAfter(ctx, after); err != nil {
			return err
		}
		older = slices.DeleteFunc(older, func(t state.ConversationTurn) bool {
			return t.Status != state.TurnCompleted || t.StartedAt.After(newest)
		})
	} else {
		slices.Reverse(older)
	}
	if len(older) == 0 {
		return nil
	}

	next := summary
	next.Excerpts = slices.Clone(summary.Excerpts)
	next.FlushedThrough = flushedThrough
	next.RefinementThrough = flushedThrough
	through, count, inputBytes := time.Time{}, 0, 0
	for _, t := range older {
		view, _, _ := compactionTurn(t)
		data, _ := json.Marshal(view)
		if count > 0 && (count >= state.CompactTurnLimit || inputBytes+len(data) > state.CompactInputBytes) {
			break
		}
		inputBytes += len(data)
		if !proactivity.IsBriefTurn(t) {
			for _, in := range t.Inputs {
				if in.IsUser() {
					if clean := proactivity.StripUntrustedProactiveData(in.Text); strings.TrimSpace(clean) != "" {
						next.Excerpts = append(next.Excerpts, sourceExcerpt(state.UserMessageID(in.ID), "user", clean, t.StartedAt))
					}
				}
			}
			if reply := proactivity.StripUntrustedProactiveData(publishedReply(t)); reply != "" {
				next.Excerpts = append(next.Excerpts, sourceExcerpt(state.AssistantMessageID(t.ID), "assistant", reply, t.StartedAt))
			}
		}
		through, count = t.StartedAt, count+1
	}
	if count == 0 {
		return nil
	}
	next.From = from
	next.Through = through
	next.Turns = summary.Turns + count
	if next.Model == "" {
		next.Model = "extractive"
	}
	next.UpdatedAt = r.now().UTC()
	state.FitConversationExcerpts(&next)
	return r.tasks.Store.SaveConversationSummary(ctx, next, expected)
}

func sourceExcerpt(id, role, text string, turnStartedAt time.Time) state.ConversationExcerpt {
	if len(text) > state.MaxSummaryQuote {
		text = docstore.ClipUTF8(text, state.MaxSummaryQuote)
	}
	return state.ConversationExcerpt{MessageID: id, Role: role, Text: text, TurnStartedAt: turnStartedAt}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func (r *Runner) callFlushAndCompactWithTool(ctx context.Context, prompt string, tool transcript.Tool) (flushModelOutput, error) {
	ctx = usage.WithScope(ctx, "compaction", "compaction")
	if r.Now != nil || r.NewID != nil {
		ctx = usage.WithSession(ctx, usage.Session{Now: r.Now, NewID: r.NewID})
	}
	response, err := r.summarizer.Respond(ctx, agent.Request{
		Instructions:    prompts.Compaction,
		Tools:           []transcript.Tool{tool},
		MaxOutputTokens: 4096,
		Reasoning:       "low",
		SingleTurn:      true,
		Messages:        []transcript.Message{{Role: transcript.RoleUser, Parts: []transcript.Part{{Text: prompt}}}},
	})
	if err != nil {
		return flushModelOutput{}, err
	}
	if len(response.Calls) != 1 || (response.Calls[0].Name != flushAndCompactToolName && response.Calls[0].Name != legacySummaryToolName) {
		return flushModelOutput{}, errors.New("summary model didn't call flush_and_compact")
	}
	data, _ := json.Marshal(response.Calls[0].Args)
	var out flushModelOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return flushModelOutput{}, errors.New("summary model returned an invalid flush_and_compact payload")
	}
	return out, nil
}
