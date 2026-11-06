// nori-worker runs the Temporal worker for task workflows and activities. With
// PORT set it serves wake requests, one bounded slice per request, so it can
// scale to zero. Without PORT it polls until stopped.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nori/internal/httpserver"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/wake"
	"nori/internal/workerservice"
)

func main() {
	s, e := settings.Load()
	if e != nil {
		log.Fatal(e)
	}
	cfg, e := loadConfig(s)
	if e != nil {
		log.Fatal(e)
	}
	platform.Configure(s)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopTelemetry := observability.Init(ctx, "nori-worker", s)
	defer observability.Shutdown(stopTelemetry)
	// Hosted workers also wake the conversation when a task event is saved.
	waker, err := platform.Waker(ctx, s, !s.Temporal.Local)
	if err != nil {
		observability.Fatal(err)
	}
	if s.Port != "" {
		err = serveSlices(ctx, s, cfg, waker)
	} else {
		err = run(ctx, s, cfg, waker, "local", nil, false)
		if err != nil {
			observability.RecordErrorCategory(ctx, "worker.lifecycle", err)
		}
	}
	if err != nil {
		observability.Shutdown(stopTelemetry)
		observability.Fatal(err)
	}
}

// serveSlices serves wake requests: each /run request is one bounded slice, and a
// registration request only registers a fresh worker deployment version.
func serveSlices(ctx context.Context, s settings.Settings, cfg config, waker wake.Waker) error {
	addr := ":" + s.Port
	if s.Temporal.Local {
		addr = "127.0.0.1:" + s.Port
	}
	server := httpserver.New(ctx, addr, workerservice.Handler(ctx, func(ctx context.Context, id string, ids []string, registration bool) error {
		if registration {
			return register(ctx, s, cfg)
		}
		return run(ctx, s, cfg, waker, id, ids, true)
	}))
	server.WriteTimeout = 11 * time.Minute
	if err := httpserver.Listen(ctx, server, 5*time.Minute); err != nil {
		observability.RecordErrorCategory(ctx, "http.listen", err)
		return err
	}
	return nil
}
