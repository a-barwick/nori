package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
)

func TestParsePoolConfigDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "uri", url: "postgres://nori@127.0.0.1:15432/nori?sslmode=disable"},
		{name: "dsn", url: "host=127.0.0.1 port=15432 user=nori dbname=nori sslmode=disable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parsePoolConfig(tc.url, Options{})
			if err != nil {
				t.Fatalf("parsePoolConfig: %v", err)
			}
			if cfg.MaxConns != defaultMaxConns {
				t.Errorf("MaxConns = %d, want %d", cfg.MaxConns, defaultMaxConns)
			}
			if cfg.MinConns != defaultMinConns {
				t.Errorf("MinConns = %d, want %d", cfg.MinConns, defaultMinConns)
			}
			if cfg.MaxConnLifetime != defaultMaxConnLifetime {
				t.Errorf("MaxConnLifetime = %v, want %v", cfg.MaxConnLifetime, defaultMaxConnLifetime)
			}
			if cfg.MaxConnIdleTime != defaultMaxConnIdleTime {
				t.Errorf("MaxConnIdleTime = %v, want %v", cfg.MaxConnIdleTime, defaultMaxConnIdleTime)
			}
			if cfg.HealthCheckPeriod != defaultHealthCheckPeriod {
				t.Errorf("HealthCheckPeriod = %v, want %v", cfg.HealthCheckPeriod, defaultHealthCheckPeriod)
			}
			if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "30000" {
				t.Errorf("statement_timeout = %q, want %q", got, "30000")
			}
		})
	}
}

func TestParsePoolConfigCustomOptions(t *testing.T) {
	raw := "postgres://nori@127.0.0.1:15432/nori?sslmode=disable&pool_max_conns=10&pool_min_conns=1&pool_max_conn_lifetime=10m&pool_max_conn_idle_time=1m&pool_health_check_period=5s&statement_timeout=5000"
	opts := Options{
		MaxConns:          40,
		MinConns:          6,
		MaxConnLifetime:   45 * time.Minute,
		MaxConnIdleTime:   8 * time.Minute,
		HealthCheckPeriod: 20 * time.Second,
		StatementTimeout:  12 * time.Second,
	}
	cfg, err := parsePoolConfig(raw, opts)
	if err != nil {
		t.Fatalf("parsePoolConfig: %v", err)
	}
	if cfg.MaxConns != 40 {
		t.Errorf("MaxConns = %d, want 40", cfg.MaxConns)
	}
	if cfg.MinConns != 6 {
		t.Errorf("MinConns = %d, want 6", cfg.MinConns)
	}
	if cfg.MaxConnLifetime != 45*time.Minute {
		t.Errorf("MaxConnLifetime = %v, want 45m", cfg.MaxConnLifetime)
	}
	if cfg.MaxConnIdleTime != 8*time.Minute {
		t.Errorf("MaxConnIdleTime = %v, want 8m", cfg.MaxConnIdleTime)
	}
	if cfg.HealthCheckPeriod != 20*time.Second {
		t.Errorf("HealthCheckPeriod = %v, want 20s", cfg.HealthCheckPeriod)
	}
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "12000" {
		t.Errorf("statement_timeout = %q, want %q", got, "12000")
	}

	cfgClamp, err := parsePoolConfig("postgres://nori@127.0.0.1:15432/nori?sslmode=disable", Options{
		MaxConns:         1,
		StatementTimeout: 500 * time.Microsecond,
	})
	if err != nil {
		t.Fatalf("parsePoolConfig clamp: %v", err)
	}
	if cfgClamp.MaxConns != 1 || cfgClamp.MinConns != 1 {
		t.Errorf("clamped MaxConns/MinConns = (%d, %d), want (1, 1)", cfgClamp.MaxConns, cfgClamp.MinConns)
	}
	if got := cfgClamp.ConnConfig.RuntimeParams["statement_timeout"]; got != "1" {
		t.Errorf("sub-ms statement_timeout = %q, want %q", got, "1")
	}

	cfgExplicitClamp, err := parsePoolConfig("postgres://nori@127.0.0.1:15432/nori?sslmode=disable", Options{
		MaxConns: 3,
		MinConns: 10,
	})
	if err != nil {
		t.Fatalf("parsePoolConfig explicit clamp: %v", err)
	}
	if cfgExplicitClamp.MaxConns != 3 || cfgExplicitClamp.MinConns != 3 {
		t.Errorf("explicit clamp MaxConns/MinConns = (%d, %d), want (3, 3)", cfgExplicitClamp.MaxConns, cfgExplicitClamp.MinConns)
	}
}

