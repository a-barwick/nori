// Package gce reaches the computer as a Compute Engine instance. The Compute
// REST API starts the instance and reports its state, and an IAP TCP tunnel
// (gcloud compute start-iap-tunnel) carries the guest RPC on port 8090.
package gce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"

	"golang.org/x/oauth2/google"
	"nori/internal/computer"
)

const (
	computeScope = "https://www.googleapis.com/auth/compute"
	rpcPort      = "8090"
)

// Config names the instance. Nothing defaults: any empty field fails the first
// connection, so a process that never uses the computer does not need one.
type Config struct{ Project, Zone, Instance string }

func New(c Config) computer.Provider {
	return computer.Managed(func(ctx context.Context) (computer.Host, error) {
		if c.Project == "" || c.Zone == "" || c.Instance == "" {
			return nil, errors.New("computer project, zone and instance are required")
		}
		api, err := google.DefaultClient(ctx, computeScope)
		if err != nil {
			return nil, err
		}
		target := "https://compute.googleapis.com/compute/v1/projects/" + url.PathEscape(c.Project) +
			"/zones/" + url.PathEscape(c.Zone) + "/instances/" + url.PathEscape(c.Instance)
		return &instance{config: c, api: api, target: target}, nil
	})
}

type instance struct {
	config Config
	api    *http.Client
	target string
}

// State maps the instance status onto the lifecycle states. Only TERMINATED
// means stopped: STOPPING can still be flushing the guest disk.
func (i *instance) State(ctx context.Context) (computer.State, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, i.target, nil)
	if err != nil {
		return 0, err
	}
	resp, err := i.api.Do(req)
	if err != nil {
		return 0, err
	}
	var vm struct {
		Status string `json:"status"`
	}
	if err = readJSON(resp, &vm); err != nil {
		return 0, err
	}
	switch vm.Status {
	case "TERMINATED":
		return computer.Stopped, nil
	case "STOPPING":
		return computer.Stopping, nil
	case "STAGING", "PROVISIONING":
		return computer.Starting, nil
	case "RUNNING":
		return computer.Running, nil
	default:
		return 0, fmt.Errorf("computer unavailable (%s)", vm.Status)
	}
}

func (i *instance) Start(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.target+"/start", nil)
	if err != nil {
		return err
	}
	resp, err := i.api.Do(req)
	if err != nil {
		return err
	}
	// A concurrent on-demand start is harmless; the caller re-reads the state.
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		return nil
	}
	return readJSON(resp, nil)
}

// Dial opens an IAP tunnel on a free loopback port. The tunnel stays up until
// the client closes, because /cancel must be acknowledged through it.
func (i *instance) Dial(context.Context) (computer.Connection, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return computer.Connection{}, err
	}
	address := listener.Addr().String()
	listener.Close()
	cmd := exec.Command("gcloud", "compute", "start-iap-tunnel", i.config.Instance, rpcPort, "--project="+i.config.Project,
		"--zone="+i.config.Zone, "--local-host-port="+address, "--quiet", "--verbosity=error", "--iap-tunnel-disable-connection-check")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return computer.Connection{}, err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return computer.Connection{
		BaseURL: "http://" + address,
		HTTP:    &http.Client{},
		Close:   func() { _ = cmd.Process.Kill(); <-done },
		Done:    done,
	}, nil
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
