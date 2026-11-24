package observability

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"nori/internal/state/docstore"
)

const (
	defaultQueueMetricsTimeout  = 2 * time.Second
	defaultQueueMetricsCacheTTL = 1 * time.Second
	prometheusTextContentType   = "text/plain; version=0.0.4; charset=utf-8"
)

// WorkerLaneStats captures active in-flight work and configured concurrency
// capacity across the worker pool's execution lanes.
type WorkerLaneStats struct {
	TaskActive           int64
	TaskCapacity         int64
	ConversationActive   int64
	ConversationCapacity int64
	SchedulerActive      int64
	SchedulerCapacity    int64
}

// RuntimeMetricsSources wires live, unsampled operational metric providers
// from the storage backend and worker pool. Every field is optional; nil
// providers are omitted from snapshots and scrapes.
type RuntimeMetricsSources struct {
	Pool        docstore.PoolStatsProvider
	Tx          docstore.TxStatsProvider
	Listen      docstore.ListenStatsProvider
	Queue       docstore.QueueMetricsProvider
	WorkerLanes func() WorkerLaneStats

	// QueueTimeout bounds QueueMetrics database queries (defaults to 2s).
	QueueTimeout time.Duration
	// QueueCacheTTL caches successful QueueMetrics snapshots briefly to prevent
	// rapid scrapes from hammering Postgres (defaults to 1s; negative disables caching).
	QueueCacheTTL time.Duration
	// Now optionally overrides time.Now for deterministic cache testing.
	Now func() time.Time
}

// RuntimeMetricsSourcesFromBackend constructs RuntimeMetricsSources by
// type-asserting docstore operational interfaces from backend.
func RuntimeMetricsSourcesFromBackend(backend docstore.Backend, workerLanes func() WorkerLaneStats) RuntimeMetricsSources {
	sources := RuntimeMetricsSources{WorkerLanes: workerLanes}
	if backend == nil {
		return sources
	}
	sources.Pool, _ = backend.(docstore.PoolStatsProvider)
	sources.Tx, _ = backend.(docstore.TxStatsProvider)
	sources.Listen, _ = backend.(docstore.ListenStatsProvider)
	sources.Queue, _ = backend.(docstore.QueueMetricsProvider)
	return sources
}

// RuntimeMetrics is a typed, unsampled point-in-time snapshot of Nori's
// operational runtime metrics (zero PII, zero user-scoped labels).
type RuntimeMetrics struct {
	HasPool           bool
	Pool              docstore.PoolStats
	HasTx             bool
	Tx                docstore.TxStats
	HasListen         bool
	Listen            docstore.ListenStats
	HasQueue          bool
	Queue             docstore.QueueMetrics
	QueueErr          error
	QueueScrapeErrors int64
	HasWorkerLanes    bool
	WorkerLanes       WorkerLaneStats
}

// MetricsCollector serves unsampled operational runtime snapshots and
// Prometheus/OpenMetrics text exposition independent of Sentry or trace sampling.
type MetricsCollector struct {
	sources RuntimeMetricsSources

	queueMu           sync.Mutex
	queueVal          docstore.QueueMetrics
	queueAt           time.Time
	queueScrapeErrors atomic.Int64
}

// NewMetricsCollector creates an isolated collector for sources.
func NewMetricsCollector(sources RuntimeMetricsSources) *MetricsCollector {
	return &MetricsCollector{sources: sources}
}

func (c *MetricsCollector) now() time.Time {
	if c != nil && c.sources.Now != nil {
		return c.sources.Now()
	}
	return time.Now()
}

func (c *MetricsCollector) queueTimeout() time.Duration {
	if c != nil && c.sources.QueueTimeout > 0 {
		return c.sources.QueueTimeout
	}
	return defaultQueueMetricsTimeout
}

func (c *MetricsCollector) queueCacheTTL() time.Duration {
	if c == nil {
		return defaultQueueMetricsCacheTTL
	}
	if c.sources.QueueCacheTTL < 0 {
		return 0
	}
	if c.sources.QueueCacheTTL > 0 {
		return c.sources.QueueCacheTTL
	}
	return defaultQueueMetricsCacheTTL
}

func (c *MetricsCollector) readQueueMetrics(ctx context.Context) (docstore.QueueMetrics, error) {
	if c == nil || c.sources.Queue == nil {
		return docstore.QueueMetrics{}, nil
	}
	ttl := c.queueCacheTTL()

	c.queueMu.Lock()
	defer c.queueMu.Unlock()

	now := c.now()
	if ttl > 0 && !c.queueAt.IsZero() && now.Sub(c.queueAt) < ttl {
		return c.queueVal, nil
	}

	qctx, cancel := context.WithTimeout(ctx, c.queueTimeout())
	defer cancel()
	qm, err := c.sources.Queue.QueueMetrics(qctx)
	if err != nil {
		c.queueScrapeErrors.Add(1)
		return docstore.QueueMetrics{}, err
	}
	c.queueVal = qm
	c.queueAt = c.now()
	return qm, nil
}