func TestParsePoolConfigURLOverridePrecedence(t *testing.T) {
	raw := "postgres://nori@127.0.0.1:15432/nori?sslmode=disable&pool_max_conns=12&pool_min_conns=0&pool_max_conn_lifetime=15m&pool_max_conn_idle_time=2m&pool_health_check_period=10s&statement_timeout=7500"
	cfg, err := parsePoolConfig(raw, Options{})
	if err != nil {
		t.Fatalf("parsePoolConfig: %v", err)
	}
	if cfg.MaxConns != 12 {
		t.Errorf("MaxConns = %d, want 12", cfg.MaxConns)
	}
	if cfg.MinConns != 0 {
		t.Errorf("MinConns = %d, want 0 (explicit URL override)", cfg.MinConns)
	}
	if cfg.MaxConnLifetime != 15*time.Minute {
		t.Errorf("MaxConnLifetime = %v, want 15m", cfg.MaxConnLifetime)
	}
	if cfg.MaxConnIdleTime != 2*time.Minute {
		t.Errorf("MaxConnIdleTime = %v, want 2m", cfg.MaxConnIdleTime)
	}
	if cfg.HealthCheckPeriod != 10*time.Second {
		t.Errorf("HealthCheckPeriod = %v, want 10s", cfg.HealthCheckPeriod)
	}
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "7500" {
		t.Errorf("statement_timeout = %q, want %q", got, "7500")
	}

	cfgOne, err := parsePoolConfig("postgres://nori@127.0.0.1:15432/nori?sslmode=disable&pool_max_conns=1", Options{})
	if err != nil {
		t.Fatalf("parsePoolConfig pool_max_conns=1: %v", err)
	}
	if cfgOne.MaxConns != 1 || cfgOne.MinConns != 1 {
		t.Errorf("pool_max_conns=1 MaxConns/MinConns = (%d, %d), want (1, 1)", cfgOne.MaxConns, cfgOne.MinConns)
	}

	dsn := "host=127.0.0.1 port=15432 user=nori dbname=nori pool_max_conns=9 pool_min_conns=3 statement_timeout=4500"
	cfgDSN, err := parsePoolConfig(dsn, Options{})
	if err != nil {
		t.Fatalf("parsePoolConfig dsn: %v", err)
	}
	if cfgDSN.MaxConns != 9 || cfgDSN.MinConns != 3 || cfgDSN.ConnConfig.RuntimeParams["statement_timeout"] != "4500" {
		t.Errorf("dsn overrides = (max=%d min=%d stmt=%q), want (9, 3, 4500)",
			cfgDSN.MaxConns, cfgDSN.MinConns, cfgDSN.ConnConfig.RuntimeParams["statement_timeout"])
	}

	cfgOpt, err := parsePoolConfig("postgres://nori@127.0.0.1:15432/nori?sslmode=disable&options=-c%20statement_timeout%3D9000", Options{})
	if err != nil {
		t.Fatalf("parsePoolConfig options: %v", err)
	}
	if got := cfgOpt.ConnConfig.RuntimeParams["statement_timeout"]; got != "" {
		t.Errorf("expected statement_timeout runtime param to stay unset when set via options, got %q", got)
	}
}

func TestParsePoolConfigInvalidURL(t *testing.T) {
	if _, err := parsePoolConfig("postgres://[::1:bad", Options{}); err == nil {
		t.Fatalf("expected error for malformed URL, got nil")
	}
}

func TestIsWatchableCollection(t *testing.T) {
	if !isWatchableCollection("owners/u1/events", nil) {
		t.Errorf("expected /events suffix to be watchable")
	}
	if !isWatchableCollection("custom/stream", map[string]any{"seq": int64(1)}) {
		t.Errorf("expected doc with seq field to be watchable")
	}
	if isWatchableCollection("owners/u1/runs", map[string]any{"status": "running"}) {
		t.Errorf("expected runs without seq to not be watchable")
	}
}

func TestRecordShard(t *testing.T) {
	for _, tc := range []struct {
		col  string
		want string
	}{
		{"system", "system"},
		{"users", "users"},
		{"owners/u1/runs", "owners/u1"},
		{"owners/u1/runs/r1/operations", "owners/u1"},
		{"owners/system/events", "owners/system"},
		{"owners/users/events", "owners/users"},
		{"owners/u1", "owners/u1"},
	} {
		if got := recordShard(tc.col); got != tc.want {
			t.Errorf("recordShard(%q) = %q, want %q", tc.col, got, tc.want)
		}
	}
}

