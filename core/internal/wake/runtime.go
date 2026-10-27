package wake

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"nori/internal/computer"
	"nori/internal/conversation"
	"nori/internal/state"
	"nori/internal/task"
)

func Env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

type Runtime struct {
	Runner *conversation.Runner
	Tasks  *conversation.Tasks
	Model  string
}

// Open uses the same durable outer loop in local and hosted entrypoints.
func Open(ctx context.Context) (*Runtime, error) {
	key, jevKey := os.Getenv("OPENROUTER_API_KEY"), os.Getenv("JEV_API_KEY")
	if strings.TrimSpace(key) == "" || strings.TrimSpace(jevKey) == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY and JEV_API_KEY must be configured in the selected project environment")
	}
	base := Env("NORI_AGENT_BASE_URL", "https://openrouter.ai/api/v1")
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("NORI_AGENT_BASE_URL must be an HTTPS API base URL")
	}
	project := Env("NORI_PROJECT_ID", "bot-111549")
	store, err := state.NewGoogle(ctx, project, Env("FIRESTORE_DATABASE_ID", "nori-poc"), os.Getenv("ARTIFACT_BUCKET"))
	if err != nil {
		return nil, err
	}
	temporal, err := task.Dial(ctx)
	if err != nil {
		store.Close()
		return nil, err
	}
	tasks := &conversation.Tasks{Store: store, Client: temporal, Computer: computer.Config{Project: project, Zone: Env("COMPUTER_ZONE", "us-central1-a"), Instance: Env("COMPUTER_INSTANCE", "nori-computer")}}
	model := Env("NORI_AGENT_MODEL", "anthropic/claude-opus-5.5")
	return &Runtime{Runner: conversation.New(conversation.NewModel(base, key, model), conversation.NewClassifier(jevKey), tasks), Tasks: tasks, Model: model}, nil
}

func (r *Runtime) Close() {
	r.Tasks.Client.Close()
	r.Tasks.Store.Close()
}

func (r *Runtime) Process(ctx context.Context) error {
	processErr := r.Runner.Process(ctx)
	// Dispatch and explicit controls can outlive their first Temporal delivery
	// attempt. Keep the Cloud Tasks notification until delivery is confirmed.
	delivery, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return errors.Join(processErr, task.Deliver(delivery, r.Tasks.Store, r.Tasks.Client))
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
