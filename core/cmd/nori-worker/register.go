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
	directory, err := platform.OpenUsers(ctx, s)
	if err != nil {
		return err
	}
	defer directory.Close()
	paused, err := directory.Paused(ctx)
	if err != nil {
		return err
	}
	if !paused {
		return fmt.Errorf("registration requires paused execution admission")
	}
	c, err := task.Dial(ctx, s.Temporal, platform.Secrets(s))
	if err != nil {
		return err
	}
	defer c.Close()
	return taskworker.Register(ctx, c, s.Temporal.TaskQueue, cfg.deployment, cfg.build)
}
