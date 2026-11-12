package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"nori/internal/state"
)

// The Library projects existing saved results. A small overlay resource per result holds what the user changed
// (title, collection, deletion); its revision is the LibraryItem revision, distinct from the result's content
// version and from state.Result.revision, which is an instruction revision.
const collectionLibrary = "library_overlay"

type libraryOverlay struct {
	Title      string     `json:"title,omitempty"`
	Collection string     `json:"collection,omitempty"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}

// libraryItemEditable is the contract LibraryItemEditable.
type libraryItemEditable struct {
	Title      string `json:"title"`
	Collection string `json:"collection,omitempty"`
}

// libraryActionValue is the contract LibraryAction value. Delete names a scope; revise names an instruction;
// export names a format.
type libraryActionValue struct {
	Action      string `json:"action"`
	Instruction string `json:"instruction,omitempty"`
	Format      string `json:"format,omitempty"`
	Scope       string `json:"scope,omitempty"`
}

func (v libraryActionValue) valid() bool {
	switch v.Action {
	case "revise":
		return strings.TrimSpace(v.Instruction) != "" && len(v.Instruction) <= 8192 && v.Format == "" && v.Scope == ""
	case "export":
		switch v.Format {
		case "md", "pdf", "docx", "csv", "ics", "original":
			return v.Instruction == "" && v.Scope == ""
		}
	case "delete":
		return (v.Scope == "version" || v.Scope == "all_versions") && v.Instruction == "" && v.Format == ""
	}
	return false
}

// memoryFactEditable is the contract MemoryFactEditable.
type memoryFactEditable struct {
	Text string `json:"text"`
}

// memoryActionValue is the contract MemoryAction value.
type memoryActionValue struct {
	Action string `json:"action"`
}

func (v memoryActionValue) valid() bool { return v.Action == "forget" }

// inboxAction is the contract InboxAction value.
type inboxAction struct {
	Action string `json:"action"`
}

func (v inboxAction) valid() bool { return v.Action == "acknowledge" }

type provenance struct {
	Content          string          `json:"content"`
	Source           json.RawMessage `json:"source,omitempty"`
	SourceURL        string          `json:"source_url,omitempty"`
	RetrievedAt      *time.Time      `json:"retrieved_at,omitempty"`
	ConnectionStatus string          `json:"connection_status,omitempty"`
}

type libraryItem struct {
	ID           string `json:"id"`
	RootID       string `json:"root_id"`
	Version      int    `json:"version"`
	Revises      string `json:"revises,omitempty"`
	SupersededBy string `json:"superseded_by,omitempty"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Name         string `json:"name,omitempty"`
	MediaType    string `json:"media_type,omitempty"`
	Size         int64  `json:"size,omitempty"`
	URL          string `json:"url,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	// Contract fields the server does not populate yet; declared so round-trips keep them.
	AutomationID   string          `json:"automation_id,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	GoalID         string          `json:"goal_id,omitempty"`
	Data           json.RawMessage `json:"data,omitempty"`
	DataKind       string          `json:"data_kind,omitempty"`
	DeleteEffect   string          `json:"delete_effect,omitempty"`
	PreviewText    string          `json:"preview_text,omitempty"`
	Provenance     provenance      `json:"provenance"`
	IndexStatus    string          `json:"index_status,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      *time.Time      `json:"updated_at,omitempty"`
	DeletedAt      *time.Time      `json:"deleted_at,omitempty"`
	Collection     string          `json:"collection,omitempty"`
	Revision       int64           `json:"revision"`
}

var libraryKinds = map[string]string{"answer": "answer", "file": "file", "screenshot": "screenshot", "link": "link", "document": "document", "report": "report"}

func (a *Assistant) overlays(ctx context.Context) (map[string]state.Resource, error) {
	out := map[string]state.Resource{}
	after := ""
	for {
		page, next, err := a.Store.Resources(ctx, collectionLibrary, nil, after, 200)
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			out[r.ID] = r
		}
		if next == "" {
			return out, nil
		}
		after = next
	}
}

func (a *Assistant) libraryItem(result state.Result, overlay *state.Resource) libraryItem {
	kind := libraryKinds[result.Kind]
	if kind == "" {
		kind = "file"
	}
	item := libraryItem{
		ID: result.ID, RootID: result.RootID, Version: result.Version, Revises: result.Revises, SupersededBy: result.SupersededBy,
		Kind: kind, Title: result.Title, Name: result.Name, MediaType: result.MediaType, Size: result.Size, URL: result.URL,
		TaskID: result.RunID, Provenance: provenance{Content: "generated"}, IndexStatus: "indexed", CreatedAt: result.CreatedAt,
	}
	if item.RootID == "" {
		item.RootID = result.ID
	}
	if item.Version == 0 {
		item.Version = 1
	}
	if result.URL != "" {
		item.Provenance = provenance{Content: "live", SourceURL: result.URL}
	}
	if result.Kind == "file" && result.Text == "" {
		item.IndexStatus = "unsupported"
	}
	if result.Text != "" {
		item.PreviewText = clipRunes(result.Text, 2048)
	}
	// What deleting does, stated by the server: Nori's copy goes; anything the item points to stays where it is.
	item.DeleteEffect = "Removes it from your Library and search. The task that made it keeps its record."
	if result.URL != "" {
		item.DeleteEffect = "Removes it from your Library and search. The linked page or file stays where it is."
	}
	if overlay != nil {
		var o libraryOverlay
		if overlay.Decode(&o) == nil {
			if o.Title != "" {
				item.Title = o.Title
			}
			item.Collection, item.DeletedAt = o.Collection, o.DeletedAt
		}
		item.Revision = overlay.Revision
		updated := overlay.UpdatedAt
		item.UpdatedAt = &updated
	}
	return item
}

// libraryItems lists visible items. Deletion is enforced here and at retrieval, not only in the list.
func (a *Assistant) libraryItems(ctx context.Context) ([]libraryItem, map[string]state.Result, error) {
	results, err := a.Store.RecentResults(ctx, 500)
	if err != nil {
		return nil, nil, err
	}
	overlays, err := a.overlays(ctx)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]state.Result{}
	items := []libraryItem{}
	for _, result := range results {
		byID[result.ID] = result
		var overlay *state.Resource
		if r, ok := overlays[result.ID]; ok {
			overlay = &r
		}
		item := a.libraryItem(result, overlay)
		if item.DeletedAt == nil {
			items = append(items, item)
		}
	}
	a.withOrigins(ctx, items)
	return items, byID, nil
}

// Deleted reports whether the user deleted a result from the Library; result routes refuse to serve it.
func (a *Assistant) Deleted(ctx context.Context, id string) bool {
	r, err := a.Store.Resource(ctx, collectionLibrary, id)
	if err != nil {
		return false
	}
	var o libraryOverlay
	return r.Decode(&o) == nil && o.DeletedAt != nil
}

func (a *Assistant) library(w http.ResponseWriter, r *http.Request) {
	items, _, err := a.libraryItems(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	q := r.URL.Query()
	filtered := []libraryItem{}
	for _, item := range items {
		if kind := q.Get("kind"); kind != "" && item.Kind != kind {
			continue
		}
		if task := q.Get("task_id"); task != "" && item.TaskID != task {
			continue
		}
		if q.Get("versions") != "all" && item.SupersededBy != "" {
			continue
		}
		filtered = append(filtered, item)
	}
	writeJSON(w, 200, list[libraryItem]{Items: filtered})
}

func (a *Assistant) libraryEntry(ctx context.Context, id string) (libraryItem, error) {
	if a.Deleted(ctx, id) {
		return libraryItem{}, state.ErrNotFound
	}
	result, err := a.Store.GetResult(ctx, id)
	if err != nil {
		return libraryItem{}, err
	}
	var overlay *state.Resource
	if r, err := a.Store.Resource(ctx, collectionLibrary, id); err == nil {
		overlay = &r
	}
	items := []libraryItem{a.libraryItem(result, overlay)}
	a.withOrigins(ctx, items)
	return items[0], nil
}

func (a *Assistant) libraryGet(w http.ResponseWriter, r *http.Request) {
	item, err := a.libraryEntry(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, item)
}

func (a *Assistant) libraryVersions(w http.ResponseWriter, r *http.Request) {
	item, err := a.libraryEntry(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	items, _, err := a.libraryItems(r.Context())
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	versions := []libraryItem{}
	for _, candidate := range items {
		if candidate.RootID == item.RootID {
			versions = append(versions, candidate)
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Version < versions[j].Version })
	writeJSON(w, 200, list[libraryItem]{Items: versions})
}

func (a *Assistant) libraryRename(w http.ResponseWriter, r *http.Request) {
	var value libraryItemEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := a.libraryEntry(r.Context(), id); err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	receipt, err := a.overlayMutation(r.Context(), id, requestID, "PUT", expected, value, func(o *libraryOverlay) error {
		if strings.TrimSpace(value.Title) == "" || len(value.Title) > 200 {
			return state.ErrInvalidResource
		}
		o.Title, o.Collection = value.Title, value.Collection
		return nil
	})
	if err != nil {
		current, _ := a.libraryEntry(r.Context(), id)
		writeAPIError(w, err, requestID, current)
		return
	}
	writeReceipt(w, receipt)
}

func (a *Assistant) overlayMutation(ctx context.Context, id, requestID, method string, expected int64, payload any, change func(*libraryOverlay) error) (state.MutationReceipt, error) {
	result, err := a.Store.GetResult(ctx, id)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	return a.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: method, Target: "/v1/library/" + id, ExpectedRevision: expected, Payload: payload,
		Collection: collectionLibrary, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			var o libraryOverlay
			revision := int64(1)
			if current != nil {
				if err := current.Decode(&o); err != nil {
					return state.MutationResult{}, err
				}
				revision = current.Revision + 1
			}
			if err := change(&o); err != nil {
				return state.MutationResult{}, err
			}
			resource, err := state.NewResource(collectionLibrary, id, revision, o, nil, time.Now())
			if err != nil {
				return state.MutationResult{}, err
			}
			resource.Revision = revision
			item := a.libraryItem(result, &resource)
			data, _ := json.Marshal(item)
			return state.MutationResult{Writes: []state.Resource{resource}, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200}}, nil
		},
	})
}

// libraryAction revises (a new task revising the result, preserving prior versions), exports or deletes.
func (a *Assistant) libraryAction(w http.ResponseWriter, r *http.Request) {
	var value libraryActionValue
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !value.valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	id := r.PathValue("id")
	item, err := a.libraryEntry(r.Context(), id)
	if err != nil {
		writeAPIError(w, err, requestID, nil)
		return
	}
	switch value.Action {
	case "revise":
		if a.Revise == nil || strings.TrimSpace(value.Instruction) == "" {
			writeAPIError(w, state.ErrInvalidResource, requestID, nil)
			return
		}
		taskID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("library-revise:"+requestID)).String()
		run, err := a.Revise(r.Context(), taskID, id, value.Instruction)
		if err != nil {
			writeAPIError(w, err, requestID, nil)
			return
		}
		data, _ := json.Marshal(item)
		writeJSON(w, 202, state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "queued", Resource: data, Work: &state.WorkRef{TaskID: run.ID}})
	case "export":
		switch value.Format {
		case "original":
			if item.Name == "" && item.URL == "" {
				writeAPIError(w, state.ErrInvalidResource, requestID, nil)
				return
			}
		case "md":
			if item.Kind != "answer" && item.Kind != "report" && item.Kind != "document" {
				writeAPIError(w, state.ErrInvalidResource, requestID, nil)
				return
			}
		default:
			writeJSON(w, 400, apiError{Error: "This format is not available for this item yet.", Code: "validation_failed", RequestID: requestID})
			return
		}
		data, _ := json.Marshal(map[string]any{"item": item, "download_url": "/v1/library/" + id + "/file?format=" + value.Format})
		writeJSON(w, 200, state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "applied", Resource: data})
	case "delete":
		// LibraryAction requires scope "version" or "all_versions". A missing scope is invalid, not a silent delete.
		receipt, err := a.overlayMutation(r.Context(), id, requestID, "DELETE", expected, value, func(o *libraryOverlay) error {
			now := time.Now().UTC()
			o.DeletedAt = &now
			return nil
		})
		if err != nil {
			current, _ := a.libraryEntry(r.Context(), id)
			writeAPIError(w, err, requestID, current)
			return
		}
		writeReceipt(w, receipt)
	default:
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
	}
}

// libraryFile serves an item's bytes or Markdown export, refusing deleted items.
func (a *Assistant) libraryFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if a.Deleted(r.Context(), id) {
		writeAPIError(w, state.ErrNotFound, "", nil)
		return
	}
	result, err := a.Store.GetResult(r.Context(), id)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	if r.URL.Query().Get("format") == "md" || result.Name == "" {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(result.Title)+`.md"`)
		_, _ = w.Write([]byte("# " + result.Title + "\n\n" + result.Text + "\n"))
		return
	}
	data, err := a.Store.GetFile(r.Context(), state.ResultKey(id))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	w.Header().Set("Content-Type", result.MediaType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+safeName(result.Name)+`"`)
	_, _ = w.Write(data)
}

