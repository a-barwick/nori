package platform

import (
	"context"
	"encoding/json"
	"fmt"

	"nori/internal/dispatch"
	"nori/internal/secret"
	"nori/internal/secret/secretmanager"
	"nori/internal/settings"
	"nori/internal/spend"
	"nori/internal/state"
	"nori/internal/task"
	"nori/internal/wake"
	"nori/internal/wake/cloudtasks"
)

// Waker returns the wake adapter this process is configured for. Cloud Tasks
// is the only adapter today; a process with nothing to wake gets wake.Disabled.
//
// withConversation requires the hosted conversation wake (NORI_WAKE_QUEUE,
// NORI_RUNTIME_URL), which only a process that sends it may ask for. Worker
// wakes are optional: without NORI_WORKER_SERVICES, workers poll on their own.
// NORI_WAKE_SERVICE_ACCOUNT names the caller of both. TEMPORAL_LOCAL=true admits
// loopback targets, and CLOUD_TASKS_EMULATOR_HOST selects a loopback emulator.
func Waker(ctx context.Context, s settings.Settings, withConversation bool) (wake.Waker, error) {
	cfg := cloudtasks.Config{Local: s.Temporal.Local}
	caller := s.Wake.Caller
	if withConversation {
		cfg.Conversation = &cloudtasks.Conversation{
			Queue: s.Wake.ConversationQueue, InvokeRoot: s.Wake.RuntimeURL, Caller: caller,
		}
	}
	if raw := s.Wake.WorkerServices; raw != "" {
		var services map[string]string
		if err := json.Unmarshal([]byte(raw), &services); err != nil {
			return nil, fmt.Errorf("invalid NORI_WORKER_SERVICES: %w", err)
		}
		if len(services) == 0 {
			return nil, fmt.Errorf("NORI_WORKER_SERVICES must contain immutable worker routes")
		}
		cfg.Workers = &cloudtasks.Workers{Queue: s.Wake.WorkerQueue, Caller: caller, Services: services}
		cfg.Emulator = s.Wake.Emulator
	}
	if cfg.Conversation == nil && cfg.Workers == nil {
		return wake.Disabled{}, nil
	}
	waker, err := cloudtasks.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("wake configuration (NORI_WAKE_QUEUE, NORI_RUNTIME_URL, NORI_WORKER_SERVICES, "+
			"NORI_WORKER_WAKE_QUEUE, NORI_WAKE_SERVICE_ACCOUNT, CLOUD_TASKS_EMULATOR_HOST): %w", err)
	}
	return waker, nil
}

// Secrets returns the secret store this deployment keeps credentials in.
func Secrets() secret.Lookup { return secretmanager.Lookup{} }

// Engine connects to the task engine that Configure set up.
func Engine(ctx context.Context) (*task.Engine, error) {
	c, err := task.Dial(ctx, Secrets())
	if err != nil {
		return nil, err
	}
	return task.NewEngine(c), nil
}

// Dispatcher assembles the reconciliation pass every delivering process shares.
func Dispatcher(s settings.Settings, store state.ConversationStore, engine dispatch.Orchestrator, waker wake.Waker) *dispatch.Dispatcher {
	return &dispatch.Dispatcher{
		Store: store, Engine: engine, Waker: waker,
		Recovery: task.Recovery{Store: store, Engine: engine, Computer: Computer(s)},
	}
}

// Configure hands the packages that keep process-wide state their settings: the Temporal connection and the spend
// allowance. Every process calls it once, right after settings.Load.
func Configure(s settings.Settings) {
	task.Configure(s.Temporal)
	spend.Configure(s)
}
