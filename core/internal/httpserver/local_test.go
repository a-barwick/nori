package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireLoopbackHost(t *testing.T) {
	handler := RequireLoopbackHost(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, tc := range []struct {
		host string
		want int
	}{
		{"127.0.0.1:8080", http.StatusNoContent},
		{"LOCALHOST", http.StatusNoContent},
		{"[::1]", http.StatusNoContent},
		{"[::1]:8080", http.StatusNoContent},
		{"example.test:8080", http.StatusForbidden},
		{"", http.StatusForbidden},
	} {
		t.Run(tc.host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = tc.host
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.want)
			}
		})
	}
}
