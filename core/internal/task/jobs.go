package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/genai"
	"nori/internal/agent"
	"nori/internal/computer"
	"nori/internal/observability"
	"nori/internal/state"
)

const (
	jobPollInterval = 15 * time.Second
	// A wait is a chain of bounded activities. A pending activity is what wakes a
	// request worker after its poll window, and a timer would not. One worker stays
	// up while a job runs.
	jobWaitSlice       = 200 * time.Second
	jobPollFailures    = 3
	jobProgressMaxText = 300
)

type JobWaitInput struct {
	Step
	Ref      string
	Index    int
	Position int64
}
type JobWaitOutcome struct{ Ended, Changed bool }

func existingJobTool(name string) bool {
	return name == "job_status" || name == "job_wait" || name == "job_stop"
}

// lostJob is the only supported answer about a job on a computer that isn't running.
func lostJob(call *genai.FunctionCall) map[string]any {
	id, _ := call.Args["job_id"].(string)
	return map[string]any{"job_id": id, "state": "lost", "reason": "The computer is stopped, so this job isn't running and its result wasn't observed."}
}

// toolOutcome maps what a tool returned to its operation record. A started job or
// a running one is only confirmed as observed; its own state reports the result.
func toolOutcome(output map[string]any) string {
	if code, ok := output["exit_code"].(float64); ok && code != 0 {
		return "failed"
	}
	if available, ok := output["page_available"].(bool); ok && !available {
		return "failed"
	}
	if available, ok := output["available"].(bool); ok && !available {
		return "failed"
	}
	switch output["state"] {
	case "lost", "timed_out", "failed_to_start", "stop_unconfirmed":
		return "failed"
	}
	return "confirmed"
}

func jobEnded(state any) bool {
	text, _ := state.(string)
	return text != "" && text != "running"
}

// jobTransition reports which job this result started or showed ended, so the
// workflow knows what terminal cleanup must stop.
func jobTransition(call *genai.FunctionCall, result map[string]any) (started, ended string) {
	switch call.Name {
	case "job_start":
		if id, _ := result["job_id"].(string); id != "" && result["outcome"] == "confirmed" && !jobEnded(result["state"]) {
			return id, ""
		}
	case "job_status", "job_wait", "job_stop":
		if id, _ := call.Args["job_id"].(string); jobEnded(result["state"]) {
			return "", id
		}
	}
	return "", ""
}

func (c *Checkpoint) trackJobs(output RefToolOutcome) {
	if output.StartedJob != "" {
		c.Jobs = append(c.Jobs, output.StartedJob)
	}
	if output.EndedJob != "" {
		for i, id := range c.Jobs {
			if id == output.EndedJob {
				c.Jobs = append(c.Jobs[:i:i], c.Jobs[i+1:]...)
				break
			}
		}
	}
}

// stopTask ends everything a finished task owns. An unconfirmed job leaves Stopped
// false so the task can't report a confirmed stop.
func (c *Checkpoint) stopTask(w workflow.Context, id string) error {
	var a *Activities
	if err := c.stopComputer(w, id); err != nil {
		return err
	}
	if len(c.Jobs) == 0 {
		return nil
	}
	if err := workflow.ExecuteActivity(w, a.StopJobs, id).Get(w, nil); err != nil {
		c.Stopped = false
		return err
	}
	c.Jobs = nil
	return nil
}

// awaitJob returns true once the job has ended and false when Austin's instructions
// changed. No model call runs while it waits.
func (c *Checkpoint) awaitJob(ctx workflow.Context, wait func() error, id string, call CallRef) (bool, error) {
	var a *Activities
	for {
		var outcome JobWaitOutcome
		err := workflow.ExecuteActivity(ctx, a.JobWaitRef, JobWaitInput{Step: Step{id, c.Revision}, Ref: c.Ref, Index: call.Index, Position: c.Position}).Get(ctx, &outcome)
		switch {
		case superseded(err):
			return false, nil
		case applicationKind(err, "paused"):
			if err = wait(); err != nil {
				return false, err
			}
			continue
		case err != nil:
			return false, err
		case outcome.Changed:
			return false, nil
		case outcome.Ended:
			return true, nil
		}
	}
}

