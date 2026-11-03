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

	"nori/internal/agent"
	"nori/internal/agent/openrouter"
	"nori/internal/api"
	"nori/internal/conversation"
	"nori/internal/dispatch"
	"nori/internal/env"
	"nori/internal/platform"
	"nori/internal/proactive"
	"nori/internal/schedules"
	"nori/internal/spend"
	"nori/internal/state"
	"nori/internal/wake"
)

type App struct {
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
	summaryKey := os.Getenv("OPENROUTER_API_KEY")
	if strings.TrimSpace(summaryKey) == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY must be configured for conversation summaries")
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
	// The conversation summary is written by the inner task model, through the same OpenRouter key the task worker uses.
	summarizer := openrouter.New(summaryKey, "openai/gpt-6.1-terra")
	if address := os.Getenv("NORI_E2E_MODEL_URL"); address != "" {
		if os.Getenv("TEMPORAL_LOCAL") != "true" || !strings.HasPrefix(address, "http://127.0.0.1:") {
			engine.Close()
			store.Close()
			return nil, fmt.Errorf("NORI_E2E_MODEL_URL requires TEMPORAL_LOCAL=true and a 127.0.0.1 address")
		}
		summarizer.Endpoint = address
	}
	runner := conversation.New(outer, conversation.NewJevClassifier(jevKey), summarizer, tasks)
	checks, err := openProactive(store, runner, tasks, outer, waker)
	if err != nil {
		engine.Close()
		store.Close()
		return nil, err
	}
	runner.Clock = conversation.Clock{Location: checks.Location, Now: checks.Now}
	later, err := openSchedules(store, tasks, waker, runner.Clock, checks.Calendar)
	if err != nil {
		engine.Close()
		store.Close()
		return nil, err
	}
	return &App{Runner: runner, Tasks: tasks, Dispatcher: dispatcher, Waker: waker, Model: model, Proactive: checks, Schedules: later, engine: engine}, nil
}

// openSchedules runs what Austin asked for later. Notifications go to his connected account by email; without one
// they are skipped with that reason, and schedules still run.
func openSchedules(store state.ConversationStore, tasks *conversation.Tasks, waker wake.Waker, clock conversation.Clock, calendar *agent.CalendarConfig) (*schedules.Service, error) {
	quiet, err := state.ParseQuietHours(env.Get("NORI_QUIET_HOURS", "22:00-07:00"))
	if err != nil {
		return nil, fmt.Errorf("NORI_QUIET_HOURS: %w", err)
	}
	s := &schedules.Service{Store: store, Tasks: tasks, Waker: waker, Clock: clock, Quiet: quiet, AppURL: os.Getenv("NORI_APP_URL")}
	if calendar != nil && os.Getenv("NORI_NOTIFY_EMAIL") != "off" {
		s.Mailer = &agent.NotifyMailer{Config: *calendar, To: os.Getenv("NORI_NOTIFY_EMAIL")}
	}
	return s, nil
}

// openProactive sets up the calendar check with the same Google account the task worker uses. A missing credential
// leaves the check refusing with a reason; delivery and offers still work.
func openProactive(store state.ConversationStore, runner *conversation.Runner, tasks *conversation.Tasks, model conversation.ReplyModel, waker wake.Waker) (*proactive.Service, error) {
	location, err := time.LoadLocation(env.Get("NORI_TIMEZONE", "America/Los_Angeles"))
	if err != nil {
		return nil, fmt.Errorf("NORI_TIMEZONE must be an IANA time zone")
	}
	s := &proactive.Service{Store: store, Runner: runner, Tasks: tasks, Model: model, Waker: waker, Location: location}
	local := os.Getenv("TEMPORAL_LOCAL") == "true"
	loopback := func(name string) (string, error) {
		address := os.Getenv(name)
		if address != "" && (!local || !strings.HasPrefix(address, "http://127.0.0.1:")) {
			return "", fmt.Errorf("%s requires TEMPORAL_LOCAL=true and a 127.0.0.1 address", name)
		}
		return address, nil
	}
	google, err := loopback("NORI_E2E_GOOGLE_URL")
	if err != nil {
		return nil, err
	}
	routes, err := loopback("NORI_E2E_ROUTES_URL")
	if err != nil {
		return nil, err
	}
	if value := os.Getenv("NORI_E2E_NOW"); value != "" {
		at, err := time.Parse(time.RFC3339, value)
		if err != nil || !local {
			return nil, fmt.Errorf("NORI_E2E_NOW requires TEMPORAL_LOCAL=true and an RFC 3339 time")
		}
		offset := time.Until(at)
		s.Now = func() time.Time { return time.Now().Add(offset) }
	}
	calendar := agent.CalendarConfig{
		ClientID: os.Getenv("GOOGLE_CALENDAR_CLIENT_ID"), ClientSecret: os.Getenv("GOOGLE_CALENDAR_CLIENT_SECRET"),
		RefreshToken: os.Getenv("GOOGLE_CALENDAR_REFRESH_TOKEN"), Account: env.Get("GOOGLE_CALENDAR_ACCOUNT", "the connected Google account"),
		WriteCalendar: os.Getenv("GOOGLE_CALENDAR_WRITE_ID"),
	}
	if google != "" {
		calendar.APIBase, calendar.TokenURL = google, google+"/token"
	}
	if calendar.ClientID != "" && calendar.ClientSecret != "" && calendar.RefreshToken != "" {
		s.Calendar = &calendar
	}
	if key := os.Getenv("GOOGLE_ROUTES_API_KEY"); key != "" {
		router := proactive.NewRoutesAPI(key)
		if routes != "" {
			router.URL = routes
		}
		s.Router = router
	}
	return s, nil
}

func (a *App) Close() {
	a.engine.Close()
	a.Tasks.Store.Close()
}

// API returns what the HTTP routes call into.
func (a *App) API() api.Deps {
	return api.Deps{Model: a.Model, Runner: a.Runner, Tasks: a.Tasks, Waker: a.Waker, Ops: a, Proactive: a.Proactive}
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
	return errors.Join(stopErr, processErr, offerErr, scheduleErr, reconcileErr)
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
