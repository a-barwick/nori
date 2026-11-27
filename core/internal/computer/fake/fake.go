package fake

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"nori/internal/computer"
)

// ExecHandler optionally intercepts Exec calls in DSTs and unit tests.
type ExecHandler func(ctx context.Context, providerID string, req computer.ExecRequest, files map[string][]byte) (computer.ExecResult, bool, error)

// Faults injects deterministic failures into the fake provider for DSTs.
type Faults struct {
	EnsureErr        error
	ExecErr          error
	DestroyErr       error
	DesktopErr       error
	NetworkPolicyErr error
	CheckpointErr    error
	RestoreErr       error
}

type fakeInstance struct {
	spec          computer.Spec
	providerID    string
	status        computer.Status
	files         map[string][]byte
	checkpoints   map[string]map[string][]byte
	checkpointLog []computer.CheckpointInfo
	policy        computer.NetworkPolicy
	desktopURL    string
	desktopHeader http.Header
	lastActive    time.Time
}

// Provider is a deterministic in-memory computer.Provider for unit tests and DSTs.
type Provider struct {
	mu           sync.Mutex
	instances    map[string]*fakeInstance
	calls        []string
	nextCkptSeq  int
	nextJobSeq   int
	ExecCostUSD  float64
	Now          func() time.Time
	OnExec       ExecHandler
	Faults       Faults
	DesktopWSURL string
}

// New returns a deterministic in-memory fake Provider.
func New() *Provider {
	return &Provider{
		instances:    map[string]*fakeInstance{},
		ExecCostUSD:  0.0005,
		DesktopWSURL: "ws://127.0.0.1:18080/websockify",
	}
}

func (p *Provider) Name() string { return "fake" }

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *Provider) record(call string) {
	p.calls = append(p.calls, call)
}

// Calls returns a copy of all provider method calls recorded so far.
func (p *Provider) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

// InstancePolicy returns the last NetworkPolicy applied to providerID.
func (p *Provider) InstancePolicy(providerID string) (computer.NetworkPolicy, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[providerID]
	if !ok {
		return computer.NetworkPolicy{}, false
	}
	return inst.policy, true
}

// SetStatus directly updates a fake instance's status (for simulating sleep/crash in DSTs).
func (p *Provider) SetStatus(providerID string, status computer.Status) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if inst, ok := p.instances[providerID]; ok {
		inst.status = status
	}
}

func (p *Provider) Ensure(_ context.Context, spec computer.Spec) (computer.Instance, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record("ensure:" + spec.ComputerID)
	if p.Faults.EnsureErr != nil {
		return computer.Instance{}, p.Faults.EnsureErr
	}
	id := "fake-" + spec.ComputerID
	inst, ok := p.instances[id]
	if !ok {
		now := p.now()
		files := map[string][]byte{
			"/home/nori/.nori/bin/nori-desktop-start": []byte("#!/bin/sh\necho ok\n"),
			"/home/nori/.nori/bin/nori-browser":       []byte("#!/bin/sh\necho ok\n"),
		}
		ckptID := "ckpt_baseline"
		inst = &fakeInstance{
			spec:       spec,
			providerID: id,
			status:     computer.StatusReady,
			files:      files,
			checkpoints: map[string]map[string][]byte{
				ckptID: cloneFiles(files),
			},
			checkpointLog: []computer.CheckpointInfo{
				{ID: ckptID, Label: "baseline", CreatedAt: now},
			},
			desktopURL:    p.DesktopWSURL,
			desktopHeader: http.Header{"Authorization": []string{"Bearer fake-internal-token"}},
			lastActive:    now,
		}
		p.instances[id] = inst
		return computer.Instance{
			ProviderID:           id,
			Status:               computer.StatusReady,
			BaselineCheckpointID: ckptID,
			BootstrapVersion:     computer.DefaultBootstrapVer,
		}, nil
	}
	// Wake sleeping instance while preserving persistent disk.
	inst.status = computer.StatusReady
	inst.lastActive = p.now()
	baseline := ""
	if len(inst.checkpointLog) > 0 {
		baseline = inst.checkpointLog[0].ID
	}
	return computer.Instance{
		ProviderID:           id,
		Status:               inst.status,
		BaselineCheckpointID: baseline,
		BootstrapVersion:     computer.DefaultBootstrapVer,
	}, nil
}