func TestPostgresPingPoolStatsAndTxStats(t *testing.T) {
	b, prefix := openTestBackend(t)
	ctx := context.Background()

	if err := b.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	var nilBackend *backend
	if err := nilBackend.Ping(ctx); err == nil {
		t.Fatalf("nil backend Ping: expected error, got nil")
	}

	ps := b.PoolStats()
	if ps.MaxConns != defaultMaxConns {
		t.Fatalf("PoolStats.MaxConns = %d, want %d", ps.MaxConns, defaultMaxConns)
	}
	if ps.TotalConns < 1 {
		t.Fatalf("PoolStats.TotalConns = %d, want >= 1", ps.TotalConns)
	}

	var stmtTimeout string
	if err := b.pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&stmtTimeout); err != nil {
		t.Fatalf("SHOW statement_timeout: %v", err)
	}
	if stmtTimeout != "30s" {
		t.Fatalf("SHOW statement_timeout = %q, want %q", stmtTimeout, "30s")
	}

	before := b.TxStats()
	docPath := "owners/" + prefix + "u1/steps/s1"
	if err := b.Apply(ctx, docstore.Mutation{Kind: "create", Path: docPath, Fields: map[string]any{"n": 1}}); err != nil {
		t.Fatalf("standalone Create: %v", err)
	}
	if err := b.Apply(ctx, docstore.Mutation{Kind: "create", Path: docPath, Fields: map[string]any{"n": 2}}); !errors.Is(err, docstore.ErrExists) {
		t.Fatalf("duplicate standalone Create = %v, want ErrExists", err)
	}
	if err := b.Apply(ctx, docstore.Mutation{Kind: "update", Path: docPath, Updates: []docstore.Update{{Path: "n", Value: 2}}}); err != nil {
		t.Fatalf("standalone Update: %v", err)
	}
	if err := b.Apply(ctx, docstore.Mutation{Kind: "delete", Path: docPath}); err != nil {
		t.Fatalf("standalone Delete: %v", err)
	}
	if err := b.Apply(ctx, docstore.Mutation{Kind: "update", Path: docPath, Updates: []docstore.Update{{Path: "n", Value: 3}}}); !errors.Is(err, docstore.ErrNotFound) {
		t.Fatalf("missing standalone Update = %v, want ErrNotFound", err)
	}
	afterApply := b.TxStats()
	if afterApply.Total != before.Total || afterApply.Attempts != before.Attempts {
		t.Fatalf("standalone Apply incremented serializable TxStats: before=%+v after=%+v", before, afterApply)
	}

	if err := b.RunTransaction(ctx, true, 5, func(tx docstore.TransactionBackend) error {
		_, err := tx.Get(ctx, "system/control")
		return err
	}); err != nil {
		t.Fatalf("RunTransaction: %v", err)
	}
	afterTx := b.TxStats()
	if afterTx.Total != before.Total+1 || afterTx.Attempts < before.Attempts+1 {
		t.Fatalf("RunTransaction TxStats = %+v, want Total=%d Attempts>=%d", afterTx, before.Total+1, before.Attempts+1)
	}

	url := os.Getenv("NORI_TEST_POSTGRES_URL")
	customB, err := OpenBackendWithOptions(ctx, url, Options{
		MaxConns:         6,
		MinConns:         1,
		StatementTimeout: 4 * time.Second,
	})
	if err != nil {
		t.Fatalf("OpenBackendWithOptions: %v", err)
	}
	defer customB.Close()
	customPG := customB.(*backend)
	if got := customPG.PoolStats().MaxConns; got != 6 {
		t.Fatalf("custom MaxConns = %d, want 6", got)
	}
	var customTimeout string
	if err := customPG.pool.QueryRow(ctx, "SHOW statement_timeout").Scan(&customTimeout); err != nil {
		t.Fatalf("custom SHOW statement_timeout: %v", err)
	}
	if customTimeout != "4s" {
		t.Fatalf("custom SHOW statement_timeout = %q, want %q", customTimeout, "4s")
	}
}

func TestPostgresTryAdvisoryLock(t *testing.T) {
	b, _ := openTestBackend(t)
	ctx := context.Background()
	lockKey := time.Now().UnixNano()

	unlock1, acquired1, err := b.TryAdvisoryLock(ctx, lockKey)
	if err != nil || !acquired1 || unlock1 == nil {
		t.Fatalf("first TryAdvisoryLock = (unlock=%v, acquired=%v, err=%v), want acquired=true", unlock1 != nil, acquired1, err)
	}

	unlock2, acquired2, err := b.TryAdvisoryLock(ctx, lockKey)
	if err != nil || acquired2 || unlock2 != nil {
		t.Fatalf("competing TryAdvisoryLock = (unlock=%v, acquired=%v, err=%v), want acquired=false", unlock2 != nil, acquired2, err)
	}

	unlock1()
	unlock1()

	unlock3, acquired3, err := b.TryAdvisoryLock(ctx, lockKey)
	if err != nil || !acquired3 || unlock3 == nil {
		t.Fatalf("re-acquire TryAdvisoryLock = (unlock=%v, acquired=%v, err=%v), want acquired=true", unlock3 != nil, acquired3, err)
	}
	unlock3()
}

