package conversation

import (
	"bytes"
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
	defaultOuterMaxRetries    = 3
	defaultOuterRetryBase     = time.Second
	defaultOuterMaxRetryAfter = 15 * time.Second
)

func isRetryableHTTPStatus(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
}

func isRetryableTransportError(ctx context.Context, err error) bool {
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

func parseRetryAfter(header string, now time.Time, maxDelay time.Duration) (time.Duration, bool) {
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

func computeRetryDelay(attempt int, retryAfterHeader string, now time.Time, baseDelay, maxDelay time.Duration, jitter func(time.Duration) time.Duration) time.Duration {
	if d, ok := parseRetryAfter(retryAfterHeader, now, maxDelay); ok {
		return d
	}
	if baseDelay <= 0 {
		baseDelay = defaultOuterRetryBase
	}
	if maxDelay <= 0 {
		maxDelay = defaultOuterMaxRetryAfter
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

func defaultRetryJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	factor := 0.75 + rand.Float64()*0.5
	return time.Duration(float64(d) * factor)
}

func sleepWithContext(ctx context.Context, d time.Duration, sleepFn func(context.Context, time.Duration) error) error {
	if d <= 0 {
		return ctx.Err()
	}
	if sleepFn != nil {
		return sleepFn(ctx, d)
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

func (m *OpenAIModel) maxRetries() int {
	if m != nil && m.MaxRetries > 0 {
		return m.MaxRetries
	}
	if m != nil && m.MaxRetries < 0 {
		return 0
	}
	return defaultOuterMaxRetries
}

func (m *OpenAIModel) retryBaseDelay() time.Duration {
	if m != nil && m.RetryBaseDelay > 0 {
		return m.RetryBaseDelay
	}
	return defaultOuterRetryBase
}

func (m *OpenAIModel) maxRetryAfter() time.Duration {
	if m != nil && m.MaxRetryAfter > 0 {
		return m.MaxRetryAfter
	}
	return defaultOuterMaxRetryAfter
}

func (m *OpenAIModel) doWithRetry(ctx context.Context, endpoint string, body []byte, onRetry func(statusCode int) error) (*http.Response, error) {
	retries := m.maxRetries()
	baseDelay := m.retryBaseDelay()
	maxDelay := m.maxRetryAfter()
	client := m.httpClient()

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+m.key)
		req.Header.Set("Content-Type", "application/json")

		res, err := client.Do(req)
		if err != nil {
			if attempt >= retries || !isRetryableTransportError(ctx, err) {
				return nil, err
			}
			delay := computeRetryDelay(attempt, "", m.now(), baseDelay, maxDelay, defaultRetryJitter)
			slog.WarnContext(ctx, "outer model transport error; retrying",
				"attempt", attempt+1,
				"max_retries", retries,
				"delay_ms", delay.Milliseconds(),
				"err", err,
			)
			if sleepErr := sleepWithContext(ctx, delay, m.Sleep); sleepErr != nil {
				return nil, sleepErr
			}
			if onRetry != nil {
				if rErr := onRetry(0); rErr != nil {
					return nil, rErr
				}
			}
			continue
		}

		if res.StatusCode == http.StatusOK || !isRetryableHTTPStatus(res.StatusCode) || attempt >= retries || (m.BYOKProviderLabel != "" && llmguard.ModelAccessError(res, m.BYOKProviderLabel) != nil) {
			return res, nil
		}

		status := res.StatusCode
		retryAfter := res.Header.Get("Retry-After")
		if m.gatewayURL == "" {
			_ = logRefusal(ctx, res)
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 16<<10))
		}
		_ = res.Body.Close()

		delay := computeRetryDelay(attempt, retryAfter, m.now(), baseDelay, maxDelay, defaultRetryJitter)
		slog.WarnContext(ctx, "outer model transient HTTP error; retrying",
			"attempt", attempt+1,
			"max_retries", retries,
			"status", status,
			"retry_after", retryAfter,
			"delay_ms", delay.Milliseconds(),
		)
		if sleepErr := sleepWithContext(ctx, delay, m.Sleep); sleepErr != nil {
			return nil, sleepErr
		}
		if onRetry != nil {
			if rErr := onRetry(status); rErr != nil {
				return nil, rErr
			}
		}
	}
}
