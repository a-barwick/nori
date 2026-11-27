package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"nori/internal/conversation"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

const (
	defaultMemoryLogsLimit = 30
	maxMemoryLogsLimit     = 100
	maxMemorySearchQuery   = 400
)

type memoryFactEditable struct {
	Text    string `json:"text"`
	About   string `json:"about,omitempty"`
	Section string `json:"section,omitempty"`
	Pinned  *bool  `json:"pinned,omitempty"`
}

func (v memoryFactEditable) valid() bool {
	if strings.TrimSpace(v.Text) == "" || len(v.Text) > state.MaxMemoryText || !utf8.ValidString(v.Text) {
		return false
	}
	if len(v.About) > state.MaxMemoryAbout || !utf8.ValidString(v.About) {
		return false
	}
	if len(v.Section) > state.MaxMemorySection || !utf8.ValidString(v.Section) {
		return false
	}
	return true
}

type memoryActionValue struct {
	Action string `json:"action"`
}

func (v memoryActionValue) validItemAction() bool {
	switch v.Action {
	case "forget", "pin", "unpin", "demote":
		return true
	default:
		return false
	}
}

type memoryFact struct {
	ID           string                 `json:"id"`
	Revision     int64                  `json:"revision"`
	Text         string                 `json:"text"`
	About        string                 `json:"about,omitempty"`
	Section      string                 `json:"section,omitempty"`
	Pinned       bool                   `json:"pinned,omitempty"`
	Provenance   state.MemoryProvenance `json:"provenance,omitempty"`
	SourceText   string                 `json:"source_text,omitempty"`
	SupersedesID string                 `json:"supersedes_id,omitempty"`
	Scope        string                 `json:"scope"`
	Origin       map[string]string      `json:"origin"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    *time.Time             `json:"updated_at,omitempty"`
}

type memoryLogEntryView struct {
	ID            string                 `json:"id"`
	Section       string                 `json:"section,omitempty"`
	Text          string                 `json:"text"`
	About         string                 `json:"about,omitempty"`
	Provenance    state.MemoryProvenance `json:"provenance"`
	SourceInputID string                 `json:"source_input_id,omitempty"`
	SourceText    string                 `json:"source_text,omitempty"`
	RunID         string                 `json:"run_id,omitempty"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     *time.Time             `json:"updated_at,omitempty"`
}

type memoryLogView struct {
	Date      string               `json:"date"`
	Revision  int64                `json:"revision"`
	Entries   []memoryLogEntryView `json:"entries"`
	UpdatedAt time.Time            `json:"updated_at"`
}

type memoryLogEntryActionValue struct {
	Action  string `json:"action"`
	Text    string `json:"text,omitempty"`
	About   string `json:"about,omitempty"`
	Section string `json:"section,omitempty"`
	Pinned  *bool  `json:"pinned,omitempty"`
}

func (v memoryLogEntryActionValue) valid() bool {
	switch v.Action {
	case "promote":
		if v.Text != "" && (strings.TrimSpace(v.Text) == "" || len(v.Text) > state.MaxMemoryText || !utf8.ValidString(v.Text)) {
			return false
		}
		if len(v.About) > state.MaxMemoryAbout || !utf8.ValidString(v.About) {
			return false
		}
		if len(v.Section) > state.MaxMemorySection || !utf8.ValidString(v.Section) {
			return false
		}
		return true
	case "forget":
		return v.Text == "" && v.About == "" && v.Section == "" && v.Pinned == nil
	default:
		return false
	}
}

type workingMemoryItemView struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Quote   string `json:"quote"`
	InputID string `json:"input_id"`
	Note    string `json:"note,omitempty"`
}

