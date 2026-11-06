// Package app assembles Nori's durable conversation and runs its outer work.
// The hosted and local entrypoints share it; only their wake differs.
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"nori/internal/agent"
	"nori/internal/agent/openrouter"
	"nori/internal/api"
	"nori/internal/chat"
	"nori/internal/conversation"
	"nori/internal/dispatch"
	"nori/internal/platform"
	"nori/internal/proactive"
	"nori/internal/schedules"
	"nori/internal/settings"
	"nori/internal/spend"
	"nori/internal/state"
	"nori/internal/wake"
)

type App struct {
	Chats      *chat.Service
	Runner     *conversation.Runner
	Tasks      *conversation.Tasks
	Dispatcher *dispatch.Dispatcher
	Waker      wake.Waker
	Model      string
	Proactive  *proactive.Service
	Schedules  *schedules.Service
	engine     dispatch.Orchestrator
}

// Open uses the same durable outer loop in local and hosted entrypoints. waker is
// how this process wakes itself and its workers.
func Open(ctx context.Context, waker wake.Waker, cfg settings.Settings) (*App, error) {
	if err := spend.ValidateConfiguration(); err != nil {
		return nil, err
	}
	jevKey, err := settings.Required("JEV_API_KEY", cfg.Models.JevKey)
	if err != nil {
		return nil, err
	}
	key, err := settings.Required("OPENAI_API_KEY", cfg.Models.OpenAIKey)
	if err != nil {
		return nil, err
	}
	// The conversation summary uses the task worker's OpenRouter key.
	summaryKey, err := settings.Required("OPENROUTER_API_KEY", cfg.Models.OpenRouterKey)
	if err != nil {
		return nil, err
	}
	store, err := platform.OpenStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	engine, err := platform.Engine(ctx)
	if err != nil {
		store.Close()
		return nil, err
	}
	if cfg.Computer.LocalURL != "" && !cfg.Temporal.Local {
		engine.Close()
		store.Close()
		return nil, fmt.Errorf("NORI_LOCAL_COMPUTER_URL requires TEMPORAL_LOCAL=true")
	}
	dispatcher := platform.Dispatcher(cfg, store, engine, waker)
	tasks := &conversation.Tasks{Store: store, Dispatcher: dispatcher}
	model := cfg.Models.Outer
	outer := conversation.NewOpenAIModel(key, model)
	if address := cfg.Test.OpenAIURL; address != "" {
		if !cfg.Temporal.Local || !strings.HasPrefix(address, "http://127.0.0.1:") {
			engine.Close()
			store.Close()
			return nil, fmt.Errorf("NORI_E2E_OPENAI_URL requires TEMPORAL_LOCAL=true and a 127.0.0.1 address")
		}
		outer.URL = address
	}
	// The conversation summary is written by the inner task model, through the same OpenRouter key the task worker uses.
	summarizer := openrouter.New(summaryKey, "openai/gpt-6.1-terra")
	if address := cfg.Test.ModelURL; address != "" {
		if !cfg.Temporal.Local || !strings.HasPrefix(address, "http://127.0.0.1:") {
			engine.Close()
			store.Close()
			return nil, fmt.Errorf("NORI_E2E_MODEL_URL requires TEMPORAL_LOCAL=true and a 127.0.0.1 address")
		}
		summarizer.Endpoint = address
	}
	classifier := conversation.NewJevClassifier(jevKey)
	if address := cfg.Test.JevURL; address != "" {
		if !cfg.Temporal.Local || !strings.HasPrefix(address, "http://127.0.0.1:") {
			engine.Close()
			store.Close()
			return nil, fmt.Errorf("NORI_E2E_JEV_URL requires TEMPORAL_LOCAL=true and a 127.0.0.1 address")
		}
		classifier.URL = address
	}
	runner := conversation.New(outer, classifier, summarizer, tasks)
	checks, err := openProactive(cfg, store, runner, tasks, outer, waker)
	if err != nil {
		engine.Close()
		store.Close()
		return nil, err
	}
	runner.Clock = conversation.Clock{Location: checks.Location, Now: checks.Now}
	later, err := openSchedules(cfg, store, tasks, waker, runner.Clock, checks.Calendar)
	if err != nil {
		engine.Close()
		store.Close()
		return nil, err
	}
	tools := agent.New(store, summarizer, platform.Computer(cfg))
	if checks.Calendar != nil {
		tools.Capabilities.Calendar = *checks.Calendar
	}
	tools.Capabilities.Location = checks.Location
	chats := chat.New(store, outer, tools, cfg.Test.OpenAIURL != "")
	return &App{Chats: chats, Runner: runner, Tasks: tasks, Dispatcher: dispatcher, Waker: waker, Model: model, Proactive: checks, Schedules: later, engine: engine}, nil
}

