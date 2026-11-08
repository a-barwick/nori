package httpserver

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"nori/internal/observability"
)

func New(ctx context.Context, addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: observability.HTTPHandler(handler),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
}

func Listen(ctx context.Context, server *http.Server, grace time.Duration) error {
	serving := make(chan error, 1)
	go func() { serving <- server.ListenAndServe() }()
	select {
	case err := <-serving:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
	return nil
}
