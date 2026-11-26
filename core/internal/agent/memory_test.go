package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"nori/internal/extensions"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/transcript"
)

type fakeMemoryStore struct {
	state.Store
	memories    []state.Memory
	logs        map[string]state.MemoryLog
	summary     state.ConversationSummary
	contextFrom time.Time
	messages    []state.Message
	searchHits  []docstore.MemorySearchHit
	lastSearch  docstore.MemorySearchQuery
}

func (f *fakeMemoryStore) Memories(context.Context) ([]state.Memory, error) {
	return append([]state.Memory(nil), f.memories...), nil
}

func (f *fakeMemoryStore) MemoryLogs(context.Context, string, int) ([]state.MemoryLog, error) {
	out := make([]state.MemoryLog, 0, len(f.logs))
	for _, l := range f.logs {
		out = append(out, l)
	}
	return out, nil
}

func (f *fakeMemoryStore) MemoryLog(_ context.Context, date string) (state.MemoryLog, error) {
	l, ok := f.logs[date]
	if !ok {
		return state.MemoryLog{}, state.ErrNotFound
	}
	return l, nil
}

func (f *fakeMemoryStore) SearchMemory(_ context.Context, q docstore.MemorySearchQuery) ([]docstore.MemorySearchHit, error) {
	f.lastSearch = q
	return append([]docstore.MemorySearchHit(nil), f.searchHits...), nil
}

func (f *fakeMemoryStore) ContextFrom(context.Context) (time.Time, error) {
	return f.contextFrom, nil
}

func (f *fakeMemoryStore) ConversationSummary(context.Context) (state.ConversationSummary, error) {
	return f.summary, nil
}

func (f *fakeMemoryStore) ConversationMessages(_ context.Context, before string) ([]state.Message, string, error) {
	if before != "" {
		return nil, "", nil
	}
	return append([]state.Message(nil), f.messages...), "", nil
}

func TestMemoryToolsRegisteredAsDefaultDirectSafeReads(t *testing.T) {
	want := map[string]bool{"memory_read": false, "memory_search": false, "memory_get": false}
	for _, d := range BuiltinDefinitions {
		if _, tracked := want[d.ModelName]; !tracked {
			continue
		}
		want[d.ModelName] = true
		if d.Exposure != extensions.ExposureDirect {
			t.Errorf("%s exposure = %q, want %q", d.ModelName, d.Exposure, extensions.ExposureDirect)
		}
		if d.Policy.Effect != "read" {
			t.Errorf("%s effect = %q, want read", d.ModelName, d.Policy.Effect)
		}
		if d.Policy.Retry != "safe" {
			t.Errorf("%s retry = %q, want safe", d.ModelName, d.Policy.Retry)
		}
		if d.Capability != "memory" {
			t.Errorf("%s capability = %q, want memory", d.ModelName, d.Capability)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("missing builtin definition %q", name)
		}
	}
}

func TestValidateMemorySearchAndGetCalls(t *testing.T) {
	snap := extensions.NewSnapshot(BuiltinDefinitions)
	validCases := []transcript.ToolCall{
		{ID: "c1", Name: "memory_search", Args: map[string]any{"query": "tokyo", "layer": "", "limit": ""}},
		{ID: "c2", Name: "memory_search", Args: map[string]any{"query": "tokyo", "layer": "log", "limit": "5"}},
		{ID: "c3", Name: "memory_search", Args: map[string]any{"query": "tokyo", "limit": float64(10)}},
		{ID: "c4", Name: "memory_get", Args: map[string]any{"target": "core", "date": "", "entry_id": ""}},
		{ID: "c5", Name: "memory_get", Args: map[string]any{"target": "log", "date": "2026-11-23", "entry_id": "e1"}},
	}
	if err := ValidateCalls(validCases, true, true, snap); err != nil {
		t.Fatalf("expected valid memory tool calls to pass, got %v", err)
	}

	invalidCases := []transcript.ToolCall{
		{ID: "b1", Name: "memory_search", Args: map[string]any{"query": "   "}},
		{ID: "b2", Name: "memory_search", Args: map[string]any{"query": "tokyo", "layer": "working"}},
		{ID: "b3", Name: "memory_search", Args: map[string]any{"query": "tokyo", "limit": "25"}},
		{ID: "b4", Name: "memory_search", Args: map[string]any{"query": "tokyo", "limit": "abc"}},
		{ID: "b5", Name: "memory_get", Args: map[string]any{"target": "working"}},
		{ID: "b6", Name: "memory_get", Args: map[string]any{"target": "log", "date": "2026-99-99"}},
	}
	for _, tc := range invalidCases {
		if err := ValidateCalls([]transcript.ToolCall{tc}, true, true, snap); err == nil {
			t.Errorf("expected error for invalid call %+v", tc)
		}
	}
}

