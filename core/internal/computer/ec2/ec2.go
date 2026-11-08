package ec2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/config"
	sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"io"
	"net"
	"net/http"
	"nori/internal/computer"
	"os"
	"os/exec"
	"strings"
)

type Config struct{ Region, Instance string }
type Host struct {
	api *sdk.Client
	cfg Config
}

func New(c Config) computer.Provider {
	return computer.Managed(func(ctx context.Context) (computer.Host, error) { return OpenHost(ctx, c) })
}
func OpenHost(ctx context.Context, c Config) (*Host, error) {
	if c.Region == "" || c.Instance == "" {
		return nil, errors.New("AWS region and computer instance are required")
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.Region))
	if err != nil {
		return nil, err
	}
	return &Host{api: sdk.NewFromConfig(cfg), cfg: c}, nil
}
func (h *Host) State(ctx context.Context) (computer.State, error) {
	out, err := h.api.DescribeInstances(ctx, &sdk.DescribeInstancesInput{InstanceIds: []string{h.cfg.Instance}})
	if err != nil {
		return 0, err
	}
	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			if i.InstanceId == nil || *i.InstanceId != h.cfg.Instance || i.State == nil {
				continue
			}
			switch i.State.Name {
			case types.InstanceStateNameStopped:
				return computer.Stopped, nil
			case types.InstanceStateNameStopping:
				return computer.Stopping, nil
			case types.InstanceStateNamePending:
				return computer.Starting, nil
			case types.InstanceStateNameRunning:
				return computer.Running, nil
			default:
				return 0, fmt.Errorf("computer unavailable (%s)", i.State.Name)
			}
		}
	}
	return 0, errors.New("computer instance not found")
}
func (h *Host) Start(ctx context.Context) error {
	_, err := h.api.StartInstances(ctx, &sdk.StartInstancesInput{InstanceIds: []string{h.cfg.Instance}})
	return err
}

// Stop preserves the instance and disks; it never terminates or force-stops compute.
// The guest's existing idle shutdown is still responsible for routine stopping.
func (h *Host) Stop(ctx context.Context) error {
	_, err := h.api.StopInstances(ctx, &sdk.StopInstancesInput{InstanceIds: []string{h.cfg.Instance}})
	return err
}
func (h *Host) Dial(context.Context) (computer.Connection, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return computer.Connection{}, err
	}
	address := listener.Addr().String()
	listener.Close()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return computer.Connection{}, err
	}
	parameters, _ := json.Marshal(map[string][]string{"portNumber": {"8090"}, "localPortNumber": {port}})
	cmd := exec.Command("aws", "ssm", "start-session", "--region", h.cfg.Region, "--target", h.cfg.Instance, "--document-name", "AWS-StartPortForwardingSession", "--parameters", string(parameters))
	// The tunnel needs only AWS identity and executable lookup, never model or management keys.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "PATH" || name == "HOME" || name == "TMPDIR" || strings.HasPrefix(name, "AWS_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return computer.Connection{}, err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	return computer.Connection{BaseURL: "http://" + address, HTTP: &http.Client{}, Done: done, Close: func() { _ = cmd.Process.Kill(); <-done }}, nil
}