func safeName(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r == '/' || r < 32 {
			return '_'
		}
		return r
	}, name)
}

type searchHit struct {
	Kind       string          `json:"kind"`
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Snippet    string          `json:"snippet"`
	Jump       *searchJump     `json:"jump,omitempty"`
	Provenance *provenance     `json:"provenance,omitempty"`
	Freshness  *time.Time      `json:"freshness,omitempty"`
	Score      float64         `json:"score"`
	extra      json.RawMessage `json:"-"`
}

// searchJump is the contract SearchHit jump. Exactly one target is set, matching the kind.
type searchJump struct {
	Kind           string `json:"kind"`
	ConversationID string `json:"conversation_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	ItemID         string `json:"item_id,omitempty"`
	GoalID         string `json:"goal_id,omitempty"`
	AutomationID   string `json:"automation_id,omitempty"`
}

// searchResponse is the contract SearchResponse.
type searchResponse struct {
	Items      []searchHit `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
	Query      string      `json:"query,omitempty"`
}

// score requires most query words to appear (all of them for short queries), so unrelated items do not match on
// one common word.
func score(query []string, texts ...string) (float64, string) {
	joined := strings.ToLower(strings.Join(texts, " \n "))
	total := 0.0
	for _, word := range query {
		if strings.Contains(joined, strings.Trim(word, ".,;:!?\"'")) {
			total++
		}
	}
	needed := float64(len(query))
	if len(query) > 3 {
		needed = float64(len(query)) * 0.75
	}
	if total == 0 || total < needed {
		return 0, ""
	}
	snippetSource := strings.Join(texts[1:], " ")
	lower := strings.ToLower(snippetSource)
	at := strings.Index(lower, query[0])
	start := max(at-120, 0)
	end := min(start+400, len(snippetSource))
	if at < 0 {
		start, end = 0, min(400, len(snippetSource))
	}
	return total / float64(len(query)), strings.TrimSpace(strings.ToValidUTF8(snippetSource[start:end], ""))
}

