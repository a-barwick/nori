package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"nori/internal/agent/openrouter"
	"nori/internal/api"
	"nori/internal/chat"
	"nori/internal/conversation"
	"nori/internal/dispatch"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/users"
	"nori/internal/wake"
)

type App struct {
	Store      state.ConversationStore
	Features   settings.Features
	Chats      *chat.Service
	Runner     *conversation.Runner
	Tasks      *conversation.Tasks
	Dispatcher *dispatch.Dispatcher
	Waker      wake.Waker
	Model      string
	engine     dispatch.Orchestrator
	handler    http.Handler
}

func Open(ctx context.Context, waker wake.Waker, cfg settings.Settings, user users.User) (*App, error) {
	cfg = platform.ForUser(cfg, user)
	waker = wake.ForUser(waker, user.ID)
	if err := validateStartup(cfg); err != nil {
		return nil, err
	}
	key := cfg.Models.OpenAIKey
	store, err := platform.OpenStore(ctx, cfg, user)
	if err != nil {
		return nil, err
	}
	model := cfg.Models.Outer
	outer := conversation.NewOpenAIModel(key, model)
	location, err := time.LoadLocation(cfg.TimeZone)
	if err != nil {
		store.Close()
		return nil, fmt.Errorf("user time zone must be an IANA time zone")
	}
	userFacts := user.Facts()
	chats := chat.New(store, outer, userFacts)
	result := &App{Store: store, Features: cfg.Features, Chats: chats, Waker: waker, Model: model}
	if cfg.Features.DisableBackgroundTasks {
		return result, nil
	}
	engine, err := platform.Engine(ctx, cfg, user.ID)
	if err != nil {
		store.Close()
		return nil, err
	}
	if cfg.Computer.LocalURL != "" && !cfg.Temporal.Local && !cfg.Features.DisableComputer {
		engine.Close()
		store.Close()
		return nil, fmt.Errorf("user computer local URL requires TEMPORAL_LOCAL=true")
	}
	dispatcher := platform.Dispatcher(cfg, store, engine, waker, user.ID)
	tasks := &conversation.Tasks{Store: store, Dispatcher: dispatcher}
	// Compaction keeps a fixed model even when the task worker uses a different one.
	summarizer := openrouter.New(cfg.Models.OpenRouterKey, "openai/gpt-6.1-terra", store)
	var classifier conversation.IntentClassifier
	if !cfg.Features.DisableJev {
		classifier = conversation.NewJevClassifier(cfg.Models.JevKey)
	}
	runner := conversation.New(outer, classifier, summarizer, tasks)
	runner.UserFacts = userFacts
	runner.Clock = conversation.Clock{Location: location}
	result.Runner, result.Tasks, result.Dispatcher, result.engine = runner, tasks, dispatcher, engine
	return result, nil
}

func (a *App) Close() {
	a.Chats.Close()
	if a.engine != nil {
		a.engine.Close()
	}
	a.Store.Close()
}

func (a *App) apiDeps() api.Deps {
	return api.Deps{Chats: a.Chats, Model: a.Model, Runner: a.Runner, Tasks: a.Tasks, Waker: a.Waker, Ops: a, Features: a.Features}
}

func (a *App) Resume(ctx context.Context) error {
	if a.Dispatcher == nil {
		return nil
	}
	return a.Dispatcher.Resume(ctx)
}

func (a *App) Process(ctx context.Context) error {
	if a.Runner == nil {
		return a.Chats.Process(ctx)
	}
	chatDone := make(chan error, 1)
	go func() { chatDone <- a.Chats.Process(ctx) }()
	assistantErr := a.processAssistant(ctx)
	return errors.Join(<-chatDone, assistantErr)
}

func (a *App) processAssistant(ctx context.Context) error {
	if a.Runner == nil {
		return nil
	}
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

var _ api.Operations = (*App)(nil)
