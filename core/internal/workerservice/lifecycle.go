// Package workerservice binds one Temporal worker lifetime to an HTTP request.
package workerservice

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
)

// Tracker remembers every execution seen by this slice, including executions
// whose terminal projection was published before their cleanup activities end.
type Tracker struct {
	interceptor.WorkerInterceptorBase
	mu     sync.Mutex
	ids    map[string]bool
	active int
	last   time.Time
}

func NewTracker(ids ...string) *Tracker {
	t := &Tracker{ids: map[string]bool{}, last: time.Now()}
	for _, id := range ids {
		t.ids[id] = true
	}
	return t
}

func (t *Tracker) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &trackedActivity{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, tracker: t}
}

type trackedActivity struct {
	interceptor.ActivityInboundInterceptorBase
	tracker *Tracker
}

func (a *trackedActivity) ExecuteActivity(ctx context.Context, in *interceptor.ExecuteActivityInput) (any, error) {
	t := a.tracker
	t.mu.Lock()
	t.ids[activity.GetInfo(ctx).WorkflowExecution.ID] = true
	t.active++
	t.last = time.Now()
	t.mu.Unlock()
	defer func() { t.mu.Lock(); t.active--; t.last = time.Now(); t.mu.Unlock() }()
	return a.Next.ExecuteActivity(ctx, in)
}

func (t *Tracker) Snapshot() ([]string, int, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ids := make([]string, 0, len(t.ids))
	for id := range t.ids {
		ids = append(ids, id)
	}
	return ids, t.active, time.Since(t.last)
}

// Handler never starts polling until a /run request owns this process. Cloud Run
// IAM authenticates production calls; the mutex also bounds local concurrency.
func Handler(process context.Context, run func(context.Context, string, []string) error) http.Handler {
	gate := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			WorkflowIDs []string `json:"workflow_ids"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			http.Error(w, "invalid worker wake", http.StatusBadRequest)
			return
		}
		for _, id := range body.WorkflowIDs {
			if !strings.HasPrefix(id, "nori-task-") || len(id) > 256 {
				http.Error(w, "invalid task identity", http.StatusBadRequest)
				return
			}
		}

		select {
		case gate <- struct{}{}:
			defer func() { <-gate }()
		default:
			http.Error(w, "worker slice is already active", http.StatusServiceUnavailable)
			return
		}
		id := uuid.NewString()
		log.Printf("worker_slice slice=%s phase=request task=%q attempt=%q", id, r.Header.Get("X-CloudTasks-TaskName"), r.Header.Get("X-CloudTasks-TaskRetryCount"))
		// A client disconnect must not cancel an already admitted paid Activity.
		// run owns its polling cutoff and drains before returning this response.
		if err := run(process, id, body.WorkflowIDs); err != nil {
			log.Printf("worker_slice slice=%s phase=response status=503 error=%q", id, err)
			http.Error(w, "worker wake will retry", http.StatusServiceUnavailable)
			return
		}
		log.Printf("worker_slice slice=%s phase=response status=200", id)
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
