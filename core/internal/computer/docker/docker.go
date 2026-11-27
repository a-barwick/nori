package docker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/computer"
	"nori/internal/computer/vmscripts"
)

const DefaultImage = "nori-computer:local"

var validCheckpointIDRe = regexp.MustCompile(`^ckpt_[a-f0-9]{12}$`)

// CmdRunner executes a host docker command. Injectable for unit tests.
type CmdRunner func(ctx context.Context, stdin []byte, args ...string) (stdout string, stderr string, exitCode int, err error)

// Config configures the local Docker computer provider (dev & localhost VSTs).
type Config struct {
	Image         string
	Binary        string // default "docker"
	LocalhostOnly bool   // if true, attaches container to --network=none for hermetic VSTs
	Network       string // optional custom Docker bridge network
	MemoryLimitMB int
	Runner        CmdRunner
}

// Provider implements computer.Provider using one labeled Docker container and
// persistent named Docker volume (/home/nori) per user.
type Provider struct {
	cfg    Config
	runner CmdRunner
}

// New creates a Docker provider adapter.
func New(cfg Config) *Provider {
	if strings.TrimSpace(cfg.Image) == "" {
		cfg.Image = DefaultImage
	}
	if strings.TrimSpace(cfg.Binary) == "" {
		cfg.Binary = "docker"
	}
	if cfg.MemoryLimitMB <= 0 {
		cfg.MemoryLimitMB = computer.DefaultMemoryLimitMB
	}
	runner := cfg.Runner
	if runner == nil {
		bin := cfg.Binary
		runner = func(ctx context.Context, stdin []byte, args ...string) (string, string, int, error) {
			cmd := exec.CommandContext(ctx, bin, args...)
			if len(stdin) > 0 {
				cmd.Stdin = bytes.NewReader(stdin)
			}
			// Bounded host-side output buffers prevent noisy guest processes from OOMing Nori Core.
			// Allow up to 16 MiB for base64 ReadFile payloads (10 MiB binary ~= 13.4 MiB base64).
			const maxBuf = 16 << 20
			outBuf := computer.NewLimitedBuffer(maxBuf)
			errBuf := computer.NewLimitedBuffer(computer.MaxExecOutputBytes + 4096)
			cmd.Stdout = outBuf
			cmd.Stderr = errBuf
			err := cmd.Run()
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return outBuf.String(), errBuf.String(), exitErr.ExitCode(), nil
				}
				return outBuf.String(), errBuf.String(), 1, err
			}
			return outBuf.String(), errBuf.String(), 0, nil
		}
	}
	return &Provider{cfg: cfg, runner: runner}
}

func (p *Provider) Name() string { return "docker" }

func containerName(computerID string) string {
	clean := strings.ToLower(strings.TrimSpace(computerID))
	clean = strings.ReplaceAll(clean, "_", "-")
	if clean == "" {
		clean = strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	}
	if strings.HasPrefix(clean, "nori-computer-") {
		return clean
	}
	return "nori-computer-" + clean
}

func volumeName(providerID string) string {
	return strings.Replace(providerID, "nori-computer-", "nori-home-", 1)
}

