package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"nori/internal/agent"
	"nori/internal/agent/openrouter"
	"nori/internal/dispatch"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/task"
	"nori/internal/users"
	"nori/internal/wake"
)

type userWorkers struct {
	directory users.Directory
	cfg       settings.Settings
	client    client.Client
	waker     wake.Waker
	mu        sync.Mutex
	entries   map[users.User]*userWorker
}
type userWorker struct {
	activities *task.Activities
	dispatcher *dispatch.Dispatcher
	store      state.ConversationStore
}

func (g *userWorkers) get(ctx context.Context, row users.User) (*userWorker, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if entry := g.entries[row]; entry != nil {
		return entry, nil
	}
	cfg := platform.ForUser(g.cfg, row)
	if cfg.Computer.LocalURL != "" {
		if err := platform.CheckComputer(cfg); err != nil {
			return nil, err
		}
	}
	caps, err := capabilities(cfg)
	if err != nil {
		return nil, err
	}
	store, err := platform.OpenStore(ctx, cfg, row)
	if err != nil {
		return nil, err
	}
	model := openrouter.New(cfg.Models.OpenRouterKey, cfg.Models.Inner)
	service := agent.New(store, model, platform.Computer(cfg))
	service.Capabilities = caps
	service.UserFacts = row.Facts()
	waker := wake.ForUser(g.waker, row.ID)
	activities := &task.Activities{Agent: service, Store: store}
	if !cfg.Temporal.Local {
		activities.Notify = waker.WakeConversation
	}
	entry := &userWorker{activities: activities, store: store, dispatcher: platform.Dispatcher(cfg, store, task.NewEngine(g.client, row.ID), waker, row.ID)}
	g.entries[row] = entry
	return entry, nil
}
func (g *userWorkers) Activity(ctx context.Context) (*task.Activities, error) {
	id, _, ok := dispatch.WorkflowUser(activity.GetInfo(ctx).WorkflowExecution.ID)
	if !ok {
		return nil, fmt.Errorf("activity has no valid user identity")
	}
	row, err := g.directory.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	entry, err := g.get(ctx, row)
	if err != nil {
		return nil, err
	}
	return entry.activities, nil
}
func (g *userWorkers) Control(ctx context.Context) (state.Control, error) {
	paused, err := g.directory.Paused(ctx)
	return state.Control{Paused: paused}, err
}
func (g *userWorkers) Reconcile(ctx context.Context, steps dispatch.Steps) error {
	rows, err := g.directory.List(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, row := range rows {
		entry, err := g.get(ctx, row)
		if err == nil {
			err = entry.dispatcher.Reconcile(ctx, steps)
		}
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
func (g *userWorkers) Runnable(ctx context.Context, build string, known []string) ([]string, error) {
	rows, err := g.directory.List(ctx)
	if err != nil {
		return nil, err
	}
	owners := map[string]bool{}
	for _, row := range rows {
		owners[row.ID] = true
	}
	for _, id := range known {
		owner, _, ok := dispatch.WorkflowUser(id)
		if !ok || !owners[owner] {
			return nil, fmt.Errorf("retained workflow has no known user")
		}
	}
	var pending []string
	for _, row := range rows {
		entry, err := g.get(ctx, row)
		if err != nil {
			return nil, err
		}
		ids, err := entry.dispatcher.Runnable(ctx, build, known)
		if err != nil {
			return nil, err
		}
		pending = append(pending, ids...)
	}
	return pending, nil
}
func (g *userWorkers) Close() {
	for _, entry := range g.entries {
		_ = entry.store.Close()
	}
}