func TestMemoryToolsAndConversationFindContextFrom(t *testing.T) {
	cutoff := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	longSnippet := strings.Repeat("a", 900)
	store := &fakeMemoryStore{
		contextFrom: cutoff,
		memories: []state.Memory{
			{
				ID:         "mem_1",
				Text:       "Use UTC timestamps.",
				About:      "the user",
				Section:    "Preferences",
				Pinned:     true,
				Provenance: state.ProvenancePreCompactFlush,
				CreatedAt:  cutoff.Add(time.Hour),
				UpdatedAt:  cutoff.Add(2 * time.Hour),
			},
		},
		summary: state.ConversationSummary{
			From:           cutoff,
			FlushedThrough: cutoff.Add(3 * time.Hour),
			Through:        cutoff.Add(4 * time.Hour),
			Gist:           "Planning Tokyo trip.",
			Items: []state.SummaryItem{
				{Kind: "constraint", Quote: "No red-eye flights.", InputID: "in_1"},
			},
		},
		logs: map[string]state.MemoryLog{
			"2026-11-23": {
				Date: "2026-11-23",
				Entries: []state.MemoryLogEntry{
					{ID: "old_e", Text: "Old pre-cutoff note", Provenance: state.ProvenancePreCompactFlush, CreatedAt: cutoff.Add(-time.Hour), UpdatedAt: cutoff.Add(-time.Hour)},
					{ID: "new_e", Section: "Decisions", Text: "Book hotel in Shinjuku", Provenance: state.ProvenancePreCompactFlush, SourceInputID: "in_2", CreatedAt: cutoff.Add(5 * time.Hour), UpdatedAt: cutoff.Add(5 * time.Hour)},
				},
				UpdatedAt: cutoff.Add(5 * time.Hour),
			},
		},
		searchHits: []docstore.MemorySearchHit{
			{
				Chunk: docstore.MemoryChunk{
					ChunkID:    "log:2026-11-23:old_e",
					Layer:      docstore.MemoryLayerLog,
					LogDate:    "2026-11-23",
					Body:       "Old secret",
					Provenance: string(state.ProvenancePreCompactFlush),
					CreatedAt:  cutoff.Add(-time.Minute),
				},
				Snippet: "Old secret",
				Score:   2.0,
			},
			{
				Chunk: docstore.MemoryChunk{
					ChunkID:    "core:mem_1",
					Layer:      docstore.MemoryLayerCore,
					Section:    "Preferences",
					About:      "the user",
					Body:       longSnippet,
					Provenance: string(state.ProvenancePreCompactFlush),
					Pinned:     true,
					CreatedAt:  cutoff.Add(time.Hour),
				},
				Snippet: longSnippet,
				Score:   1.15,
			},
			{
				Chunk: docstore.MemoryChunk{
					ChunkID:    "log:2026-11-23:new_e",
					Layer:      docstore.MemoryLayerLog,
					LogDate:    "2026-11-23",
					Section:    "Decisions",
					About:      "trip",
					Body:       "Book hotel in Shinjuku",
					Provenance: string(state.ProvenancePreCompactFlush),
					CreatedAt:  cutoff.Add(5 * time.Hour),
				},
				Snippet: "Book hotel in Shinjuku",
				Score:   0.85,
			},
		},
		messages: []state.Message{
			{ID: "m-old", Role: "user", Text: "Secret tokyo flight before rewind", CreatedAt: cutoff.Add(-time.Minute)},
			{ID: "m-new", Role: "user", Text: "New tokyo hotel plan after rewind", CreatedAt: cutoff.Add(time.Minute)},
		},
	}
	svc := &Service{Store: store, Now: func() time.Time { return now }}
	ctx := context.Background()

	readOut, err := svc.CallRuntimeTool(ctx, "run-1", "memory_read", map[string]any{})
	if err != nil {
		t.Fatalf("memory_read: %v", err)
	}
	mems := readOut["memories"].([]map[string]any)
	if len(mems) != 1 || mems[0]["section"] != "Preferences" || mems[0]["pinned"] != true || mems[0]["provenance"] != state.ProvenancePreCompactFlush {
		t.Fatalf("unexpected memory_read memories: %+v", mems)
	}
	if _, ok := readOut["conversation_summary"].(map[string]any); !ok {
		t.Fatalf("expected conversation_summary in memory_read output")
	}

	searchOut, err := svc.CallRuntimeTool(ctx, "run-1", "memory_search", map[string]any{"query": "tokyo", "layer": "", "limit": "5"})
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	if !store.lastSearch.CreatedAfter.Equal(cutoff) || !store.lastSearch.Now.Equal(now) || store.lastSearch.Limit != 5 {
		t.Fatalf("unexpected SearchMemory query: %+v", store.lastSearch)
	}
	results := searchOut["results"].([]map[string]any)
	if len(results) != 2 {
		t.Fatalf("expected 2 post-ContextFrom search hits, got %d: %+v", len(results), results)
	}
	if results[0]["memory_id"] != "mem_1" || len(results[0]["snippet"].(string)) > docstore.MaxMemorySnippetBytes {
		t.Fatalf("unexpected core search hit: %+v", results[0])
	}
	if results[1]["log_date"] != "2026-11-23" || results[1]["entry_id"] != "new_e" {
		t.Fatalf("unexpected log search hit: %+v", results[1])
	}
	if searchOut["note"] != memoryNote {
		t.Fatalf("expected memoryNote on memory_search")
	}

	coreOut, err := svc.CallRuntimeTool(ctx, "run-1", "memory_get", map[string]any{"target": "core"})
	if err != nil {
		t.Fatalf("memory_get core: %v", err)
	}
	coreItems := coreOut["memories"].([]map[string]any)
	if len(coreItems) != 1 || coreItems[0]["memory_id"] != "mem_1" || coreItems[0]["section"] != "Preferences" {
		t.Fatalf("unexpected memory_get core: %+v", coreItems)
	}

	logOut, err := svc.CallRuntimeTool(ctx, "run-1", "memory_get", map[string]any{"target": "log", "date": "2026-11-23"})
	if err != nil {
		t.Fatalf("memory_get log: %v", err)
	}
	logEntries := logOut["entries"].([]map[string]any)
	if len(logEntries) != 1 || logEntries[0]["entry_id"] != "new_e" {
		t.Fatalf("expected only post-ContextFrom entry new_e, got %+v", logEntries)
	}
	if _, err := svc.CallRuntimeTool(ctx, "run-1", "memory_get", map[string]any{"target": "log", "date": "2026-11-23", "entry_id": "old_e"}); err == nil {
		t.Fatalf("expected error requesting pre-ContextFrom entry_id old_e")
	}
	entryOut, err := svc.CallRuntimeTool(ctx, "run-1", "memory_get", map[string]any{"target": "log", "date": "2026-11-23", "entry_id": "new_e"})
	if err != nil || len(entryOut["entries"].([]map[string]any)) != 1 {
		t.Fatalf("memory_get specific entry failed: out=%+v err=%v", entryOut, err)
	}

	findOut, err := svc.CallRuntimeTool(ctx, "run-1", "conversation_find", map[string]any{"query": "tokyo"})
	if err != nil {
		t.Fatalf("conversation_find: %v", err)
	}
	foundMsgs := findOut["messages"].([]map[string]any)
	if len(foundMsgs) != 1 || foundMsgs[0]["message_id"] != "m-new" {
		t.Fatalf("expected only post-ContextFrom message m-new, got %+v", foundMsgs)
	}
}
