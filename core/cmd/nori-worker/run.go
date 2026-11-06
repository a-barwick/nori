package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nori/internal/agent"
	"nori/internal/agent/openrouter"
	"nori/internal/dispatch"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/task"
	"nori/internal/taskworker"
	"nori/internal/wake"
	"nori/internal/workerservice"
)

// run is one worker lifetime. A bounded run belongs to one wake request and ends
// when its build has no work left; an unbounded run polls until ctx ends.
func run(ctx context.Context, s settings.Settings, waker wake.Waker, sliceID string, known []string, bounded bool) (result error) {
	cfg, err := loadConfig(s, bounded, waker)
	if err != nil {
		return err
	}
	slice := &workerservice.Slice{
		ID: sliceID, Build: cfg.build, Known: known, Bounded: bounded,
		PollWindow: cfg.pollWindow, IdleWindow: cfg.idleWindow, Waker: waker,
	}
	ctx, cancel := slice.Poll(ctx)
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
	if err = control.Check(); err != nil {
		return slice.Paused(ctx)
	}
	c, err := task.Dial(ctx, platform.Secrets())
	if err != nil {
		return err
	}
	defer c.Close()
	key := s.Models.OpenRouterKey
	if key == "" {
		return fmt.Errorf("OPENROUTER_API_KEY must be configured for the task worker")
	}
	local := s.Temporal.Local
	if s.Computer.LocalURL != "" && !local {
		return fmt.Errorf("NORI_LOCAL_COMPUTER_URL requires TEMPORAL_LOCAL=true")
	}
	// A hosted worker wakes the conversation after saving a task event, and
	// retries that wake on its own tick. A local worker leaves both to the API.
	steps := dispatch.Commands | dispatch.Publications
	model := openrouter.New(key, s.Models.Inner)
	if address := s.Test.ModelURL; address != "" {
		if !local || !strings.HasPrefix(address, "http://127.0.0.1:") {
			return fmt.Errorf("NORI_E2E_MODEL_URL requires TEMPORAL_LOCAL=true and a 127.0.0.1 address")
		}
		model.Endpoint = address
	}
	service := agent.New(store, model, platform.Computer(s))
	service.Capabilities = cfg.capabilities
	activities := &task.Activities{Agent: service, Store: store}
	if !local {
		activities.Notify = waker.WakeConversation
		steps |= dispatch.ConversationWake
	}
	stopTimeout := 25 * time.Second
	if bounded {
		stopTimeout = 5 * time.Minute
	}
	dispatcher := platform.Dispatcher(s, store, task.NewEngine(c), waker)
	worker, err := taskworker.Start(c, taskworker.Options{Deployment: cfg.deployment, Build: cfg.build, StopTimeout: stopTimeout}, activities, known...)
	if err != nil {
		return err
	}
	defer func() { result = slice.Finish(worker, dispatcher, result) }()
	if err = dispatcher.Reconcile(ctx, dispatch.RepairClosed); err != nil {
		if bounded {
			return err
		}
		log.Printf("worker reconciliation: %v", err)
	}
	return slice.Loop(ctx, worker, dispatcher, steps)
}
