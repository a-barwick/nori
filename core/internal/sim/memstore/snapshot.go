package memstore

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
	"math"
	"slices"
	"time"

	"nori/internal/billing"
	"nori/internal/state/docstore"
)

type BillingSnapshot struct {
	Entitlements []billing.Entitlement
	Grants       []billing.CreditGrant
	GlobalBudget []billing.GlobalBudgetUsage
}

type BackendSnapshot struct {
	Collections  map[string]map[string]map[string]any
	Sequences    map[string]int64
	TaskLeases   []docstore.TaskLease
	UserWakes    []docstore.UserWake
	MemoryChunks []docstore.MemoryChunk
	Billing      BillingSnapshot
}

type ArtifactsSnapshot struct {
	PNGs  map[string][]byte
	Files map[string]StoredFile
}

func (b *Backend) Snapshot() BackendSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	cols := make(map[string]map[string]map[string]any, len(b.docs))
	for col, docs := range b.docs {
		if len(docs) == 0 {
			continue
		}
		colCopy := make(map[string]map[string]any, len(docs))
		for id, raw := range docs {
			fields, err := decode(raw)
			if err != nil {
				panic("memstore: corrupt stored document: " + err.Error())
			}
			colCopy[id] = fields
		}
		cols[col] = colCopy
	}

	seqs := make(map[string]int64, len(b.seqs))
	for path, seq := range b.seqs {
		seqs[path] = seq
	}

	taskLeases := make([]docstore.TaskLease, 0, len(b.taskLeases))
	for _, row := range b.taskLeases {
		taskLeases = append(taskLeases, cloneTaskLease(row))
	}
	slices.SortFunc(taskLeases, compareTaskLeaseKeyOrder)

	userWakes := make([]docstore.UserWake, 0, len(b.userWakes))
	for _, row := range b.userWakes {
		userWakes = append(userWakes, cloneUserWake(row))
	}
	slices.SortFunc(userWakes, compareUserWakeKeyOrder)

	memoryChunks := make([]docstore.MemoryChunk, 0, len(b.memoryChunks))
	for _, chunk := range b.memoryChunks {
		memoryChunks = append(memoryChunks, cloneMemoryChunk(chunk))
	}
	slices.SortFunc(memoryChunks, compareMemoryChunkKeyOrder)

	entitlements := make([]billing.Entitlement, 0, len(b.billing.entitlements))
	for _, ent := range b.billing.entitlements {
		entitlements = append(entitlements, ent.Clone())
	}
	slices.SortFunc(entitlements, func(a, b billing.Entitlement) int {
		if a.UserID < b.UserID {
			return -1
		}
		if a.UserID > b.UserID {
			return 1
		}
		return 0
	})
	grants := make([]billing.CreditGrant, 0, len(b.billing.grants))
	for _, g := range b.billing.grants {
		grants = append(grants, g.Clone())
	}
	slices.SortFunc(grants, func(a, b billing.CreditGrant) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	budgets := make([]billing.GlobalBudgetUsage, 0, len(b.billing.globalBudget))
	for _, row := range b.billing.globalBudget {
		budgets = append(budgets, row)
	}
	slices.SortFunc(budgets, func(a, b billing.GlobalBudgetUsage) int {
		if a.Day != b.Day {
			if a.Day < b.Day {
				return -1
			}
			return 1
		}
		if a.Scope < b.Scope {
			return -1
		}
		if a.Scope > b.Scope {
			return 1
		}
		return 0
	})

	return BackendSnapshot{
		Collections:  cols,
		Sequences:    seqs,
		TaskLeases:   taskLeases,
		UserWakes:    userWakes,
		MemoryChunks: memoryChunks,
		Billing: BillingSnapshot{
			Entitlements: entitlements,
			Grants:       grants,
			GlobalBudget: budgets,
		},
	}
}

