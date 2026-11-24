package docstore

import (
	"context"
	"time"
	"unicode/utf8"
)

const (
	MemoryLayerCore = "core"
	MemoryLayerLog  = "log"

	DefaultMemorySearchLimit   = 8
	MaxMemorySearchLimit       = 20
	MaxMemorySnippetBytes      = 700
	MemorySearchCandidateLimit = 32
	MemoryRecencyHalfLifeDays  = 30.0
	MemoryMMRLambda            = 0.75
	MemoryPinnedBoost          = 1.15
)

// MemoryChunk mirrors one row of the isolated 32-way hash-partitioned
// nori_memory_chunks FTS index table.
type MemoryChunk struct {
	Owner       string    `json:"owner"`
	ChunkID     string    `json:"chunk_id"`           // "core:<memory_id>" or "log:<date>:<entry_id>"
	Layer       string    `json:"layer"`              // "core" or "log"
	LogDate     string    `json:"log_date,omitempty"` // "" for core; "YYYY-MM-DD" for log
	Section     string    `json:"section,omitempty"`
	About       string    `json:"about,omitempty"`
	Body        string    `json:"body"`
	Provenance  string    `json:"provenance"`
	SourceInput string    `json:"source_input,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	Pinned      bool      `json:"pinned,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MemorySearchQuery configures one full-text memory search over MemoryIndex.
type MemorySearchQuery struct {
	Owner        string    // DataOwner ID (matches owners/<owner>)
	Query        string    // Free-text query (dual-dictionary english + simple)
	Layer        string    // "" (all), "core", or "log"
	CreatedAfter time.Time // Optional lower bound for ContextFrom isolation
	Limit        int       // Default DefaultMemorySearchLimit (8), max MaxMemorySearchLimit (20)
	Now          time.Time // Reference clock for 30-day half-life recency decay
}

// MemorySearchHit is one ranked, MMR-reranked memory match returned by MemoryIndex.
type MemorySearchHit struct {
	Chunk   MemoryChunk `json:"chunk"`
	Snippet string      `json:"snippet"` // Capped at MaxMemorySnippetBytes (700) UTF-8 bytes
	Score   float64     `json:"score"`
}

// MemoryIndexWriter maintains nori_memory_chunks inside the same transaction as
// canonical nori_records mutations.
type MemoryIndexWriter interface {
	UpsertMemoryChunk(ctx context.Context, chunk MemoryChunk) error
	DeleteMemoryChunk(ctx context.Context, owner, chunkID string) error
}

// MemoryIndex provides READ COMMITTED full-text search and chunk enumeration
// over nori_memory_chunks, plus standalone MemoryIndexWriter methods.
// Implemented by both state/postgres and sim/memstore.
type MemoryIndex interface {
	SearchMemory(ctx context.Context, query MemorySearchQuery) ([]MemorySearchHit, error)
	ListMemoryChunks(ctx context.Context, owner string) ([]MemoryChunk, error)
	MemoryIndexWriter
}

// BackendUnwrapper lets thin Backend wrappers (such as records.NewShared) expose
// optional backend capabilities like MemoryIndex.
type BackendUnwrapper interface {
	Unwrap() Backend
}

// AsMemoryIndex returns the underlying MemoryIndex if b (or an unwrapped Backend) implements it.
func AsMemoryIndex(b Backend) (MemoryIndex, bool) {
	for b != nil {
		if idx, ok := b.(MemoryIndex); ok {
			return idx, true
		}
		u, ok := b.(BackendUnwrapper)
		if !ok {
			return nil, false
		}
		b = u.Unwrap()
	}
	return nil, false
}

// MemoryCoreChunkID returns the canonical chunk_id for a Core memory.
func MemoryCoreChunkID(memoryID string) string {
	return "core:" + memoryID
}

// MemoryLogChunkID returns the canonical chunk_id for a Daily Log entry.
func MemoryLogChunkID(date, entryID string) string {
	return "log:" + date + ":" + entryID
}

// ClipUTF8 truncates s to at most maxBytes without splitting a multi-byte UTF-8 rune.
func ClipUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// HasMemoryIndex reports whether the transaction backend supports same-tx memory FTS writes.
func (t *Transaction) HasMemoryIndex() bool {
	_, ok := t.backend.(MemoryIndexWriter)
	return ok
}

// UpsertMemoryChunk writes or updates a chunk inside the current transaction when supported.
func (t *Transaction) UpsertMemoryChunk(chunk MemoryChunk) error {
	w, ok := t.backend.(MemoryIndexWriter)
	if !ok {
		return nil
	}
	return w.UpsertMemoryChunk(t.ctx, chunk)
}

// DeleteMemoryChunk removes a chunk inside the current transaction when supported.
func (t *Transaction) DeleteMemoryChunk(owner, chunkID string) error {
	w, ok := t.backend.(MemoryIndexWriter)
	if !ok {
		return nil
	}
	return w.DeleteMemoryChunk(t.ctx, owner, chunkID)
}

// HasMemoryIndex reports whether the client's backend implements MemoryIndex.
func (c *Client) HasMemoryIndex() bool {
	_, ok := AsMemoryIndex(c.Backend)
	return ok
}

// SearchMemory queries the backend's MemoryIndex when supported.
func (c *Client) SearchMemory(ctx context.Context, query MemorySearchQuery) ([]MemorySearchHit, bool, error) {
	idx, ok := AsMemoryIndex(c.Backend)
	if !ok {
		return nil, false, nil
	}
	hits, err := idx.SearchMemory(ctx, query)
	return hits, true, err
}

// ListMemoryChunks lists all indexed memory chunks for owner when supported.
func (c *Client) ListMemoryChunks(ctx context.Context, owner string) ([]MemoryChunk, bool, error) {
	idx, ok := AsMemoryIndex(c.Backend)
	if !ok {
		return nil, false, nil
	}
	chunks, err := idx.ListMemoryChunks(ctx, owner)
	return chunks, true, err
}
