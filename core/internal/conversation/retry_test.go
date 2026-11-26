package conversation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"nori/internal/httpretry"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func okChatResponseJSON(text string) []byte {
	content, _ := json.Marshal(map[string]any{"actions": []any{}, "text": text})
	body, _ := json.Marshal(map[string]any{
		"id":    "chatcmpl-test",
		"model": openAIModelName,
		"choices": []map[string]any{
			{
				"finish_reason": "stop",
				"message": map[string]any{
					"role":    "assistant",
					"content": string(content),
				},
			},
		},
	})
	return body
}

func makeHTTPResponse(status int, headers map[string]string, body []byte) *http.Response {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func TestOpenAIModelRetriesTransient503AndHonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		n := attempts.Add(1)
		switch n {
		case 1:
			return makeHTTPResponse(http.StatusServiceUnavailable, map[string]string{"Retry-After": "6"},
				[]byte(`{"error":{"type":"service_unavailable_error","code":"server_is_overloaded"}}`)), nil
		case 2:
			return makeHTTPResponse(http.StatusTooManyRequests, map[string]string{"Retry-After": "999"},
				[]byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`)), nil
		default:
			return makeHTTPResponse(http.StatusOK, map[string]string{"Content-Type": "application/json"},
				okChatResponseJSON("Recovered cleanly.")), nil
		}
	})

	var sleeps []time.Duration
	model := NewOpenAIModel("test-key", openAIModelName)
	model.Client = &http.Client{Transport: transport}
	model.MaxRetryAfter = 12 * time.Second
	model.Sleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}

	reply, err := model.Respond(context.Background(), ReplyRequest{
		Messages: []json.RawMessage{modelMessage(map[string]string{"role": "user", "content": "Hello"})},
	})
	if err != nil {
		t.Fatalf("Respond failed after retries: %v", err)
	}
	if reply.Text != "Recovered cleanly." {
		t.Fatalf("reply.Text = %q", reply.Text)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
	if len(sleeps) != 2 {
		t.Fatalf("expected 2 sleeps, got %v", sleeps)
	}
	if sleeps[0] != 6*time.Second {
		t.Fatalf("first Retry-After sleep = %v, want 6s", sleeps[0])
	}
	if sleeps[1] != 12*time.Second {
		t.Fatalf("second Retry-After sleep = %v, want capped 12s", sleeps[1])
	}
}

func TestOpenAIModelRetriesTransportErrorsAndDoesNotRetry400(t *testing.T) {
	t.Run("transient transport error retries", func(t *testing.T) {
		var attempts atomic.Int32
		model := NewOpenAIModel("test-key", openAIModelName)
		model.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if attempts.Add(1) == 1 {
				return nil, errors.New("connection reset by peer")
			}
			return makeHTTPResponse(http.StatusOK, map[string]string{"Content-Type": "application/json"},
				okChatResponseJSON("Back online.")), nil
		})}
		model.Sleep = func(context.Context, time.Duration) error { return nil }

		reply, err := model.Respond(context.Background(), ReplyRequest{
			Messages: []json.RawMessage{modelMessage(map[string]string{"role": "user", "content": "Hello"})},
		})
		if err != nil {
			t.Fatalf("expected recovery after transport error, got %v", err)
		}
		if reply.Text != "Back online." || attempts.Load() != 2 {
			t.Fatalf("reply=%q attempts=%d", reply.Text, attempts.Load())
		}
	})

	t.Run("non-retryable 400", func(t *testing.T) {
		var attempts atomic.Int32
		model := NewOpenAIModel("test-key", openAIModelName)
		model.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return makeHTTPResponse(http.StatusBadRequest, nil,
				[]byte(`{"error":{"type":"invalid_request_error","code":"bad_request"}}`)), nil
		})}
		model.Sleep = func(context.Context, time.Duration) error {
			t.Fatalf("should not sleep for non-retryable 400")
			return nil
		}
		_, err := model.Respond(context.Background(), ReplyRequest{
			Messages: []json.RawMessage{modelMessage(map[string]string{"role": "user", "content": "Hello"})},
		})
		if err == nil {
			t.Fatalf("expected error on 400")
		}
		if attempts.Load() != 1 {
			t.Fatalf("expected 1 attempt on 400, got %d", attempts.Load())
		}
	})

	t.Run("cancellation during retry wait", func(t *testing.T) {
		var attempts atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		model := NewOpenAIModel("test-key", openAIModelName)
		model.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return makeHTTPResponse(http.StatusServiceUnavailable, map[string]string{"Retry-After": "10"}, nil), nil
		})}
		model.Sleep = func(c context.Context, _ time.Duration) error {
			cancel()
			return c.Err()
		}
		_, err := model.Respond(ctx, ReplyRequest{
			Messages: []json.RawMessage{modelMessage(map[string]string{"role": "user", "content": "Hello"})},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if attempts.Load() != 1 {
			t.Fatalf("expected 1 attempt before cancel, got %d", attempts.Load())
		}
	})
}

func TestParseRetryAfterHTTPDateAndDelta(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	if d, ok := httpretry.ParseRetryAfter("5", now, 15*time.Second); !ok || d != 5*time.Second {
		t.Fatalf("delta 5s: got (%v, %v)", d, ok)
	}
	if d, ok := httpretry.ParseRetryAfter("30", now, 15*time.Second); !ok || d != 15*time.Second {
		t.Fatalf("capped delta 30s: got (%v, %v)", d, ok)
	}
	futureDate := now.Add(7 * time.Second).Format(http.TimeFormat)
	if d, ok := httpretry.ParseRetryAfter(futureDate, now, 15*time.Second); !ok || d != 7*time.Second {
		t.Fatalf("HTTP-date 7s: got (%v, %v)", d, ok)
	}
	pastDate := now.Add(-5 * time.Second).Format(http.TimeFormat)
	if d, ok := httpretry.ParseRetryAfter(pastDate, now, 15*time.Second); !ok || d != 0 {
		t.Fatalf("past HTTP-date: got (%v, %v)", d, ok)
	}
	if _, ok := httpretry.ParseRetryAfter("not-a-date", now, 15*time.Second); ok {
		t.Fatalf("expected invalid Retry-After to return ok=false")
	}
}
