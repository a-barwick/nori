package postgres

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"nori/internal/state/docstore"
)

var (
	_ docstore.MemoryIndex       = (*backend)(nil)
	_ docstore.MemoryIndexWriter = (*backend)(nil)
	_ docstore.MemoryIndexWriter = (*transaction)(nil)
)

const upsertMemoryChunkSQL = `
INSERT INTO nori_memory_chunks (
	owner, chunk_id, layer, log_date, section, about, body,
	provenance, source_input, run_id, pinned, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
ON CONFLICT (owner, chunk_id) DO UPDATE SET
	layer = EXCLUDED.layer,
	log_date = EXCLUDED.log_date,
	section = EXCLUDED.section,
	about = EXCLUDED.about,
	body = EXCLUDED.body,
	provenance = EXCLUDED.provenance,
	source_input = EXCLUDED.source_input,
	run_id = EXCLUDED.run_id,
	pinned = EXCLUDED.pinned,
	created_at = EXCLUDED.created_at,
	updated_at = EXCLUDED.updated_at`

const deleteMemoryChunkSQL = `
DELETE FROM nori_memory_chunks WHERE owner = $1 AND chunk_id = $2`

const listMemoryChunksSQL = `
SELECT owner, chunk_id, layer, COALESCE(to_char(log_date, 'YYYY-MM-DD'), ''),
       section, about, body, provenance, source_input, run_id, pinned,
       created_at, updated_at
FROM nori_memory_chunks
WHERE owner = $1
ORDER BY chunk_id ASC`

const searchMemoryChunksSQL = `
WITH q AS (
	SELECT
		websearch_to_tsquery('english', $2) || websearch_to_tsquery('simple', $2) AS tsq,
		lower($2) AS q_lower
)
SELECT
	c.owner,
	c.chunk_id,
	c.layer,
	COALESCE(to_char(c.log_date, 'YYYY-MM-DD'), ''),
	c.section,
	c.about,
	c.body,
	c.provenance,
	c.source_input,
	c.run_id,
	c.pinned,
	c.created_at,
	c.updated_at,
	(
		ts_rank_cd(c.search_tsv, q.tsq, 32)
		+ CASE
			WHEN q.q_lower <> '' AND (
				position(q.q_lower IN lower(c.section)) > 0
				OR position(q.q_lower IN lower(c.about)) > 0
				OR position(q.q_lower IN lower(c.body)) > 0
			) THEN 0.25
			ELSE 0.0
		  END
	) * (
		CASE
			WHEN c.layer = 'core' AND c.pinned THEN 1.15
			WHEN c.layer = 'core' THEN 1.0
			ELSE power(2.0, -GREATEST(0.0, EXTRACT(EPOCH FROM ($5::timestamptz - c.updated_at)) / 86400.0) / 30.0)
		END
	) AS score
FROM nori_memory_chunks c, q
WHERE c.owner = $1
  AND ($3 = '' OR c.layer = $3)
  AND ($4::timestamptz IS NULL OR c.created_at >= $4)
  AND c.search_tsv @@ q.tsq
ORDER BY score DESC, c.chunk_id ASC
LIMIT $6`

func upsertMemoryChunk(ctx context.Context, q executor, chunk docstore.MemoryChunk) error {
	if chunk.Owner == "" || chunk.ChunkID == "" {
		return errors.New("memory chunk requires owner and chunk_id")
	}
	if chunk.Layer != docstore.MemoryLayerCore && chunk.Layer != docstore.MemoryLayerLog {
		return errors.New("memory chunk layer must be core or log")
	}
	var logDate any
	if chunk.Layer == docstore.MemoryLayerLog && chunk.LogDate != "" {
		logDate = chunk.LogDate
	}
	createdAt := chunk.CreatedAt.UTC()
	updatedAt := chunk.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	_, err := q.Exec(ctx, upsertMemoryChunkSQL,
		chunk.Owner,
		chunk.ChunkID,
		chunk.Layer,
		logDate,
		chunk.Section,
		chunk.About,
		chunk.Body,
		chunk.Provenance,
		chunk.SourceInput,
		chunk.RunID,
		chunk.Pinned,
		createdAt,
		updatedAt,
	)
	return err
}

