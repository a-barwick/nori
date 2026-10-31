package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/genai"
	"nori/internal/agent"
	"nori/internal/computer"
	"nori/internal/env"
	"nori/internal/state"
	"nori/internal/task"
	"nori/internal/wake"
)

func main() {
	if e := env.Load(); e != nil {
		log.Fatal(e)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	project := env.Get("NORI_PROJECT_ID", "bot-111549")
	store, e := state.NewGoogle(ctx, project, env.Get("FIRESTORE_DATABASE_ID", "nori-poc"), os.Getenv("ARTIFACT_BUCKET"))
	if e != nil {
		log.Fatal(e)
	}
	defer store.Close()
	control, e := store.Control(ctx)
	if e != nil {
		log.Fatal(e)
	}
	if e = control.Check(); e != nil {
		log.Print("execution paused; worker exiting without cancelling durable tasks")
		return
	}
	c, e := task.Dial(ctx)
	if e != nil {
		log.Fatal(e)
	}
	defer c.Close()
	attempts := int32(1)
	model, e := genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: project, Location: env.Get("MODEL_LOCATION", "global"), HTTPOptions: genai.HTTPOptions{RetryOptions: &genai.HTTPRetryOptions{Attempts: &attempts}}})
	if e != nil {
		log.Fatal(e)
	}
	build := os.Getenv("TEMPORAL_BUILD_ID")
	if build == "" {
		log.Fatal("TEMPORAL_BUILD_ID is required; use an immutable release identifier")
	}
	// Gemini 3.8 Flash global introductory pricing through 2026-12-31.
	// https://cloud.google.com/gemini-enterprise-agent-platform/generative-ai/pricing
	price, e := agent.ParsePrice(env.Get("MODEL_INPUT_USD_PER_MTOK", "0.75"), env.Get("MODEL_OUTPUT_USD_PER_MTOK", "3.75"))
	if e != nil {
		log.Fatal(e)
	}
	local := os.Getenv("TEMPORAL_LOCAL") == "true"
	localComputer := os.Getenv("NORI_LOCAL_COMPUTER_URL")
	if localComputer != "" && !local {
		log.Fatal("NORI_LOCAL_COMPUTER_URL requires TEMPORAL_LOCAL=true")
	}
	var notify func(context.Context) error
	if !local {
		notifier, err := wake.NewCloud(ctx, os.Getenv("NORI_WAKE_QUEUE"), os.Getenv("NORI_RUNTIME_URL"), os.Getenv("NORI_WAKE_SERVICE_ACCOUNT"))
		if err != nil {
			log.Fatal(err)
		}
		notify = notifier.Schedule
	}
	service := agent.New(store, model, env.Get("MODEL_NAME", "gemini-3.8-flash"), price, computer.Config{Project: project, Zone: os.Getenv("COMPUTER_ZONE"), Instance: env.Get("COMPUTER_INSTANCE", "nori-computer"), LocalURL: localComputer})
	w := worker.New(c, task.Queue(), worker.Options{MaxConcurrentActivityExecutionSize: 4, MaxConcurrentWorkflowTaskExecutionSize: 2, WorkerStopTimeout: 25 * time.Second, DeploymentOptions: worker.DeploymentOptions{UseVersioning: true, Version: worker.WorkerDeploymentVersion{DeploymentName: env.Get("TEMPORAL_DEPLOYMENT_NAME", "nori-task"), BuildID: build}, DefaultVersioningBehavior: workflow.VersioningBehaviorPinned}})
	w.RegisterWorkflowWithOptions(task.Workflow, workflow.RegisterOptions{Name: task.WorkflowName, VersioningBehavior: workflow.VersioningBehaviorPinned})
	w.RegisterActivity(&task.Activities{Agent: service, Store: store, Notify: notify})
	if e = w.Start(); e != nil {
		log.Fatal(e)
	}
	defer w.Stop()
	if e = task.RepairClosed(ctx, store, c, service.Computer); e != nil {
		log.Printf("terminal recovery: %v", e)
	}
	// Repair delivery on startup and while already running. This process never
	// opens admission or resumes a paused task on its own.
	lastRepair := time.Now()
	var lastWakeReceived int64
	var lastWakeAttempt time.Time
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if time.Since(lastRepair) >= 30*time.Second {
			lastRepair = time.Now()
			if e = task.RepairClosed(ctx, store, c, service.Computer); e != nil && ctx.Err() == nil {
				log.Printf("terminal recovery: %v", e)
			}
		}
		if e = task.Deliver(ctx, store, c); e != nil && ctx.Err() == nil {
			log.Printf("pending command delivery: %v", e)
		}
		// Reuse the same user-only routing fence as terminal publication.
		if pending, err := store.UserRoutingPending(ctx); err == nil && !pending {
			if err = task.WakePublications(ctx, store, c); err != nil && ctx.Err() == nil {
				log.Printf("held publication wake: %v", err)
			}
		}
		if notify != nil {
			relay, cancel := context.WithTimeout(ctx, 5*time.Second)
			control, _, err := store.ConversationStatus(relay)
			now := time.Now()
			if err == nil && control.Received > control.Consumed && !(control.Active != "" && control.LeaseUntil.After(now)) && (control.Received != lastWakeReceived || now.Sub(lastWakeAttempt) >= time.Minute) {
				// Record the attempt before scheduling: an error may still mean
				// Cloud Tasks accepted the wake. Bound duplicate fallback wakes
				// while preserving immediate delivery for newly received input.
				lastWakeReceived, lastWakeAttempt = control.Received, now
				err = notify(relay)
			}
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("pending task event wake: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		control, e = store.Control(ctx)
		if e != nil {
			log.Print("control unavailable; worker exiting")
			return
		}
		if e = control.Check(); e != nil {
			log.Print("execution paused; worker exiting without cancelling durable tasks")
			return
		}
	}
}
