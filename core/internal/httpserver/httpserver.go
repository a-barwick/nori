package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"nori/internal/observability"
)

func New(_ context.Context, addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: observability.HTTPHandler(handler),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 13 * time.Minute, IdleTimeout: 60 * time.Second,
	}
}

func Listen(ctx context.Context, server *http.Server, grace time.Duration) error {
	return runServe(ctx, server, grace, server.ListenAndServe)
}

func Serve(ctx context.Context, server *http.Server, ln net.Listener, grace time.Duration) error {
	return runServe(ctx, server, grace, func() error { return server.Serve(ln) })
}

func runServe(ctx context.Context, server *http.Server, grace time.Duration, serve func() error) error {
	serving := make(chan error, 1)
	go func() { serving <- serve() }()
	select {
	case err := <-serving:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			slog.ErrorContext(ctx, "HTTP server shutdown failed", "addr", server.Addr, "err", err)
		}
	}
	return nil
}
