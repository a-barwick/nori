package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"nori/internal/agent"
	"nori/internal/llmguard"
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

// slimCoreMemory strips verbose SourceText and timestamps from maintenance prompts
// (compaction, extraction, AutoDream), saving ~60% of core_memories input bytes.
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

// extractOnlyTool omits summary_items/gist for session-idle extraction calls.
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

// summaryOnlyTool omits core_upserts/daily_log_entries when all turns in a flush batch
// were already extracted during idle extraction.
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

// flushAndCompactTool asks the compaction model to perform pre-compaction memory
// extraction (Core upserts + Daily Log notes) and Working Memory summarization
// in a single pass. The runtime verifies every citation before committing.
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

// SummaryModel writes the coupled pre-compaction flush/summary and AutoDream consolidations.
// The runtime uses the inner task model for it.
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

// Older records can carry an operator-set context cutoff. Respect it when replaying history.
func (r *Runner) contextStart(ctx context.Context) (time.Time, error) {
	control, _, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return control.ContextFrom, nil
}

// compact runs out-of-band coupled pre-compaction flush + compaction, or
// session-idle memory extraction, when no user message is waiting. Arriving
// user input cancels the in-flight model call immediately without advancing
// FlushedThrough or ExtractedThrough.
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
	canCompact := !now.Before(r.compactAfter)
	canExtract := !now.Before(r.extractAfter)
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
	if watchCtx.Err() != nil || r.now().Before(r.extractAfter) {
		return
	}
	if err := r.extractIdleMemory(watchCtx); err != nil && watchCtx.Err() == nil && !errors.Is(err, state.ErrConversationStale) {
		slog.WarnContext(ctx, "conversation idle memory extraction failed", "err", err)
	}
}

type flushPlan struct {
	from               time.Time
	expected           state.ConversationSummary
	base               state.ConversationSummary
	flushedThrough     time.Time
	effectiveExtracted time.Time
	catchUp            bool
	hasWork            bool
	ready              bool
	latestTurnAt       time.Time
	batch              []state.ConversationTurn
	batchThrough       time.Time
}

func isUserTurn(t state.ConversationTurn) bool {
	return slices.ContainsFunc(t.Inputs, state.ConversationInput.IsUser)
}

// verbatimKeepCount returns how many newest completed turns to keep verbatim so
// at least CompactKeep total turns AND CompactKeepUserTurns user turns survive
// compaction (bounded by RecentTurnLimit, RecentTurnBytes, and optional byteLimit).
// When stopAtThrough is non-zero (catch-up), turns at or before stopAtThrough are never kept.
func verbatimKeepCount(newestFirst []state.ConversationTurn, stopAtThrough time.Time, byteLimit int) int {
	windowCap := min(len(newestFirst), state.RecentTurnLimit)
	targetUserTurns := 0
	for i := 0; i < windowCap; i++ {
		if !stopAtThrough.IsZero() && !newestFirst[i].StartedAt.After(stopAtThrough) {
			break
		}
		if isUserTurn(newestFirst[i]) {
			targetUserTurns++
			if targetUserTurns >= state.CompactKeepUserTurns {
				break
			}
		}
	}

	keep, keptUsers, keptBytes := 0, 0, 0
	for keep < windowCap {
		t := newestFirst[keep]
		if !stopAtThrough.IsZero() && !t.StartedAt.After(stopAtThrough) {
			break
		}
		if keep >= state.CompactKeep && keptUsers >= targetUserTurns {
			break
		}
		b := state.TurnBytes(t)
		if keptBytes+b > state.RecentTurnBytes {
			break
		}
		if byteLimit > 0 && keptBytes+b >= byteLimit {
			break
		}
		keptBytes += b
		if isUserTurn(t) {
			keptUsers++
		}
		keep++
	}
	return keep
}

