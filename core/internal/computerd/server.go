// Package computerd implements the private computer RPC inside the retained guest.
package computerd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const maxOutput = 64 << 10
const maxScreenshot = 2 << 20

var runPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

type server struct {
	tasks   string
	browser chan struct{}
	runsMu  sync.Mutex
	runs    map[string]*runState
}
type runState struct {
	ctx     context.Context
	cancel  context.CancelFunc
	active  int
	idle    chan struct{}
	stopErr error
}

func New(tasks string) (http.Handler, error) {
	if err := os.MkdirAll(tasks, 0700); err != nil {
		return nil, err
	}
	s := &server{tasks: tasks, browser: make(chan struct{}, 1), runs: make(map[string]*runState)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		c, err := browser(ctx)
		if err != nil {
			http.Error(w, "browser not ready", http.StatusServiceUnavailable)
			return
		}
		// Readiness only validates the CDP page connection; it changes no page state.
		_ = c.conn.CloseNow()
		respond(w, map[string]bool{"ready": true})
	})
	mux.HandleFunc("POST /cancel", s.cancelRun)
	mux.HandleFunc("POST /exec", s.exec)
	mux.HandleFunc("POST /navigate", s.navigate)
	mux.HandleFunc("POST /screenshot", s.screenshot)
	return mux, nil
}

// Proxy only exposes the computer RPC routes; IAP and the host firewall authenticate callers.
func Proxy() http.Handler {
	u, _ := url.Parse("http://172.30.0.2:8090")
	p := httputil.NewSingleHostReverseProxy(u)
	p.Transport = &http.Transport{ResponseHeaderTimeout: 123 * time.Second}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !((r.Method == "GET" && r.URL.Path == "/healthz") || (r.Method == "POST" && (r.URL.Path == "/cancel" || r.URL.Path == "/exec" || r.URL.Path == "/navigate" || r.URL.Path == "/screenshot"))) {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		p.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		http.Error(w, "invalid JSON request", 400)
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		http.Error(w, "one JSON object required", 400)
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// cappedWriter continues accepting data after the cap so tools cannot block on output.
type cappedWriter struct {
	data      []byte
	truncated bool
	mu        sync.Mutex
}

func (b *cappedWriter) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := maxOutput - len(b.data)
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *cappedWriter) String() string {
	if b.truncated {
		return string(b.data) + "\n[output truncated]"
	}
	return string(b.data)
}

func (s *server) exec(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID   string `json:"run_id"`
		Command string `json:"command"`
		Timeout int    `json:"timeout_seconds"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !runPattern.MatchString(req.RunID) || req.Command == "" {
		http.Error(w, "run_id and command required", 400)
		return
	}
	if req.Timeout <= 0 || req.Timeout > 120 {
		req.Timeout = 120
	}
	operation, finish, err := s.begin(r.Context(), req.RunID)
	if err != nil {
		http.Error(w, "run canceled", 409)
		return
	}
	defer finish()
	ctx, cancel := context.WithTimeout(operation, time.Duration(req.Timeout)*time.Second)
	defer cancel()
	root, err := os.OpenRoot(s.tasks)
	if err != nil {
		http.Error(w, "task directory unavailable", 500)
		return
	}
	defer root.Close()
	if err = root.Mkdir(req.RunID, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		http.Error(w, "task directory unavailable", 500)
		return
	}
	dir, err := root.Open(req.RunID)
	if err != nil {
		http.Error(w, "task directory unavailable", 400)
		return
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		http.Error(w, "invalid task directory", 400)
		return
	}
	group, err := newOperationGroup()
	if err != nil {
		http.Error(w, "shell execution requires delegated cgroup v2", 503)
		return
	}
	defer group.close()
	cmd := exec.CommandContext(ctx, "/bin/bash", "-lc", req.Command)
	// Chdir resolves the already-open directory before exec closes descriptors.
	cmd.Dir = "/proc/self/fd/" + strconv.Itoa(int(dir.Fd()))
	// CLONE_INTO_CGROUP places the child before it can fork or execute user code.
	cmd.SysProcAttr = group.attributes()
	cmd.Cancel = group.kill
	cmd.WaitDelay = 2 * time.Second
	stdout, stderr := &cappedWriter{}, &cappedWriter{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
	stopErr := group.stop(cleanup)
	stop()
	if stopErr != nil {
		s.stopFailed(req.RunID, stopErr)
		http.Error(w, "shell descendants could not be confirmed stopped", 502)
		return
	}
	code := 0
	if err != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		if ctx.Err() != nil {
			code = 124
		}
	}
	result := map[string]any{"stdout": stdout.String(), "stderr": stderr.String(), "exit_code": code}
	if err != nil {
		result["error"] = err.Error()
	}
	respond(w, result)
}

// begin registers work before it can start. A canceled run cannot issue later tools.
func (s *server) begin(parent context.Context, id string) (context.Context, func(), error) {
	if !runPattern.MatchString(id) {
		return nil, nil, errors.New("invalid run id")
	}
	s.runsMu.Lock()
	state := s.runs[id]
	if state == nil {
		ctx, cancel := context.WithCancel(context.Background())
		idle := make(chan struct{})
		close(idle)
		state = &runState{ctx: ctx, cancel: cancel, idle: idle}
		s.runs[id] = state
	}
	if state.ctx.Err() != nil {
		s.runsMu.Unlock()
		return nil, nil, state.ctx.Err()
	}
	if state.active == 0 {
		state.idle = make(chan struct{})
	}
	state.active++
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(state.ctx, cancel)
	s.runsMu.Unlock()
	return ctx, func() {
		stop()
		cancel()
		s.runsMu.Lock()
		defer s.runsMu.Unlock()
		state.active--
		if state.active == 0 {
			close(state.idle)
		}
	}, nil
}
func (s *server) cancelRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !runPattern.MatchString(req.RunID) {
		http.Error(w, "invalid run_id", 400)
		return
	}
	s.runsMu.Lock()
	state := s.runs[req.RunID]
	if state == nil {
		ctx, cancel := context.WithCancel(context.Background())
		idle := make(chan struct{})
		close(idle)
		state = &runState{ctx: ctx, cancel: cancel, idle: idle}
		s.runs[req.RunID] = state
	}
	state.cancel()
	idle := state.idle
	s.runsMu.Unlock()
	select {
	case <-idle:
		s.runsMu.Lock()
		stopErr := state.stopErr
		s.runsMu.Unlock()
		if stopErr != nil {
			http.Error(w, "computer stop could not be confirmed", 502)
			return
		}
		respond(w, map[string]bool{"stopped": true})
	case <-r.Context().Done():
		http.Error(w, "stop acknowledgment timed out", 504)
	}
}
func (s *server) lockBrowser(ctx context.Context) bool {
	select {
	case s.browser <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
func (s *server) unlockBrowser() { <-s.browser }

func (s *server) stopFailed(id string, err error) {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	if state := s.runs[id]; state != nil {
		state.stopErr = err
	}
}