func TestPostgresStandaloneApplyConcurrencyAndWatchNotify(t *testing.T) {
	b, prefix := openTestBackend(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	mergePath := "owners/" + prefix + "u1/runs/merge_race"
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			key := fmt.Sprintf("k%d", i)
			if err := b.Apply(ctx, docstore.Mutation{
				Kind:   "set",
				Path:   mergePath,
				Merge:  true,
				Fields: map[string]any{key: i},
			}); err != nil {
				t.Errorf("concurrent MergeAll %d: %v", i, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	rec, err := b.Get(ctx, mergePath)
	if err != nil {
		t.Fatalf("Get merged doc: %v", err)
	}
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("k%d", i)
		if got, ok := rec.Fields[key].(int64); !ok || got != int64(i) {
			t.Fatalf("merged doc missing %s=%d: got %#v", key, i, rec.Fields)
		}
	}

	conn, err := pgx.Connect(ctx, b.listenURL)
	if err != nil {
		t.Fatalf("pgx.Connect listen: %v", err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+watchChannel); err != nil {
		t.Fatalf("LISTEN %s: %v", watchChannel, err)
	}

	eventsCol := "owners/" + prefix + "u1/events"
	if err := b.Apply(ctx, docstore.Mutation{
		Kind:   "create",
		Path:   eventsCol + "/00000000000000000001",
		Fields: map[string]any{"seq": int64(1), "kind": "task"},
	}); err != nil {
		t.Fatalf("standalone Apply event: %v", err)
	}

	waitNotify := func(wantPayload string) {
		t.Helper()
		waitCtx, waitCancel := context.WithTimeout(ctx, 3*time.Second)
		defer waitCancel()
		for {
			n, err := conn.WaitForNotification(waitCtx)
			if err != nil {
				t.Fatalf("WaitForNotification(%q): %v", wantPayload, err)
			}
			if n.Channel == watchChannel && n.Payload == wantPayload {
				return
			}
		}
	}
	waitNotify(eventsCol)

	seqCol := "owners/" + prefix + "u1/custom_watch"
	if err := b.RunTransaction(ctx, false, 5, func(tx docstore.TransactionBackend) error {
		return tx.Apply(ctx, docstore.Mutation{
			Kind:   "create",
			Path:   seqCol + "/d1",
			Fields: map[string]any{"seq": int64(1)},
		})
	}); err != nil {
		t.Fatalf("tx Apply seq doc: %v", err)
	}
	waitNotify(seqCol)
}

func TestPostgresMigration004IndexesAndPartitionedSequences(t *testing.T) {
	b, prefix := openTestBackend(t)
	ctx := context.Background()

	for _, dead := range []string{
		"nori_users_email",
		"nori_records_attention",
		"nori_records_message_sequence",
		"nori_records_index_gin",
	} {
		var exists bool
		if err := b.pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1)", dead,
		).Scan(&exists); err != nil {
			t.Fatalf("check dead index %s: %v", dead, err)
		}
		if exists {
			t.Errorf("dead index %s still exists after migration 004", dead)
		}
	}

	for idxName, wantSubstr := range map[string]string{
		"nori_records_sequence":            "idx_seq IS NOT NULL",
		"nori_records_created":             "idx_created_at IS NOT NULL",
		"nori_records_started":             "idx_started_at IS NOT NULL",
		"nori_records_position":            "idx_position IS NOT NULL",
		"nori_records_status":              "running",
		"nori_records_run_id":              "idx_run_id IS NOT NULL",
		"nori_records_results_run_id":      "idx_run_id IS NOT NULL",
		"nori_records_index_status":        "idx_index_status IS NOT NULL",
		"nori_records_index_task_id":       "idx_index_task_id IS NOT NULL",
		"nori_records_undelivered":         "idx_delivered",
		"nori_records_cleanup_pending":     "idx_cleanup_pending",
		"nori_records_publication_pending": "idx_publication_pending",
		"nori_users_email_jsonb":           "idx_email IS NOT NULL",
	} {
		var def string
		if err := b.pool.QueryRow(ctx,
			"SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = $1", idxName,
		).Scan(&def); err != nil {
			t.Fatalf("lookup index %s: %v", idxName, err)
		}
		if !strings.Contains(def, "WHERE") || !strings.Contains(def, wantSubstr) {
			t.Errorf("index %s def = %q, want WHERE containing %q", idxName, def, wantSubstr)
		}
		if strings.Contains(def, "data ->") || strings.Contains(def, "data->") || strings.Contains(def, "data #>") || strings.Contains(def, "data#>") {
			t.Errorf("index %s still references raw data column (blocks HOT updates): %q", idxName, def)
		}
	}

	hotCol := "owners/" + prefix + "u1/runs"
	if err := b.Apply(ctx, docstore.Mutation{Kind: "set", Path: hotCol + "/r1", Fields: map[string]any{"status": "running", "started_at": "2026-11-22T00:00:00Z", "progress": "a"}}); err != nil {
		t.Fatalf("seed r1: %v", err)
	}
	if err := b.Apply(ctx, docstore.Mutation{Kind: "set", Path: hotCol + "/r2", Fields: map[string]any{"status": "running", "started_at": "2026-11-22T00:00:00Z", "progress": "b"}}); err != nil {
		t.Fatalf("seed r2: %v", err)
	}
	tx1, err := b.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	defer tx1.Rollback(ctx)
	tx2, err := b.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("begin tx2: %v", err)
	}
	defer tx2.Rollback(ctx)
	_, _ = tx1.Exec(ctx, "SET LOCAL enable_seqscan = off")
	_, _ = tx2.Exec(ctx, "SET LOCAL enable_seqscan = off")
	t1 := &transaction{q: tx1}
	t2 := &transaction{q: tx2}
	if _, err := t1.Get(ctx, hotCol+"/r1"); err != nil {
		t.Fatalf("t1.Get: %v", err)
	}
	if _, err := t2.Get(ctx, hotCol+"/r2"); err != nil {
		t.Fatalf("t2.Get: %v", err)
	}
	if err := t1.Apply(ctx, docstore.Mutation{Kind: "update", Path: hotCol + "/r1", Updates: []docstore.Update{{Path: "progress", Value: "a2"}}}); err != nil {
		t.Fatalf("t1.Apply: %v", err)
	}
	if err := t2.Apply(ctx, docstore.Mutation{Kind: "update", Path: hotCol + "/r2", Updates: []docstore.Update{{Path: "progress", Value: "b2"}}}); err != nil {
		t.Fatalf("t2.Apply: %v", err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("tx1.Commit: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("tx2.Commit (expected HOT update with zero leaf-page 40001): %v", err)
	}

	for _, tc := range []struct {
		table string
		want  int
	}{
		{"nori_records", 3},
		{"nori_records_owners", 32},
		{"nori_sequences", 32},
	} {
		var relkind string
		if err := b.pool.QueryRow(ctx, `
			SELECT c.relkind::text
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema() AND c.relname = $1
		`, tc.table).Scan(&relkind); err != nil {
			t.Fatalf("check %s relkind: %v", tc.table, err)
		}
		if relkind != "p" {
			t.Fatalf("%s relkind = %q, want 'p' (partitioned)", tc.table, relkind)
		}
		var partCount int
		if err := b.pool.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_inherits i
			JOIN pg_class parent ON parent.oid = i.inhparent
			JOIN pg_namespace n ON n.oid = parent.relnamespace
			WHERE n.nspname = current_schema() AND parent.relname = $1
		`, tc.table).Scan(&partCount); err != nil {
			t.Fatalf("count %s partitions: %v", tc.table, err)
		}
		if partCount != tc.want {
			t.Fatalf("%s partitions = %d, want %d", tc.table, partCount, tc.want)
		}
	}

	seqPath := "owners/" + prefix + "u1/event_head/head"
	t.Cleanup(func() {
		_, _ = b.pool.Exec(context.Background(), "DELETE FROM nori_sequences WHERE path = $1", seqPath)
	})
	var start int64
	if err := b.RunTransaction(ctx, false, 5, func(tx docstore.TransactionBackend) error {
		var err error
		start, err = tx.(docstore.Sequencer).NextSequence(ctx, seqPath, 3)
		return err
	}); err != nil {
		t.Fatalf("NextSequence: %v", err)
	}
	if start != 1 {
		t.Fatalf("first NextSequence(3) = %d, want 1", start)
	}
	cur, err := b.CurrentSequence(ctx, seqPath)
	if err != nil || cur != 3 {
		t.Fatalf("CurrentSequence = (%d, %v), want (3, nil)", cur, err)
	}
}

func TestParseListenConfigStripsPoolAndStatementTimeout(t *testing.T) {
	raw := "postgres://nori@127.0.0.1:15432/nori?sslmode=disable&pool_max_conns=40&pool_min_conns=4&statement_timeout=30000&application_name=nori-listen-test"
	cfg, err := parseListenConfig(raw)
	if err != nil {
		t.Fatalf("parseListenConfig: %v", err)
	}
	for _, forbidden := range []string{"pool_max_conns", "pool_min_conns", "statement_timeout"} {
		if v, exists := cfg.RuntimeParams[forbidden]; exists {
			t.Fatalf("RuntimeParams[%q] = %q, want stripped for LISTEN connection", forbidden, v)
		}
	}
	if got := cfg.RuntimeParams["application_name"]; got != "nori-listen-test" {
		t.Fatalf("RuntimeParams[application_name] = %q, want nori-listen-test", got)
	}
}

func TestPostgresListenWithPoolParamsAndApplyMergeUniqueViolation(t *testing.T) {
	url := os.Getenv("NORI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("NORI_TEST_POSTGRES_URL not set")
	}
	ctx := context.Background()
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pooledURL := url + sep + "pool_max_conns=12&pool_min_conns=1&statement_timeout=15000"

	conn, err := connectListen(ctx, pooledURL)
	if err != nil {
		t.Fatalf("connectListen with pool_* params: %v", err)
	}
	if _, err := conn.Exec(ctx, "LISTEN "+docstore.WakeChannel); err != nil {
		closeConn(conn)
		t.Fatalf("LISTEN on connectListen conn: %v", err)
	}
	closeConn(conn)

	b, prefix := openTestBackend(t)
	email := prefix + "dup@example.test"
	u1 := "users/" + prefix + "u1"
	u2 := "users/" + prefix + "u2"
	t.Cleanup(func() {
		_, _ = b.pool.Exec(context.Background(), "DELETE FROM nori_records WHERE shard='users' AND collection='users' AND id IN ($1,$2)", prefix+"u1", prefix+"u2")
	})
	if err := b.Apply(ctx, docstore.Mutation{Kind: "create", Path: u1, Fields: map[string]any{"email": email}}); err != nil {
		t.Fatalf("create u1: %v", err)
	}
	if err := b.Apply(ctx, docstore.Mutation{Kind: "set", Merge: true, Path: u2, Fields: map[string]any{"email": email}}); !errors.Is(err, docstore.ErrExists) {
		t.Fatalf("merge insert duplicate email: got %v, want docstore.ErrExists", err)
	}
	if err := b.Apply(ctx, docstore.Mutation{Kind: "create", Path: u2, Fields: map[string]any{"email": prefix + "other@example.test"}}); err != nil {
		t.Fatalf("create u2: %v", err)
	}
	if err := b.Apply(ctx, docstore.Mutation{Kind: "update", Path: u2, Updates: []docstore.Update{{Path: "email", Value: email}}}); !errors.Is(err, docstore.ErrExists) {
		t.Fatalf("update duplicate email: got %v, want docstore.ErrExists", err)
	}
}

func TestPostgresMigrationLedgerUpgradeAndDrift(t *testing.T) {
	b := openIsolatedTestBackend(t)
	ctx := context.Background()
	var count int
	if err := b.pool.QueryRow(ctx, "SELECT count(*) FROM nori_migrations WHERE length(checksum)=64").Scan(&count); err != nil || count == 0 {
		t.Fatalf("fresh ledger has %d checksums, error %v", count, err)
	}
	if _, err := b.pool.Exec(ctx, "ALTER TABLE nori_migrations DROP COLUMN checksum"); err != nil {
		t.Fatal(err)
	}
	if err := b.migrate(ctx); err != nil {
		t.Fatalf("upgrade legacy ledger: %v", err)
	}
	var upgraded int
	if err := b.pool.QueryRow(ctx, "SELECT count(*) FROM nori_migrations WHERE length(checksum)=64").Scan(&upgraded); err != nil || upgraded != count {
		t.Fatalf("upgraded ledger has %d checksums, want %d, error %v", upgraded, count, err)
	}
	if _, err := b.pool.Exec(ctx, "UPDATE nori_migrations SET checksum='changed' WHERE name='001_records.sql'"); err != nil {
		t.Fatal(err)
	}
	if err := b.migrate(ctx); err == nil || !strings.Contains(err.Error(), "001_records.sql: applied migration checksum differs") {
		t.Fatalf("migrate with drift = %v", err)
	}
}

func TestPostgresUsageWindowBoundaries(t *testing.T) {
	b, prefix := openTestBackend(t)
	ctx := context.Background()
	collection := "owners/" + prefix + "usage/usage"
	hour := time.Date(2026, 11, 25, 6, 0, 0, 0, time.FixedZone("PST", -8*60*60))
	day := hour.Add(-23*time.Hour + 123*time.Millisecond)
	rows := []struct {
		at     time.Time
		fields map[string]any
	}{
		{day.Add(-time.Nanosecond), map[string]any{"cost_usd": 64}},
		{day, map[string]any{"cost_usd": 1.5}},
		{hour.Add(-time.Nanosecond), map[string]any{"estimated_exposure_usd": .75}},
		{hour, map[string]any{"estimated_exposure_usd": -1.0}},
		{hour, map[string]any{"cost_status": "calculated", "cost_usd": 0}},
		{hour, map[string]any{"request_status": "not_sent", "cost_usd": 64}},
	}
	for i, row := range rows {
		row.fields["created_at"] = row.at
		if err := b.Apply(ctx, docstore.Mutation{Kind: "set", Path: fmt.Sprintf("%s/%d", collection, i), Fields: row.fields}); err != nil {
			t.Fatal(err)
		}
	}
	want := docstore.UsageWindow{Calls: 2, ActualUSD: 1.5, ExposureUSD: 1, Unknown: 2}
	window, err := b.ReadUsageWindow(ctx, collection, hour, day, .25)
	if err != nil || window != want {
		t.Fatalf("window = %+v, %v; want %+v", window, err, want)
	}
}

func TestPostgresHomeUsageKeepsCloudBudgetAvailable(t *testing.T) {
	b := openIsolatedTestBackend(t)
	ctx := context.Background()
	user := users.User{ID: users.LegacyID}
	db := records.NewShared(b, user, 0)
	now := time.Now().UTC()
	meter := usage.WithAdmission(db, db, 3, .02, func() time.Time { return now })
	local := state.Usage{ID: "home-pending", Provider: "home", CostStatus: "local", RequestStatus: "pending", CreatedAt: now}
	if err := meter.RecordUsage(ctx, local); err != nil {
		t.Fatal(err)
	}
	local.ID, local.RequestStatus = "home-complete", "sent"
	if err := usage.Finish(ctx, meter, local); err != nil {
		t.Fatal(err)
	}
	collection := "owners/" + user.DataOwner() + "/model_usage"
	window, err := b.ReadUsageWindow(ctx, collection, now.Add(-time.Hour), now.Add(-24*time.Hour), .02)
	if err != nil || window != (docstore.UsageWindow{Calls: 2}) {
		t.Fatalf("home usage window = %+v, %v", window, err)
	}
	cloud := state.Usage{ID: "cloud-pending", Provider: "openai", CostStatus: "unknown", RequestStatus: "pending", CreatedAt: now}
	if err := meter.RecordUsage(ctx, cloud); err != nil {
		t.Fatalf("home usage blocked cloud budget: %v", err)
	}
	local.ID, local.RequestStatus = "home-over-limit", "pending"
	if err := meter.RecordUsage(ctx, local); !errors.Is(err, usage.ErrUserQuotaExceeded) {
		t.Fatalf("home usage bypassed hourly rate limit: %v", err)
	}
}

func TestPostgresMemoryLogsPreserveDaysAcrossPages(t *testing.T) {
	b := openIsolatedTestBackend(t)
	ctx := context.Background()
	user := users.User{ID: users.LegacyID}
	store := docstore.New(b).Collection("owners").Doc(user.DataOwner()).Collection("memory_logs")
	for day, count := range map[string]int{"2026-11-24": 67, "2026-11-23": 3, "2026-11-22": 1} {
		for shard := 1; shard <= count; shard++ {
			id := fmt.Sprintf("%s#%04d", day, shard)
			log := state.MemoryLog{
				Date: day, Shard: shard, Entries: []state.MemoryLogEntry{{ID: id, Text: "fixture entry"}},
			}
			if _, err := store.Doc(id).Set(ctx, log); err != nil {
				t.Fatal(err)
			}
		}
	}
	db := records.NewShared(b, user, 0)
	logs, err := db.MemoryLogs(ctx, "", 2)
	if err != nil || len(logs) != 2 || logs[0].Date != "2026-11-24" || len(logs[0].Entries) != 67 || logs[1].Date != "2026-11-23" || len(logs[1].Entries) != 3 {
		t.Fatalf("unexpected grouped days: days=%d, err=%v", len(logs), err)
	}
	if logs[0].Entries[0].ID != "2026-11-24#0001" || logs[0].Entries[66].ID != "2026-11-24#0067" {
		t.Fatal("newest day's entries are out of shard order")
	}
	logs, err = db.MemoryLogs(ctx, "2026-11-24", 0)
	if err != nil || len(logs) != 1 || len(logs[0].Entries) != 67 {
		t.Fatalf("inclusive cutoff returned %d days, err=%v", len(logs), err)
	}
}

func BenchmarkPostgresUsageWindow(b *testing.B) {
	db, prefix := openTestBackend(b)
	ctx := context.Background()
	collection := "owners/" + prefix + "usage-bench/model_usage"
	hour := time.Date(2026, 11, 25, 23, 0, 0, 0, time.UTC)
	day := hour.Add(-23 * time.Hour)
	if err := db.RunTransaction(ctx, false, 1, func(tx docstore.TransactionBackend) error {
		for i := 0; i < 5000; i++ {
			row := docstore.Mutation{Kind: "set", Path: fmt.Sprintf("%s/%d", collection, i), Fields: map[string]any{
				"created_at": day.Add(time.Duration(i) * 20 * time.Second), "cost_usd": .125, "cost_status": "provider",
			}}
			if err := tx.Apply(ctx, row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	b.Run("fetch_and_decode", func(b *testing.B) {
		q := docstore.QuerySpec{Collection: collection, Filters: []docstore.Filter{{Field: "created_at", Op: ">=", Value: day}}}
		b.ReportAllocs()
		for b.Loop() {
			rows, err := db.Query(ctx, q)
			if err != nil || len(rows) != 5000 {
				b.Fatalf("got %d usage rows, error %v", len(rows), err)
			}
			total := 0.0
			for _, row := range rows {
				var entry state.Usage
				if err := docstore.Decode(row.Fields, &entry); err != nil {
					b.Fatal(err)
				}
				total += *entry.CostUSD
			}
			if total != 625 {
				b.Fatalf("got %v total USD, want 625", total)
			}
		}
	})
	b.Run("aggregate", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			window, err := db.ReadUsageWindow(ctx, collection, hour, day, .25)
			if err != nil || window.ActualUSD != 625 {
				b.Fatalf("unexpected usage summary %+v, error %v", window, err)
			}
		}
	})
}

func BenchmarkPostgresMemoryLogs(b *testing.B) {
	db, prefix := openTestBackend(b)
	ctx := context.Background()
	user := users.User{ID: users.LegacyID, Email: prefix + "memory@example.test"}
	collection := "owners/" + user.DataOwner() + "/memory_logs"
	today := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	if err := db.RunTransaction(ctx, false, 1, func(tx docstore.TransactionBackend) error {
		for offset := range 730 {
			date := today.AddDate(0, 0, -offset).Format(time.DateOnly)
			for shard := range 3 {
				id := fmt.Sprintf("%s#%04d", date, shard+1)
				fields, err := docstore.Encode(state.MemoryLog{
					Date: date, Shard: shard + 1, Entries: []state.MemoryLogEntry{{ID: id, Text: "Fixture memory"}},
				})
				if err != nil {
					return err
				}
				if err := tx.Apply(ctx, docstore.Mutation{Kind: "set", Path: collection + "/" + id, Fields: fields}); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	store := records.NewShared(db, user, 0)
	b.Run("whole_history", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			days, err := store.MemoryLogs(ctx, "", 0)
			if err != nil || len(days) != 730 {
				b.Fatalf("got %d days, error %v", len(days), err)
			}
		}
	})
	b.Run("latest_seven_days", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			days, err := store.MemoryLogs(ctx, "", 7)
			if err != nil || len(days) != 7 {
				b.Fatalf("got %d days, error %v", len(days), err)
			}
		}
	})
}

func TestOwnerFromCollection(t *testing.T) {
	cases := []struct {
		collection string
		want       string
	}{
		{collection: "owners/u1/tasks", want: "u1"},
		{collection: "owners/u1/tasks/r1/events", want: "u1"},
		{collection: "owners/user-abc-123/memories", want: "user-abc-123"},
		{collection: "owners/u1", want: "u1"},
		{collection: "owners/", want: ""},
		{collection: "owners//tasks", want: ""},
		{collection: "owners", want: ""},
		{collection: "system", want: ""},
		{collection: "users", want: ""},
		{collection: "other/u1/tasks", want: ""},
		{collection: "", want: ""},
	}
	for _, tc := range cases {
		if got := OwnerFromCollection(tc.collection); got != tc.want {
			t.Errorf("OwnerFromCollection(%q) = %q, want %q", tc.collection, got, tc.want)
		}
	}
}

func TestTenantRLSMigrationEmbedded(t *testing.T) {
	raw, err := migrations.ReadFile("migrations/006_tenant_rls.sql")
	if err != nil {
		t.Fatalf("read 006_tenant_rls.sql: %v", err)
	}
	sql := string(raw)
	for _, want := range []string{
		"ALTER TABLE nori_records",
		"ALTER TABLE nori_task_leases",
		"ALTER TABLE nori_user_wakes",
		"GENERATED ALWAYS AS",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"app.current_owner",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("006_tenant_rls.sql missing %q", want)
		}
	}
}
