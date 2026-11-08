package main

import (
	"context"
	"time"

	"nori/internal/dispatch"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/task"
	"nori/internal/taskworker"
	"nori/internal/users"
	"nori/internal/wake"
	"nori/internal/workerservice"
)

// run is one worker lifetime. A bounded run belongs to one wake request and ends
// when its build has no work left; an unbounded run polls until ctx ends.
func run(ctx context.Context, s settings.Settings, cfg config, waker wake.Waker, sliceID string, known []string, bounded bool) (result error) {
	steps := dispatch.Commands | dispatch.Publications
	if !s.Temporal.Local {
		steps |= dispatch.ConversationWake
	}
	slice := &workerservice.Slice{
		ID: sliceID, Build: cfg.build, Known: known, Bounded: bounded,
		PollWindow: cfg.pollWindow, IdleWindow: cfg.idleWindow, Waker: waker,
		Steps: steps,
	}
	ctx, cancel := slice.Poll(ctx)
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
	if paused {
		return slice.Paused(ctx)
	}
	c, err := task.Dial(ctx, s.Temporal, platform.Secrets(s))
	if err != nil {
		return err
	}
	defer c.Close()
	group := &userWorkers{directory: directory, cfg: s, client: c, waker: waker, entries: map[users.User]*userWorker{}}
	defer group.Close()
	stopTimeout := 25 * time.Second
	if bounded {
		stopTimeout = 5 * time.Minute
	}
	worker, err := taskworker.Start(c, taskworker.Options{
		TaskQueue: s.Temporal.TaskQueue, Deployment: cfg.deployment, Build: cfg.build, StopTimeout: stopTimeout,
	}, group.Activity, known...)
	if err != nil {
		return err
	}
	defer func() { result = slice.Finish(worker, group, result) }()
	return slice.Loop(ctx, worker, group)
}
