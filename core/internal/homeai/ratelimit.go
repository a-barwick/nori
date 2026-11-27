package homeai

import (
	"net/http"
	"sync"
	"time"

	"nori/internal/netguard"
)

const (
	relayRateLimitPerMin  = 60.0
	relayRateLimitBurst   = 60.0
	relayRateLimitMaxKeys = 4096
)

type ipBucket struct {
	tokens   float64
	lastSeen time.Time
}

type ipRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	now     func() time.Time
}

func newIPRateLimiter() *ipRateLimiter {
	return &ipRateLimiter{
		buckets: make(map[string]*ipBucket),
		now:     time.Now,
	}
}

func (l *ipRateLimiter) allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= relayRateLimitMaxKeys {
			l.evictOldest(now)
		}
		l.buckets[key] = &ipBucket{tokens: relayRateLimitBurst - 1, lastSeen: now}
		return true
	}

	elapsed := now.Sub(b.lastSeen).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * (relayRateLimitPerMin / 60.0)
		if b.tokens > relayRateLimitBurst {
			b.tokens = relayRateLimitBurst
		}
		b.lastSeen = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *ipRateLimiter) evictOldest(now time.Time) {
	cutoff := now.Add(-5 * time.Minute)
	for k, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
	if len(l.buckets) < relayRateLimitMaxKeys {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, b := range l.buckets {
		if first || b.lastSeen.Before(oldestTime) {
			oldestKey = k
			oldestTime = b.lastSeen
			first = false
		}
	}
	if !first {
		delete(l.buckets, oldestKey)
	}
}

func (l *ipRateLimiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && (r.URL.Path == "/healthz" || r.URL.Path == "/readyz") {
			next.ServeHTTP(w, r)
			return
		}
		key := netguard.RateLimitIPKey(netguard.ClientIP(r))
		if !l.allow(key) {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, oauthError{Error: "slow_down"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