func (p *Provider) Status(_ context.Context, providerID string) (computer.InstanceStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[providerID]
	if !ok {
		return computer.InstanceStatus{}, computer.ErrNotFound
	}
	var used int64
	for _, b := range inst.files {
		used += int64(len(b))
	}
	last := inst.lastActive
	return computer.InstanceStatus{
		Status:        inst.status,
		DiskUsedBytes: used,
		DiskMaxBytes:  int64(inst.spec.MaxDiskGB) << 30,
		Checkpoints:   slices.Clone(inst.checkpointLog),
		LastActiveAt:  &last,
	}, nil
}

func (p *Provider) Exec(ctx context.Context, providerID string, req computer.ExecRequest) (computer.ExecResult, error) {
	p.mu.Lock()
	inst, ok := p.instances[providerID]
	if !ok {
		p.mu.Unlock()
		return computer.ExecResult{}, computer.ErrNotFound
	}
	p.record("exec:" + req.Command)
	if p.Faults.ExecErr != nil {
		err := p.Faults.ExecErr
		p.mu.Unlock()
		return computer.ExecResult{}, err
	}
	inst.status = computer.StatusReady
	inst.lastActive = p.now()
	handler := p.OnExec
	cost := p.ExecCostUSD
	if req.Background {
		p.nextJobSeq++
		jobID := fmt.Sprintf("job_%d", p.nextJobSeq)
		p.mu.Unlock()
		return computer.ExecResult{ExitCode: 0, JobID: jobID, DurationMS: 5, CostUSD: cost}, nil
	}
	filesSnapshot := inst.files
	p.mu.Unlock()

	if handler != nil {
		res, handled, err := handler(ctx, providerID, req, filesSnapshot)
		if handled {
			if res.CostUSD == 0 {
				res.CostUSD = cost
			}
			return res, err
		}
	}

	// Built-in deterministic handlers for helper scripts & basic commands.
	cmd := strings.TrimSpace(req.Command)
	if strings.Contains(cmd, "nori-desktop-start") {
		return computer.ExecResult{
			ExitCode:   0,
			Stdout:     `{"ok":true,"display":":99","desktop_port":8080,"cdp_port":9222}` + "\n",
			DurationMS: 10,
			CostUSD:    cost,
		}, nil
	}
	if strings.HasPrefix(cmd, "echo ") {
		return computer.ExecResult{
			ExitCode:   0,
			Stdout:     strings.Trim(strings.TrimPrefix(cmd, "echo "), "\"'") + "\n",
			DurationMS: 5,
			CostUSD:    cost,
		}, nil
	}
	return computer.ExecResult{
		ExitCode:   0,
		Stdout:     "ok\n",
		DurationMS: 10,
		CostUSD:    cost,
	}, nil
}

func (p *Provider) ReadFile(_ context.Context, providerID, filePath string, maxBytes int64) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[providerID]
	if !ok {
		return nil, computer.ErrNotFound
	}
	data, ok := inst.files[filePath]
	if !ok {
		return nil, computer.ErrNotFound
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, computer.ErrFileTooLarge
	}
	inst.lastActive = p.now()
	return slices.Clone(data), nil
}

func (p *Provider) WriteFile(_ context.Context, providerID, filePath string, data []byte, _ uint32) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[providerID]
	if !ok {
		return computer.ErrNotFound
	}
	inst.files[filePath] = slices.Clone(data)
	inst.status = computer.StatusReady
	inst.lastActive = p.now()
	return nil
}

