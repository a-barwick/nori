package platform

import (
	"context"
	"encoding/json"
	"fmt"

	"nori/internal/dispatch"
	"nori/internal/secret"
	"nori/internal/secret/awssecrets"
	"nori/internal/secret/secretmanager"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/task"
	"nori/internal/wake"
	"nori/internal/wake/awssqs"
	"nori/internal/wake/cloudtasks"
)

// Waker selects Cloud Tasks on GCP, SQS on AWS, or in-process local delivery.
//
// withConversation requires the hosted conversation wake (NORI_WAKE_QUEUE,
// NORI_RUNTIME_URL), which only a process that sends it may ask for. Worker
// wakes are optional: without NORI_WORKER_SERVICES, workers poll on their own.
// NORI_WAKE_SERVICE_ACCOUNT names the caller of both. TEMPORAL_LOCAL=true admits
// loopback targets, and CLOUD_TASKS_EMULATOR_HOST selects a loopback emulator.
func Waker(ctx context.Context, s settings.Settings, withConversation bool) (wake.Waker, error) {
	if s.Cloud == "local" {
		return wake.Disabled{}, nil
	}
	if s.Cloud == "aws" {
		if !withConversation && s.Wake.WorkerServices == "" {
			return wake.Disabled{}, nil
		}
		cfg, err := AWSWakes(s, withConversation)
		if err != nil {
			return nil, err
		}
		return awssqs.Open(ctx, cfg)
	}
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
var configuredSecrets secret.Lookup = secretmanager.Lookup{}

func Secrets() secret.Lookup { return configuredSecrets }

func AWSWakes(s settings.Settings, withConversation bool) (awssqs.Config, error) {
	cfg := awssqs.Config{Region: s.AWS.Region, QueueURL: s.AWS.WakeQueueURL, Auth: s.ServiceAuth, Local: s.Temporal.Local}
	if withConversation {
		if s.Wake.RuntimeURL == "" {
			return cfg, fmt.Errorf("NORI_RUNTIME_URL must be configured")
		}
		cfg.RuntimeURL = s.Wake.RuntimeURL
	}
	if s.Wake.WorkerServices != "" {
		if err := json.Unmarshal([]byte(s.Wake.WorkerServices), &cfg.Workers); err != nil || len(cfg.Workers) == 0 {
			return cfg, fmt.Errorf("invalid NORI_WORKER_SERVICES")
		}
	}
	return cfg, nil
}

// Engine connects to the task engine that Configure set up.
func Engine(ctx context.Context, userID string) (*task.Engine, error) {
	c, err := task.Dial(ctx, Secrets())
	if err != nil {
		return nil, err
	}
	return task.NewEngine(c, userID), nil
}

// Dispatcher assembles the reconciliation pass every delivering process shares.
func Dispatcher(s settings.Settings, store state.ConversationStore, engine dispatch.Orchestrator, waker wake.Waker, userID string) *dispatch.Dispatcher {
	return &dispatch.Dispatcher{
		Store: store, Engine: engine, Waker: wake.ForUser(waker, userID), UserID: userID,
		Recovery: task.Recovery{Store: store, Engine: engine, Computer: Computer(s)},
	}
}

// Configure sets the Temporal connection once at process startup.
func Configure(s settings.Settings) {
	task.Configure(s.Temporal)
	switch s.Cloud {
	case "aws":
		configuredSecrets = awssecrets.Lookup{Region: s.AWS.Region}
	case "local":
		configuredSecrets = nil
	default:
		configuredSecrets = secretmanager.Lookup{}
	}
}
