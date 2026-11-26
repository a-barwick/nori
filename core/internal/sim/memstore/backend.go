package memstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"nori/internal/state/docstore"
)

const defaultTxAttempts = 5

type docKey struct {
	collection string
	id         string
}

type Option func(*Backend)

func WithFaultInjector(inj *FaultInjector) Option {
	return func(b *Backend) {
		b.faults = inj
	}
}

func WithFaults(cfg FaultConfig, rng Float64Source) Option {
	return func(b *Backend) {
		b.faults = NewFaultInjector(cfg, rng)
	}
}

type Backend struct {
	mu             sync.Mutex
	docs           map[string]map[string][]byte
	seqs           map[string]int64
	taskLeases     map[docstore.TaskKey]docstore.TaskLease
	userWakes      map[userWakeKey]docstore.UserWake
	memoryChunks   map[memoryChunkKey]docstore.MemoryChunk
	wakeSubs       map[uint64]chan docstore.WakeEvent
	wakeSubNotify  chan struct{}
	dropWakeEvents bool
	nextWakeSub    uint64
	nowFn          func() time.Time
	version        uint64
	notify         chan struct{}
	faults         *FaultInjector
}

var (
	_ docstore.Backend          = (*Backend)(nil)
	_ docstore.Sequencer        = (*Backend)(nil)
	_ docstore.SequenceReader   = (*Backend)(nil)
	_ docstore.LeaseStore       = (*Backend)(nil)
	_ docstore.ScopedLeaseStore = (*Backend)(nil)
	_ docstore.WakeListener     = (*Backend)(nil)
)