// Snapshot collects a live, unsampled snapshot from all configured sources.
func (c *MetricsCollector) Snapshot(ctx context.Context) RuntimeMetrics {
	if c == nil {
		return RuntimeMetrics{}
	}
	var out RuntimeMetrics
	if c.sources.Pool != nil {
		out.HasPool = true
		out.Pool = c.sources.Pool.PoolStats()
	}
	if c.sources.Tx != nil {
		out.HasTx = true
		out.Tx = c.sources.Tx.TxStats()
	}
	if c.sources.Listen != nil {
		out.HasListen = true
		out.Listen = c.sources.Listen.ListenStats()
	}
	if c.sources.Queue != nil {
		out.HasQueue = true
		qm, err := c.readQueueMetrics(ctx)
		out.Queue = qm
		out.QueueErr = err
		out.QueueScrapeErrors = c.queueScrapeErrors.Load()
	}
	if c.sources.WorkerLanes != nil {
		out.HasWorkerLanes = true
		out.WorkerLanes = c.sources.WorkerLanes()
	}
	return out
}

// FormatPrometheus renders snap in Prometheus text exposition format (0.0.4).
// Labels are strictly static low-cardinality enums (listener, lane); no user IDs
// or PII are ever emitted.
func FormatPrometheus(snap RuntimeMetrics) []byte {
	var b bytes.Buffer
	if snap.HasPool {
		writeGaugeInt(&b, "nori_postgres_pool_acquired_conns", "Current number of acquired Postgres pool connections.", int64(snap.Pool.AcquiredConns))
		writeGaugeInt(&b, "nori_postgres_pool_idle_conns", "Current number of idle Postgres pool connections.", int64(snap.Pool.IdleConns))
		writeGaugeInt(&b, "nori_postgres_pool_total_conns", "Current total number of Postgres pool connections.", int64(snap.Pool.TotalConns))
		writeGaugeInt(&b, "nori_postgres_pool_max_conns", "Configured maximum number of Postgres pool connections.", int64(snap.Pool.MaxConns))
		writeCounterInt(&b, "nori_postgres_pool_empty_acquire_total", "Cumulative count of pool acquires that waited for a connection.", snap.Pool.EmptyAcquireCount)
		writeCounterFloat(&b, "nori_postgres_pool_acquire_duration_seconds_total", "Cumulative time spent acquiring Postgres pool connections in seconds.", sanitizeNonNegativeFloat(snap.Pool.AcquireDuration.Seconds()))
	}
	if snap.HasTx {
		writeCounterInt(&b, "nori_postgres_tx_total", "Cumulative number of serializable Postgres transactions executed.", snap.Tx.Total)
		writeCounterInt(&b, "nori_postgres_tx_attempts_total", "Cumulative number of serializable Postgres transaction attempts.", snap.Tx.Attempts)
		writeCounterInt(&b, "nori_postgres_tx_retries_total", "Cumulative number of serializable Postgres transaction retries (SQLSTATE 40001).", snap.Tx.Retries)
		writeCounterInt(&b, "nori_postgres_tx_exhausted_total", "Cumulative number of serializable Postgres transactions that exhausted retries.", snap.Tx.Exhausted)
	}
	if snap.HasListen {
		writeHeader(&b, "nori_postgres_listen_reconnects_total", "counter", "Cumulative number of Postgres LISTEN reconnects by listener.")
		writeSampleInt(&b, "nori_postgres_listen_reconnects_total", `listener="wake"`, snap.Listen.WakeReconnects)
		writeSampleInt(&b, "nori_postgres_listen_reconnects_total", `listener="watch"`, snap.Listen.WatchReconnects)

		writeHeader(&b, "nori_postgres_listen_errors_total", "counter", "Cumulative number of Postgres LISTEN errors by listener.")
		writeSampleInt(&b, "nori_postgres_listen_errors_total", `listener="wake"`, snap.Listen.WakeErrors)
		writeSampleInt(&b, "nori_postgres_listen_errors_total", `listener="watch"`, snap.Listen.WatchErrors)

		writeGaugeInt(&b, "nori_sse_watchers_active", "Current number of active SSE watch subscriptions.", snap.Listen.ActiveWatchers)
	}
	if snap.HasQueue {
		if snap.QueueErr == nil {
			writeGaugeInt(&b, "nori_task_leases_runnable", "Current number of runnable durable task leases.", snap.Queue.TaskRunnable)
			writeGaugeInt(&b, "nori_task_leases_parked", "Current number of parked durable task leases.", snap.Queue.TaskParked)
			writeGaugeInt(&b, "nori_task_leases_leased", "Current number of actively leased durable task leases.", snap.Queue.TaskLeased)
			writeGaugeFloat(&b, "nori_task_leases_oldest_lag_seconds", "Age in seconds of the oldest overdue runnable task lease.", sanitizeNonNegativeFloat(snap.Queue.TaskOldestLagSec))

			writeHeader(&b, "nori_user_wakes_runnable", "gauge", "Current number of runnable user wakes by lane.")
			writeSampleInt(&b, "nori_user_wakes_runnable", `lane="conversation"`, snap.Queue.ConvRunnable)
			writeSampleInt(&b, "nori_user_wakes_runnable", `lane="scheduler"`, snap.Queue.SchedRunnable)

			writeHeader(&b, "nori_user_wakes_oldest_lag_seconds", "gauge", "Age in seconds of the oldest overdue runnable user wake by lane.")
			writeSampleFloat(&b, "nori_user_wakes_oldest_lag_seconds", `lane="conversation"`, sanitizeNonNegativeFloat(snap.Queue.ConvOldestLagSec))
			writeSampleFloat(&b, "nori_user_wakes_oldest_lag_seconds", `lane="scheduler"`, sanitizeNonNegativeFloat(snap.Queue.SchedOldestLagSec))
		}
		writeCounterInt(&b, "nori_queue_metrics_scrape_errors_total", "Cumulative number of failed durable queue metric scrapes.", snap.QueueScrapeErrors)
	}
	if snap.HasWorkerLanes {
		writeHeader(&b, "nori_worker_lane_active", "gauge", "Current number of active workers by execution lane.")
		writeSampleInt(&b, "nori_worker_lane_active", `lane="task"`, snap.WorkerLanes.TaskActive)
		writeSampleInt(&b, "nori_worker_lane_active", `lane="conversation"`, snap.WorkerLanes.ConversationActive)
		writeSampleInt(&b, "nori_worker_lane_active", `lane="scheduler"`, snap.WorkerLanes.SchedulerActive)

		writeHeader(&b, "nori_worker_lane_capacity", "gauge", "Configured worker concurrency capacity by execution lane.")
		writeSampleInt(&b, "nori_worker_lane_capacity", `lane="task"`, snap.WorkerLanes.TaskCapacity)
		writeSampleInt(&b, "nori_worker_lane_capacity", `lane="conversation"`, snap.WorkerLanes.ConversationCapacity)
		writeSampleInt(&b, "nori_worker_lane_capacity", `lane="scheduler"`, snap.WorkerLanes.SchedulerCapacity)
	}
	return b.Bytes()
}

