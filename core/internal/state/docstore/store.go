// Package docstore is the small transactional document surface used by Nori's
// persistence rules. Firestore and Postgres implement the transport; rules live
// once in state/records. It is not a general-purpose database API.
package docstore

import (
	"context"
	"errors"
	"strings"
)

var ErrNotFound = errors.New("document not found")
var ErrExists = errors.New("document already exists")
var Done = errors.New("end of documents")

const DocumentID = "__name__"

type Direction int

const (
	Asc  Direction = 1
	Desc Direction = -1
)

type Filter struct {
	Field, Op string
	Value     any
}
type Order struct {
	Field     string
	Direction Direction
}
type QuerySpec struct {
	Collection string
	Filters    []Filter
	Orders     []Order
	Fields     []string
	Count      int
	After      *Record
}
type Record struct {
	Path   string
	Fields map[string]any
}
type Update struct {
	Path  string
	Value any
}
type Mutation struct {
	Kind, Path string
	Fields     map[string]any
	Updates    []Update
	Merge      bool
}
type Union []any

func ArrayUnion(values ...any) Union { return values }

type deletion struct{}

var Delete = deletion{}

func IsDelete(v any) bool { _, ok := v.(deletion); return ok }

type SetOption bool

const MergeAll SetOption = true

type Reader interface {
	Get(context.Context, string) (Record, error)
	Query(context.Context, QuerySpec) ([]Record, error)
}
type Writer interface {
	Apply(context.Context, Mutation) error
}
type TransactionBackend interface {
	Reader
	Writer
}

// Sequencer allocates monotonically increasing per-head sequence numbers inside
// a transaction without serializing all transactions on a single document read.
type Sequencer interface {
	NextSequence(ctx context.Context, path string, count int) (start int64, err error)
}

// SequenceReader reads the current sequence head without requiring a document row.
type SequenceReader interface {
	CurrentSequence(ctx context.Context, path string) (int64, error)
}
type Backend interface {
	Reader
	Writer
	RunTransaction(context.Context, bool, int, func(TransactionBackend) error) error
	Watch(context.Context, QuerySpec, func([]Record) error) error
	DeleteBatch(context.Context, []string) error
	Close() error
}
type Client struct{ Backend }