func deleteMemoryChunk(ctx context.Context, q executor, owner, chunkID string) error {
	if owner == "" || chunkID == "" {
		return errors.New("delete memory chunk requires owner and chunk_id")
	}
	_, err := q.Exec(ctx, deleteMemoryChunkSQL, owner, chunkID)
	return err
}

func (b *backend) UpsertMemoryChunk(ctx context.Context, chunk docstore.MemoryChunk) error {
	return upsertMemoryChunk(ctx, b.pool, chunk)
}

func (b *backend) DeleteMemoryChunk(ctx context.Context, owner, chunkID string) error {
	return deleteMemoryChunk(ctx, b.pool, owner, chunkID)
}

func (t *transaction) UpsertMemoryChunk(ctx context.Context, chunk docstore.MemoryChunk) error {
	if chunk.Owner != "" {
		if err := t.ensureTx(ctx, "owners/"+chunk.Owner); err != nil {
			return err
		}
	}
	return upsertMemoryChunk(ctx, t.q, chunk)
}

func (t *transaction) DeleteMemoryChunk(ctx context.Context, owner, chunkID string) error {
	if owner != "" {
		if err := t.ensureTx(ctx, "owners/"+owner); err != nil {
			return err
		}
	}
	return deleteMemoryChunk(ctx, t.q, owner, chunkID)
}

