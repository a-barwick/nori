package gateway

import (
	"crypto/sha256"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type bucket struct {
	limiter *rate.Limiter
	last    time.Time
}

type limits struct {
	mu       sync.Mutex
	perToken map[[32]byte]bucket
	sweep    time.Time
	rate     rate.Limit
	burst    int
	total    *rate.Limiter
	devices  *rate.Limiter
}

func newLimits(perMinute, burst int) *limits {
	return &limits{
		perToken: map[[32]byte]bucket{}, rate: rate.Limit(float64(perMinute) / 60), burst: burst,
		total: rate.NewLimiter(80, 80), devices: rate.NewLimiter(12, 24),
	}
}

func (l *limits) allow(token string, now time.Time) bool {
	if token == "" {
		return l.devices.AllowN(now, 1)
	}
	if !l.total.AllowN(now, 1) {
		return false
	}
	key := sha256.Sum256([]byte(token))
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.After(l.sweep) {
		for hash, entry := range l.perToken {
			if now.Sub(entry.last) > 90*time.Second {
				delete(l.perToken, hash)
			}
		}
		l.sweep = now.Add(15 * time.Second)
	}
	entry, ok := l.perToken[key]
	if !ok {
		if len(l.perToken) >= 10_000 {
			return false
		}
		entry.limiter = rate.NewLimiter(l.rate, l.burst)
	}
	entry.last = now
	l.perToken[key] = entry
	return entry.limiter.AllowN(now, 1)
}
