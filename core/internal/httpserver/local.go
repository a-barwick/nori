package httpserver

import (
	"net"
	"net/http"

	"nori/internal/netguard"
)

// RequireLoopbackHost protects local listeners from browser DNS rebinding.
func RequireLoopbackHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if name, _, err := net.SplitHostPort(host); err == nil {
			host = name
		}
		if netguard.IsLoopbackHost(host) {
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "A loopback host is required.", http.StatusForbidden)
	})
}