func New(b Backend) *Client { return &Client{b} }
func (c *Client) Collection(path string) *CollectionRef {
	return &CollectionRef{Query: Query{client: c, spec: QuerySpec{Collection: path}}, Path: path}
}
func (c *Client) GetAll(ctx context.Context, refs []*DocumentRef) ([]*DocumentSnapshot, error) {
	out := make([]*DocumentSnapshot, 0, len(refs))
	for _, ref := range refs {
		s, e := ref.Get(ctx)
		if errors.Is(e, ErrNotFound) {
			out = append(out, &DocumentSnapshot{Ref: ref})
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, nil
}

type TransactionOption struct {
	ReadOnly bool
	Attempts int
}

var ReadOnly = TransactionOption{ReadOnly: true}

func MaxAttempts(n int) TransactionOption { return TransactionOption{Attempts: n} }
func (c *Client) RunTransaction(ctx context.Context, f func(context.Context, *Transaction) error, opts ...TransactionOption) error {
	// Leave Attempts=0 when unspecified so each backend chooses its own default
	// (5 on memstore for deterministic fault injection, 15 with jitter on Postgres).
	o := TransactionOption{}
	for _, v := range opts {
		if v.ReadOnly {
			o.ReadOnly = true
		}
		if v.Attempts > 0 {
			o.Attempts = v.Attempts
		}
	}
	return c.Backend.RunTransaction(ctx, o.ReadOnly, o.Attempts, func(b TransactionBackend) error { return f(ctx, &Transaction{ctx: ctx, client: c, backend: b}) })
}

type CollectionRef struct {
	Query
	Path string
}

func (c *CollectionRef) Doc(id string) *DocumentRef {
	return &DocumentRef{client: c.client, Path: c.Path + "/" + id, ID: id}
}

type DocumentRef struct {
	client   *Client
	Path, ID string
}

func (d *DocumentRef) Collection(name string) *CollectionRef {
	return d.client.Collection(d.Path + "/" + name)
}
func (d *DocumentRef) Get(ctx context.Context) (*DocumentSnapshot, error) {
	r, e := d.client.Backend.Get(ctx, d.Path)
	if e != nil {
		return nil, e
	}
	return snapshot(d.client, r), nil
}
func (d *DocumentRef) Set(ctx context.Context, v any, opts ...SetOption) (struct{}, error) {
	m, e := mutation("set", d, v, opts)
	if e == nil {
		e = d.client.Apply(ctx, m)
	}
	return struct{}{}, e
}
func (d *DocumentRef) Create(ctx context.Context, v any) (struct{}, error) {
	m, e := mutation("create", d, v, nil)
	if e == nil {
		e = d.client.Apply(ctx, m)
	}
	return struct{}{}, e
}
func (d *DocumentRef) Update(ctx context.Context, u []Update) (struct{}, error) {
	return struct{}{}, d.client.Apply(ctx, Mutation{Kind: "update", Path: d.Path, Updates: u})
}
func (d *DocumentRef) Delete(ctx context.Context) (struct{}, error) {
	return struct{}{}, d.client.Apply(ctx, Mutation{Kind: "delete", Path: d.Path})
}
func mutation(kind string, d *DocumentRef, v any, opts []SetOption) (Mutation, error) {
	m, e := Encode(v)
	return Mutation{Kind: kind, Path: d.Path, Fields: m, Merge: len(opts) > 0 && opts[0] == MergeAll}, e
}

type DocumentSnapshot struct {
	Ref    *DocumentRef
	fields map[string]any
}

func snapshot(c *Client, r Record) *DocumentSnapshot {
	return &DocumentSnapshot{Ref: &DocumentRef{client: c, Path: r.Path, ID: r.Path[strings.LastIndex(r.Path, "/")+1:]}, fields: r.Fields}
}
func (d *DocumentSnapshot) Data() map[string]any { return d.fields }
func (d *DocumentSnapshot) DataTo(v any) error   { return Decode(d.fields, v) }
func (d *DocumentSnapshot) Exists() bool         { return d.fields != nil }

type Query struct {
	client *Client
	spec   QuerySpec
}

func (q Query) Where(f, op string, v any) Query {
	q.spec.Filters = append(append([]Filter{}, q.spec.Filters...), Filter{f, op, v})
	return q
}
func (q Query) OrderBy(f string, d Direction) Query {
	q.spec.Orders = append(append([]Order{}, q.spec.Orders...), Order{f, d})
	return q
}
func (q Query) Select(fields ...string) Query {
	q.spec.Fields = append([]string{}, fields...)
	return q
}
func (q Query) Limit(n int) Query { q.spec.Count = n; return q }
func (q Query) StartAfter(d *DocumentSnapshot) Query {
	q.spec.After = &Record{Path: d.Ref.Path, Fields: d.fields}
	return q
}
func (q Query) Documents(ctx context.Context) *DocumentIterator {
	return &DocumentIterator{ctx: ctx, client: q.client, reader: q.client.Backend, query: q.spec}
}
func (q Query) Watch(ctx context.Context, deliver func([]*DocumentSnapshot) error) error {
	return q.client.Backend.Watch(ctx, q.spec, func(rows []Record) error {
		docs := make([]*DocumentSnapshot, 0, len(rows))
		for _, r := range rows {
			docs = append(docs, snapshot(q.client, r))
		}
		return deliver(docs)
	})
}

type DocumentIterator struct {
	ctx    context.Context
	client *Client
	reader Reader
	query  QuerySpec
	rows   []Record
	err    error
	loaded bool
	at     int
}

func (i *DocumentIterator) Stop() {}
func (i *DocumentIterator) Next() (*DocumentSnapshot, error) {
	if !i.loaded {
		i.rows, i.err = i.reader.Query(i.ctx, i.query)
		i.loaded = true
	}
	if i.err != nil {
		return nil, i.err
	}
	if i.at >= len(i.rows) {
		return nil, Done
	}
	r := i.rows[i.at]
	i.at++
	return snapshot(i.client, r), nil
}
func (i *DocumentIterator) GetAll() ([]*DocumentSnapshot, error) {
	out := []*DocumentSnapshot{}
	for {
		d, e := i.Next()
		if errors.Is(e, Done) {
			return out, nil
		}
		if e != nil {
			return nil, e
		}
		out = append(out, d)
	}
}

type Transaction struct {
	ctx     context.Context
	client  *Client
	backend TransactionBackend
}

func (t *Transaction) Context() context.Context { return t.ctx }
func (t *Transaction) HasSequencer() bool {
	_, ok := t.backend.(Sequencer)
	return ok
}
func (t *Transaction) Get(d *DocumentRef) (*DocumentSnapshot, error) {
	r, e := t.backend.Get(t.ctx, d.Path)
	if e != nil {
		return nil, e
	}
	return snapshot(t.client, r), nil
}
func (t *Transaction) Documents(q Query) *DocumentIterator {
	return &DocumentIterator{ctx: t.ctx, client: t.client, reader: t.backend, query: q.spec}
}
func (t *Transaction) Set(d *DocumentRef, v any, opts ...SetOption) error {
	m, e := mutation("set", d, v, opts)
	if e != nil {
		return e
	}
	return t.backend.Apply(t.ctx, m)
}
func (t *Transaction) Create(d *DocumentRef, v any) error {
	m, e := mutation("create", d, v, nil)
	if e != nil {
		return e
	}
	return t.backend.Apply(t.ctx, m)
}
func (t *Transaction) Update(d *DocumentRef, u []Update) error {
	return t.backend.Apply(t.ctx, Mutation{Kind: "update", Path: d.Path, Updates: u})
}
func (t *Transaction) Delete(d *DocumentRef) error {
	return t.backend.Apply(t.ctx, Mutation{Kind: "delete", Path: d.Path})
}

// NextSequence returns the first of count consecutive 1-based sequence numbers
// when the transaction backend implements Sequencer.
func (t *Transaction) NextSequence(d *DocumentRef, count int) (int64, bool, error) {
	seq, ok := t.backend.(Sequencer)
	if !ok {
		return 0, false, nil
	}
	start, err := seq.NextSequence(t.ctx, d.Path, count)
	return start, true, err
}

// CurrentSequence returns the latest allocated sequence number when the backend
// implements SequenceReader.
func (c *Client) CurrentSequence(ctx context.Context, d *DocumentRef) (int64, bool, error) {
	seq, ok := c.Backend.(SequenceReader)
	if !ok {
		return 0, false, nil
	}
	head, err := seq.CurrentSequence(ctx, d.Path)
	return head, true, err
}