type workingMemoryView struct {
	Revision         int64                   `json:"revision"`
	From             *time.Time              `json:"from,omitempty"`
	ExtractedThrough *time.Time              `json:"extracted_through,omitempty"`
	FlushedThrough   *time.Time              `json:"flushed_through,omitempty"`
	Through          *time.Time              `json:"through,omitempty"`
	DreamThrough     *time.Time              `json:"dream_through,omitempty"`
	LastDreamAt      *time.Time              `json:"last_dream_at,omitempty"`
	Items            []workingMemoryItemView `json:"items"`
	Gist             string                  `json:"gist"`
	Turns            int                     `json:"turns"`
	Model            string                  `json:"model,omitempty"`
	CoreBytes        int                     `json:"core_bytes"`
	CoreBudgetBytes  int                     `json:"core_budget_bytes"`
	UpdatedAt        *time.Time              `json:"updated_at,omitempty"`
}

type workingMemoryItemActionValue struct {
	Action  string `json:"action"`
	Text    string `json:"text,omitempty"`
	About   string `json:"about,omitempty"`
	Section string `json:"section,omitempty"`
	Pinned  *bool  `json:"pinned,omitempty"`
}

func (v workingMemoryItemActionValue) valid() bool {
	switch v.Action {
	case "promote":
		if v.Text != "" && (strings.TrimSpace(v.Text) == "" || len(v.Text) > state.MaxMemoryText || !utf8.ValidString(v.Text)) {
			return false
		}
		if len(v.About) > state.MaxMemoryAbout || !utf8.ValidString(v.About) {
			return false
		}
		if len(v.Section) > state.MaxMemorySection || !utf8.ValidString(v.Section) {
			return false
		}
		return true
	case "dismiss":
		return v.Text == "" && v.About == "" && v.Section == "" && v.Pinned == nil
	default:
		return false
	}
}

type memorySearchHitView struct {
	ChunkID    string                 `json:"chunk_id"`
	Layer      string                 `json:"layer"`
	MemoryID   string                 `json:"memory_id,omitempty"`
	LogDate    string                 `json:"log_date,omitempty"`
	EntryID    string                 `json:"entry_id,omitempty"`
	Section    string                 `json:"section,omitempty"`
	About      string                 `json:"about,omitempty"`
	Snippet    string                 `json:"snippet"`
	Provenance state.MemoryProvenance `json:"provenance"`
	Pinned     bool                   `json:"pinned,omitempty"`
	Score      float64                `json:"score"`
	CreatedAt  time.Time              `json:"created_at"`
	UpdatedAt  *time.Time             `json:"updated_at,omitempty"`
}

type memorySearchResponse struct {
	Items      []memorySearchHitView `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
	Query      string                `json:"query,omitempty"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t
	return &u
}

func effectiveMemoryProvenance(p state.MemoryProvenance) state.MemoryProvenance {
	if p == "" {
		return state.ProvenanceUserExplicit
	}
	return p
}

func memoryView(m state.Memory) memoryFact {
	origin := map[string]string{}
	if m.SourceInputID != "" && m.SourceInputID != "ui" {
		origin["conversation_id"], origin["message_id"] = "assistant", m.SourceInputID
	}
	return memoryFact{
		ID:           m.ID,
		Revision:     m.UpdatedAt.UnixMilli(),
		Text:         m.Text,
		About:        m.About,
		Section:      m.Section,
		Pinned:       m.Pinned,
		Provenance:   m.EffectiveProvenance(),
		SourceText:   m.SourceText,
		SupersedesID: m.SupersedesID,
		Scope:        "user",
		Origin:       origin,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    timePtr(m.UpdatedAt),
	}
}

func memoryLogEntryToView(e state.MemoryLogEntry) memoryLogEntryView {
	return memoryLogEntryView{
		ID:            e.ID,
		Section:       e.Section,
		Text:          e.Text,
		About:         e.About,
		Provenance:    effectiveMemoryProvenance(e.Provenance),
		SourceInputID: e.SourceInputID,
		SourceText:    e.SourceText,
		RunID:         e.RunID,
		CreatedAt:     e.CreatedAt,
		UpdatedAt:     timePtr(e.UpdatedAt),
	}
}

