package postgres

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	neturl "net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

const (
	defaultMaxConns          int32 = 25
	defaultMinConns          int32 = 2
	defaultMaxConnLifetime         = 30 * time.Minute
	defaultMaxConnIdleTime         = 5 * time.Minute
	defaultHealthCheckPeriod       = 30 * time.Second
	defaultStatementTimeout        = 30 * time.Second
	shardLockShards                = 512
)

//go:embed migrations/*.sql
var migrations embed.FS

type Options struct {
	ListenURL         string
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	StatementTimeout  time.Duration
}

func OpenBackend(ctx context.Context, url string) (docstore.Backend, error) {
	return openBackend(ctx, url, Options{})
}

func OpenBackendWithOptions(ctx context.Context, url string, opts Options) (docstore.Backend, error) {
	return openBackend(ctx, url, opts)
}

func connStringKeys(connStr string) map[string]bool {
	keys := map[string]bool{}
	s := strings.TrimSpace(connStr)
	if strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://") {
		if u, err := neturl.Parse(s); err == nil {
			for k := range u.Query() {
				keys[k] = true
			}
		}
		return keys
	}
	for _, tok := range strings.Fields(s) {
		if k, _, ok := strings.Cut(tok, "="); ok && k != "" {
			keys[k] = true
		}
	}
	return keys
}

func parsePoolConfig(rawURL string, opts Options) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(rawURL)
	if err != nil {
		return nil, err
	}
	keys := connStringKeys(rawURL)

	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	} else if !keys["pool_max_conns"] {
		cfg.MaxConns = defaultMaxConns
	}

	if opts.MinConns > 0 {
		cfg.MinConns = opts.MinConns
	} else if !keys["pool_min_conns"] {
		cfg.MinConns = defaultMinConns
	}
	if cfg.MaxConns > 0 && cfg.MinConns > cfg.MaxConns {
		cfg.MinConns = cfg.MaxConns
	}

	if opts.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = opts.MaxConnLifetime
	} else if !keys["pool_max_conn_lifetime"] {
		cfg.MaxConnLifetime = defaultMaxConnLifetime
	}

	if opts.MaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = opts.MaxConnIdleTime
	} else if !keys["pool_max_conn_idle_time"] {
		cfg.MaxConnIdleTime = defaultMaxConnIdleTime
	}

	if opts.HealthCheckPeriod > 0 {
		cfg.HealthCheckPeriod = opts.HealthCheckPeriod
	} else if !keys["pool_health_check_period"] {
		cfg.HealthCheckPeriod = defaultHealthCheckPeriod
	}

	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	if opts.StatementTimeout > 0 {
		ms := max(int64(1), opts.StatementTimeout.Milliseconds())
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(ms, 10)
	} else if cfg.ConnConfig.RuntimeParams["statement_timeout"] == "" && !strings.Contains(cfg.ConnConfig.RuntimeParams["options"], "statement_timeout") {
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(defaultStatementTimeout.Milliseconds(), 10)
	}
	return cfg, nil
}

func openBackend(ctx context.Context, url string, opts Options) (*backend, error) {
	if strings.TrimSpace(url) == "" {
		return nil, errors.New("NORI_POSTGRES_URL must be configured")
	}
	cfg, e := parsePoolConfig(url, opts)
	if e != nil {
		return nil, errors.New("invalid NORI_POSTGRES_URL")
	}
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		return nil, errors.New("invalid NORI_POSTGRES_URL")
	}
	if e = pool.Ping(ctx); e != nil {
		pool.Close()
		return nil, errors.New("Postgres is unavailable")
	}
	b := &backend{
		pool:          pool,
		connURL:       url,
		listenURL:     cmp.Or(strings.TrimSpace(opts.ListenURL), url),
		userDataLocks: make(chan struct{}, max(1, cfg.MaxConns/2)),
	}
	for i := range b.ownerWriteLocks {
		b.ownerWriteLocks[i] = make(chan struct{}, 1)
	}
	if e = b.migrate(ctx); e != nil {
		pool.Close()
		return nil, fmt.Errorf("Postgres migration: %w", e)
	}
	return b, nil
}

type TxStats struct {
	Total     int64
	Attempts  int64
	Retries   int64
	Exhausted int64
}

func TxStatsOf(b docstore.Backend) (TxStats, bool) {
	pg, ok := b.(*backend)
	if !ok {
		return TxStats{}, false
	}
	return TxStats{
		Total:     pg.txTotal.Load(),
		Attempts:  pg.txAttempts.Load(),
		Retries:   pg.txRetries.Load(),
		Exhausted: pg.txExhausted.Load(),
	}, true
}

