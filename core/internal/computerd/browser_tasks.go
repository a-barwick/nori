package computerd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
)

var errTargetGone = errors.New("owned browser target is gone")
var errBrowserRestarted = errors.New("task browser process exited; navigate again")

type taskBrowser struct{ gate chan struct{} }

func (s *server) taskBrowser(id string) *taskBrowser {
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	b := s.browsers[id]
	if b == nil {
		b = &taskBrowser{gate: make(chan struct{}, 1)}
		s.browsers[id] = b
	}
	return b
}
func (b *taskBrowser) lock(ctx context.Context) bool {
	select {
	case b.gate <- struct{}{}:
		if ctx.Err() == nil {
			return true
		}
		<-b.gate
		return false
	case <-ctx.Done():
		return false
	}
}
func (b *taskBrowser) unlock() { <-b.gate }

// Tabs share the retained profile's cookies/storage. This association protects
// cooperative task ownership, not access by shell code running as the same user.
// A persisted creation intent without a target is uncertain and never guessed.
type browserAssociation struct {
	Task    string `json:"task_id"`
	Browser string `json:"browser"`
	Target  string `json:"target_id,omitempty"`
	Status  string `json:"status"`
}

func (s *server) browserRoot() (*os.Root, error) {
	root, err := os.OpenRoot(s.tasks)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = root.Mkdir(".nori-browser", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	return root.OpenRoot(".nori-browser")
}
func (s *server) readBrowser(id string) (browserAssociation, error) {
	root, err := s.browserRoot()
	if err != nil {
		return browserAssociation{}, err
	}
	defer root.Close()
	data, err := root.ReadFile(id + ".json")
	if err != nil {
		return browserAssociation{}, err
	}
	var owner browserAssociation
	if err = json.Unmarshal(data, &owner); err != nil {
		return owner, err
	}
	if owner.Task != id || (owner.Status != "creating" && owner.Status != "ready" && owner.Status != "closed") || (owner.Status == "ready" && (owner.Target == "" || owner.Browser == "")) {
		return owner, errors.New("invalid browser ownership record")
	}
	return owner, nil
}
func (s *server) writeBrowser(owner browserAssociation) error {
	root, err := s.browserRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	name := owner.Task + "-" + uuid.NewString() + ".next"
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = root.Rename(name, owner.Task+".json"); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// The caller holds the task's browser lock throughout the complete operation.
func (s *server) openTaskPage(ctx context.Context, task string, navigating bool) (*cdp, browserAssociation, error) {
	root, err := browser(ctx)
	if err != nil {
		return nil, browserAssociation{}, err
	}
	defer root.conn.CloseNow()
	owner, err := s.readBrowser(task)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, owner, err
	}
	// An explicit navigation can start a fresh page after the old browser exits.
	// A screenshot must not silently substitute a blank page for lost state.
	if errors.Is(err, os.ErrNotExist) || owner.Status == "closed" || (navigating && owner.Browser != root.endpoint) {
		owner = browserAssociation{Task: task, Browser: root.endpoint, Status: "creating"}
		if err = s.writeBrowser(owner); err != nil {
			return nil, owner, err
		}
		var result struct {
			Target string `json:"targetId"`
		}
		if err = root.call(ctx, "Target.createTarget", map[string]any{"url": "about:blank", "background": true}, &result); err != nil {
			return nil, owner, err
		}
		if result.Target == "" {
			return nil, owner, errors.New("browser target creation outcome uncertain")
		}
		owner.Target, owner.Status = result.Target, "ready"
		// If persistence fails, the durable creation intent remains uncertain.
		if err = s.writeBrowser(owner); err != nil {
			return nil, owner, err
		}
	}
	if owner.Status != "ready" {
		return nil, owner, errors.New("browser target ownership uncertain")
	}
	if owner.Browser != root.endpoint {
		return nil, owner, errBrowserRestarted
	}
	c, err := targetPage(ctx, owner.Target)
	return c, owner, err
}

// Terminal cleanup is separate from revision cancellation so a superseded run
// cannot close the tab a newer revision is still using. The task workflow calls
// this only after every admitted revision has acknowledged stopped operations.
func (s *server) closeTaskBrowser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !runPattern.MatchString(req.TaskID) {
		http.Error(w, "invalid task_id", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	session := s.taskBrowser(req.TaskID)
	if !session.lock(ctx) {
		http.Error(w, "task browser cleanup timed out", 504)
		return
	}
	defer session.unlock()
	owner, err := s.readBrowser(req.TaskID)
	if errors.Is(err, os.ErrNotExist) {
		respond(w, map[string]bool{"closed": true})
		return
	}
	if err != nil {
		http.Error(w, "task browser ownership uncertain; cleanup not confirmed", 502)
		return
	}
	if owner.Status == "closed" {
		respond(w, map[string]bool{"closed": true})
		return
	}
	root, err := browser(ctx)
	if err != nil {
		http.Error(w, "browser cleanup unavailable", 503)
		return
	}
	defer root.conn.CloseNow()
	if root.endpoint == owner.Browser {
		if owner.Status == "creating" {
			http.Error(w, "task browser ownership uncertain; cleanup not confirmed", 502)
			return
		}
		present, err := targetPresent(ctx, owner.Target)
		if err != nil {
			http.Error(w, "browser target status unavailable", 502)
			return
		}
		if present {
			if err = root.call(ctx, "Target.closeTarget", map[string]string{"targetId": owner.Target}, nil); err != nil {
				http.Error(w, "browser target close uncertain", 502)
				return
			}
			// Chromium acknowledges close before discovery necessarily removes the
			// target. Wait for actual disappearance before acknowledging cleanup.
			for {
				present, err = targetPresent(ctx, owner.Target)
				if err != nil {
					http.Error(w, "browser target exit not confirmed", 502)
					return
				}
				if !present {
					break
				}
				select {
				case <-ctx.Done():
					http.Error(w, "browser target exit timed out", 504)
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
		}
	}
	// A different browser identity also proves the original process and tab
	// exited. Keep the record; a later explicit task continuation may open a tab.
	owner.Status = "closed"
	if err = s.writeBrowser(owner); err != nil {
		http.Error(w, "browser cleanup receipt unavailable", 502)
		return
	}
	respond(w, map[string]bool{"closed": true})
}

func targetPresent(ctx context.Context, target string) (bool, error) {
	var pages []page
	if err := browserJSON(ctx, "/json/list", &pages); err != nil {
		return false, err
	}
	for _, p := range pages {
		if p.ID == target {
			return true, nil
		}
	}
	return false, nil
}
