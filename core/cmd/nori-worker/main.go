package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"nori/internal/agent"
	"nori/internal/env"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/spend"
	"nori/internal/task"
	"nori/internal/wake"
	"nori/internal/workerservice"
	"nori/internal/workerwake"
)

func main() {
	if e := env.Load(); e != nil {
		log.Fatal(e)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopTelemetry := observability.Init(ctx, "nori-worker")
	defer observability.Shutdown(stopTelemetry)
	if err := spend.ValidateConfiguration(); err != nil {
		observability.Fatal(err)
	}
	wakes, err := env.WorkerWake(ctx)
	if err != nil {
		observability.Fatal(err)
	}
	if port := os.Getenv("PORT"); port != "" {
		addr := ":" + port
		if os.Getenv("TEMPORAL_LOCAL") == "true" {
			addr = "127.0.0.1:" + port
		}
		server := wake.Server(ctx, addr, workerservice.Handler(ctx, func(ctx context.Context, id string, ids []string, registration bool) error {
			if registration {
				return register(ctx)
			}
			return run(ctx, wakes, true, id, ids)
		}))
		server.WriteTimeout = 11 * time.Minute
		if err := wake.Listen(ctx, server, 5*time.Minute); err != nil {
			observability.RecordErrorCategory(ctx, "http.listen", err)
			observability.Shutdown(stopTelemetry)
			observability.Fatal(err)
		}
		return
	}
	if err := run(ctx, wakes, false, "local", nil); err != nil {
		observability.RecordErrorCategory(ctx, "worker.lifecycle", err)
		observability.Shutdown(stopTelemetry)
		observability.Fatal(err)
	}
}

// Registration polls only a fresh, non-current version. It never enters the
// normal worker's recovery/delivery loop or constructs a model/computer client.
func register(ctx context.Context) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	build, deployment := os.Getenv("TEMPORAL_BUILD_ID"), os.Getenv("TEMPORAL_DEPLOYMENT_NAME")
	if build == "" || deployment == "" {
		return fmt.Errorf("registration requires an immutable build and deployment")
	}
	store, err := platform.OpenStore(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	control, err := store.Control(ctx)
	if err != nil {
		return err
	}
	if !control.Paused {
		return fmt.Errorf("registration requires paused execution admission")
	}
	c, err := task.Dial(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	handle := c.WorkerDeploymentClient().GetHandle(deployment)
	_, err = handle.DescribeVersion(ctx, client.WorkerDeploymentDescribeVersionOptions{BuildID: build})
	var missing *serviceerror.NotFound
	if !errors.As(err, &missing) {
		if err != nil {
			return err
		}
		return fmt.Errorf("registration requires a fresh deployment version")
	}
	guard := &registrationGuard{}
	w := worker.New(c, task.Queue(), worker.Options{
		Interceptors: []interceptor.WorkerInterceptor{guard}, WorkerStopTimeout: 5 * time.Second,
		DeploymentOptions: worker.DeploymentOptions{UseVersioning: true,
			Version:                   worker.WorkerDeploymentVersion{DeploymentName: deployment, BuildID: build},
			DefaultVersioningBehavior: workflow.VersioningBehaviorPinned},
	})
	w.RegisterWorkflowWithOptions(task.Workflow, workflow.RegisterOptions{Name: task.WorkflowName, VersioningBehavior: workflow.VersioningBehaviorPinned})
	w.RegisterActivity(&task.Activities{})
	if err = w.Start(); err != nil {
		return err
	}
	defer func() {
		w.Stop()
		if guard.unexpected.Load() {
			result = errors.Join(result, fmt.Errorf("registration refused unexpected task execution"))
		}
	}()
	for {
		version, err := handle.DescribeVersion(ctx, client.WorkerDeploymentDescribeVersionOptions{BuildID: build})
		if err == nil {
			if !version.Info.CurrentSinceTime.IsZero() || !version.Info.RampingSinceTime.IsZero() {
				return fmt.Errorf("registration version became current or ramping")
			}
			workflowQueue, activityQueue := false, false
			for _, queue := range version.Info.TaskQueuesInfos {
				if queue.Name == task.Queue() {
					workflowQueue = workflowQueue || queue.Type == client.TaskQueueTypeWorkflow
					activityQueue = activityQueue || queue.Type == client.TaskQueueTypeActivity
				}
			}
			if workflowQueue && activityQueue {
				return nil
			}
		} else if !errors.As(err, &missing) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

type registrationGuard struct {
	interceptor.WorkerInterceptorBase
	unexpected atomic.Bool
}

func (g *registrationGuard) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &registrationWorkflow{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, guard: g}
}

func (g *registrationGuard) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &registrationActivity{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, guard: g}
}

type registrationWorkflow struct {
	interceptor.WorkflowInboundInterceptorBase
	guard *registrationGuard
}

func (r *registrationWorkflow) ExecuteWorkflow(workflow.Context, *interceptor.ExecuteWorkflowInput) (any, error) {
	r.guard.unexpected.Store(true)
	// Fail only this workflow task if routing is unexpectedly violated; returning
	// an error here would permanently fail the user's workflow execution.
	panic("registration cannot execute workflows")
}

type registrationActivity struct {
	interceptor.ActivityInboundInterceptorBase
	guard *registrationGuard
}

func (r *registrationActivity) ExecuteActivity(context.Context, *interceptor.ExecuteActivityInput) (any, error) {
	r.guard.unexpected.Store(true)
	return nil, fmt.Errorf("registration cannot execute activities")
}

func run(ctx context.Context, wakes *workerwake.Cloud, bounded bool, sliceID string, known []string) (result error) {
	build := os.Getenv("TEMPORAL_BUILD_ID")
	if build == "" {
		return fmt.Errorf("TEMPORAL_BUILD_ID is required; use an immutable release identifier")
	}
	if bounded && !wakes.Enabled() {
		return fmt.Errorf("request worker requires durable worker wake configuration")
	}
	pollWindow, err := time.ParseDuration(env.Get("NORI_WORKER_POLL_WINDOW", "240s"))
	if err != nil || pollWindow <= 0 || pollWindow > 240*time.Second {
		return fmt.Errorf("NORI_WORKER_POLL_WINDOW must be in (0,240s]")
	}
	idleWindow, err := time.ParseDuration(env.Get("NORI_WORKER_IDLE_WINDOW", "15s"))
	if err != nil || idleWindow <= 0 {
		return fmt.Errorf("NORI_WORKER_IDLE_WINDOW must be positive")
	}
	if bounded {
		pollCtx, cancel := context.WithTimeout(ctx, pollWindow)
		defer cancel()
		ctx = pollCtx
	}
	store, e := platform.OpenStore(ctx)
	if e != nil {
		return e
	}
	defer store.Close()
	control, e := store.Control(ctx)
	if e != nil {
		return e
	}
	if e = control.Check(); e != nil {
		if bounded && len(known) > 0 {
			// The operator fences the native queue before closing admission.
			// Retain this dispatched wake without starting any SDK worker.
			if err := wakes.Schedule(ctx, build, known...); err != nil {
				return err
			}
			log.Printf("worker_slice slice=%s build=%s phase=paused-retained workflows=%q", sliceID, build, known)
		}
		log.Print("execution paused; worker exiting without cancelling durable tasks")
		return nil
	}
	c, e := task.Dial(ctx)
	if e != nil {
		return e
	}
	defer c.Close()
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		return fmt.Errorf("OPENROUTER_API_KEY must be configured for the task worker")
	}
	local := os.Getenv("TEMPORAL_LOCAL") == "true"
	localComputer := os.Getenv("NORI_LOCAL_COMPUTER_URL")
	if localComputer != "" && !local {
		return fmt.Errorf("NORI_LOCAL_COMPUTER_URL requires TEMPORAL_LOCAL=true")
	}
	var notify func(context.Context) error
	if !local {
		notifier, err := wake.NewCloud(ctx, os.Getenv("NORI_WAKE_QUEUE"), os.Getenv("NORI_RUNTIME_URL"), os.Getenv("NORI_WAKE_SERVICE_ACCOUNT"))
		if err != nil {
			return err
		}
		notify = notifier.Schedule
	}
	service := agent.New(store, key, env.Get("MODEL_NAME", "openai/gpt-6.1-terra"), platform.Computer())
	stopTimeout := 25 * time.Second
	if bounded {
		stopTimeout = 5 * time.Minute
	}
	tracker := workerservice.NewTracker(known...)
	w := worker.New(c, task.Queue(), worker.Options{Interceptors: []interceptor.WorkerInterceptor{tracker}, MaxConcurrentActivityExecutionSize: 4, MaxConcurrentWorkflowTaskExecutionSize: 2, WorkerStopTimeout: stopTimeout, DeploymentOptions: worker.DeploymentOptions{UseVersioning: true, Version: worker.WorkerDeploymentVersion{DeploymentName: env.Get("TEMPORAL_DEPLOYMENT_NAME", "nori-task"), BuildID: build}, DefaultVersioningBehavior: workflow.VersioningBehaviorPinned}})
	w.RegisterWorkflowWithOptions(task.Workflow, workflow.RegisterOptions{Name: task.WorkflowName, VersioningBehavior: workflow.VersioningBehaviorPinned})
	w.RegisterActivity(&task.Activities{Agent: service, Store: store, Notify: notify, JevKey: os.Getenv("JEV_API_KEY")})
	if e = w.Start(); e != nil {
		return e
	}
	defer func() {
		_, active, _ := tracker.Snapshot()
		log.Printf("worker_slice slice=%s build=%s phase=draining active=%d", sliceID, build, active)
		w.Stop()
		log.Printf("worker_slice slice=%s build=%s phase=stopped", sliceID, build)
		if bounded {
			check, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			control, err := store.Control(check)
			if err != nil {
				result = errors.Join(result, err)
				return
			}
			ids, _, _ := tracker.Snapshot()
			if control.Paused {
				if len(ids) > 0 {
					result = errors.Join(result, wakes.Schedule(check, build, ids...))
				}
				log.Printf("worker_slice slice=%s build=%s phase=paused-retained workflows=%q", sliceID, build, ids)
				return
			}
			pending, err := task.Runnable(check, store, c, build, ids)
			if err == nil && len(pending) > 0 {
				err = wakes.Schedule(check, build, pending...)
			}
			log.Printf("worker_slice slice=%s build=%s phase=handoff runnable=%q error=%v", sliceID, build, pending, err)
			result = errors.Join(result, err)
		}
	}()
	if e = task.RepairClosed(ctx, store, c, service.Computer); e != nil {
		if bounded {
			return e
		}
		log.Printf("terminal recovery: %v", e)
	}
	// Repair delivery on startup and while already running. This process never
	// opens admission or resumes a paused task on its own.
	log.Printf("worker_slice slice=%s build=%s phase=polling poll_window=%s", sliceID, build, pollWindow)
	lastRepair := time.Now()
	var lastWakeReceived int64
	var lastWakeAttempt time.Time
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(lastRepair) >= 30*time.Second {
			lastRepair = time.Now()
			if e = task.RepairClosed(ctx, store, c, service.Computer); e != nil && ctx.Err() == nil {
				log.Printf("terminal recovery: %v", e)
			}
		}
		if e = task.Deliver(ctx, store, c, wakes); e != nil && ctx.Err() == nil {
			log.Printf("pending command delivery: %v", e)
		}
		// Reuse the same user-only routing fence as terminal publication.
		if pending, err := store.UserRoutingPending(ctx); err == nil && !pending {
			if err = task.WakePublications(ctx, store, c, wakes); err != nil && ctx.Err() == nil {
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
		if bounded {
			ids, active, quiet := tracker.Snapshot()
			if active == 0 && quiet >= idleWindow {
				pending, err := task.Runnable(ctx, store, c, build, ids)
				if err != nil {
					if ctx.Err() != nil {
						return nil
					}
					return err
				}
				if len(pending) == 0 {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		control, e = store.Control(ctx)
		if e != nil {
			log.Print("control unavailable; worker exiting")
			return nil
		}
		if e = control.Check(); e != nil {
			log.Print("execution paused; worker exiting without cancelling durable tasks")
			return nil
		}
	}
}
