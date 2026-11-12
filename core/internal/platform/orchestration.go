package platform

import (
	"context"
	"fmt"
	"sync"

	"go.temporal.io/sdk/client"
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
		cfg, err := AWSWakes(s, withConversation)
		if err != nil {
			return nil, err
		}
		if !withConversation && len(cfg.Workers) == 0 {
			return wake.Disabled{}, nil
		}
		return awssqs.Open(ctx, cfg)
	}
	services, err := s.Wake.WorkerServices()
	if err != nil {
		return nil, err
	}
	if !withConversation && len(services) == 0 {
		return wake.Disabled{}, nil
	}
	cfg := cloudtasks.Config{Local: s.Temporal.Local}
	caller := s.Wake.Caller
	if withConversation {
		cfg.Conversation = &cloudtasks.Conversation{
			Queue: s.Wake.ConversationQueue, InvokeRoot: s.Wake.RuntimeURL, Caller: caller,
		}
	}
	if len(services) != 0 {
		cfg.Workers = &cloudtasks.Workers{Queue: s.Wake.WorkerQueue, Caller: caller, Services: services}
		cfg.Emulator = s.Wake.Emulator
	}
	waker, err := cloudtasks.New(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("wake configuration (NORI_WAKE_QUEUE, NORI_RUNTIME_URL, NORI_WORKER_SERVICES, "+
			"NORI_WORKER_WAKE_QUEUE, NORI_WAKE_SERVICE_ACCOUNT, CLOUD_TASKS_EMULATOR_HOST): %w", err)
	}
	return waker, nil
}

func Secrets(s settings.Settings) secret.Lookup {
	switch s.Cloud {
	case "aws":
		return awssecrets.Lookup{Region: s.AWS.Region}
	case "local":
		return nil
	default:
		return secretmanager.Lookup{}
	}
}

func AWSWakes(s settings.Settings, withConversation bool) (awssqs.Config, error) {
	services, err := s.Wake.WorkerServices()
	if err != nil {
		return awssqs.Config{}, err
	}
	cfg := awssqs.Config{Region: s.AWS.Region, QueueURL: s.AWS.WakeQueueURL, Auth: s.ServiceAuth, Local: s.Temporal.Local, Workers: services}
	if withConversation {
		if s.Wake.RuntimeURL == "" {
			return cfg, fmt.Errorf("NORI_RUNTIME_URL must be configured")
		}
		cfg.RuntimeURL = s.Wake.RuntimeURL
	}
	return cfg, nil
}

func Engine(ctx context.Context, s settings.Settings, userID string) (*task.Engine, error) {
	c, err := task.Dial(ctx, s.Temporal, Secrets(s))
	if err != nil {
		return nil, err
	}
	return task.NewEngine(c, s.Temporal, userID), nil
}

// Engines shares one lazy Temporal gRPC client across per-user engines in a process.
type Engines struct {
	cfg settings.Settings
	mu  sync.Mutex
	c   client.Client
}

func NewEngines(s settings.Settings) *Engines {
	return &Engines{cfg: s}
}

func (e *Engines) ForUser(ctx context.Context, userID string) (*task.Engine, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.c == nil {
		c, err := task.Dial(ctx, e.cfg.Temporal, Secrets(e.cfg))
		if err != nil {
			return nil, err
		}
		e.c = c
	}
	return task.NewSharedEngine(e.c, e.cfg.Temporal, userID), nil
}

func (e *Engines) Close() {
	e.mu.Lock()
	c := e.c
	e.c = nil
	e.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// Dispatcher assembles the reconciliation pass every delivering process shares.
func Dispatcher(s settings.Settings, store state.ConversationStore, engine dispatch.Orchestrator, waker wake.Waker, userID string) *dispatch.Dispatcher {
	waker = wake.ForUser(waker, userID)
	return &dispatch.Dispatcher{
		Store: store, Engine: engine, Waker: waker, UserID: userID,
		Recovery:   task.Recovery{Store: store, Engine: engine, Computer: Computer(s)},
		WakeRepair: WorkWakes(store, waker, true),
	}
}

func WorkWakes(store state.Database, waker wake.Waker, tasks bool) *wake.Repairer {
	return &wake.Repairer{Waker: waker, Pending: func(ctx context.Context) (string, error) {
		return store.WorkVersion(ctx, tasks)
	}}
}
