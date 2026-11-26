package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/settings"
	"nori/internal/state/docstore"
)

type stubRuntimeBackend struct {
	docstore.Backend
	pool   docstore.PoolStats
	tx     docstore.TxStats
	listen docstore.ListenStats
	queue  func(context.Context) (docstore.QueueMetrics, error)
}

func (s *stubRuntimeBackend) PoolStats() docstore.PoolStats     { return s.pool }
func (s *stubRuntimeBackend) TxStats() docstore.TxStats         { return s.tx }
func (s *stubRuntimeBackend) ListenStats() docstore.ListenStats { return s.listen }
func (s *stubRuntimeBackend) QueueMetrics(ctx context.Context) (docstore.QueueMetrics, error) {
	if s.queue != nil {
		return s.queue(ctx)
	}
	return docstore.QueueMetrics{}, nil
}

func TestUnsampledRuntimeMetricsPrometheusOutput(t *testing.T) {
	stop := Init(context.Background(), "nori-test", settings.Settings{
		Observability: settings.Observability{
			SentryDSN:  "",
			SampleRate: "0",
		},
	})
	defer Shutdown(stop)
	if TracingEnabled() {
		t.Fatalf("expected TracingEnabled() == false with no exporters")
	}

	var queueCalls atomic.Int32
	backend := &stubRuntimeBackend{
		pool: docstore.PoolStats{
			AcquiredConns:     4,
			IdleConns:         6,
			TotalConns:        10,
			MaxConns:          25,
			EmptyAcquireCount: 3,
			AcquireDuration:   1500 * time.Millisecond,
		},
		tx: docstore.TxStats{
			Total:     120,
			Attempts:  125,
			Retries:   5,
			Exhausted: 1,
		},
		listen: docstore.ListenStats{
			WakeReconnects:  2,
			WakeErrors:      4,
			WatchReconnects: 3,
			WatchErrors:     5,
			ActiveWatchers:  17,
		},
		queue: func(ctx context.Context) (docstore.QueueMetrics, error) {
			queueCalls.Add(1)
			return docstore.QueueMetrics{
				TaskRunnable:      11,
				TaskParked:        7,
				TaskLeased:        3,
				TaskOldestLagSec:  4.25,
				ConvRunnable:      2,
				ConvOldestLagSec:  1.5,
				SchedRunnable:     5,
				SchedOldestLagSec: 12.75,
			}, nil
		},
	}

	sources := RuntimeMetricsSourcesFromBackend(backend, func() WorkerLaneStats {
		return WorkerLaneStats{
			TaskActive:           8,
			TaskCapacity:         64,
			ConversationActive:   4,
			ConversationCapacity: 32,
			SchedulerActive:      2,
			SchedulerCapacity:    16,
		}
	})
	SetRuntimeMetrics(sources)
	t.Cleanup(func() { SetRuntimeMetrics(RuntimeMetricsSources{}) })

	snap := RuntimeMetricsSnapshot(context.Background())
	if !snap.HasPool || !snap.HasTx || !snap.HasListen || !snap.HasQueue || !snap.HasWorkerLanes {
		t.Fatalf("expected all metric groups populated in snapshot: %+v", snap)
	}
	if snap.Pool.MaxConns != 25 || snap.Tx.Retries != 5 || snap.Listen.ActiveWatchers != 17 || snap.Queue.TaskRunnable != 11 || snap.WorkerLanes.TaskActive != 8 {
		t.Fatalf("unexpected snapshot values: %+v", snap)
	}

	handler := MetricsHandler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}

	body := rec.Body.String()
	wantLines := []string{
		"nori_postgres_pool_acquired_conns 4",
		"nori_postgres_pool_idle_conns 6",
		"nori_postgres_pool_total_conns 10",
		"nori_postgres_pool_max_conns 25",
		"nori_postgres_pool_empty_acquire_total 3",
		"nori_postgres_pool_acquire_duration_seconds_total 1.5",
		"nori_postgres_tx_total 120",
		"nori_postgres_tx_attempts_total 125",
		"nori_postgres_tx_retries_total 5",
		"nori_postgres_tx_exhausted_total 1",
		`nori_postgres_listen_reconnects_total{listener="wake"} 2`,
		`nori_postgres_listen_reconnects_total{listener="watch"} 3`,
		`nori_postgres_listen_errors_total{listener="wake"} 4`,
		`nori_postgres_listen_errors_total{listener="watch"} 5`,
		"nori_sse_watchers_active 17",
		"nori_task_leases_runnable 11",
		"nori_task_leases_parked 7",
		"nori_task_leases_leased 3",
		"nori_task_leases_oldest_lag_seconds 4.25",
		`nori_user_wakes_runnable{lane="conversation"} 2`,
		`nori_user_wakes_runnable{lane="scheduler"} 5`,
		`nori_user_wakes_oldest_lag_seconds{lane="conversation"} 1.5`,
		`nori_user_wakes_oldest_lag_seconds{lane="scheduler"} 12.75`,
		`nori_worker_lane_active{lane="task"} 8`,
		`nori_worker_lane_active{lane="conversation"} 4`,
		`nori_worker_lane_active{lane="scheduler"} 2`,
		`nori_worker_lane_capacity{lane="task"} 64`,
		`nori_worker_lane_capacity{lane="conversation"} 32`,
		`nori_worker_lane_capacity{lane="scheduler"} 16`,
	}
	for _, want := range wantLines {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("metrics output missing line %q\nfull output:\n%s", want, body)
		}
	}

	for i := range 250 {
		r := httptest.NewRecorder()
		handler.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "nori_postgres_tx_total 120") {
			t.Fatalf("scrape #%d failed or dropped metrics: code=%d", i, r.Code)
		}
	}

	headRec := httptest.NewRecorder()
	handler.ServeHTTP(headRec, httptest.NewRequest(http.MethodHead, "/metrics", nil))
	if headRec.Code != http.StatusOK || headRec.Body.Len() != 0 {
		t.Fatalf("HEAD /metrics status=%d bodyLen=%d", headRec.Code, headRec.Body.Len())
	}
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if postRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /metrics status=%d, want 405", postRec.Code)
	}
}

