// Package postgres stores Nori records as indexed JSONB documents. The shared
// state/records layer owns every state transition; serializable transactions
// preserve its read/check/write fences and event-head updates.
package postgres

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Config struct {
	URL           string
	TaskBudgetUSD float64
}
type Database = records.Database
type Users = records.Users

func Open(ctx context.Context, cfg Config) (*Database, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("NORI_POSTGRES_URL must be configured")
	}
	pool, e := pgxpool.New(ctx, cfg.URL)
	if e != nil {
		return nil, errors.New("invalid NORI_POSTGRES_URL")
	}
	if e = pool.Ping(ctx); e != nil {
		pool.Close()
		return nil, errors.New("Postgres is unavailable")
	}
	b := &backend{pool: pool}
	if e = b.migrate(ctx); e != nil {
		pool.Close()
		return nil, fmt.Errorf("Postgres migration: %w", e)
	}
	return records.New(b, cfg.TaskBudgetUSD), nil
}
func OpenUsers(ctx context.Context, cfg Config) (*Users, error) {
	d, e := Open(ctx, cfg)
	if e != nil {
		return nil, e
	}
	return records.NewUsers(d), nil
}

type backend struct{ pool *pgxpool.Pool }

func (b *backend) Close() error { b.pool.Close(); return nil }

func (b *backend) DeleteBatch(ctx context.Context, paths []string) error {
	collections, ids := make([]string, len(paths)), make([]string, len(paths))
	for i, path := range paths {
		var err error
		collections[i], ids[i], err = split(path)
		if err != nil {
			return err
		}
	}
	_, err := b.pool.Exec(ctx, "DELETE FROM nori_records WHERE (collection,id) IN (SELECT unnest($1::text[]),unnest($2::text[]))", collections, ids)
	return err
}

type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type transaction struct{ q executor }

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
	if e := d.Decode(&m); e != nil {
		return nil, e
	}
	docstore.NormalizeNumbers(m)
	return m, nil
}
func get(ctx context.Context, q executor, path string) (docstore.Record, error) {
	collection, id, e := split(path)
	if e != nil {
		return docstore.Record{}, e
	}
	var raw []byte
	e = q.QueryRow(ctx, "SELECT data FROM nori_records WHERE collection=$1 AND id=$2", collection, id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return docstore.Record{}, docstore.ErrNotFound
	}
	if e != nil {
		return docstore.Record{}, e
	}
	data, e := decode(raw)
	return docstore.Record{Path: path, Fields: data}, e
}
func (b *backend) Get(ctx context.Context, path string) (docstore.Record, error) {
	return get(ctx, b.pool, path)
}
func (t *transaction) Get(ctx context.Context, path string) (docstore.Record, error) {
	return get(ctx, t.q, path)
}
func (b *backend) Query(ctx context.Context, s docstore.QuerySpec) ([]docstore.Record, error) {
	return query(ctx, b.pool, s)
}
func (t *transaction) Query(ctx context.Context, s docstore.QuerySpec) ([]docstore.Record, error) {
	return query(ctx, t.q, s)
}
func (b *backend) Apply(ctx context.Context, m docstore.Mutation) error {
	return b.RunTransaction(ctx, false, 5, func(tx docstore.TransactionBackend) error { return tx.Apply(ctx, m) })
}
func (t *transaction) Apply(ctx context.Context, m docstore.Mutation) error {
	collection, id, e := split(m.Path)
	if e != nil {
		return e
	}
	if m.Kind == "delete" {
		_, e = t.q.Exec(ctx, "DELETE FROM nori_records WHERE collection=$1 AND id=$2", collection, id)
		return e
	}
	fields := m.Fields
	if m.Kind == "update" || m.Merge {
		old, e := t.Get(ctx, m.Path)
		if errors.Is(e, docstore.ErrNotFound) && m.Kind == "set" {
			old.Fields = map[string]any{}
		} else if e != nil {
			return e
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
					encoded, e := docstore.Value(value)
					if e != nil {
						return e
					}
					encoded = docstore.JSONValue(encoded)
					raw, e := json.Marshal(encoded)
					if e != nil {
						return e
					}
					found := false
					for _, old := range prior {
						previous, _ := json.Marshal(docstore.JSONValue(old))
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
			value, e := docstore.Value(change.Value)
			if e != nil {
				return e
			}
			parent[key] = value
		}
	}
	if m.Kind != "create" && m.Kind != "set" && m.Kind != "update" {
		return errors.New("unknown document mutation")
	}
	data, e := json.Marshal(docstore.JSONValue(fields))
	if e != nil {
		return e
	}
	sql := "INSERT INTO nori_records(collection,id,data) VALUES($1,$2,$3::jsonb)"
	if m.Kind != "create" {
		sql += " ON CONFLICT(collection,id) DO UPDATE SET data=EXCLUDED.data"
	}
	_, e = t.q.Exec(ctx, sql, collection, id, data)
	return e
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
func retryable(e error) bool {
	var p *pgconn.PgError
	return errors.As(e, &p) && (p.Code == "40001" || p.Code == "40P01" || p.Code == "23505")
}
func (b *backend) RunTransaction(ctx context.Context, readOnly bool, attempts int, f func(docstore.TransactionBackend) error) error {
	options := pgx.TxOptions{IsoLevel: pgx.Serializable}
	if readOnly {
		options.AccessMode = pgx.ReadOnly
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		tx, e := b.pool.BeginTx(ctx, options)
		if e != nil {
			return e
		}
		err = f(&transaction{q: tx})
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		// Rollback releases the connection even when commit or callback failed.
		_ = tx.Rollback(context.WithoutCancel(ctx))
		if err == nil {
			return nil
		}
		if !retryable(err) {
			return err
		}
		timer := time.NewTimer(time.Duration(min(50, attempt+1)) * 5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return docstore.ErrExists
	}
	return err
}

// Poll the committed log in ordered, bounded batches. No connection is held
// while idle; reconnect resumes from the durable sequence, not a notification.
func (b *backend) Watch(ctx context.Context, s docstore.QuerySpec, deliver func([]docstore.Record) error) error {
	s.Filters = slices.Clone(s.Filters)
	s.Count = 200
	for {
		rows, e := b.Query(ctx, s)
		if e != nil {
			return e
		}
		if len(rows) > 0 {
			if e = deliver(rows); e != nil {
				return e
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
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (b *backend) migrate(ctx context.Context) error {
	tx, e := b.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// One migrator at a time, including concurrent API and worker startup.
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7162384101)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS nori_migrations(name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())"); e != nil {
		return e
	}
	files, e := migrations.ReadDir("migrations")
	if e != nil {
		return e
	}
	for _, file := range files {
		var exists bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM nori_migrations WHERE name=$1)", file.Name()).Scan(&exists); e != nil {
			return e
		}
		if exists {
			continue
		}
		sql, e := migrations.ReadFile("migrations/" + file.Name())
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, string(sql)); e != nil {
			return fmt.Errorf("%s failed: %w", file.Name(), e)
		}
		if _, e = tx.Exec(ctx, "INSERT INTO nori_migrations(name) VALUES($1)", file.Name()); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
