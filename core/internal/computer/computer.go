package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nori/internal/computer/rpc"
	"nori/internal/observability"
)

type Client struct {
	conn      Connection
	once      sync.Once
	receipts  bool
	taskTabs  bool
	jobs      bool
	sandbox   bool
	reopen    bool
	files     bool
	operation string
	taskID    string
	run       string
}

var ErrStopped = errors.New("computer is stopped")

// ErrBrowserRestarted proves a screenshot was refused before capture because
// its saved page belonged to a browser process that has exited.
var ErrBrowserRestarted = errors.New("browser restarted; navigate again before taking a screenshot")

// ErrDraining proves that the host rejected this RPC before forwarding it to
// the guest. Only this explicit rejection is safe to reconnect and send again.
var ErrDraining = errors.New("computer is stopping after idle; request was not dispatched")

// ErrUnhealthy means the guest answered its health check without saying it is serving.
var ErrUnhealthy = errors.New("computer health check failed")

type Connection struct {
	BaseURL string
	HTTP    *http.Client
	// Close releases the transport, for example by stopping a tunnel process.
	// Optional. It runs once, before idle HTTP connections are closed.
	Close func()
	// Done, when set, closes if the transport dies on its own. The lifecycle
	// loop in Managed dials again after that.
	Done <-chan struct{}
}

func NewClient(conn Connection, run string) *Client {
	if conn.HTTP == nil {
		conn.HTTP = &http.Client{}
	}
	return &Client{conn: conn, run: run}
}

// Probe asks the guest for its health, waiting at most three seconds. It records
// the capabilities the guest reports, which RequireReceipts later enforces.
// Transport failures are returned as they are; an answer that is not HTTP 200
// with a health body wraps ErrUnhealthy.
func (c *Client) Probe(ctx context.Context) (rpc.Health, error) {
	check, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(check, http.MethodGet, c.conn.BaseURL+rpc.HealthPath, nil)
	if err != nil {
		return rpc.Health{}, err
	}
	resp, err := c.conn.HTTP.Do(req)
	if err != nil {
		return rpc.Health{}, err
	}
	var health rpc.Health
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return rpc.Health{}, fmt.Errorf("%w: HTTP %d", ErrUnhealthy, resp.StatusCode)
	}
	if decodeErr != nil {
		return rpc.Health{}, fmt.Errorf("%w: %v", ErrUnhealthy, decodeErr)
	}
	c.receipts, c.taskTabs, c.jobs, c.files, c.sandbox = health.Receipts, health.TaskTabs, health.Jobs, health.Files, health.Sandbox
	c.reopen = health.Reopen
	return health, nil
}

// Close releases the connection. It is safe to call more than once.
func (c *Client) Close() {
	c.once.Do(func() {
		if c.conn.Close != nil {
			c.conn.Close()
		}
		c.conn.HTTP.CloseIdleConnections()
	})
}

// Guest receipts hash the path and exact JSON bytes across worker revisions.
func (c *Client) post(ctx context.Context, path string, body any, max int) (response []byte, err error) {
	operation := "other"
	switch path {
	case rpc.ExecPath:
		operation = "exec"
	case rpc.NavigatePath:
		operation = "navigate"
	case rpc.ScreenshotPath:
		operation = "screenshot"
	case rpc.OperationPath:
		operation = "operation_status"
	case rpc.CancelReadPath:
		operation = "operation_cancel"
	case rpc.CancelPath:
		operation = "cancel"
	case rpc.CloseBrowserPath:
		operation = "browser_close"
	case rpc.StartJobPath:
		operation = "job_start"
	case rpc.JobStatusPath:
		operation = "job_status"
	case rpc.StopJobPath:
		operation = "job_stop"
	case rpc.StopTaskJobsPath:
		operation = "job_stop_task"
	case rpc.SandboxRunPath:
		operation = "sandbox_run"
	case rpc.ReopenRunPath:
		operation = "run_reopen"
	}
	ctx, span := otel.Tracer("nori/computer").Start(ctx, "computer."+operation, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID()),
			attribute.String("nori.computer.operation", operation), attribute.String("nori.task.id", c.taskID), attribute.String("nori.operation.id", c.operation)))
	defer func() {
		if err != nil && !errors.Is(err, ErrDraining) && !errors.Is(err, ErrBrowserRestarted) && !errors.Is(ctx.Err(), context.Canceled) {
			observability.RecordErrorCategory(ctx, "computer.rpc", err, attribute.String("nori.computer.operation", operation))
			observability.Event(ctx, "computer.rpc.failed", attribute.String("nori.computer.operation", operation))
		} else if err == nil {
			observability.Event(ctx, "computer.rpc.completed", attribute.String("nori.computer.operation", operation))
		}
		span.End()
	}()
	data, e := json.Marshal(body)
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.conn.BaseURL+path, bytes.NewReader(data))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	if c.operation != "" {
		req.Header.Set(rpc.OperationIDHeader, c.operation)
	}
	for key, value := range observability.InjectTrace(ctx) {
		req.Header.Set(key, value)
	}
	resp, e := c.conn.HTTP.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get(rpc.DrainingHeader) == "true" {
		return nil, ErrDraining
	}
	if path == rpc.ScreenshotPath && resp.StatusCode == http.StatusConflict {
		var refusal struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&refusal) == nil && refusal.Error == rpc.BrowserRestarted {
			return nil, ErrBrowserRestarted
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &RPCError{Path: path, Status: resp.StatusCode}
	}
	data, e = io.ReadAll(io.LimitReader(resp.Body, int64(max+1)))
	if len(data) > max {
		return nil, errors.New("computer response exceeds limit")
	}
	return data, e
}
func (c *Client) Cancel(ctx context.Context) error {
	data, e := c.post(ctx, rpc.CancelPath, map[string]any{"run_id": c.run, "task_id": c.taskID}, 4096)
	if e != nil {
		return e
	}
	var ack struct {
		Stopped bool `json:"stopped"`
	}
	if e = json.Unmarshal(data, &ack); e != nil {
		return e
	}
	if !ack.Stopped {
		return errors.New("computer did not acknowledge stopped operations")
	}
	return nil
}
func (c *Client) Exec(ctx context.Context, command string) (map[string]any, error) {
	body := map[string]any{"run_id": c.run, "command": command, "timeout_seconds": 45}
	if c.taskID != "" {
		body["task_id"] = c.taskID
	}
	data, e := c.post(ctx, rpc.ExecPath, body, 512<<10)
	if e != nil {
		return nil, e
	}
	var result map[string]any
	if e = json.Unmarshal(data, &result); e != nil {
		return nil, e
	}
	return bound(result), nil
}

