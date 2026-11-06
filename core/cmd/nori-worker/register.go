package main

import (
	"context"
	"fmt"
	"time"

	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/task"
	"nori/internal/taskworker"
)

// register creates a fresh worker deployment version without running any task.
// It needs paused admission so that no execution can reach the new version.
func register(ctx context.Context, s settings.Settings, cfg config) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	store, err := platform.OpenStore(ctx, s)
	if err != nil {
		return err
	}
	defer store.Close()
	control, err := store.Control(ctx)
	if err != nil {
		return err
	}
	if !control.Paused {
		return fmt.Errorf("registration requires paused execution admission")
	}
	c, err := task.Dial(ctx, platform.Secrets())
	if err != nil {
		return err
	}
	defer c.Close()
	return taskworker.Register(ctx, c, cfg.deployment, cfg.build)
}
