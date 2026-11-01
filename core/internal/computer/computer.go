// Package computer is the client for the computer's guest RPC and the contract
// for reaching a computer. It knows nothing about the infrastructure the
// computer runs on: adapters in the gce and local packages supply that.
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
	"nori/internal/observability"
)

// Client speaks the guest RPC over a Connection that an adapter established.
type Client struct {
	conn      Connection
	once      sync.Once
	receipts  bool
	taskTabs  bool
	jobs      bool
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

// Connection is the path to a guest RPC that an adapter has established. It is
// all a provider has to produce: everything after it is the same on every
// infrastructure.
type Connection struct {
	// BaseURL is the origin of the guest RPC, such as http://127.0.0.1:8090.
	BaseURL string
	// HTTP carries the RPC. The adapter chooses its transport, proxy and redirect policy.
	HTTP *http.Client
	// Close releases the transport, for example by stopping a tunnel process.
	// Optional. It runs once, before idle HTTP connections are closed.
	Close func()
	// Done, when set, closes if the transport dies on its own. The lifecycle
	// loop in Managed dials again after that.
	Done <-chan struct{}
}

// NewClient wraps an established connection. run selects the cancellation scope.
func NewClient(conn Connection, run string) *Client {
	if conn.HTTP == nil {
		conn.HTTP = &http.Client{}
	}
	return &Client{conn: conn, run: run}
}

// Health is what the guest reports at /healthz.
type Health struct {
	Ready    bool `json:"ready"`
	Receipts bool `json:"operation_receipts_v1"`
	TaskTabs bool `json:"browser_task_tabs_v1"`
	Jobs     bool `json:"shell_jobs_v1"`
}

// Probe asks the guest for its health, waiting at most three seconds. It records
// the capabilities the guest reports, which RequireReceipts later enforces.
// Transport failures are returned as they are; an answer that is not HTTP 200
// with a health body wraps ErrUnhealthy.
func (c *Client) Probe(ctx context.Context) (Health, error) {
	check, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(check, http.MethodGet, c.conn.BaseURL+"/healthz", nil)
	if err != nil {
		return Health{}, err
	}
	resp, err := c.conn.HTTP.Do(req)
	if err != nil {
		return Health{}, err
	}
	var health Health
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("%w: HTTP %d", ErrUnhealthy, resp.StatusCode)
	}
	if decodeErr != nil {
		return Health{}, fmt.Errorf("%w: %v", ErrUnhealthy, decodeErr)
	}
	c.receipts, c.taskTabs, c.jobs = health.Receipts, health.TaskTabs, health.Jobs
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

func (c *Client) post(ctx context.Context, path string, body any, max int) (response []byte, err error) {
	operation := "other"
	switch path {
	case "/exec":
		operation = "exec"
	case "/navigate":
		operation = "navigate"
	case "/screenshot":
		operation = "screenshot"
	case "/operation":
		operation = "operation_status"
	case "/operation/cancel":
		operation = "operation_cancel"
	case "/cancel":
		operation = "cancel"
	case "/browser/close":
		operation = "browser_close"
	case "/job/start":
		operation = "job_start"
	case "/job/status":
		operation = "job_status"
	case "/job/stop":
		operation = "job_stop"
	case "/job/stop-task":
		operation = "job_stop_task"
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
		req.Header.Set("Nori-Operation-ID", c.operation)
	}
	for key, value := range observability.InjectTrace(ctx) {
		req.Header.Set(key, value)
	}
	resp, e := c.conn.HTTP.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("Nori-Computer-Draining") == "true" {
		return nil, ErrDraining
	}
	if path == "/screenshot" && resp.StatusCode == http.StatusConflict {
		var refusal struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&refusal) == nil && refusal.Error == "browser_restarted" {
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
	data, e := c.post(ctx, "/cancel", map[string]any{"run_id": c.run, "task_id": c.taskID}, 4096)
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
	data, e := c.post(ctx, "/exec", body, 512<<10)
	if e != nil {
		return nil, e
	}
	var result map[string]any
	if e = json.Unmarshal(data, &result); e != nil {
		return nil, e
	}
	return bound(result), nil
}
func (c *Client) Navigate(ctx context.Context, address string) (map[string]any, error) {
	u, e := url.Parse(address)
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("URL must use HTTP or HTTPS")
	}
	data, e := c.post(ctx, "/navigate", map[string]any{"run_id": c.run, "task_id": c.taskID, "url": address}, 512<<10)
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
	data, e := c.post(ctx, "/screenshot", map[string]any{"run_id": c.run, "task_id": c.taskID}, 2<<20)
	if e == nil && (len(data) < 8 || !bytes.Equal(data[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10})) {
		return nil, errors.New("computer returned invalid PNG")
	}
	return data, e
}

// CloseTaskBrowser releases only this task's owned tab. Call after every revision
// cancellation scope has acknowledged stopped work and terminal state is committed.
func (c *Client) CloseTaskBrowser(ctx context.Context, taskID string) error {
	data, err := c.post(ctx, "/browser/close", map[string]string{"task_id": taskID}, 4096)
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