// planFlush evaluates whether completed turns after EffectiveFlushedThrough()
// need coupled pre-compaction flushing now (threshold pressure or hot-window catch-up).
// Compaction never triggers on idle time alone. Shared by NextConversationWake and
// flushAndCompact so both agree exactly.
func (r *Runner) planFlush(
	ctx context.Context,
	control state.ConversationControl,
	summary state.ConversationSummary,
	now time.Time,
	collectBatch bool,
) (flushPlan, error) {
	from := control.ContextFrom
	plan := flushPlan{
		from:         from,
		expected:     summary,
		latestTurnAt: control.LatestAt,
	}
	if summary.Applies(from) {
		plan.base = summary
		if plan.base.Items == nil {
			plan.base.Items = []state.SummaryItem{}
		}
		plan.flushedThrough = maxTime(from, summary.EffectiveFlushedThrough())
		plan.catchUp = plan.flushedThrough.Before(summary.Through)
	} else {
		plan.base = state.ConversationSummary{From: from, Items: []state.SummaryItem{}}
		if summary.From.Equal(from) {
			if summary.ExtractedThrough.After(from) {
				plan.base.ExtractedThrough = summary.ExtractedThrough
			}
			plan.base.ExtractFailures = summary.ExtractFailures
			plan.base.LastExtractFailureThrough = summary.LastExtractFailureThrough
			plan.base.DismissedItemIDs = slices.Clone(summary.DismissedItemIDs)
			plan.base.FlushFailures = summary.FlushFailures
			plan.base.LastFailureThrough = summary.LastFailureThrough
			plan.base.DreamThrough = summary.DreamThrough
			plan.base.LastDreamAt = summary.LastDreamAt
		}
		plan.flushedThrough = from
	}
	plan.effectiveExtracted = maxTime(from, plan.base.EffectiveExtractedThrough())

	newestRaw, err := r.tasks.Store.ConversationTurnsAfter(ctx, plan.flushedThrough)
	if err != nil {
		return plan, err
	}
	pageFull := len(newestRaw) == state.HistoryTurnsExamined
	completed := make([]state.ConversationTurn, 0, len(newestRaw))
	totalBytes := 0
	userTurns := 0
	for _, t := range newestRaw {
		if t.Status != "completed" {
			continue
		}
		completed = append(completed, t)
		totalBytes += state.TurnBytes(t)
		if isUserTurn(t) {
			userTurns++
		}
	}
	if len(completed) > 0 && completed[0].StartedAt.After(plan.latestTurnAt) {
		plan.latestTurnAt = completed[0].StartedAt
	}

	// Keep the newest turns verbatim (at least CompactKeep total AND CompactKeepUserTurns user turns,
	// bounded by RecentTurnBytes) unless they have already moved past Through.
	var stopAtThrough time.Time
	if plan.catchUp {
		stopAtThrough = plan.base.Through
	}
	byteLimit := 0
	if totalBytes >= state.SoftCompactBytes {
		byteLimit = state.SoftCompactBytes
	}
	keep := verbatimKeepCount(completed, stopAtThrough, byteLimit)
	eligibleNewestFirst := completed[keep:]
	hasEligible := len(eligibleNewestFirst) > 0 || pageFull

	if !plan.catchUp && !hasEligible {
		return plan, nil
	}

	needed := plan.catchUp ||
		pageFull ||
		userTurns >= state.SoftCompactUserTurns ||
		len(completed) >= state.SoftCompactTurns ||
		totalBytes >= state.SoftCompactBytes
	if !needed {
		return plan, nil
	}

	plan.hasWork = true
	plan.ready = true

	if !collectBatch {
		return plan, nil
	}

	var older []state.ConversationTurn
	if pageFull {
		oldestRaw, err := r.tasks.Store.OldestConversationTurnsAfter(ctx, plan.flushedThrough)
		if err != nil {
			return plan, err
		}
		older = make([]state.ConversationTurn, 0, len(oldestRaw))
		for _, t := range oldestRaw {
			if t.Status != "completed" {
				continue
			}
			if keep > 0 && !t.StartedAt.Before(completed[keep-1].StartedAt) {
				continue
			}
			older = append(older, t)
		}
	} else {
		older = slices.Clone(eligibleNewestFirst)
		slices.Reverse(older)
	}

	inputBytes := 0
	for _, t := range older {
		view, _, _ := compactionTurn(t)
		view["already_extracted"] = !t.StartedAt.After(plan.effectiveExtracted)
		data, _ := json.Marshal(view)
		if len(plan.batch) > 0 && (len(plan.batch) >= state.CompactTurnLimit || inputBytes+len(data) > state.CompactInputBytes) {
			break
		}
		inputBytes += len(data)
		plan.batch = append(plan.batch, t)
	}

	if len(plan.batch) > 0 {
		plan.batchThrough = plan.batch[len(plan.batch)-1].StartedAt
		if plan.catchUp && plan.batchThrough.Before(plan.base.Through) {
			hasMoreBeforeThrough := false
			for _, rem := range older[len(plan.batch):] {
				if !rem.StartedAt.After(plan.base.Through) {
					hasMoreBeforeThrough = true
					break
				}
			}
			if !hasMoreBeforeThrough && (!pageFull || len(older) < state.HistoryTurnsExamined) {
				plan.batchThrough = plan.base.Through
			}
		}
	} else if plan.catchUp {
		plan.batchThrough = plan.base.Through
	}
	return plan, nil
}

