package main

import (
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nori/internal/env"
	"nori/internal/observability"
	"nori/internal/wake"
)

func main() {
	if err := env.Load(); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopTelemetry := observability.Init(ctx, "nori-api")
	defer observability.Shutdown(stopTelemetry)
	runtime, err := wake.Open(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer runtime.Close()
	queue, err := wake.NewCloud(ctx, os.Getenv("NORI_WAKE_QUEUE"), os.Getenv("NORI_RUNTIME_URL"), os.Getenv("NORI_WAKE_SERVICE_ACCOUNT"))
	if err != nil {
		log.Fatal(err)
	}
	// Only request-bound /api/process performs outer work in hosted mode. Cloud
	// Tasks owns retry delivery across scale-to-zero and process termination.
	server := wake.Server(ctx, net.JoinHostPort(env.Get("BIND_HOST", "127.0.0.1"), env.Get("PORT", "8080")), runtime.Handler(queue.Schedule, false))
	log.Printf("Nori durable conversation listening on %s model=%s", server.Addr, runtime.Model)
	if err := wake.Listen(ctx, server, 25*time.Second); err != nil {
		observability.RecordErrorCategory(ctx, "http.listen", err)
		observability.Shutdown(stopTelemetry)
		log.Fatal(err)
	}
}
