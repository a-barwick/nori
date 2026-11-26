package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"nori/internal/llmguard"
	"nori/internal/state"
)

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
	Items           []state.SummaryItem        `json:"items"`
	Gist            string                     `json:"gist"`
}

func isUserTurn(t state.ConversationTurn) bool {
	return slices.ContainsFunc(t.Inputs, state.ConversationInput.IsUser)
}

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
		if t.Status != state.TurnCompleted {
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
			if t.Status != state.TurnCompleted {
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
		r.setCompactAfter(now.Add(flushBackoff(failures)))
		return callErr
	}

	slog.WarnContext(ctx, "flush_and_compact reached max retries; committing lossless extractive fallback",
		"failures", failures, "batch_through", plan.batchThrough, "err", callErr)
	fallbackEntries := extractiveFallbackEntries(unextractedTurns(plan.batch, plan.effectiveExtracted), nowUTC)
	modelName := plan.base.Model
	if modelName == "" {
		modelName = "extractive"
	}
	err := r.commitFlushWithRebase(ctx, plan, nil, fallbackEntries, slices.Clone(plan.base.Items), plan.base.Gist, modelName, nowUTC)
	r.setCompactAfter(now.Add(flushBackoff(failures)))
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
			r.setCompactAfter(r.now().Add(flushBackoff(1)))
			return err
		}
		if attempt >= maxRebaseAttempts {
			return err
		}
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
