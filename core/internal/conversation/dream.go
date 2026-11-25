package conversation

import (
	"context"
	"encoding/json"
	"errors"
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
	dreamConsolidateToolName = "dream_consolidate"
	dreamLogReadLimit        = 90
	dreamMaxLogBytes         = 24 << 10
)

var dreamConsolidateTool = transcript.Tool{
	Name:        dreamConsolidateToolName,
	Description: prompts.Runtime("dream-consolidate", nil),
	Parameters: &transcript.Schema{
		Type:     transcript.TypeObject,
		Required: []string{"core_upserts", "core_deletes", "daily_log_entries"},
		Properties: map[string]*transcript.Schema{
			"core_upserts": {
				Type: transcript.TypeArray,
				Items: &transcript.Schema{
					Type:     transcript.TypeObject,
					Required: []string{"id", "text", "about", "section", "supersedes_id", "source_input_id"},
					Properties: map[string]*transcript.Schema{
						"id":              {Type: transcript.TypeString, Description: "Existing Core memory ID when merging/updating, or empty string for a newly promoted recurring fact."},
						"text":            {Type: transcript.TypeString, Description: "Consolidated durable fact (1-2000 bytes)."},
						"about":           {Type: transcript.TypeString, Description: "Who this describes: 'the user' or the person named."},
						"section":         {Type: transcript.TypeString, Description: "Category such as Preferences, People, Projects, or Context."},
						"supersedes_id":   {Type: transcript.TypeString, Description: "Optional ID of an older Core memory superseded by this entry, or empty string."},
						"source_input_id": {Type: transcript.TypeString, Description: "Verified user source_input_id from an existing Core memory or user-sourced Daily Log entry."},
					},
				},
			},
			"core_deletes": {
				Type:        transcript.TypeArray,
				Description: "IDs of duplicate, superseded, or stale unpinned Core memories to remove. Pinned memories require explicit contradiction by a newer user input.",
				Items:       &transcript.Schema{Type: transcript.TypeString},
			},
			"daily_log_entries": {
				Type: transcript.TypeArray,
				Items: &transcript.Schema{
					Type:     transcript.TypeObject,
					Required: []string{"text", "about", "section", "source_input_id"},
					Properties: map[string]*transcript.Schema{
						"text":            {Type: transcript.TypeString, Description: "Archived or consolidated episodic note (1-2000 bytes)."},
						"about":           {Type: transcript.TypeString, Description: "Who this describes (default 'the user')."},
						"section":         {Type: transcript.TypeString, Description: "Category such as Archived, Decisions, or Context."},
						"source_input_id": {Type: transcript.TypeString, Description: "Verified source_input_id if derived from a user memory or log entry."},
					},
				},
			},
		},
	},
}

type dreamPlan struct {
	eligible bool
	ready    bool
	wakeAt   time.Time
}

// planDream evaluates the AutoDream triple gate:
// 1) Conversation idle and (!summary.Applies(from) || EffectiveFlushedThrough() >= Through)
// 2) At least DreamMinInterval (12h) since LastDreamAt (or first dream)
// 3) At least DreamMinLogEntries (10) user-sourced Daily Log entries since DreamThrough
// 4) Quiet for IdleDreamDelay (15m) after latestTurnAt
func (r *Runner) planDream(
	ctx context.Context,
	control state.ConversationControl,
	summary state.ConversationSummary,
	latestTurnAt time.Time,
	now time.Time,
) (dreamPlan, error) {
	if r.summarizer == nil || control.Pending() || control.Active != "" {
		return dreamPlan{}, nil
	}
	from := control.ContextFrom
	if summary.Applies(from) && summary.EffectiveFlushedThrough().Before(summary.Through) {
		return dreamPlan{}, nil
	}

	var lastDreamAt, dreamThrough time.Time
	if summary.From.Equal(from) {
		lastDreamAt = summary.LastDreamAt
		dreamThrough = summary.DreamThrough
	}

	logs, err := r.tasks.Store.MemoryLogs(ctx, "", dreamLogReadLimit)
	if err != nil {
		return dreamPlan{}, err
	}
	userEntries := 0
	for _, logDoc := range logs {
		for _, entry := range logDoc.Entries {
			if !from.IsZero() && entry.CreatedAt.Before(from) {
				continue
			}
			if entry.Provenance == state.ProvenanceTaskOutcome {
				continue
			}
			if !dreamThrough.IsZero() && !entry.CreatedAt.After(dreamThrough) {
				continue
			}
			userEntries++
		}
	}
	if userEntries < state.DreamMinLogEntries {
		return dreamPlan{}, nil
	}

	if latestTurnAt.IsZero() {
		latestTurnAt = control.LatestAt
	}
	var dueAt time.Time
	if !latestTurnAt.IsZero() {
		dueAt = latestTurnAt.Add(state.IdleDreamDelay)
	}
	if !lastDreamAt.IsZero() {
		dueAt = maxTime(dueAt, lastDreamAt.Add(state.DreamMinInterval))
	}
	if !dueAt.IsZero() && now.Before(dueAt) {
		return dreamPlan{eligible: true, ready: false, wakeAt: dueAt}, nil
	}
	return dreamPlan{eligible: true, ready: true, wakeAt: now}, nil
}

