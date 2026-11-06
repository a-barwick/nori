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
func register(ctx context.Context, s settings.Settings) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	build, deployment := s.Temporal.BuildID, s.Temporal.DeploymentName
	if build == "" || deployment == "" {
		return fmt.Errorf("registration requires an immutable build and deployment")
	}
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
	return taskworker.Register(ctx, c, deployment, build)
}