type pendingWakeWaiting struct {
	userID string
	reason string
}

var (
	_ docstore.Pinger               = (*backend)(nil)
	_ docstore.PoolStatsProvider    = (*backend)(nil)
	_ docstore.TxStatsProvider      = (*backend)(nil)
	_ docstore.ListenStatsProvider  = (*backend)(nil)
	_ docstore.QueueMetricsProvider = (*backend)(nil)
	_ docstore.AdvisoryLocker       = (*backend)(nil)
)

type backend struct {
	pool        *pgxpool.Pool
	connURL     string
	listenURL   string
	txTotal     atomic.Int64
	txAttempts  atomic.Int64
	txRetries   atomic.Int64
	txExhausted atomic.Int64

	ownerWriteLocks [shardLockShards]chan struct{}
	userDataLocks   chan struct{}

	wakeReconnects  atomic.Int64
	wakeErrors      atomic.Int64
	watchReconnects atomic.Int64
	watchErrors     atomic.Int64
	watchMu         sync.Mutex
	hub             *watchHub
}

func (b *backend) Close() error {
	b.closeWatchHub()
	b.pool.Close()
	return nil
}

func (b *backend) Ping(ctx context.Context) error {
	if b == nil || b.pool == nil {
		return errors.New("postgres pool is not open")
	}
	return b.pool.Ping(ctx)
}

func (b *backend) PoolStats() docstore.PoolStats {
	if b == nil || b.pool == nil {
		return docstore.PoolStats{}
	}
	st := b.pool.Stat()
	return docstore.PoolStats{
		AcquiredConns:     st.AcquiredConns(),
		IdleConns:         st.IdleConns(),
		TotalConns:        st.TotalConns(),
		MaxConns:          st.MaxConns(),
		ConstructingConns: st.ConstructingConns(),
		EmptyAcquireCount: st.EmptyAcquireCount(),
		AcquireDuration:   st.AcquireDuration(),
	}
}

func (b *backend) TxStats() docstore.TxStats {
	if b == nil {
		return docstore.TxStats{}
	}
	return docstore.TxStats{
		Total:     b.txTotal.Load(),
		Attempts:  b.txAttempts.Load(),
		Retries:   b.txRetries.Load(),
		Exhausted: b.txExhausted.Load(),
	}
}

func (b *backend) TryAdvisoryLock(ctx context.Context, key int64) (func(), bool, error) {
	if b == nil || b.pool == nil {
		return nil, false, errors.New("postgres pool is not open")
	}
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = conn.Conn().Close(closeCtx)
		cancel()
		conn.Release()
		return nil, false, err
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}
	var once sync.Once
	unlock := func() {
		once.Do(func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, unlockErr := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1)", key)
			if unlockErr != nil {
				_ = conn.Conn().Close(unlockCtx)
			}
			conn.Release()
		})
	}
	return unlock, true, nil
}

// OwnerFromCollection extracts the tenant owner ID from a collection path of the
// form "owners/<owner>/..." (or "owners/<owner>"). Shared collections such as
// "system" or "users" return "".
func OwnerFromCollection(collection string) string {
	rest, ok := strings.CutPrefix(collection, "owners/")
	if !ok || rest == "" {
		return ""
	}
	owner, _, _ := strings.Cut(rest, "/")
	return owner
}

func recordShard(collection string) string {
	if rest, ok := strings.CutPrefix(collection, "owners/"); ok {
		if owner, _, found := strings.Cut(rest, "/"); found && owner != "" {
			return "owners/" + owner
		}
	}
	return collection
}

func (b *backend) acquireShardLock(ctx context.Context, shard string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var h uint64 = 14695981039346656037
	for i := 0; i < len(shard); i++ {
		h ^= uint64(shard[i])
		h *= 1099511628211
	}
	ch := b.ownerWriteLocks[h%shardLockShards]
	if ch == nil {
		return nil, nil
	}
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *backend) DeleteBatch(ctx context.Context, paths []string) error {
	shards := make([]string, len(paths))
	collections, ids := make([]string, len(paths)), make([]string, len(paths))
	for i, path := range paths {
		var err error
		collections[i], ids[i], err = split(path)
		if err != nil {
			return err
		}
		shards[i] = recordShard(collections[i])
	}
	if users.ID(ctx) != "" {
		return b.readCommitted(ctx, false, func(q executor) error {
			_, err := q.Exec(ctx, "DELETE FROM nori_records WHERE (shard,collection,id) IN (SELECT unnest($1::text[]),unnest($2::text[]),unnest($3::text[]))", shards, collections, ids)
			return err
		})
	}
	_, err := b.pool.Exec(ctx, "DELETE FROM nori_records WHERE (shard,collection,id) IN (SELECT unnest($1::text[]),unnest($2::text[]),unnest($3::text[]))", shards, collections, ids)
	return err
}

