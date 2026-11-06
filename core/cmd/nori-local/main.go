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
	"nori/internal/wake"
)

func main() {
	s, err := settings.Load()
	if err != nil {
		log.Fatal(err)
	}
	platform.Configure(s)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopTelemetry := observability.Init(ctx, "nori-local", s)
	defer observability.Shutdown(stopTelemetry)
	// The conversation wakes its own loop; only worker wakes can leave the process.
	workers, err := platform.Waker(ctx, s, false)
	if err != nil {
		observability.Fatal(err)
	}
	waker := wake.NewInProcess(workers)
	nori, err := app.Open(ctx, waker, s)
	if err != nil {
		observability.Fatal(err)
	}
	defer nori.Close()
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); nori.LocalLoop(ctx, waker.Notifications()) }()
	server := httpserver.New(ctx, net.JoinHostPort("127.0.0.1", cmp.Or(s.Port, "8080")), api.Handler(nori.API(), true))
	log.Printf("Nori durable conversation on http://%s model=%s; local processing recovers from Firestore", server.Addr, nori.Model)
	if err := httpserver.Listen(ctx, server, 5*time.Second); err != nil {
		observability.RecordErrorCategory(ctx, "http.listen", err)
		observability.Shutdown(stopTelemetry)
		observability.Fatal(err)
	}
	stop()
	<-loopDone
}
