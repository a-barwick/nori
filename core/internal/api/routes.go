package api

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"nori/internal/state"
)

const (
	CallerBrowser  = "browser"
	CallerClient   = "client"
	CallerInternal = "internal"
	CallerLocal    = "local"
	CallerHealth   = "health"

	jsonLimit = 64 << 10
	mcpLimit  = 1 << 20

	uuidSegment = "[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}"
	safeSegment = "[A-Za-z0-9._:@-]{1,200}"
)

type RouteRule struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	User   bool   `json:"user_scoped"`
	RoutePolicy
}

type RoutePolicy struct {
	Callers     []string          `json:"callers"`
	TokenScope  string            `json:"token_scope,omitempty"`
	Body        string            `json:"body,omitempty"`
	MaxBytes    int64             `json:"max_request_bytes,omitempty"`
	Responses   []string          `json:"response_media_types"`
	PathParams  map[string]string `json:"path_parameters,omitempty"`
	MaxSegments int               `json:"max_path_segments,omitempty"`
}

func route(callers ...string) RoutePolicy {
	return RoutePolicy{Callers: callers, Responses: []string{"application/json"}}
}

func (p RoutePolicy) token(scope string) RoutePolicy {
	p.Callers = append(slices.Clone(p.Callers), CallerClient)
	p.TokenScope = scope
	return p
}

func (p RoutePolicy) body(kind string, limit int64) RoutePolicy {
	p.Body, p.MaxBytes = kind, limit
	return p
}

func (p RoutePolicy) responses(types ...string) RoutePolicy {
	p.Responses = append(slices.Clone(p.Responses), types...)
	return p
}

func (p RoutePolicy) param(name, pattern string) RoutePolicy {
	params := make(map[string]string, len(p.PathParams)+1)
	for k, v := range p.PathParams {
		params[k] = v
	}
	params[name] = "^(?:" + pattern + ")$"
	p.PathParams = params
	return p
}

var (
	browserRead = route(CallerBrowser, CallerLocal)
	browserJSON = browserRead.body("json", jsonLimit)
	clientRead  = browserRead.token("read")
	clientJSON  = browserJSON.token("mutate")
	internal    = route(CallerInternal)
	localOnly   = route(CallerLocal)
	healthRule  = route(CallerHealth)
	mcpRule     = route().token("mcp").body("mcp", mcpLimit).responses("text/event-stream", "text/plain")
	fileTypes   = append([]string{"application/octet-stream"}, state.UploadMediaTypes...)
)

type routeMux struct {
	mux      *http.ServeMux
	rules    []RouteRule
	user     bool
	manifest bool
}

func newRouteMux(user, manifest bool) *routeMux {
	return &routeMux{mux: http.NewServeMux(), user: user, manifest: manifest}
}

func (m *routeMux) handle(pattern string, policy RoutePolicy, handler http.HandlerFunc) {
	if len(policy.Callers) == 0 || len(policy.Responses) == 0 ||
		slices.Contains(policy.Callers, CallerClient) != (policy.TokenScope != "") ||
		(policy.Body != "") != (policy.MaxBytes > 0) {
		panic("missing or invalid route rule for " + pattern)
	}
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		method, path = "*", method
	}
	for _, segment := range strings.Split(path, "/") {
		if !strings.HasPrefix(segment, "{") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}"), "...")
		if policy.PathParams[name] == "" {
			policy = policy.param(name, safeSegment)
		}
	}
	if strings.HasPrefix(path, "/v1/") && policy.MaxSegments == 0 {
		policy.MaxSegments = 6
	}
	m.mux.HandleFunc(pattern, handler)
	m.rules = append(m.rules, RouteRule{Method: method, Path: path, User: m.user, RoutePolicy: policy})
}

// RouteManifest builds the complete API, including routes enabled by optional services.
func RouteManifest() []RouteRule {
	root := newRouteMux(false, true)
	(&userServer{}).register(root)
	scoped := newRouteMux(true, true)
	(&server{Deps{Assistant: &Assistant{}}}).register(scoped)
	rules := append(root.rules, scoped.rules...)
	slices.SortFunc(rules, func(a, b RouteRule) int {
		return cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Method, b.Method), strings.Compare(strconv.FormatBool(a.User), strconv.FormatBool(b.User)))
	})
	return rules
}