func New(opts ...Option) *Backend {
	b := &Backend{
		docs: map[string]map[string][]byte{
			"system": {
				"control": []byte(`{"paused":false}`),
			},
		},
		seqs:          map[string]int64{},
		taskLeases:    map[docstore.TaskKey]docstore.TaskLease{},
		userWakes:     map[userWakeKey]docstore.UserWake{},
		memoryChunks:  map[memoryChunkKey]docstore.MemoryChunk{},
		wakeSubs:      map[uint64]chan docstore.WakeEvent{},
		wakeSubNotify: make(chan struct{}),
		notify:        make(chan struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	return b
}

func (b *Backend) SetFaults(cfg FaultConfig, rng Float64Source) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.faults == nil {
		b.faults = NewFaultInjector(cfg, rng)
		return
	}
	b.faults.Configure(cfg, rng)
}

func (b *Backend) SetFaultsEnabled(enabled bool) {
	b.mu.Lock()
	inj := b.faults
	b.mu.Unlock()
	if inj != nil {
		inj.SetEnabled(enabled)
	}
}

func (b *Backend) FaultStats() FaultStats {
	b.mu.Lock()
	inj := b.faults
	b.mu.Unlock()
	if inj == nil {
		return FaultStats{}
	}
	return inj.Stats()
}

func (b *Backend) faultInjector() *FaultInjector {
	b.mu.Lock()
	inj := b.faults
	b.mu.Unlock()
	return inj
}

func (b *Backend) Close() error { return nil }

func split(path string) (string, string, error) {
	i := strings.LastIndex(path, "/")
	if i < 1 || i == len(path)-1 {
		return "", "", errors.New("invalid document path")
	}
	return path[:i], path[i+1:], nil
}

func decode(raw []byte) (map[string]any, error) {
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	docstore.NormalizeNumbers(m)
	return m, nil
}

func encodeDoc(fields map[string]any) ([]byte, error) {
	if fields == nil {
		return nil, errors.New("document must be an object")
	}
	raw, err := json.Marshal(docstore.JSONValue(fields))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || raw[0] != '{' {
		return nil, errors.New("document must be an object")
	}
	return raw, nil
}

func (b *Backend) notifyLocked() {
	b.version++
	close(b.notify)
	b.notify = make(chan struct{})
}

func (b *Backend) notifyChan() <-chan struct{} {
	b.mu.Lock()
	ch := b.notify
	b.mu.Unlock()
	return ch
}

func (b *Backend) Get(ctx context.Context, path string) (docstore.Record, error) {
	if err := ctx.Err(); err != nil {
		return docstore.Record{}, err
	}
	if err := b.faultInjector().maybeGetErrFor(path); err != nil {
		return docstore.Record{}, err
	}
	collection, id, err := split(path)
	if err != nil {
		return docstore.Record{}, err
	}
	b.mu.Lock()
	raw, ok := b.docs[collection][id]
	b.mu.Unlock()
	if !ok {
		return docstore.Record{}, docstore.ErrNotFound
	}
	data, err := decode(raw)
	if err != nil {
		return docstore.Record{}, err
	}
	return docstore.Record{Path: path, Fields: data}, nil
}

func (b *Backend) Query(ctx context.Context, s docstore.QuerySpec) ([]docstore.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.faultInjector().maybeQueryErrFor(s.Collection); err != nil {
		return nil, err
	}
	b.mu.Lock()
	colDocs := cloneCollection(b.docs[s.Collection])
	b.mu.Unlock()
	return executeQuery(colDocs, s)
}

func (b *Backend) CurrentSequence(ctx context.Context, path string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := b.faultInjector().maybeGetErrFor(path); err != nil {
		return 0, err
	}
	b.mu.Lock()
	seq := b.seqs[path]
	b.mu.Unlock()
	return seq, nil
}

func (b *Backend) NextSequence(ctx context.Context, path string, count int) (int64, error) {
	if count <= 0 {
		return 0, ctx.Err()
	}
	var start int64
	err := b.RunTransaction(ctx, false, defaultTxAttempts, func(tx docstore.TransactionBackend) error {
		s, err := tx.(docstore.Sequencer).NextSequence(ctx, path, count)
		if err != nil {
			return err
		}
		start = s
		return nil
	})
	return start, err
}

func (b *Backend) Apply(ctx context.Context, m docstore.Mutation) error {
	return b.RunTransaction(ctx, false, defaultTxAttempts, func(tx docstore.TransactionBackend) error {
		return tx.Apply(ctx, m)
	})
}

func (b *Backend) DeleteBatch(ctx context.Context, paths []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	batchKey := sortedPathsKey(paths)
	if err := b.faultInjector().maybeApplyErrFor(batchKey); err != nil {
		return err
	}
	keys := make([]docKey, len(paths))
	for i, path := range paths {
		col, id, err := split(path)
		if err != nil {
			return err
		}
		keys[i] = docKey{collection: col, id: id}
	}
	if len(keys) == 0 {
		return nil
	}
	b.mu.Lock()
	changed := false
	for _, k := range keys {
		if colMap, ok := b.docs[k.collection]; ok {
			if _, exists := colMap[k.id]; exists {
				delete(colMap, k.id)
				if len(colMap) == 0 {
					delete(b.docs, k.collection)
				}
				changed = true
			}
		}
	}
	if changed {
		b.notifyLocked()
	}
	b.mu.Unlock()
	if changed {
		if err := b.faultInjector().maybeCommitThenErrFor(batchKey); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) RunTransaction(ctx context.Context, readOnly bool, attempts int, f func(docstore.TransactionBackend) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if attempts <= 0 {
		attempts = defaultTxAttempts
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		tx := b.beginTx(readOnly)
		err := f(tx)
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrTxConflict) {
				continue
			}
			return err
		}
		if err := b.commitTx(tx); err != nil {
			lastErr = err
			if errors.Is(err, ErrTxConflict) {
				continue
			}
			return err
		}
		return nil
	}
	if errors.Is(lastErr, ErrTxConflict) {
		b.faultInjector().recordTxConflictExhausted()
	}
	return lastErr
}

