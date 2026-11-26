package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode"

	"nori/internal/llmguard"
	"nori/internal/state"
)

var pureAckPhrases = map[string]bool{
	"thanks": true, "thank you": true, "thx": true, "ty": true, "tysm": true,
	"got it": true, "cool": true, "nice": true, "awesome": true,
	"np": true, "no problem": true, "cheers": true,
	"🙏": true, "❤️": true, "❤": true, "🎉": true, "🙌": true,
}

func isPureAckText(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	cleaned := strings.TrimRightFunc(strings.ToLower(trimmed), func(r rune) bool {
		return unicode.IsSpace(r) || r == '!' || r == '.' || r == ','
	})
	if cleaned == "" {
		return false
	}
	return pureAckPhrases[cleaned]
}

func isPureAckInputs(inputs []state.ConversationInput) bool {
	if len(inputs) == 0 {
		return false
	}
	hasUser := false
	for _, in := range inputs {
		if in.Event != nil || in.Introduction || len(in.Attachments) > 0 {
			return false
		}
		if in.IsUser() {
			hasUser = true
			if !isPureAckText(in.Text) {
				return false
			}
		}
	}
	return hasUser
}

func isPureAckBatch(batch []state.ConversationTurn) bool {
	if len(batch) == 0 {
		return false
	}
	for _, t := range batch {
		if !isPureAckInputs(t.Inputs) {
			return false
		}
	}
	return true
}

type extractPlan struct {
	from             time.Time
	expected         state.ConversationSummary
	base             state.ConversationSummary
	extractedThrough time.Time
	hasWork          bool
	ready            bool
	wakeAt           time.Time
	latestTurnAt     time.Time
	batch            []state.ConversationTurn
	batchThrough     time.Time
}

func (r *Runner) planExtract(
	ctx context.Context,
	control state.ConversationControl,
	summary state.ConversationSummary,
	now time.Time,
	collectBatch bool,
) (extractPlan, error) {
	from := control.ContextFrom
	plan := extractPlan{
		from:         from,
		expected:     summary,
		latestTurnAt: control.LatestAt,
	}
	if summary.From.Equal(from) {
		plan.base = summary
		if plan.base.Items == nil {
			plan.base.Items = []state.SummaryItem{}
		}
		plan.extractedThrough = maxTime(from, summary.EffectiveExtractedThrough())
	} else {
		plan.base = state.ConversationSummary{From: from, Items: []state.SummaryItem{}}
		plan.extractedThrough = from
	}

	newestRaw, err := r.tasks.Store.ConversationTurnsAfter(ctx, plan.extractedThrough)
	if err != nil {
		return plan, err
	}
	pageFull := len(newestRaw) == state.HistoryTurnsExamined
	completed := make([]state.ConversationTurn, 0, len(newestRaw))
	userTurns := 0
	for _, t := range newestRaw {
		if t.Status != "completed" {
			continue
		}
		completed = append(completed, t)
		if isUserTurn(t) {
			userTurns++
		}
	}
	if len(completed) == 0 && !pageFull {
		return plan, nil
	}
	if len(completed) > 0 && completed[0].StartedAt.After(plan.latestTurnAt) {
		plan.latestTurnAt = completed[0].StartedAt
	}

	plan.hasWork = true
	if userTurns >= state.ExtractTurnThreshold || pageFull {
		plan.ready = true
	} else {
		dueAt := plan.latestTurnAt.Add(state.IdleExtractDelay)
		if !plan.latestTurnAt.IsZero() && now.Before(dueAt) {
			plan.wakeAt = dueAt
			return plan, nil
		}
		plan.ready = true
	}

	if !collectBatch {
		return plan, nil
	}

	var older []state.ConversationTurn
	if pageFull {
		oldestRaw, err := r.tasks.Store.OldestConversationTurnsAfter(ctx, plan.extractedThrough)
		if err != nil {
			return plan, err
		}
		older = make([]state.ConversationTurn, 0, len(oldestRaw))
		for _, t := range oldestRaw {
			if t.Status != "completed" {
				continue
			}
			older = append(older, t)
		}
	} else {
		older = slices.Clone(completed)
		slices.Reverse(older)
	}

	inputBytes := 0
	for _, t := range older {
		view, _, _ := compactionTurn(t)
		view["already_extracted"] = false
		data, _ := json.Marshal(view)
		if len(plan.batch) > 0 && (len(plan.batch) >= state.CompactTurnLimit || inputBytes+len(data) > state.CompactInputBytes) {
			break
		}
		inputBytes += len(data)
		plan.batch = append(plan.batch, t)
	}
	if len(plan.batch) > 0 {
		plan.batchThrough = plan.batch[len(plan.batch)-1].StartedAt
	}
	return plan, nil
}

