package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"nori/internal/wake"
)

func main() {
	envFile := wake.Env("NORI_ENV_FILE", "../.env")
	if err := godotenv.Load(envFile); err != nil && !os.IsNotExist(err) {
		log.Fatal("could not load the configured .env; check its syntax and permissions")
	}
	if err := godotenv.Load(wake.Env("NORI_TASK_ENV_FILE", envFile)); err != nil && !os.IsNotExist(err) {
		log.Fatal("could not load task configuration")
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
	server := &http.Server{Addr: net.JoinHostPort("127.0.0.1", wake.Env("PORT", "8080")), Handler: runtime.Handler(schedule, true), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	log.Printf("Nori durable conversation on http://%s model=%s; local processing recovers from Firestore", server.Addr, runtime.Model)
	serving := make(chan error, 1)
	go func() { serving <- server.ListenAndServe() }()
	select {
	case err := <-serving:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-ctx.Done():
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(cleanup); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
	stop()
	<-loopDone
}