type flushCoreUpsertCandidate struct {
	Text     string `json:"text"`
	About    string `json:"about"`
	Section  string `json:"section"`
	Replaces string `json:"replaces"`
	InputID  string `json:"input_id"`
	Quote    string `json:"quote"`
}

type flushLogEntryCandidate struct {
	Text    string `json:"text"`
	About   string `json:"about"`
	Section string `json:"section"`
	InputID string `json:"input_id"`
	RunID   string `json:"run_id"`
	Quote   string `json:"quote"`
}

type flushModelOutput struct {
	CoreUpserts     []flushCoreUpsertCandidate `json:"core_upserts"`
	DailyLogEntries []flushLogEntryCandidate   `json:"daily_log_entries"`
	SummaryItems    []state.SummaryItem        `json:"summary_items"`
	Items           []state.SummaryItem        `json:"items"` // legacy save_summary fallback
	Gist            string                     `json:"gist"`
}

func (r *Runner) flushAndCompact(ctx context.Context) error {
	control, _, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return err
	}
	if blocked, err := r.backgroundIdleBlocked(ctx, control); err != nil || blocked {
		return err
	}
	summary, err := r.tasks.Store.ConversationSummary(ctx)
	if err != nil {
		return err
	}
	now := r.now()
	plan, err := r.planFlush(ctx, control, summary, now, true)
	if err != nil || !plan.ready || !plan.batchThrough.After(plan.flushedThrough) {
		return err
	}

	// Zero-turn catch-up: only non-completed turns fell inside (FlushedThrough, Through].
	if len(plan.batch) == 0 {
		nowUTC := r.now().UTC()
		next := plan.base
		next.From = plan.from
		next.FlushedThrough = plan.batchThrough
		next.RefinementThrough = plan.batchThrough
		next.ExtractedThrough = maxTime(plan.base.EffectiveExtractedThrough(), next.FlushedThrough)
		if !next.LastExtractFailureThrough.IsZero() && !next.ExtractedThrough.Before(next.LastExtractFailureThrough) {
			next.ExtractFailures = 0
			next.LastExtractFailureThrough = time.Time{}
		}
		next.Through = maxTime(plan.base.Through, plan.batchThrough)
		next.Excerpts = pruneExcerpts(plan.base.Excerpts, next.FlushedThrough, next.Through)
		next.FlushFailures = 0
		next.LastFailureThrough = time.Time{}
		next.UpdatedAt = nowUTC
		_, err = r.tasks.Store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			ExpectedSummary: plan.expected,
			NextSummary:     next,
			DailyLogDate:    r.localDate(ctx, nowUTC),
			CheckPending:    true,
		})
		return err
	}

	coreMemories, err := r.tasks.Store.Memories(ctx)
	if err != nil {
		return err
	}
	state.SortCoreMemories(coreMemories)

	// Build verified source maps and rendered prompt payload from the batch.
	// summarySources spans every turn in plan.batch (plus previous summary items)
	// so Working Memory summarizes already-extracted turns leaving the recent window,
	// whereas batchUsers/batchUserTimes/batchRuns include ONLY unextracted turns
	// (StartedAt > effectiveExtracted) to guarantee zero double-extraction.
	effectiveExtracted := plan.effectiveExtracted
	summarySources := map[string]string{}
	for _, item := range plan.base.Items {
		summarySources[item.InputID] = strings.TrimPrefix(summarySources[item.InputID]+"\n"+item.Quote, "\n")
	}
	batchUsers := map[string]string{}
	batchUserTimes := map[string]time.Time{}
	batchRuns := map[string]bool{}
	rendered := make([]any, 0, len(plan.batch))
	anyUnextracted := false
	for _, t := range plan.batch {
		view, users, runs := compactionTurn(t)
		alreadyExtracted := !t.StartedAt.After(effectiveExtracted)
		view["already_extracted"] = alreadyExtracted
		rendered = append(rendered, view)
		for id, text := range users {
			summarySources[id] = text
		}
		if alreadyExtracted {
			continue
		}
		anyUnextracted = true
		recordBatchUserTimes(t, batchUserTimes)
		for id, text := range users {
			batchUsers[id] = text
		}
		for runID := range runs {
			batchRuns[runID] = true
		}
	}

	loc := r.userLocation(ctx)
	promptPayload := map[string]any{
		"previous_summary": map[string]any{
			"items": plan.base.Items,
			"gist":  plan.base.Gist,
		},
		"dismissed_item_ids": plan.base.DismissedItemIDs,
		"local_date":         now.In(loc).Format("2006-01-02"),
		"timezone":           loc.String(),
		"turns":              rendered,
	}
	tool := summaryOnlyTool
	if anyUnextracted {
		promptPayload["core_memories"] = projectMaintenanceMemories(coreMemories, false)
		tool = flushAndCompactTool
	}
	promptBytes, _ := json.Marshal(promptPayload)

	modelCtx, cancel := context.WithTimeout(llmguard.WithScope(ctx, llmguard.ScopeCompaction), 2*time.Minute)
	defer cancel()
	output, callErr := r.callFlushAndCompactWithTool(modelCtx, string(promptBytes), tool)
	if callErr != nil {
		if ctx.Err() != nil {
			// Preempted by arriving user input or shutdown: leave FlushedThrough untouched.
			return ctx.Err()
		}
		return r.handleFlushFailure(ctx, plan, callErr)
	}

	nowUTC := r.now().UTC()
	coreUpserts := verifyFlushCoreUpserts(output.CoreUpserts, batchUsers, batchUserTimes, coreMemories, nowUTC)
	baseLogEntries := verifyFlushLogEntries(output.DailyLogEntries, batchUsers, batchRuns, nowUTC)
	rawItems := output.SummaryItems
	if len(rawItems) == 0 && len(output.Items) > 0 {
		rawItems = output.Items
	}
	keptItems, dropped := state.CheckSummaryItems(rawItems, summarySources)
	if dropped > 0 {
		slog.WarnContext(ctx, "conversation flush dropped unverified summary quotes", "dropped", dropped)
	}
	trimmedGist := strings.TrimSpace(output.Gist)
	modelName := r.summarizer.Name()

	if err := r.commitFlushWithRebase(ctx, plan, coreUpserts, baseLogEntries, keptItems, trimmedGist, modelName, nowUTC); err != nil {
		return err
	}
	slog.InfoContext(ctx, "conversation flush and compaction completed",
		"turns", len(plan.batch),
		"core_candidates", len(output.CoreUpserts),
		"core_upserts", len(coreUpserts),
		"log_candidates", len(output.DailyLogEntries),
		"log_entries", len(baseLogEntries),
		"summary_items", len(keptItems),
		"flushed_through", plan.batchThrough,
	)
	return nil
}

