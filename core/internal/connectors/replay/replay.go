// Package replay serves recorded provider responses to the Connections
// service on a local development stack, so connection, approval and recovery
// journeys can run before any real provider grant exists.
//
// It is never a provider: recordings are hand-written from the provider's
// documented response shapes, and evidence produced with it is "recorded",
// not "provider verified". platform refuses to enable it outside
// NORI_CLOUD=local.
//
// A recording directory holds *.json files, each {"exchanges": [...]}. The
// first exchange that matches a request and still has uses left answers it.
// An exchange can require that another exchange was used first ("after"),
// which models provider state such as an insert that already happened. A
// "drop" fault applies the exchange (it counts as used) and then fails the
// request as a lost response, which is how an uncertain write is produced.
package replay

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Exchange is one recorded request and its response.
type Exchange struct {
	Name         string            `json:"name"`
	Method       string            `json:"method"`
	Host         string            `json:"host"`
	Path         string            `json:"path"` // exact, or a prefix ending in "*"
	Query        map[string]string `json:"query,omitempty"`
	BodyContains []string          `json:"body_contains,omitempty"`
	BasicAuth    *BasicAuth        `json:"basic_auth,omitempty"` // the request must carry exactly these credentials
	// Capture saves the matched request under a name so a later response can echo it: {{capture:NAME.path}} is the
	// request path, {{capture:NAME.stem}} its last segment without extension, {{capture:NAME.body}} the request body
	// and {{capture:NAME.body_xml}} the body escaped for XML text. A read-back can then return exactly what was written.
	Capture  string   `json:"capture,omitempty"`
	After    []string `json:"after,omitempty"` // exchanges that must have been used first
	Times    int      `json:"times,omitempty"` // 0 means unlimited
	DelayMS  int      `json:"delay_ms,omitempty"`
	Drop     bool     `json:"drop,omitempty"` // apply, then lose the response
	Response Response `json:"response"`
}

// BasicAuth names the made-up credentials an exchange answers to, so an accepted and a rejected pair can be recorded
// for the same request. Recordings never hold real credentials.
type BasicAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Response is the recorded reply. Body is JSON; Text is used when Body is absent.
type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
	Text    string            `json:"text,omitempty"`
}

// Transport is an http.RoundTripper over a recording directory.
type Transport struct {
	mu        sync.Mutex
	exchanges []Exchange
	used      map[string]int
	captured  map[string]string // "NAME.path" and the like -> value
}

// ErrDropped is returned for an exchange recorded with "drop": the provider applied it, the response was lost.
var ErrDropped = errors.New("replay: response lost after the provider applied the request")

var shared sync.Map // dir + "\x00" + owner -> *Transport

// Shared returns one transport per recording directory and owner for the life of the process, so recorded
// provider state (an insert that already happened) survives across requests and users stay separate.
func Shared(dir, owner string) (*Transport, error) {
	key := dir + "\x00" + owner
	if t, ok := shared.Load(key); ok {
		return t.(*Transport), nil
	}
	t, err := New(dir)
	if err != nil {
		return nil, err
	}
	actual, _ := shared.LoadOrStore(key, t)
	return actual.(*Transport), nil
}