type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

type lazyExecutor struct {
	t *transaction
}

func (l lazyExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := l.t.ensureTx(ctx, ""); err != nil {
		return pgconn.CommandTag{}, err
	}
	return l.t.tx.Exec(ctx, sql, args...)
}
func (l lazyExecutor) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := l.t.ensureTx(ctx, ""); err != nil {
		return nil, err
	}
	return l.t.tx.Query(ctx, sql, args...)
}
func (l lazyExecutor) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := l.t.ensureTx(ctx, ""); err != nil {
		return errRow{err: err}
	}
	return l.t.tx.QueryRow(ctx, sql, args...)
}

type transaction struct {
	b            *backend
	opts         pgx.TxOptions
	tx           pgx.Tx
	q            executor
	unlockShard  func()
	wakes        []docstore.WakeEvent
	watchCols    []string
	wakeWaitings []pendingWakeWaiting
}

func (t *transaction) ensureTx(ctx context.Context, shard string) error {
	if t.tx != nil || t.b == nil {
		return nil
	}
	if t.opts.AccessMode != pgx.ReadOnly && strings.HasPrefix(shard, "owners/") {
		unlock, err := t.b.acquireShardLock(ctx, shard)
		if err != nil {
			return err
		}
		t.unlockShard = unlock
	}
	tx, err := t.b.pool.BeginTx(ctx, t.opts)
	if err != nil {
		if t.unlockShard != nil {
			t.unlockShard()
			t.unlockShard = nil
		}
		return err
	}
	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		if t.unlockShard != nil {
			t.unlockShard()
			t.unlockShard = nil
		}
		return err
	}
	if ownerID := users.ID(ctx); ownerID != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_owner', $1, true)", ownerID); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			if t.unlockShard != nil {
				t.unlockShard()
				t.unlockShard = nil
			}
			return err
		}
	}
	t.tx = tx
	return nil
}

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
	e = q.QueryRow(ctx, "SELECT data FROM nori_records WHERE shard=$1 AND collection=$2 AND id=$3", recordShard(collection), collection, id).Scan(&raw)
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
	if users.ID(ctx) != "" {
		var rec docstore.Record
		err := b.readCommitted(ctx, true, func(q executor) error {
			var err error
			rec, err = get(ctx, q, path)
			return err
		})
		return rec, err
	}
	return get(ctx, b.pool, path)
}
func (t *transaction) Get(ctx context.Context, path string) (docstore.Record, error) {
	if col, _, err := split(path); err == nil {
		if err := t.ensureTx(ctx, recordShard(col)); err != nil {
			return docstore.Record{}, err
		}
	}
	return get(ctx, t.q, path)
}
func (b *backend) Query(ctx context.Context, s docstore.QuerySpec) ([]docstore.Record, error) {
	if users.ID(ctx) != "" {
		var recs []docstore.Record
		err := b.readCommitted(ctx, true, func(q executor) error {
			var err error
			recs, err = query(ctx, q, s)
			return err
		})
		return recs, err
	}
	return query(ctx, b.pool, s)
}
func (t *transaction) Query(ctx context.Context, s docstore.QuerySpec) ([]docstore.Record, error) {
	if err := t.ensureTx(ctx, recordShard(s.Collection)); err != nil {
		return nil, err
	}
	return query(ctx, t.q, s)
}
func (b *backend) CurrentSequence(ctx context.Context, path string) (int64, error) {
	var seq int64
	err := b.pool.QueryRow(ctx, "SELECT seq FROM nori_sequences WHERE path=$1", path).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return seq, err
}
func (t *transaction) NextSequence(ctx context.Context, path string, count int) (int64, error) {
	if count <= 0 {
		return 0, nil
	}
	if col, _, err := split(path); err == nil {
		if err := t.ensureTx(ctx, recordShard(col)); err != nil {
			return 0, err
		}
	}
	return advanceSequence(ctx, t.q, path, count)
}

func advanceSequence(ctx context.Context, q executor, path string, count int) (int64, error) {
	var end int64
	err := q.QueryRow(ctx,
		"INSERT INTO nori_sequences(path,seq) VALUES($1,$2) ON CONFLICT(path) DO UPDATE SET seq=nori_sequences.seq+$2 RETURNING seq",
		path, int64(count),
	).Scan(&end)
	if err != nil {
		return 0, err
	}
	return end - int64(count) + 1, nil
}
func isWatchableCollection(collection string, fields map[string]any) bool {
	return strings.HasSuffix(collection, "/events") || (fields != nil && fields["seq"] != nil)
}

