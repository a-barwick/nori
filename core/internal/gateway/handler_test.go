package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"nori/internal/clientauth"
	"nori/internal/serviceauth"
)

func TestGatewayRoutesCredentialsAndRateLimit(t *testing.T) {
	key := strings.Repeat("g", 32)
	owner := uuid.NewString()
	id := uuid.NewString()
	token := clientauth.Token(owner, "mcpc_"+strings.Repeat("a", 32), clientauth.NewSecret())
	var forwarded atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		role, _ := (serviceauth.Callers{Gateway: key}).Verify(r.Header.Get(serviceauth.CallerHeader))
		if role != "gateway" || r.URL.Path != clientauth.GatewayPrefix+"/users/"+owner+"/v1/work" ||
			r.Header.Get(clientauth.Header) != token || r.Header.Get("Authorization") != "Bearer service-fixture" ||
			r.Header.Get("Cookie") != "" || r.Header.Get("X-Request-ID") != id {
			t.Error("gateway did not construct the expected upstream request")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	handler, err := New(Config{Upstream: base, CallerKey: key, PerMinute: 1, Burst: 1,
		ServiceToken: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "service-fixture"})})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Cookie", "session=browser-fixture")
		req.Header.Set("X-Request-ID", id)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if res := request("/v1/work"); res.Code != 200 || res.Header().Get("X-Request-ID") != id {
		t.Fatalf("work status %d, want 200 with correlation ID", res.Code)
	}
	if res := request("/v1/preferences"); res.Code != 404 {
		t.Fatalf("browser-only status %d, want 404", res.Code)
	}
	if res := request("/users/" + owner + "/v1/work"); res.Code != 404 {
		t.Fatalf("scoped path status %d, want 404", res.Code)
	}
	res := request("/v1/work")
	var body struct{ Code string }
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil || res.Code != 429 || body.Code != "rate_limited" || res.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit response status %d, code %q", res.Code, body.Code)
	}
	if forwarded.Load() != 1 {
		t.Fatalf("sent %d upstream requests, want 1", forwarded.Load())
	}
}

func TestVertexRateLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	base, _ := url.Parse(upstream.URL)
	handler, err := New(Config{Upstream: base, CallerKey: strings.Repeat("g", 32), Vertex: true, PerMinute: 1, Burst: 1})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("POST", clientauth.DeviceStartPath, strings.NewReader("{}")))
	var body struct {
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != 429 ||
		body.Code != "rate_limited" || !body.Retryable || response.Header().Get("Retry-After") != "17" {
		t.Fatalf("unexpected response: HTTP %d, code %q, retryable %t", response.Code, body.Code, body.Retryable)
	}
}