func (b *Backend) Watch(ctx context.Context, s docstore.QuerySpec, deliver func([]docstore.Record) error) error {
	s.Filters = slices.Clone(s.Filters)
	s.Count = 200
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ch := b.notifyChan()
		rows, err := b.Query(ctx, s)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := deliver(rows); err != nil {
				return err
			}
			last := rows[len(rows)-1].Fields["seq"]
			for i := range s.Filters {
				if s.Filters[i].Field == "seq" && s.Filters[i].Op == ">" {
					s.Filters[i].Value = last
				}
			}
			if len(rows) == s.Count {
				continue
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

type transaction struct {
	b               *Backend
	readOnly        bool
	snapDocs        map[string]map[string][]byte
	snapSeqs        map[string]int64
	snapChunks      map[memoryChunkKey]docstore.MemoryChunk
	readDocs        map[docKey]bool
	readCollections map[string]bool
	writes          map[docKey][]byte
	seqDeltas       map[string]int64
	wakeOps         []stagedWakeOp
	chunkWrites     map[memoryChunkKey]*docstore.MemoryChunk
}

var (
	_ docstore.TransactionBackend = (*transaction)(nil)
	_ docstore.Sequencer          = (*transaction)(nil)
	_ docstore.SequenceReader     = (*transaction)(nil)
	_ docstore.WakeWriter         = (*transaction)(nil)
)

func (b *Backend) beginTx(readOnly bool) *transaction {
	b.mu.Lock()
	defer b.mu.Unlock()
	snapDocs := make(map[string]map[string][]byte, len(b.docs))
	for col, docs := range b.docs {
		snapDocs[col] = cloneCollection(docs)
	}
	snapSeqs := make(map[string]int64, len(b.seqs))
	for path, seq := range b.seqs {
		snapSeqs[path] = seq
	}
	snapChunks := make(map[memoryChunkKey]docstore.MemoryChunk, len(b.memoryChunks))
	for k, c := range b.memoryChunks {
		snapChunks[k] = c
	}
	return &transaction{
		b:               b,
		readOnly:        readOnly,
		snapDocs:        snapDocs,
		snapSeqs:        snapSeqs,
		snapChunks:      snapChunks,
		readDocs:        map[docKey]bool{},
		readCollections: map[string]bool{},
		writes:          map[docKey][]byte{},
		seqDeltas:       map[string]int64{},
		chunkWrites:     map[memoryChunkKey]*docstore.MemoryChunk{},
	}
}

func (b *Backend) commitTx(tx *transaction) error {
	inj := b.faultInjector()
	mutated := len(tx.writes) > 0 || len(tx.seqDeltas) > 0 || len(tx.wakeOps) > 0 || len(tx.chunkWrites) > 0

	b.mu.Lock()
	for k := range tx.readDocs {
		snapVal, snapOK := lookupRaw(tx.snapDocs, k)
		currVal, currOK := lookupRaw(b.docs, k)
		if snapOK != currOK || !bytes.Equal(snapVal, currVal) {
			b.mu.Unlock()
			return ErrTxConflict
		}
	}
	for col := range tx.readCollections {
		if !collectionEqual(tx.snapDocs[col], b.docs[col]) {
			b.mu.Unlock()
			return ErrTxConflict
		}
	}
	for k := range tx.writes {
		snapVal, snapOK := lookupRaw(tx.snapDocs, k)
		currVal, currOK := lookupRaw(b.docs, k)
		if snapOK != currOK || !bytes.Equal(snapVal, currVal) {
			b.mu.Unlock()
			return ErrTxConflict
		}
	}
	for path := range tx.seqDeltas {
		if b.seqs[path] != tx.snapSeqs[path] {
			b.mu.Unlock()
			return ErrTxConflict
		}
	}
	for k := range tx.chunkWrites {
		snapChunk, snapOK := tx.snapChunks[k]
		currChunk, currOK := b.memoryChunks[k]
		if snapOK != currOK || (snapOK && !memoryChunkEqual(snapChunk, currChunk)) {
			b.mu.Unlock()
			return ErrTxConflict
		}
	}

	txKey := txFaultKey(tx)
	if err := inj.maybeTxConflictFor(txKey); err != nil {
		b.mu.Unlock()
		return err
	}

	for k, raw := range tx.writes {
		if raw == nil {
			if colMap, ok := b.docs[k.collection]; ok {
				delete(colMap, k.id)
				if len(colMap) == 0 {
					delete(b.docs, k.collection)
				}
			}
			continue
		}
		colMap, ok := b.docs[k.collection]
		if !ok {
			colMap = map[string][]byte{}
			b.docs[k.collection] = colMap
		}
		colMap[k.id] = raw
	}
	for path, delta := range tx.seqDeltas {
		b.seqs[path] += delta
	}
	for k, chunk := range tx.chunkWrites {
		if chunk == nil {
			delete(b.memoryChunks, k)
		} else {
			b.memoryChunks[k] = *chunk
		}
	}
	var wakeEvents []docstore.WakeEvent
	if len(tx.wakeOps) > 0 {
		for _, op := range tx.wakeOps {
			wakeEvents = append(wakeEvents, b.applyWakeOpLocked(op)...)
		}
	}
	if mutated {
		b.notifyLocked()
	}
	b.mu.Unlock()

	if len(wakeEvents) > 0 {
		b.emitWakeEvents(wakeEvents)
	}

	if mutated {
		if err := inj.maybeCommitThenErrFor(txKey); err != nil {
			return err
		}
	}
	return nil
}

func (t *transaction) getInternal(path string) (docstore.Record, error) {
	col, id, err := split(path)
	if err != nil {
		return docstore.Record{}, err
	}
	k := docKey{collection: col, id: id}
	if raw, written := t.writes[k]; written {
		if raw == nil {
			return docstore.Record{}, docstore.ErrNotFound
		}
		data, err := decode(raw)
		if err != nil {
			return docstore.Record{}, err
		}
		return docstore.Record{Path: path, Fields: data}, nil
	}
	t.readDocs[k] = true
	raw, ok := lookupRaw(t.snapDocs, k)
	if !ok {
		return docstore.Record{}, docstore.ErrNotFound
	}
	data, err := decode(raw)
	if err != nil {
		return docstore.Record{}, err
	}
	return docstore.Record{Path: path, Fields: data}, nil
}

func (t *transaction) Get(ctx context.Context, path string) (docstore.Record, error) {
	if err := ctx.Err(); err != nil {
		return docstore.Record{}, err
	}
	if err := t.b.faultInjector().maybeGetErrFor("tx:" + path); err != nil {
		return docstore.Record{}, err
	}
	return t.getInternal(path)
}

func (t *transaction) Query(ctx context.Context, s docstore.QuerySpec) ([]docstore.Record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := t.b.faultInjector().maybeQueryErrFor("tx:" + s.Collection); err != nil {
		return nil, err
	}
	t.readCollections[s.Collection] = true
	view := cloneCollection(t.snapDocs[s.Collection])
	for k, raw := range t.writes {
		if k.collection != s.Collection {
			continue
		}
		if raw == nil {
			delete(view, k.id)
		} else {
			view[k.id] = raw
		}
	}
	return executeQuery(view, s)
}

func (t *transaction) CurrentSequence(ctx context.Context, path string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := t.b.faultInjector().maybeGetErrFor("txseq:" + path); err != nil {
		return 0, err
	}
	return t.snapSeqs[path] + t.seqDeltas[path], nil
}

func (t *transaction) NextSequence(ctx context.Context, path string, count int) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if t.readOnly {
		return 0, ErrReadOnly
	}
	if count <= 0 {
		return 0, nil
	}
	if err := t.b.faultInjector().maybeApplyErrFor("seq:" + path); err != nil {
		return 0, err
	}
	cur := t.snapSeqs[path] + t.seqDeltas[path]
	t.seqDeltas[path] += int64(count)
	return cur + 1, nil
}

func (t *transaction) Apply(ctx context.Context, m docstore.Mutation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.readOnly {
		return ErrReadOnly
	}
	if err := t.b.faultInjector().maybeApplyErrFor(m.Path); err != nil {
		return err
	}
	col, id, err := split(m.Path)
	if err != nil {
		return err
	}
	k := docKey{collection: col, id: id}
	if m.Kind == "delete" {
		t.writes[k] = nil
		return nil
	}
	fields := m.Fields
	if m.Kind == "update" || m.Merge {
		old, err := t.getInternal(m.Path)
		if errors.Is(err, docstore.ErrNotFound) && m.Kind == "set" {
			old.Fields = map[string]any{}
		} else if err != nil {
			return err
		}
		fields = old.Fields
		if m.Merge {
			merge(fields, m.Fields)
		}
		for _, change := range m.Updates {
			parts := strings.Split(change.Path, ".")
			parent := fields
			for _, key := range parts[:len(parts)-1] {
				child, ok := parent[key].(map[string]any)
				if !ok {
					child = map[string]any{}
					parent[key] = child
				}
				parent = child
			}
			key := parts[len(parts)-1]
			if docstore.IsDelete(change.Value) {
				delete(parent, key)
				continue
			}
			if values, ok := change.Value.(docstore.Union); ok {
				prior, _ := parent[key].([]any)
				for _, value := range values {
					encoded, err := docstore.Value(value)
					if err != nil {
						return err
					}
					encoded = docstore.JSONValue(encoded)
					raw, err := json.Marshal(encoded)
					if err != nil {
						return err
					}
					found := false
					for _, oldElem := range prior {
						previous, _ := json.Marshal(docstore.JSONValue(oldElem))
						if bytes.Equal(raw, previous) {
							found = true
							break
						}
					}
					if !found {
						prior = append(prior, encoded)
					}
				}
				parent[key] = prior
				continue
			}
			value, err := docstore.Value(change.Value)
			if err != nil {
				return err
			}
			parent[key] = value
		}
	}
	if m.Kind != "create" && m.Kind != "set" && m.Kind != "update" {
		return errors.New("unknown document mutation")
	}
	if m.Kind == "create" {
		if raw, written := t.writes[k]; written {
			if raw != nil {
				return docstore.ErrExists
			}
		} else {
			t.readDocs[k] = true
			if _, exists := lookupRaw(t.snapDocs, k); exists {
				return docstore.ErrExists
			}
		}
	}
	raw, err := encodeDoc(fields)
	if err != nil {
		return err
	}
	t.writes[k] = raw
	return nil
}

func merge(dst, src map[string]any) {
	for k, v := range src {
		if nested, ok := v.(map[string]any); ok && len(nested) > 0 {
			existing, _ := dst[k].(map[string]any)
			if existing == nil {
				existing = map[string]any{}
			}
			merge(existing, nested)
			dst[k] = existing
		} else {
			dst[k] = v
		}
	}
}

func cloneCollection(src map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(src))
	for id, raw := range src {
		out[id] = raw
	}
	return out
}