// maybeDream runs idle AutoDream consolidation when all triple-gate criteria
// and the quiet delay are satisfied, yielding immediately to arriving user input.
func (r *Runner) maybeDream(ctx context.Context) {
	if r.summarizer == nil || r.now().Before(r.dreamAfter) {
		return
	}
	control, _, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return
	}
	if blocked, err := r.backgroundIdleBlocked(ctx, control); err != nil || blocked {
		return
	}
	summary, err := r.tasks.Store.ConversationSummary(ctx)
	if err != nil {
		return
	}
	now := r.now()
	flushState, err := r.planFlush(ctx, control, summary, now, false)
	if err != nil || flushState.ready {
		return
	}
	extractState, err := r.planExtract(ctx, control, summary, now, false)
	if err != nil || extractState.ready {
		return
	}
	latestTurnAt := maxTime(flushState.latestTurnAt, extractState.latestTurnAt)
	dp, err := r.planDream(ctx, control, summary, latestTurnAt, now)
	if err != nil || !dp.ready {
		return
	}
	if _, err := r.dreamConsolidate(ctx, false); err != nil && ctx.Err() == nil && !errors.Is(err, state.ErrConversationStale) {
		slog.WarnContext(ctx, "idle autodream consolidation failed", "err", err)
		r.dreamAfter = r.now().Add(compactionPause)
	}
}

type dreamCoreUpsertCandidate struct {
	ID            string `json:"id"`
	Text          string `json:"text"`
	About         string `json:"about"`
	Section       string `json:"section"`
	SupersedesID  string `json:"supersedes_id"`
	SourceInputID string `json:"source_input_id"`
}

type dreamDeleteCandidate struct {
	MemoryID      string
	SourceInputID string
}

