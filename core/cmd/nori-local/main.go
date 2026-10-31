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
	"nori/internal/wake"
)

func main() {
	if err := env.Load(); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime, err := wake.Open(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer runtime.Close()
	notifications := make(chan struct{}, 1)
	schedule := func(ctx context.Context) error {
		select {
		case notifications <- struct{}{}:
		default:
		}
		return nil
	}
	loopDone := make(chan struct{})
	go func() { defer close(loopDone); runtime.LocalLoop(ctx, notifications) }()
	server := wake.Server(ctx, net.JoinHostPort("127.0.0.1", env.Get("PORT", "8080")), runtime.Handler(schedule, true))
	log.Printf("Nori durable conversation on http://%s model=%s; local processing recovers from Firestore", server.Addr, runtime.Model)
	wake.Listen(ctx, server, 5*time.Second)
	stop()
	<-loopDone
}