func (r *Runner) handleFlushFailure(ctx context.Context, plan flushPlan, callErr error) error {
	now := r.now()
	nowUTC := now.UTC()
	failures := 1
	// Increment retries whenever the watermark (flushedThrough) has not advanced
	// past the previous failure, so newly arriving turns cannot reset the 3-strike
	// fallback into an infinite loop.
	if plan.base.FlushFailures > 0 && !plan.base.LastFailureThrough.IsZero() && !plan.flushedThrough.After(plan.base.LastFailureThrough) {
		failures = plan.base.FlushFailures + 1
	}

	if failures < state.MaxFlushRetries {
		next := plan.base
		next.From = plan.from
		next.FlushFailures = failures
		next.LastFailureThrough = plan.batchThrough
		next.UpdatedAt = nowUTC
		if err := r.tasks.Store.SaveConversationSummary(ctx, next, plan.expected); err != nil && !errors.Is(err, state.ErrConversationStale) {
			slog.WarnContext(ctx, "failed to record flush retry counter", "err", err)
		}
		r.compactAfter = now.Add(flushBackoff(failures))
		return callErr
	}

	// 3-strike poison-pill fallback: commit a lossless extractive flush of raw
	// unextracted user messages into Daily Logs before advancing FlushedThrough.
	slog.WarnContext(ctx, "flush_and_compact reached max retries; committing lossless extractive fallback",
		"failures", failures, "batch_through", plan.batchThrough, "err", callErr)
	fallbackEntries := extractiveFallbackEntries(unextractedTurns(plan.batch, plan.effectiveExtracted), nowUTC)
	modelName := plan.base.Model
	if modelName == "" {
		modelName = "extractive"
	}
	err := r.commitFlushWithRebase(ctx, plan, nil, fallbackEntries, slices.Clone(plan.base.Items), plan.base.Gist, modelName, nowUTC)
	r.compactAfter = now.Add(flushBackoff(failures))
	return err
}