func (p *Provider) Ensure(ctx context.Context, spec computer.Spec) (computer.Instance, error) {
	name := containerName(spec.ComputerID)
	vol := volumeName(name)

	stdout, _, code, err := p.runner(ctx, nil, "inspect", "-f", "{{.State.Status}}", name)
	if err != nil {
		return computer.Instance{}, fmt.Errorf("docker inspect %s: %w", name, err)
	}
	created := false
	if code != 0 {
		memMB := spec.MemoryLimitMB
		if memMB <= 0 {
			memMB = p.cfg.MemoryLimitMB
		}
		args := []string{
			"run", "-d",
			"--name", name,
			"--label", "nori.managed=true",
			"--label", "nori.computer_id=" + spec.ComputerID,
			"--label", "nori.user_id=" + spec.UserID,
			"--volume", vol + ":/home/nori",
			"--publish", fmt.Sprintf("127.0.0.1::%d", computer.DefaultDesktopPort),
			"--memory", fmt.Sprintf("%dm", memMB),
			"--pids-limit", "512",
			"--security-opt", "no-new-privileges:true",
			// Block cloud metadata SSRF hosts at container /etc/hosts level.
			"--add-host", "metadata.google.internal:127.0.0.1",
			"--add-host", "metadata:127.0.0.1",
			"-e", fmt.Sprintf("NORI_DISPLAY_WIDTH=%d", max(spec.DisplayWidth, computer.DefaultDisplayWidth)),
			"-e", fmt.Sprintf("NORI_DISPLAY_HEIGHT=%d", max(spec.DisplayHeight, computer.DefaultDisplayHeight)),
			"-e", fmt.Sprintf("NORI_COMPUTER_MEMORY_WATCHDOG_MB=%d", memMB),
		}
		if p.cfg.LocalhostOnly {
			args = append(args, "--network", "none")
		} else if p.cfg.Network != "" {
			args = append(args, "--network", p.cfg.Network)
		}
		args = append(args, p.cfg.Image, "sleep", "infinity")

		_, stderr, rc, rErr := p.runner(ctx, nil, args...)
		if rErr != nil {
			return computer.Instance{}, fmt.Errorf("docker run %s: %w", name, rErr)
		}
		if rc != 0 {
			return computer.Instance{}, fmt.Errorf("docker run %s failed (exit %d): %s", name, rc, strings.TrimSpace(stderr))
		}
		created = true
	} else {
		state := strings.TrimSpace(stdout)
		if state != "running" {
			_, stderr, rc, rErr := p.runner(ctx, nil, "start", name)
			if rErr != nil {
				return computer.Instance{}, fmt.Errorf("docker start %s: %w", name, rErr)
			}
			if rc != 0 {
				return computer.Instance{}, fmt.Errorf("docker start %s failed: %s", name, strings.TrimSpace(stderr))
			}
		}
	}

	if err := p.ensureScripts(ctx, name); err != nil {
		return computer.Instance{}, err
	}

	baselineID := ""
	if created {
		if ckpt, cErr := p.Checkpoint(ctx, name, "baseline"); cErr == nil {
			baselineID = ckpt
		}
	}

	return computer.Instance{
		ProviderID:           name,
		Status:               computer.StatusReady,
		BaselineCheckpointID: baselineID,
		BootstrapVersion:     vmscripts.Version,
	}, nil
}

func (p *Provider) ensureScripts(ctx context.Context, providerID string) error {
	scripts := []struct {
		path string
		body string
	}{
		{"/home/nori/.nori/bin/nori-desktop-start", vmscripts.DesktopStartSH},
		{"/home/nori/.nori/bin/nori-browser", vmscripts.BrowserJS},
		{"/home/nori/.nori/bin/nori-watchdog", vmscripts.WatchdogSH},
	}
	for _, s := range scripts {
		if err := p.WriteFile(ctx, providerID, s.path, []byte(s.body), 0o755); err != nil {
			return fmt.Errorf("install docker script %s: %w", s.path, err)
		}
	}
	_, _, _, err := p.runner(ctx, nil, "exec", providerID, "mkdir", "-p", "/home/nori/workspace", "/home/nori/workspace/uploads", "/home/nori/.config/chromium", "/home/nori/.nori/checkpoints")
	return err
}

