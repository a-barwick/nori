// Package taskworker runs the Temporal worker for task workflows and activities.
// It is the only place that builds an SDK worker; the lifecycle around it lives
// in workerservice.
package taskworker

import (
	"context"
	"sync"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"nori/internal/task"
)

// Options pin the worker to one immutable release.
type Options struct {
	// Deployment and Build are the Temporal worker deployment version.
	Deployment, Build string
	// StopTimeout is how long Stop waits for running activities.
	StopTimeout time.Duration
}

// Runner is a started worker. It remembers which executions it has served so a
// request-bound slice can hand unfinished work to the next wake.
type Runner struct {
	w       worker.Worker
	tracker *tracker
}

// Start begins polling the task queue. known are executions an earlier wake
// named; the tracker reports them even if this worker never runs an activity.
func Start(c client.Client, o Options, activities *task.Activities, known ...string) (*Runner, error) {
	tracker := newTracker(known...)
	w := worker.New(c, task.Queue(), worker.Options{
		Interceptors:                       []interceptor.WorkerInterceptor{tracker},
		MaxConcurrentActivityExecutionSize: 4, MaxConcurrentWorkflowTaskExecutionSize: 2,
		WorkerStopTimeout: o.StopTimeout,
		DeploymentOptions: deployment(o.Deployment, o.Build),
	})
	registerTask(w, activities)
	if err := w.Start(); err != nil {
		return nil, err
	}
	return &Runner{w: w, tracker: tracker}, nil
}

// Stop stops polling and waits up to the stop timeout for running activities.
func (r *Runner) Stop() { r.w.Stop() }

// Activity reports every execution this worker has seen, how many activities are
// running, and how long it has been since one started or finished.
func (r *Runner) Activity() ([]string, int, time.Duration) { return r.tracker.snapshot() }

func deployment(name, build string) worker.DeploymentOptions {
	return worker.DeploymentOptions{
		UseVersioning:             true,
		Version:                   worker.WorkerDeploymentVersion{DeploymentName: name, BuildID: build},
		DefaultVersioningBehavior: workflow.VersioningBehaviorPinned,
	}
}

func registerTask(w worker.Worker, activities *task.Activities) {
	w.RegisterWorkflowWithOptions(task.Workflow, workflow.RegisterOptions{Name: task.WorkflowName, VersioningBehavior: workflow.VersioningBehaviorPinned})
	w.RegisterActivity(activities)
}

// tracker remembers every execution seen by this worker, including executions
// whose terminal projection was published before their cleanup activities end.
type tracker struct {
	interceptor.WorkerInterceptorBase
	mu     sync.Mutex
	ids    map[string]bool
	active int
	last   time.Time
}

func newTracker(ids ...string) *tracker {
	t := &tracker{ids: map[string]bool{}, last: time.Now()}
	for _, id := range ids {
		t.ids[id] = true
	}
	return t
}

func (t *tracker) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &trackedActivity{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, tracker: t}
}

type trackedActivity struct {
	interceptor.ActivityInboundInterceptorBase
	tracker *tracker
}

func (a *trackedActivity) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (any, error) {
	t := a.tracker
	t.mu.Lock()
	t.ids[activity.GetInfo(ctx).WorkflowExecution.ID] = true
	t.active++
	t.last = time.Now()
	t.mu.Unlock()
	defer func() { t.mu.Lock(); t.active--; t.last = time.Now(); t.mu.Unlock() }()
	return a.Next.ExecuteActivity(ctx, in)
}

func (t *tracker) snapshot() ([]string, int, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.ids))
	for id := range t.ids {
		ids = append(ids, id)
	}
	return ids, t.active, time.Since(t.last)
}