func (p *Provider) ListFiles(_ context.Context, providerID, dir string) ([]computer.FileEntry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[providerID]
	if !ok {
		return nil, computer.ErrNotFound
	}
	prefix := strings.TrimSuffix(dir, "/") + "/"
	seenDirs := map[string]bool{}
	var entries []computer.FileEntry
	for fullPath, content := range inst.files {
		if !strings.HasPrefix(fullPath, prefix) {
			continue
		}
		rest := strings.TrimPrefix(fullPath, prefix)
		if rest == "" {
			continue
		}
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			name := rest[:slash]
			if !seenDirs[name] {
				seenDirs[name] = true
				entries = append(entries, computer.FileEntry{
					Name:  name,
					Path:  path.Join(dir, name),
					IsDir: true,
					Mode:  "drwxr-xr-x",
				})
			}
			continue
		}
		entries = append(entries, computer.FileEntry{
			Name:    rest,
			Path:    fullPath,
			IsDir:   false,
			Size:    int64(len(content)),
			Mode:    "-rw-r--r--",
			ModTime: inst.lastActive,
		})
	}
	slices.SortFunc(entries, func(a, b computer.FileEntry) int {
		return strings.Compare(a.Name, b.Name)
	})
	return entries, nil
}

func (p *Provider) DeletePath(_ context.Context, providerID, targetPath string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[providerID]
	if !ok {
		return computer.ErrNotFound
	}
	prefix := strings.TrimSuffix(targetPath, "/") + "/"
	removed := false
	if _, exists := inst.files[targetPath]; exists {
		delete(inst.files, targetPath)
		removed = true
	}
	for k := range inst.files {
		if strings.HasPrefix(k, prefix) {
			delete(inst.files, k)
			removed = true
		}
	}
	if !removed {
		return computer.ErrNotFound
	}
	inst.lastActive = p.now()
	return nil
}

func (p *Provider) DesktopEndpoint(_ context.Context, providerID string) (computer.DesktopTarget, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Faults.DesktopErr != nil {
		return computer.DesktopTarget{}, p.Faults.DesktopErr
	}
	inst, ok := p.instances[providerID]
	if !ok {
		return computer.DesktopTarget{}, computer.ErrNotFound
	}
	return computer.DesktopTarget{
		WSURL:   inst.desktopURL,
		Headers: inst.desktopHeader.Clone(),
	}, nil
}

func (p *Provider) SetNetworkPolicy(_ context.Context, providerID string, policy computer.NetworkPolicy) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Faults.NetworkPolicyErr != nil {
		return p.Faults.NetworkPolicyErr
	}
	inst, ok := p.instances[providerID]
	if !ok {
		return computer.ErrNotFound
	}
	inst.policy = policy
	p.record("network_policy:" + providerID)
	return nil
}

func (p *Provider) Checkpoint(_ context.Context, providerID, label string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Faults.CheckpointErr != nil {
		return "", p.Faults.CheckpointErr
	}
	inst, ok := p.instances[providerID]
	if !ok {
		return "", computer.ErrNotFound
	}
	p.nextCkptSeq++
	id := fmt.Sprintf("ckpt_%d", p.nextCkptSeq)
	inst.checkpoints[id] = cloneFiles(inst.files)
	inst.checkpointLog = append(inst.checkpointLog, computer.CheckpointInfo{
		ID:        id,
		Label:     label,
		CreatedAt: p.now(),
	})
	p.record("checkpoint:" + id)
	return id, nil
}

func (p *Provider) Restore(_ context.Context, providerID, checkpointID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Faults.RestoreErr != nil {
		return p.Faults.RestoreErr
	}
	inst, ok := p.instances[providerID]
	if !ok {
		return computer.ErrNotFound
	}
	snap, ok := inst.checkpoints[checkpointID]
	if !ok {
		return errors.New("checkpoint not found")
	}
	inst.files = cloneFiles(snap)
	inst.status = computer.StatusReady
	inst.lastActive = p.now()
	p.record("restore:" + checkpointID)
	return nil
}

func (p *Provider) Sleep(_ context.Context, providerID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	inst, ok := p.instances[providerID]
	if !ok {
		return computer.ErrNotFound
	}
	inst.status = computer.StatusSleeping
	p.record("sleep:" + providerID)
	return nil
}

func (p *Provider) Destroy(_ context.Context, providerID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record("destroy:" + providerID)
	if p.Faults.DestroyErr != nil {
		return p.Faults.DestroyErr
	}
	if _, ok := p.instances[providerID]; !ok {
		return computer.ErrNotFound
	}
	delete(p.instances, providerID)
	return nil
}

func cloneFiles(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range maps.All(in) {
		out[k] = slices.Clone(v)
	}
	return out
}