func (p *Provider) Status(ctx context.Context, providerID string) (computer.InstanceStatus, error) {
	stdout, _, code, err := p.runner(ctx, nil, "inspect", "-f", "{{.State.Status}}", providerID)
	if err != nil {
		return computer.InstanceStatus{}, err
	}
	if code != 0 {
		return computer.InstanceStatus{}, computer.ErrNotFound
	}
	status := computer.StatusSleeping
	if strings.TrimSpace(stdout) == "running" {
		status = computer.StatusReady
	}
	out := computer.InstanceStatus{
		Status:       status,
		DiskMaxBytes: int64(computer.DefaultMaxDiskGB) << 30,
	}
	if status == computer.StatusReady {
		if duOut, _, duCode, _ := p.runner(ctx, nil, "exec", providerID, "du", "-sb", "/home/nori"); duCode == 0 {
			fields := strings.Fields(duOut)
			if len(fields) > 0 {
				if n, pErr := strconv.ParseInt(fields[0], 10, 64); pErr == nil {
					out.DiskUsedBytes = n
				}
			}
		}
		if ckOut, _, ckCode, _ := p.runner(ctx, nil, "exec", providerID, "bash", "-lc", "ls -1 /home/nori/.nori/checkpoints/*.meta 2>/dev/null || true"); ckCode == 0 {
			for _, line := range strings.Split(strings.TrimSpace(ckOut), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if raw, rErr := p.ReadFile(ctx, providerID, line, 4096); rErr == nil {
					var info computer.CheckpointInfo
					if json.Unmarshal(raw, &info) == nil && info.ID != "" {
						out.Checkpoints = append(out.Checkpoints, info)
					}
				}
			}
		}
	}
	return out, nil
}

func (p *Provider) Exec(ctx context.Context, providerID string, req computer.ExecRequest) (computer.ExecResult, error) {
	timeout := req.Timeout
	if timeout <= 0 || timeout > computer.MaxExecTimeout {
		timeout = computer.DefaultExecTimeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	const noriPathPrefix = `export NPM_CONFIG_PREFIX="/home/nori/.local/npm" PATH="/home/nori/.nori/bin:/home/nori/.local/npm/bin:/home/nori/.local/bin:/home/nori/.local/venv/bin:$PATH"; `
	command := noriPathPrefix + req.Command
	jobID := ""
	if req.Background {
		jobID = "job_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
		logPath := fmt.Sprintf("/home/nori/.nori/logs/%s.log", jobID)
		pidPath := fmt.Sprintf("/home/nori/.nori/logs/%s.pid", jobID)
		exitPath := fmt.Sprintf("/home/nori/.nori/logs/%s.exit", jobID)
		wrapped := fmt.Sprintf(`(%s); echo $? > %s`, noriPathPrefix+req.Command, shellQuote(exitPath))
		command = fmt.Sprintf(`%smkdir -p /home/nori/.nori/logs && nohup bash -lc %s >%s 2>&1 </dev/null & pid=$!; echo "$pid" > %s; echo "$pid"`,
			noriPathPrefix, shellQuote(wrapped), shellQuote(logPath), shellQuote(pidPath))
	}

	args := []string{"exec"}
	if req.WorkDir != "" {
		args = append(args, "-w", req.WorkDir)
	}
	for k, v := range req.Env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, providerID, "bash", "-lc", command)

	started := time.Now()
	stdout, stderr, code, err := p.runner(execCtx, nil, args...)
	elapsed := time.Since(started)
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return computer.ExecResult{ExitCode: 124, Stdout: stdout, Stderr: stderr, DurationMS: elapsed.Milliseconds(), JobID: jobID}, computer.ErrTimeout
		}
		return computer.ExecResult{ExitCode: 1, Stdout: stdout, Stderr: stderr, DurationMS: elapsed.Milliseconds(), JobID: jobID}, err
	}
	return computer.ExecResult{
		ExitCode:   code,
		Stdout:     stdout,
		Stderr:     stderr,
		DurationMS: elapsed.Milliseconds(),
		JobID:      jobID,
		CostUSD:    0, // local Docker has $0 cloud compute cost
	}, nil
}