func (b *backend) ListMemoryChunks(ctx context.Context, owner string) ([]docstore.MemoryChunk, error) {
	if strings.TrimSpace(owner) == "" {
		return []docstore.MemoryChunk{}, nil
	}
	out := []docstore.MemoryChunk{}
	err := b.readCommitted(ctx, true, func(q executor) error {
		rows, err := q.Query(ctx, listMemoryChunksSQL, owner)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = out[:0]
		for rows.Next() {
			var c docstore.MemoryChunk
			if err := rows.Scan(
				&c.Owner, &c.ChunkID, &c.Layer, &c.LogDate,
				&c.Section, &c.About, &c.Body, &c.Provenance, &c.SourceInput, &c.RunID, &c.Pinned,
				&c.CreatedAt, &c.UpdatedAt,
			); err != nil {
				return err
			}
			c.CreatedAt = c.CreatedAt.UTC()
			c.UpdatedAt = c.UpdatedAt.UTC()
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (b *backend) SearchMemory(ctx context.Context, query docstore.MemorySearchQuery) ([]docstore.MemorySearchHit, error) {
	owner := strings.TrimSpace(query.Owner)
	qText := strings.TrimSpace(query.Query)
	if owner == "" || qText == "" {
		return []docstore.MemorySearchHit{}, nil
	}
	layer := strings.TrimSpace(query.Layer)
	if layer != "" && layer != docstore.MemoryLayerCore && layer != docstore.MemoryLayerLog {
		return []docstore.MemorySearchHit{}, nil
	}
	limit := query.Limit
	if limit <= 0 {
		limit = docstore.DefaultMemorySearchLimit
	}
	if limit > docstore.MaxMemorySearchLimit {
		limit = docstore.MaxMemorySearchLimit
	}
	now := query.Now.UTC()
	if query.Now.IsZero() {
		now = time.Now().UTC()
	}
	var createdAfter any
	if !query.CreatedAfter.IsZero() {
		createdAfter = query.CreatedAfter.UTC()
	}

	candidates := []docstore.MemorySearchHit{}
	err := b.readCommitted(ctx, true, func(q executor) error {
		rows, err := q.Query(ctx, searchMemoryChunksSQL,
			owner,
			qText,
			layer,
			createdAfter,
			now,
			docstore.MemorySearchCandidateLimit,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		candidates = candidates[:0]
		for rows.Next() {
			var c docstore.MemoryChunk
			var score float64
			if err := rows.Scan(
				&c.Owner, &c.ChunkID, &c.Layer, &c.LogDate,
				&c.Section, &c.About, &c.Body, &c.Provenance, &c.SourceInput, &c.RunID, &c.Pinned,
				&c.CreatedAt, &c.UpdatedAt, &score,
			); err != nil {
				return err
			}
			c.CreatedAt = c.CreatedAt.UTC()
			c.UpdatedAt = c.UpdatedAt.UTC()
			candidates = append(candidates, docstore.MemorySearchHit{
				Chunk:   c,
				Snippet: buildMemorySnippet(c.Body, qText, docstore.MaxMemorySnippetBytes),
				Score:   score,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return []docstore.MemorySearchHit{}, nil
	}

	slices.SortFunc(candidates, func(a, b docstore.MemorySearchHit) int {
		if a.Score != b.Score {
			if a.Score > b.Score {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Chunk.ChunkID, b.Chunk.ChunkID)
	})

	return rerankMemoryMMR(candidates, limit, docstore.MemoryMMRLambda), nil
}

func rerankMemoryMMR(candidates []docstore.MemorySearchHit, limit int, lambda float64) []docstore.MemorySearchHit {
	if len(candidates) == 0 || limit <= 0 {
		return []docstore.MemorySearchHit{}
	}
	if limit > len(candidates) {
		limit = len(candidates)
	}
	maxScore := candidates[0].Score
	for _, c := range candidates[1:] {
		if c.Score > maxScore {
			maxScore = c.Score
		}
	}
	tokenSets := make([]map[string]struct{}, len(candidates))
	for i, c := range candidates {
		tokenSets[i] = mmrTokens(c.Chunk.Section + " " + c.Chunk.About + " " + c.Chunk.Body)
	}

	selectedIdx := make([]int, 0, limit)
	used := make([]bool, len(candidates))

	selectedIdx = append(selectedIdx, 0)
	used[0] = true

	for len(selectedIdx) < limit {
		bestIdx := -1
		bestMMR := -2.0
		for i, c := range candidates {
			if used[i] {
				continue
			}
			rel := 0.0
			if maxScore > 0 {
				rel = c.Score / maxScore
			}
			maxSim := 0.0
			for _, s := range selectedIdx {
				if sim := jaccardSim(tokenSets[i], tokenSets[s]); sim > maxSim {
					maxSim = sim
				}
			}
			mmr := lambda*rel - (1.0-lambda)*maxSim
			if bestIdx == -1 || mmr > bestMMR ||
				(mmr == bestMMR && (c.Score > candidates[bestIdx].Score ||
					(c.Score == candidates[bestIdx].Score && c.Chunk.ChunkID < candidates[bestIdx].Chunk.ChunkID))) {
				bestIdx = i
				bestMMR = mmr
			}
		}
		if bestIdx == -1 {
			break
		}
		used[bestIdx] = true
		selectedIdx = append(selectedIdx, bestIdx)
	}

	out := make([]docstore.MemorySearchHit, 0, len(selectedIdx))
	for _, idx := range selectedIdx {
		out = append(out, candidates[idx])
	}
	return out
}

func mmrTokens(s string) map[string]struct{} {
	set := map[string]struct{}{}
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			set[b.String()] = struct{}{}
			b.Reset()
		}
	}
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return set
}

func jaccardSim(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func buildMemorySnippet(body, query string, maxBytes int) string {
	text := strings.TrimSpace(body)
	if len(text) <= maxBytes {
		return text
	}
	lowerText := strings.ToLower(text)
	matchPos := -1
	qLower := strings.ToLower(strings.TrimSpace(query))
	if qLower != "" {
		matchPos = strings.Index(lowerText, qLower)
	}
	if matchPos < 0 {
		for tok := range mmrTokens(query) {
			if idx := strings.Index(lowerText, tok); idx >= 0 && (matchPos < 0 || idx < matchPos) {
				matchPos = idx
			}
		}
	}
	if matchPos < 0 || matchPos < maxBytes/2 {
		return docstore.ClipUTF8(text, maxBytes)
	}
	if matchPos > len(text) {
		matchPos = len(text)
	}
	start := matchPos - maxBytes/3
	if start < 0 {
		start = 0
	}
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return docstore.ClipUTF8(text[start:], maxBytes)
}
