package wake

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"nori/internal/computer"
	"nori/internal/conversation"
	"nori/internal/env"
	"nori/internal/observability"
	"nori/internal/spend"
	"nori/internal/state"
	"nori/internal/task"
)

type Runtime struct {
	Runner *conversation.Runner
	Tasks  *conversation.Tasks
	Model  string
}

// Open uses the same durable outer loop in local and hosted entrypoints.
func Open(ctx context.Context) (*Runtime, error) {
	if err := spend.Validate(ctx); err != nil {
		return nil, err
	}
	key, jevKey := os.Getenv("OPENROUTER_API_KEY"), os.Getenv("JEV_API_KEY")
	if strings.TrimSpace(jevKey) == "" {
		return nil, fmt.Errorf("JEV_API_KEY must be configured in the selected project environment")
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY must be configured in the selected project environment")
	}
	base := env.Get("NORI_AGENT_BASE_URL", "https://openrouter.ai/api/v1")
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("NORI_AGENT_BASE_URL must be an HTTPS API base URL")
	}
	project := env.Get("NORI_PROJECT_ID", "bot-111549")
	store, err := state.NewGoogle(ctx, project, env.Get("FIRESTORE_DATABASE_ID", "nori-poc"), os.Getenv("ARTIFACT_BUCKET"))
	if err != nil {
		return nil, err
	}
	temporal, err := task.Dial(ctx)
	if err != nil {
		store.Close()
		return nil, err
	}
	localComputer := os.Getenv("NORI_LOCAL_COMPUTER_URL")
	if localComputer != "" && os.Getenv("TEMPORAL_LOCAL") != "true" {
		temporal.Close()
		store.Close()
		return nil, fmt.Errorf("NORI_LOCAL_COMPUTER_URL requires TEMPORAL_LOCAL=true")
	}
	tasks := &conversation.Tasks{Store: store, Client: temporal, Computer: computer.Config{Project: project, Zone: env.Get("COMPUTER_ZONE", "us-central1-a"), Instance: env.Get("COMPUTER_INSTANCE", "nori-computer"), LocalURL: localComputer}}
	model := env.Get("NORI_AGENT_MODEL", "anthropic/claude-opus-5.5")
	return &Runtime{Runner: conversation.New(conversation.NewModel(base, key, model), conversation.NewClassifier(jevKey), tasks), Tasks: tasks, Model: model}, nil
}

func (r *Runtime) Close() {
	r.Tasks.Client.Close()
	r.Tasks.Store.Close()
}

func (r *Runtime) Process(ctx context.Context) error {
	// A paused worker cannot finish a queued Stop. Retry only accepted Stop
	// targets here, including a timed-out cleanup from the submitting request.
	var stopErr error
	control, err := r.Tasks.Store.Control(ctx)
	if err != nil {
		return err
	}
	if control.Paused {
		runs, err := r.Tasks.Store.OpenTasks(ctx)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.CancelRequested {
				cleanup, cancel := context.WithTimeout(ctx, 10*time.Second)
				_, err := task.StopPaused(cleanup, r.Tasks.Store, r.Tasks.Computer, run.ID)
				cancel()
				stopErr = errors.Join(stopErr, err)
			}
		}
	}
	processErr := r.Runner.Process(ctx)
	// Dispatch and explicit controls can outlive their first Temporal delivery
	// attempt. Keep the Cloud Tasks notification until delivery is confirmed.
	delivery, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	deliveryErr := task.Deliver(delivery, r.Tasks.Store, r.Tasks.Client)
	pending, pendingErr := r.Tasks.Store.UserRoutingPending(delivery)
	var wakeErr error
	if pendingErr == nil && !pending {
		wakeErr = task.WakePublications(delivery, r.Tasks.Store, r.Tasks.Client)
	}
	return errors.Join(stopErr, processErr, deliveryErr, pendingErr, wakeErr)
}

// LocalLoop is tied to the application, so browser cancellation and reconnects
// cannot cancel accepted work. The periodic check also recovers after restart.
func (r *Runtime) LocalLoop(ctx context.Context, notifications <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		process, cancel := context.WithTimeout(ctx, 9*time.Minute)
		if err := r.Process(process); err != nil && ctx.Err() == nil {
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

// Server builds the HTTP server shared by the local and hosted entrypoints.
func Server(ctx context.Context, addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: observability.HTTPHandler(handler),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
}

// Listen serves until ctx ends, then shuts down within the grace period.
func Listen(ctx context.Context, server *http.Server, grace time.Duration) error {
	serving := make(chan error, 1)
	go func() { serving <- server.ListenAndServe() }()
	select {
	case err := <-serving:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
	return nil
}
