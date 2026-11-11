package computerd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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

// preparedSandbox is a program ready to start in its sandbox: its directory, cgroup limits, optional egress proxy and
// command. cleanup releases everything.
type preparedSandbox struct {
	cmd     *exec.Cmd
	group   *operationGroup
	proxy   *egressProxy
	cleanup func()
}

// sandboxError is a refusal before the program starts; status 503 means it could not be constrained.
type sandboxError struct {
	status  int
	message string
}

func (e *sandboxError) Error() string { return e.message }

// prepareSandbox validates a request and builds the program's command in its own directory with cgroup memory,
// process and CPU limits, a minimal environment and either no network or a declared-host egress allowlist. It refuses
// when any constraint cannot be applied.
func (s *server) prepareSandbox(ctx context.Context, req rpc.SandboxRequest) (*preparedSandbox, error) {
	if len(req.Argv) == 0 || req.TaskID == "" || req.RunID == "" {
		return nil, &sandboxError{400, "sandbox run needs a task and a program"}
	}
	switch req.Network {
	case "none":
	case "allowlist":
		if err := validEgressHosts(req.AllowHosts); err != nil {
			return nil, &sandboxError{400, "invalid allowlist: " + err.Error()}
		}
		if !egressSupported() {
			return nil, &sandboxError{503, "this computer cannot enforce an egress allowlist, so the program did not run"}
		}
	default:
		return nil, &sandboxError{400, "network must be none or allowlist"}
	}
	if !sandboxSupported() {
		return nil, &sandboxError{503, "sandboxed execution needs Linux with delegated cgroup v2 and user namespaces"}
	}
	root, err := os.MkdirTemp(s.tasks, "sandbox-")
	if err != nil {
		return nil, &sandboxError{500, "sandbox directory unavailable"}
	}
	p := &preparedSandbox{}
	var closers []func()
	p.cleanup = func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
	closers = append(closers, func() { os.RemoveAll(root) })
	fail := func(e *sandboxError) (*preparedSandbox, error) { p.cleanup(); return nil, e }
	for name, content := range req.Files {
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return fail(&sandboxError{400, "package file names must stay inside the package"})
		}
		path := filepath.Join(root, clean)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err == nil {
			err = os.WriteFile(path, []byte(content), 0700)
		}
		if err != nil {
			return fail(&sandboxError{500, "package files could not be written"})
		}
	}
	group, err := newLimitedGroup(req.MemoryMB, req.Pids, req.CPUPct)
	if err != nil {
		return fail(&sandboxError{503, "sandbox limits could not be applied: " + err.Error()})
	}
	p.group = group
	closers = append(closers, group.close)
	argv := req.Argv
	if req.Network == "allowlist" {
		// The proxy runs here, outside the namespace; the shim inside forwards to it and starts the program.
		socket := filepath.Join(root, egressSocketName)
		if p.proxy, err = startEgressProxy(socket, req.AllowHosts); err != nil {
			return fail(&sandboxError{503, "the egress proxy could not start, so the program did not run"})
		}
		closers = append(closers, func() { p.proxy.close() })
		self, err := os.Executable()
		if err != nil {
			return fail(&sandboxError{503, "the egress shim is unavailable, so the program did not run"})
		}
		argv = append([]string{self, "sandbox-shim", socket, "--"}, req.Argv...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + root, "TMPDIR=" + root, "LANG=C.UTF-8"}
	cmd.SysProcAttr = sandboxAttributes(group, p.proxy != nil)
	cmd.Cancel = group.kill
	cmd.WaitDelay = time.Second
	p.cmd = cmd
	return p, nil
}

func writeSandboxError(w http.ResponseWriter, err error) {
	var se *sandboxError
	if errors.As(err, &se) {
		http.Error(w, se.message, se.status)
		return
	}
	http.Error(w, err.Error(), 500)
}

// sandboxRun runs a packaged program once with a wall clock deadline and returns its output.
func (s *server) sandboxRun(w http.ResponseWriter, r *http.Request) {
	var req rpc.SandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "sandbox run needs a task and a program", 400)
		return
	}
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
	p, err := s.prepareSandbox(ctx, req)
	if err != nil {
		writeSandboxError(w, err)
		return
	}
	defer p.cleanup()
	cmd := p.cmd
	cmd.Stdin = strings.NewReader(req.Stdin)
	stdout, stderr := &boundedBuffer{limit: maxSandboxStdout}, &boundedBuffer{limit: maxSandboxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	started := time.Now()
	err = cmd.Run()
	result := rpc.SandboxResult{Stdout: stdout.String(), Stderr: redact(stderr.String()), WallMS: time.Since(started).Milliseconds(), OOMKilled: p.group.oomKilled()}
	if p.proxy != nil {
		result.Denied = p.proxy.close()
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

// sandboxSession runs a packaged program as a long-lived process, such as a stdio MCP server, and connects its stdin
// and stdout to the caller over an upgraded HTTP connection. The same limits and network rules apply as for a run.
// The session ends when either side closes, the run is cancelled or the wall clock limit passes.
func (s *server) sandboxSession(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), rpc.SandboxUpgrade) {
		http.Error(w, "a sandbox session needs Upgrade: "+rpc.SandboxUpgrade, 400)
		return
	}
	var req rpc.SandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "sandbox session needs a task and a program", 400)
		return
	}
	operation, finish, err := s.begin(context.WithoutCancel(r.Context()), req.RunID)
	if err != nil {
		http.Error(w, "run canceled", 409)
		return
	}
	defer finish()
	wall := time.Duration(max(min(req.WallMS, 600_000), 1000)) * time.Millisecond
	ctx, cancel := context.WithTimeout(operation, wall)
	defer cancel()
	p, err := s.prepareSandbox(ctx, req)
	if err != nil {
		writeSandboxError(w, err)
		return
	}
	defer p.cleanup()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "this server cannot stream a session", 500)
		return
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(buffered, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: %s\r\n\r\n", rpc.SandboxUpgrade)
	if err := buffered.Flush(); err != nil {
		return
	}
	stderr := &boundedBuffer{limit: maxSandboxStderr}
	p.cmd.Stdin, p.cmd.Stdout, p.cmd.Stderr = buffered, conn, stderr
	if err := p.cmd.Start(); err != nil {
		return
	}
	// A closed connection ends the program; a finished program closes the connection.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	_ = p.cmd.Wait()
	if p.proxy != nil {
		if denied := p.proxy.close(); len(denied) > 0 {
			log.Printf("sandbox session %s: egress denied for %v", req.RunID, denied)
		}
	}
}
