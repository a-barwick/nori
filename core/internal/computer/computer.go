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

	"golang.org/x/oauth2/google"
)

type Config struct{ Project, Zone, Instance string }
type Client struct {
	base      string
	http      *http.Client
	process   *exec.Cmd
	once      sync.Once
	receipts  bool
	operation string
	taskID    string
	run       string
	done      chan struct{}
}

var ErrStopped = errors.New("computer is stopped")

func Open(ctx context.Context, c Config, run string) (*Client, error) {
	return connect(ctx, c, run, true)
}

// Attach establishes cleanup access without starting paid compute.
func Attach(ctx context.Context, c Config, run string) (*Client, error) {
	return connect(ctx, c, run, false)
}
func connect(ctx context.Context, c Config, run string, allowStart bool) (*Client, error) {
	ctx, readyCancel := context.WithTimeout(ctx, 90*time.Second)
	defer readyCancel()
	if c.Project == "" || c.Zone == "" || c.Instance == "" {
		return nil, errors.New("computer project, zone and instance are required")
	}
	api, e := google.DefaultClient(ctx, "https://www.googleapis.com/auth/compute")
	if e != nil {
		return nil, e
	}
	target := "https://compute.googleapis.com/compute/v1/projects/" + url.PathEscape(c.Project) + "/zones/" + url.PathEscape(c.Zone) + "/instances/" + url.PathEscape(c.Instance)
	req, e := http.NewRequestWithContext(ctx, "GET", target, nil)
	if e != nil {
		return nil, e
	}
	resp, e := api.Do(req)
	if e != nil {
		return nil, e
	}
	var vm struct {
		Status string `json:"status"`
	}
	e = readJSON(resp, &vm)
	if e != nil {
		return nil, e
	}
	if vm.Status == "TERMINATED" {
		if !allowStart {
			return nil, ErrStopped
		}
		req, e = http.NewRequestWithContext(ctx, "POST", target+"/start", nil)
		if e != nil {
			return nil, e
		}
		resp, e = api.Do(req)
		if e != nil {
			return nil, e
		}
		if e = readJSON(resp, nil); e != nil {
			return nil, e
		}
	} else if vm.Status != "RUNNING" && vm.Status != "STAGING" && vm.Status != "PROVISIONING" {
		return nil, fmt.Errorf("computer unavailable (%s)", vm.Status)
	}
	for vm.Status != "RUNNING" {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
		req, e = http.NewRequestWithContext(ctx, "GET", target, nil)
		if e != nil {
			return nil, e
		}
		resp, e = api.Do(req)
		if e != nil {
			return nil, e
		}
		if e = readJSON(resp, &vm); e != nil {
			return nil, e
		}
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	address := listener.Addr().String()
	listener.Close()
	// Keep the tunnel alive until /cancel has acknowledged guest process exit.
	cmd := exec.Command("gcloud", "compute", "start-iap-tunnel", c.Instance, "8090", "--project="+c.Project, "--zone="+c.Zone, "--local-host-port="+address, "--quiet", "--verbosity=error", "--iap-tunnel-disable-connection-check")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	client := &Client{base: "http://" + address, http: &http.Client{}, process: cmd, run: run, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(client.done) }()
	for {
		req, e = http.NewRequestWithContext(ctx, "GET", client.base+"/healthz", nil)
		if e != nil {
			client.Close()
			return nil, e
		}
		resp, e = client.http.Do(req)
		if e == nil {
			var health struct {
				Receipts bool `json:"operation_receipts_v1"`
			}
			_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				client.receipts = health.Receipts
				return client, nil
			}
		}
		select {
		case <-client.done:
			client.Close()
			return nil, errors.New("IAP tunnel helper exited before computer readiness")
		case <-ctx.Done():
			client.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
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
func (c *Client) post(ctx context.Context, path string, body any, max int) ([]byte, error) {
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
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
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
	data, e := c.post(ctx, "/cancel", map[string]any{"run_id": c.run}, 4096)
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
	data, e := c.post(ctx, "/navigate", map[string]any{"run_id": c.run, "url": address}, 512<<10)
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
	data, e := c.post(ctx, "/screenshot", map[string]any{"run_id": c.run}, 2<<20)
	if e == nil && (len(data) < 8 || !bytes.Equal(data[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10})) {
		return nil, errors.New("computer returned invalid PNG")
	}
	return data, e
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
	return nil
}

// SetRun selects another cancellation scope on the same cleanup tunnel.
func (c *Client) SetRun(id string) { c.run = id }
