package bootstrap

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"nori/internal/api"
	"nori/internal/app"
	"nori/internal/httpserver"
	"nori/internal/netguard"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/worker"
)

// Options controls how the unified Nori runtime starts API, worker, and ops servers.
type Options struct {
	Settings      settings.Settings
	ServiceName   string
	Role          string
	MetricsAddr   string
	LocalDirect   bool // true for nori-local (RequireLoopbackHost directly instead of ServiceHandler)
	ShutdownGrace time.Duration
}

// Run boots telemetry, storage/app service, optional worker pool, runtime metrics,
// pending account-deletion sweeps, and HTTP server, shutting down cleanly on ctx cancel.
func Run(ctx context.Context, opts Options) {
	s := opts.Settings
	role := cmp.Or(opts.Role, s.Role, "all")
	s.Role = role
	serviceName := opts.ServiceName
	if serviceName == "" {
		serviceName = "nori"
		switch role {
		case "api":
			serviceName = "nori-api"
		case "worker":
			serviceName = "nori-worker"
		}
	}
	grace := opts.ShutdownGrace
	if grace <= 0 {
		grace = 25 * time.Second
	}

	stopTelemetry := observability.Init(ctx, serviceName, s)
	defer observability.Shutdown(stopTelemetry)

	nori, err := app.OpenService(ctx, nil, s)
	if err != nil {
		observability.Fatal(err)
	}
	defer nori.Close()

	runAPI := role == "all" || role == "api"
	runWorker := (role == "all" || role == "worker") && !s.Features.DisableBackgroundTasks
	if !runAPI && !runWorker {
		observability.Fatal(fmt.Errorf("role=%s has nothing to run (background tasks are disabled)", role))
	}

	var pool *worker.Pool
	if runWorker {
		pool, err = worker.New(nori, worker.Options{})
		if err != nil {
			observability.Fatal(err)
		}
	}

	var workerLanes func() observability.WorkerLaneStats
	if pool != nil {
		workerLanes = func() observability.WorkerLaneStats {
			st := pool.LaneStats()
			return observability.WorkerLaneStats{
				TaskActive:           st.TaskActive,
				TaskCapacity:         st.TaskCapacity,
				ConversationActive:   st.ConversationActive,
				ConversationCapacity: st.ConversationCapacity,
				SchedulerActive:      st.SchedulerActive,
				SchedulerCapacity:    st.SchedulerCapacity,
			}
		}
	}
	observability.SetRuntimeMetrics(observability.RuntimeMetricsSourcesFromBackend(nori.Backend(), workerLanes))

	localMode := s.Cloud == "local"
	var server *http.Server
	if runAPI {
		bindHost := s.BindHost
		if opts.LocalDirect {
			bindHost = "127.0.0.1"
		} else if localMode {
			if bindHost == "" {
				bindHost = "127.0.0.1"
			} else if !netguard.IsLoopbackHost(bindHost) {
				observability.Fatal(fmt.Errorf("NORI_CLOUD=local requires a loopback bind host (got %q)", bindHost))
			}
		}
		var handler http.Handler
		if opts.LocalDirect {
			handler = httpserver.RequireLoopbackHost(nori.Handler(true))
		} else {
			handler, err = platform.ServiceHandler(s, nori.Handler(localMode), localMode)
			if err != nil {
				observability.Fatal(err)
			}
		}
		server = httpserver.New(ctx, net.JoinHostPort(bindHost, cmp.Or(s.Port, "8080")), handler)
	} else if opts.MetricsAddr != "" {
		if localMode {
			host, _, err := net.SplitHostPort(opts.MetricsAddr)
			if err != nil || !netguard.IsLoopbackHost(host) {
				observability.Fatal(fmt.Errorf("NORI_CLOUD=local requires a loopback --metrics-addr host (got %q)", opts.MetricsAddr))
			}
		}
		ops := api.OpsHandler(localMode, s.Models.Outer, api.UserHandlerOptions{
			ReadyCheck: nori.Ping,
			Metrics:    observability.MetricsHandler(),
		})
		handler, err := platform.ServiceHandler(s, ops, localMode)
		if err != nil {
			observability.Fatal(err)
		}
		server = httpserver.New(ctx, opts.MetricsAddr, handler)
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
		errCat   string
	)
	recordErr := func(cat string, err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			errCat = cat
		}
		errMu.Unlock()
		cancelRun()
	}

	if pool != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slog.InfoContext(runCtx, "worker pool started", "owner", pool.Owner(), "role", role)
			if err := pool.Run(runCtx); err != nil {
				recordErr("worker.lifecycle", err)
			}
			slog.InfoContext(ctx, "worker pool stopped", "owner", pool.Owner())
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			timer := time.NewTimer(30 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-runCtx.Done():
					return
				case <-timer.C:
					nori.SweepPendingAccountDeletions(runCtx)
					timer.Reset(5 * time.Minute)
				}
			}
		}()
	}

	if server != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if runAPI {
				slog.InfoContext(runCtx, serviceName+" listening", "addr", server.Addr, "role", role, "model", s.Models.Outer, "backend", s.Store.Backend)
			} else {
				slog.InfoContext(runCtx, "worker ops server listening", "addr", server.Addr, "role", role)
			}
			if err := httpserver.Listen(runCtx, server, grace); err != nil {
				recordErr("http.listen", err)
			}
			cancelRun()
		}()
	}

	wg.Wait()
	if firstErr != nil {
		observability.RecordErrorCategory(ctx, errCat, firstErr)
		observability.Shutdown(stopTelemetry)
		observability.Fatal(firstErr)
	}
}