// search finds existing work across results, conversation messages, tasks, goals and automations. Every hit comes
// from this user's scoped store and deleted items are excluded at query time.
func (a *Assistant) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	hits, err := a.searchHits(r.Context(), q)
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	writeJSON(w, 200, searchResponse{Items: hits, Query: q})
}

func (a *Assistant) searchHits(ctx context.Context, q string) ([]searchHit, error) {
	words := strings.Fields(strings.ToLower(q))
	if len(words) == 0 {
		return nil, state.ErrInvalidResource
	}
	hits := []searchHit{}
	items, byID, err := a.libraryItems(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if s, snippet := score(words, item.Title, byID[item.ID].Text, item.Name); s > 0 {
			created := item.CreatedAt
			p := item.Provenance
			hits = append(hits, searchHit{Kind: "result", ID: item.ID, Title: item.Title, Snippet: snippet, Score: s + 0.2,
				Jump: &searchJump{Kind: "library", ItemID: item.ID}, Provenance: &p, Freshness: &created})
		}
	}
	if runs, err := a.Store.RecentRuns(ctx, 200); err == nil {
		for _, run := range runs {
			if s, snippet := score(words, clip(run.Request, 200), run.Request, run.Result); s > 0 {
				started := run.StartedAt
				hits = append(hits, searchHit{Kind: "task", ID: run.ID, Title: clip(run.Request, 120), Snippet: snippet, Score: s,
					Jump: &searchJump{Kind: "task", TaskID: run.ID}, Freshness: &started})
			}
		}
	}
	before := ""
	for page := 0; page < 5; page++ {
		messages, next, err := a.Store.ConversationMessages(ctx, before)
		if err != nil {
			break
		}
		for _, m := range messages {
			if s, snippet := score(words, "", m.Text); s > 0 {
				created := m.CreatedAt
				hits = append(hits, searchHit{Kind: "message", ID: m.ID, Title: clip(m.Text, 80), Snippet: snippet, Score: s - 0.1,
					Jump: &searchJump{Kind: "conversation", ConversationID: "assistant", MessageID: m.ID}, Freshness: &created})
			}
		}
		if next == "" {
			break
		}
		before = next
	}
	if a.Chats != nil {
		if chats, err := a.Store.ChatSummaries(ctx); err == nil {
			for _, chat := range chats {
				page, err := a.Store.ChatMessages(ctx, chat.ID, 0)
				if err != nil {
					continue
				}
				for _, m := range page.Messages {
					if s, snippet := score(words, "", m.Text); s > 0 {
						hits = append(hits, searchHit{Kind: "message", ID: m.ID, Title: chat.Title + ": " + clip(m.Text, 60), Snippet: snippet, Score: s - 0.1,
							Jump: &searchJump{Kind: "conversation", ConversationID: chat.ID, MessageID: m.ID}})
					}
				}
			}
		}
	}
	if a.Scheduler != nil {
		if goals, err := a.Scheduler.Goals(ctx, ""); err == nil {
			for _, g := range goals {
				if s, snippet := score(words, g.Title, g.Outcome, g.Progress); s > 0 {
					hits = append(hits, searchHit{Kind: "goal", ID: g.ID, Title: g.Title, Snippet: snippet, Score: s,
						Jump: &searchJump{Kind: "goal", GoalID: g.ID}})
				}
			}
		}
		if automations, err := a.Scheduler.List(ctx, ""); err == nil {
			for _, auto := range automations {
				if s, snippet := score(words, auto.Title, auto.Instruction); s > 0 {
					hits = append(hits, searchHit{Kind: "automation", ID: auto.ID, Title: auto.Title, Snippet: snippet, Score: s,
						Jump: &searchJump{Kind: "automation", AutomationID: auto.ID}})
				}
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > 50 {
		hits = hits[:50]
	}
	return hits, nil
}

type memoryFact struct {
	ID        string            `json:"id"`
	Revision  int64             `json:"revision"`
	Text      string            `json:"text"`
	Scope     string            `json:"scope"`
	Origin    map[string]string `json:"origin"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// memoryView shows a saved memory; its revision is the last update in milliseconds, which guards edits.
func memoryView(m state.Memory) memoryFact {
	origin := map[string]string{}
	if m.SourceInputID != "" {
		origin["conversation_id"], origin["message_id"] = "assistant", m.SourceInputID
	}
	return memoryFact{ID: m.ID, Revision: m.UpdatedAt.UnixMilli(), Text: m.Text, Scope: "user", Origin: origin, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt}
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

func (a *Assistant) editMemory(w http.ResponseWriter, r *http.Request) {
	var value memoryFactEditable
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if strings.TrimSpace(value.Text) == "" || len(value.Text) > 2000 {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	m, err := a.Store.EditMemory(r.Context(), r.PathValue("id"), expected, value.Text)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentMemory(r.Context(), r.PathValue("id")))
		return
	}
	data, _ := json.Marshal(memoryView(m))
	writeJSON(w, 200, state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "applied", Resource: data})
}

func (a *Assistant) memoryAction(w http.ResponseWriter, r *http.Request) {
	var value memoryActionValue
	requestID, expected, ok := decodeChange(w, r, &value)
	if !ok {
		return
	}
	if !value.valid() {
		writeAPIError(w, state.ErrInvalidResource, requestID, nil)
		return
	}
	m, err := a.Store.ForgetMemory(r.Context(), r.PathValue("id"), expected)
	if err != nil {
		writeAPIError(w, err, requestID, a.currentMemory(r.Context(), r.PathValue("id")))
		return
	}
	data, _ := json.Marshal(map[string]any{"forgotten": memoryView(m), "note": "Nori will no longer use this. Conversation history and external documents are unchanged."})
	writeJSON(w, 200, state.MutationReceipt{RequestID: requestID, Accepted: true, Status: "applied", Resource: data})
}

func clipRunes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// withOrigins fills each item's chat, routine and goal from the task that made it, reading each task once.
func (a *Assistant) withOrigins(ctx context.Context, items []libraryItem) {
	runs := map[string]*state.Run{}
	for i := range items {
		id := items[i].TaskID
		if id == "" {
			continue
		}
		run, seen := runs[id]
		if !seen {
			if r, err := a.Store.GetRun(ctx, id); err == nil {
				run = &r
			}
			runs[id] = run
		}
		if run == nil {
			continue
		}
		origin := originOf(*run)
		items[i].ConversationID, items[i].AutomationID, items[i].GoalID = origin.ConversationID, origin.AutomationID, origin.GoalID
	}
}