// openSchedules runs what the user asked for later. Notifications go to their connected account by email; without one
// they are skipped with that reason, and schedules still run.
func openSchedules(cfg settings.Settings, store state.ConversationStore, tasks *conversation.Tasks, waker wake.Waker, clock conversation.Clock, calendar *agent.CalendarConfig) (*schedules.Service, error) {
	quiet, err := state.ParseQuietHours(cfg.QuietHours)
	if err != nil {
		return nil, fmt.Errorf("NORI_QUIET_HOURS: %w", err)
	}
	s := &schedules.Service{Store: store, Tasks: tasks, Waker: waker, Clock: clock, Quiet: quiet, AppURL: cfg.AppURL}
	if calendar != nil && cfg.NotifyEmail != "off" {
		s.Mailer = &agent.NotifyMailer{Config: *calendar, To: cfg.NotifyEmail}
	}
	return s, nil
}

// openProactive sets up the calendar check with the same Google account the task worker uses. A missing credential
// leaves the check refusing with a reason; delivery and offers still work.
func openProactive(cfg settings.Settings, store state.ConversationStore, runner *conversation.Runner, tasks *conversation.Tasks, model conversation.ReplyModel, waker wake.Waker) (*proactive.Service, error) {
	location, err := time.LoadLocation(cfg.TimeZone)
	if err != nil {
		return nil, fmt.Errorf("NORI_TIMEZONE must be an IANA time zone")
	}
	s := &proactive.Service{Store: store, Runner: runner, Tasks: tasks, Model: model, Waker: waker, Location: location}
	local := cfg.Temporal.Local
	loopback := func(name, address string) (string, error) {
		if address != "" && (!local || !strings.HasPrefix(address, "http://127.0.0.1:")) {
			return "", fmt.Errorf("%s requires TEMPORAL_LOCAL=true and a 127.0.0.1 address", name)
		}
		return address, nil
	}
	google, err := loopback("NORI_E2E_GOOGLE_URL", cfg.Test.GoogleURL)
	if err != nil {
		return nil, err
	}
	routes, err := loopback("NORI_E2E_ROUTES_URL", cfg.Test.RoutesURL)
	if err != nil {
		return nil, err
	}
	if value := cfg.Test.Now; value != "" {
		at, err := time.Parse(time.RFC3339, value)
		if err != nil || !local {
			return nil, fmt.Errorf("NORI_E2E_NOW requires TEMPORAL_LOCAL=true and an RFC 3339 time")
		}
		offset := time.Until(at)
		s.Now = func() time.Time { return time.Now().Add(offset) }
	}
	calendar := agent.CalendarConfig{
		ClientID: cfg.Calendar.ClientID, ClientSecret: cfg.Calendar.ClientSecret,
		RefreshToken: cfg.Calendar.RefreshToken, Account: cfg.Calendar.Account,
		WriteCalendar: cfg.Calendar.WriteID,
	}
	if google != "" {
		calendar.APIBase, calendar.TokenURL = google, google+"/token"
	}
	if calendar.ClientID != "" && calendar.ClientSecret != "" && calendar.RefreshToken != "" {
		s.Calendar = &calendar
	}
	if key := cfg.RoutesKey; key != "" {
		router := proactive.NewRoutesAPI(key)
		if routes != "" {
			router.URL = routes
		}
		s.Router = router
	}
	return s, nil
}

func (a *App) Close() {
	a.Chats.Close()
	a.engine.Close()
	a.Tasks.Store.Close()
}

// API returns what the HTTP routes call into.
func (a *App) API() api.Deps {
	return api.Deps{Chats: a.Chats, Model: a.Model, Runner: a.Runner, Tasks: a.Tasks, Waker: a.Waker, Ops: a, Proactive: a.Proactive}
}

func (a *App) Resume(ctx context.Context) error { return a.Dispatcher.Resume(ctx) }

func (a *App) Process(ctx context.Context) error { return a.process(ctx, true) }
func (a *App) process(ctx context.Context, foreground bool) error {
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
	chatDone := make(chan error, 1)
	if foreground {
		go func() { chatDone <- a.Chats.Process(ctx) }()
	} else {
		chatDone <- nil
	}
	processErr := a.Runner.Process(ctx)
	// Offers wait for an idle conversation, so they go out after the pass that published the latest reply.
	var offerErr, scheduleErr error
	if processErr == nil {
		offerErr = a.Proactive.Deliver(ctx)
	}
	// Schedules fire after the pass too, so a reminder never cuts into a reply.
	if processErr == nil {
		scheduleErr = a.Schedules.Run(ctx)
		if errors.Is(scheduleErr, state.ErrConversationBusy) {
			scheduleErr = nil
		}
	}
	// Dispatch and explicit controls can outlive their first delivery attempt.
	// Keep the queue notification until delivery is confirmed.
	delivery, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reconcileErr := a.Dispatcher.Reconcile(delivery, dispatch.Commands|dispatch.Publications)
	return errors.Join(<-chatDone, stopErr, processErr, offerErr, scheduleErr, reconcileErr)
}

// LocalLoop is tied to the application, so browser cancellation and reconnects
// cannot cancel accepted work. The periodic check also recovers after restart.
func (a *App) LocalLoop(ctx context.Context, notifications <-chan struct{}) {
	go a.Chats.Loop(ctx)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		process, cancel := context.WithTimeout(ctx, 9*time.Minute)
		if err := a.process(process, false); err != nil && ctx.Err() == nil {
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
