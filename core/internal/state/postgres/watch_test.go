package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/state/docstore"
)

type capturedLog struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

type captureLogHandler struct {
	mu      sync.Mutex
	records []capturedLog
}

func (h *captureLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureLogHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, capturedLog{
		level: r.Level,
		msg:   r.Message,
		attrs: attrs,
	})
	h.mu.Unlock()
	return nil
}
func (h *captureLogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureLogHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureLogHandler) snapshot() []capturedLog {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.records)
}

type traceRecordsKey struct{}

type sqlQueryCounter struct {
	recordsSelects     atomic.Int64
	recordsSelectsDone atomic.Int64
	notifyExecs        atomic.Int64
}

func (c *sqlQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	sql := data.SQL
	if strings.Contains(sql, "FROM nori_records") {
		c.recordsSelects.Add(1)
		ctx = context.WithValue(ctx, traceRecordsKey{}, true)
	}
	if strings.Contains(sql, "pg_notify(") {
		c.notifyExecs.Add(1)
	}
	return ctx
}

func (c *sqlQueryCounter) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if isRecords, _ := ctx.Value(traceRecordsKey{}).(bool); isRecords {
		c.recordsSelectsDone.Add(1)
	}
}

func openTracedTestBackend(t *testing.T, opts Options) (*backend, *sqlQueryCounter, string) {
	t.Helper()
	url := os.Getenv("NORI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("NORI_TEST_POSTGRES_URL not set")
	}
	ctx := context.Background()
	counter := &sqlQueryCounter{}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("pool.Ping: %v", err)
	}
	listenURL := strings.TrimSpace(opts.ListenURL)
	if listenURL == "" {
		listenURL = url
	}
	b := &backend{
		pool:      pool,
		connURL:   url,
		listenURL: listenURL,
	}
	if err := b.migrate(ctx); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	counter.recordsSelects.Store(0)
	counter.recordsSelectsDone.Store(0)
	counter.notifyExecs.Store(0)
	prefix := fmt.Sprintf("wtest-%d-", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = b.pool.Exec(cleanCtx, "DELETE FROM nori_records WHERE collection LIKE $1 OR collection LIKE $2", prefix+"%", "owners/"+prefix+"%")
		_, _ = b.pool.Exec(cleanCtx, "DELETE FROM nori_task_leases WHERE user_id LIKE $1", prefix+"%")
		_, _ = b.pool.Exec(cleanCtx, "DELETE FROM nori_user_wakes WHERE user_id LIKE $1", prefix+"%")
		_ = b.Close()
	})
	return b, counter, prefix
}

func countListenConnections(t *testing.T, b *backend, channel string) int {
	t.Helper()
	var n int
	err := b.pool.QueryRow(context.Background(), `
		SELECT COUNT(*)
		FROM pg_stat_activity
		WHERE datname = current_database()
		  AND pid <> pg_backend_pid()
		  AND query ILIKE $1
	`, "LISTEN "+channel+"%").Scan(&n)
	if err != nil {
		t.Fatalf("query pg_stat_activity for LISTEN %s: %v", channel, err)
	}
	return n
}

func terminateListenConnections(t *testing.T, b *backend, channel string) int {
	t.Helper()
	var terminated int
	err := b.pool.QueryRow(context.Background(), `
		SELECT COUNT(*)
		FROM (
			SELECT pg_terminate_backend(pid)
			FROM pg_stat_activity
			WHERE datname = current_database()
			  AND pid <> pg_backend_pid()
			  AND query ILIKE $1
		) s
	`, "LISTEN "+channel+"%").Scan(&terminated)
	if err != nil {
		t.Fatalf("terminate LISTEN %s connections: %v", channel, err)
	}
	return terminated
}