func (r *Runner) commitFlushWithRebase(
	ctx context.Context,
	plan flushPlan,
	coreUpserts []state.Memory,
	baseLogEntries []state.MemoryLogEntry,
	keptItems []state.SummaryItem,
	gist string,
	modelName string,
	nowUTC time.Time,
) error {
	expected := plan.expected
	base := plan.base
	const maxRebaseAttempts = 3

	for attempt := 0; ; attempt++ {
		next, allLogs := buildNextFlushSummary(base, plan.from, plan.batch, plan.batchThrough, keptItems, gist, modelName, baseLogEntries, nowUTC)
		_, err := r.tasks.Store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			ExpectedSummary: expected,
			NextSummary:     next,
			CoreUpserts:     coreUpserts,
			DailyLogDate:    r.localDate(ctx, nowUTC),
			DailyLogEntries: allLogs,
			CheckPending:    true,
		})
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(err, state.ErrConversationStale) {
			r.compactAfter = r.now().Add(flushBackoff(1))
			return err
		}
		if attempt >= maxRebaseAttempts {
			return err
		}
		// Check whether a user message arrived or another replica already advanced FlushedThrough.
		control, _, statusErr := r.tasks.Store.ConversationStatus(ctx)
		if statusErr != nil || control.Pending() || control.Active != "" || !control.ContextFrom.Equal(plan.from) {
			return state.ErrConversationStale
		}
		latest, sumErr := r.tasks.Store.ConversationSummary(ctx)
		if sumErr != nil || !latest.From.Equal(plan.from) {
			return state.ErrConversationStale
		}
		if !maxTime(plan.from, latest.EffectiveFlushedThrough()).Equal(plan.flushedThrough) ||
			!maxTime(plan.from, latest.EffectiveExtractedThrough()).Equal(plan.effectiveExtracted) {
			return state.ErrConversationStale
		}
		expected = latest
		base = latest
		nowUTC = r.now().UTC()
	}
}

