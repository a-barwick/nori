package main

import (
	"cmp"
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"nori/internal/app"
	"nori/internal/httpserver"
	"nori/internal/observability"
	"nori/internal/settings"
	"nori/internal/worker"
)

func main() {
	s, err := settings.LoadDefaultCloud("local")
	if err != nil {
		slog.Error("failed to load settings", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopTelemetry := observability.Init(ctx, "nori-local", s)
	defer observability.Shutdown(stopTelemetry)

	nori, err := app.OpenService(ctx, nil, s)
	if err != nil {
		observability.Fatal(err)
	}
	defer nori.Close()

	var pool *worker.Pool
	if !s.Features.DisableBackgroundTasks {
		pool, err = worker.New(nori, worker.Options{})
		if err != nil {
			observability.Fatal(err)
		}
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
			if err := pool.Run(runCtx); err != nil {
				recordErr("worker.lifecycle", err)
			}
		}()
	}

	server := httpserver.New(ctx, net.JoinHostPort("127.0.0.1", cmp.Or(s.Port, "8080")), nori.Handler(true))
	slog.InfoContext(runCtx, "nori-local listening", "addr", server.Addr, "model", s.Models.Outer, "backend", s.Store.Backend)
	if err := httpserver.Listen(runCtx, server, 5*time.Second); err != nil {
		recordErr("http.listen", err)
	}
	cancelRun()
	wg.Wait()

	if firstErr != nil {
		observability.RecordErrorCategory(ctx, errCat, firstErr)
		observability.Shutdown(stopTelemetry)
		observability.Fatal(firstErr)
	}
}
