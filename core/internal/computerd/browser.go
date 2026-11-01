package computerd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type page struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	WebSocket string `json:"webSocketDebuggerUrl"`
}
type cdp struct {
	conn     *websocket.Conn
	id       int
	endpoint string
}

func browser(ctx context.Context) (*cdp, error) {
	var version struct {
		WebSocket string `json:"webSocketDebuggerUrl"`
	}
	if err := browserJSON(ctx, "/json/version", &version); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(version.WebSocket, "ws://127.0.0.1:9222/devtools/browser/") {
		return nil, errors.New("invalid browser debugging endpoint")
	}
	return dialCDP(ctx, version.WebSocket)
}

var browserHTTP = &http.Client{
	Timeout:       5 * time.Second,
	Transport:     &http.Transport{IdleConnTimeout: 30 * time.Second},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func browserJSON(ctx context.Context, path string, result any) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:9222"+path, nil)
	res, err := browserHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New("browser discovery unavailable")
	}
	return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(result)
}
func dialCDP(ctx context.Context, address string) (*cdp, error) {
	if !strings.HasPrefix(address, "ws://127.0.0.1:9222/devtools/") {
		return nil, errors.New("invalid browser target endpoint")
	}
	conn, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(4 << 20)
	return &cdp{conn: conn, endpoint: address}, nil
}
func targetPage(ctx context.Context, target string) (*cdp, error) {
	var pages []page
	if err := browserJSON(ctx, "/json/list", &pages); err != nil {
		return nil, err
	}
	for _, p := range pages {
		if p.ID == target && p.Type == "page" {
			return dialCDP(ctx, p.WebSocket)
		}
	}
	return nil, errTargetGone
}
func (c *cdp) call(ctx context.Context, method string, params any, result any) error {
	c.id++
	id := c.id
	data, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return err
	}
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return err
		}
		var reply struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err = json.Unmarshal(data, &reply); err != nil {
			return err
		}
		if reply.ID != id {
			continue
		}
		if reply.Error != nil {
			return fmt.Errorf("browser: %s", reply.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(reply.Result, result)
		}
		return nil
	}
}
func stopLoading(owner browserAssociation) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	root, err := browser(ctx)
	if err != nil {
		return err
	}
	defer root.conn.CloseNow()
	// A different browser identity proves the original process has exited.
	if root.endpoint != owner.Browser {
		return nil
	}
	c, err := targetPage(ctx, owner.Target)
	if errors.Is(err, errTargetGone) {
		return nil
	}
	if err != nil {
		return err
	}
	defer c.conn.CloseNow()
	return c.call(ctx, "Page.stopLoading", map[string]any{}, nil)
}

