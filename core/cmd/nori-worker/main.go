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

	"nori/internal/env"
	"nori/internal/httpserver"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/spend"
	"nori/internal/wake"
	"nori/internal/workerservice"
)

func main() {
	if e := env.Load(); e != nil {
		log.Fatal(e)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopTelemetry := observability.Init(ctx, "nori-worker")
	defer observability.Shutdown(stopTelemetry)
	if err := spend.ValidateConfiguration(); err != nil {
		observability.Fatal(err)
	}
	// Hosted workers also wake the conversation when a task event is saved.
	waker, err := platform.Waker(ctx, os.Getenv("TEMPORAL_LOCAL") != "true")
	if err != nil {
		observability.Fatal(err)
	}
	if port := os.Getenv("PORT"); port != "" {
		err = serveSlices(ctx, port, waker)
	} else {
		err = run(ctx, waker, "local", nil, false)
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
func serveSlices(ctx context.Context, port string, waker wake.Waker) error {
	addr := ":" + port
	if os.Getenv("TEMPORAL_LOCAL") == "true" {
		addr = "127.0.0.1:" + port
	}
	server := httpserver.New(ctx, addr, workerservice.Handler(ctx, func(ctx context.Context, id string, ids []string, registration bool) error {
		if registration {
			return register(ctx)
		}
		return run(ctx, waker, id, ids, true)
	}))
	server.WriteTimeout = 11 * time.Minute
	if err := httpserver.Listen(ctx, server, 5*time.Minute); err != nil {
		observability.RecordErrorCategory(ctx, "http.listen", err)
		return err
	}
	return nil
}
