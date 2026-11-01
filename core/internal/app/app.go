// Package app assembles Nori's durable conversation and runs its outer work.
// The hosted and local entrypoints share it; only their wake differs.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"nori/internal/api"
	"nori/internal/conversation"
	"nori/internal/dispatch"
	"nori/internal/env"
	"nori/internal/platform"
	"nori/internal/spend"
	"nori/internal/wake"
)

type App struct {
	Runner     *conversation.Runner
	Tasks      *conversation.Tasks
	Dispatcher *dispatch.Dispatcher
	Waker      wake.Waker
	Model      string
	engine     dispatch.Orchestrator
}

// Open uses the same durable outer loop in local and hosted entrypoints. waker is
// how this process wakes itself and its workers.
func Open(ctx context.Context, waker wake.Waker) (*App, error) {
	if err := spend.ValidateConfiguration(); err != nil {
		return nil, err
	}
	key, jevKey := os.Getenv("OPENAI_API_KEY"), os.Getenv("JEV_API_KEY")
	if strings.TrimSpace(jevKey) == "" {
		return nil, fmt.Errorf("JEV_API_KEY must be configured in the selected project environment")
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY must be configured in the selected project environment")
	}
	store, err := platform.OpenStore(ctx)
	if err != nil {
		return nil, err
	}
	engine, err := platform.Engine(ctx)
	if err != nil {
		store.Close()
		return nil, err
	}
	localComputer := os.Getenv("NORI_LOCAL_COMPUTER_URL")
	if localComputer != "" && os.Getenv("TEMPORAL_LOCAL") != "true" {
		engine.Close()
		store.Close()
		return nil, fmt.Errorf("NORI_LOCAL_COMPUTER_URL requires TEMPORAL_LOCAL=true")
	}
	dispatcher := platform.Dispatcher(store, engine, waker)
	tasks := &conversation.Tasks{Store: store, Dispatcher: dispatcher}
	model := env.Get("NORI_AGENT_MODEL", "gpt-6.1-sol")
	outer := conversation.NewOpenAIModel(key, model)
	if address := os.Getenv("NORI_E2E_OPENAI_URL"); address != "" {
		if os.Getenv("TEMPORAL_LOCAL") != "true" || !strings.HasPrefix(address, "http://127.0.0.1:") {
			engine.Close()
			store.Close()
			return nil, fmt.Errorf("NORI_E2E_OPENAI_URL requires TEMPORAL_LOCAL=true and a 127.0.0.1 address")
		}
		outer.URL = address
	}
	runner := conversation.New(outer, conversation.NewJevClassifier(jevKey), tasks)
	return &App{Runner: runner, Tasks: tasks, Dispatcher: dispatcher, Waker: waker, Model: model, engine: engine}, nil
}

func (a *App) Close() {
	a.engine.Close()
	a.Tasks.Store.Close()
}

// API returns what the HTTP routes call into.
func (a *App) API() api.Deps {
	return api.Deps{Model: a.Model, Runner: a.Runner, Tasks: a.Tasks, Waker: a.Waker, Ops: a}
}

func (a *App) Resume(ctx context.Context) error { return a.Dispatcher.Resume(ctx) }

func (a *App) Process(ctx context.Context) error {
	// A paused worker cannot finish a queued Stop. Retry only accepted Stop
	// targets here, including a timed-out cleanup from the submitting request.
	var stopErr error
	store := a.Tasks.Store
	control, err := store.Control(ctx)
	if err != nil {
		return err
	}
	if control.Paused {
		runs, err := store.OpenTasks(ctx)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.CancelRequested {
				cleanup, cancel := context.WithTimeout(ctx, 10*time.Second)
				_, err := a.Dispatcher.StopPaused(cleanup, run.ID)
				cancel()
				stopErr = errors.Join(stopErr, err)
			}
		}
	}
	processErr := a.Runner.Process(ctx)
	// Dispatch and explicit controls can outlive their first delivery attempt.
	// Keep the queue notification until delivery is confirmed.
	delivery, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reconcileErr := a.Dispatcher.Reconcile(delivery, dispatch.Commands|dispatch.Publications)
	return errors.Join(stopErr, processErr, reconcileErr)
}

// LocalLoop is tied to the application, so browser cancellation and reconnects
// cannot cancel accepted work. The periodic check also recovers after restart.
func (a *App) LocalLoop(ctx context.Context, notifications <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		process, cancel := context.WithTimeout(ctx, 9*time.Minute)
		if err := a.Process(process); err != nil && ctx.Err() == nil {
			log.Printf("conversation processing will retry: %v", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-notifications:
		case <-ticker.C:
		}
	}
}

var _ api.Operations = (*App)(nil)
