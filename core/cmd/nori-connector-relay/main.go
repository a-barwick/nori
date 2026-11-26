package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"nori/internal/homeai"
)

func run(ctx context.Context) error {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("NORI_HOME_AI_KEY"))
	if err != nil || len(key) != 32 {
		return errors.New("NORI_HOME_AI_KEY must be a base64-encoded 32-byte key")
	}
	databaseURL := os.Getenv("NORI_HOME_AI_POSTGRES_URL")
	if databaseURL == "" {
		databaseURL = os.Getenv("NORI_POSTGRES_URL")
	}
	listenURL := os.Getenv("NORI_HOME_AI_POSTGRES_LISTEN_URL")
	if listenURL == "" {
		listenURL = os.Getenv("NORI_POSTGRES_LISTEN_URL")
	}
	maxConns := int64(8)
	if raw := os.Getenv("NORI_HOME_AI_MAX_CONNS"); raw != "" {
		maxConns, err = strconv.ParseInt(raw, 10, 32)
		if err != nil || maxConns < 1 || maxConns > 100 {
			return errors.New("NORI_HOME_AI_MAX_CONNS must be between 1 and 100")
		}
	}
	service, err := homeai.Open(ctx, homeai.Options{PostgresURL: databaseURL, PostgresListenURL: listenURL, Key: key, VerificationURI: os.Getenv("NORI_HOME_AI_VERIFY_URL"), MaxConns: int32(maxConns)})
	if err != nil {
		return err
	}
	defer service.Close()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	host := os.Getenv("BIND_HOST")
	handler := service.Handler()
	server := &http.Server{Addr: net.JoinHostPort(host, port), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCtx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		handler.ServeHTTP(w, r.WithContext(requestCtx))
	}), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("relay server: %w", err)
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("home connector relay failed", "error", err)
		os.Exit(1)
	}
}