// Symlink-safe ReadFile: verifies realpath stays strictly inside /home/nori before reading.
func (p *Provider) ReadFile(ctx context.Context, providerID, filePath string, maxBytes int64) ([]byte, error) {
	py := `import base64, os, sys
p, max_b = sys.argv[1], int(sys.argv[2])
home = os.path.realpath('/home/nori')
if not os.path.exists(p):
    sys.exit(44)
rp = os.path.realpath(p)
if rp != home and not rp.startswith(home + os.sep):
    sys.exit(46)
if not os.path.isfile(rp):
    sys.exit(44)
if os.path.getsize(rp) > max_b:
    sys.exit(45)
with open(rp, 'rb') as f:
    sys.stdout.write(base64.b64encode(f.read()).decode('ascii'))
`
	stdout, stderr, code, err := p.runner(ctx, nil, "exec", providerID, "python3", "-c", py, filePath, strconv.FormatInt(maxBytes, 10))
	if err != nil {
		return nil, err
	}
	switch code {
	case 0:
		cleaned := strings.ReplaceAll(strings.ReplaceAll(stdout, "\n", ""), "\r", "")
		return base64.StdEncoding.DecodeString(cleaned)
	case 44:
		return nil, computer.ErrNotFound
	case 45:
		return nil, computer.ErrFileTooLarge
	case 46:
		return nil, computer.ErrPathTraversal
	default:
		return nil, fmt.Errorf("read %s failed (exit %d): %s", filePath, code, strings.TrimSpace(stderr))
	}
}

// Symlink-safe WriteFile: verifies parent realpath and target (if symlink) stay inside /home/nori.
func (p *Provider) WriteFile(ctx context.Context, providerID, filePath string, data []byte, mode uint32) error {
	if mode == 0 {
		mode = 0o644
	}
	py := `import os, sys
p, mode = sys.argv[1], int(sys.argv[2], 8)
home = os.path.realpath('/home/nori')
parent = os.path.dirname(p)
os.makedirs(parent, exist_ok=True)
rp_parent = os.path.realpath(parent)
if rp_parent != home and not rp_parent.startswith(home + os.sep):
    sys.exit(46)
if os.path.islink(p):
    rp = os.path.realpath(p)
    if rp != home and not rp.startswith(home + os.sep):
        sys.exit(46)
target = os.path.join(rp_parent, os.path.basename(p))
with open(target, 'wb') as f:
    f.write(sys.stdin.buffer.read())
os.chmod(target, mode)
`
	_, stderr, code, err := p.runner(ctx, data, "exec", "-i", providerID, "python3", "-c", py, filePath, fmt.Sprintf("%o", mode&0o777))
	if err != nil {
		return err
	}
	if code == 46 {
		return computer.ErrPathTraversal
	}
	if code != 0 {
		return fmt.Errorf("write %s failed (exit %d): %s", filePath, code, strings.TrimSpace(stderr))
	}
	return nil
}

