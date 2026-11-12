package api

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
)

type routeTable struct {
	mux   *http.ServeMux
	rules map[string]RouteRule
}

var routeTableOnce sync.Once
var routeTables [2]routeTable

func initRouteTables() {
	tables := &routeTables
	for i := range tables {
		tables[i] = routeTable{mux: http.NewServeMux(), rules: map[string]RouteRule{}}
	}
	for _, rule := range RouteManifest() {
		table := &tables[0]
		if rule.User {
			table = &tables[1]
		}
		pattern := rule.Path
		if rule.Method != "*" {
			pattern = rule.Method + " " + pattern
		}
		table.mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
		table.rules[pattern] = rule
	}
}

var paramPatterns sync.Map

// MatchRoute selects the same pattern as Core's ServeMux and checks its
// declared path constraints. Paths are decoded before matching.
func MatchRoute(method, path string, user bool) (RouteRule, bool) {
	if !strings.HasPrefix(path, "/") {
		return RouteRule{}, false
	}
	segments := strings.Split(path[1:], "/")
	for _, part := range segments {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00") {
			return RouteRule{}, false
		}
	}
	routeTableOnce.Do(initRouteTables)
	table := routeTables[0]
	if user {
		table = routeTables[1]
	}
	_, pattern := table.mux.Handler(&http.Request{Method: method, URL: &url.URL{Path: path}})
	rule, ok := table.rules[pattern]
	if !ok || rule.MaxSegments > 0 && len(segments) > rule.MaxSegments {
		return RouteRule{}, false
	}
	for i, part := range strings.Split(rule.Path[1:], "/") {
		name, param := strings.CutPrefix(part, "{")
		if !param {
			continue
		}
		name = strings.TrimSuffix(name, "}")
		values := segments[i : i+1]
		if strings.HasSuffix(name, "...") {
			name = strings.TrimSuffix(name, "...")
			values = segments[i:]
		}
		expression := rule.PathParams[name]
		value, cached := paramPatterns.Load(expression)
		if !cached {
			value, _ = paramPatterns.LoadOrStore(expression, regexp.MustCompile(expression))
		}
		for _, segment := range values {
			if !value.(*regexp.Regexp).MatchString(segment) {
				return RouteRule{}, false
			}
		}
	}
	return rule, true
}