func (b *Backend) Restore(snap BackendSnapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()

	docs := make(map[string]map[string][]byte, len(snap.Collections))
	for col, colDocs := range snap.Collections {
		if len(colDocs) == 0 {
			continue
		}
		colCopy := make(map[string][]byte, len(colDocs))
		for id, fields := range colDocs {
			raw, err := encodeDoc(fields)
			if err != nil {
				panic("memstore: restore invalid document: " + err.Error())
			}
			colCopy[id] = raw
		}
		docs[col] = colCopy
	}

	seqs := make(map[string]int64, len(snap.Sequences))
	for path, seq := range snap.Sequences {
		seqs[path] = seq
	}

	taskLeases := make(map[docstore.TaskKey]docstore.TaskLease, len(snap.TaskLeases))
	for _, row := range snap.TaskLeases {
		cp := cloneTaskLease(row)
		taskLeases[docstore.TaskKey{UserID: cp.UserID, RunID: cp.RunID}] = cp
	}

	userWakes := make(map[userWakeKey]docstore.UserWake, len(snap.UserWakes))
	for _, row := range snap.UserWakes {
		cp := cloneUserWake(row)
		userWakes[userWakeKey{userID: cp.UserID, lane: cp.Lane}] = cp
	}

	memoryChunks := make(map[memoryChunkKey]docstore.MemoryChunk, len(snap.MemoryChunks))
	for _, chunk := range snap.MemoryChunks {
		cp := cloneMemoryChunk(chunk)
		memoryChunks[memoryChunkKey{owner: cp.Owner, chunkID: cp.ChunkID}] = cp
	}

	bs := newBillingState()
	bs.trialMaxDay = b.billing.trialMaxDay
	for _, ent := range snap.Billing.Entitlements {
		cp := ent.Clone()
		bs.entitlements[cp.UserID] = cp
		if cp.AppAccountToken != "" {
			bs.byAppToken[cp.AppAccountToken] = cp.UserID
		}
	}
	for _, g := range snap.Billing.Grants {
		cp := g.Clone()
		bs.grants[cp.ID] = cp
	}
	for _, row := range snap.Billing.GlobalBudget {
		bs.globalBudget[billingBudgetKey{day: row.Day, scope: row.Scope}] = row
	}

	b.docs = docs
	b.seqs = seqs
	b.taskLeases = taskLeases
	b.userWakes = userWakes
	b.memoryChunks = memoryChunks
	b.billing = bs
	b.notifyLocked()
}

func (b *Backend) Digest() string {
	return b.Snapshot().Digest()
}

func (s BackendSnapshot) Digest() string {
	h := sha256.New()
	writeTag(h, "memstore.backend.v2")

	colNames := make([]string, 0, len(s.Collections))
	for col, docs := range s.Collections {
		if len(docs) > 0 {
			colNames = append(colNames, col)
		}
	}
	slices.Sort(colNames)
	writeTag(h, "collections")
	writeUint64(h, uint64(len(colNames)))
	for _, col := range colNames {
		writeString(h, col)
		docs := s.Collections[col]
		ids := make([]string, 0, len(docs))
		for id := range docs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		writeUint64(h, uint64(len(ids)))
		for _, id := range ids {
			writeString(h, id)
			writeCanonicalValue(h, docs[id])
		}
	}

	seqPaths := make([]string, 0, len(s.Sequences))
	for path := range s.Sequences {
		seqPaths = append(seqPaths, path)
	}
	slices.Sort(seqPaths)
	writeTag(h, "sequences")
	writeUint64(h, uint64(len(seqPaths)))
	for _, path := range seqPaths {
		writeString(h, path)
		writeInt64(h, s.Sequences[path])
	}

	taskLeases := slices.Clone(s.TaskLeases)
	slices.SortFunc(taskLeases, compareTaskLeaseKeyOrder)
	writeTag(h, "task_leases")
	writeUint64(h, uint64(len(taskLeases)))
	for _, row := range taskLeases {
		writeString(h, row.UserID)
		writeString(h, row.RunID)
		writeBool(h, row.Runnable)
		writeString(h, row.WaitingReason)
		writeString(h, row.Owner)
		writeInt64(h, row.WakeSeq)
		writeTimePtr(h, row.LeaseUntil)
		writeTime(h, row.WakeAt)
		writeTime(h, row.UpdatedAt)
	}

	userWakes := slices.Clone(s.UserWakes)
	slices.SortFunc(userWakes, compareUserWakeKeyOrder)
	writeTag(h, "user_wakes")
	writeUint64(h, uint64(len(userWakes)))
	for _, row := range userWakes {
		writeString(h, row.UserID)
		writeString(h, row.Lane)
		writeBool(h, row.Runnable)
		writeInt64(h, row.WakeSeq)
		writeTime(h, row.WakeAt)
		writeString(h, row.Owner)
		writeTimePtr(h, row.LeaseUntil)
		writeTime(h, row.UpdatedAt)
	}

	memoryChunks := slices.Clone(s.MemoryChunks)
	slices.SortFunc(memoryChunks, compareMemoryChunkKeyOrder)
	writeTag(h, "memory_chunks")
	writeUint64(h, uint64(len(memoryChunks)))
	for _, chunk := range memoryChunks {
		writeString(h, chunk.Owner)
		writeString(h, chunk.ChunkID)
		writeString(h, chunk.Layer)
		writeString(h, chunk.LogDate)
		writeString(h, chunk.Section)
		writeString(h, chunk.About)
		writeString(h, chunk.Body)
		writeString(h, chunk.Provenance)
		writeString(h, chunk.SourceInput)
		writeString(h, chunk.RunID)
		writeBool(h, chunk.Pinned)
		writeTime(h, chunk.CreatedAt)
		writeTime(h, chunk.UpdatedAt)
	}

	return hex.EncodeToString(h.Sum(nil))
}

