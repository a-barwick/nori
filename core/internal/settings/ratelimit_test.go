package settings

import (
	"testing"
	"time"
)

func TestLoadRateLimitFromEnvDefaults(t *testing.T) {
	for _, key := range []string{
		"NORI_RATE_LIMIT_ENABLED",
		"NORI_RATE_LIMIT_RESOLVE_PER_MIN",
		"NORI_RATE_LIMIT_RESOLVE_BURST",
		"NORI_RATE_LIMIT_WRITE_PER_MIN",
		"NORI_RATE_LIMIT_WRITE_BURST",
		"NORI_RATE_LIMIT_UPLOAD_PER_MIN",
		"NORI_RATE_LIMIT_UPLOAD_BURST",
		"NORI_RATE_LIMIT_READ_PER_MIN",
		"NORI_RATE_LIMIT_READ_BURST",
		"NORI_RATE_LIMIT_MAX_CONCURRENT_SSE",
		"NORI_RATE_LIMIT_MAX_ENTRIES",
		"NORI_RATE_LIMIT_IDLE_TTL",
	} {
		t.Setenv(key, "")
	}

	cfg, err := LoadRateLimitFromEnv()
	if err != nil {
		t.Fatalf("LoadRateLimitFromEnv() error = %v", err)
	}
	want := DefaultRateLimit()
	if cfg != want {
		t.Fatalf("LoadRateLimitFromEnv() = %+v, want %+v", cfg, want)
	}
	if cfg.ResolvePerMinute != 120 || cfg.ResolveBurst != 30 {
		t.Fatalf("unexpected default resolve rate limit: per_min=%d burst=%d, want 120/30", cfg.ResolvePerMinute, cfg.ResolveBurst)
	}
}

func TestLoadRateLimitFromEnvOverrides(t *testing.T) {
	t.Setenv("NORI_RATE_LIMIT_ENABLED", "false")
	t.Setenv("NORI_RATE_LIMIT_RESOLVE_PER_MIN", "45")
	t.Setenv("NORI_RATE_LIMIT_RESOLVE_BURST", "15")
	t.Setenv("NORI_RATE_LIMIT_WRITE_PER_MIN", "90")
	t.Setenv("NORI_RATE_LIMIT_WRITE_BURST", "30")
	t.Setenv("NORI_RATE_LIMIT_UPLOAD_PER_MIN", "12")
	t.Setenv("NORI_RATE_LIMIT_UPLOAD_BURST", "4")
	t.Setenv("NORI_RATE_LIMIT_READ_PER_MIN", "600")
	t.Setenv("NORI_RATE_LIMIT_READ_BURST", "120")
	t.Setenv("NORI_RATE_LIMIT_MAX_CONCURRENT_SSE", "5")
	t.Setenv("NORI_RATE_LIMIT_MAX_ENTRIES", "2048")
	t.Setenv("NORI_RATE_LIMIT_IDLE_TTL", "5m")

	cfg, err := LoadRateLimitFromEnv()
	if err != nil {
		t.Fatalf("LoadRateLimitFromEnv() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatal("expected Enabled=false")
	}
	if cfg.ResolvePerMinute != 45 || cfg.ResolveBurst != 15 {
		t.Fatalf("unexpected resolve config: %+v", cfg)
	}
	if cfg.WritePerMinute != 90 || cfg.WriteBurst != 30 {
		t.Fatalf("unexpected write config: %+v", cfg)
	}
	if cfg.UploadPerMinute != 12 || cfg.UploadBurst != 4 {
		t.Fatalf("unexpected upload config: %+v", cfg)
	}
	if cfg.ReadPerMinute != 600 || cfg.ReadBurst != 120 {
		t.Fatalf("unexpected read config: %+v", cfg)
	}
	if cfg.MaxConcurrentSSE != 5 || cfg.MaxEntries != 2048 || cfg.IdleTTL != 5*time.Minute {
		t.Fatalf("unexpected limits: %+v", cfg)
	}
}

func TestLoadRateLimitFromEnvValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		env  string
		val  string
	}{
		{"invalid_bool", "NORI_RATE_LIMIT_ENABLED", "maybe"},
		{"negative_int", "NORI_RATE_LIMIT_WRITE_PER_MIN", "-5"},
		{"non_numeric", "NORI_RATE_LIMIT_READ_PER_MIN", "fast"},
		{"invalid_duration", "NORI_RATE_LIMIT_IDLE_TTL", "notaduration"},
		{"negative_duration", "NORI_RATE_LIMIT_IDLE_TTL", "-1m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.env, tc.val)
			if _, err := LoadRateLimitFromEnv(); err == nil {
				t.Fatalf("expected error for %s=%q", tc.env, tc.val)
			}
		})
	}
}

func TestDisabledRateLimit(t *testing.T) {
	cfg := DisabledRateLimit()
	if cfg.Enabled {
		t.Fatal("DisabledRateLimit().Enabled should be false")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DisabledRateLimit().Validate() error = %v", err)
	}
}
