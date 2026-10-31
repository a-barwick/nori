// Package computerd implements the private computer RPC inside the retained guest.
package computerd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"nori/internal/httpjson"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"

	"nori/internal/observability"
)

const maxOutput = 64 << 10
const maxScreenshot = 2 << 20

var runPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

type server struct {
	tasks          string
	browserMu      sync.Mutex
	browsers       map[string]*taskBrowser
	screenshotGate chan struct{}
	runsMu         sync.Mutex
	runs           map[string]*runState
	receiptMu      sync.Mutex
	activeReceipts map[string]bool
	readOperations map[string]*readOperation
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
	s := &server{tasks: tasks, browsers: make(map[string]*taskBrowser), screenshotGate: make(chan struct{}, 1), runs: make(map[string]*runState), activeReceipts: make(map[string]bool), readOperations: make(map[string]*readOperation)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		c, err := browser(ctx)
		if err != nil {
			http.Error(w, "browser not ready", http.StatusServiceUnavailable)
			return
		}
		// Readiness validates the browser CDP connection without creating a task tab.
		_ = c.conn.CloseNow()
		respond(w, map[string]bool{"ready": true, "operation_receipts_v1": true, "browser_task_tabs_v1": true})
	})
	mux.HandleFunc("POST /cancel", s.cancelRun)
	mux.HandleFunc("POST /browser/close", s.closeTaskBrowser)
	mux.HandleFunc("POST /exec", s.receipted(s.exec))
	mux.HandleFunc("POST /navigate", s.receipted(s.navigate))
	mux.HandleFunc("POST /screenshot", s.receipted(s.screenshot))
	mux.HandleFunc("POST /operation", s.operationStatus)
	mux.HandleFunc("POST /operation/cancel", s.cancelRead)
	activity := &activityState{last: time.Now()}
	// This endpoint is host-only: Proxy deliberately does not forward it.
	mux.HandleFunc("GET /activity", func(w http.ResponseWriter, r *http.Request) {
		activity.mu.Lock()
		defer activity.mu.Unlock()
		respond(w, map[string]any{"active": activity.active, "idle_seconds": time.Since(activity.last).Seconds()})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			mux.ServeHTTP(w, r)
			return
		}
		activity.mu.Lock()
		activity.active++
		activity.mu.Unlock()
		defer func() {
			activity.mu.Lock()
			activity.active--
			activity.last = time.Now()
			activity.mu.Unlock()
		}()
		// Count until the complete handler returns, including guest cleanup and
		// writing the operation receipt after a caller disconnects.
		mux.ServeHTTP(w, r)
	}), nil
}

type activityState struct {
	mu      sync.Mutex
	active  int
	last    time.Time
	closing bool
}

// Proxy only exposes the computer RPC routes; IAP and the host firewall authenticate callers.
// The host owns idle shutdown so no cloud credentials or host privileges enter the guest.
func Proxy(ctx context.Context, guestURL *url.URL, idle time.Duration, onIdle func()) http.Handler {
	u := guestURL
	p := httputil.NewSingleHostReverseProxy(u)
	director := p.Director
	p.Director = func(r *http.Request) {
		director(r)
		for name, value := range observability.InjectTrace(r.Context()) {
			r.Header.Set(name, value)
		}
	}
	p.Transport = &http.Transport{ResponseHeaderTimeout: 123 * time.Second}
	p.ModifyResponse = func(response *http.Response) error {
		// This admission signal belongs to the trusted host, never the guest.
		response.Header.Del("Nori-Computer-Draining")
		return nil
	}
	activity := &activityState{last: time.Now()}
	go func() {
		interval := min(30*time.Second, idle/4)
		if interval <= 0 {
			return
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		client := &http.Client{Timeout: 3 * time.Second}
		warned := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			activity.mu.Lock()
			if activity.active != 0 || time.Since(activity.last) < idle {
				activity.mu.Unlock()
				continue
			}
			// Keep admission locked through the guest check. A request either
			// registers before this gate or is rejected before guest effects.
			check, cancel := context.WithTimeout(ctx, 3*time.Second)
			req, _ := http.NewRequestWithContext(check, http.MethodGet, u.String()+"/activity", nil)
			resp, err := client.Do(req)
			var guest struct {
				Active      *int     `json:"active"`
				IdleSeconds *float64 `json:"idle_seconds"`
			}
			if err == nil {
				err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&guest)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					err = errors.New("guest activity unavailable")
				}
			}
			cancel()
			verified := err == nil && guest.Active != nil && guest.IdleSeconds != nil
			if !verified && !warned {
				log.Print("computer idle check unavailable; guest activity is unknown, host remains on; explicit pause/reconciliation is required if the guest does not recover")
				warned = true
			} else if verified {
				warned = false
			}
			safe := verified && *guest.Active == 0 && *guest.IdleSeconds >= idle.Seconds()
			if safe {
				activity.closing = true
			}
			activity.mu.Unlock()
			if safe {
				onIdle()
				return
			}
		}
	}()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !((r.Method == "GET" && r.URL.Path == "/healthz") || (r.Method == "POST" && (r.URL.Path == "/cancel" || r.URL.Path == "/browser/close" || r.URL.Path == "/exec" || r.URL.Path == "/navigate" || r.URL.Path == "/screenshot" || r.URL.Path == "/operation" || r.URL.Path == "/operation/cancel"))) {
			http.NotFound(w, r)
			return
		}
		activity.mu.Lock()
		if activity.closing {
			activity.mu.Unlock()
			w.Header().Set("Nori-Computer-Draining", "true")
			http.Error(w, "computer stopping after idle", http.StatusServiceUnavailable)
			return
		}
		tool := r.Method == http.MethodPost
		if tool {
			activity.active++
		}
		activity.mu.Unlock()
		defer func() {
			if tool {
				activity.mu.Lock()
				activity.active--
				activity.last = time.Now()
				activity.mu.Unlock()
			}
		}()
		r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
		p.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := httpjson.Decode(w, r, value, 128<<10); err != nil {
		message := "invalid JSON request"
		if errors.Is(err, httpjson.ErrMultiple) {
			message = "one JSON object required"
		}
		http.Error(w, message, 400)
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any) {
	httpjson.Write(w, http.StatusOK, value)
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
		TaskID  string `json:"task_id"`
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
	if req.TaskID == "" {
		req.TaskID = req.RunID
	}
	if !runPattern.MatchString(req.TaskID) {
		http.Error(w, "invalid task_id", 400)
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
	if err = root.Mkdir(req.TaskID, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		http.Error(w, "task directory unavailable", 500)
		return
	}
	dir, err := root.Open(req.TaskID)
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
	if s.wasCancelled(id) {
		return nil, nil, errors.New("run durably cancelled")
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
		RunID  string `json:"run_id"`
		TaskID string `json:"task_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !runPattern.MatchString(req.RunID) || (req.TaskID != "" && !runPattern.MatchString(req.TaskID)) {
		http.Error(w, "invalid run_id or task_id", 400)
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
		if err := s.persistCancelled(req.RunID); err != nil {
			http.Error(w, "cancellation receipt unavailable", 502)
			return
		}
		if s.uncertainReceipts(req.RunID) {
			http.Error(w, "prior operation stop is uncertain after guest agent restart", 502)
			return
		}
		respond(w, map[string]bool{"stopped": true})
	case <-r.Context().Done():
		http.Error(w, "stop acknowledgment timed out", 504)
	}
}

func (s *server) stopFailed(id string, err error) {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	if state := s.runs[id]; state != nil {
		state.stopErr = err
	}
}
