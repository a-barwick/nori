package computerd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type page struct {
	Type      string `json:"type"`
	WebSocket string `json:"webSocketDebuggerUrl"`
}
type cdp struct {
	conn *websocket.Conn
	id   int
}

func browser(ctx context.Context) (*cdp, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:9222/json/list", nil)
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var pages []page
	if err = json.NewDecoder(res.Body).Decode(&pages); err != nil {
		return nil, err
	}
	for _, p := range pages {
		if p.Type == "page" && strings.HasPrefix(p.WebSocket, "ws://127.0.0.1:9222/") {
			conn, _, err := websocket.Dial(ctx, p.WebSocket, nil)
			if err != nil {
				return nil, err
			}
			conn.SetReadLimit(4 << 20)
			return &cdp{conn: conn}, nil
		}
	}
	return nil, errors.New("browser page unavailable")
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
func stopLoading() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := browser(ctx)
	if err != nil {
		return err
	}
	defer c.conn.CloseNow()
	return c.call(ctx, "Page.stopLoading", map[string]any{}, nil)
}
func (s *server) navigate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID string `json:"run_id"`
		URL   string `json:"url"`
	}
	if !decode(w, r, &req) {
		return
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		http.Error(w, "HTTP or HTTPS URL required", 400)
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
	if !s.lockBrowser(ctx) {
		http.Error(w, "run canceled", 409)
		return
	}
	defer s.unlockBrowser()
	if ctx.Err() != nil {
		http.Error(w, "request canceled", 408)
		return
	}
	c, err := browser(ctx)
	if err != nil {
		http.Error(w, "browser unavailable", 503)
		return
	}
	defer c.conn.CloseNow()
	complete := false
	defer func() {
		if !complete {
			if err := stopLoading(); err != nil {
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
		RunID string `json:"run_id"`
	}
	if !decode(w, r, &req) {
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
	if !s.lockBrowser(ctx) {
		http.Error(w, "run canceled", 409)
		return
	}
	defer s.unlockBrowser()
	c, err := browser(ctx)
	if err != nil {
		http.Error(w, "browser unavailable", 503)
		return
	}
	defer c.conn.CloseNow()
	var result struct {
		Data string `json:"data"`
	}
	if err = c.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png", "captureBeyondViewport": false}, &result); err != nil {
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
