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

	"nori/internal/computer/rpc"
	"nori/internal/observability"
)

const maxOutput = 64 << 10
const maxRPCRequest = 128 << 10

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
	jobsMu         sync.Mutex
	jobs           map[string]*activeJob
	unsavedJobs    map[string]jobRecord
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
	s := &server{tasks: tasks, browsers: make(map[string]*taskBrowser), screenshotGate: make(chan struct{}, 1), runs: make(map[string]*runState), activeReceipts: make(map[string]bool), readOperations: make(map[string]*readOperation), jobs: make(map[string]*activeJob)}
	s.recoverJobs()
	mux := http.NewServeMux()
	for pattern, route := range guestRoutes {
		handle := func(w http.ResponseWriter, r *http.Request) { route.serve(s, w, r) }
		if route.receipt != noReceipt {
			handle = s.receipted(handle, route.receipt == interruptibleReceipt)
		}
		mux.HandleFunc(pattern, handle)
	}
	activity := &activityState{last: time.Now()}
	mux.HandleFunc("GET /activity", func(w http.ResponseWriter, r *http.Request) {
		activity.mu.Lock()
		defer activity.mu.Unlock()
		respond(w, map[string]any{"active": activity.active + s.runningJobs(), "idle_seconds": time.Since(activity.last).Seconds()})
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

type receiptMode int

const (
	noReceipt receiptMode = iota
	durableReceipt
	interruptibleReceipt
)

type guestRoute struct {
	serve        func(*server, http.ResponseWriter, *http.Request)
	receipt      receiptMode
	requestLimit int64
}

// The host forwards exactly these guest routes. /activity is private to the host.
var guestRoutes = map[string]guestRoute{
	http.MethodGet + " " + rpc.HealthPath:        {serve: (*server).health},
	http.MethodPost + " " + rpc.CancelPath:       {serve: (*server).cancelRun},
	http.MethodPost + " " + rpc.CloseBrowserPath: {serve: (*server).closeTaskBrowser},
	http.MethodPost + " " + rpc.ExecPath:         {serve: (*server).exec, receipt: durableReceipt},
	http.MethodPost + " " + rpc.NavigatePath:     {serve: (*server).navigate, receipt: interruptibleReceipt},
	http.MethodPost + " " + rpc.ScreenshotPath:   {serve: (*server).screenshot, receipt: interruptibleReceipt},
	http.MethodPost + " " + rpc.StartJobPath:     {serve: (*server).startJob, receipt: durableReceipt},
	http.MethodPost + " " + rpc.PutFilePath:      {serve: (*server).putFile, requestLimit: maxFileRequest},
	http.MethodPost + " " + rpc.GetFilePath:      {serve: (*server).getFile},
	http.MethodPost + " " + rpc.JobStatusPath:    {serve: (*server).jobStatus},
	http.MethodPost + " " + rpc.StopJobPath:      {serve: (*server).jobStop},
	http.MethodPost + " " + rpc.StopTaskJobsPath: {serve: (*server).stopTaskJobs},
	http.MethodPost + " " + rpc.OperationPath:    {serve: (*server).operationStatus},
	http.MethodPost + " " + rpc.CancelReadPath:   {serve: (*server).cancelRead},
	http.MethodPost + " " + rpc.ReopenRunPath:    {serve: (*server).reopenRun},
	http.MethodPost + " " + rpc.SandboxRunPath:   {serve: (*server).sandboxRun, receipt: durableReceipt, requestLimit: maxFileRequest},
	// A session is a live stream, not a receipted operation; each MCP call inside it carries its own receipt.
	http.MethodPost + " " + rpc.SandboxSessionPath: {serve: (*server).sandboxSession, requestLimit: maxFileRequest},
}

func (*server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	c, err := browser(ctx)
	if err != nil {
		http.Error(w, "browser not ready", http.StatusServiceUnavailable)
		return
	}
	// Probe the CDP connection without creating a task tab.
	_ = c.conn.CloseNow()
	respond(w, rpc.Health{Ready: true, Receipts: true, TaskTabs: true, Jobs: true, Files: true, Sandbox: sandboxSupported(), Egress: egressSupported(), Sessions: sandboxSupported(), Reopen: true})
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
		response.Header.Del(rpc.DrainingHeader)
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
		route, ok := guestRoutes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		activity.mu.Lock()
		if activity.closing {
			activity.mu.Unlock()
			w.Header().Set(rpc.DrainingHeader, "true")
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
		limit := route.requestLimit
		if limit == 0 {
			limit = maxRPCRequest
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		p.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	if err := httpjson.Decode(w, r, value, maxRPCRequest); err != nil {
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
	dir, closeDir, err := s.openTaskDir(req.TaskID)
	if err != nil {
		http.Error(w, "task directory unavailable", 500)
		return
	}
	defer closeDir()
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

// openTaskDir keeps the directory open so a command starts inside this exact task's
// folder even if the path is replaced while it starts.
func (s *server) openTaskDir(taskID string) (*os.File, func(), error) {
	root, err := os.OpenRoot(s.tasks)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	if err = root.Mkdir(taskID, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, nil, err
	}
	dir, err := root.Open(taskID)
	if err != nil {
		return nil, nil, err
	}
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		_ = dir.Close()
		return nil, nil, errors.New("invalid task directory")
	}
	return dir, func() { _ = dir.Close() }, nil
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
	log.Printf("run %s stop failed: %v", id, err)
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	if state := s.runs[id]; state != nil {
		state.stopErr = err
	}
}

// reopenRun lets a run that was stopped while it waited (for a question or a user takeover) take new operations
// again. Only a drained run reopens: operations that were in flight when it was cancelled stay cancelled.
func (s *server) reopenRun(w http.ResponseWriter, r *http.Request) {
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
	defer s.runsMu.Unlock()
	state := s.runs[req.RunID]
	if state != nil && state.active > 0 {
		http.Error(w, "run still has operations in flight", 409)
		return
	}
	delete(s.runs, req.RunID)
	if root, err := s.receiptRoot(); err == nil {
		_ = root.Remove(req.RunID + ".cancelled")
		root.Close()
	}
	respond(w, map[string]bool{"reopened": true})
}