// New loads every recording in dir and in its immediate subdirectories (one per provider).
func New(dir string) (*Transport, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	nested, err := filepath.Glob(filepath.Join(dir, "*", "*.json"))
	if err != nil {
		return nil, err
	}
	files = append(files, nested...)
	if len(files) == 0 {
		return nil, fmt.Errorf("replay: no recordings in %s", dir)
	}
	sort.Strings(files)
	t := &Transport{used: map[string]int{}}
	names := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var doc struct {
			Exchanges []Exchange `json:"exchanges"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("replay: %s: %w", filepath.Base(f), err)
		}
		for _, x := range doc.Exchanges {
			if x.Name == "" || x.Method == "" || x.Host == "" || x.Path == "" || x.Response.Status == 0 {
				return nil, fmt.Errorf("replay: %s: exchange %q needs name, method, host, path and response.status", filepath.Base(f), x.Name)
			}
			if names[x.Name] {
				return nil, fmt.Errorf("replay: duplicate exchange name %q", x.Name)
			}
			names[x.Name] = true
			t.exchanges = append(t.exchanges, x)
		}
	}
	for _, x := range t.exchanges {
		for _, a := range x.After {
			if !names[a] {
				return nil, fmt.Errorf("replay: exchange %q waits for unknown exchange %q", x.Name, a)
			}
		}
	}
	log.Printf("PROVIDER REPLAY ENABLED: %d recorded exchanges from %s; no provider is contacted", len(t.exchanges), dir)
	return t, nil
}

// RoundTrip answers from the first matching recorded exchange.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}
	t.mu.Lock()
	var hit *Exchange
	for i := range t.exchanges {
		x := &t.exchanges[i]
		if t.matches(x, req, body) {
			hit = x
			t.used[x.Name]++
			if x.Capture != "" {
				if t.captured == nil {
					t.captured = map[string]string{}
				}
				segment := path.Base(req.URL.Path)
				var escaped bytes.Buffer
				_ = xml.EscapeText(&escaped, body)
				t.captured[x.Capture+".path"] = req.URL.Path
				t.captured[x.Capture+".stem"] = strings.TrimSuffix(segment, path.Ext(segment))
				t.captured[x.Capture+".body"] = string(body)
				t.captured[x.Capture+".body_xml"] = escaped.String()
			}
			break
		}
	}
	reply := Response{}
	if hit != nil {
		reply = hit.Response
		reply.Text = t.expand(reply.Text)
		if len(reply.Body) > 0 {
			reply.Body = json.RawMessage(t.expand(string(reply.Body)))
		}
	}
	t.mu.Unlock()
	if hit == nil {
		log.Printf("provider replay: no recording for %s %s%s", req.Method, req.URL.Host, req.URL.Path)
		return response(req, Response{Status: http.StatusNotImplemented, Body: json.RawMessage(`{"error":{"code":501,"message":"no recorded response"}}`)}), nil
	}
	log.Printf("provider replay: %s %s%s -> %s (%d)", req.Method, req.URL.Host, req.URL.Path, hit.Name, hit.Response.Status)
	if hit.DelayMS > 0 {
		select {
		case <-time.After(time.Duration(hit.DelayMS) * time.Millisecond):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	if hit.Drop {
		return nil, ErrDropped
	}
	return response(req, reply), nil
}

var placeholder = regexp.MustCompile(`\{\{capture:([A-Za-z0-9_-]+\.(?:path|stem|body|body_xml))\}\}`)

// expand fills {{capture:...}} placeholders from earlier captured requests; an unknown capture is left as is. Callers
// hold t.mu.
func (t *Transport) expand(text string) string {
	if t.captured == nil || !strings.Contains(text, "{{capture:") {
		return text
	}
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		if v, ok := t.captured[placeholder.FindStringSubmatch(m)[1]]; ok {
			return v
		}
		return m
	})
}

// Used reports how many times each exchange answered, for evidence.
func (t *Transport) Used() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return maps.Clone(t.used)
}

func (t *Transport) matches(x *Exchange, req *http.Request, body []byte) bool {
	if !strings.EqualFold(x.Method, req.Method) || !strings.EqualFold(x.Host, req.URL.Host) {
		return false
	}
	if prefix, ok := strings.CutSuffix(x.Path, "*"); ok {
		if !strings.HasPrefix(req.URL.Path, prefix) {
			return false
		}
	} else if x.Path != req.URL.Path {
		return false
	}
	q := req.URL.Query()
	for k, v := range x.Query {
		if q.Get(k) != v {
			return false
		}
	}
	for _, s := range x.BodyContains {
		if !bytes.Contains(body, []byte(s)) {
			return false
		}
	}
	if x.BasicAuth != nil {
		user, password, ok := req.BasicAuth()
		if !ok || user != x.BasicAuth.Username || password != x.BasicAuth.Password {
			return false
		}
	}
	if x.Times > 0 && t.used[x.Name] >= x.Times {
		return false
	}
	for _, a := range x.After {
		if t.used[a] == 0 {
			return false
		}
	}
	return true
}

func response(req *http.Request, r Response) *http.Response {
	body := []byte(r.Body)
	if len(body) == 0 {
		body = []byte(r.Text)
	}
	h := http.Header{}
	for k, v := range r.Headers {
		h.Set(k, v)
	}
	if h.Get("Content-Type") == "" && len(r.Body) > 0 {
		h.Set("Content-Type", "application/json; charset=utf-8")
	}
	return &http.Response{
		StatusCode: r.Status, Status: fmt.Sprintf("%d %s", r.Status, http.StatusText(r.Status)),
		Header: h, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)),
		Request: req, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
	}
}
