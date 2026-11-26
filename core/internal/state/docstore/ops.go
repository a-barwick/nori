package docstore

import (
	"context"
	"time"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type PoolStats struct {
	AcquiredConns     int32
	IdleConns         int32
	TotalConns        int32
	MaxConns          int32
	ConstructingConns int32
	EmptyAcquireCount int64
	AcquireDuration   time.Duration
}

type PoolStatsProvider interface {
	PoolStats() PoolStats
}

type TxStats struct {
	Total     int64
	Attempts  int64
	Retries   int64
	Exhausted int64
}

type TxStatsProvider interface {
	TxStats() TxStats
}

type ListenStats struct {
	WakeReconnects  int64
	WakeErrors      int64
	WatchReconnects int64
	WatchErrors     int64
	ActiveWatchers  int64
}

type ListenStatsProvider interface {
	ListenStats() ListenStats
}

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

type QueueMetricsProvider interface {
	QueueMetrics(ctx context.Context) (QueueMetrics, error)
}

type AdvisoryLocker interface {
	TryAdvisoryLock(ctx context.Context, key int64) (unlock func(), acquired bool, err error)
}
