package settings

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultRateLimitResolvePerMin   = 30
	DefaultRateLimitResolveBurst    = 10
	DefaultRateLimitWritePerMin     = 60
	DefaultRateLimitWriteBurst      = 20
	DefaultRateLimitUploadPerMin    = 20
	DefaultRateLimitUploadBurst     = 10
	DefaultRateLimitReadPerMin      = 300
	DefaultRateLimitReadBurst       = 60
	DefaultRateLimitMaxConcurrentSS = 10
	DefaultRateLimitMaxEntries      = 10000
	DefaultRateLimitIdleTTL         = 15 * time.Minute
)

// RateLimit configures HTTP-layer token-bucket rate limiting and concurrent SSE stream caps.
type RateLimit struct {
	Enabled          bool
	ResolvePerMinute int
	ResolveBurst     int
	WritePerMinute   int
	WriteBurst       int
	UploadPerMinute  int
	UploadBurst      int
	ReadPerMinute    int
	ReadBurst        int
	MaxConcurrentSSE int
	MaxEntries       int
	IdleTTL          time.Duration
}

// DefaultRateLimit returns production-safe default HTTP rate-limit settings.
func DefaultRateLimit() RateLimit {
	return RateLimit{
		Enabled:          true,
		ResolvePerMinute: DefaultRateLimitResolvePerMin,
		ResolveBurst:     DefaultRateLimitResolveBurst,
		WritePerMinute:   DefaultRateLimitWritePerMin,
		WriteBurst:       DefaultRateLimitWriteBurst,
		UploadPerMinute:  DefaultRateLimitUploadPerMin,
		UploadBurst:      DefaultRateLimitUploadBurst,
		ReadPerMinute:    DefaultRateLimitReadPerMin,
		ReadBurst:        DefaultRateLimitReadBurst,
		MaxConcurrentSSE: DefaultRateLimitMaxConcurrentSS,
		MaxEntries:       DefaultRateLimitMaxEntries,
		IdleTTL:          DefaultRateLimitIdleTTL,
	}
}

// DisabledRateLimit returns a configuration with HTTP rate limiting disabled (for load tests / DSTs).
func DisabledRateLimit() RateLimit {
	cfg := DefaultRateLimit()
	cfg.Enabled = false
	return cfg
}

// LoadRateLimitFromEnv loads HTTP rate-limit settings from environment variables with safe defaults.
func LoadRateLimitFromEnv() (RateLimit, error) {
	cfg := DefaultRateLimit()

	if raw := strings.TrimSpace(os.Getenv("NORI_RATE_LIMIT_ENABLED")); raw != "" {
		switch strings.ToLower(raw) {
		case "true", "1":
			cfg.Enabled = true
		case "false", "0":
			cfg.Enabled = false
		default:
			return RateLimit{}, fmt.Errorf("NORI_RATE_LIMIT_ENABLED must be true or false")
		}
	}

	intFields := []struct {
		env  string
		dest *int
	}{
		{"NORI_RATE_LIMIT_RESOLVE_PER_MIN", &cfg.ResolvePerMinute},
		{"NORI_RATE_LIMIT_RESOLVE_BURST", &cfg.ResolveBurst},
		{"NORI_RATE_LIMIT_WRITE_PER_MIN", &cfg.WritePerMinute},
		{"NORI_RATE_LIMIT_WRITE_BURST", &cfg.WriteBurst},
		{"NORI_RATE_LIMIT_UPLOAD_PER_MIN", &cfg.UploadPerMinute},
		{"NORI_RATE_LIMIT_UPLOAD_BURST", &cfg.UploadBurst},
		{"NORI_RATE_LIMIT_READ_PER_MIN", &cfg.ReadPerMinute},
		{"NORI_RATE_LIMIT_READ_BURST", &cfg.ReadBurst},
		{"NORI_RATE_LIMIT_MAX_CONCURRENT_SSE", &cfg.MaxConcurrentSSE},
		{"NORI_RATE_LIMIT_MAX_ENTRIES", &cfg.MaxEntries},
	}
	for _, f := range intFields {
		raw := strings.TrimSpace(os.Getenv(f.env))
		if raw == "" {
			continue
		}
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return RateLimit{}, fmt.Errorf("%s must be a non-negative integer", f.env)
		}
		*f.dest = v
	}

	if raw := strings.TrimSpace(os.Getenv("NORI_RATE_LIMIT_IDLE_TTL")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			return RateLimit{}, fmt.Errorf("NORI_RATE_LIMIT_IDLE_TTL must be a non-negative Go duration")
		}
		cfg.IdleTTL = d
	}

	if err := cfg.Validate(); err != nil {
		return RateLimit{}, err
	}
	return cfg, nil
}

// Validate checks that all rate-limit settings are non-negative.
func (r RateLimit) Validate() error {
	checks := []struct {
		name string
		val  int
	}{
		{"NORI_RATE_LIMIT_RESOLVE_PER_MIN", r.ResolvePerMinute},
		{"NORI_RATE_LIMIT_RESOLVE_BURST", r.ResolveBurst},
		{"NORI_RATE_LIMIT_WRITE_PER_MIN", r.WritePerMinute},
		{"NORI_RATE_LIMIT_WRITE_BURST", r.WriteBurst},
		{"NORI_RATE_LIMIT_UPLOAD_PER_MIN", r.UploadPerMinute},
		{"NORI_RATE_LIMIT_UPLOAD_BURST", r.UploadBurst},
		{"NORI_RATE_LIMIT_READ_PER_MIN", r.ReadPerMinute},
		{"NORI_RATE_LIMIT_READ_BURST", r.ReadBurst},
		{"NORI_RATE_LIMIT_MAX_CONCURRENT_SSE", r.MaxConcurrentSSE},
		{"NORI_RATE_LIMIT_MAX_ENTRIES", r.MaxEntries},
	}
	for _, c := range checks {
		if c.val < 0 {
			return fmt.Errorf("%s must be a non-negative integer", c.name)
		}
	}
	if r.IdleTTL < 0 {
		return fmt.Errorf("NORI_RATE_LIMIT_IDLE_TTL must be a non-negative Go duration")
	}
	return nil
}
