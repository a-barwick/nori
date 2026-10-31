package computerd

import (
	"context"
	"net/http"
)

type readOperation struct {
	run    string
	cancel context.CancelFunc
	done   chan struct{}
}

// Only registered browser reads are interruptible. This never fences a task
// revision or cancels a shell operation.
func (s *server) cancelRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID    string `json:"id"`
		RunID string `json:"run_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !runPattern.MatchString(req.ID) || !runPattern.MatchString(req.RunID) {
		http.Error(w, "invalid IDs", 400)
		return
	}
	s.receiptMu.Lock()
	op := s.readOperations[req.ID]
	if op != nil && op.run != req.RunID {
		s.receiptMu.Unlock()
		http.Error(w, "operation belongs to another run", 409)
		return
	}
	if op == nil {
		rec, err := s.readReceipt(req.ID)
		s.receiptMu.Unlock()
		if err != nil || rec.RunID != req.RunID || rec.Status != "completed" || !rec.ReadOperation || !rec.StopConfirmed {
			http.Error(w, "operation stop unconfirmed", 409)
			return
		}
		s.runsMu.Lock()
		run := s.runs[req.RunID]
		failed := run != nil && run.stopErr != nil
		s.runsMu.Unlock()
		if failed {
			http.Error(w, "browser stop unconfirmed", 502)
			return
		}
		respond(w, map[string]bool{"stopped": true})
		return
	}
	op.cancel()
	s.receiptMu.Unlock()
	select {
	case <-op.done:
		// A handler may have failed to stop browser loading; do not hide that.
		s.runsMu.Lock()
		run := s.runs[req.RunID]
		ok := run == nil || run.stopErr == nil
		s.runsMu.Unlock()
		if !ok {
			http.Error(w, "browser stop unconfirmed", 502)
			return
		}
		respond(w, map[string]bool{"stopped": true})
	case <-r.Context().Done():
		http.Error(w, "operation stop unconfirmed", 504)
	}
}