func (s *server) navigate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID  string `json:"run_id"`
		TaskID string `json:"task_id"`
		URL    string `json:"url"`
	}
	if !decode(w, r, &req) {
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		http.Error(w, "HTTP or HTTPS URL required", 400)
		return
	}
	if !runPattern.MatchString(req.TaskID) {
		http.Error(w, "invalid task_id", 400)
		return
	}
	operation, finish, err := s.begin(r.Context(), req.RunID)
	if err != nil {
		http.Error(w, "run canceled", 409)
		return
	}
	defer finish()
	ctx, cancel := context.WithTimeout(operation, 45*time.Second)
	defer cancel()
	session := s.taskBrowser(req.TaskID)
	if !session.lock(ctx) {
		http.Error(w, "run canceled", 409)
		return
	}
	defer session.unlock()
	if ctx.Err() != nil {
		http.Error(w, "request canceled", 408)
		return
	}
	c, owner, err := s.openTaskPage(ctx, req.TaskID, true)
	if err != nil {
		http.Error(w, "task browser unavailable or ownership uncertain", 503)
		return
	}
	defer c.conn.CloseNow()
	complete := false
	defer func() {
		if !complete {
			if err := stopLoading(owner); err != nil {
				s.stopFailed(req.RunID, err)
			}
		}
	}()
	var nav struct {
		ErrorText string `json:"errorText"`
		LoaderID  string `json:"loaderId"`
	}
	err = c.call(ctx, "Page.navigate", map[string]any{"url": req.URL}, &nav)
	if err != nil || nav.ErrorText != "" {
		http.Error(w, "navigation failed", 502)
		return
	}
	for {
		if nav.LoaderID != "" {
			var tree struct {
				FrameTree struct {
					Frame struct {
						LoaderID string `json:"loaderId"`
					} `json:"frame"`
				} `json:"frameTree"`
			}
			if err = c.call(ctx, "Page.getFrameTree", map[string]any{}, &tree); err != nil {
				http.Error(w, "navigation interrupted", 502)
				return
			}
			if tree.FrameTree.Frame.LoaderID != nav.LoaderID {
				select {
				case <-ctx.Done():
					http.Error(w, "navigation timed out", 504)
					return
				case <-time.After(150 * time.Millisecond):
					continue
				}
			}
		}
		var result struct {
			Result struct {
				Value struct {
					Ready bool   `json:"ready"`
					URL   string `json:"url"`
					Title string `json:"title"`
					Text  string `json:"text"`
				} `json:"value"`
			} `json:"result"`
		}
		err = c.call(ctx, "Runtime.evaluate", map[string]any{"expression": "({ready:document.readyState==='complete',url:location.href,title:document.title,text:(document.body?.innerText||'').slice(0,64000)})", "returnByValue": true}, &result)
		if err != nil {
			http.Error(w, "navigation canceled or unavailable", 502)
			return
		}
		if result.Result.Value.Ready {
			complete = true
			v := result.Result.Value
			respond(w, map[string]string{"url": v.URL, "title": v.Title, "text": v.Text})
			return
		}
		select {
		case <-ctx.Done():
			http.Error(w, "navigation timed out", 504)
			return
		case <-time.After(150 * time.Millisecond):
		}
	}
}
func (s *server) screenshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID  string `json:"run_id"`
		TaskID string `json:"task_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !runPattern.MatchString(req.TaskID) {
		http.Error(w, "invalid task_id", 400)
		return
	}
	operation, finish, err := s.begin(r.Context(), req.RunID)
	if err != nil {
		http.Error(w, "run canceled", 409)
		return
	}
	defer finish()
	ctx, cancel := context.WithTimeout(operation, 20*time.Second)
	defer cancel()
	session := s.taskBrowser(req.TaskID)
	if !session.lock(ctx) {
		http.Error(w, "run canceled", 409)
		return
	}
	defer session.unlock()
	c, _, err := s.openTaskPage(ctx, req.TaskID, false)
	if errors.Is(err, errBrowserRestarted) {
		// No capture was attempted. Persist the typed refusal in the receipt
		// body too, so replay gives the model the same safe recovery result.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "browser_restarted"})
		return
	}
	if err != nil {
		http.Error(w, "task browser unavailable or ownership uncertain", 503)
		return
	}
	defer c.conn.CloseNow()
	var result struct {
		Data string `json:"data"`
	}
	// Chromium can stall surface capture for background pages. Foreground
	// visibility is shared, so keep activation and capture together across tasks.
	captureStarted := time.Now()
	stage := "queue"
	err = func() error {
		select {
		case s.screenshotGate <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		defer func() { <-s.screenshotGate }()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		stage = "activation"
		if err := c.call(ctx, "Page.bringToFront", map[string]any{}, nil); err != nil {
			return err
		}
		stage = "capture"
		return c.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false}, &result)
	}()
	if err != nil {
		log.Printf("browser screenshot failed task=%q run=%q operation=%q stage=%s elapsed=%s context_error=%v error=%.512q", req.TaskID, req.RunID, r.Header.Get("Nori-Operation-ID"), stage, time.Since(captureStarted).Round(time.Millisecond), ctx.Err(), err.Error())
		if stage == "queue" && errors.Is(err, context.Canceled) {
			http.Error(w, "run canceled", 409)
			return
		}
		http.Error(w, "screenshot unavailable", 502)
		return
	}
	if len(result.Data) > base64.StdEncoding.EncodedLen(maxScreenshot) {
		http.Error(w, "screenshot exceeds 2 MiB", 413)
		return
	}
	png, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil || len(png) > maxScreenshot {
		http.Error(w, "invalid screenshot", 502)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}
