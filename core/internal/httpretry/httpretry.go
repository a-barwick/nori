package httpretry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nori/internal/llmguard"
)

const (
	DefaultMaxRetries = 3
	DefaultBaseDelay  = time.Second
	DefaultMaxDelay   = 15 * time.Second
)

// Config controls HTTP request retry behavior.
type Config struct {
	Client            *http.Client
	MaxRetries        int                                        // 0 uses DefaultMaxRetries; negative disables retries
	BaseDelay         time.Duration                              // <= 0 uses DefaultBaseDelay
	MaxDelay          time.Duration                              // <= 0 uses DefaultMaxDelay
	Now               func() time.Time                           // nil uses time.Now
	Sleep             func(context.Context, time.Duration) error // nil uses SleepWithContext
	Jitter            func(time.Duration) time.Duration          // nil uses DefaultJitter
	ShouldRetryStatus func(int) bool                             // nil uses IsRetryableStatus
	ShouldStopRetry   func(*http.Response) bool                  // optional predicate to return response without retrying
	DrainBody         func(context.Context, *http.Response)      // optional custom body drain before close
	OnRetry           func(statusCode int) error                 // optional hook invoked after sleep, before next attempt
	LogPrefix         string                                     // prefix for warning log messages
}

// IsRetryableStatus reports whether status is a transient HTTP error that warrants a retry.
// It covers HTTP 429 and all 5xx status codes (including 529 overloaded).
func IsRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
}

// IsRetryableTransportError reports whether a client.Do error is a transient transport failure.
// Context cancellation/deadline, llmguard blocks, and http.ErrUseLastResponse are never retried.
func IsRetryableTransportError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || llmguard.IsGuardError(err) {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && errors.Is(urlErr.Err, http.ErrUseLastResponse) {
		return false
	}
	return true
}

// ParseRetryAfter parses an HTTP Retry-After header (delta-seconds or HTTP-date),
// clamping non-negative delays to maxDelay when maxDelay > 0.
func ParseRetryAfter(header string, now time.Time, maxDelay time.Duration) (time.Duration, bool) {
	raw := strings.TrimSpace(header)
	if raw == "" {
		return 0, false
	}
	if secs, err := strconv.ParseFloat(raw, 64); err == nil && !math.IsNaN(secs) && !math.IsInf(secs, 0) {
		if secs <= 0 {
			return 0, true
		}
		d := time.Duration(secs * float64(time.Second))
		if maxDelay > 0 && d > maxDelay {
			d = maxDelay
		}
		return d, true
	}
	if ts, err := http.ParseTime(raw); err == nil {
		d := ts.Sub(now)
		if d < 0 {
			return 0, true
		}
		if maxDelay > 0 && d > maxDelay {
			d = maxDelay
		}
		return d, true
	}
	return 0, false
}

// DefaultJitter applies +/-25% multiplicative jitter to d.
func DefaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	factor := 0.75 + rand.Float64()*0.5
	return time.Duration(float64(d) * factor)
}

// ComputeDelay calculates the retry wait duration, preferring a valid Retry-After header
// and otherwise falling back to jittered exponential backoff capped at maxDelay.
func ComputeDelay(attempt int, retryAfterHeader string, now time.Time, baseDelay, maxDelay time.Duration, jitter func(time.Duration) time.Duration) time.Duration {
	if d, ok := ParseRetryAfter(retryAfterHeader, now, maxDelay); ok {
		return d
	}
	if baseDelay <= 0 {
		baseDelay = DefaultBaseDelay
	}
	if maxDelay <= 0 {
		maxDelay = DefaultMaxDelay
	}
	d := baseDelay
	for i := 0; i < attempt; i++ {
		if d >= maxDelay/2 {
			d = maxDelay
			break
		}
		d *= 2
	}
	if d > maxDelay {
		d = maxDelay
	}
	if jitter != nil {
		d = jitter(d)
	}
	if d > maxDelay {
		d = maxDelay
	}
	if d < 0 {
		return 0
	}
	return d
}

// SleepWithContext sleeps for d or returns early if ctx is canceled, using time.NewTimer
// so no timer leaks on cancellation.
func SleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func resolveRetries(maxRetries int) int {
	if maxRetries > 0 {
		return maxRetries
	}
	if maxRetries < 0 {
		return 0
	}
	return DefaultMaxRetries
}

// Do executes buildReq through cfg.Client with transient transport and HTTP status retries.
func Do(ctx context.Context, cfg Config, buildReq func() (*http.Request, error)) (*http.Response, error) {
	retries := resolveRetries(cfg.MaxRetries)
	baseDelay := cfg.BaseDelay
	if baseDelay <= 0 {
		baseDelay = DefaultBaseDelay
	}
	maxDelay := cfg.MaxDelay
	if maxDelay <= 0 {
		maxDelay = DefaultMaxDelay
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	sleepFn := cfg.Sleep
	if sleepFn == nil {
		sleepFn = SleepWithContext
	}
	jitterFn := cfg.Jitter
	if jitterFn == nil {
		jitterFn = DefaultJitter
	}
	shouldRetryStatus := cfg.ShouldRetryStatus
	if shouldRetryStatus == nil {
		shouldRetryStatus = IsRetryableStatus
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	logPrefix := strings.TrimSpace(cfg.LogPrefix)
	if logPrefix == "" {
		logPrefix = "http"
	}

	for attempt := 0; ; attempt++ {
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		res, err := client.Do(req)
		if err != nil {
			if attempt >= retries || !IsRetryableTransportError(ctx, err) {
				return nil, err
			}
			delay := ComputeDelay(attempt, "", nowFn(), baseDelay, maxDelay, jitterFn)
			slog.WarnContext(ctx, logPrefix+" transport error; retrying",
				"attempt", attempt+1,
				"max_retries", retries,
				"delay_ms", delay.Milliseconds(),
				"err", err,
			)
			if sleepErr := sleepFn(ctx, delay); sleepErr != nil {
				return nil, sleepErr
			}
			if cfg.OnRetry != nil {
				if rErr := cfg.OnRetry(0); rErr != nil {
					return nil, rErr
				}
			}
			continue
		}

		if res.StatusCode == http.StatusOK || !shouldRetryStatus(res.StatusCode) || attempt >= retries || (cfg.ShouldStopRetry != nil && cfg.ShouldStopRetry(res)) {
			return res, nil
		}

		status := res.StatusCode
		retryAfter := res.Header.Get("Retry-After")
		if cfg.DrainBody != nil {
			cfg.DrainBody(ctx, res)
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 16<<10))
		}
		_ = res.Body.Close()

		delay := ComputeDelay(attempt, retryAfter, nowFn(), baseDelay, maxDelay, jitterFn)
		slog.WarnContext(ctx, logPrefix+" transient HTTP error; retrying",
			"attempt", attempt+1,
			"max_retries", retries,
			"status", status,
			"retry_after", retryAfter,
			"delay_ms", delay.Milliseconds(),
		)
		if sleepErr := sleepFn(ctx, delay); sleepErr != nil {
			return nil, sleepErr
		}
		if cfg.OnRetry != nil {
			if rErr := cfg.OnRetry(status); rErr != nil {
				return nil, rErr
			}
		}
	}
}
