package docstore

import (
	"context"
	"time"
)

// Pinger checks live backend connectivity (used by readiness probes).
type Pinger interface {
	Ping(ctx context.Context) error
}

// PoolStats captures connection-pool health for operational metrics.
type PoolStats struct {
	AcquiredConns     int32
	IdleConns         int32
	TotalConns        int32
	MaxConns          int32
	ConstructingConns int32
	EmptyAcquireCount int64
	AcquireDuration   time.Duration
}

// PoolStatsProvider reports connection-pool metrics when supported by the backend.
type PoolStatsProvider interface {
	PoolStats() PoolStats
}

// TxStats reports cumulative serializable transaction counts.
type TxStats struct {
	Total     int64
	Attempts  int64
	Retries   int64
	Exhausted int64
}

// TxStatsProvider reports serializable transaction metrics when supported.
type TxStatsProvider interface {
	TxStats() TxStats
}

// ListenStats reports LISTEN/NOTIFY connection and SSE watch hub health.
type ListenStats struct {
	WakeReconnects  int64
	WakeErrors      int64
	WatchReconnects int64
	WatchErrors     int64
	ActiveWatchers  int64
}

// ListenStatsProvider reports LISTEN/watch health metrics when supported.
type ListenStatsProvider interface {
	ListenStats() ListenStats
}

// QueueMetrics captures durable task lease and user wake queue depth and claim lag.
type QueueMetrics struct {
	TaskRunnable      int64
	TaskParked        int64
	TaskLeased        int64
	TaskOldestLagSec  float64
	ConvRunnable      int64
	ConvOldestLagSec  float64
	SchedRunnable     int64
	SchedOldestLagSec float64
}

// QueueMetricsProvider computes durable queue depth and lag metrics.
type QueueMetricsProvider interface {
	QueueMetrics(ctx context.Context) (QueueMetrics, error)
}

// AdvisoryLocker provides non-blocking distributed advisory locks (e.g., Postgres
// session advisory locks) for leader-electing cluster-wide maintenance sweeps.
type AdvisoryLocker interface {
	TryAdvisoryLock(ctx context.Context, key int64) (unlock func(), acquired bool, err error)
}
