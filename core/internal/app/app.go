package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"nori/internal/agent"
	"nori/internal/agent/responses"
	"nori/internal/api"
	"nori/internal/chat"
	"nori/internal/conversation"
	"nori/internal/dispatch"
	"nori/internal/platform"
	"nori/internal/scheduler"
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
	Scheduler  *scheduler.Service
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
	outer.TestURL = cfg.Models.TestURL
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
	chats.Profile = platform.Profile(user, services.Connections)
	toolSource := &agent.Service{Extensions: services.Tools, Computer: platform.Computer(cfg)}
	result.Scheduler = services.Scheduler
	result.Assistant = &api.Assistant{
		Store: store, Connections: services.Connections, Permissions: services.Permissions, Tools: toolSource.Tools,
		Scheduler: services.Scheduler, UnattendedWrites: cfg.Connections.UnattendedWrites, PushPublicKey: pushKey(cfg), MCP: services.MCP, Plugins: services.Plugins, Computer: services.Sessions, Chats: chats,
		Features: map[string]bool{"integrations": true, "connections": true, "interactions": true, "permissions": true,
			"preferences": true, "work": !cfg.Features.DisableBackgroundTasks, "operations": true,
			"automations": !cfg.Features.DisableBackgroundTasks, "goals": true, "inbox": true,
			"library": true, "search": true, "memory": true, "mcp": true, "plugins": true, "skills": true, "computer_sessions": !cfg.Features.DisableComputer},
		NextScheduled: services.Scheduler.NextScheduled, InboxUnread: services.Scheduler.UnreadCount,
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
	summarizer.TestURL = cfg.Models.TestURL
	var classifier conversation.IntentClassifier
	if !cfg.Features.DisableJev {
		classifier = conversation.NewJevClassifier(cfg.Models.JevKey)
	}
	runner := conversation.New(outer, classifier, summarizer, tasks)
	runner.UserFacts = userFacts
	runner.Profile = platform.Profile(user, services.Connections)
	runner.Clock = conversation.Clock{Location: location}
	result.Runner, result.Tasks, result.Dispatcher, result.engine = runner, tasks, dispatcher, engine
	result.Assistant.Answer = func(ctx context.Context, commandID, taskID, questionID, text string) error {
		_, err := tasks.AcceptBatch(ctx, []state.Command{{ID: commandID, RunID: taskID, Kind: state.CommandAnswer, QuestionID: questionID, Text: text}}, nil)
		return err
	}
	services.Scheduler.Arm = func(ctx context.Context, at time.Time) {
		if err := wake.At(ctx, waker, at); err != nil && !errors.Is(err, wake.ErrNotConfigured) {
			log.Printf("scheduler: due-time wake could not be armed; the next pass repairs it: %v", err)
		}
	}
	services.Scheduler.Accept = func(ctx context.Context, cmd state.Command) (state.Run, error) {
		receipts, err := tasks.AcceptBatch(ctx, []state.Command{cmd}, nil)
		if err != nil {
			return state.Run{}, err
		}
		return receipts[0].Run, nil
	}
	chats.StartTask = func(ctx context.Context, commandID, chatID, messageID, request string, attachments []state.Attachment) (state.Run, error) {
		receipts, err := tasks.AcceptBatch(ctx, []state.Command{{ID: commandID, RunID: commandID, Kind: state.CommandStart, Text: request, Attachments: attachments,
			Origin: &state.TaskOrigin{ConversationID: chatID, MessageID: messageID}}}, nil)
		if err != nil {
			return state.Run{}, err
		}
		return receipts[0].Run, nil
	}
	result.Assistant.Resume = result.Resume
	result.Assistant.DeliverAnswers = result.deliverAnswers
	if result.Assistant.Connections != nil {
		// A completed sign-in resumes the tasks that were waiting for it.
		result.Assistant.Connections.OnConnected = result.Assistant.ResolveSignIns
	}
	if result.Assistant.MCP != nil {
		result.Assistant.MCP.OnSignedIn = result.Assistant.ResolveSignIns
	}
	result.Assistant.Continue = func(ctx context.Context, taskID, previous, request string, budget float64) (state.Run, error) {
		receipts, err := tasks.AcceptBatch(ctx, []state.Command{{ID: taskID, RunID: taskID, Kind: state.CommandStart, Text: request, BudgetUSD: budget,
			Origin: &state.TaskOrigin{ConversationID: "assistant", ContinuesTaskID: previous}}}, nil)
		if err != nil {
			return state.Run{}, err
		}
		return receipts[0].Run, nil
	}
	result.Assistant.Submit = func(ctx context.Context, taskID, request string) (state.Run, error) {
		receipts, err := tasks.AcceptBatch(ctx, []state.Command{{ID: taskID, RunID: taskID, Kind: state.CommandStart, Text: request,
			Origin: &state.TaskOrigin{ConversationID: "external"}}}, nil)
		if err != nil {
			return state.Run{}, err
		}
		return receipts[0].Run, nil
	}
	result.Assistant.Revise = func(ctx context.Context, taskID, resultID, instruction string) (state.Run, error) {
		receipts, err := tasks.AcceptBatch(ctx, []state.Command{{ID: taskID, RunID: taskID, Kind: state.CommandStart, Text: instruction, Revises: resultID}}, nil)
		if err != nil {
			return state.Run{}, err
		}
		return receipts[0].Run, nil
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

// Resume repairs delivery and, through the same fan-out, any due automation work whose wake was lost.
func (a *App) Resume(ctx context.Context) error {
	if a.Dispatcher == nil {
		return nil
	}
	return errors.Join(a.Dispatcher.Resume(ctx), a.Schedule(ctx))
}

func (a *App) RepairWake(ctx context.Context) error { return a.WakeRepair.Repair(ctx) }

func (a *App) Process(ctx context.Context) error {
	if a.Runner == nil {
		return a.Chats.Process(ctx)
	}
	chatDone := make(chan error, 1)
	go func() { chatDone <- a.Chats.Process(ctx) }()
	assistantErr := errors.Join(a.processAssistant(ctx), a.Schedule(ctx))
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

// Schedule turns due automation slots into tasks, settles finished occurrences and delivers results. Duplicate or
// lost passes are harmless: every step is keyed by a stable occurrence identity.
func (a *App) Schedule(ctx context.Context) error {
	if a.Scheduler == nil || a.Scheduler.Accept == nil {
		return nil
	}
	_, err := a.Scheduler.Tick(ctx)
	return errors.Join(err, a.Chats.DeliverTasks(ctx, a.Store.GetRun), a.deliverAnswers(ctx))
}

// deliverAnswers retries answer commands for interactions resolved before their task asked. An answer for a task
// that ended or moved to a newer instruction is retired: the newer revision supersedes it.
func (a *App) deliverAnswers(ctx context.Context) error {
	if a.Assistant == nil || a.Assistant.Permissions == nil || a.Assistant.Answer == nil {
		return nil
	}
	answers, ids, err := a.Assistant.Permissions.UndeliveredAnswers(ctx)
	if err != nil {
		return err
	}
	for i, answer := range answers {
		run, runErr := a.Store.GetRun(ctx, answer.TaskID)
		waiting := runErr == nil && run.Status == state.RunRunning && run.Question != nil && run.Question.ID == answer.Question && run.Question.ResolvedBy == ""
		if answer.Recheck && !waiting {
			// Recorded as delivered and the task is not waiting on it: nothing to repair.
			continue
		}
		err := a.Assistant.Answer(ctx, answer.CommandID, answer.TaskID, answer.Question, answer.Text)
		if errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy) {
			// Still running: the task has not asked yet or is mid-step, so try again on the next pass.
			if current, e := a.Store.GetRun(ctx, answer.TaskID); e == nil && current.Status == state.RunRunning && (current.Question == nil || current.Question.ID == answer.Question) {
				continue
			}
		}
		if (err == nil || errors.Is(err, state.ErrDuplicate) || errors.Is(err, state.ErrQuestionStale) || errors.Is(err, state.ErrBusy)) && !answer.Recheck {
			a.Assistant.Permissions.MarkDelivered(ctx, ids[i])
		}
	}
	return nil
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

// pushKey is the VAPID public key when push delivery is fully configured.
func pushKey(cfg settings.Settings) string {
	c := cfg.Connections
	if c.VAPIDPublicKey == "" || c.VAPIDPrivateKey == "" || c.VAPIDSubject == "" {
		return ""
	}
	return c.VAPIDPublicKey
}
