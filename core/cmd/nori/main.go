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
	queue, err := wake.NewCloud(ctx, os.Getenv("NORI_WAKE_QUEUE"), os.Getenv("NORI_RUNTIME_URL"), os.Getenv("NORI_WAKE_SERVICE_ACCOUNT"))
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: net.JoinHostPort(wake.Env("BIND_HOST", "127.0.0.1"), wake.Env("PORT", "8080")), Handler: runtime.Handler(queue.Schedule, false), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	// Only request-bound /api/process performs outer work in hosted mode. Cloud
	// Tasks owns retry delivery across scale-to-zero and process termination.
	log.Printf("Nori durable conversation listening on %s model=%s", server.Addr, runtime.Model)
	serving := make(chan error, 1)
	go func() { serving <- server.ListenAndServe() }()
	select {
	case err := <-serving:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
}
