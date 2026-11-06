package main

import (
	"cmp"
	"context"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nori/internal/api"
	"nori/internal/app"
	"nori/internal/httpserver"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/settings"
)

func main() {
	s, err := settings.Load()
	if err != nil {
		log.Fatal(err)
	}
	platform.Configure(s)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopTelemetry := observability.Init(ctx, "nori-api", s)
	defer observability.Shutdown(stopTelemetry)
	waker, err := platform.Waker(ctx, s, true)
	if err != nil {
		observability.Fatal(err)
	}
	nori, err := app.Open(ctx, waker, s)
	if err != nil {
		observability.Fatal(err)
	}
	defer nori.Close()
	// Only request-bound /api/process performs outer work in hosted mode. Cloud
	// Tasks owns retry delivery across scale-to-zero and process termination.
	server := httpserver.New(ctx, net.JoinHostPort(s.BindHost, cmp.Or(s.Port, "8080")), api.Handler(nori.API(), false))
	log.Printf("Nori durable conversation listening on %s model=%s", server.Addr, nori.Model)
	if err := httpserver.Listen(ctx, server, 25*time.Second); err != nil {
		observability.RecordErrorCategory(ctx, "http.listen", err)
		observability.Shutdown(stopTelemetry)
		observability.Fatal(err)
	}
}
