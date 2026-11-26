package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nori/cmd/internal/bootstrap"
	"nori/internal/settings"
)

func main() {
	s, err := settings.LoadDefaultCloud("local")
	if err != nil {
		slog.Error("failed to load settings", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bootstrap.Run(ctx, bootstrap.Options{
		Settings:      s,
		ServiceName:   "nori-local",
		Role:          "all",
		LocalDirect:   true,
		ShutdownGrace: 5 * time.Second,
	})
}
