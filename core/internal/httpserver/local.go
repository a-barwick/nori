package httpserver

import (
	"net"
	"net/http"
	"strings"
)

// RequireLoopbackHost protects local listeners from browser DNS rebinding.
func RequireLoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = name
		}
		switch strings.ToLower(host) {
		case "localhost", "127.0.0.1", "::1", "[::1]":
			next.ServeHTTP(w, r)
		default:
			http.Error(w, "A loopback host is required.", http.StatusForbidden)
		}
	})
}
