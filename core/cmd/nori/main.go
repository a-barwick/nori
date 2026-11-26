package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"nori/internal/api"
	"nori/internal/app"
	"nori/internal/httpserver"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state/postgres"
	"nori/internal/worker"
)

func main() {
	roleFlag := flag.String("role", "", "runtime role: all, api, or worker (defaults to NORI_ROLE or all)")
	migrateOnly := flag.Bool("migrate-only", false, "apply and verify Postgres migrations, then exit")
	metricsAddrFlag := flag.String("metrics-addr", "", "optional listen address for operational endpoints (/healthz, /readyz, /metrics) when --role=worker")
	flag.Parse()

	s, err := settings.Load()
	if err != nil {
		slog.Error("failed to load settings", "err", err)
		os.Exit(1)
	}
	role := cmp.Or(*roleFlag, s.Role, "all")
	if !settings.ValidRole(role) {
		slog.Error("invalid runtime role; must be all, api, or worker", "role", role)
		os.Exit(1)
	}
	s.Role = role

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *migrateOnly {
		backend, err := postgres.OpenBackend(ctx, s.Store.PostgresURL)
		if err != nil {
			observability.Fatal(err)
		}
		if err := backend.Close(); err != nil {
			observability.Fatal(err)
		}
		return
	}

	serviceName := "nori"
	switch role {
	case "api":
		serviceName = "nori-api"
	case "worker":
		serviceName = "nori-worker"
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
		if localMode {
			if bindHost == "" {
				bindHost = "127.0.0.1"
			} else if bindHost != "127.0.0.1" && bindHost != "::1" && bindHost != "localhost" {
				observability.Fatal(fmt.Errorf("NORI_CLOUD=local requires a loopback bind host (got %q)", bindHost))
			}
		}
		handler, err := platform.ServiceHandler(s, nori.Handler(localMode), localMode)
		if err != nil {
			observability.Fatal(err)
		}
		server = httpserver.New(ctx, net.JoinHostPort(bindHost, cmp.Or(s.Port, "8080")), handler)
	} else if *metricsAddrFlag != "" {
		if localMode {
			host, _, err := net.SplitHostPort(*metricsAddrFlag)
			if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
				observability.Fatal(fmt.Errorf("NORI_CLOUD=local requires a loopback --metrics-addr host (got %q)", *metricsAddrFlag))
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
		server = httpserver.New(ctx, *metricsAddrFlag, handler)
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
				slog.InfoContext(runCtx, "API server listening", "addr", server.Addr, "role", role, "model", s.Models.Outer)
			} else {
				slog.InfoContext(runCtx, "worker ops server listening", "addr", server.Addr, "role", role)
			}
			if err := httpserver.Listen(runCtx, server, 25*time.Second); err != nil {
				recordErr("http.listen", err)
			}
		}()
	}

	wg.Wait()
	if firstErr != nil {
		observability.RecordErrorCategory(ctx, errCat, firstErr)
		observability.Shutdown(stopTelemetry)
		observability.Fatal(firstErr)
	}
}