func buildNextFlushSummary(
	base state.ConversationSummary,
	from time.Time,
	batch []state.ConversationTurn,
	batchThrough time.Time,
	keptItems []state.SummaryItem,
	gist string,
	modelName string,
	baseLogEntries []state.MemoryLogEntry,
	nowUTC time.Time,
) (state.ConversationSummary, []state.MemoryLogEntry) {
	filtered := filterDismissedSummaryItems(keptItems, base.DismissedItemIDs)
	next := base
	next.From = from
	next.Items = filtered
	next.Gist = gist
	next.Model = modelName
	next.FlushedThrough = batchThrough
	next.RefinementThrough = batchThrough
	next.ExtractedThrough = maxTime(base.EffectiveExtractedThrough(), next.FlushedThrough)
	if !next.LastExtractFailureThrough.IsZero() && !next.ExtractedThrough.Before(next.LastExtractFailureThrough) {
		next.ExtractFailures = 0
		next.LastExtractFailureThrough = time.Time{}
	}
	next.Through = maxTime(base.Through, batchThrough)
	next.Turns = base.Turns + countTurnsAfter(batch, base.Through)
	next.Excerpts = pruneExcerpts(base.Excerpts, next.FlushedThrough, next.Through)
	next.FlushFailures = 0
	next.LastFailureThrough = time.Time{}
	next.UpdatedAt = nowUTC

	evicted := state.FitSummaryWithArchive(&next, nowUTC)
	allLogs := make([]state.MemoryLogEntry, 0, len(baseLogEntries)+len(evicted))
	allLogs = append(allLogs, baseLogEntries...)
	allLogs = append(allLogs, evicted...)
	return next, allLogs
}

func countTurnsAfter(batch []state.ConversationTurn, after time.Time) int {
	n := 0
	for _, t := range batch {
		if after.IsZero() || t.StartedAt.After(after) {
			n++
		}
	}
	return n
}

func filterDismissedSummaryItems(items []state.SummaryItem, dismissedIDs []string) []state.SummaryItem {
	dismissed := make(map[string]bool, len(dismissedIDs))
	for _, id := range dismissedIDs {
		dismissed[id] = true
	}
	seen := make(map[string]bool, len(items))
	out := make([]state.SummaryItem, 0, len(items))
	for _, item := range items {
		item.ID = state.SummaryItemID(item)
		if dismissed[item.ID] || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		out = append(out, item)
	}
	return out
}

func pruneExcerpts(excerpts []state.ConversationExcerpt, flushedThrough, through time.Time) []state.ConversationExcerpt {
	if !flushedThrough.Before(through) {
		return nil
	}
	kept := make([]state.ConversationExcerpt, 0, len(excerpts))
	for _, ex := range excerpts {
		if !ex.TurnStartedAt.IsZero() && !ex.TurnStartedAt.After(flushedThrough) {
			continue
		}
		kept = append(kept, ex)
	}
	return kept
}

func verifyFlushCoreUpserts(
	candidates []flushCoreUpsertCandidate,
	batchUsers map[string]string,
	batchUserTimes map[string]time.Time,
	existingCore []state.Memory,
	nowUTC time.Time,
) []state.Memory {
	existingByID := make(map[string]state.Memory, len(existingCore))
	for _, m := range existingCore {
		existingByID[m.ID] = m
	}
	byID := map[string]int{}
	out := make([]state.Memory, 0, len(candidates))
	for _, c := range candidates {
		text := strings.TrimSpace(c.Text)
		about := strings.TrimSpace(c.About)
		section := strings.TrimSpace(c.Section)
		inputID := strings.TrimSpace(c.InputID)
		quote := strings.TrimSpace(c.Quote)
		replaces := strings.TrimSpace(c.Replaces)

		sourceText, ok := batchUsers[inputID]
		if !ok || strings.TrimSpace(sourceText) == "" {
			continue
		}
		if quote != "" && !strings.Contains(sourceText, quote) {
			continue
		}
		if text == "" || len(text) > state.MaxMemoryText || !utf8.ValidString(text) {
			continue
		}
		if !utf8.ValidString(about) || !utf8.ValidString(section) {
			continue
		}
		if about == "" {
			about = "the user"
		}
		about = docstore.ClipUTF8(about, state.MaxMemoryAbout)
		section = docstore.ClipUTF8(section, state.MaxMemorySection)

		m := state.Memory{
			Text:          text,
			About:         about,
			Section:       section,
			Provenance:    state.ProvenancePreCompactFlush,
			SourceInputID: inputID,
			SourceText:    state.BoundedSource(sourceText),
			CreatedAt:     nowUTC,
			UpdatedAt:     nowUTC,
		}
		if replaces != "" {
			if existing, exists := existingByID[replaces]; exists {
				if existing.Pinned && !batchUserTimes[inputID].After(existing.UpdatedAt) {
					continue
				}
				m.ID = existing.ID
				m.CreatedAt = existing.CreatedAt
				if m.CreatedAt.After(m.UpdatedAt) {
					m.UpdatedAt = m.CreatedAt
				}
				m.Pinned = existing.Pinned
				m.SupersedesID = existing.ID
				if m.Section == "" {
					m.Section = existing.Section
				}
			}
		}
		if m.ID == "" {
			m.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("flush-core:"+inputID+":"+text)).String()
		}
		if idx, dup := byID[m.ID]; dup {
			out[idx] = m
		} else {
			byID[m.ID] = len(out)
			out = append(out, m)
		}
	}
	return out
}

