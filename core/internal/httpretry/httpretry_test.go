package httpretry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func makeResp(status int, headers map[string]string) *http.Response {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(nil)),
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	if d, ok := ParseRetryAfter("5", now, 15*time.Second); !ok || d != 5*time.Second {
		t.Fatalf("delta 5s: got (%v, %v)", d, ok)
	}
	if d, ok := ParseRetryAfter("30", now, 15*time.Second); !ok || d != 15*time.Second {
		t.Fatalf("capped delta 30s: got (%v, %v)", d, ok)
	}
	if d, ok := ParseRetryAfter("-1", now, 15*time.Second); !ok || d != 0 {
		t.Fatalf("negative delta: got (%v, %v)", d, ok)
	}
	futureDate := now.Add(7 * time.Second).Format(http.TimeFormat)
	if d, ok := ParseRetryAfter(futureDate, now, 15*time.Second); !ok || d != 7*time.Second {
		t.Fatalf("HTTP-date 7s: got (%v, %v)", d, ok)
	}
	pastDate := now.Add(-5 * time.Second).Format(http.TimeFormat)
	if d, ok := ParseRetryAfter(pastDate, now, 15*time.Second); !ok || d != 0 {
		t.Fatalf("past HTTP-date: got (%v, %v)", d, ok)
	}
	if _, ok := ParseRetryAfter("not-a-date", now, 15*time.Second); ok {
		t.Fatalf("expected invalid Retry-After to return ok=false")
	}
}

func TestIsRetryableTransportError(t *testing.T) {
	ctx := context.Background()
	if IsRetryableTransportError(ctx, nil) {
		t.Fatal("nil error should not be retryable")
	}
	if IsRetryableTransportError(ctx, context.DeadlineExceeded) {
		t.Fatal("context.DeadlineExceeded should not be retryable")
	}
	if IsRetryableTransportError(ctx, context.Canceled) {
		t.Fatal("context.Canceled should not be retryable")
	}
	if IsRetryableTransportError(ctx, &url.Error{Op: "Post", URL: "https://example.com", Err: http.ErrUseLastResponse}) {
		t.Fatal("http.ErrUseLastResponse should not be retryable")
	}
	if !IsRetryableTransportError(ctx, errors.New("connection reset by peer")) {
		t.Fatal("generic transport error should be retryable")
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if IsRetryableTransportError(canceledCtx, errors.New("connection reset")) {
		t.Fatal("canceled context should not be retryable")
	}
}

func TestDoRetries529AndHonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	var retryStatuses []int
	var sleeps []time.Duration
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch attempts.Add(1) {
		case 1:
			return makeResp(529, map[string]string{"Retry-After": "3"}), nil
		case 2:
			return makeResp(http.StatusTooManyRequests, nil), nil
		default:
			return makeResp(http.StatusOK, nil), nil
		}
	})}

	res, err := Do(context.Background(), Config{
		Client:    client,
		BaseDelay: 2 * time.Second,
		MaxDelay:  10 * time.Second,
		Now:       func() time.Time { return now },
		Jitter:    func(d time.Duration) time.Duration { return d },
		Sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
		OnRetry: func(statusCode int) error {
			retryStatuses = append(retryStatuses, statusCode)
			return nil
		},
	}, func() (*http.Request, error) {
		return http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com", nil)
	})
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts = %d, want 3", attempts.Load())
	}
	if len(sleeps) != 2 || sleeps[0] != 3*time.Second || sleeps[1] != 4*time.Second {
		t.Fatalf("sleeps = %v, want [3s 4s]", sleeps)
	}
	if len(retryStatuses) != 2 || retryStatuses[0] != 529 || retryStatuses[1] != 429 {
		t.Fatalf("retryStatuses = %v, want [529 429]", retryStatuses)
	}
}

func TestDoStopsOnContextCancelDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		return makeResp(http.StatusServiceUnavailable, map[string]string{"Retry-After": "10"}), nil
	})}

	_, err := Do(ctx, Config{
		Client: client,
	}, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com", nil)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
