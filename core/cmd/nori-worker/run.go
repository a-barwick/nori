package main

import (
	"context"
	"log"
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
	slice := &workerservice.Slice{
		ID: sliceID, Build: cfg.build, Known: known, Bounded: bounded,
		PollWindow: cfg.pollWindow, IdleWindow: cfg.idleWindow, Waker: waker,
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
	c, err := task.Dial(ctx, platform.Secrets())
	if err != nil {
		return err
	}
	defer c.Close()
	group := &userWorkers{directory: directory, cfg: s, client: c, waker: waker, entries: map[users.User]*userWorker{}}
	defer group.Close()
	activities := &task.Activities{Resolve: group.Activity}
	steps := dispatch.Commands | dispatch.Publications
	if !s.Temporal.Local {
		steps |= dispatch.ConversationWake
	}
	stopTimeout := 25 * time.Second
	if bounded {
		stopTimeout = 5 * time.Minute
	}
	worker, err := taskworker.Start(c, taskworker.Options{Deployment: cfg.deployment, Build: cfg.build, StopTimeout: stopTimeout}, activities, known...)
	if err != nil {
		return err
	}
	defer func() { result = slice.Finish(worker, group, result) }()
	if err = group.Reconcile(ctx, dispatch.RepairClosed); err != nil {
		if bounded {
			return err
		}
		log.Printf("worker reconciliation: %v", err)
	}
	return slice.Loop(ctx, worker, group, steps)
}
