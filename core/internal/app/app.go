package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"nori/internal/agent"
	"nori/internal/agent/responses"
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
	WakeRepair *wake.Repairer
	Model      string
	engine     dispatch.Orchestrator
	handler    http.Handler
	Assistant  *api.Assistant
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
	services, err := platform.Assistant(cfg, user, store)
	if err != nil {
		store.Close()
		return nil, err
	}
	toolSource := &agent.Service{Extensions: services.Connections, Computer: platform.Computer(cfg)}
	result.Assistant = &api.Assistant{
		Store: store, Connections: services.Connections, Permissions: services.Permissions, Tools: toolSource.Tools,
		Features: map[string]bool{"integrations": true, "connections": true, "interactions": true, "permissions": true,
			"preferences": true, "work": !cfg.Features.DisableBackgroundTasks, "operations": true},
	}
	result.WakeRepair = platform.WorkWakes(store, waker, !cfg.Features.DisableBackgroundTasks)
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
	dispatcher.WakeRepair = result.WakeRepair
	tasks := &conversation.Tasks{Store: store, Dispatcher: dispatcher}
	_, taskKey := cfg.Models.TaskCredential()
	summaryModel := "openai/gpt-6.1-terra"
	if cfg.Models.UsesOpenAIForTasks() {
		summaryModel = cfg.Models.Inner
	}
	summarizer := responses.New(taskKey, summaryModel, store)
	var classifier conversation.IntentClassifier
	if !cfg.Features.DisableJev {
		classifier = conversation.NewJevClassifier(cfg.Models.JevKey)
	}
	runner := conversation.New(outer, classifier, summarizer, tasks)
	runner.UserFacts = userFacts
	runner.Clock = conversation.Clock{Location: location}
	result.Runner, result.Tasks, result.Dispatcher, result.engine = runner, tasks, dispatcher, engine
	result.Assistant.Answer = func(ctx context.Context, commandID, taskID, questionID, text string) error {
		_, err := tasks.AcceptBatch(ctx, []state.Command{{ID: commandID, RunID: taskID, Kind: state.CommandAnswer, QuestionID: questionID, Text: text}}, nil)
		return err
	}
	result.Assistant.Stop = func(ctx context.Context, commandID, taskID string) (state.Run, error) {
		receipts, err := tasks.AcceptBatch(ctx, []state.Command{{ID: commandID, RunID: taskID, Kind: state.CommandStop}}, nil)
		if err != nil {
			return state.Run{}, err
		}
		return receipts[0].Run, nil
	}
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
	return api.Deps{Chats: a.Chats, Model: a.Model, Runner: a.Runner, Tasks: a.Tasks, Waker: a.Waker, Ops: a, Features: a.Features, Assistant: a.Assistant}
}

func (a *App) Resume(ctx context.Context) error {
	if a.Dispatcher == nil {
		return nil
	}
	return a.Dispatcher.Resume(ctx)
}

func (a *App) RepairWake(ctx context.Context) error { return a.WakeRepair.Repair(ctx) }

func (a *App) Process(ctx context.Context) error {
	if a.Runner == nil {
		return a.Chats.Process(ctx)
	}
	chatDone := make(chan error, 1)
	go func() { chatDone <- a.Chats.Process(ctx) }()
	assistantErr := a.processAssistant(ctx)
	if err := errors.Join(<-chatDone, assistantErr); err != nil {
		return err
	}
	// A command inserted behind an outbox scan's cursor, or input committed
	// while another lane drained, still needs this queue request to retry.
	pending, err := a.Store.WorkVersion(ctx, true)
	if err == nil && pending != "" {
		err = state.ErrConversationBusy
	}
	return err
}

func (a *App) processAssistant(ctx context.Context) error {
	if a.Runner == nil {
		return nil
	}
	// Advance already accepted tasks before doing potentially slow model work.
	// The same reconciliation boundary is used by worker startup and recovery.
	before, cancel := context.WithTimeout(ctx, 10*time.Second)
	beforeErr := a.Dispatcher.Reconcile(before, dispatch.RepairClosed|dispatch.Commands|dispatch.Publications)
	cancel()
	processErr := a.Runner.Process(ctx)
	// Dispatch and explicit controls can outlive their first delivery attempt.
	// Keep the queue notification until delivery is confirmed.
	delivery, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reconcileErr := a.Dispatcher.Reconcile(delivery, dispatch.RepairClosed|dispatch.Commands|dispatch.Publications)
	return errors.Join(beforeErr, processErr, reconcileErr)
}

var _ api.Operations = (*App)(nil)
