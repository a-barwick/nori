package agent

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

var memoryNote = prompts.Runtime("memory-note", nil)

var conversationNote = prompts.Runtime("conversation-source-note", nil)

const conversationPages = 6

func (s *Service) memoryRead(ctx context.Context) (map[string]any, error) {
	memories, err := s.Store.Memories(ctx)
	if err != nil {
		return nil, err
	}
	saved := make([]map[string]any, 0, len(memories))
	for _, m := range memories {
		entry := map[string]any{
			"memory_id":  m.ID,
			"about":      m.About,
			"provenance": m.EffectiveProvenance(),
			"text":       m.Text,
			"saved_at":   m.UpdatedAt.UTC().Format("2006-01-02"),
		}
		if m.Section != "" {
			entry["section"] = m.Section
		}
		if m.Pinned {
			entry["pinned"] = true
		}
		saved = append(saved, entry)
	}
	out := map[string]any{"memories": saved, "note": memoryNote}
	summary, err := s.Store.ConversationSummary(ctx)
	if err != nil {
		return nil, err
	}
	contextFrom, err := s.Store.ContextFrom(ctx)
	if err != nil {
		return nil, err
	}
	if summary.Applies(contextFrom) {
		items := make([]map[string]any, 0, len(summary.Items))
		for _, item := range summary.Items {
			row := map[string]any{
				"id":         state.SummaryItemID(item),
				"kind":       item.Kind,
				"user_words": item.Quote,
				"input_id":   item.InputID,
			}
			if item.Note != "" {
				row["model_note"] = item.Note
			}
			items = append(items, row)
		}
		cs := map[string]any{
			"through":    summary.Through.UTC().Format(time.RFC3339),
			"items":      items,
			"model_gist": summary.Gist,
		}
		if len(summary.Excerpts) > 0 {
			cs["excerpt_count"] = len(summary.Excerpts)
		}
		out["conversation_summary"] = cs
	}
	return out, nil
}

func (s *Service) memorySearch(ctx context.Context, query, layer string, limit int) (map[string]any, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > maxSearchQuery {
		return nil, errors.New("memory_search requires a query of 1-400 bytes")
	}
	layer = strings.TrimSpace(layer)
	if layer != "" && layer != docstore.MemoryLayerCore && layer != docstore.MemoryLayerLog {
		return nil, errors.New("memory_search layer must be \"\", \"core\", or \"log\"")
	}
	if limit <= 0 {
		limit = docstore.DefaultMemorySearchLimit
	}
	if limit > docstore.MaxMemorySearchLimit {
		limit = docstore.MaxMemorySearchLimit
	}
	contextFrom, err := s.Store.ContextFrom(ctx)
	if err != nil {
		return nil, err
	}
	found, err := s.Store.SearchMemory(ctx, docstore.MemorySearchQuery{
		Query:        query,
		Layer:        layer,
		CreatedAfter: contextFrom,
		Limit:        limit,
		Now:          s.currentTime().UTC(),
	})
	if err != nil {
		return nil, err
	}
	results := []map[string]any{}
	for _, hit := range found {
		if !contextFrom.IsZero() && !hit.Chunk.CreatedAt.IsZero() && hit.Chunk.CreatedAt.Before(contextFrom) {
			continue
		}
		snip := hit.Snippet
		if snip == "" {
			snip = hit.Chunk.Body
		}
		snip = docstore.ClipUTF8(snip, docstore.MaxMemorySnippetBytes)
		provenance := state.MemoryProvenance(hit.Chunk.Provenance)
		if provenance == "" {
			provenance = state.ProvenanceUserExplicit
		}
		memID, logDate, entryID := parseMemoryChunkTarget(hit.Chunk)
		item := map[string]any{
			"chunk_id":   hit.Chunk.ChunkID,
			"layer":      hit.Chunk.Layer,
			"section":    hit.Chunk.Section,
			"about":      hit.Chunk.About,
			"snippet":    snip,
			"provenance": provenance,
			"pinned":     hit.Chunk.Pinned,
			"score":      hit.Score,
			"created_at": hit.Chunk.CreatedAt,
		}
		if memID != "" {
			item["memory_id"] = memID
		}
		if logDate != "" {
			item["log_date"] = logDate
		}
		if entryID != "" {
			item["entry_id"] = entryID
		}
		results = append(results, item)
	}
	return map[string]any{"query": query, "results": results, "note": memoryNote}, nil
}