func (a *Artifacts) Snapshot() ArtifactsSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()

	pngs := make(map[string][]byte, len(a.pngs))
	for k, data := range a.pngs {
		pngs[k] = cloneBytes(data)
	}
	files := make(map[string]StoredFile, len(a.files))
	for k, f := range a.files {
		files[k] = StoredFile{
			MediaType: f.MediaType,
			Data:      cloneBytes(f.Data),
		}
	}
	return ArtifactsSnapshot{
		PNGs:  pngs,
		Files: files,
	}
}

func (a *Artifacts) Digest() string {
	return a.Snapshot().Digest()
}

func (s ArtifactsSnapshot) Digest() string {
	h := sha256.New()
	writeTag(h, "memstore.artifacts.v1")

	pngKeys := make([]string, 0, len(s.PNGs))
	for k := range s.PNGs {
		pngKeys = append(pngKeys, k)
	}
	slices.Sort(pngKeys)
	writeTag(h, "pngs")
	writeUint64(h, uint64(len(pngKeys)))
	for _, k := range pngKeys {
		writeString(h, k)
		writeBytes(h, s.PNGs[k])
	}

	fileKeys := make([]string, 0, len(s.Files))
	for k := range s.Files {
		fileKeys = append(fileKeys, k)
	}
	slices.Sort(fileKeys)
	writeTag(h, "files")
	writeUint64(h, uint64(len(fileKeys)))
	for _, k := range fileKeys {
		f := s.Files[k]
		writeString(h, k)
		writeString(h, f.MediaType)
		writeBytes(h, f.Data)
	}

	return hex.EncodeToString(h.Sum(nil))
}

func CombinedDigest(b *Backend, a *Artifacts) string {
	h := sha256.New()
	writeTag(h, "memstore.combined.v1")
	if b != nil {
		writeString(h, b.Digest())
	} else {
		writeString(h, "")
	}
	if a != nil {
		writeString(h, a.Digest())
	} else {
		writeString(h, "")
	}
	return hex.EncodeToString(h.Sum(nil))
}

func writeTag(h hash.Hash, tag string) {
	writeString(h, tag)
}

func writeUint64(h hash.Hash, n uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], n)
	_, _ = h.Write(buf[:])
}

func writeInt64(h hash.Hash, n int64) {
	writeUint64(h, uint64(n))
}

func writeString(h hash.Hash, s string) {
	writeUint64(h, uint64(len(s)))
	_, _ = h.Write([]byte(s))
}

func writeBytes(h hash.Hash, b []byte) {
	writeUint64(h, uint64(len(b)))
	_, _ = h.Write(b)
}

func writeBool(h hash.Hash, v bool) {
	if v {
		_, _ = h.Write([]byte{1})
	} else {
		_, _ = h.Write([]byte{0})
	}
}

func writeTime(h hash.Hash, t time.Time) {
	if t.IsZero() {
		_, _ = h.Write([]byte{0})
		return
	}
	u := t.UTC()
	_, _ = h.Write([]byte{1})
	writeInt64(h, u.Unix())
	writeInt64(h, int64(u.Nanosecond()))
}

func writeTimePtr(h hash.Hash, t *time.Time) {
	if t == nil {
		_, _ = h.Write([]byte{0})
		return
	}
	_, _ = h.Write([]byte{1})
	writeTime(h, *t)
}

func writeCanonicalValue(h hash.Hash, v any) {
	switch x := v.(type) {
	case nil:
		_, _ = h.Write([]byte{'N'})
	case bool:
		if x {
			_, _ = h.Write([]byte{'B', 1})
		} else {
			_, _ = h.Write([]byte{'B', 0})
		}
	case int64:
		_, _ = h.Write([]byte{'I'})
		writeInt64(h, x)
	case float64:
		if x == 0 {
			x = 0
		}
		_, _ = h.Write([]byte{'F'})
		writeUint64(h, math.Float64bits(x))
	case json.Number:
		if n, err := x.Int64(); err == nil {
			writeCanonicalValue(h, n)
			return
		}
		f, _ := x.Float64()
		writeCanonicalValue(h, f)
	case string:
		_, _ = h.Write([]byte{'S'})
		writeString(h, x)
	case []any:
		_, _ = h.Write([]byte{'A'})
		writeUint64(h, uint64(len(x)))
		for _, elem := range x {
			writeCanonicalValue(h, elem)
		}
	case map[string]any:
		_, _ = h.Write([]byte{'O'})
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		writeUint64(h, uint64(len(keys)))
		for _, k := range keys {
			writeString(h, k)
			writeCanonicalValue(h, x[k])
		}
	default:
		raw, _ := json.Marshal(x)
		_, _ = h.Write([]byte{'J'})
		writeBytes(h, raw)
	}
}