// JobWaitRef polls one job for at most one slice. It reports an end state the
// workflow then records through the ordinary job_wait operation.
func (a *Activities) JobWaitRef(parent context.Context, in JobWaitInput) (out JobWaitOutcome, err error) {
	contents, err := a.history(parent, in.RunID, in.Ref)
	if err != nil {
		return out, classified(err)
	}
	call, err := lastCall(contents, in.Index)
	if err != nil {
		return out, classified(err)
	}
	id, _ := call.Args["job_id"].(string)
	ctx, done, run, err := a.bounded(parent, in.Step, true)
	if err != nil {
		return out, err
	}
	defer done()
	ctx, span := startActivity(ctx, "task.job_wait", "TOOL", run, in.Revision)
	defer func() { recordActivityFailure(ctx, err, "task.job_wait"); span.End() }()
	// Interrupted polling learns why from the saved task, not the canceled context.
	boundary := func() (JobWaitOutcome, error) {
		check, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, guardErr := a.Store.Guard(check, in.RunID, in.Revision); guardErr != nil {
			return JobWaitOutcome{}, classified(guardErr)
		}
		return JobWaitOutcome{Changed: true}, nil
	}
	c, err := computer.Attach(observability.SuppressErrorCapture(ctx), a.Agent.Computer, computerRun(in.Step))
	if errors.Is(err, computer.ErrStopped) {
		return JobWaitOutcome{Ended: true}, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return boundary()
		}
		// The recorded job_wait operation reports the failed read to the model.
		return JobWaitOutcome{Ended: true}, nil
	}
	defer c.Close()
	c.SetTaskID(in.RunID)
	deadline := time.Now().Add(jobWaitSlice)
	failures := 0
	for time.Now().Before(deadline) {
		// Reconciling is the same check a model call makes before acting on newer input.
		snap, readErr := a.Reconcile(ctx, in.RunID)
		if ctx.Err() != nil {
			return boundary()
		}
		if readErr != nil && expectedBoundary(readErr) {
			return JobWaitOutcome{}, readErr
		}
		if readErr == nil && (snap.Run.Revision != in.Revision || snap.Run.Position != in.Position) {
			return JobWaitOutcome{Changed: true}, nil
		}
		status, statusErr := c.JobStatus(ctx, id)
		if ctx.Err() != nil {
			return boundary()
		}
		if statusErr != nil || readErr != nil {
			if failures++; failures >= jobPollFailures {
				return JobWaitOutcome{Ended: true}, nil
			}
		} else {
			failures = 0
			if jobEnded(status["state"]) {
				return JobWaitOutcome{Ended: true}, nil
			}
			if e := a.Store.SetProgress(ctx, in.RunID, in.Revision, jobProgress(status)); e != nil && expectedBoundary(e) {
				return boundary()
			}
		}
		activity.RecordHeartbeat(ctx)
		select {
		case <-ctx.Done():
			return boundary()
		case <-time.After(jobPollInterval):
		}
	}
	return JobWaitOutcome{}, nil
}

func jobProgress(status map[string]any) string {
	seconds, _ := status["elapsed_seconds"].(float64)
	text := fmt.Sprintf("Job running for %s.", time.Duration(seconds)*time.Second)
	tail, _ := status["output_tail"].(string)
	lines := strings.Split(strings.TrimSpace(tail), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		if len(last) > jobProgressMaxText {
			last = last[len(last)-jobProgressMaxText:]
			for !utf8.ValidString(last) {
				last = last[1:]
			}
		}
		text += " Latest output: " + last
	}
	return text
}

// StopJobs ends every job this task owns. A stopped computer has no running jobs.
func (a *Activities) StopJobs(ctx context.Context, id string) (err error) {
	ctx, span := startActivity(ctx, "task.stop_jobs", "CHAIN", a.activityRun(ctx, id), 0)
	defer func() { recordActivityFailure(ctx, err, "task.stop_jobs"); span.End() }()
	return classified(stopTaskJobs(observability.SuppressErrorCapture(ctx), a.Agent.Computer, id))
}

func stopTaskJobs(ctx context.Context, config computer.Config, id string) error {
	for attempt := 0; ; attempt++ {
		c, err := computer.Attach(ctx, config, id)
		if errors.Is(err, computer.ErrStopped) {
			return nil
		}
		if err != nil {
			return err
		}
		err = c.StopTaskJobs(ctx, id)
		c.Close()
		if attempt > 0 || !errors.Is(err, computer.ErrDraining) {
			return err
		}
	}
}

// runtimeTool runs a tool that needs no computer. It keeps the same operation
// record as a computer tool and is never repeated automatically.
func (a *Activities) runtimeTool(ctx context.Context, in ToolInput, op *state.OperationRecord, recovering bool, persist func(string, map[string]any, error) (map[string]any, error)) (map[string]any, error) {
	if recovering {
		// The earlier attempt may have been billed and its cost was not returned.
		return persist("failed", map[string]any{"cost_unknown": in.Call.Name == "screenshot_inspect", "cost_usd": recoveredCost(in.Call.Name)}, errors.New("the worker stopped before this read finished; it was not repeated"))
	}
	if _, err := a.Store.Guard(ctx, in.RunID, in.Revision); err != nil {
		if _, saveErr := persist("skipped", nil, err); saveErr != nil {
			return nil, saveErr
		}
		return nil, classified(err)
	}
	op.Attempt = 1
	if err := a.Store.(state.TaskEvidenceStore).PutOperation(ctx, in.RunID, *op); err != nil {
		return nil, classified(err)
	}
	output, err := a.Agent.CallRuntimeTool(observability.SuppressErrorCapture(ctx), in.RunID, in.Call.Name, in.Call.Args)
	if err != nil {
		return persist("failed", output, err)
	}
	return persist(toolOutcome(output), output, nil)
}

func recoveredCost(name string) float64 {
	if name == "web_search" {
		return agent.SearchCostUSD
	}
	return 0
}