func (c *Client) PutFile(ctx context.Context, name string, data []byte) (string, error) {
	if !c.files {
		return "", errors.New("the computer needs an update before it can receive files")
	}
	response, err := c.post(ctx, rpc.PutFilePath, map[string]any{"run_id": c.run, "task_id": c.taskID, "name": name, "data": data}, 64<<10)
	if err != nil {
		return "", err
	}
	var saved struct {
		Path string `json:"path"`
	}
	if err = json.Unmarshal(response, &saved); err != nil || saved.Path == "" {
		return "", errors.New("the computer didn't confirm the file")
	}
	return saved.Path, nil
}

func (c *Client) GetFile(ctx context.Context, path string) ([]byte, error) {
	if !c.files {
		return nil, errors.New("the computer needs an update before it can send files")
	}
	response, err := c.post(ctx, rpc.GetFilePath, map[string]any{"run_id": c.run, "task_id": c.taskID, "path": path}, 15<<20)
	if err != nil {
		return nil, err
	}
	var file struct {
		Data []byte `json:"data"`
	}
	if err = json.Unmarshal(response, &file); err != nil {
		return nil, errors.New("the computer returned an unreadable file")
	}
	return file.Data, nil
}

func (c *Client) Navigate(ctx context.Context, address string) (map[string]any, error) {
	u, e := url.Parse(address)
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("URL must use HTTP or HTTPS")
	}
	data, e := c.post(ctx, rpc.NavigatePath, map[string]any{"run_id": c.run, "task_id": c.taskID, "url": address}, 512<<10)
	if e != nil {
		return nil, e
	}
	var result map[string]any
	if e = json.Unmarshal(data, &result); e != nil {
		return nil, e
	}
	return bound(result), nil
}
func (c *Client) Screenshot(ctx context.Context) ([]byte, error) {
	data, e := c.post(ctx, rpc.ScreenshotPath, map[string]any{"run_id": c.run, "task_id": c.taskID}, rpc.MaxScreenshot)
	if e == nil && (len(data) < 8 || !bytes.Equal(data[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10})) {
		return nil, errors.New("computer returned invalid PNG")
	}
	return data, e
}

// CloseTaskBrowser releases only this task's owned tab. Call after every revision
// cancellation scope has acknowledged stopped work and terminal state is committed.
func (c *Client) CloseTaskBrowser(ctx context.Context, taskID string) error {
	data, err := c.post(ctx, rpc.CloseBrowserPath, map[string]string{"task_id": taskID}, 4096)
	if err != nil {
		return err
	}
	var ack struct {
		Closed bool `json:"closed"`
	}
	if err = json.Unmarshal(data, &ack); err != nil {
		return err
	}
	if !ack.Closed {
		return errors.New("computer did not acknowledge task browser cleanup")
	}
	return nil
}
func bound(result map[string]any) map[string]any {
	encoded, _ := json.Marshal(result)
	if len(encoded) <= 32<<10 {
		return result
	}
	output := string(encoded[:(32<<10)-100])
	for {
		bounded := map[string]any{"output": output, "truncated": true}
		data, _ := json.Marshal(bounded)
		if len(data) <= 32<<10 {
			return bounded
		}
		output = output[:len(output)/2]
	}
}

// SetOperation binds a durable ID to a single activity's RPC. The guest records
// the request and response before acknowledging it and never replays uncertainty.
func (c *Client) SetOperation(id string) { c.operation = id }
func (c *Client) OperationID() string    { return c.operation }

func (c *Client) SetTaskID(id string) { c.taskID = id }

func (c *Client) RequireReceipts() error {
	if !c.receipts {
		return errors.New("guest operation receipts v1 is required before durable tool execution")
	}
	if !c.taskTabs {
		return errors.New("guest task-owned browser tabs v1 is required before durable tool execution")
	}
	return nil
}

// SetRun selects another cancellation scope on the same cleanup tunnel.
func (c *Client) SetRun(id string) { c.run = id }

// SandboxRun runs a packaged program in the guest's sandbox. It fails when the guest cannot enforce the limits.
func (c *Client) SandboxRun(ctx context.Context, req rpc.SandboxRequest) (rpc.SandboxResult, error) {
	var result rpc.SandboxResult
	if !c.sandbox {
		return result, errors.New("this computer cannot run sandboxed packages")
	}
	req.RunID, req.TaskID = c.run, c.taskID
	data, err := c.post(ctx, rpc.SandboxRunPath, req, 512<<10)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(data, &result)
	return result, err
}

// Reopen makes a run that was stopped while waiting accept operations again. Guests without the capability keep
// the earlier behavior.
func (c *Client) Reopen(ctx context.Context) error {
	if !c.reopen {
		return nil
	}
	_, err := c.post(ctx, rpc.ReopenRunPath, map[string]any{"run_id": c.run}, 4<<10)
	return err
}
