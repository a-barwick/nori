package memstore

import (
	"cmp"
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"nori/internal/state/docstore"
)

type memoryChunkKey struct {
	owner   string
	chunkID string
}

var (
	_ docstore.MemoryIndex       = (*Backend)(nil)
	_ docstore.MemoryIndexWriter = (*Backend)(nil)
	_ docstore.MemoryIndexWriter = (*transaction)(nil)
)

func cloneMemoryChunk(c docstore.MemoryChunk) docstore.MemoryChunk {
	c.CreatedAt = c.CreatedAt.UTC()
	c.UpdatedAt = c.UpdatedAt.UTC()
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = c.CreatedAt
	}
	if c.Layer != docstore.MemoryLayerLog {
		c.LogDate = ""
	}
	return c
}

func memoryChunkEqual(a, b docstore.MemoryChunk) bool {
	return a.Owner == b.Owner &&
		a.ChunkID == b.ChunkID &&
		a.Layer == b.Layer &&
		a.LogDate == b.LogDate &&
		a.Section == b.Section &&
		a.About == b.About &&
		a.Body == b.Body &&
		a.Provenance == b.Provenance &&
		a.SourceInput == b.SourceInput &&
		a.RunID == b.RunID &&
		a.Pinned == b.Pinned &&
		a.CreatedAt.Equal(b.CreatedAt) &&
		a.UpdatedAt.Equal(b.UpdatedAt)
}

func compareMemoryChunkKeyOrder(a, b docstore.MemoryChunk) int {
	if c := cmp.Compare(a.Owner, b.Owner); c != 0 {
		return c
	}
	return cmp.Compare(a.ChunkID, b.ChunkID)
}

func validateMemoryChunk(chunk docstore.MemoryChunk) error {
	if chunk.Owner == "" || chunk.ChunkID == "" {
		return errors.New("memory chunk requires owner and chunk_id")
	}
	if chunk.Layer != docstore.MemoryLayerCore && chunk.Layer != docstore.MemoryLayerLog {
		return errors.New("memory chunk layer must be core or log")
	}
	return nil
}

func (b *Backend) UpsertMemoryChunk(ctx context.Context, chunk docstore.MemoryChunk) error {
	return b.RunTransaction(ctx, false, defaultTxAttempts, func(tx docstore.TransactionBackend) error {
		return tx.(docstore.MemoryIndexWriter).UpsertMemoryChunk(ctx, chunk)
	})
}

func (b *Backend) DeleteMemoryChunk(ctx context.Context, owner, chunkID string) error {
	return b.RunTransaction(ctx, false, defaultTxAttempts, func(tx docstore.TransactionBackend) error {
		return tx.(docstore.MemoryIndexWriter).DeleteMemoryChunk(ctx, owner, chunkID)
	})
}

func (t *transaction) UpsertMemoryChunk(ctx context.Context, chunk docstore.MemoryChunk) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.readOnly {
		return ErrReadOnly
	}
	if err := validateMemoryChunk(chunk); err != nil {
		return err
	}
	if err := t.b.faultInjector().maybeApplyErrFor("memchunk:" + chunk.Owner + ":" + chunk.ChunkID); err != nil {
		return err
	}
	cp := cloneMemoryChunk(chunk)
	k := memoryChunkKey{owner: cp.Owner, chunkID: cp.ChunkID}
	t.chunkWrites[k] = &cp
	return nil
}

func (t *transaction) DeleteMemoryChunk(ctx context.Context, owner, chunkID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.readOnly {
		return ErrReadOnly
	}
	if owner == "" || chunkID == "" {
		return errors.New("delete memory chunk requires owner and chunk_id")
	}
	if err := t.b.faultInjector().maybeApplyErrFor("memchunk:" + owner + ":" + chunkID); err != nil {
		return err
	}
	k := memoryChunkKey{owner: owner, chunkID: chunkID}
	t.chunkWrites[k] = nil
	return nil
}

func (b *Backend) ListMemoryChunks(ctx context.Context, owner string) ([]docstore.MemoryChunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return []docstore.MemoryChunk{}, nil
	}
	if err := b.faultInjector().maybeQueryErrFor("memory_chunks:" + owner); err != nil {
		return nil, err
	}
	b.mu.Lock()
	out := make([]docstore.MemoryChunk, 0)
	for k, c := range b.memoryChunks {
		if k.owner == owner {
			out = append(out, cloneMemoryChunk(c))
		}
	}
	b.mu.Unlock()
	slices.SortFunc(out, func(a, b docstore.MemoryChunk) int {
		return cmp.Compare(a.ChunkID, b.ChunkID)
	})
	return out, nil
}