func applyMutationFields(fields map[string]any, m docstore.Mutation) (map[string]any, error) {
	if fields == nil {
		fields = map[string]any{}
	}
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
					return nil, e
				}
				encoded = docstore.JSONValue(encoded)
				raw, e := json.Marshal(encoded)
				if e != nil {
					return nil, e
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
			return nil, e
		}
		parent[key] = value
	}
	return fields, nil
}

func (b *backend) Apply(ctx context.Context, m docstore.Mutation) error {
	collection, id, e := split(m.Path)
	if e != nil {
		return e
	}
	shard := recordShard(collection)
	switch m.Kind {
	case "delete":
		if users.ID(ctx) != "" {
			return b.readCommitted(ctx, false, func(q executor) error {
				_, err := q.Exec(ctx, "DELETE FROM nori_records WHERE shard=$1 AND collection=$2 AND id=$3", shard, collection, id)
				return err
			})
		}
		_, e = b.pool.Exec(ctx, "DELETE FROM nori_records WHERE shard=$1 AND collection=$2 AND id=$3", shard, collection, id)
		return e
	case "create", "set", "update":
	default:
		return errors.New("unknown document mutation")
	}
	if m.Kind != "update" && !m.Merge {
		data, e := json.Marshal(docstore.JSONValue(m.Fields))
		if e != nil {
			return e
		}
		sql := "INSERT INTO nori_records(shard,collection,id,data) VALUES($1,$2,$3,$4::jsonb)"
		if m.Kind != "create" {
			sql += " ON CONFLICT(shard,collection,id) DO UPDATE SET data=EXCLUDED.data"
		}
		if users.ID(ctx) != "" {
			e = b.readCommitted(ctx, false, func(q executor) error {
				_, err := q.Exec(ctx, sql, shard, collection, id, data)
				return err
			})
		} else {
			_, e = b.pool.Exec(ctx, sql, shard, collection, id, data)
		}
		var p *pgconn.PgError
		if errors.As(e, &p) && p.Code == "23505" {
			return docstore.ErrExists
		}
		if e == nil && isWatchableCollection(collection, m.Fields) {
			b.emitWatchCollections(ctx, []string{collection})
		}
		return e
	}
	var watch bool
	err := b.readCommitted(ctx, false, func(q executor) error {
		watch = false
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			var raw []byte
			err := q.QueryRow(ctx, "SELECT data FROM nori_records WHERE shard=$1 AND collection=$2 AND id=$3 FOR UPDATE", shard, collection, id).Scan(&raw)
			if errors.Is(err, pgx.ErrNoRows) {
				if m.Kind != "set" {
					return docstore.ErrNotFound
				}
				fields, err := applyMutationFields(map[string]any{}, m)
				if err != nil {
					return err
				}
				data, err := json.Marshal(docstore.JSONValue(fields))
				if err != nil {
					return err
				}
				tag, err := q.Exec(ctx,
					"INSERT INTO nori_records(shard,collection,id,data) VALUES($1,$2,$3,$4::jsonb) ON CONFLICT(shard,collection,id) DO NOTHING",
					shard, collection, id, data,
				)
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 1 {
					watch = isWatchableCollection(collection, fields)
					return nil
				}
				continue
			}
			if err != nil {
				return err
			}
			if m.Kind == "create" {
				return docstore.ErrExists
			}
			oldFields, err := decode(raw)
			if err != nil {
				return err
			}
			fields, err := applyMutationFields(oldFields, m)
			if err != nil {
				return err
			}
			data, err := json.Marshal(docstore.JSONValue(fields))
			if err != nil {
				return err
			}
			_, err = q.Exec(ctx, "UPDATE nori_records SET data=$4::jsonb WHERE shard=$1 AND collection=$2 AND id=$3", shard, collection, id, data)
			if err != nil {
				return err
			}
			watch = isWatchableCollection(collection, fields)
			return nil
		}
	})
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return docstore.ErrExists
	}
	if err == nil && watch {
		b.emitWatchCollections(ctx, []string{collection})
	}
	return err
}

