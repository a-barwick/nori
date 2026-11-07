package main

import (
	"context"
	"errors"
	"log"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/wake/awssqs"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	s, err := settings.Load()
	if err != nil {
		log.Fatal(err)
	}
	if s.Cloud != "aws" {
		log.Fatal("nori-wake-consumer requires NORI_CLOUD=aws")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := platform.AWSWakes(s, true)
	if err != nil {
		log.Fatal(err)
	}
	if err = cfg.Auth.Validate(); err != nil {
		log.Fatal(err)
	}
	w, err := awssqs.Open(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	if err = w.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