func (b *Backend) SearchMemory(ctx context.Context, query docstore.MemorySearchQuery) ([]docstore.MemorySearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owner := strings.TrimSpace(query.Owner)
	qText := strings.TrimSpace(query.Query)
	if owner == "" || qText == "" {
		return []docstore.MemorySearchHit{}, nil
	}
	layer := strings.TrimSpace(query.Layer)
	if layer != "" && layer != docstore.MemoryLayerCore && layer != docstore.MemoryLayerLog {
		return []docstore.MemorySearchHit{}, nil
	}
	if err := b.faultInjector().maybeQueryErrFor("memory_search:" + owner); err != nil {
		return nil, err
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
		now = b.now()
	}

	engQuery := parseWebsearchQuery(qText, true)
	simQuery := parseWebsearchQuery(qText, false)
	if engQuery.empty() && simQuery.empty() {
		return []docstore.MemorySearchHit{}, nil
	}
	qLower := strings.ToLower(qText)

	b.mu.Lock()
	chunks := make([]docstore.MemoryChunk, 0)
	for k, c := range b.memoryChunks {
		if k.owner != owner {
			continue
		}
		if layer != "" && c.Layer != layer {
			continue
		}
		if !query.CreatedAfter.IsZero() && c.CreatedAt.Before(query.CreatedAfter.UTC()) {
			continue
		}
		chunks = append(chunks, cloneMemoryChunk(c))
	}
	b.mu.Unlock()

	candidates := make([]docstore.MemorySearchHit, 0)
	for _, c := range chunks {
		headText := c.Section + " " + c.About
		engHead := tokenizeEnglish(headText)
		engBody := tokenizeEnglish(c.Body)
		simAll := tokenizeSimple(headText + " " + c.Body)

		engMatched, engRank := scoreQuery(engQuery, engHead, engBody, 1.0, 0.4)
		simMatched, simRank := scoreQuery(simQuery, nil, simAll, 0.0, 0.4)

		if !engMatched && !simMatched {
			continue
		}

		rawRank := engRank + simRank
		lexScore := rawRank / (rawRank + 1.0)
		if qLower != "" && (strings.Contains(strings.ToLower(c.Section), qLower) ||
			strings.Contains(strings.ToLower(c.About), qLower) ||
			strings.Contains(strings.ToLower(c.Body), qLower)) {
			lexScore += 0.25
		}

		decay := 1.0
		if c.Layer == docstore.MemoryLayerCore {
			if c.Pinned {
				decay = docstore.MemoryPinnedBoost
			}
		} else {
			ageDays := now.Sub(c.UpdatedAt).Seconds() / 86400.0
			if ageDays < 0 {
				ageDays = 0
			}
			decay = math.Pow(2.0, -ageDays/docstore.MemoryRecencyHalfLifeDays)
		}

		candidates = append(candidates, docstore.MemorySearchHit{
			Chunk:   c,
			Snippet: buildMemorySnippet(c.Body, qText, docstore.MaxMemorySnippetBytes),
			Score:   lexScore * decay,
		})
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
	if len(candidates) > docstore.MemorySearchCandidateLimit {
		candidates = candidates[:docstore.MemorySearchCandidateLimit]
	}

	return rerankMemoryMMR(candidates, limit, docstore.MemoryMMRLambda), nil
}

type websearchQuery struct {
	clauses []websearchClause
}

type websearchClause struct {
	must    [][]string
	mustNot [][]string
}

func (q websearchQuery) empty() bool {
	for _, cl := range q.clauses {
		if len(cl.must) > 0 || len(cl.mustNot) > 0 {
			return false
		}
	}
	return true
}

func parseWebsearchQuery(raw string, english bool) websearchQuery {
	type rawPart struct {
		text    string
		quoted  bool
		negated bool
		isOR    bool
	}
	var parts []rawPart
	i := 0
	n := len(raw)
	for i < n {
		for i < n && unicode.IsSpace(rune(raw[i])) {
			i++
		}
		if i >= n {
			break
		}
		neg := false
		if raw[i] == '-' && i+1 < n && !unicode.IsSpace(rune(raw[i+1])) {
			neg = true
			i++
		}
		if i < n && raw[i] == '"' {
			i++
			start := i
			for i < n && raw[i] != '"' {
				i++
			}
			phrase := raw[start:i]
			if i < n && raw[i] == '"' {
				i++
			}
			if strings.TrimSpace(phrase) != "" {
				parts = append(parts, rawPart{text: phrase, quoted: true, negated: neg})
			}
			continue
		}
		start := i
		for i < n && !unicode.IsSpace(rune(raw[i])) {
			i++
		}
		word := raw[start:i]
		if !neg && word == "OR" {
			parts = append(parts, rawPart{isOR: true})
			continue
		}
		parts = append(parts, rawPart{text: word, negated: neg})
	}

	var clauses []websearchClause
	var cur websearchClause
	tokFn := tokenizeSimple
	if english {
		tokFn = tokenizeEnglish
	}
	flushClause := func() {
		if len(cur.must) > 0 || len(cur.mustNot) > 0 {
			clauses = append(clauses, cur)
			cur = websearchClause{}
		}
	}
	for _, p := range parts {
		if p.isOR {
			flushClause()
			continue
		}
		toks := tokFn(p.text)
		if len(toks) == 0 {
			continue
		}
		if p.quoted {
			if p.negated {
				cur.mustNot = append(cur.mustNot, toks)
			} else {
				cur.must = append(cur.must, toks)
			}
		} else {
			for _, tok := range toks {
				if p.negated {
					cur.mustNot = append(cur.mustNot, []string{tok})
				} else {
					cur.must = append(cur.must, []string{tok})
				}
			}
		}
	}
	flushClause()
	return websearchQuery{clauses: clauses}
}

func scoreQuery(q websearchQuery, headToks, bodyToks []string, headWeight, bodyWeight float64) (bool, float64) {
	if q.empty() {
		return false, 0
	}
	headSet := tokenSet(headToks)
	bodySet := tokenSet(bodyToks)
	matchedAny := false
	totalScore := 0.0

	for _, cl := range q.clauses {
		clauseOK := true
		for _, seq := range cl.mustNot {
			if matchSeq(seq, headToks, headSet) || matchSeq(seq, bodyToks, bodySet) {
				clauseOK = false
				break
			}
		}
		if !clauseOK {
			continue
		}
		clauseScore := 0.0
		for _, seq := range cl.must {
			inHead := matchSeq(seq, headToks, headSet)
			inBody := matchSeq(seq, bodyToks, bodySet)
			if !inHead && !inBody {
				clauseOK = false
				break
			}
			if inHead {
				clauseScore += headWeight
			}
			if inBody {
				clauseScore += bodyWeight
			}
		}
		if clauseOK && (len(cl.must) > 0 || len(cl.mustNot) > 0) {
			matchedAny = true
			if clauseScore == 0 {
				clauseScore = 0.1
			}
			totalScore += clauseScore
		}
	}
	return matchedAny, totalScore
}

func tokenSet(toks []string) map[string]struct{} {
	if len(toks) == 0 {
		return nil
	}
	s := make(map[string]struct{}, len(toks))
	for _, t := range toks {
		s[t] = struct{}{}
	}
	return s
}

func matchSeq(seq []string, doc []string, set map[string]struct{}) bool {
	if len(seq) == 0 || len(doc) == 0 {
		return false
	}
	if len(seq) == 1 {
		_, ok := set[seq[0]]
		return ok
	}
	for i := 0; i+len(seq) <= len(doc); i++ {
		if doc[i] != seq[0] {
			continue
		}
		ok := true
		for j := 1; j < len(seq); j++ {
			if doc[i+j] != seq[j] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func tokenizeSimple(s string) []string {
	lower := strings.ToLower(s)
	var out []string
	var buf strings.Builder
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		tok := strings.Trim(buf.String(), "-./@:")
		buf.Reset()
		if tok == "" {
			return
		}
		out = append(out, tok)
		if strings.ContainsAny(tok, "-./@:") {
			parts := strings.FieldsFunc(tok, func(r rune) bool {
				return r == '-' || r == '.' || r == '/' || r == '@' || r == ':'
			})
			if len(parts) > 1 {
				for _, p := range parts {
					if p != "" && p != tok {
						out = append(out, p)
					}
				}
			}
		}
	}
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '.' || r == '@' || r == '/' || r == ':' {
			buf.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func tokenizeEnglish(s string) []string {
	lower := strings.ToLower(s)
	var out []string
	var buf strings.Builder
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		word := buf.String()
		buf.Reset()
		if _, stop := englishStopwords[word]; stop {
			return
		}
		stem := stemEnglish(word)
		if stem != "" {
			out = append(out, stem)
		}
	}
	for _, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			buf.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

var englishStopwords = map[string]struct{}{
	"a": {}, "about": {}, "above": {}, "after": {}, "again": {}, "against": {}, "all": {}, "am": {}, "an": {},
	"and": {}, "any": {}, "are": {}, "as": {}, "at": {}, "be": {}, "because": {}, "been": {}, "before": {},
	"being": {}, "below": {}, "between": {}, "both": {}, "but": {}, "by": {}, "can": {}, "did": {}, "do": {},
	"does": {}, "doing": {}, "don": {}, "down": {}, "during": {}, "each": {}, "few": {}, "for": {}, "from": {},
	"further": {}, "had": {}, "has": {}, "have": {}, "having": {}, "he": {}, "her": {}, "here": {}, "hers": {},
	"herself": {}, "him": {}, "himself": {}, "his": {}, "how": {}, "i": {}, "if": {}, "in": {}, "into": {},
	"is": {}, "it": {}, "its": {}, "itself": {}, "just": {}, "me": {}, "more": {}, "most": {}, "my": {},
	"myself": {}, "no": {}, "nor": {}, "not": {}, "now": {}, "of": {}, "off": {}, "on": {}, "once": {},
	"only": {}, "or": {}, "other": {}, "our": {}, "ours": {}, "ourselves": {}, "out": {}, "over": {}, "own": {},
	"s": {}, "same": {}, "she": {}, "should": {}, "so": {}, "some": {}, "such": {}, "t": {}, "than": {},
	"that": {}, "the": {}, "their": {}, "theirs": {}, "them": {}, "themselves": {}, "then": {}, "there": {},
	"these": {}, "they": {}, "this": {}, "those": {}, "through": {}, "to": {}, "too": {}, "under": {},
	"until": {}, "up": {}, "very": {}, "was": {}, "we": {}, "were": {}, "what": {}, "when": {}, "where": {},
	"which": {}, "while": {}, "who": {}, "whom": {}, "why": {}, "will": {}, "with": {}, "you": {}, "your": {},
	"yours": {}, "yourself": {}, "yourselves": {},
}

func stemEnglish(w string) string {
	if len(w) <= 2 || strings.Contains(w, "_") {
		return w
	}
	for _, r := range w {
		if unicode.IsDigit(r) {
			return w
		}
	}
	if strings.HasSuffix(w, "ies") && len(w) > 4 {
		return w[:len(w)-3] + "i"
	}
	if strings.HasSuffix(w, "sses") && len(w) > 5 {
		return w[:len(w)-2]
	}
	if strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && !strings.HasSuffix(w, "us") && !strings.HasSuffix(w, "is") && len(w) > 3 {
		w = w[:len(w)-1]
	}
	switch {
	case strings.HasSuffix(w, "ing") && len(w) > 5:
		base := w[:len(w)-3]
		if len(base) >= 2 && base[len(base)-1] == base[len(base)-2] && !isVowel(base[len(base)-1]) {
			base = base[:len(base)-1]
		}
		return base
	case strings.HasSuffix(w, "ed") && len(w) > 4:
		base := w[:len(w)-2]
		if len(base) >= 2 && base[len(base)-1] == base[len(base)-2] && !isVowel(base[len(base)-1]) {
			base = base[:len(base)-1]
		}
		return base
	case strings.HasSuffix(w, "ion") && len(w) > 5 && (w[len(w)-4] == 't' || w[len(w)-4] == 's'):
		return w[:len(w)-3]
	case strings.HasSuffix(w, "ment") && len(w) > 6:
		return w[:len(w)-4]
	case strings.HasSuffix(w, "ness") && len(w) > 6:
		return w[:len(w)-4]
	case strings.HasSuffix(w, "ly") && len(w) > 4:
		return w[:len(w)-2]
	case strings.HasSuffix(w, "y") && len(w) > 3 && !isVowel(w[len(w)-2]):
		return w[:len(w)-1] + "i"
	case strings.HasSuffix(w, "e") && len(w) > 4 && !isVowel(w[len(w)-2]):
		return w[:len(w)-1]
	}
	return w
}

func isVowel(b byte) bool {
	switch b {
	case 'a', 'e', 'i', 'o', 'u':
		return true
	default:
		return false
	}
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
