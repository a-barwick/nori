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
	"nori/internal/state/docstore"
)

const collectionLibrary = "library_overlay"

type libraryOverlay struct {
	Title      string     `json:"title,omitempty"`
	Collection string     `json:"collection,omitempty"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}

type libraryItemEditable struct {
	Title      string `json:"title"`
	Collection string `json:"collection,omitempty"`
}

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
	ID             string          `json:"id"`
	RootID         string          `json:"root_id"`
	Version        int             `json:"version"`
	Revises        string          `json:"revises,omitempty"`
	SupersededBy   string          `json:"superseded_by,omitempty"`
	Kind           string          `json:"kind"`
	Title          string          `json:"title"`
	Name           string          `json:"name,omitempty"`
	MediaType      string          `json:"media_type,omitempty"`
	Size           int64           `json:"size,omitempty"`
	URL            string          `json:"url,omitempty"`
	TaskID         string          `json:"task_id,omitempty"`
	AutomationID   string          `json:"automation_id,omitempty"`
	ConversationID string          `json:"conversation_id,omitempty"`
	Data           json.RawMessage `json:"data,omitempty"`
	DataKind       string          `json:"data_kind,omitempty"`
	DeleteEffect   string          `json:"delete_effect,omitempty"`
	PreviewText    string          `json:"preview_text,omitempty"`
	Excerpt        string          `json:"excerpt,omitempty"`
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
	} else if result.Kind == state.ResultFile && result.ID != "" {
		// Provide an authenticated Library download URL for stored generated binary files
		// without changing Provenance from "generated".
		item.URL = "/api/v1/library/" + result.ID + "/file"
	}
	if result.Kind == "file" && result.Text == "" {
		item.IndexStatus = "unsupported"
	}
	if result.Text != "" {
		previewSource := result.Text
		if kind == "answer" || kind == "report" || kind == "document" {
			if extracted, rest := state.ExtractDocumentTitle(result.Text); extracted != "" {
				if item.Title == "" || item.Title != extracted {
					item.Title = extracted
				}
				if rest != "" {
					previewSource = rest
				}
			}
		}
		item.PreviewText = clipRunes(previewSource, 2048)
		item.Excerpt = state.PlainExcerpt(previewSource, 240)
	}
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
		if err := a.checkConcurrentTaskCap(r.Context()); err != nil {
			writeAPIError(w, err, requestID, nil)
			return
		}
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
			if item.Name == "" && item.URL == "" && item.Kind != state.ResultScreenshot {
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
		receipt, err := a.deleteLibraryItem(r.Context(), id, requestID, expected, value)
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

func (a *Assistant) versionIDs(ctx context.Context, target state.Result) ([]string, error) {
	rootID := target.RootID
	if rootID == "" {
		rootID = target.ID
	}
	seen := map[string]bool{target.ID: true}
	results, err := a.Store.RecentResults(ctx, 500)
	if err != nil {
		return nil, err
	}
	for _, r := range results {
		rRoot := r.RootID
		if rRoot == "" {
			rRoot = r.ID
		}
		if rRoot == rootID {
			seen[r.ID] = true
		}
	}
	queue := []state.Result{target}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, linked := range []string{cur.Revises, cur.SupersededBy, cur.RootID} {
			if linked == "" || seen[linked] {
				continue
			}
			if r, err := a.Store.GetResult(ctx, linked); err == nil {
				seen[r.ID] = true
				queue = append(queue, r)
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for rid := range seen {
		if rid != target.ID && state.ValidResourceID(rid) {
			ids = append(ids, rid)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (a *Assistant) deleteLibraryItem(ctx context.Context, id, requestID string, expected int64, value libraryActionValue) (state.MutationReceipt, error) {
	result, err := a.Store.GetResult(ctx, id)
	if err != nil {
		return state.MutationReceipt{}, err
	}
	var siblings []string
	if value.Scope == "all_versions" {
		siblings, err = a.versionIDs(ctx, result)
		if err != nil {
			return state.MutationReceipt{}, err
		}
	}
	var newlyDeleted []string
	receipt, err := a.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "DELETE", Target: "/v1/library/" + id, ExpectedRevision: expected, Payload: value,
		Collection: collectionLibrary, ID: id,
		Apply: func(current *state.Resource, read func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			newlyDeleted = nil
			now := time.Now().UTC()
			var o libraryOverlay
			revision := int64(1)
			alreadyDeleted := false
			if current != nil {
				if err := current.Decode(&o); err != nil {
					return state.MutationResult{}, err
				}
				alreadyDeleted = o.DeletedAt != nil
				revision = current.Revision + 1
			}
			o.DeletedAt = &now
			if !alreadyDeleted {
				newlyDeleted = append(newlyDeleted, id)
			}
			resource, err := state.NewResource(collectionLibrary, id, revision, o, nil, now)
			if err != nil {
				return state.MutationResult{}, err
			}
			resource.Revision = revision
			writes := []state.Resource{resource}
			for _, sibID := range siblings {
				sibCurrent, err := read(collectionLibrary, sibID)
				if err != nil {
					return state.MutationResult{}, err
				}
				var sibOverlay libraryOverlay
				sibRev := int64(1)
				if sibCurrent != nil {
					if err := sibCurrent.Decode(&sibOverlay); err != nil {
						return state.MutationResult{}, err
					}
					if sibOverlay.DeletedAt != nil {
						continue
					}
					sibRev = sibCurrent.Revision + 1
				}
				sibOverlay.DeletedAt = &now
				newlyDeleted = append(newlyDeleted, sibID)
				sibRes, err := state.NewResource(collectionLibrary, sibID, sibRev, sibOverlay, nil, now)
				if err != nil {
					return state.MutationResult{}, err
				}
				sibRes.Revision = sibRev
				writes = append(writes, sibRes)
			}
			item := a.libraryItem(result, &resource)
			data, _ := json.Marshal(item)
			return state.MutationResult{Writes: writes, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200}}, nil
		},
	})
	if err == nil {
		// Purge underlying GCS/artifact blobs and decrement nori_user_storage for newly deleted items.
		var freedBytes int64
		for _, delID := range newlyDeleted {
			res := result
			if delID != id {
				var getErr error
				res, getErr = a.Store.GetResult(ctx, delID)
				if getErr != nil {
					continue
				}
			}
			if res.Size > 0 {
				freedBytes += res.Size
			}
			switch res.Kind {
			case state.ResultFile:
				_ = a.Store.DeleteFile(ctx, state.ResultKey(res.ID))
			case state.ResultScreenshot:
				if res.RunID != "" && res.ArtifactID != "" {
					_ = a.Store.DeleteFile(ctx, res.RunID+"/"+res.ArtifactID+".png")
				}
			}
		}
		if freedBytes > 0 && a.ReleaseStorageBytes != nil {
			_ = a.ReleaseStorageBytes(ctx, freedBytes)
		}
		if value.Scope == "all_versions" && a.OnLibraryDelete != nil {
			allIDs := append([]string{id}, siblings...)
			a.OnLibraryDelete(ctx, allIDs)
		}
	}
	return receipt, err
}

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
	if r.URL.Query().Get("format") != "md" && result.Kind == state.ResultScreenshot {
		data, err := a.Store.GetPNG(r.Context(), result.RunID, result.ArtifactID)
		if err != nil {
			writeAPIError(w, err, "", nil)
			return
		}
		name := result.Name
		if name == "" {
			name = result.Title + ".png"
		}
		serveFile(w, safeName(name), "image/png", data)
		return
	}
	if r.URL.Query().Get("format") == "md" || result.Name == "" {
		data := []byte("# " + result.Title + "\n\n" + result.Text + "\n")
		serveFile(w, safeName(result.Title)+".md", "text/markdown; charset=utf-8", data)
		return
	}
	data, err := a.Store.GetFile(r.Context(), state.ResultKey(id))
	if err != nil {
		writeAPIError(w, err, "", nil)
		return
	}
	serveFile(w, safeName(result.Name), result.MediaType, data)
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
	Kind       string      `json:"kind"`
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Snippet    string      `json:"snippet"`
	Jump       *searchJump `json:"jump,omitempty"`
	Provenance *provenance `json:"provenance,omitempty"`
	Freshness  *time.Time  `json:"freshness,omitempty"`
	Score      float64     `json:"score"`
}

type searchJump struct {
	Kind           string `json:"kind"`
	ConversationID string `json:"conversation_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	ItemID         string `json:"item_id,omitempty"`
	AutomationID   string `json:"automation_id,omitempty"`
	Layer          string `json:"layer,omitempty"`
	MemoryID       string `json:"memory_id,omitempty"`
	LogDate        string `json:"log_date,omitempty"`
	EntryID        string `json:"entry_id,omitempty"`
}

