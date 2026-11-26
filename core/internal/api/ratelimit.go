package api

import (
	"crypto/sha256"
	"encoding/hex"
	"hash/fnv"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nori/internal/settings"
)

const (
	rateLimitShards      = 32
	defaultSSERetryAfter = 5
	rateLimitedErrorCode = "rate_limited"
	sseLimitExceededCode = "sse_limit_exceeded"
)

// RateLimitCategory identifies the HTTP traffic class for token-bucket accounting.
type RateLimitCategory string

const (
	RateLimitResolve RateLimitCategory = "resolve"
	RateLimitWrite   RateLimitCategory = "write"
	RateLimitUpload  RateLimitCategory = "upload"
	RateLimitRead    RateLimitCategory = "read"
)

// RateLimitStats snapshots rejection counts and active SSE streams.
type RateLimitStats struct {
	ResolveRejected int64
	WriteRejected   int64
	UploadRejected  int64
	ReadRejected    int64
	SSERejected     int64
	ActiveSSE       int64
}

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
}

type rateLimitShard struct {
	mu      sync.Mutex
	entries map[string]*tokenBucket
}

// RateLimiter enforces sharded, bounded per-key token-bucket rate limits and per-user concurrent SSE caps.
type RateLimiter struct {
	cfg           settings.RateLimit
	now           func() time.Time
	shards        [rateLimitShards]rateLimitShard
	maxPerShard   int
	sseMu         sync.Mutex
	sseByUser     map[string]int
	activeSSE     atomic.Int64
	resolveReject atomic.Int64
	writeReject   atomic.Int64
	uploadReject  atomic.Int64
	readReject    atomic.Int64
	sseReject     atomic.Int64
}

// NewRateLimiter constructs a RateLimiter with the provided settings.
func NewRateLimiter(cfg settings.RateLimit) *RateLimiter {
	return NewRateLimiterWithClock(cfg, nil)
}

// NewRateLimiterWithClock constructs a RateLimiter with an injectable clock for deterministic tests.
func NewRateLimiterWithClock(cfg settings.RateLimit, now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = settings.DefaultRateLimitMaxEntries
	}
	maxPerShard := (maxEntries + rateLimitShards - 1) / rateLimitShards
	if maxPerShard < 1 {
		maxPerShard = 1
	}
	rl := &RateLimiter{
		cfg:         cfg,
		now:         now,
		maxPerShard: maxPerShard,
		sseByUser:   make(map[string]int),
	}
	for i := range rl.shards {
		rl.shards[i].entries = make(map[string]*tokenBucket)
	}
	return rl
}

// NewRateLimiterFromEnv loads rate-limit settings from environment variables,
// falling back to defaults if any env variable is invalid.
func NewRateLimiterFromEnv() *RateLimiter {
	cfg, err := settings.LoadRateLimitFromEnv()
	if err != nil {
		slog.Warn("invalid rate-limit env configuration; using defaults", "err", err)
		cfg = settings.DefaultRateLimit()
	}
	return NewRateLimiter(cfg)
}

func (rl *RateLimiter) limitsFor(cat RateLimitCategory) (perMin int, burst int) {
	switch cat {
	case RateLimitResolve:
		return rl.cfg.ResolvePerMinute, rl.cfg.ResolveBurst
	case RateLimitUpload:
		return rl.cfg.UploadPerMinute, rl.cfg.UploadBurst
	case RateLimitWrite:
		return rl.cfg.WritePerMinute, rl.cfg.WriteBurst
	default:
		return rl.cfg.ReadPerMinute, rl.cfg.ReadBurst
	}
}

func (rl *RateLimiter) recordReject(cat RateLimitCategory) {
	switch cat {
	case RateLimitResolve:
		rl.resolveReject.Add(1)
	case RateLimitUpload:
		rl.uploadReject.Add(1)
	case RateLimitWrite:
		rl.writeReject.Add(1)
	case RateLimitRead:
		rl.readReject.Add(1)
	}
}

// Allow checks whether a request for (category, key) is permitted.
// If rejected, retryAfter is the recommended Retry-After delay in whole seconds (>= 1).
func (rl *RateLimiter) Allow(cat RateLimitCategory, key string) (allowed bool, retryAfter int) {
	if rl == nil || !rl.cfg.Enabled {
		return true, 0
	}
	perMin, burst := rl.limitsFor(cat)
	if perMin <= 0 {
		return true, 0
	}
	if burst <= 0 {
		burst = perMin
	}

	bucketKey := string(cat) + ":" + key
	shard := &rl.shards[shardIndex(bucketKey)]
	now := rl.now()
	ratePerSec := float64(perMin) / 60.0
	maxTokens := float64(burst)

	shard.mu.Lock()
	defer shard.mu.Unlock()

	b, ok := shard.entries[bucketKey]
	if ok {
		if rl.cfg.IdleTTL > 0 && now.Sub(b.lastSeen) > rl.cfg.IdleTTL {
			b.tokens = maxTokens
			b.lastRefill = now
			b.lastSeen = now
		} else {
			elapsed := now.Sub(b.lastRefill).Seconds()
			if elapsed > 0 {
				b.tokens = math.Min(maxTokens, b.tokens+elapsed*ratePerSec)
				b.lastRefill = now
			}
			b.lastSeen = now
		}
	} else {
		if len(shard.entries) >= rl.maxPerShard {
			evictShardLocked(shard, now, rl.cfg.IdleTTL, rl.maxPerShard)
		}
		b = &tokenBucket{
			tokens:     maxTokens,
			lastRefill: now,
			lastSeen:   now,
		}
		shard.entries[bucketKey] = b
	}

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true, 0
	}

	needed := 1.0 - b.tokens
	secs := int(math.Ceil(needed / ratePerSec))
	if secs < 1 {
		secs = 1
	}
	rl.recordReject(cat)
	return false, secs
}