func verifyFlushLogEntries(
	candidates []flushLogEntryCandidate,
	batchUsers map[string]string,
	batchRuns map[string]bool,
	nowUTC time.Time,
) []state.MemoryLogEntry {
	seen := map[string]bool{}
	out := make([]state.MemoryLogEntry, 0, len(candidates))
	for _, c := range candidates {
		text := strings.TrimSpace(c.Text)
		about := strings.TrimSpace(c.About)
		section := strings.TrimSpace(c.Section)
		inputID := strings.TrimSpace(c.InputID)
		runID := strings.TrimSpace(c.RunID)
		quote := strings.TrimSpace(c.Quote)

		if text == "" || len(text) > state.MaxMemoryText || !utf8.ValidString(text) {
			continue
		}
		if !utf8.ValidString(about) || !utf8.ValidString(section) {
			continue
		}
		if about == "" {
			about = "the user"
		}
		about = docstore.ClipUTF8(about, state.MaxMemoryAbout)
		section = docstore.ClipUTF8(section, state.MaxMemorySection)

		entry := state.MemoryLogEntry{
			Text:      text,
			About:     about,
			Section:   section,
			CreatedAt: nowUTC,
			UpdatedAt: nowUTC,
		}
		if sourceText, ok := batchUsers[inputID]; ok && strings.TrimSpace(sourceText) != "" {
			if quote != "" && !strings.Contains(sourceText, quote) {
				continue
			}
			entry.Provenance = state.ProvenancePreCompactFlush
			entry.SourceInputID = inputID
			entry.SourceText = state.BoundedSource(sourceText)
			if entry.Section == "" {
				entry.Section = "Context"
			}
		} else if runID != "" && batchRuns[runID] {
			entry.Provenance = state.ProvenanceTaskOutcome
			entry.RunID = runID
			if entry.Section == "" || entry.Section == "Context" {
				entry.Section = "Completed Work"
			}
		} else {
			continue
		}

		entry.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("flush-log:"+entry.SourceInputID+":"+entry.RunID+":"+entry.Section+":"+entry.Text)).String()
		if seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		out = append(out, entry)
	}
	return out
}

func extractiveFallbackEntries(batch []state.ConversationTurn, nowUTC time.Time) []state.MemoryLogEntry {
	out := []state.MemoryLogEntry{}
	seen := map[string]bool{}
	for _, t := range batch {
		for _, in := range t.Inputs {
			if !in.IsUser() {
				continue
			}
			text := strings.TrimSpace(in.Text)
			if text == "" || !utf8.ValidString(text) {
				continue
			}
			chunks := splitUTF8Chunks(text, state.MaxMemoryText)
			for idx, chunk := range chunks {
				id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("fallback-log:%s:%d", in.ID, idx))).String()
				if seen[id] {
					continue
				}
				seen[id] = true
				out = append(out, state.MemoryLogEntry{
					ID:            id,
					Section:       "Context",
					Text:          chunk,
					About:         "the user",
					Provenance:    state.ProvenancePreCompactFlush,
					SourceInputID: in.ID,
					SourceText:    state.BoundedSource(text),
					CreatedAt:     nowUTC,
					UpdatedAt:     nowUTC,
				})
			}
		}
	}
	return out
}