func (r *Runner) extractIdleMemory(ctx context.Context) error {
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

	flushState, err := r.planFlush(ctx, control, summary, now, false)
	if err != nil || flushState.ready {
		return err
	}

	plan, err := r.planExtract(ctx, control, summary, now, true)
	if err != nil || !plan.ready || len(plan.batch) == 0 || !plan.batchThrough.After(plan.extractedThrough) {
		return err
	}

	if isPureAckBatch(plan.batch) {
		nowUTC := r.now().UTC()
		if err := r.commitExtractWithRebase(ctx, plan, nil, nil, nowUTC); err != nil {
			return err
		}
		slog.InfoContext(ctx, "conversation idle memory extraction skipped pure-ack batch",
			"turns", len(plan.batch),
			"extracted_through", plan.batchThrough,
		)
		return nil
	}

	coreMemories, err := r.tasks.Store.Memories(ctx)
	if err != nil {
		return err
	}
	state.SortCoreMemories(coreMemories)

	batchUsers := map[string]string{}
	batchUserTimes := map[string]time.Time{}
	batchRuns := map[string]bool{}
	rendered := make([]any, 0, len(plan.batch))
	for _, t := range plan.batch {
		view, users, runs := compactionTurn(t)
		view["already_extracted"] = false
		rendered = append(rendered, view)
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
		"extract_only":  true,
		"core_memories": projectMaintenanceMemories(coreMemories, false),
		"local_date":    now.In(loc).Format("2006-01-02"),
		"timezone":      loc.String(),
		"turns":         rendered,
	}
	promptBytes, _ := json.Marshal(promptPayload)

	modelCtx, cancel := context.WithTimeout(llmguard.WithScope(ctx, llmguard.ScopeExtract), 2*time.Minute)
	defer cancel()
	output, callErr := r.callFlushAndCompactWithTool(modelCtx, string(promptBytes), extractOnlyTool)
	if callErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return r.handleExtractFailure(ctx, plan, callErr)
	}

	nowUTC := r.now().UTC()
	coreUpserts := verifyFlushCoreUpserts(output.CoreUpserts, batchUsers, batchUserTimes, coreMemories, nowUTC)
	logEntries := verifyFlushLogEntries(output.DailyLogEntries, batchUsers, batchRuns, nowUTC)

	if err := r.commitExtractWithRebase(ctx, plan, coreUpserts, logEntries, nowUTC); err != nil {
		return err
	}
	slog.InfoContext(ctx, "conversation idle memory extraction completed",
		"turns", len(plan.batch),
		"core_candidates", len(output.CoreUpserts),
		"core_upserts", len(coreUpserts),
		"log_candidates", len(output.DailyLogEntries),
		"log_entries", len(logEntries),
		"extracted_through", plan.batchThrough,
	)
	return nil
}

func (r *Runner) handleExtractFailure(ctx context.Context, plan extractPlan, callErr error) error {
	now := r.now()
	nowUTC := now.UTC()
	failures := 1
	if plan.base.ExtractFailures > 0 && !plan.base.LastExtractFailureThrough.IsZero() && !plan.extractedThrough.After(plan.base.LastExtractFailureThrough) {
		failures = plan.base.ExtractFailures + 1
	}

	if failures < state.MaxFlushRetries {
		next := plan.base
		next.From = plan.from
		next.ExtractFailures = failures
		next.LastExtractFailureThrough = plan.batchThrough
		next.UpdatedAt = nowUTC
		if err := r.tasks.Store.SaveConversationSummary(ctx, next, plan.expected); err != nil && !errors.Is(err, state.ErrConversationStale) {
			slog.WarnContext(ctx, "failed to record idle extract retry counter", "err", err)
		}
		r.extractAfter = now.Add(flushBackoff(failures))
		return callErr
	}

	slog.WarnContext(ctx, "idle memory extraction reached max retries; committing lossless extractive fallback",
		"failures", failures, "batch_through", plan.batchThrough, "err", callErr)
	fallbackEntries := extractiveFallbackEntries(plan.batch, nowUTC)
	err := r.commitExtractWithRebase(ctx, plan, nil, fallbackEntries, nowUTC)
	r.extractAfter = now.Add(flushBackoff(failures))
	return err
}

func (r *Runner) commitExtractWithRebase(
	ctx context.Context,
	plan extractPlan,
	coreUpserts []state.Memory,
	logEntries []state.MemoryLogEntry,
	nowUTC time.Time,
) error {
	expected := plan.expected
	base := plan.base
	const maxRebaseAttempts = 3

	for attempt := 0; ; attempt++ {
		next := buildNextExtractSummary(base, plan.from, plan.batchThrough, nowUTC)
		_, err := r.tasks.Store.CommitFlushAndCompact(ctx, state.FlushAndCompactRequest{
			ExpectedSummary: expected,
			NextSummary:     next,
			CoreUpserts:     coreUpserts,
			DailyLogDate:    r.localDate(ctx, nowUTC),
			DailyLogEntries: logEntries,
			CheckPending:    true,
		})
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !errors.Is(err, state.ErrConversationStale) {
			r.extractAfter = r.now().Add(flushBackoff(1))
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
		if !maxTime(plan.from, latest.EffectiveExtractedThrough()).Equal(plan.extractedThrough) {
			return state.ErrConversationStale
		}
		expected = latest
		base = latest
		if base.Items == nil {
			base.Items = []state.SummaryItem{}
		}
		nowUTC = r.now().UTC()
	}
}

func buildNextExtractSummary(
	base state.ConversationSummary,
	from time.Time,
	batchThrough time.Time,
	nowUTC time.Time,
) state.ConversationSummary {
	next := base
	next.From = from
	if next.Items == nil {
		next.Items = []state.SummaryItem{}
	}
	next.ExtractedThrough = maxTime(base.EffectiveExtractedThrough(), batchThrough)
	next.ExtractFailures = 0
	next.LastExtractFailureThrough = time.Time{}
	next.UpdatedAt = nowUTC
	return next
}

func recordBatchUserTimes(t state.ConversationTurn, batchUserTimes map[string]time.Time) {
	for _, in := range t.Inputs {
		if !in.IsUser() || in.ID == "" {
			continue
		}
		ts := in.CreatedAt
		if ts.IsZero() {
			ts = t.StartedAt
		}
		if prev, ok := batchUserTimes[in.ID]; !ok || ts.After(prev) {
			batchUserTimes[in.ID] = ts
		}
	}
}

func unextractedTurns(batch []state.ConversationTurn, effectiveExtracted time.Time) []state.ConversationTurn {
	out := make([]state.ConversationTurn, 0, len(batch))
	for _, t := range batch {
		if t.StartedAt.After(effectiveExtracted) {
			out = append(out, t)
		}
	}
	return out
}
