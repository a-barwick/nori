package records_test

import (
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
	"nori/internal/state/postgres"
	"nori/internal/state/records"
	"nori/internal/users"
)

func percentileDuration(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100.0*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// TestReserveUsagePostgresConcurrencyBenchmark benchmarks ReserveUsage under realistic
// multi-task concurrency (10 concurrent background tasks for one active user) against an
// isolated ephemeral Postgres database.
//
// Set NORI_POSTGRES_BENCH_URL to an isolated ephemeral Postgres database URL to run.
func TestReserveUsagePostgresConcurrencyBenchmark(t *testing.T) {
	pgURL := strings.TrimSpace(os.Getenv("NORI_POSTGRES_BENCH_URL"))
	if pgURL == "" {
		t.Skip("set NORI_POSTGRES_BENCH_URL to an isolated ephemeral Postgres DB to run ReserveUsage concurrency benchmark")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	backend, err := postgres.OpenBackend(ctx, pgURL)
	if err != nil {
		t.Fatalf("OpenBackend: %v", err)
	}
	defer backend.Close()

	runConcurrency := func(t *testing.T, workers, callsPerWorker, maxCallsPerHour int, maxUSDPerDay float64) (allowed int64, denied int64, retries int64, latencies []time.Duration, wall time.Duration) {
		t.Helper()
		user := users.User{
			ID:          uuid.NewString(),
			Email:       fmt.Sprintf("bench-%s@example.com", uuid.NewString()[:8]),
			DisplayName: "Bench User",
			TimeZone:    "UTC",
		}
		db := records.NewShared(backend, user, 100.0)
		defer db.Close()

		now := time.Now().UTC()
		hourCutoff := now.Add(-time.Hour)
		dayCutoff := now.Add(-24 * time.Hour)

		beforeStats, _ := postgres.TxStatsOf(backend)
		start := time.Now()

		var (
			wg       sync.WaitGroup
			latMu    sync.Mutex
			errCount atomic.Int64
			allowCnt atomic.Int64
			denyCnt  atomic.Int64
			firstErr error
			firstMu  sync.Mutex
		)
		latencies = make([]time.Duration, 0, workers*callsPerWorker)

		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(workerIdx int) {
				defer wg.Done()
				for i := 0; i < callsPerWorker; i++ {
					u := state.Usage{
						ID:                   uuid.NewString(),
						InputID:              fmt.Sprintf("task-%d", workerIdx),
						Provider:             "benchmark",
						Model:                "bench-model",
						EstimatedExposureUSD: 0.01,
						CreatedAt:            now,
					}
					opStart := time.Now()
					ok, err := db.ReserveUsage(ctx, u, hourCutoff, dayCutoff, maxCallsPerHour, maxUSDPerDay, 0.01)
					opDur := time.Since(opStart)

					latMu.Lock()
					latencies = append(latencies, opDur)
					latMu.Unlock()

					if err != nil {
						errCount.Add(1)
						firstMu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						firstMu.Unlock()
						continue
					}
					if ok {
						allowCnt.Add(1)
					} else {
						denyCnt.Add(1)
					}
				}
			}(w)
		}
		wg.Wait()
		wall = time.Since(start)
		afterStats, _ := postgres.TxStatsOf(backend)

		if errCount.Load() > 0 {
			t.Fatalf("ReserveUsage had %d errors (first: %v)", errCount.Load(), firstErr)
		}
		slices.Sort(latencies)
		retries = afterStats.Retries - beforeStats.Retries
		return allowCnt.Load(), denyCnt.Load(), retries, latencies, wall
	}

	t.Run("10_concurrent_tasks_200_reservations_all_allowed", func(t *testing.T) {
		allowed, denied, retries, latencies, wall := runConcurrency(t, 10, 20, 300, 15.0)
		if allowed != 200 || denied != 0 {
			t.Fatalf("allowed=%d denied=%d, want 200 / 0", allowed, denied)
		}
		opsPerSec := float64(len(latencies)) / wall.Seconds()
		t.Logf("BENCH 10x20 (all allowed): total=%d wall=%s throughput=%.1f ops/s retries=%d p50=%s p95=%s p99=%s max=%s",
			len(latencies), wall.Round(time.Millisecond), opsPerSec, retries,
			percentileDuration(latencies, 50).Round(time.Millisecond),
			percentileDuration(latencies, 95).Round(time.Millisecond),
			percentileDuration(latencies, 99).Round(time.Millisecond),
			latencies[len(latencies)-1].Round(time.Millisecond),
		)
	})

	t.Run("10_concurrent_tasks_strict_quota_cap_50", func(t *testing.T) {
		allowed, denied, retries, latencies, wall := runConcurrency(t, 10, 10, 50, 15.0)
		if allowed != 50 || denied != 50 {
			t.Fatalf("strict cap violation: allowed=%d denied=%d, want 50 / 50", allowed, denied)
		}
		opsPerSec := float64(len(latencies)) / wall.Seconds()
		t.Logf("BENCH 10x10 (cap=50): total=%d allowed=%d denied=%d wall=%s throughput=%.1f ops/s retries=%d p50=%s p95=%s p99=%s max=%s",
			len(latencies), allowed, denied, wall.Round(time.Millisecond), opsPerSec, retries,
			percentileDuration(latencies, 50).Round(time.Millisecond),
			percentileDuration(latencies, 95).Round(time.Millisecond),
			percentileDuration(latencies, 99).Round(time.Millisecond),
			latencies[len(latencies)-1].Round(time.Millisecond),
		)
	})
}