// AcquireSSE reserves one concurrent SSE stream slot for userID.
// The returned release function is idempotent and must be deferred by the caller.
func (rl *RateLimiter) AcquireSSE(userID string) (release func(), ok bool) {
	if rl == nil || !rl.cfg.Enabled || rl.cfg.MaxConcurrentSSE <= 0 {
		return func() {}, true
	}

	rl.sseMu.Lock()
	current := rl.sseByUser[userID]
	if current >= rl.cfg.MaxConcurrentSSE {
		rl.sseMu.Unlock()
		rl.sseReject.Add(1)
		return nil, false
	}
	rl.sseByUser[userID] = current + 1
	rl.sseMu.Unlock()
	rl.activeSSE.Add(1)

	var once sync.Once
	return func() {
		once.Do(func() {
			rl.sseMu.Lock()
			if n := rl.sseByUser[userID]; n <= 1 {
				delete(rl.sseByUser, userID)
			} else {
				rl.sseByUser[userID] = n - 1
			}
			rl.sseMu.Unlock()
			rl.activeSSE.Add(-1)
		})
	}, true
}

// Stats returns current rejection counters and active SSE stream count.
func (rl *RateLimiter) Stats() RateLimitStats {
	if rl == nil {
		return RateLimitStats{}
	}
	return RateLimitStats{
		ResolveRejected: rl.resolveReject.Load(),
		WriteRejected:   rl.writeReject.Load(),
		UploadRejected:  rl.uploadReject.Load(),
		ReadRejected:    rl.readReject.Load(),
		SSERejected:     rl.sseReject.Load(),
		ActiveSSE:       rl.activeSSE.Load(),
	}
}

func evictShardLocked(shard *rateLimitShard, now time.Time, idleTTL time.Duration, maxPerShard int) {
	if idleTTL > 0 {
		for k, b := range shard.entries {
			if now.Sub(b.lastSeen) > idleTTL {
				delete(shard.entries, k)
			}
		}
	}
	if len(shard.entries) < maxPerShard {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	first := true
	for k, b := range shard.entries {
		if first || b.lastSeen.Before(oldestTime) {
			oldestKey = k
			oldestTime = b.lastSeen
			first = false
		}
	}
	if !first {
		delete(shard.entries, oldestKey)
	}
}

func shardIndex(key string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return h.Sum32() % rateLimitShards
}

func hashRateLimitKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		if ip := strings.TrimSpace(first); ip != "" {
			return ip
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err == nil && host != "" {
		return host
	}
	if addr := strings.TrimSpace(r.RemoteAddr); addr != "" {
		return addr
	}
	return "unknown"
}

func classifyUserRequest(method, path string) (cat RateLimitCategory, isSSE bool) {
	if method == http.MethodGet && (path == "/v1/conversations/assistant/events" || path == "/v1/events") {
		return RateLimitRead, true
	}
	if method == http.MethodPost && (path == "/v1/conversations/assistant/uploads" || path == "/v1/uploads") {
		return RateLimitUpload, false
	}
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return RateLimitRead, false
	}
	return RateLimitWrite, false
}

func writeRateLimited(w http.ResponseWriter, r *http.Request, cat RateLimitCategory, retryAfter int) {
	if retryAfter < 1 {
		retryAfter = 1
	}
	slog.WarnContext(r.Context(), "http rate limit exceeded",
		"category", string(cat),
		"retry_after_sec", retryAfter,
		"method", r.Method,
		"path", r.URL.Path,
	)
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeJSON(w, http.StatusTooManyRequests, map[string]string{
		"error": "Too many requests. Try again shortly.",
		"code":  rateLimitedErrorCode,
	})
}

func writeSSELimitExceeded(w http.ResponseWriter, r *http.Request) {
	slog.WarnContext(r.Context(), "concurrent sse stream limit exceeded",
		"retry_after_sec", defaultSSERetryAfter,
		"method", r.Method,
		"path", r.URL.Path,
	)
	w.Header().Set("Retry-After", strconv.Itoa(defaultSSERetryAfter))
	writeJSON(w, http.StatusTooManyRequests, map[string]string{
		"error": "Too many concurrent event streams. Close an existing tab or try again shortly.",
		"code":  sseLimitExceededCode,
	})
}
