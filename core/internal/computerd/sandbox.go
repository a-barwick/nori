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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Argv) == 0 || req.TaskID == "" {
		http.Error(w, "sandbox run needs a task and a program", 400)
		return
	}
	if req.Network != "none" {
		http.Error(w, "only network-free sandboxes are supported", 400)
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
	wall := time.Duration(max(min(req.WallMS, 300_000), 100)) * time.Millisecond
	ctx, cancel := context.WithTimeout(r.Context(), wall)
	defer cancel()
	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + root, "LANG=C.UTF-8"}
	cmd.Stdin = strings.NewReader(req.Stdin)
	stdout, stderr := &boundedBuffer{limit: maxSandboxStdout}, &boundedBuffer{limit: maxSandboxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.SysProcAttr = sandboxAttributes(group)
	cmd.Cancel = group.kill
	cmd.WaitDelay = time.Second
	started := time.Now()
	err = cmd.Run()
	result := rpc.SandboxResult{Stdout: stdout.String(), Stderr: redact(stderr.String()), WallMS: time.Since(started).Milliseconds(), OOMKilled: group.oomKilled()}
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