func (s *Service) memoryGet(ctx context.Context, target, date, entryID string) (map[string]any, error) {
	target = strings.TrimSpace(target)
	date = strings.TrimSpace(date)
	entryID = strings.TrimSpace(entryID)
	switch target {
	case docstore.MemoryLayerCore:
		memories, err := s.Store.Memories(ctx)
		if err != nil {
			return nil, err
		}
		items := []map[string]any{}
		for _, m := range memories {
			items = append(items, map[string]any{
				"memory_id":  m.ID,
				"text":       m.Text,
				"about":      m.About,
				"section":    m.Section,
				"pinned":     m.Pinned,
				"provenance": m.EffectiveProvenance(),
				"created_at": m.CreatedAt,
				"updated_at": m.UpdatedAt,
			})
		}
		return map[string]any{"target": docstore.MemoryLayerCore, "memories": items, "note": memoryNote}, nil
	case docstore.MemoryLayerLog:
		if !state.ValidMemoryDate(date) {
			return nil, errors.New("memory_get with target=\"log\" requires date in YYYY-MM-DD format")
		}
		contextFrom, err := s.Store.ContextFrom(ctx)
		if err != nil {
			return nil, err
		}
		log, err := s.Store.MemoryLog(ctx, date)
		if errors.Is(err, state.ErrNotFound) {
			if entryID != "" {
				return nil, errors.New("no memory log entry found for that date and entry_id")
			}
			return map[string]any{"target": docstore.MemoryLayerLog, "date": date, "entries": []map[string]any{}, "note": memoryNote}, nil
		}
		if err != nil {
			return nil, err
		}
		entries := []map[string]any{}
		for _, e := range log.Entries {
			if !contextFrom.IsZero() && e.CreatedAt.Before(contextFrom) {
				continue
			}
			if entryID != "" && e.ID != entryID {
				continue
			}
			row := map[string]any{
				"entry_id":   e.ID,
				"section":    e.Section,
				"text":       e.Text,
				"about":      e.About,
				"provenance": e.Provenance,
				"created_at": e.CreatedAt,
				"updated_at": e.UpdatedAt,
			}
			if e.SourceInputID != "" {
				row["source_input_id"] = e.SourceInputID
			}
			if e.RunID != "" {
				row["run_id"] = e.RunID
			}
			entries = append(entries, row)
		}
		if entryID != "" && len(entries) == 0 {
			return nil, errors.New("no memory log entry found for that date and entry_id")
		}
		return map[string]any{"target": docstore.MemoryLayerLog, "date": date, "entries": entries, "note": memoryNote}, nil
	default:
		return nil, errors.New("memory_get target must be \"core\" or \"log\"")
	}
}

func parseMemoryChunkTarget(chunk docstore.MemoryChunk) (memoryID, logDate, entryID string) {
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

func parseMemorySearchLimit(raw any) (int, bool) {
	if raw == nil {
		return 0, true
	}
	switch v := raw.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return 0, true
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > docstore.MaxMemorySearchLimit {
			return 0, false
		}
		return n, true
	case int:
		if v == 0 {
			return 0, true
		}
		if v < 1 || v > docstore.MaxMemorySearchLimit {
			return 0, false
		}
		return v, true
	case int64:
		if v == 0 {
			return 0, true
		}
		if v < 1 || v > docstore.MaxMemorySearchLimit {
			return 0, false
		}
		return int(v), true
	case float64:
		if v == 0 {
			return 0, true
		}
		n := int(v)
		if float64(n) != v || n < 1 || n > docstore.MaxMemorySearchLimit {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func (s *Service) conversationFind(ctx context.Context, query string) (map[string]any, error) {
	contextFrom, err := s.Store.ContextFrom(ctx)
	if err != nil {
		return nil, err
	}
	words := []string{}
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if len(word) >= 3 {
			words = append(words, word)
		}
	}
	type match struct {
		score int
		at    time.Time
		item  map[string]any
	}
	matches := []match{}
	before := ""
	for page := 0; page < conversationPages; page++ {
		messages, next, err := s.Store.ConversationMessages(ctx, before)
		if err != nil {
			return nil, err
		}
		for _, m := range messages {
			if !contextFrom.IsZero() && m.CreatedAt.Before(contextFrom) {
				continue
			}
			text := strings.ToLower(m.Text)
			count := 0
			for _, word := range words {
				if strings.Contains(text, word) {
					count++
				}
			}
			if count > 0 {
				matches = append(matches, match{count, m.CreatedAt, map[string]any{
					"message_id": m.ID, "role": m.Role, "task_id": m.RunID, "created_at": m.CreatedAt, "text": snippet(m.Text),
				}})
			}
		}
		if next == "" {
			break
		}
		before = next
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].at.After(matches[j].at)
	})
	found := []map[string]any{}
	for i := 0; i < len(matches) && i < resultFound; i++ {
		found = append(found, matches[i].item)
	}
	return map[string]any{"messages": found, "note": conversationNote}, nil
}

func loadedCapabilities(results []map[string]any) map[string]bool {
	loaded := map[string]bool{}
	for _, output := range results {
		switch names := output["loaded"].(type) {
		case []string:
			for _, name := range names {
				loaded[name] = true
			}
		case []any:
			for _, name := range names {
				if text, ok := name.(string); ok {
					loaded[text] = true
				}
			}
		}
	}
	return loaded
}

func (s *Service) capabilityLoad(ctx context.Context, names []string) (map[string]any, error) {
	byName := map[string]Capability{}
	valid := []string{}
	snapshot, dynamic := s.Tools(ctx)
	for _, capability := range s.catalog(dynamic, snapshot) {
		if !capability.Default {
			lower := strings.ToLower(capability.Name)
			byName[lower] = capability
			if bare, ok := strings.CutSuffix(lower, " (mcp)"); ok {
				bare = strings.TrimSpace(bare)
				if bare != "" {
					if _, exists := byName[bare]; !exists {
						byName[bare] = capability
					}
					if _, exists := byName["mcp_"+bare]; !exists {
						byName["mcp_"+bare] = capability
					}
					if _, exists := byName["mcp_"+bare+"__"]; !exists {
						byName["mcp_"+bare+"__"] = capability
					}
				}
			}
			valid = append(valid, capability.Name)
		}
	}
	loaded := []string{}
	seen := map[string]bool{}
	described := []map[string]any{}
	for _, name := range names {
		capability, ok := byName[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			return nil, errors.New(prompts.Runtime("capability-unknown", map[string]any{"Name": name, "Choices": strings.Join(valid, ", ")}))
		}
		if seen[capability.Name] {
			continue
		}
		seen[capability.Name] = true
		loaded = append(loaded, capability.Name)
		described = append(described, map[string]any{
			"name": capability.Name, "tools": capability.ToolList(), "account": capability.Account, "availability": capability.Availability,
		})
	}
	return map[string]any{"loaded": loaded, "capabilities": described, "note": prompts.Runtime("memory-these-tools-are-available-from-your-next-step", nil)}, nil
}