type searchResponse struct {
	Items      []searchHit `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
	Query      string      `json:"query,omitempty"`
}

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
	var snippetSource string
	if len(texts) > 1 {
		snippetSource = strings.Join(texts[1:], " ")
	}
	needle := strings.Trim(query[0], ".,;:!?\"'")
	if needle == "" {
		needle = query[0]
	}
	return total / float64(len(query)), extractSnippet(snippetSource, needle)
}

func extractSnippet(source, needle string) string {
	clean := strings.ToValidUTF8(source, "")
	if clean == "" {
		return ""
	}
	runes := []rune(clean)
	var lowerBuf strings.Builder
	lowerBuf.Grow(len(clean))
	lowerToRune := make([]int, 0, len(clean))
	for ri, r := range runes {
		ls := strings.ToLower(string(r))
		lowerBuf.WriteString(ls)
		for range len(ls) {
			lowerToRune = append(lowerToRune, ri)
		}
	}
	at := -1
	if needle != "" {
		at = strings.Index(lowerBuf.String(), needle)
	}
	start, end := 0, min(400, len(runes))
	if at >= 0 && at < len(lowerToRune) {
		matchRune := lowerToRune[at]
		start = max(matchRune-120, 0)
		end = min(start+400, len(runes))
	}
	return strings.TrimSpace(string(runes[start:end]))
}

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
			title := state.TaskTitle(run)
			if s, snippet := score(words, title, run.Request, run.Result); s > 0 {
				started := run.StartedAt
				hits = append(hits, searchHit{Kind: "task", ID: run.ID, Title: title, Snippet: snippet, Score: s,
					Jump: &searchJump{Kind: "task", TaskID: run.ID}, Freshness: &started})
			}
		}
	}
	contextFrom, err := a.Store.ContextFrom(ctx)
	if err != nil {
		return nil, err
	}
	before := ""
	for page := 0; page < 5; page++ {
		messages, next, err := a.Store.ConversationMessages(ctx, before)
		if err != nil {
			break
		}
		for _, m := range messages {
			if !contextFrom.IsZero() && m.CreatedAt.Before(contextFrom) {
				continue
			}
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
	if memHits, err := a.Store.SearchMemory(ctx, docstore.MemorySearchQuery{
		Query:        q,
		CreatedAfter: contextFrom,
		Limit:        docstore.MaxMemorySearchLimit,
	}); err == nil {
		for _, mh := range memHits {
			if !contextFrom.IsZero() && !mh.Chunk.CreatedAt.IsZero() && mh.Chunk.CreatedAt.Before(contextFrom) {
				continue
			}
			memID, logDate, entryID := parseChunkTarget(mh.Chunk)
			title := mh.Chunk.Section
			if title == "" {
				title = mh.Chunk.About
			}
			if title == "" {
				if mh.Chunk.Layer == docstore.MemoryLayerLog && logDate != "" {
					title = "Daily log " + logDate
				} else {
					title = "Memory"
				}
			}
			snip := mh.Snippet
			if snip == "" {
				snip = mh.Chunk.Body
			}
			snip = docstore.ClipUTF8(snip, docstore.MaxMemorySnippetBytes)
			freshness := mh.Chunk.UpdatedAt
			if freshness.IsZero() {
				freshness = mh.Chunk.CreatedAt
			}
			hits = append(hits, searchHit{
				Kind:    "memory",
				ID:      mh.Chunk.ChunkID,
				Title:   clip(title, 120),
				Snippet: snip,
				Score:   mh.Score,
				Jump: &searchJump{
					Kind:     "memory",
					Layer:    mh.Chunk.Layer,
					MemoryID: memID,
					LogDate:  logDate,
					EntryID:  entryID,
				},
				Freshness: timePtr(freshness),
			})
		}
	}
	if a.Scheduler != nil {
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

func clipRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func (a *Assistant) withOrigins(ctx context.Context, items []libraryItem) {
	if len(items) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(items))
	unique := make([]string, 0, len(items))
	for i := range items {
		id := items[i].TaskID
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return
	}
	runs := make(map[string]state.Run, len(unique))
	if len(unique) > 1 {
		if batch, ok := a.Store.(interface {
			GetRuns(context.Context, []string) (map[string]state.Run, error)
		}); ok {
			if loaded, err := batch.GetRuns(ctx, unique); err == nil {
				runs = loaded
			}
		}
	}
	if len(runs) == 0 {
		for _, id := range unique {
			if r, err := a.Store.GetRun(ctx, id); err == nil {
				runs[id] = r
			}
		}
	}
	for i := range items {
		run, ok := runs[items[i].TaskID]
		if !ok {
			continue
		}
		origin := originOf(run)
		items[i].ConversationID, items[i].AutomationID = origin.ConversationID, origin.AutomationID
	}
}
