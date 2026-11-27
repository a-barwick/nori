package main

import (
	"cmp"
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nori/cmd/internal/bootstrap"
	"nori/internal/observability"
	"nori/internal/settings"
	"nori/internal/state/postgres"
)

func main() {
	roleFlag := flag.String("role", "", "runtime role: all, api, or worker (defaults to NORI_ROLE or all)")
	migrateOnly := flag.Bool("migrate-only", false, "apply and verify Postgres migrations, then exit")
	metricsAddrFlag := flag.String("metrics-addr", "", "optional listen address for operational endpoints (/healthz, /readyz, /metrics) when --role=worker")
	flag.Parse()

	s, err := settings.Load()
	if err != nil {
		slog.Error("failed to load settings", "err", err)
		os.Exit(1)
	}
	role := cmp.Or(*roleFlag, s.Role, "all")
	if !settings.ValidRole(role) {
		slog.Error("invalid runtime role; must be all, api, or worker", "role", role)
		os.Exit(1)
	}
	s.Role = role

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *migrateOnly {
		backend, err := postgres.OpenBackend(ctx, s.Store.PostgresURL)
		if err != nil {
			observability.Fatal(err)
		}
		if err := backend.Close(); err != nil {
			observability.Fatal(err)
		}
		return
	}

	bootstrap.Run(ctx, bootstrap.Options{
		Settings:      s,
		ServiceName:   "nori-core",
		Role:          role,
		MetricsAddr:   *metricsAddrFlag,
		ShutdownGrace: 25 * time.Second,
	})
}