func lookupRaw(docs map[string]map[string][]byte, k docKey) ([]byte, bool) {
	col, ok := docs[k.collection]
	if !ok {
		return nil, false
	}
	raw, ok := col[k.id]
	return raw, ok
}

func collectionEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for id, ra := range a {
		rb, ok := b[id]
		if !ok || !bytes.Equal(ra, rb) {
			return false
		}
	}
	return true
}

func sortedPathsKey(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	cp := slices.Clone(paths)
	slices.Sort(cp)
	return strings.Join(cp, ",")
}

func txFaultKey(tx *transaction) string {
	keys := make([]string, 0, len(tx.writes)+len(tx.seqDeltas)+len(tx.wakeOps)+len(tx.chunkWrites))
	for k := range tx.writes {
		keys = append(keys, k.collection+"/"+k.id)
	}
	for path := range tx.seqDeltas {
		keys = append(keys, "seq:"+path)
	}
	for k := range tx.chunkWrites {
		keys = append(keys, "memchunk:"+k.owner+":"+k.chunkID)
	}
	for _, op := range tx.wakeOps {
		switch op.kind {
		case wakeOpTask:
			keys = append(keys, "wake:t:"+op.userID+":"+op.runID)
		case wakeOpWaitingTasks:
			keys = append(keys, "wake:wait:"+op.userID+":"+op.reason)
		case wakeOpUser:
			keys = append(keys, "wake:u:"+op.userID+":"+op.lane)
		case wakeOpPause:
			keys = append(keys, "wake:pause")
		}
	}
	if len(keys) == 0 {
		for k := range tx.readDocs {
			keys = append(keys, "r:"+k.collection+"/"+k.id)
		}
		for col := range tx.readCollections {
			keys = append(keys, "q:"+col)
		}
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}