func memoryLogToView(log state.MemoryLog, contextFrom time.Time) (memoryLogView, bool) {
	entries := []memoryLogEntryView{}
	for _, e := range log.Entries {
		if !contextFrom.IsZero() && e.CreatedAt.Before(contextFrom) {
			continue
		}
		entries = append(entries, memoryLogEntryToView(e))
	}
	if len(log.Entries) > 0 && len(entries) == 0 {
		return memoryLogView{}, false
	}
	return memoryLogView{
		Date:      log.Date,
		Revision:  log.UpdatedAt.UnixMilli(),
		Entries:   entries,
		UpdatedAt: log.UpdatedAt,
	}, true
}

func parseChunkTarget(chunk docstore.MemoryChunk) (memoryID, logDate, entryID string) {
	switch chunk.Layer {
	case docstore.MemoryLayerCore:
		return strings.TrimPrefix(chunk.ChunkID, "core:"), "", ""
	case docstore.MemoryLayerLog:
		logDate = chunk.LogDate
		rest := strings.TrimPrefix(chunk.ChunkID, "log:")
		if d, eid, ok := strings.Cut(rest, ":"); ok {
			if logDate == "" {
				logDate = d
			}
			entryID = eid
		} else {
			entryID = rest
		}
		return "", logDate, entryID
	default:
		return "", chunk.LogDate, ""
	}
}

func memorySearchHitToView(h docstore.MemorySearchHit) memorySearchHitView {
	memID, logDate, entryID := parseChunkTarget(h.Chunk)
	snip := h.Snippet
	if snip == "" {
		snip = h.Chunk.Body
	}
	snip = docstore.ClipUTF8(snip, docstore.MaxMemorySnippetBytes)
	return memorySearchHitView{
		ChunkID:    h.Chunk.ChunkID,
		Layer:      h.Chunk.Layer,
		MemoryID:   memID,
		LogDate:    logDate,
		EntryID:    entryID,
		Section:    h.Chunk.Section,
		About:      h.Chunk.About,
		Snippet:    snip,
		Provenance: effectiveMemoryProvenance(state.MemoryProvenance(h.Chunk.Provenance)),
		Pinned:     h.Chunk.Pinned,
		Score:      h.Score,
		CreatedAt:  h.Chunk.CreatedAt,
		UpdatedAt:  timePtr(h.Chunk.UpdatedAt),
	}
}

func buildWorkingMemoryView(summary state.ConversationSummary, memories []state.Memory, contextFrom time.Time) workingMemoryView {
	return buildWorkingMemoryViewWithBytes(summary, state.CoreMemorySize(memories), contextFrom)
}

func buildWorkingMemoryViewWithBytes(summary state.ConversationSummary, coreBytes int, contextFrom time.Time) workingMemoryView {
	view := workingMemoryView{
		Items:           []workingMemoryItemView{},
		CoreBytes:       coreBytes,
		CoreBudgetBytes: state.CoreMemoryBytes,
	}
	applies := summary.Applies(contextFrom)
	hasMetadata := summary.From.Equal(contextFrom) && (!summary.ExtractedThrough.IsZero() || !summary.LastDreamAt.IsZero())
	if !applies && !hasMetadata {
		return view
	}
	if !summary.UpdatedAt.IsZero() {
		view.Revision = summary.UpdatedAt.UnixMilli()
	}
	view.From = timePtr(summary.From)
	view.DreamThrough = timePtr(summary.DreamThrough)
	view.LastDreamAt = timePtr(summary.LastDreamAt)
	view.UpdatedAt = timePtr(summary.UpdatedAt)
	if !applies {
		view.ExtractedThrough = timePtr(summary.ExtractedThrough)
		return view
	}
	view.ExtractedThrough = timePtr(summary.EffectiveExtractedThrough())
	view.FlushedThrough = timePtr(summary.EffectiveFlushedThrough())
	view.Through = timePtr(summary.Through)
	view.Gist = summary.Gist
	view.Turns = summary.Turns
	view.Model = summary.Model
	for _, item := range summary.Items {
		view.Items = append(view.Items, workingMemoryItemView{
			ID:      state.SummaryItemID(item),
			Kind:    item.Kind,
			Quote:   item.Quote,
			InputID: item.InputID,
			Note:    item.Note,
		})
	}
	return view
}