func TestLogLimiterRateLimitingAndRecovery(t *testing.T) {
	lim := newLogLimiter(30 * time.Second)
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if wasFailing, suppressed := lim.recover(); wasFailing || suppressed != 0 {
		t.Fatalf("initial recover = (%v, %d), want (false, 0)", wasFailing, suppressed)
	}

	if emit, suppressed := lim.allowWarn(t0); !emit || suppressed != 0 {
		t.Fatalf("first allowWarn = (%v, %d), want (true, 0)", emit, suppressed)
	}

	for i := 1; i <= 50; i++ {
		if emit, suppressed := lim.allowWarn(t0.Add(time.Duration(i) * 500 * time.Millisecond)); emit || suppressed != 0 {
			t.Fatalf("suppressed allowWarn %d = (%v, %d), want (false, 0)", i, emit, suppressed)
		}
	}

	if emit, suppressed := lim.allowWarn(t0.Add(30 * time.Second)); !emit || suppressed != 50 {
		t.Fatalf("post-interval allowWarn = (%v, %d), want (true, 50)", emit, suppressed)
	}

	for i := 0; i < 3; i++ {
		if emit, _ := lim.allowWarn(t0.Add(35 * time.Second)); emit {
			t.Fatalf("expected suppression during second interval")
		}
	}
	if wasFailing, suppressed := lim.recover(); !wasFailing || suppressed != 3 {
		t.Fatalf("recover = (%v, %d), want (true, 3)", wasFailing, suppressed)
	}

	if emit, suppressed := lim.allowWarn(t0.Add(36 * time.Second)); !emit || suppressed != 0 {
		t.Fatalf("new episode allowWarn = (%v, %d), want (true, 0)", emit, suppressed)
	}
}

func TestDedupeWakeEvents(t *testing.T) {
	ch, pl := dedupeWakeEvents([]docstore.WakeEvent{
		{Channel: "", Payload: "skip-empty-ch"},
		{Channel: docstore.WakeChannel, Payload: ""},
		{Channel: docstore.WakeChannel, Payload: "t:u1:r1"},
		{Channel: watchChannel, Payload: "owners/u1/events"},
		{Channel: docstore.WakeChannel, Payload: "t:u1:r1"},
		{Channel: watchChannel, Payload: "owners/u1/events"},
		{Channel: docstore.WakeChannel, Payload: "c:u1"},
	})
	wantCh := []string{docstore.WakeChannel, watchChannel, docstore.WakeChannel}
	wantPl := []string{"t:u1:r1", "owners/u1/events", "c:u1"}
	if !slices.Equal(ch, wantCh) || !slices.Equal(pl, wantPl) {
		t.Fatalf("dedupeWakeEvents = (%v, %v), want (%v, %v)", ch, pl, wantCh, wantPl)
	}
}

func TestListenEndpointFallback(t *testing.T) {
	var nilB *backend
	if got := nilB.listenEndpoint(); got != "" {
		t.Fatalf("nil backend listenEndpoint = %q, want empty", got)
	}
	b := &backend{connURL: "postgres://primary", listenURL: "  postgres://direct  "}
	if got := b.listenEndpoint(); got != "postgres://direct" {
		t.Fatalf("explicit listenURL = %q, want postgres://direct", got)
	}
	b2 := &backend{connURL: "postgres://primary", listenURL: "   "}
	if got := b2.listenEndpoint(); got != "postgres://primary" {
		t.Fatalf("fallback listenURL = %q, want postgres://primary", got)
	}
}

