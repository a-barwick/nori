package main

// Scenarios are ordered request/response sequences for the baseline's
// verification obligations: idempotent retries, revision conflicts, owner
// isolation, stale approvals, lost wakes. Offline, every request body and
// every expected response shape is checked against its schema. With -live,
// the steps run against a core, addressing each user through the same
// /users/{id} prefix the authenticated web server uses.

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type scenario struct {
	Name       string            `json:"name"`
	Obligation string            `json:"obligation"`
	Note       string            `json:"note,omitempty"`
	Requires   []string          `json:"requires,omitempty"` // capability features that must be advertised before -live runs it
	Users      map[string]string `json:"users"`
	Steps      []step            `json:"steps"`
}

type step struct {
	Name       string            `json:"name"`
	User       string            `json:"user"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Body       json.RawMessage   `json:"body,omitempty"`
	BodySchema string            `json:"body_schema,omitempty"`
	Expect     expectation       `json:"expect"`
	Capture    map[string]string `json:"capture,omitempty"` // variable -> JSON pointer into the response
	Wait       *wait             `json:"wait,omitempty"`
}

type expectation struct {
	Status         []int                      `json:"status"`
	Schema         string                     `json:"schema,omitempty"`
	ResourceSchema string                     `json:"resource_schema,omitempty"` // applied to /resource of a receipt
	Match          map[string]json.RawMessage `json:"match,omitempty"`           // JSON pointer -> expected value
	Absent         []string                   `json:"absent,omitempty"`          // JSON pointers that must not exist
	SameAs         string                     `json:"same_as,omitempty"`         // earlier step whose response body must be identical
	Count          map[string]int             `json:"count,omitempty"`           // JSON pointer to an array -> exact length
}

// wait repeats a GET until a pointer matches, for eventually consistent state such as a task finishing.
type wait struct {
	Pointer string          `json:"pointer"`
	Value   json.RawMessage `json:"value"`
	Seconds int             `json:"seconds"`
}

var (
	varPattern = regexp.MustCompile(`\$\{([a-z]+):?([A-Za-z0-9_]*)\}`)
	// intPattern is a quoted "${int:name}": it expands to the captured value without quotes, for numbers.
	intPattern = regexp.MustCompile(`"\$\{int:([A-Za-z0-9_]+)\}"`)
)

type run struct {
	vars      map[string]string
	responses map[string][]byte
}

func newRun() *run { return &run{vars: map[string]string{}, responses: map[string][]byte{}} }

// expand substitutes ${uuid:name} (one fresh UUID per name per run, so a retry reuses it),
// ${var:name} (a captured value) and ${user:name} (a scenario user id).
func (r *run) expand(s string, users map[string]string) (string, error) {
	var err error
	s = intPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := intPattern.FindStringSubmatch(m)[1]
		v, ok := r.vars[name]
		if !ok {
			err = fmt.Errorf("variable %s not captured yet", name)
		}
		return v
	})
	out := varPattern.ReplaceAllStringFunc(s, func(m string) string {
		g := varPattern.FindStringSubmatch(m)
		switch g[1] {
		case "uuid":
			key := "uuid:" + g[2]
			if _, ok := r.vars[key]; !ok {
				r.vars[key] = newUUID()
			}
			return r.vars[key]
		case "var":
			v, ok := r.vars[g[2]]
			if !ok {
				err = fmt.Errorf("variable %s not captured yet", g[2])
			}
			return v
		case "user":
			v, ok := users[g[2]]
			if !ok {
				err = fmt.Errorf("unknown user %s", g[2])
			}
			return v
		}
		err = fmt.Errorf("unknown placeholder %s", m)
		return m
	})
	return out, err
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func loadScenarios(dir string) ([]string, []scenario, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "scenarios", "*.json"))
	sort.Strings(files)
	var out []scenario
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, nil, err
		}
		var sc scenario
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&sc); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
		out = append(out, sc)
	}
	return files, out, nil
}

// checkScenariosOffline validates request bodies (with placeholders expanded) against their schemas
// and that every referenced schema compiles. It returns the number of failures.
func checkScenariosOffline(files []string, scs []scenario, compile func(string) (*jsonschema.Schema, error)) int {
	failures := 0
	for i, sc := range scs {
		name := filepath.Base(files[i])
		r := newRun()
		for _, st := range sc.Steps {
			for k := range st.Capture {
				r.vars[k] = "1"
			}
		}
		if len(sc.Steps) == 0 {
			failures++
			fmt.Printf("FAIL scenario %s: no steps\n", name)
		}
		for _, st := range sc.Steps {
			if _, ok := sc.Users[st.User]; !ok {
				failures++
				fmt.Printf("FAIL scenario %s/%s: unknown user %q\n", name, st.Name, st.User)
			}
			if len(st.Expect.Status) == 0 {
				failures++
				fmt.Printf("FAIL scenario %s/%s: no expected status\n", name, st.Name)
			}
			for _, ref := range []string{st.BodySchema, st.Expect.Schema, st.Expect.ResourceSchema} {
				if ref == "" {
					continue
				}
				if _, err := compile(ref); err != nil {
					failures++
					fmt.Printf("FAIL scenario %s/%s: schema %s: %v\n", name, st.Name, ref, err)
				}
			}
			if st.BodySchema != "" && len(st.Body) > 0 {
				body, err := r.expand(string(st.Body), sc.Users)
				if err == nil {
					err = validateBytes(compile, st.BodySchema, []byte(body))
				}
				if err != nil {
					failures++
					fmt.Printf("FAIL scenario %s/%s: request body: %v\n", name, st.Name, err)
				}
			}
		}
	}
	return failures
}

func validateBytes(compile func(string) (*jsonschema.Schema, error), ref string, body []byte) error {
	sch, err := compile(ref)
	if err != nil {
		return err
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("not JSON: %w", err)
	}
	return sch.Validate(v)
}

// runScenariosLive executes every scenario against base (e.g. http://127.0.0.1:8083).
func runScenariosLive(base string, files []string, scs []scenario, compile func(string) (*jsonschema.Schema, error), only string) int {
	client := &http.Client{Timeout: 30 * time.Second}
	features := liveFeatures(client, base, scs)
	failures := 0
	for i, sc := range scs {
		name := strings.TrimSuffix(filepath.Base(files[i]), ".json")
		if only != "" && !strings.Contains(name, only) {
			continue
		}
		if missing := missingFeatures(sc.Requires, features); len(missing) > 0 {
			fmt.Printf("SKIP %s: core does not advertise %s\n", name, strings.Join(missing, ", "))
			continue
		}
		r := newRun()
		ok := true
		for _, st := range sc.Steps {
			if err := r.do(client, base, sc, st, compile); err != nil {
				failures++
				ok = false
				fmt.Printf("FAIL %s/%s: %v\n", name, st.Name, err)
				break
			}
		}
		if ok {
			fmt.Printf("PASS %s (%s)\n", name, sc.Obligation)
		}
	}
	return failures
}

func liveFeatures(client *http.Client, base string, scs []scenario) map[string]bool {
	out := map[string]bool{}
	var user string
	for _, sc := range scs {
		for _, u := range sc.Users {
			user = u
			break
		}
		if user != "" {
			break
		}
	}
	if user == "" {
		return out
	}
	resp, err := client.Get(strings.TrimRight(base, "/") + "/users/" + user + "/v1/capabilities")
	if err != nil {
		return out
	}
	defer resp.Body.Close()
	var caps struct {
		Features map[string]bool `json:"assistant_features"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&caps)
	maps.Copy(out, caps.Features)
	return out
}

