// Package workerservice binds one worker lifetime, a slice, to an HTTP request.
// Nothing here knows the engine behind the worker: handler.go is the request
// surface, and slice.go is the lifecycle a request-bound worker follows.
package workerservice

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/google/uuid"
	"nori/internal/dispatch"
	"nori/internal/observability"
)

// RunFunc runs one worker slice to completion. registration asks for a
// registration-only run that executes no task.
type RunFunc func(ctx context.Context, sliceID string, workflowIDs []string, registration bool) error

// Handler never starts polling until a /run request owns this process. Cloud Run
// IAM authenticates production calls; the gate also bounds local concurrency.
func Handler(process context.Context, run RunFunc) http.Handler {
	gate := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			WorkflowIDs      []string `json:"workflow_ids"`
			RegistrationOnly bool     `json:"registration_only"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			http.Error(w, "invalid worker wake", http.StatusBadRequest)
			return
		}
		for _, id := range body.WorkflowIDs {
			if _, ok := dispatch.RunID(id); !ok || len(id) > 256 {
				http.Error(w, "invalid task identity", http.StatusBadRequest)
				return
			}
		}
		if body.RegistrationOnly && len(body.WorkflowIDs) != 0 {
			http.Error(w, "registration cannot include workflows", http.StatusBadRequest)
			return
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
		workCtx := observability.ExtractTrace(process, observability.InjectTrace(r.Context()))
		observability.Event(workCtx, "worker.slice.started")
		if err := run(workCtx, id, body.WorkflowIDs, body.RegistrationOnly); err != nil {
			observability.RecordErrorCategory(workCtx, "worker.lifecycle", err)
			observability.Event(workCtx, "worker.slice.failed")
			log.Printf("worker_slice slice=%s phase=response status=503 error=%q", id, err)
			http.Error(w, "worker wake will retry", http.StatusServiceUnavailable)
			return
		}
		observability.Event(workCtx, "worker.slice.completed")
		log.Printf("worker_slice slice=%s phase=response status=200", id)
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