func splitUTF8Chunks(s string, maxBytes int) []string {
	if maxBytes <= 0 || len(s) == 0 {
		return nil
	}
	var chunks []string
	for len(s) > 0 {
		part := docstore.ClipUTF8(s, maxBytes)
		if len(part) == 0 {
			break
		}
		chunks = append(chunks, part)
		s = s[len(part):]
	}
	return chunks
}

// advanceHotWindow keeps the live reply path fast (< OuterInputBytes) during high-rate
// bursts by advancing summary.Through and staging lossless ConversationExcerpts for
// (FlushedThrough, Through], while leaving FlushedThrough UNCHANGED so out-of-band
// flushAndCompact catches up later.
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
	turns = slices.DeleteFunc(turns, func(t state.ConversationTurn) bool { return t.Status != "completed" })
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
			return t.Status != "completed" || t.StartedAt.After(newest)
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
		for _, in := range t.Inputs {
			if in.IsUser() && strings.TrimSpace(in.Text) != "" {
				next.Excerpts = append(next.Excerpts, sourceExcerpt(state.UserMessageID(in.ID), "user", in.Text, t.StartedAt))
			}
		}
		if reply := publishedReply(t); reply != "" {
			next.Excerpts = append(next.Excerpts, sourceExcerpt(state.AssistantMessageID(t.ID), "assistant", reply, t.StartedAt))
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

func (r *Runner) callFlushAndCompact(ctx context.Context, prompt string) (flushModelOutput, error) {
	return r.callFlushAndCompactWithTool(ctx, prompt, flushAndCompactTool)
}

func (r *Runner) callFlushAndCompactWithTool(ctx context.Context, prompt string, tool transcript.Tool) (flushModelOutput, error) {
	ctx = usage.WithScope(ctx, "compaction", "compaction")
	if r.Now != nil || r.NewID != nil {
		ctx = usage.WithSession(ctx, usage.Session{Now: r.Now, NewID: r.NewID})
	}
	// Use low reasoning effort and single-turn mode for structured extraction/compaction
	// to cut output/reasoning token spend and latency while preserving schema fidelity.
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

func compactionTurn(t state.ConversationTurn) (map[string]any, map[string]string, map[string]bool) {
	users := map[string]string{}
	runs := map[string]bool{}
	messages := []map[string]string{}
	for _, in := range t.Inputs {
		switch {
		case in.IsUser():
			users[in.ID] = in.Text
			messages = append(messages, map[string]string{"from": "user", "input_id": in.ID, "text": in.Text})
		case in.Event != nil:
			if in.Event.RunID != "" {
				runs[in.Event.RunID] = true
			}
			text := in.Event.Text
			if len(text) > 600 {
				text = docstore.ClipUTF8(text, 600)
			}
			messages = append(messages, map[string]string{"from": "task " + in.Event.RunID, "run_id": in.Event.RunID, "event": in.Event.Kind, "text": strings.ToValidUTF8(text, "")})
		}
	}
	if reply := publishedReply(t); reply != "" {
		messages = append(messages, map[string]string{"from": "Nori", "text": reply})
	}
	return map[string]any{"started_at": t.StartedAt, "messages": messages}, users, runs
}

func publishedReply(t state.ConversationTurn) string {
	for i := len(t.Protocol) - 1; i >= 0; i-- {
		var m struct {
			Role    string  `json:"role"`
			Content *string `json:"content"`
		}
		if json.Unmarshal([]byte(t.Protocol[i]), &m) != nil || m.Role != "assistant" || m.Content == nil {
			continue
		}
		var structured struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal([]byte(*m.Content), &structured) == nil && structured.Text != nil {
			return *structured.Text
		}
		return *m.Content
	}
	return ""
}
