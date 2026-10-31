// Package computer connects to the existing Firecracker computer through IAP.
package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/oauth2/google"
	"nori/internal/observability"
)

type Config struct {
	Project, Zone, Instance string
	// LocalURL connects directly to a development computer RPC on a loopback IP.
	// It never starts a VM or creates an IAP tunnel.
	LocalURL string
}
type Client struct {
	base      string
	http      *http.Client
	process   *exec.Cmd
	once      sync.Once
	receipts  bool
	taskTabs  bool
	operation string
	taskID    string
	run       string
	done      chan struct{}
}

var ErrStopped = errors.New("computer is stopped")

// ErrBrowserRestarted proves a screenshot was refused before capture because
// its saved page belonged to a browser process that has exited.
var ErrBrowserRestarted = errors.New("browser restarted; navigate again before taking a screenshot")

// ErrDraining proves that the host rejected this RPC before forwarding it to
// the guest. Only this explicit rejection is safe to reconnect and send again.
var ErrDraining = errors.New("computer is stopping after idle; request was not dispatched")

func Open(ctx context.Context, c Config, run string) (*Client, error) {
	return tracedConnect(ctx, c, run, true)
}

// Attach establishes cleanup access without starting paid compute.
func Attach(ctx context.Context, c Config, run string) (*Client, error) {
	return tracedConnect(ctx, c, run, false)
}
func tracedConnect(ctx context.Context, config Config, run string, allowStart bool) (*Client, error) {
	ctx, span := otel.Tracer("nori/computer").Start(ctx, "computer.connect", trace.WithAttributes(attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID()), attribute.Bool("nori.computer.allow_start", allowStart)))
	defer span.End()
	client, err := connect(ctx, config, run, allowStart)
	if err != nil && !errors.Is(err, ErrStopped) && !errors.Is(ctx.Err(), context.Canceled) {
		observability.RecordErrorCategory(ctx, "computer.connect", err)
	}
	return client, err
}
func connect(ctx context.Context, c Config, run string, allowStart bool) (*Client, error) {
	if c.LocalURL != "" {
		return connectLocal(ctx, c.LocalURL, run)
	}
	// An idle stop may already be flushing the guest when a new task arrives.
	// Allow that stop to complete before starting it; no tool has been sent yet.
	ctx, readyCancel := context.WithTimeout(ctx, 180*time.Second)
	defer readyCancel()
	if c.Project == "" || c.Zone == "" || c.Instance == "" {
		return nil, errors.New("computer project, zone and instance are required")
	}
	api, err := google.DefaultClient(ctx, "https://www.googleapis.com/auth/compute")
	if err != nil {
		return nil, err
	}
	target := "https://compute.googleapis.com/compute/v1/projects/" + url.PathEscape(c.Project) + "/zones/" + url.PathEscape(c.Zone) + "/instances/" + url.PathEscape(c.Instance)
	var client *Client
	defer func() {
		if client != nil {
			client.Close()
		}
	}()
	startIssued := false
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		resp, err := api.Do(req)
		if err != nil {
			return nil, err
		}
		var vm struct {
			Status string `json:"status"`
		}
		if err = readJSON(resp, &vm); err != nil {
			return nil, err
		}
		if vm.Status != "RUNNING" && client != nil {
			client.Close()
			client = nil
		}
		switch vm.Status {
		case "TERMINATED":
			if !allowStart {
				return nil, ErrStopped
			}
			if !startIssued {
				req, err = http.NewRequestWithContext(ctx, http.MethodPost, target+"/start", nil)
				if err != nil {
					return nil, err
				}
				resp, err = api.Do(req)
				if err != nil {
					return nil, err
				}
				// A concurrent on-demand start is harmless; re-read its state.
				if resp.StatusCode == http.StatusConflict {
					resp.Body.Close()
				} else if err = readJSON(resp, nil); err != nil {
					return nil, err
				}
				startIssued = true
			}
		case "STOPPING":
			// Cleanup may only claim stopped after GCE reports TERMINATED.
			startIssued = false
		case "STAGING", "PROVISIONING":
		case "RUNNING":
			startIssued = false
			if client != nil {
				select {
				case <-client.done:
					client.Close()
					client = nil
				default:
				}
			}
			if client == nil {
				client, err = tunnel(c, run)
				if err != nil {
					return nil, err
				}
			}
			check, cancel := context.WithTimeout(ctx, 3*time.Second)
			req, err = http.NewRequestWithContext(check, http.MethodGet, client.base+"/healthz", nil)
			if err != nil {
				cancel()
				return nil, err
			}
			resp, err = client.http.Do(req)
			if err == nil {
				var health struct {
					Receipts bool `json:"operation_receipts_v1"`
					TaskTabs bool `json:"browser_task_tabs_v1"`
				}
				decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK && decodeErr == nil {
					client.receipts = health.Receipts
					client.taskTabs = health.TaskTabs
					ready := client
					client = nil
					cancel()
					return ready, nil
				}
			}
			cancel()
		default:
			return nil, fmt.Errorf("computer unavailable (%s)", vm.Status)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func connectLocal(ctx context.Context, address, run string) (*Client, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("local computer URL must be an HTTP loopback origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("local computer URL must use a literal loopback IP")
	}
	u.Path = ""
	c := &Client{base: u.String(), run: run, http: &http.Client{
		Transport:     &http.Transport{}, // Never send local RPC through an environment proxy.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	check, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(check, http.MethodGet, c.base+"/healthz", nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("local computer unavailable: %w", err)
	}
	var health struct {
		Ready    bool `json:"ready"`
		Receipts bool `json:"operation_receipts_v1"`
		TaskTabs bool `json:"browser_task_tabs_v1"`
	}
	err = readJSON(resp, &health)
	if err != nil || !health.Ready {
		c.Close()
		return nil, errors.New("local computer is not ready")
	}
	c.receipts, c.taskTabs = health.Receipts, health.TaskTabs
	return c, nil
}

func tunnel(c Config, run string) (*Client, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	listener.Close()
	// Keep the tunnel alive until /cancel has acknowledged guest process exit.
	cmd := exec.Command("gcloud", "compute", "start-iap-tunnel", c.Instance, "8090", "--project="+c.Project, "--zone="+c.Zone, "--local-host-port="+address, "--quiet", "--verbosity=error", "--iap-tunnel-disable-connection-check")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	client := &Client{base: "http://" + address, http: &http.Client{}, process: cmd, run: run, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(client.done) }()
	return client, nil
}

func readJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("computer API returned HTTP %d", resp.StatusCode)
	}
	if out == nil {
		_, e := io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
		return e
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(out)
}
func (c *Client) Close() {
	c.once.Do(func() {
		if c.process != nil && c.process.Process != nil {
			_ = c.process.Process.Kill()
			<-c.done
		}
		c.http.CloseIdleConnections()
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
	case "/cancel":
		operation = "cancel"
	case "/browser/close":
		operation = "browser_close"
	}
	ctx, span := otel.Tracer("nori/computer").Start(ctx, "computer."+operation, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("openinference.span.kind", "CHAIN"), attribute.String("session.id", observability.SessionID()), attribute.String("nori.computer.operation", operation), attribute.String("nori.task.id", c.taskID), attribute.String("nori.operation.id", c.operation)))
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
	req, e := http.NewRequestWithContext(ctx, "POST", c.base+path, bytes.NewReader(data))
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
	resp, e := c.http.Do(req)
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
		return nil, fmt.Errorf("computer %s returned HTTP %d", path, resp.StatusCode)
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