func (d *dreamDeleteCandidate) UnmarshalJSON(data []byte) error {
	var id string
	if err := json.Unmarshal(data, &id); err == nil {
		d.MemoryID = strings.TrimSpace(id)
		return nil
	}
	var obj struct {
		ID                    string `json:"id"`
		MemoryID              string `json:"memory_id"`
		SourceInputID         string `json:"source_input_id"`
		ContradictedByInputID string `json:"contradicted_by_input_id"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	if obj.MemoryID != "" {
		d.MemoryID = strings.TrimSpace(obj.MemoryID)
	} else {
		d.MemoryID = strings.TrimSpace(obj.ID)
	}
	if obj.ContradictedByInputID != "" {
		d.SourceInputID = strings.TrimSpace(obj.ContradictedByInputID)
	} else {
		d.SourceInputID = strings.TrimSpace(obj.SourceInputID)
	}
	return nil
}

type dreamLogEntryCandidate struct {
	Text          string `json:"text"`
	About         string `json:"about"`
	Section       string `json:"section"`
	SourceInputID string `json:"source_input_id"`
}

type dreamModelOutput struct {
	CoreUpserts     []dreamCoreUpsertCandidate `json:"core_upserts"`
	CoreDeletes     []dreamDeleteCandidate     `json:"core_deletes"`
	DailyLogEntries []dreamLogEntryCandidate   `json:"daily_log_entries"`
}

type dreamSource struct {
	Text      string
	CreatedAt time.Time
}

func (r *Runner) dreamConsolidate(ctx context.Context, manual bool) (state.ConversationSummary, error) {
	if r.summarizer == nil {
		return state.ConversationSummary{}, errors.New("memory consolidation model is unavailable")
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if !manual {
		runCtx, cancel = context.WithCancel(ctx)
		defer cancel()
		done := make(chan struct{})
		defer close(done)
		go r.runPeriodic(runCtx, done, 500*time.Millisecond, func() bool {
			if pending, err := r.tasks.Store.UserRoutingPending(runCtx); err == nil && pending {
				cancel()
				return false
			}
			return true
		})
	}

	control, _, err := r.tasks.Store.ConversationStatus(runCtx)
	if err != nil {
		return state.ConversationSummary{}, err
	}
	if !manual && (control.Pending() || control.Active != "") {
		return state.ConversationSummary{}, state.ErrConversationStale
	}
	summary, err := r.tasks.Store.ConversationSummary(runCtx)
	if err != nil {
		return state.ConversationSummary{}, err
	}
	from := control.ContextFrom
	var dreamThrough time.Time
	if summary.From.Equal(from) {
		dreamThrough = summary.DreamThrough
	}

	coreMemories, err := r.tasks.Store.Memories(runCtx)
	if err != nil {
		return state.ConversationSummary{}, err
	}
	state.SortCoreMemories(coreMemories)

	logs, err := r.tasks.Store.MemoryLogs(runCtx, "", dreamLogReadLimit)
	if err != nil {
		return state.ConversationSummary{}, err
	}

	// Build verified user-sourced provenance table; never include task_outcome entries.
	verifiedSources := map[string]dreamSource{}
	addSource := func(id, text string, ts time.Time) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if prev, ok := verifiedSources[id]; !ok || ts.After(prev.CreatedAt) {
			if text == "" {
				text = prev.Text
			}
			verifiedSources[id] = dreamSource{Text: text, CreatedAt: ts}
		}
	}
	for _, m := range coreMemories {
		if m.EffectiveProvenance() == state.ProvenanceTaskOutcome {
			continue
		}
		addSource(m.SourceInputID, m.SourceText, maxTime(m.CreatedAt, m.UpdatedAt))
	}

	type promptLogEntry struct {
		Date          string                 `json:"date"`
		ID            string                 `json:"id"`
		Section       string                 `json:"section,omitempty"`
		Text          string                 `json:"text"`
		About         string                 `json:"about,omitempty"`
		Provenance    state.MemoryProvenance `json:"provenance"`
		SourceInputID string                 `json:"source_input_id,omitempty"`
		CreatedAt     time.Time              `json:"created_at"`
	}
	promptLogs := []promptLogEntry{}
	for _, logDoc := range logs {
		for _, entry := range logDoc.Entries {
			if !from.IsZero() && entry.CreatedAt.Before(from) {
				continue
			}
			if entry.Provenance == state.ProvenanceTaskOutcome {
				continue
			}
			addSource(entry.SourceInputID, entry.SourceText, entry.CreatedAt)
			if !manual && !dreamThrough.IsZero() && !entry.CreatedAt.After(dreamThrough) {
				continue
			}
			promptLogs = append(promptLogs, promptLogEntry{
				Date:          logDoc.Date,
				ID:            entry.ID,
				Section:       entry.Section,
				Text:          entry.Text,
				About:         entry.About,
				Provenance:    entry.Provenance,
				SourceInputID: entry.SourceInputID,
				CreatedAt:     entry.CreatedAt,
			})
		}
	}

	// Process bounded log batches in chronological oldest-first order so DreamThrough
	// never jumps past unconsolidated older entries.
	slices.SortFunc(promptLogs, func(a, b promptLogEntry) int {
		if a.CreatedAt.Before(b.CreatedAt) {
			return -1
		}
		if a.CreatedAt.After(b.CreatedAt) {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	selectedLogs := make([]promptLogEntry, 0, len(promptLogs))
	selectedBytes := 0
	logsTruncated := false
	for _, entry := range promptLogs {
		raw, _ := json.Marshal(entry)
		if len(selectedLogs) > 0 && selectedBytes+len(raw) > dreamMaxLogBytes {
			logsTruncated = true
			break
		}
		selectedLogs = append(selectedLogs, entry)
		selectedBytes += len(raw)
	}

	now := r.now()
	loc := r.userLocation(runCtx)
	promptPayload := map[string]any{
		"core_memories":     projectMaintenanceMemories(coreMemories, true),
		"daily_logs":        selectedLogs,
		"local_date":        now.In(loc).Format("2006-01-02"),
		"timezone":          loc.String(),
		"core_budget_bytes": state.CoreMemoryBytes,
	}
	promptBytes, _ := json.Marshal(promptPayload)

	modelCtx, modelCancel := context.WithTimeout(llmguard.WithScope(runCtx, llmguard.ScopeDream), 2*time.Minute)
	defer modelCancel()
	output, err := r.callDreamConsolidate(modelCtx, string(promptBytes))
	if err != nil {
		return state.ConversationSummary{}, err
	}

	nowUTC := r.now().UTC()
	processedThrough := nowUTC
	if logsTruncated && len(selectedLogs) > 0 {
		// Advance DreamThrough only through the batch actually processed so remaining
		// newer entries are picked up on the next consolidation cycle.
		processedThrough = selectedLogs[len(selectedLogs)-1].CreatedAt.UTC()
	}
	coreUpserts, coreDeletes, dailyLogEntries := verifyDreamOutput(output, coreMemories, verifiedSources, nowUTC)

	expected := summary
	const maxManualRetries = 2
	for attempt := 0; ; attempt++ {
		next := buildNextDreamSummary(expected, from, processedThrough, nowUTC)
		committed, err := r.tasks.Store.CommitDreamConsolidate(runCtx, state.DreamConsolidateRequest{
			ExpectedSummary: expected,
			NextSummary:     next,
			CoreUpserts:     coreUpserts,
			CoreDeletes:     coreDeletes,
			DailyLogDate:    r.localDate(runCtx, nowUTC),
			DailyLogEntries: dailyLogEntries,
			CheckPending:    !manual,
		})
		if err == nil {
			slog.InfoContext(runCtx, "memory dream consolidation completed",
				"manual", manual,
				"core_upserts", len(coreUpserts),
				"core_deletes", len(coreDeletes),
				"log_entries", len(dailyLogEntries),
			)
			return committed, nil
		}
		if !manual || !errors.Is(err, state.ErrConversationStale) || attempt >= maxManualRetries {
			return state.ConversationSummary{}, err
		}
		latest, sumErr := r.tasks.Store.ConversationSummary(runCtx)
		if sumErr != nil {
			return state.ConversationSummary{}, sumErr
		}
		expected = latest
		nowUTC = r.now().UTC()
	}
}

func buildNextDreamSummary(base state.ConversationSummary, from, processedThrough, nowUTC time.Time) state.ConversationSummary {
	next := base
	if !base.From.Equal(from) {
		next = state.ConversationSummary{From: from, Items: []state.SummaryItem{}}
	}
	if next.Items == nil {
		next.Items = []state.SummaryItem{}
	}
	next.DreamThrough = processedThrough
	next.LastDreamAt = nowUTC
	next.UpdatedAt = nowUTC
	return next
}

func isConversationalContradiction(sourceInputID string, src dreamSource, existing state.Memory) bool {
	return sourceInputID != "" && sourceInputID != "ui" && src.CreatedAt.After(existing.UpdatedAt)
}

func verifyDreamOutput(
	output dreamModelOutput,
	existingCore []state.Memory,
	verifiedSources map[string]dreamSource,
	nowUTC time.Time,
) ([]state.Memory, []string, []state.MemoryLogEntry) {
	existingByID := make(map[string]state.Memory, len(existingCore))
	for _, m := range existingCore {
		existingByID[m.ID] = m
	}

	upsertsByID := map[string]int{}
	coreUpserts := make([]state.Memory, 0, len(output.CoreUpserts))
	pinnedContradicted := map[string]bool{}

	for _, c := range output.CoreUpserts {
		id := strings.TrimSpace(c.ID)
		text := strings.TrimSpace(c.Text)
		about := strings.TrimSpace(c.About)
		section := strings.TrimSpace(c.Section)
		supersedesID := strings.TrimSpace(c.SupersedesID)
		sourceInputID := strings.TrimSpace(c.SourceInputID)

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

		existing, isUpdate := existingByID[id]
		if isUpdate && sourceInputID == "" && existing.EffectiveProvenance() != state.ProvenanceTaskOutcome {
			sourceInputID = existing.SourceInputID
		}
		src, ok := verifiedSources[sourceInputID]
		if !ok {
			continue
		}

		// Protect pinned Core memories unless contradicted by a strictly newer cited user conversation input.
		if isUpdate && existing.Pinned && !isConversationalContradiction(sourceInputID, src, existing) {
			continue
		}

		if supersedesID != "" {
			if sup, exists := existingByID[supersedesID]; exists {
				if sup.Pinned && isConversationalContradiction(sourceInputID, src, sup) {
					pinnedContradicted[sup.ID] = true
				}
			} else {
				supersedesID = ""
			}
		}

		m := state.Memory{
			Text:          text,
			About:         about,
			Section:       section,
			Provenance:    state.ProvenanceDreamCurated,
			SourceInputID: sourceInputID,
			SourceText:    state.BoundedSource(src.Text),
			SupersedesID:  supersedesID,
			CreatedAt:     nowUTC,
			UpdatedAt:     nowUTC,
		}
		if isUpdate {
			m.ID = existing.ID
			m.CreatedAt = existing.CreatedAt
			if m.CreatedAt.After(m.UpdatedAt) {
				m.UpdatedAt = m.CreatedAt
			}
			m.Pinned = existing.Pinned
			if m.Section == "" {
				m.Section = existing.Section
			}
		} else {
			m.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("dream-core:"+sourceInputID+":"+text)).String()
		}

		if idx, dup := upsertsByID[m.ID]; dup {
			coreUpserts[idx] = m
		} else {
			upsertsByID[m.ID] = len(coreUpserts)
			coreUpserts = append(coreUpserts, m)
		}
	}

	seenDeletes := map[string]bool{}
	coreDeletes := make([]string, 0, len(output.CoreDeletes))
	for _, d := range output.CoreDeletes {
		id := d.MemoryID
		if id == "" || seenDeletes[id] {
			continue
		}
		if _, upserted := upsertsByID[id]; upserted {
			continue
		}
		existing, exists := existingByID[id]
		if !exists {
			continue
		}
		if existing.Pinned {
			contradicted := pinnedContradicted[id]
			if !contradicted && d.SourceInputID != "" && d.SourceInputID != "ui" {
				if src, ok := verifiedSources[d.SourceInputID]; ok && isConversationalContradiction(d.SourceInputID, src, existing) {
					contradicted = true
				}
			}
			if !contradicted {
				continue
			}
		}
		seenDeletes[id] = true
		coreDeletes = append(coreDeletes, id)
	}
	slices.Sort(coreDeletes)

	seenLogs := map[string]bool{}
	dailyLogEntries := make([]state.MemoryLogEntry, 0, len(output.DailyLogEntries))
	for _, c := range output.DailyLogEntries {
		text := strings.TrimSpace(c.Text)
		about := strings.TrimSpace(c.About)
		section := strings.TrimSpace(c.Section)
		sourceInputID := strings.TrimSpace(c.SourceInputID)

		if text == "" || len(text) > state.MaxMemoryText || !utf8.ValidString(text) {
			continue
		}
		if !utf8.ValidString(about) || !utf8.ValidString(section) {
			continue
		}
		if about == "" {
			about = "the user"
		}
		if section == "" {
			section = "Archived"
		}
		about = docstore.ClipUTF8(about, state.MaxMemoryAbout)
		section = docstore.ClipUTF8(section, state.MaxMemorySection)

		var sourceText string
		if src, ok := verifiedSources[sourceInputID]; ok {
			sourceText = state.BoundedSource(src.Text)
		} else {
			sourceInputID = ""
		}

		entry := state.MemoryLogEntry{
			ID:            uuid.NewSHA1(uuid.NameSpaceOID, []byte("dream-log:"+sourceInputID+":"+section+":"+text)).String(),
			Section:       section,
			Text:          text,
			About:         about,
			Provenance:    state.ProvenanceDreamCurated,
			SourceInputID: sourceInputID,
			SourceText:    sourceText,
			CreatedAt:     nowUTC,
			UpdatedAt:     nowUTC,
		}
		if seenLogs[entry.ID] {
			continue
		}
		seenLogs[entry.ID] = true
		dailyLogEntries = append(dailyLogEntries, entry)
	}

	return coreUpserts, coreDeletes, dailyLogEntries
}

func (r *Runner) callDreamConsolidate(ctx context.Context, prompt string) (dreamModelOutput, error) {
	ctx = usage.WithScope(ctx, "dream", "dream")
	if r.Now != nil || r.NewID != nil {
		ctx = usage.WithSession(ctx, usage.Session{Now: r.Now, NewID: r.NewID})
	}
	response, err := r.summarizer.Respond(ctx, agent.Request{
		Instructions:    prompts.Runtime("dream", nil),
		Tools:           []transcript.Tool{dreamConsolidateTool},
		MaxOutputTokens: 4096,
		Reasoning:       "low",
		SingleTurn:      true,
		Messages:        []transcript.Message{{Role: transcript.RoleUser, Parts: []transcript.Part{{Text: prompt}}}},
	})
	if err != nil {
		return dreamModelOutput{}, err
	}
	if len(response.Calls) != 1 || response.Calls[0].Name != dreamConsolidateToolName {
		return dreamModelOutput{}, errors.New("consolidation model didn't call dream_consolidate")
	}
	data, _ := json.Marshal(response.Calls[0].Args)
	var out dreamModelOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return dreamModelOutput{}, errors.New("consolidation model returned an invalid dream_consolidate payload")
	}
	return out, nil
}