func (a *Assistant) memory(w http.ResponseWriter, r *http.Request) {
	memories, err := a.Store.Memories(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	items := []memoryFact{}
	for _, m := range memories {
		items = append(items, memoryView(m))
	}
	writeJSON(w, 200, list[memoryFact]{Items: items})
}

func (a *Assistant) currentMemory(ctx context.Context, id string) any {
	memories, err := a.Store.Memories(ctx)
	if err != nil {
		return nil
	}
	for _, m := range memories {
		if m.ID == id {
			return memoryView(m)
		}
	}
	return nil
}

func (a *Assistant) currentMemoryLog(ctx context.Context, date string) any {
	contextFrom, err := a.Store.ContextFrom(ctx)
	if err != nil {
		return nil
	}
	log, err := a.Store.MemoryLog(ctx, date)
	if err != nil {
		return nil
	}
	view, ok := memoryLogToView(log, contextFrom)
	if !ok {
		return nil
	}
	return view
}

func (a *Assistant) currentWorkingMemory(ctx context.Context) any {
	summary, err := a.Store.ConversationSummary(ctx)
	if err != nil {
		return nil
	}
	memories, err := a.Store.Memories(ctx)
	if err != nil {
		return nil
	}
	contextFrom, err := a.Store.ContextFrom(ctx)
	if err != nil {
		return nil
	}
	return buildWorkingMemoryView(summary, memories, contextFrom)
}

func (a *Assistant) createMemory(w http.ResponseWriter, r *http.Request) {
	var value memoryFactEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if expected != 0 {
		writeAPIError(w, state.ErrRevisionConflict, requestID, nil)
		return
	}
	if !value.valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	text := value.Text
	about := value.About
	section := value.Section
	var replacesID, sourceText, binding string
	if a.NormalizeMemory != nil {
		rawBinding, err := state.MutationBinding(state.Mutation{
			Method:           http.MethodPost,
			Target:           "/v1/memory",
			ExpectedRevision: expected,
			Payload:          value,
		})
		if err != nil {
			writeAPIError(w, err, requestID, nil)
			return
		}
		if existing, err := a.Store.Receipt(r.Context(), requestID); err == nil {
			if existing.Binding != rawBinding {
				writeAPIError(w, state.ErrIdempotencyConflict, requestID, nil)
				return
			}
			writeReceipt(w, existing)
			return
		} else if !errors.Is(err, state.ErrNotFound) {
			writeAPIError(w, err, requestID, nil)
			return
		}
		binding = rawBinding
		existingMemories, err := a.Store.Memories(r.Context())
		if err != nil {
			slog.WarnContext(r.Context(), "could not load existing memories for normalizer", "err", err)
		}
		normalized, err := a.NormalizeMemory(r.Context(), conversation.ManualMemoryInput{
			Text:     value.Text,
			About:    value.About,
			Section:  value.Section,
			Existing: existingMemories,
		})
		if err != nil {
			slog.WarnContext(r.Context(), "manual memory normalizer failed; storing raw memory", "err", err)
		} else {
			if trimmed := strings.TrimSpace(normalized.Text); trimmed != "" && len(trimmed) <= state.MaxMemoryText && utf8.ValidString(trimmed) {
				if trimmed != strings.TrimSpace(value.Text) {
					sourceText = value.Text
				}
				text = trimmed
			}
			if strings.TrimSpace(value.About) == "" {
				if candidate := strings.TrimSpace(normalized.About); candidate != "" && len(candidate) <= state.MaxMemoryAbout && utf8.ValidString(candidate) {
					about = candidate
				}
			}
			if strings.TrimSpace(value.Section) == "" {
				if candidate := strings.TrimSpace(normalized.Section); candidate != "" && len(candidate) <= state.MaxMemorySection && utf8.ValidString(candidate) {
					section = candidate
				}
			}
			replacesID = strings.TrimSpace(normalized.ReplacesID)
		}
	}
	receipt, err := a.Store.MutateMemory(r.Context(), state.MemoryMutationRequest{
		RequestID:        requestID,
		ExpectedRevision: 0,
		Kind:             state.MemoryMutateCreateCore,
		ReplacesID:       replacesID,
		Text:             text,
		About:            about,
		Section:          section,
		SourceText:       sourceText,
		Binding:          binding,
		Pinned:           value.Pinned,
		FormatResource: func(res state.MemoryMutationResult) (json.RawMessage, error) {
			if res.Memory == nil {
				return json.RawMessage(`{}`), nil
			}
			return json.Marshal(memoryView(*res.Memory))
		},
	})
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	if receipt.HTTPStatus == 0 {
		receipt.HTTPStatus = http.StatusCreated
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) editMemory(w http.ResponseWriter, r *http.Request) {
	var value memoryFactEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !value.valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	id := r.PathValue("id")
	receipt, err := a.Store.MutateMemory(r.Context(), state.MemoryMutationRequest{
		RequestID:        requestID,
		ExpectedRevision: expected,
		Kind:             state.MemoryMutateEditCore,
		TargetID:         id,
		Text:             value.Text,
		About:            value.About,
		Section:          value.Section,
		Pinned:           value.Pinned,
		FormatResource: func(res state.MemoryMutationResult) (json.RawMessage, error) {
			if res.Memory == nil {
				return json.RawMessage(`{}`), nil
			}
			return json.Marshal(memoryView(*res.Memory))
		},
	})
	if err != nil {
		writeAPIError(w, err, requestID, a.currentMemory(r.Context(), id))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) memoryAction(w http.ResponseWriter, r *http.Request) {
	var value memoryActionValue
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !value.validItemAction() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	id := r.PathValue("id")
	var kind string
	var format func(res state.MemoryMutationResult) (json.RawMessage, error)
	var forgottenTags []string
	switch value.Action {
	case "forget":
		kind = state.MemoryMutateForgetCore
		format = func(res state.MemoryMutationResult) (json.RawMessage, error) {
			note := res.Note
			if note == "" {
				note = "Nori will no longer use this. Conversation history and external documents are unchanged."
			}
			out := map[string]any{"note": note}
			if res.Memory != nil {
				out["forgotten"] = memoryView(*res.Memory)
				if about := strings.TrimSpace(res.Memory.About); about != "" {
					forgottenTags = append(forgottenTags, about)
				}
				if text := strings.TrimSpace(res.Memory.Text); text != "" {
					forgottenTags = append(forgottenTags, text)
				}
			}
			return json.Marshal(out)
		}
	case "pin":
		kind = state.MemoryMutatePinCore
		format = func(res state.MemoryMutationResult) (json.RawMessage, error) {
			if res.Memory == nil {
				return json.RawMessage(`{}`), nil
			}
			return json.Marshal(memoryView(*res.Memory))
		}
	case "unpin":
		kind = state.MemoryMutateUnpinCore
		format = func(res state.MemoryMutationResult) (json.RawMessage, error) {
			if res.Memory == nil {
				return json.RawMessage(`{}`), nil
			}
			return json.Marshal(memoryView(*res.Memory))
		}
	case "demote":
		kind = state.MemoryMutateDemoteCore
		format = func(res state.MemoryMutationResult) (json.RawMessage, error) {
			out := map[string]any{}
			if res.Memory != nil {
				out["demoted"] = memoryView(*res.Memory)
			}
			if res.LogEntry != nil {
				out["log_entry"] = memoryLogEntryToView(*res.LogEntry)
			}
			if res.Note != "" {
				out["note"] = res.Note
			}
			return json.Marshal(out)
		}
	}
	receipt, err := a.Store.MutateMemory(r.Context(), state.MemoryMutationRequest{
		RequestID:        requestID,
		ExpectedRevision: expected,
		Kind:             kind,
		TargetID:         id,
		FormatResource:   format,
	})
	if err != nil {
		writeAPIError(w, err, requestID, a.currentMemory(r.Context(), id))
		return
	}
	if value.Action == "forget" && a.OnForget != nil && len(forgottenTags) > 0 {
		a.OnForget(r.Context(), forgottenTags)
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) memoryCollectionAction(w http.ResponseWriter, r *http.Request) {
	var value memoryActionValue
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if value.Action != "consolidate" {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	if expected != 0 {
		writeAPIError(w, state.ErrRevisionConflict, requestID, nil)
		return
	}
	binding, err := state.MutationBinding(state.Mutation{
		Method:           http.MethodPost,
		Target:           "/v1/memory/actions",
		ExpectedRevision: expected,
		Payload:          value,
	})
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	if existing, err := a.Store.Receipt(r.Context(), requestID); err == nil {
		if existing.Binding != binding {
			writeAPIError(w, state.ErrIdempotencyConflict, requestID, nil)
			return
		}
		writeReceipt(w, existing)
		return
	} else if !errors.Is(err, state.ErrNotFound) {
		writeAPIError(w, err, requestID, nil)
		return
	}
	if a.ConsolidateMemory == nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{
			Error:     "Memory consolidation is unavailable.",
			Code:      "provider_unavailable",
			Retryable: false,
			RequestID: requestID,
		})
		return
	}
	summary, err := a.ConsolidateMemory(r.Context())
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	memories, err := a.Store.Memories(r.Context())
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	contextFrom, err := a.Store.ContextFrom(r.Context())
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	resource, err := json.Marshal(buildWorkingMemoryView(summary, memories, contextFrom))
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	receipt, err := a.Store.SaveMemoryActionReceipt(r.Context(), requestID, binding, resource)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) searchMemory(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > maxMemorySearchQuery {
		writeAPIError(w, state.ErrInvalidResource, "", nil)
		return
	}
	layer := strings.TrimSpace(r.URL.Query().Get("layer"))
	if layer != "" && layer != docstore.MemoryLayerCore && layer != docstore.MemoryLayerLog {
		writeAPIError(w, state.ErrInvalidResource, "", nil)
		return
	}
	limit := docstore.DefaultMemorySearchLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > docstore.MaxMemorySearchLimit {
			writeAPIError(w, state.ErrInvalidResource, "", nil)
			return
		}
		limit = n
	}
	contextFrom, err := a.Store.ContextFrom(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	found, err := a.Store.SearchMemory(r.Context(), docstore.MemorySearchQuery{
		Query:        q,
		Layer:        layer,
		CreatedAfter: contextFrom,
		Limit:        limit,
	})
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	items := []memorySearchHitView{}
	for _, h := range found {
		if !contextFrom.IsZero() && !h.Chunk.CreatedAt.IsZero() && h.Chunk.CreatedAt.Before(contextFrom) {
			continue
		}
		items = append(items, memorySearchHitToView(h))
	}
	writeJSON(w, 200, memorySearchResponse{Items: items, Query: q})
}

func (a *Assistant) memoryLogs(w http.ResponseWriter, r *http.Request) {
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	if after != "" && !state.ValidMemoryDate(after) {
		writeAPIError(w, state.ErrInvalidResource, "", nil)
		return
	}
	limit := defaultMemoryLogsLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxMemoryLogsLimit {
			writeAPIError(w, state.ErrInvalidResource, "", nil)
			return
		}
		limit = n
	}
	contextFrom, err := a.Store.ContextFrom(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	logs, err := a.Store.MemoryLogs(r.Context(), after, limit)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	items := []memoryLogView{}
	for _, log := range logs {
		if view, ok := memoryLogToView(log, contextFrom); ok {
			items = append(items, view)
		}
	}
	writeJSON(w, 200, list[memoryLogView]{Items: items})
}

func (a *Assistant) memoryLog(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	if !state.ValidMemoryDate(date) {
		writeAPIError(w, state.ErrInvalidResource, "", nil)
		return
	}
	contextFrom, err := a.Store.ContextFrom(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	log, err := a.Store.MemoryLog(r.Context(), date)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	view, ok := memoryLogToView(log, contextFrom)
	if !ok {
		writeAPIError(w, state.ErrNotFound, "", nil)
		return
	}
	writeJSON(w, 200, view)
}

func (a *Assistant) memoryLogEntryAction(w http.ResponseWriter, r *http.Request) {
	date := r.PathValue("date")
	entryID := r.PathValue("entry_id")
	var value memoryLogEntryActionValue
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !state.ValidMemoryDate(date) || !state.ValidResourceID(entryID) || !value.valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	var kind string
	var format func(res state.MemoryMutationResult) (json.RawMessage, error)
	switch value.Action {
	case "promote":
		kind = state.MemoryMutatePromoteLog
		format = func(res state.MemoryMutationResult) (json.RawMessage, error) {
			if res.Memory == nil {
				return json.RawMessage(`{}`), nil
			}
			return json.Marshal(memoryView(*res.Memory))
		}
	case "forget":
		kind = state.MemoryMutateForgetLog
		format = func(res state.MemoryMutationResult) (json.RawMessage, error) {
			note := res.Note
			if note == "" {
				note = "Nori removed this daily log entry."
			}
			out := map[string]any{"note": note}
			if res.LogEntry != nil {
				out["forgotten"] = memoryLogEntryToView(*res.LogEntry)
			}
			return json.Marshal(out)
		}
	}
	receipt, err := a.Store.MutateMemory(r.Context(), state.MemoryMutationRequest{
		RequestID:        requestID,
		ExpectedRevision: expected,
		Kind:             kind,
		Date:             date,
		TargetID:         entryID,
		Text:             value.Text,
		About:            value.About,
		Section:          value.Section,
		Pinned:           value.Pinned,
		FormatResource:   format,
	})
	if err != nil {
		writeAPIError(w, err, requestID, a.currentMemoryLog(r.Context(), date))
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) workingMemory(w http.ResponseWriter, r *http.Request) {
	summary, err := a.Store.ConversationSummary(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	memories, err := a.Store.Memories(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	contextFrom, err := a.Store.ContextFrom(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, buildWorkingMemoryView(summary, memories, contextFrom))
}

func (a *Assistant) workingMemoryItemAction(w http.ResponseWriter, r *http.Request) {
	itemID := r.PathValue("item_id")
	var value workingMemoryItemActionValue
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !state.ValidResourceID(itemID) || !value.valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	var kind string
	var format func(res state.MemoryMutationResult) (json.RawMessage, error)
	switch value.Action {
	case "promote":
		kind = state.MemoryMutatePromoteWorking
		format = func(res state.MemoryMutationResult) (json.RawMessage, error) {
			if res.Memory == nil {
				return json.RawMessage(`{}`), nil
			}
			return json.Marshal(memoryView(*res.Memory))
		}
	case "dismiss":
		kind = state.MemoryMutateDismissWorking
		format = func(res state.MemoryMutationResult) (json.RawMessage, error) {
			if res.Summary != nil {
				return json.Marshal(buildWorkingMemoryViewWithBytes(*res.Summary, res.CoreBytes, res.ContextFrom))
			}
			return json.Marshal(map[string]any{"dismissed_item_id": itemID})
		}
	}
	receipt, err := a.Store.MutateMemory(r.Context(), state.MemoryMutationRequest{
		RequestID:        requestID,
		ExpectedRevision: expected,
		Kind:             kind,
		TargetID:         itemID,
		Text:             value.Text,
		About:            value.About,
		Section:          value.Section,
		Pinned:           value.Pinned,
		FormatResource:   format,
	})
	if err != nil {
		writeAPIError(w, err, requestID, a.currentWorkingMemory(r.Context()))
		return
	}
	writeReceipt(w, receipt)
}
