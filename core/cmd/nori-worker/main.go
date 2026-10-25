package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/genai"
	"nori/internal/agent"
	"nori/internal/computer"
	"nori/internal/state"
	"nori/internal/task"
)

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func main() {
	if e := godotenv.Load(env("NORI_ENV_FILE", "../.env")); e != nil && !os.IsNotExist(e) {
		log.Fatal("could not load root .env")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	project := env("NORI_PROJECT_ID", "bot-111549")
	store, e := state.NewGoogle(ctx, project, env("FIRESTORE_DATABASE_ID", "nori-poc"), os.Getenv("ARTIFACT_BUCKET"))
	if e != nil {
		log.Fatal(e)
	}
	defer store.Close()
	control, e := store.Control(ctx)
	if e != nil {
		log.Fatal(e)
	}
	expiry, e := control.Deadline()
	cleanupOnly := e != nil
	if cleanupOnly {
		// An operator can start a stopped poller solely to finish cancellation.
		// This grants no execution admission and never resumes paid computers.
		expiry = time.Now().Add(30 * time.Second)
	}
	ctx, cancel := context.WithDeadline(ctx, expiry)
	defer cancel()
	c, e := task.Dial(ctx)
	if e != nil {
		log.Fatal(e)
	}
	defer c.Close()
	attempts := int32(1)
	model, e := genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: project, Location: env("MODEL_LOCATION", "global"), HTTPOptions: genai.HTTPOptions{RetryOptions: &genai.HTTPRetryOptions{Attempts: &attempts}}})
	if e != nil {
		log.Fatal(e)
	}
	build := os.Getenv("TEMPORAL_BUILD_ID")
	if build == "" {
		log.Fatal("TEMPORAL_BUILD_ID is required; use an immutable release identifier")
	}
	service := agent.New(store, model, env("MODEL_NAME", "gemini-3.1-flash-lite"), computer.Config{Project: project, Zone: os.Getenv("COMPUTER_ZONE"), Instance: env("COMPUTER_INSTANCE", "nori-computer")})
	stopTimeout := 25 * time.Second
	if cleanupOnly {
		stopTimeout = 5 * time.Second
	}
	w := worker.New(c, task.Queue(), worker.Options{MaxConcurrentActivityExecutionSize: 1, MaxConcurrentWorkflowTaskExecutionSize: 2, WorkerStopTimeout: stopTimeout, DeploymentOptions: worker.DeploymentOptions{UseVersioning: true, Version: worker.WorkerDeploymentVersion{DeploymentName: env("TEMPORAL_DEPLOYMENT_NAME", "nori-task"), BuildID: build}, DefaultVersioningBehavior: workflow.VersioningBehaviorPinned}})
	w.RegisterWorkflowWithOptions(task.Workflow, workflow.RegisterOptions{Name: task.WorkflowName, VersioningBehavior: workflow.VersioningBehaviorPinned})
	w.RegisterActivity(&task.Activities{Agent: service, Store: store, CleanupOnly: cleanupOnly})
	if e = w.Start(); e != nil {
		log.Fatal(e)
	}
	defer w.Stop()
	if cleanupOnly {
		log.Print("session paused or expired; worker draining cleanup only")
		if e = task.Drain(ctx, store, c, service.Computer); e != nil {
			log.Printf("worker cleanup incomplete: %v", e)
		}
		return
	}
	defer func() {
		check, cancelCheck := context.WithTimeout(context.Background(), 5*time.Second)
		control, readErr := store.Control(check)
		cancelCheck()
		// Ordinary SIGTERM/scale-to-zero preserves durable tasks. Only a durable
		// pause or expired admission authorizes task cancellation on shutdown.
		if readErr != nil {
			return
		}
		if _, admissionErr := control.Deadline(); admissionErr == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if e := task.Drain(cleanup, store, c, service.Computer); e != nil {
			log.Printf("worker cleanup incomplete: %v", e)
		}
	}()
	if e = task.RepairClosed(ctx, store, c, service.Computer); e != nil {
		log.Printf("terminal recovery: %v", e)
	}
	// Repair delivery on startup and while already running. This process never
	// resumes a session or creates paid compute after its admission expires.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if e = task.Deliver(ctx, store, c); e != nil && ctx.Err() == nil {
			log.Printf("pending command delivery: %v", e)
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
		if _, e = control.Deadline(); e != nil {
			log.Print("session paused or expired; worker exiting")
			return
		}
	}
}
