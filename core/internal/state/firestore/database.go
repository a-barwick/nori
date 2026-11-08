// Package firestore adapts Firestore to Nori's shared persistence rules. Existing
// collection paths and saved field names remain unchanged.
package firestore

import (
	"context"
	"fmt"
	"net"
	"strings"

	native "cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/users"
)

type Config struct {
	Project, Database string
	EmulatorAddress   string
}

func Open(ctx context.Context, cfg Config, user users.User, taskBudgetUSD float64) (*records.Database, error) {
	b, err := openBackend(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return records.New(b, user, taskBudgetUSD), nil
}

func OpenUsers(ctx context.Context, cfg Config) (*records.Users, error) {
	b, err := openBackend(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return records.NewUsers(b), nil
}

func openBackend(ctx context.Context, cfg Config) (*backend, error) {
	var options []option.ClientOption
	if cfg.EmulatorAddress != "" {
		if !IsLoopback(cfg.EmulatorAddress) {
			return nil, fmt.Errorf("Firestore emulator requires loopback")
		}
		options = append(options, option.WithEndpoint(cfg.EmulatorAddress), option.WithoutAuthentication(), option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	}
	db, e := native.NewClientWithDatabase(ctx, cfg.Project, cfg.Database, options...)
	if e != nil {
		return nil, e
	}
	return &backend{db: db}, nil
}
func IsLoopback(address string) bool {
	host, _, e := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	return e == nil && (host == "localhost" || ip != nil && ip.IsLoopback())
}

type backend struct{ db *native.Client }

func (b *backend) DeleteBatch(ctx context.Context, paths []string) error {
	batch := b.db.Batch()
	for _, path := range paths {
		batch.Delete(b.db.Doc(path))
	}
	_, err := batch.Commit(ctx)
	return mapped(err)
}

type transaction struct {
	db *native.Client
	tx *native.Transaction
}

func (b *backend) Close() error { return b.db.Close() }
func mapped(e error) error {
	switch status.Code(e) {
	case codes.NotFound:
		return docstore.ErrNotFound
	case codes.AlreadyExists:
		return docstore.ErrExists
	}
	return e
}
func record(d *native.DocumentSnapshot) docstore.Record {
	return docstore.Record{Path: d.Ref.Path, Fields: d.Data()}
}

// Firestore paths include project/database prefixes. Relative paths are the
// durable keys used by the shared layer and Postgres.
func relative(d *native.DocumentSnapshot) docstore.Record {
	r := record(d)
	if _, path, ok := strings.Cut(r.Path, "/documents/"); ok {
		r.Path = path
	}
	return r
}
func (b *backend) Get(ctx context.Context, path string) (docstore.Record, error) {
	d, e := b.db.Doc(path).Get(ctx)
	if e != nil {
		return docstore.Record{}, mapped(e)
	}
	return relative(d), nil
}
func (t *transaction) Get(ctx context.Context, path string) (docstore.Record, error) {
	d, e := t.tx.Get(t.db.Doc(path))
	if e != nil {
		return docstore.Record{}, mapped(e)
	}
	return relative(d), nil
}
func query(db *native.Client, s docstore.QuerySpec) native.Query {
	q := db.Collection(s.Collection).Query
	for _, f := range s.Filters {
		q = q.Where(f.Field, f.Op, f.Value)
	}
	for _, o := range s.Orders {
		direction := native.Asc
		if o.Direction == docstore.Desc {
			direction = native.Desc
		}
		q = q.OrderBy(o.Field, direction)
	}
	if s.Fields != nil {
		q = q.Select(s.Fields...)
	}
	if s.Count > 0 {
		q = q.Limit(s.Count)
	}
	if s.After != nil {
		cursor := []any{}
		direction := native.Asc
		hasID := false
		for _, o := range s.Orders {
			direction = native.Asc
			if o.Direction == docstore.Desc {
				direction = native.Desc
			}
			if o.Field == docstore.DocumentID {
				hasID = true
				cursor = append(cursor, db.Doc(s.After.Path))
			} else {
				cursor = append(cursor, s.After.Fields[o.Field])
			}
		}
		// A snapshot cursor includes the ID tie-breaker, so equal timestamps
		// cannot cause messages to disappear between pages.
		if !hasID {
			q = q.OrderBy(native.DocumentID, direction)
			cursor = append(cursor, db.Doc(s.After.Path))
		}
		q = q.StartAfter(cursor...)
	}
	return q
}
func rows(db *native.Client, it *native.DocumentIterator) ([]docstore.Record, error) {
	defer it.Stop()
	out := []docstore.Record{}
	for {
		d, e := it.Next()
		if e == iterator.Done {
			return out, nil
		}
		if e != nil {
			return nil, mapped(e)
		}
		out = append(out, relative(d))
	}
}
func (b *backend) Query(ctx context.Context, s docstore.QuerySpec) ([]docstore.Record, error) {
	return rows(b.db, query(b.db, s).Documents(ctx))
}
func (t *transaction) Query(ctx context.Context, s docstore.QuerySpec) ([]docstore.Record, error) {
	return rows(t.db, t.tx.Documents(query(t.db, s)))
}
func updates(changes []docstore.Update) ([]native.Update, error) {
	out := make([]native.Update, 0, len(changes))
	for _, change := range changes {
		var value any
		var e error
		if docstore.IsDelete(change.Value) {
			value = native.Delete
		} else if union, ok := change.Value.(docstore.Union); ok {
			values := []any{}
			for _, v := range union {
				x, err := docstore.Value(v)
				if err != nil {
					return nil, err
				}
				values = append(values, x)
			}
			value = native.ArrayUnion(values...)
		} else {
			value, e = docstore.Value(change.Value)
			if e != nil {
				return nil, e
			}
		}
		out = append(out, native.Update{Path: change.Path, Value: value})
	}
	return out, nil
}
func (b *backend) Apply(ctx context.Context, m docstore.Mutation) error {
	ref := b.db.Doc(m.Path)
	var e error
	switch m.Kind {
	case "set":
		var opts []native.SetOption
		if m.Merge {
			opts = append(opts, native.MergeAll)
		}
		_, e = ref.Set(ctx, m.Fields, opts...)
	case "create":
		_, e = ref.Create(ctx, m.Fields)
	case "delete":
		_, e = ref.Delete(ctx)
	case "update":
		u, err := updates(m.Updates)
		if err != nil {
			return err
		}
		_, e = ref.Update(ctx, u)
	default:
		return fmt.Errorf("unknown document mutation")
	}
	return mapped(e)
}
func (t *transaction) Apply(ctx context.Context, m docstore.Mutation) error {
	ref := t.db.Doc(m.Path)
	var e error
	switch m.Kind {
	case "set":
		var opts []native.SetOption
		if m.Merge {
			opts = append(opts, native.MergeAll)
		}
		e = t.tx.Set(ref, m.Fields, opts...)
	case "create":
		e = t.tx.Create(ref, m.Fields)
	case "delete":
		e = t.tx.Delete(ref)
	case "update":
		u, err := updates(m.Updates)
		if err != nil {
			return err
		}
		e = t.tx.Update(ref, u)
	default:
		return fmt.Errorf("unknown document mutation")
	}
	return mapped(e)
}
func (b *backend) RunTransaction(ctx context.Context, readOnly bool, attempts int, f func(docstore.TransactionBackend) error) error {
	opts := []native.TransactionOption{native.MaxAttempts(attempts)}
	if readOnly {
		opts = append(opts, native.ReadOnly)
	}
	return mapped(b.db.RunTransaction(ctx, func(ctx context.Context, tx *native.Transaction) error { return f(&transaction{db: b.db, tx: tx}) }, opts...))
}
func (b *backend) Watch(ctx context.Context, s docstore.QuerySpec, deliver func([]docstore.Record) error) error {
	snapshots := query(b.db, s).Snapshots(ctx)
	defer snapshots.Stop()
	for {
		snapshot, e := snapshots.Next()
		if e != nil {
			return e
		}
		batch := []docstore.Record{}
		for _, change := range snapshot.Changes {
			if change.Kind == native.DocumentAdded {
				batch = append(batch, relative(change.Doc))
			}
		}
		if len(batch) > 0 {
			if e := deliver(batch); e != nil {
				return e
			}
		}
	}
}
