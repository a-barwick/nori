package computerd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"nori/internal/computer/rpc"
)

// Receipts live on the retained guest disk. They provide transport deduplication,
// not a security boundary from shell code running as the same guest Unix user.
type receipt struct {
	ReadOperation bool   `json:"read_operation,omitempty"`
	StopConfirmed bool   `json:"stop_confirmed,omitempty"`
	ID            string `json:"id"`
	RunID         string `json:"run_id"`
	Digest        string `json:"digest"`
	Status        string `json:"status"`
	HTTPStatus    int    `json:"http_status,omitempty"`
	ContentType   string `json:"content_type,omitempty"`
	Body          []byte `json:"body,omitempty"`
}

func (s *server) receiptRoot() (*os.Root, error) {
	root, e := os.OpenRoot(s.tasks)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	if e = root.Mkdir(".nori-receipts", 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return nil, e
	}
	return root.OpenRoot(".nori-receipts")
}
func (s *server) readReceipt(id string) (receipt, error) {
	root, e := s.receiptRoot()
	if e != nil {
		return receipt{}, e
	}
	defer root.Close()
	data, e := root.ReadFile(id + ".json")
	if e != nil {
		return receipt{}, e
	}
	var r receipt
	e = json.Unmarshal(data, &r)
	return r, e
}
func (s *server) writeReceipt(r receipt, create bool) error {
	root, e := s.receiptRoot()
	if e != nil {
		return e
	}
	defer root.Close()
	data, e := json.Marshal(r)
	if e != nil {
		return e
	}
	name := r.ID + ".json"
	flags := os.O_CREATE | os.O_EXCL | os.O_WRONLY
	if !create {
		name += ".next"
	}
	f, e := root.OpenFile(name, flags, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if !create {
		if e = root.Rename(name, r.ID+".json"); e != nil {
			return e
		}
	}
	dir, e := root.Open(".")
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}

type receiptResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *receiptResponse) Header() http.Header { return w.header }
func (w *receiptResponse) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
	}
}
func (w *receiptResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if w.body.Len()+len(b) > rpc.MaxScreenshot+4096 {
		return 0, errors.New("receipt response too large")
	}
	return w.body.Write(b)
}
func sendReceipt(w http.ResponseWriter, r receipt) {
	w.Header().Set("Content-Type", r.ContentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(r.HTTPStatus)
	_, _ = w.Write(r.Body)
}

func (s *server) receipted(next http.HandlerFunc, interruptible bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(rpc.OperationIDHeader)
		if id == "" {
			next(w, r)
			return
		} // Preserve the deployed inline protocol.
		if !runPattern.MatchString(id) {
			http.Error(w, "invalid operation ID", 400)
			return
		}
		body, e := io.ReadAll(io.LimitReader(r.Body, maxRPCRequest+1))
		if e != nil || len(body) > maxRPCRequest {
			http.Error(w, "invalid body", 400)
			return
		}
		var input struct {
			RunID string `json:"run_id"`
		}
		if json.Unmarshal(body, &input) != nil || !runPattern.MatchString(input.RunID) {
			http.Error(w, "invalid run_id", 400)
			return
		}
		hash := sha256.Sum256(append([]byte(r.URL.Path+"\n"), body...))
		digest := hex.EncodeToString(hash[:])
		s.receiptMu.Lock()
		old, e := s.readReceipt(id)
		if e == nil {
			s.receiptMu.Unlock()
			if old.Digest != digest || old.RunID != input.RunID {
				http.Error(w, "operation ID conflict", 409)
				return
			}
			if old.Status != "completed" {
				http.Error(w, "operation outcome uncertain; not repeated", 409)
				return
			}
			sendReceipt(w, old)
			return
		}
		if !errors.Is(e, os.ErrNotExist) {
			s.receiptMu.Unlock()
			http.Error(w, "receipt unavailable", 503)
			return
		}
		rec := receipt{ID: id, RunID: input.RunID, Digest: digest, Status: "started"}
		if e = s.writeReceipt(rec, true); e != nil {
			s.receiptMu.Unlock()
			http.Error(w, "receipt unavailable", 503)
			return
		}
		s.activeReceipts[id] = true
		s.receiptMu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		response := &receiptResponse{header: make(http.Header)}
		if interruptible {
			ctx, cancel := context.WithCancel(r.Context())
			op := &readOperation{run: input.RunID, cancel: cancel, done: make(chan struct{})}
			s.receiptMu.Lock()
			s.readOperations[id] = op
			s.receiptMu.Unlock()
			defer func() {
				cancel()
				s.receiptMu.Lock()
				delete(s.readOperations, id)
				close(op.done)
				s.receiptMu.Unlock()
			}()
			r = r.WithContext(ctx)
		}
		next(response, r)
		if interruptible {
			rec.ReadOperation = true
			s.runsMu.Lock()
			run := s.runs[input.RunID]
			rec.StopConfirmed = run != nil && run.stopErr == nil
			s.runsMu.Unlock()
		}
		rec.Status = "completed"
		rec.HTTPStatus = response.status
		rec.ContentType = response.header.Get("Content-Type")
		rec.Body = response.body.Bytes()
		if rec.HTTPStatus == 0 {
			rec.HTTPStatus = 200
		}
		s.receiptMu.Lock()
		e = s.writeReceipt(rec, false)
		delete(s.activeReceipts, id)
		s.receiptMu.Unlock()
		if e != nil {
			http.Error(w, "operation result receipt uncertain; not safe to repeat", 502)
			return
		}
		sendReceipt(w, rec)
	}
}
func (s *server) operationStatus(w http.ResponseWriter, r *http.Request) {
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
	rec, e := s.readReceipt(req.ID)
	s.receiptMu.Unlock()
	if errors.Is(e, os.ErrNotExist) {
		http.Error(w, "unknown operation", 404)
		return
	}
	if e != nil {
		http.Error(w, "receipt unavailable", 503)
		return
	}
	if rec.RunID != req.RunID {
		http.Error(w, "operation belongs to another run", 409)
		return
	}
	if rec.Status == "started" {
		rec.Status = "uncertain"
	}
	respond(w, rec)
}
func (s *server) persistCancelled(run string) error {
	root, e := s.receiptRoot()
	if e != nil {
		return e
	}
	defer root.Close()
	f, e := root.OpenFile(run+".cancelled", os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	e = f.Sync()
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	dir, e := root.Open(".")
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *server) wasCancelled(run string) bool {
	root, e := s.receiptRoot()
	if e != nil {
		return true
	}
	defer root.Close()
	_, e = root.Stat(run + ".cancelled")
	return e == nil || !errors.Is(e, os.ErrNotExist)
}
func (s *server) uncertainReceipts(run string) bool {
	s.receiptMu.Lock()
	defer s.receiptMu.Unlock()
	root, e := s.receiptRoot()
	if e != nil {
		return true
	}
	defer root.Close()
	dir, e := root.Open(".")
	if e != nil {
		return true
	}
	defer dir.Close()
	names, e := dir.Readdirnames(-1)
	if e != nil {
		return true
	}
	for _, name := range names {
		if filepath.Ext(name) != ".json" {
			continue
		}
		rec, e := s.readReceipt(name[:len(name)-5])
		if e != nil {
			return true
		}
		if rec.RunID == run && rec.Status == "started" && !s.activeReceipts[rec.ID] {
			return true
		}
	}
	return false
}
