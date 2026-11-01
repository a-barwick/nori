package main

import (
	"fmt"
	"os"
	"time"

	"nori/internal/env"
	"nori/internal/wake"
)

// config is the worker's release identity and polling windows. It is read per
// run, so a missing release surfaces on the request that needs it.
type config struct {
	build, deployment      string
	pollWindow, idleWindow time.Duration
}

func loadConfig(bounded bool, waker wake.Waker) (config, error) {
	build := os.Getenv("TEMPORAL_BUILD_ID")
	if build == "" {
		return config{}, fmt.Errorf("TEMPORAL_BUILD_ID is required; use an immutable release identifier")
	}
	if bounded && !waker.WorkersNeedWake() {
		return config{}, fmt.Errorf("request worker requires durable worker wake configuration")
	}
	pollWindow, err := time.ParseDuration(env.Get("NORI_WORKER_POLL_WINDOW", "240s"))
	if err != nil || pollWindow <= 0 || pollWindow > 240*time.Second {
		return config{}, fmt.Errorf("NORI_WORKER_POLL_WINDOW must be in (0,240s]")
	}
	idleWindow, err := time.ParseDuration(env.Get("NORI_WORKER_IDLE_WINDOW", "15s"))
	if err != nil || idleWindow <= 0 {
		return config{}, fmt.Errorf("NORI_WORKER_IDLE_WINDOW must be positive")
	}
	return config{build: build, deployment: env.Get("TEMPORAL_DEPLOYMENT_NAME", "nori-task"), pollWindow: pollWindow, idleWindow: idleWindow}, nil
}
