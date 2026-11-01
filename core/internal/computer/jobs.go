package computer

import (
	"context"
	"encoding/json"
	"errors"
)

// RequireJobs refuses a job on a guest image older than the job protocol.
func (c *Client) RequireJobs() error {
	if !c.jobs {
		return errors.New("guest shell jobs v1 is required; the computer agent needs updating")
	}
	return nil
}

func (c *Client) jobCall(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	data, err := c.post(ctx, path, body, 64<<10)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return bound(result), nil
}

// JobStart uses the bound operation as the job's identity, so a lost response can
// be inspected through its receipt without starting a second copy.
func (c *Client) JobStart(ctx context.Context, command string) (map[string]any, error) {
	return c.jobCall(ctx, "/job/start", map[string]any{"run_id": c.run, "task_id": c.taskID, "job_id": c.operation, "command": command})
}

func (c *Client) JobStatus(ctx context.Context, jobID string) (map[string]any, error) {
	return c.jobCall(ctx, "/job/status", map[string]any{"task_id": c.taskID, "job_id": jobID})
}

func (c *Client) JobStop(ctx context.Context, jobID string) (map[string]any, error) {
	return c.jobCall(ctx, "/job/stop", map[string]any{"task_id": c.taskID, "job_id": jobID})
}

// StopTaskJobs ends every job a task owns. A guest older than the job protocol
// cannot have started one, so it has nothing to stop.
func (c *Client) StopTaskJobs(ctx context.Context, taskID string) error {
	if !c.jobs {
		return nil
	}
	data, err := c.post(ctx, "/job/stop-task", map[string]string{"task_id": taskID}, 4096)
	if err != nil {
		return err
	}
	var ack struct {
		Stopped bool `json:"stopped"`
	}
	if err = json.Unmarshal(data, &ack); err != nil {
		return err
	}
	if !ack.Stopped {
		return errors.New("computer did not acknowledge stopped jobs")
	}
	return nil
}
