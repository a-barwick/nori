package responses

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
	defaultInnerMaxRetries    = 3
	defaultInnerRetryBase     = time.Second
	defaultInnerMaxRetryAfter = 15 * time.Second
)

func isRetryableStatus(status int) bool {
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

func retryDelay(attempt int, retryAfterHeader string, now time.Time, baseDelay, maxDelay time.Duration) time.Duration {
	if d, ok := parseRetryAfter(retryAfterHeader, now, maxDelay); ok {
		return d
	}
	if baseDelay <= 0 {
		baseDelay = defaultInnerRetryBase
	}
	if maxDelay <= 0 {
		maxDelay = defaultInnerMaxRetryAfter
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
	factor := 0.75 + rand.Float64()*0.5
	d = time.Duration(float64(d) * factor)
	if d > maxDelay {
		d = maxDelay
	}
	return d
}

func (c *Client) maxRetries() int {
	if c != nil && c.MaxRetries > 0 {
		return c.MaxRetries
	}
	if c != nil && c.MaxRetries < 0 {
		return 0
	}
	return defaultInnerMaxRetries
}

func (c *Client) retryBaseDelay() time.Duration {
	if c != nil && c.RetryBaseDelay > 0 {
		return c.RetryBaseDelay
	}
	return defaultInnerRetryBase
}

func (c *Client) maxRetryAfter() time.Duration {
	if c != nil && c.MaxRetryAfter > 0 {
		return c.MaxRetryAfter
	}
	return defaultInnerMaxRetryAfter
}

func (c *Client) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	if c != nil && c.Sleep != nil {
		return c.Sleep(ctx, d)
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

func (c *Client) doWithRetry(ctx context.Context, buildReq func() (*http.Request, error), onRetry func(statusCode int) error) (*http.Response, error) {
	retries := c.maxRetries()
	baseDelay := c.retryBaseDelay()
	maxDelay := c.maxRetryAfter()

	for attempt := 0; ; attempt++ {
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		res, err := c.httpClient().Do(req)
		if err != nil {
			if attempt >= retries || !isRetryableTransportError(ctx, err) {
				return nil, err
			}
			delay := retryDelay(attempt, "", c.now(), baseDelay, maxDelay)
			slog.WarnContext(ctx, "inner model transport error; retrying",
				"attempt", attempt+1,
				"max_retries", retries,
				"delay_ms", delay.Milliseconds(),
				"err", err,
			)
			if sleepErr := c.sleep(ctx, delay); sleepErr != nil {
				return nil, sleepErr
			}
			if onRetry != nil {
				if rErr := onRetry(0); rErr != nil {
					return nil, rErr
				}
			}
			continue
		}

		if res.StatusCode == http.StatusOK || !isRetryableStatus(res.StatusCode) || attempt >= retries {
			return res, nil
		}

		status := res.StatusCode
		retryAfter := res.Header.Get("Retry-After")
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 16<<10))
		_ = res.Body.Close()

		delay := retryDelay(attempt, retryAfter, c.now(), baseDelay, maxDelay)
		slog.WarnContext(ctx, "inner model transient HTTP error; retrying",
			"attempt", attempt+1,
			"max_retries", retries,
			"status", status,
			"retry_after", retryAfter,
			"delay_ms", delay.Milliseconds(),
		)
		if sleepErr := c.sleep(ctx, delay); sleepErr != nil {
			return nil, sleepErr
		}
		if onRetry != nil {
			if rErr := onRetry(status); rErr != nil {
				return nil, rErr
			}
		}
	}
}