func (t *transaction) Apply(ctx context.Context, m docstore.Mutation) error {
	collection, id, e := split(m.Path)
	if e != nil {
		return e
	}
	shard := recordShard(collection)
	if e := t.ensureTx(ctx, shard); e != nil {
		return e
	}
	if m.Kind == "delete" {
		_, e = t.q.Exec(ctx, "DELETE FROM nori_records WHERE shard=$1 AND collection=$2 AND id=$3", shard, collection, id)
		return e
	}
	if m.Kind != "create" && m.Kind != "set" && m.Kind != "update" {
		return errors.New("unknown document mutation")
	}
	fields := m.Fields
	if m.Kind == "update" || m.Merge {
		old, e := t.Get(ctx, m.Path)
		if errors.Is(e, docstore.ErrNotFound) && m.Kind == "set" {
			old.Fields = map[string]any{}
		} else if e != nil {
			return e
		}
		fields, e = applyMutationFields(old.Fields, m)
		if e != nil {
			return e
		}
	}
	data, e := json.Marshal(docstore.JSONValue(fields))
	if e != nil {
		return e
	}
	sql := "INSERT INTO nori_records(shard,collection,id,data) VALUES($1,$2,$3,$4::jsonb)"
	if m.Kind != "create" {
		sql += " ON CONFLICT(shard,collection,id) DO UPDATE SET data=EXCLUDED.data"
	}
	_, e = t.q.Exec(ctx, sql, shard, collection, id, data)
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return docstore.ErrExists
	}
	if e == nil && isWatchableCollection(collection, fields) {
		t.watchCols = append(t.watchCols, collection)
	}
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
	return errors.As(e, &p) && (p.Code == "40001" || p.Code == "40P01")
}
func (b *backend) runAttempt(ctx context.Context, options pgx.TxOptions, f func(docstore.TransactionBackend) error) error {
	t := &transaction{b: b, opts: options}
	t.q = lazyExecutor{t: t}
	defer func() {
		if t.tx != nil {
			_ = t.tx.Rollback(context.WithoutCancel(ctx))
		}
		if t.unlockShard != nil {
			t.unlockShard()
		}
	}()
	if err := f(t); err != nil {
		return err
	}
	if t.tx != nil {
		if err := t.tx.Commit(ctx); err != nil {
			return err
		}
		t.tx = nil
	}
	if t.unlockShard != nil {
		t.unlockShard()
		t.unlockShard = nil
	}
	events := t.wakes
	if len(t.wakeWaitings) > 0 {
		events = append(events, b.flushWakeWaitings(ctx, t.wakeWaitings)...)
	}
	if len(t.watchCols) > 0 {
		events = append(events, b.watchWakeEvents(t.watchCols)...)
	}
	b.emitWakes(ctx, events)
	return nil
}

func (b *backend) RunTransaction(ctx context.Context, readOnly bool, attempts int, f func(docstore.TransactionBackend) error) error {
	if attempts <= 0 {
		attempts = 15
	}
	b.txTotal.Add(1)
	options := pgx.TxOptions{IsoLevel: pgx.Serializable}
	if readOnly {
		options.AccessMode = pgx.ReadOnly
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		b.txAttempts.Add(1)
		err = b.runAttempt(ctx, options, f)
		if err == nil {
			return nil
		}
		if !retryable(err) {
			break
		}
		b.txRetries.Add(1)
		if attempt == attempts-1 {
			b.txExhausted.Add(1)
			break
		}
		capMS := min(250, 5*(1<<min(attempt, 6)))
		wait := time.Duration(1+rand.IntN(capMS)) * time.Millisecond
		timer := time.NewTimer(wait)
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

func (b *backend) migrate(ctx context.Context) error {
	err := b.migrationTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS nori_migrations (
			name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now(), checksum text
		);
		ALTER TABLE nori_migrations ADD COLUMN IF NOT EXISTS checksum text`)
		return err
	})
	if err != nil {
		return err
	}

	files, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, file := range files {
		name := file.Name()
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])
		err = b.migrationTransaction(ctx, func(ctx context.Context, tx pgx.Tx) error {
			var existing *string
			err := tx.QueryRow(ctx, "SELECT checksum FROM nori_migrations WHERE name=$1", name).Scan(&existing)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				if _, err = tx.Exec(ctx, string(body)); err != nil {
					return err
				}
				_, err = tx.Exec(ctx, "INSERT INTO nori_migrations(name, checksum) VALUES($1, $2)", name, checksum)
				return err
			case err != nil:
				return err
			case existing == nil:
				_, err = tx.Exec(ctx, "UPDATE nori_migrations SET checksum=$2 WHERE name=$1", name, checksum)
				return err
			case *existing != checksum:
				return errors.New("applied migration checksum differs from the embedded file")
			default:
				return nil
			}
		})
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (b *backend) migrationTransaction(ctx context.Context, run func(context.Context, pgx.Tx) error) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '5min'"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7162384101)"); err != nil {
		return err
	}
	if err := run(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
