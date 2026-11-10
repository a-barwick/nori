package computerd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"nori/internal/computer/rpc"
)

const (
	maxSandboxStdout = 256 << 10
	maxSandboxStderr = 16 << 10
)

type boundedBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.Len(); room < len(p) {
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		b.truncated = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// redact keeps diagnostics useful without echoing secrets a program printed.
func redact(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lower := strings.ToLower(line)
		for _, word := range []string{"token", "secret", "password", "authorization", "api_key", "apikey"} {
			if strings.Contains(lower, word) {
				lines[i] = "[redacted]"
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// sandboxRun executes a packaged program in its own directory with cgroup memory, process and CPU limits, a wall
// clock deadline, a minimal environment and no network. It refuses to run when any limit cannot be applied.
func (s *server) sandboxRun(w http.ResponseWriter, r *http.Request) {
	var req rpc.SandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Argv) == 0 || req.TaskID == "" || req.RunID == "" {
		http.Error(w, "sandbox run needs a task and a program", 400)
		return
	}
	switch req.Network {
	case "none":
	case "allowlist":
		if err := validEgressHosts(req.AllowHosts); err != nil {
			http.Error(w, "invalid allowlist: "+err.Error(), 400)
			return
		}
		if !egressSupported() {
			http.Error(w, "this computer cannot enforce an egress allowlist, so the program did not run", 503)
			return
		}
	default:
		http.Error(w, "network must be none or allowlist", 400)
		return
	}
	if !sandboxSupported() {
		http.Error(w, "sandboxed execution needs Linux with delegated cgroup v2 and user namespaces", 503)
		return
	}
	root, err := os.MkdirTemp(s.tasks, "sandbox-")
	if err != nil {
		http.Error(w, "sandbox directory unavailable", 500)
		return
	}
	defer os.RemoveAll(root)
	for name, content := range req.Files {
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			http.Error(w, "package file names must stay inside the package", 400)
			return
		}
		path := filepath.Join(root, clean)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err == nil {
			err = os.WriteFile(path, []byte(content), 0700)
		}
		if err != nil {
			http.Error(w, "package files could not be written", 500)
			return
		}
	}
	group, err := newLimitedGroup(req.MemoryMB, req.Pids, req.CPUPct)
	if err != nil {
		http.Error(w, "sandbox limits could not be applied: "+err.Error(), 503)
		return
	}
	defer group.close()
	// The run's cancellation (Stop, a correction) ends the program like the deadline does.
	operation, finish, err := s.begin(r.Context(), req.RunID)
	if err != nil {
		http.Error(w, "run canceled", 409)
		return
	}
	defer finish()
	wall := time.Duration(max(min(req.WallMS, 300_000), 100)) * time.Millisecond
	ctx, cancel := context.WithTimeout(operation, wall)
	defer cancel()
	argv := req.Argv
	var proxy *egressProxy
	if req.Network == "allowlist" {
		// The proxy runs here, outside the namespace; the shim inside forwards to it and starts the program.
		socket := filepath.Join(root, egressSocketName)
		if proxy, err = startEgressProxy(socket, req.AllowHosts); err != nil {
			http.Error(w, "the egress proxy could not start, so the program did not run", 503)
			return
		}
		self, err := os.Executable()
		if err != nil {
			proxy.close()
			http.Error(w, "the egress shim is unavailable, so the program did not run", 503)
			return
		}
		argv = append([]string{self, "sandbox-shim", socket, "--"}, req.Argv...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + root, "LANG=C.UTF-8"}
	cmd.Stdin = strings.NewReader(req.Stdin)
	stdout, stderr := &boundedBuffer{limit: maxSandboxStdout}, &boundedBuffer{limit: maxSandboxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = sandboxAttributes(group, proxy != nil)
	cmd.Cancel = group.kill
	cmd.WaitDelay = time.Second
	started := time.Now()
	err = cmd.Run()
	result := rpc.SandboxResult{Stdout: stdout.String(), Stderr: redact(stderr.String()), WallMS: time.Since(started).Milliseconds(), OOMKilled: group.oomKilled()}
	if proxy != nil {
		result.Denied = proxy.close()
		// The shim refuses to start the program when it cannot establish the forwarder.
		if strings.Contains(stderr.String(), egressFailMarker) {
			http.Error(w, "the egress allowlist could not be enforced, so the program did not run: "+redact(stderr.String()), 503)
			return
		}
	}
	if stdout.truncated {
		result.Stderr += "\n[output truncated]"
	}
	var exit *exec.ExitError
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		result.TimedOut, result.ExitCode = true, -1
	case errors.As(err, &exit):
		result.ExitCode = exit.ExitCode()
	case err != nil:
		http.Error(w, "the program could not start: "+redact(err.Error()), 422)
		return
	}
	respond(w, result)
}