func (p *Provider) ListFiles(ctx context.Context, providerID, dir string) ([]computer.FileEntry, error) {
	py := `import json, os, stat, sys, datetime
d = sys.argv[1]
home = os.path.realpath('/home/nori')
if not os.path.isdir(d):
    sys.exit(44)
rd = os.path.realpath(d)
if rd != home and not rd.startswith(home + os.sep):
    sys.exit(46)
out = []
for name in sorted(os.listdir(rd)):
    full = os.path.join(rd, name)
    try:
        st = os.lstat(full)
    except OSError:
        continue
    out.append({
        "name": name,
        "path": full,
        "is_dir": stat.S_ISDIR(st.st_mode),
        "size": st.st_size,
        "mode": stat.filemode(st.st_mode),
        "mod_time": datetime.datetime.fromtimestamp(st.st_mtime, datetime.timezone.utc).isoformat(),
    })
print(json.dumps(out))
`
	stdout, stderr, code, err := p.runner(ctx, nil, "exec", providerID, "python3", "-c", py, dir)
	if err != nil {
		return nil, err
	}
	switch code {
	case 44:
		return nil, computer.ErrNotFound
	case 46:
		return nil, computer.ErrPathTraversal
	}
	if code != 0 {
		return nil, fmt.Errorf("list %s failed (exit %d): %s", dir, code, strings.TrimSpace(stderr))
	}
	var entries []computer.FileEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (p *Provider) DeletePath(ctx context.Context, providerID, targetPath string) error {
	py := `import os, shutil, sys
p = sys.argv[1]
home = os.path.realpath('/home/nori')
if not os.path.lexists(p):
    sys.exit(44)
parent_rp = os.path.realpath(os.path.dirname(p))
if parent_rp != home and not parent_rp.startswith(home + os.sep):
    sys.exit(46)
target = os.path.join(parent_rp, os.path.basename(p))
if target == home or target == os.path.join(home, 'workspace'):
    sys.exit(46)
if os.path.islink(target) or os.path.isfile(target):
    os.unlink(target)
else:
    shutil.rmtree(target)
`
	_, stderr, code, err := p.runner(ctx, nil, "exec", providerID, "python3", "-c", py, targetPath)
	if err != nil {
		return err
	}
	switch code {
	case 44:
		return computer.ErrNotFound
	case 46:
		return computer.ErrPathTraversal
	}
	if code != 0 {
		return fmt.Errorf("delete %s failed (exit %d): %s", targetPath, code, strings.TrimSpace(stderr))
	}
	return nil
}

func (p *Provider) DesktopEndpoint(ctx context.Context, providerID string) (computer.DesktopTarget, error) {
	stdout, stderr, code, err := p.runner(ctx, nil, "port", providerID, fmt.Sprintf("%d/tcp", computer.DefaultDesktopPort))
	if err != nil {
		return computer.DesktopTarget{}, err
	}
	if code != 0 {
		return computer.DesktopTarget{}, fmt.Errorf("docker port %s failed: %s", providerID, strings.TrimSpace(stderr))
	}
	line := strings.TrimSpace(strings.Split(stdout, "\n")[0])
	if line == "" {
		return computer.DesktopTarget{}, fmt.Errorf("docker container %s has no exposed desktop port", providerID)
	}
	headers := make(http.Header)
	if rawTok, tErr := p.ReadFile(ctx, providerID, "/home/nori/.nori/desktop.token", 256); tErr == nil {
		if tok := strings.TrimSpace(string(rawTok)); tok != "" {
			headers.Set("X-Nori-Desktop-Token", tok)
		}
	}
	return computer.DesktopTarget{
		WSURL:   "ws://" + line + "/websockify",
		Headers: headers,
	}, nil
}

func (p *Provider) SetNetworkPolicy(ctx context.Context, providerID string, policy computer.NetworkPolicy) error {
	raw, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	return p.WriteFile(ctx, providerID, "/home/nori/.nori/network-policy.json", raw, 0o644)
}

func (p *Provider) Checkpoint(ctx context.Context, providerID, label string) (string, error) {
	id := "ckpt_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	tarPath := fmt.Sprintf("/home/nori/.nori/checkpoints/%s.tar.gz", id)
	metaPath := fmt.Sprintf("/home/nori/.nori/checkpoints/%s.meta", id)
	script := fmt.Sprintf("mkdir -p /home/nori/.nori/checkpoints && tar --exclude='./.nori/checkpoints' -czf %s -C /home/nori .", shellQuote(tarPath))
	_, stderr, code, err := p.runner(ctx, nil, "exec", providerID, "bash", "-lc", script)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("checkpoint failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	meta, _ := json.Marshal(computer.CheckpointInfo{ID: id, Label: label, CreatedAt: time.Now().UTC()})
	if err := p.WriteFile(ctx, providerID, metaPath, meta, 0o644); err != nil {
		return "", err
	}
	return id, nil
}

// Restore validates the checkpoint ID against trusted metadata, verifies and stages the archive
// into a temporary directory BEFORE modifying live /home/nori contents so corrupt archives never cause data loss.
func (p *Provider) Restore(ctx context.Context, providerID, checkpointID string) error {
	if !validCheckpointIDRe.MatchString(checkpointID) {
		return fmt.Errorf("%w: invalid checkpoint ID %q", computer.ErrNotFound, checkpointID)
	}
	tarPath := fmt.Sprintf("/home/nori/.nori/checkpoints/%s.tar.gz", checkpointID)
	metaPath := fmt.Sprintf("/home/nori/.nori/checkpoints/%s.meta", checkpointID)
	stageDir := fmt.Sprintf("/home/nori/.nori/restore-stage-%s", checkpointID)
	script := fmt.Sprintf(`set -euo pipefail
if [ ! -f %s ] || [ ! -f %s ]; then exit 44; fi
# Validate archive integrity and reject absolute or parent-escaping entries before touching live files.
if tar -tzf %s | grep -Eq '(^/|^\.\./|/\.\./|/\.\.$)'; then exit 46; fi
rm -rf %s
mkdir -p %s
tar -xzf %s -C %s
# Only after staged extraction succeeds, replace live home contents (preserving .nori).
find /home/nori -mindepth 1 -maxdepth 1 ! -name '.nori' -exec rm -rf {} +
shopt -s dotglob nullglob
for entry in %s/*; do
  base=$(basename "$entry")
  if [ "$base" = ".nori" ]; then continue; fi
  mv "$entry" /home/nori/
done
rm -rf %s`,
		shellQuote(tarPath), shellQuote(metaPath),
		shellQuote(tarPath),
		shellQuote(stageDir), shellQuote(stageDir),
		shellQuote(tarPath), shellQuote(stageDir),
		shellQuote(stageDir),
		shellQuote(stageDir),
	)
	_, stderr, code, err := p.runner(ctx, nil, "exec", providerID, "bash", "-lc", script)
	if err != nil {
		return err
	}
	switch code {
	case 44:
		return computer.ErrNotFound
	case 46:
		return computer.ErrPathTraversal
	}
	if code != 0 {
		return fmt.Errorf("restore %s failed (exit %d): %s", checkpointID, code, strings.TrimSpace(stderr))
	}
	return nil
}

func (p *Provider) Sleep(ctx context.Context, providerID string) error {
	// Gracefully close Chromium via CDP so NetworkService commits cookies/sessions to SQLite before stopping.
	flushCmd := `export PATH="/home/nori/.nori/bin:/home/nori/.local/bin:/home/nori/.local/venv/bin:$PATH"
nori-browser close >/dev/null 2>&1 || true
if [ -f /home/nori/.nori/logs/chromium.pid ]; then
  pid=$(cat /home/nori/.nori/logs/chromium.pid)
  for _ in $(seq 1 30); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
fi`
	_, _, _, _ = p.runner(ctx, nil, "exec", providerID, "bash", "-lc", flushCmd)
	_, stderr, code, err := p.runner(ctx, nil, "stop", "-t", "5", providerID)
	if err != nil {
		return err
	}
	if code != 0 {
		if strings.Contains(strings.ToLower(stderr), "no such container") {
			return computer.ErrNotFound
		}
		return fmt.Errorf("docker stop %s failed: %s", providerID, strings.TrimSpace(stderr))
	}
	return nil
}

func (p *Provider) Destroy(ctx context.Context, providerID string) error {
	_, stderr, code, err := p.runner(ctx, nil, "rm", "-f", providerID)
	if err != nil {
		return err
	}
	if code != 0 && !strings.Contains(strings.ToLower(stderr), "no such container") {
		return fmt.Errorf("docker rm %s failed: %s", providerID, strings.TrimSpace(stderr))
	}
	vol := volumeName(providerID)
	_, volStderr, volCode, volErr := p.runner(ctx, nil, "volume", "rm", "-f", vol)
	if volErr != nil {
		return fmt.Errorf("docker volume rm %s: %w", vol, volErr)
	}
	if volCode != 0 && !strings.Contains(strings.ToLower(volStderr), "no such volume") {
		return fmt.Errorf("docker volume rm %s failed: %s", vol, strings.TrimSpace(volStderr))
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var _ = path.Base