func TestWatchAndWakeListenersRateLimitErrorLogsAndCountErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("unable to allocate local port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	origInit, origMax := listenInitialBackoff, listenMaxBackoff
	listenInitialBackoff = 10 * time.Millisecond
	listenMaxBackoff = 20 * time.Millisecond
	t.Cleanup(func() {
		listenInitialBackoff = origInit
		listenMaxBackoff = origMax
	})

	handler := &captureLogHandler{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	secret := "super-secret-password-do-not-log"
	badURL := fmt.Sprintf("postgres://nori:%s@%s/nori?sslmode=disable&connect_timeout=1", secret, addr)
	b := &backend{connURL: badURL, listenURL: badURL}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_ = b.ListenWakes(ctx, nil)

	stats := b.ListenStats()
	if stats.WakeErrors < 3 {
		t.Fatalf("WakeErrors = %d, want >= 3", stats.WakeErrors)
	}
	if stats.WakeReconnects != 0 {
		t.Fatalf("WakeReconnects = %d, want 0 (never connected)", stats.WakeReconnects)
	}

	var wakeWarns int
	for _, rec := range handler.snapshot() {
		if rec.level == slog.LevelWarn && strings.Contains(rec.msg, "wake listener") {
			wakeWarns++
		}
		for _, v := range rec.attrs {
			if strings.Contains(fmt.Sprint(v), secret) {
				t.Fatalf("log leaked database password: %+v", rec)
			}
		}
	}
	if wakeWarns != 1 {
		t.Fatalf("wake listener warning count = %d, want 1 (rate-limited across %d errors)", wakeWarns, stats.WakeErrors)
	}

	sub, _, err := b.subscribeWatch("owners/u1/events")
	if err != nil {
		t.Fatalf("subscribeWatch: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for b.ListenStats().WatchErrors < 3 && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
	}
	b.unsubscribeWatch(sub)
	b.closeWatchHub()

	stats = b.ListenStats()
	if stats.WatchErrors < 3 {
		t.Fatalf("WatchErrors = %d, want >= 3", stats.WatchErrors)
	}
	if stats.WatchReconnects != 0 {
		t.Fatalf("WatchReconnects = %d, want 0", stats.WatchReconnects)
	}
	if stats.ActiveWatchers != 0 {
		t.Fatalf("ActiveWatchers = %d, want 0", stats.ActiveWatchers)
	}

	var watchWarns int
	for _, rec := range handler.snapshot() {
		if rec.level == slog.LevelWarn && strings.Contains(rec.msg, "watch listener") {
			watchWarns++
		}
		for _, v := range rec.attrs {
			if strings.Contains(fmt.Sprint(v), secret) {
				t.Fatalf("watch log leaked database password: %+v", rec)
			}
		}
	}
	if watchWarns != 1 {
		t.Fatalf("watch listener warning count = %d, want 1 (rate-limited across %d errors)", watchWarns, stats.WatchErrors)
	}
}

func TestWatchIdleQPSAndSingleListenConnection(t *testing.T) {
	b, counter, prefix := openTracedTestBackend(t, Options{})
	ctx := context.Background()

	const numWatchers = 50
	cols := make([]string, numWatchers)
	for i := 0; i < numWatchers; i++ {
		cols[i] = fmt.Sprintf("%s/u%02d/events", prefix, i)
		if err := b.Apply(ctx, docstore.Mutation{
			Kind:   "create",
			Path:   fmt.Sprintf("%s/%020d", cols[i], 1),
			Fields: map[string]any{"seq": int64(1), "kind": "init"},
		}); err != nil {
			t.Fatalf("seed col %d: %v", i, err)
		}
	}

	counter.recordsSelects.Store(0)

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var initialWG sync.WaitGroup
	initialWG.Add(numWatchers)
	var watchWG sync.WaitGroup
	errs := make(chan error, numWatchers)

	for i := 0; i < numWatchers; i++ {
		col := cols[i]
		watchWG.Add(1)
		go func() {
			defer watchWG.Done()
			var gotInit sync.Once
			spec := docstore.QuerySpec{
				Collection: col,
				Filters:    []docstore.Filter{{Field: "seq", Op: ">", Value: int64(0)}},
				Orders:     []docstore.Order{{Field: "seq", Direction: docstore.Asc}},
			}
			err := b.Watch(watchCtx, spec, func(rows []docstore.Record) error {
				if len(rows) > 0 {
					gotInit.Do(initialWG.Done)
				}
				return nil
			})
			if !errors.Is(err, context.Canceled) {
				errs <- err
			}
		}()
	}

	initDone := make(chan struct{})
	go func() {
		initialWG.Wait()
		close(initDone)
	}()
	select {
	case <-initDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %d watchers to receive initial batch", numWatchers)
	}

	if got := b.ListenStats().ActiveWatchers; got != numWatchers {
		t.Fatalf("ActiveWatchers = %d, want %d", got, numWatchers)
	}
	if listenConns := countListenConnections(t, b, watchChannel); listenConns != 1 {
		t.Fatalf("LISTEN %s connections = %d, want 1 across %d watchers", watchChannel, listenConns, numWatchers)
	}

	queriesAfterInit := counter.recordsSelects.Load()
	if queriesAfterInit != numWatchers {
		t.Fatalf("initial nori_records queries = %d, want exactly %d (1 per watcher)", queriesAfterInit, numWatchers)
	}

	time.Sleep(2 * time.Second)
	queriesAfterIdle := counter.recordsSelects.Load()
	idleQueries := queriesAfterIdle - queriesAfterInit
	if idleQueries != 0 {
		t.Fatalf("idle nori_records queries during 2s across %d watchers = %d (total=%d), want 0", numWatchers, idleQueries, queriesAfterIdle)
	}
	t.Logf("MEASURED idle SSE proof: watchers=%d listen_conns=1 initial_queries=%d idle_2s_queries=%d (old 250ms baseline: ~400)",
		numWatchers, queriesAfterInit, idleQueries)

	cancel()
	watchWG.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("watcher exited with unexpected error: %v", err)
	}
	if got := b.ListenStats().ActiveWatchers; got != 0 {
		t.Fatalf("ActiveWatchers after cancel = %d, want 0", got)
	}
}

func TestWatchLiveNotifyLatencyFanoutAndDrain(t *testing.T) {
	b, counter, prefix := openTracedTestBackend(t, Options{})
	ctx := context.Background()

	targetCol := prefix + "/target/events"
	const idleOthers = 15

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var otherWG sync.WaitGroup
	for i := 0; i < idleOthers; i++ {
		col := fmt.Sprintf("%s/other-%02d/events", prefix, i)
		otherWG.Add(1)
		go func() {
			defer otherWG.Done()
			_ = b.Watch(watchCtx, docstore.QuerySpec{
				Collection: col,
				Filters:    []docstore.Filter{{Field: "seq", Op: ">", Value: int64(0)}},
				Orders:     []docstore.Order{{Field: "seq", Direction: docstore.Asc}},
			}, func([]docstore.Record) error { return nil })
		}()
	}

	type watcherState struct {
		mu   sync.Mutex
		seqs []int64
		ping chan time.Time
	}
	watchers := [2]*watcherState{
		{ping: make(chan time.Time, 32)},
		{ping: make(chan time.Time, 32)},
	}
	var targetWG sync.WaitGroup
	for idx := range watchers {
		ws := watchers[idx]
		targetWG.Add(1)
		go func() {
			defer targetWG.Done()
			_ = b.Watch(watchCtx, docstore.QuerySpec{
				Collection: targetCol,
				Filters:    []docstore.Filter{{Field: "seq", Op: ">", Value: int64(0)}},
				Orders:     []docstore.Order{{Field: "seq", Direction: docstore.Asc}},
			}, func(rows []docstore.Record) error {
				now := time.Now()
				ws.mu.Lock()
				for _, r := range rows {
					ws.seqs = append(ws.seqs, r.Fields["seq"].(int64))
				}
				ws.mu.Unlock()
				select {
				case ws.ping <- now:
				default:
				}
				return nil
			})
		}()
	}

	deadline := time.Now().Add(3 * time.Second)
	for (b.ListenStats().ActiveWatchers < idleOthers+2 || counter.recordsSelectsDone.Load() < idleOthers+2) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := b.ListenStats().ActiveWatchers; got != idleOthers+2 {
		t.Fatalf("ActiveWatchers = %d, want %d", got, idleOthers+2)
	}
	if done := counter.recordsSelectsDone.Load(); done != idleOthers+2 {
		t.Fatalf("initial completed queries = %d, want %d", done, idleOthers+2)
	}

	queriesBefore := counter.recordsSelects.Load()

	sentAt := time.Now()
	if err := b.Apply(ctx, docstore.Mutation{
		Kind:   "create",
		Path:   fmt.Sprintf("%s/%020d", targetCol, 1),
		Fields: map[string]any{"seq": int64(1), "kind": "live"},
	}); err != nil {
		t.Fatalf("insert seq 1: %v", err)
	}

	var maxLatency time.Duration
	for idx, ws := range watchers {
		select {
		case gotAt := <-ws.ping:
			lat := gotAt.Sub(sentAt)
			if lat > maxLatency {
				maxLatency = lat
			}
			if lat > 500*time.Millisecond {
				t.Fatalf("watcher %d wake latency = %v, want < 500ms (notify-driven, far below the %v fallback poll)", idx, lat, watchFallbackInterval)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("watcher %d timed out waiting for live notification", idx)
		}
	}

	time.Sleep(50 * time.Millisecond)
	if delta := counter.recordsSelects.Load() - queriesBefore; delta != 2 {
		t.Fatalf("nori_records queries on single-collection notify = %d, want 2 (15 unrelated watchers must stay idle)", delta)
	}

	err := b.RunTransaction(ctx, false, 3, func(tb docstore.TransactionBackend) error {
		for i := int64(2); i <= 231; i++ {
			if err := tb.Apply(ctx, docstore.Mutation{
				Kind:   "create",
				Path:   fmt.Sprintf("%s/%020d", targetCol, i),
				Fields: map[string]any{"seq": i, "kind": "bulk"},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("bulk insert 230 rows: %v", err)
	}
	if _, err := b.pool.Exec(ctx, "SELECT pg_notify($1, $2)", watchChannel, targetCol); err != nil {
		t.Fatalf("raw pg_notify: %v", err)
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		allDone := true
		for _, ws := range watchers {
			ws.mu.Lock()
			n := len(ws.seqs)
			ws.mu.Unlock()
			if n < 231 {
				allDone = false
			}
		}
		if allDone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	for idx, ws := range watchers {
		ws.mu.Lock()
		got := slices.Clone(ws.seqs)
		ws.mu.Unlock()
		if len(got) != 231 {
			t.Fatalf("watcher %d delivered %d events, want 231", idx, len(got))
		}
		for i := 0; i < 231; i++ {
			if got[i] != int64(i+1) {
				t.Fatalf("watcher %d seq[%d] = %d, want %d", idx, i, got[i], i+1)
			}
		}
	}
	t.Logf("MEASURED live nori_watch wake latency: max=%v across 2 fan-out watchers (15 unrelated watchers stayed at 0 queries)", maxLatency)

	cancel()
	targetWG.Wait()
	otherWG.Wait()
}

func TestWatchHubReconnectBroadcastsAndCountsMetrics(t *testing.T) {
	origInit, origMax := listenInitialBackoff, listenMaxBackoff
	listenInitialBackoff = 20 * time.Millisecond
	listenMaxBackoff = 100 * time.Millisecond
	t.Cleanup(func() {
		listenInitialBackoff = origInit
		listenMaxBackoff = origMax
	})

	b, _, prefix := openTracedTestBackend(t, Options{})
	ctx := context.Background()
	col := prefix + "/reconnect/events"

	if err := b.Apply(ctx, docstore.Mutation{
		Kind:   "create",
		Path:   fmt.Sprintf("%s/%020d", col, 1),
		Fields: map[string]any{"seq": int64(1), "kind": "init"},
	}); err != nil {
		t.Fatalf("seed seq 1: %v", err)
	}

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	gotSeq := make(chan int64, 16)
	done := make(chan error, 1)
	go func() {
		done <- b.Watch(watchCtx, docstore.QuerySpec{
			Collection: col,
			Filters:    []docstore.Filter{{Field: "seq", Op: ">", Value: int64(0)}},
			Orders:     []docstore.Order{{Field: "seq", Direction: docstore.Asc}},
		}, func(rows []docstore.Record) error {
			for _, r := range rows {
				gotSeq <- r.Fields["seq"].(int64)
			}
			return nil
		})
	}()

	select {
	case s := <-gotSeq:
		if s != 1 {
			t.Fatalf("initial seq = %d, want 1", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for initial seq 1")
	}

	if _, err := b.pool.Exec(ctx,
		"INSERT INTO nori_records(shard, collection, id, data) VALUES($1, $2, $3, $4::jsonb)",
		recordShard(col), col, fmt.Sprintf("%020d", 2), `{"seq":2,"kind":"during-disconnect"}`,
	); err != nil {
		t.Fatalf("insert seq 2 without notify: %v", err)
	}

	killed := terminateListenConnections(t, b, watchChannel)
	if killed != 1 {
		t.Fatalf("terminated %d LISTEN %s connections, want 1", killed, watchChannel)
	}

	select {
	case s := <-gotSeq:
		if s != 2 {
			t.Fatalf("post-reconnect seq = %d, want 2", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for reconnect broadcast to deliver seq 2")
	}

	stats := b.ListenStats()
	if stats.WatchErrors < 1 || stats.WatchReconnects < 1 {
		t.Fatalf("ListenStats after reconnect = %+v, want WatchErrors>=1 and WatchReconnects>=1", stats)
	}

	if err := b.Apply(ctx, docstore.Mutation{
		Kind:   "create",
		Path:   fmt.Sprintf("%s/%020d", col, 3),
		Fields: map[string]any{"seq": int64(3), "kind": "after-reconnect"},
	}); err != nil {
		t.Fatalf("insert seq 3: %v", err)
	}

	select {
	case s := <-gotSeq:
		if s != 3 {
			t.Fatalf("post-recovery live seq = %d, want 3", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for live seq 3 after reconnect")
	}

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Watch exit err = %v, want context.Canceled", err)
	}
}

func TestWatchHubIdleTeardownRestartAndSlowClient(t *testing.T) {
	origGrace := watchHubIdleGrace
	watchHubIdleGrace = 80 * time.Millisecond
	t.Cleanup(func() { watchHubIdleGrace = origGrace })

	b, _, prefix := openTracedTestBackend(t, Options{})
	ctx := context.Background()
	col := prefix + "/lifecycle/events"

	w1Ctx, w1Cancel := context.WithCancel(ctx)
	w1Done := make(chan error, 1)
	go func() {
		w1Done <- b.Watch(w1Ctx, docstore.QuerySpec{
			Collection: col,
			Filters:    []docstore.Filter{{Field: "seq", Op: ">", Value: int64(0)}},
			Orders:     []docstore.Order{{Field: "seq", Direction: docstore.Asc}},
		}, func([]docstore.Record) error { return nil })
	}()

	deadline := time.Now().Add(3 * time.Second)
	for countListenConnections(t, b, watchChannel) != 1 && time.Now().Before(deadline) {
		time.Sleep(15 * time.Millisecond)
	}
	if got := countListenConnections(t, b, watchChannel); got != 1 {
		t.Fatalf("LISTEN conns while active = %d, want 1", got)
	}

	w1Cancel()
	<-w1Done

	deadline = time.Now().Add(3 * time.Second)
	for countListenConnections(t, b, watchChannel) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := countListenConnections(t, b, watchChannel); got != 0 {
		t.Fatalf("LISTEN conns after idle grace = %d, want 0", got)
	}

	if err := b.Apply(ctx, docstore.Mutation{
		Kind:   "create",
		Path:   fmt.Sprintf("%s/%020d", col, 1),
		Fields: map[string]any{"seq": int64(1)},
	}); err != nil {
		t.Fatalf("seed seq 1: %v", err)
	}

	w2Ctx, w2Cancel := context.WithCancel(ctx)
	defer w2Cancel()

	slowEntered := make(chan struct{})
	slowRelease := make(chan struct{})
	var slowSeqs []int64
	var slowMu sync.Mutex
	slowDone := make(chan error, 1)
	go func() {
		var blockOnce sync.Once
		slowDone <- b.Watch(w2Ctx, docstore.QuerySpec{
			Collection: col,
			Filters:    []docstore.Filter{{Field: "seq", Op: ">", Value: int64(0)}},
			Orders:     []docstore.Order{{Field: "seq", Direction: docstore.Asc}},
		}, func(rows []docstore.Record) error {
			blockOnce.Do(func() {
				close(slowEntered)
				<-slowRelease
			})
			slowMu.Lock()
			for _, r := range rows {
				slowSeqs = append(slowSeqs, r.Fields["seq"].(int64))
			}
			slowMu.Unlock()
			return nil
		})
	}()

	fastSeq := make(chan int64, 16)
	fastDone := make(chan error, 1)
	go func() {
		fastDone <- b.Watch(w2Ctx, docstore.QuerySpec{
			Collection: col,
			Filters:    []docstore.Filter{{Field: "seq", Op: ">", Value: int64(0)}},
			Orders:     []docstore.Order{{Field: "seq", Direction: docstore.Asc}},
		}, func(rows []docstore.Record) error {
			for _, r := range rows {
				fastSeq <- r.Fields["seq"].(int64)
			}
			return nil
		})
	}()

	<-slowEntered
	if got := <-fastSeq; got != 1 {
		t.Fatalf("fast initial seq = %d, want 1", got)
	}

	for seq := int64(2); seq <= 3; seq++ {
		if err := b.Apply(ctx, docstore.Mutation{
			Kind:   "create",
			Path:   fmt.Sprintf("%s/%020d", col, seq),
			Fields: map[string]any{"seq": seq},
		}); err != nil {
			t.Fatalf("insert seq %d: %v", seq, err)
		}
		b.emitWatchCollections(ctx, []string{col})
		select {
		case got := <-fastSeq:
			if got != seq {
				t.Fatalf("fast seq = %d, want %d", got, seq)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("fast watcher blocked by slow watcher waiting for seq %d", seq)
		}
	}

	close(slowRelease)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		slowMu.Lock()
		n := len(slowSeqs)
		slowMu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	slowMu.Lock()
	gotSlow := slices.Clone(slowSeqs)
	slowMu.Unlock()
	if !slices.Equal(gotSlow, []int64{1, 2, 3}) {
		t.Fatalf("slow watcher seqs = %v, want [1 2 3]", gotSlow)
	}

	w2Cancel()
	<-slowDone
	<-fastDone

	_, _ = b.pool.Exec(ctx, "DELETE FROM nori_records WHERE collection LIKE $1", prefix+"%")
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := b.Watch(ctx, docstore.QuerySpec{Collection: col}, func([]docstore.Record) error { return nil }); err == nil {
		t.Fatalf("Watch on closed backend succeeded, want error")
	}
}

func TestWatchFallbackSafetyNet(t *testing.T) {
	origFallback := watchFallbackInterval
	watchFallbackInterval = 80 * time.Millisecond
	t.Cleanup(func() { watchFallbackInterval = origFallback })

	b, _, prefix := openTracedTestBackend(t, Options{})
	ctx := context.Background()
	col := prefix + "/fallback/events"

	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	gotSeq := make(chan int64, 4)
	done := make(chan error, 1)
	go func() {
		done <- b.Watch(watchCtx, docstore.QuerySpec{
			Collection: col,
			Filters:    []docstore.Filter{{Field: "seq", Op: ">", Value: int64(0)}},
			Orders:     []docstore.Order{{Field: "seq", Direction: docstore.Asc}},
		}, func(rows []docstore.Record) error {
			for _, r := range rows {
				gotSeq <- r.Fields["seq"].(int64)
			}
			return nil
		})
	}()

	time.Sleep(50 * time.Millisecond)
	if err := b.Apply(ctx, docstore.Mutation{
		Kind:   "create",
		Path:   fmt.Sprintf("%s/%020d", col, 1),
		Fields: map[string]any{"seq": int64(1)},
	}); err != nil {
		t.Fatalf("insert seq 1: %v", err)
	}

	select {
	case s := <-gotSeq:
		if s != 1 {
			t.Fatalf("fallback seq = %d, want 1", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("fallback safety net failed to deliver seq 1")
	}

	cancel()
	<-done
}