func TestQueueMetricsCacheAndTimeoutResilience(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}

	var calls atomic.Int32
	var mode atomic.Int32
	backend := &stubRuntimeBackend{
		pool: docstore.PoolStats{MaxConns: 25, TotalConns: 5},
		queue: func(ctx context.Context) (docstore.QueueMetrics, error) {
			n := calls.Add(1)
			switch mode.Load() {
			case 1:
				return docstore.QueueMetrics{}, errors.New("db query failed")
			case 2:
				<-ctx.Done()
				return docstore.QueueMetrics{}, ctx.Err()
			default:
				return docstore.QueueMetrics{TaskRunnable: int64(n)}, nil
			}
		},
	}

	collector := NewMetricsCollector(RuntimeMetricsSources{
		Pool:          backend,
		Queue:         backend,
		QueueTimeout:  40 * time.Millisecond,
		QueueCacheTTL: time.Second,
		Now:           clock,
	})

	s1 := collector.Snapshot(context.Background())
	advance(500 * time.Millisecond)
	s2 := collector.Snapshot(context.Background())
	if calls.Load() != 1 || s1.Queue.TaskRunnable != 1 || s2.Queue.TaskRunnable != 1 {
		t.Fatalf("expected 1 cached QueueMetrics call, got calls=%d s1=%d s2=%d", calls.Load(), s1.Queue.TaskRunnable, s2.Queue.TaskRunnable)
	}

	advance(600 * time.Millisecond)
	s3 := collector.Snapshot(context.Background())
	if calls.Load() != 2 || s3.Queue.TaskRunnable != 2 {
		t.Fatalf("expected refresh after TTL, got calls=%d s3=%d", calls.Load(), s3.Queue.TaskRunnable)
	}

	advance(2 * time.Second)
	mode.Store(1)
	rec := httptest.NewRecorder()
	collector.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics on queue error status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "nori_postgres_pool_max_conns 25") {
		t.Fatalf("expected pool metrics preserved on queue error: %s", body)
	}
	if strings.Contains(body, "nori_task_leases_runnable ") {
		t.Fatalf("queue depth metric must be omitted on scrape error to avoid fake zeros: %s", body)
	}
	if !strings.Contains(body, "nori_queue_metrics_scrape_errors_total 1") {
		t.Fatalf("expected scrape error counter == 1: %s", body)
	}

	mode.Store(2)
	start := time.Now()
	s4 := collector.Snapshot(context.Background())
	elapsed := time.Since(start)
	if !errors.Is(s4.QueueErr, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded on hung QueueMetrics, got %v", s4.QueueErr)
	}
	if elapsed > time.Second {
		t.Fatalf("hung QueueMetrics was not bounded by QueueTimeout: %s", elapsed)
	}
}

func TestFailedPollRouteAndSafeMethod(t *testing.T) {
	for _, path := range []string{"/healthz", "/api/healthz", "/readyz", "/api/readyz", "/metrics"} {
		if got := failedPollRoute(path); got != path {
			t.Fatalf("failedPollRoute(%q) = %q, want %q", path, got, path)
		}
	}
	if got := failedPollRoute("/secret/user/path"); got != "unknown" {
		t.Fatalf("failedPollRoute(unknown) = %q, want unknown", got)
	}
	if got := safeMethod(http.MethodHead); got != http.MethodHead {
		t.Fatalf("safeMethod(HEAD) = %q, want HEAD", got)
	}
	if s := RuntimeMetricsSourcesFromBackend(nil, nil); s.Pool != nil || s.Tx != nil || s.Listen != nil || s.Queue != nil {
		t.Fatalf("expected nil providers for nil backend: %+v", s)
	}
}

func TestMetricsConcurrentScrapesAndHTTPHandlerReadyz(t *testing.T) {
	backend := &stubRuntimeBackend{
		pool:   docstore.PoolStats{MaxConns: 25, TotalConns: 3, IdleConns: 2, AcquiredConns: 1},
		tx:     docstore.TxStats{Total: 10, Attempts: 11, Retries: 1},
		listen: docstore.ListenStats{ActiveWatchers: 4},
		queue: func(ctx context.Context) (docstore.QueueMetrics, error) {
			return docstore.QueueMetrics{TaskRunnable: 2}, nil
		},
	}
	SetRuntimeMetrics(RuntimeMetricsSourcesFromBackend(backend, func() WorkerLaneStats {
		return WorkerLaneStats{TaskActive: 1, TaskCapacity: 64}
	}))
	t.Cleanup(func() { SetRuntimeMetrics(RuntimeMetricsSources{}) })

	handler := MetricsHandler()
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
				if rec.Code != http.StatusOK {
					t.Errorf("concurrent GET /metrics status = %d", rec.Code)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 10 {
			SetRuntimeMetrics(RuntimeMetricsSourcesFromBackend(backend, func() WorkerLaneStats {
				return WorkerLaneStats{TaskActive: 2, TaskCapacity: 64}
			}))
		}
	}()
	wg.Wait()

	var status atomic.Int32
	status.Store(http.StatusOK)
	wrapped := HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTPHandler GET /readyz healthy = %d, want 200", rec.Code)
	}
	status.Store(http.StatusServiceUnavailable)
	rec = httptest.NewRecorder()
	wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("HTTPHandler GET /readyz unhealthy = %d, want 503", rec.Code)
	}
}