// Handler returns an HTTP handler exposing this collector in Prometheus text format.
func (c *MetricsCollector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snap := c.Snapshot(r.Context())
		body := FormatPrometheus(snap)
		w.Header().Set("Content-Type", prometheusTextContentType)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	})
}

var defaultMetricsCollector atomic.Pointer[MetricsCollector]

// SetRuntimeMetrics registers the process-wide unsampled operational metric
// providers used by RuntimeMetricsSnapshot and MetricsHandler.
func SetRuntimeMetrics(sources RuntimeMetricsSources) {
	defaultMetricsCollector.Store(NewMetricsCollector(sources))
}

// RuntimeMetricsSnapshot returns a live, unsampled snapshot from the registered
// process-wide RuntimeMetricsSources.
func RuntimeMetricsSnapshot(ctx context.Context) RuntimeMetrics {
	return defaultMetricsCollector.Load().Snapshot(ctx)
}

// MetricsHandler returns an HTTP handler serving the process-wide unsampled
// operational metrics in Prometheus text exposition format.
func MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defaultMetricsCollector.Load().Handler().ServeHTTP(w, r)
	})
}

func sanitizeNonNegativeFloat(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func writeHeader(b *bytes.Buffer, name, metricType, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func writeGaugeInt(b *bytes.Buffer, name, help string, v int64) {
	writeHeader(b, name, "gauge", help)
	writeSampleInt(b, name, "", v)
}

func writeGaugeFloat(b *bytes.Buffer, name, help string, v float64) {
	writeHeader(b, name, "gauge", help)
	writeSampleFloat(b, name, "", v)
}

func writeCounterInt(b *bytes.Buffer, name, help string, v int64) {
	writeHeader(b, name, "counter", help)
	writeSampleInt(b, name, "", v)
}

func writeCounterFloat(b *bytes.Buffer, name, help string, v float64) {
	writeHeader(b, name, "counter", help)
	writeSampleFloat(b, name, "", v)
}

func writeSampleInt(b *bytes.Buffer, name, labels string, v int64) {
	if labels != "" {
		b.WriteString(name)
		b.WriteByte('{')
		b.WriteString(labels)
		b.WriteString("} ")
	} else {
		b.WriteString(name)
		b.WriteByte(' ')
	}
	b.WriteString(strconv.FormatInt(v, 10))
	b.WriteByte('\n')
}

func writeSampleFloat(b *bytes.Buffer, name, labels string, v float64) {
	if labels != "" {
		b.WriteString(name)
		b.WriteByte('{')
		b.WriteString(labels)
		b.WriteString("} ")
	} else {
		b.WriteString(name)
		b.WriteByte(' ')
	}
	b.WriteString(strconv.FormatFloat(v, 'g', -1, 64))
	b.WriteByte('\n')
}