func missingFeatures(req []string, have map[string]bool) []string {
	var out []string
	for _, f := range req {
		if !have[f] {
			out = append(out, f)
		}
	}
	return out
}

func (r *run) do(client *http.Client, base string, sc scenario, st step, compile func(string) (*jsonschema.Schema, error)) error {
	path, err := r.expand(st.Path, sc.Users)
	if err != nil {
		return err
	}
	var body []byte
	if len(st.Body) > 0 {
		s, err := r.expand(string(st.Body), sc.Users)
		if err != nil {
			return err
		}
		body = []byte(s)
	}
	url := strings.TrimRight(base, "/") + "/users/" + sc.Users[st.User] + path
	deadline := time.Now()
	if st.Wait != nil {
		deadline = deadline.Add(time.Duration(st.Wait.Seconds) * time.Second)
	}
	var status int
	var got []byte
	for {
		req, err := http.NewRequest(st.Method, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		got, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		status = resp.StatusCode
		if st.Wait == nil || time.Now().After(deadline) {
			break
		}
		if v, ok := pointer(got, st.Wait.Pointer); ok && jsonEqual(v, st.Wait.Value) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !slices.Contains(st.Expect.Status, status) {
		return fmt.Errorf("status %d, want %v: %s", status, st.Expect.Status, truncate(got))
	}
	if st.Expect.Schema != "" {
		if err := validateBytes(compile, st.Expect.Schema, got); err != nil {
			return fmt.Errorf("response does not match %s: %v", st.Expect.Schema, err)
		}
	}
	if st.Expect.ResourceSchema != "" {
		res, ok := pointer(got, "/resource")
		if !ok {
			return fmt.Errorf("response has no /resource")
		}
		if err := validateBytes(compile, st.Expect.ResourceSchema, res); err != nil {
			return fmt.Errorf("resource does not match %s: %v", st.Expect.ResourceSchema, err)
		}
	}
	for rawP, want := range st.Expect.Match {
		w, err := r.expand(string(want), sc.Users)
		if err != nil {
			return err
		}
		p, err := r.expand(rawP, sc.Users)
		if err != nil {
			return err
		}
		v, ok := pointer(got, p)
		if !ok || !jsonEqual(v, []byte(w)) {
			return fmt.Errorf("%s = %s, want %s", p, truncate(v), w)
		}
	}
	for _, rawP := range st.Expect.Absent {
		p, err := r.expand(rawP, sc.Users)
		if err != nil {
			return err
		}
		if _, ok := pointer(got, p); ok {
			return fmt.Errorf("%s must be absent", p)
		}
	}
	for rawP, n := range st.Expect.Count {
		spec, err := r.expand(rawP, sc.Users)
		if err != nil {
			return err
		}
		// "<pointer>|key=value" counts only elements whose key equals value.
		p, filter, _ := strings.Cut(spec, "|")
		key, value, _ := strings.Cut(filter, "=")
		v, ok := pointer(got, p)
		var arr []map[string]any
		if !ok || json.Unmarshal(v, &arr) != nil {
			return fmt.Errorf("%s is not an array of objects", p)
		}
		count := 0
		for _, el := range arr {
			if filter == "" || fmt.Sprint(el[key]) == value {
				count++
			}
		}
		if count != n {
			return fmt.Errorf("%s has %d matching items, want %d", spec, count, n)
		}
	}
	if st.Expect.SameAs != "" {
		prev, ok := r.responses[st.Expect.SameAs]
		if !ok {
			return fmt.Errorf("same_as step %s has not run", st.Expect.SameAs)
		}
		if !jsonEqual(prev, got) {
			return fmt.Errorf("response differs from step %s:\n  then %s\n  now  %s", st.Expect.SameAs, truncate(prev), truncate(got))
		}
	}
	for k, p := range st.Capture {
		v, ok := pointer(got, p)
		if !ok {
			return fmt.Errorf("capture %s: %s missing", k, p)
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			s = string(v)
		}
		r.vars[k] = s
	}
	r.responses[st.Name] = got
	return nil
}

// pointer resolves an RFC 6901 JSON pointer, plus one extension: a segment
// "[key=value]" selects the first array element whose key equals value.
func pointer(doc []byte, p string) ([]byte, bool) {
	var v any
	if json.Unmarshal(doc, &v) != nil {
		return nil, false
	}
	if p == "" || p == "/" {
		return doc, true
	}
	for seg := range strings.SplitSeq(strings.TrimPrefix(p, "/"), "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		switch cur := v.(type) {
		case map[string]any:
			next, ok := cur[seg]
			if !ok {
				return nil, false
			}
			v = next
		case []any:
			if strings.HasPrefix(seg, "[") && strings.HasSuffix(seg, "]") {
				key, want, _ := strings.Cut(seg[1:len(seg)-1], "=")
				found := false
				for _, el := range cur {
					if m, ok := el.(map[string]any); ok && fmt.Sprint(m[key]) == want {
						v, found = el, true
						break
					}
				}
				if !found {
					return nil, false
				}
				continue
			}
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(cur) {
				return nil, false
			}
			v = cur[i]
		default:
			return nil, false
		}
	}
	out, _ := json.Marshal(v)
	return out, true
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func truncate(b []byte) string {
	if len(b) > 400 {
		return string(b[:400]) + "…"
	}
	return string(b)
}
