package responses

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

	"nori/internal/agent"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/transcript"
)

type noopMeter struct{}

func (noopMeter) RecordUsage(context.Context, state.Usage) error { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func makeResponse(status int, headers map[string]string, body []byte) *http.Response {
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

func okResponsesPayload(answer string) []byte {
	body, _ := json.Marshal(map[string]any{
		"status": "completed",
		"model":  settings.DefaultInnerModel,
		"output": []map[string]any{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": answer},
				},
			},
		},
	})
	return body
}

func TestClientRespondRetries503AndHonorsRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

	var sleeps []time.Duration
	client := New("test-key", settings.DefaultInnerModel, noopMeter{})
	client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		n := attempts.Add(1)
		switch n {
		case 1:
			return makeResponse(http.StatusServiceUnavailable, map[string]string{"Retry-After": "4"}, nil), nil
		case 2:
			return makeResponse(http.StatusTooManyRequests, map[string]string{
				"Retry-After": now.Add(8 * time.Second).Format(http.TimeFormat),
			}, nil), nil
		default:
			return makeResponse(http.StatusOK, map[string]string{"Content-Type": "application/json"},
				okResponsesPayload("Inner model succeeded.")), nil
		}
	})}
	client.Now = func() time.Time { return now }
	client.MaxRetryAfter = 10 * time.Second
	client.Sleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}

	resp, err := client.Respond(context.Background(), agent.Request{
		Instructions:    "Be concise.",
		MaxOutputTokens: 256,
		Messages:        []transcript.Message{{Role: transcript.RoleUser, Parts: []transcript.Part{{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("Respond failed: %v", err)
	}
	if resp.Answer != "Inner model succeeded." {
		t.Fatalf("Answer = %q", resp.Answer)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts.Load())
	}
	if len(sleeps) != 2 || sleeps[0] != 4*time.Second || sleeps[1] != 8*time.Second {
		t.Fatalf("unexpected sleeps: %v", sleeps)
	}
}

func TestClientRespondStopsOnCancelAndDoesNotRetry400(t *testing.T) {
	t.Run("does not retry 400", func(t *testing.T) {
		var attempts atomic.Int32
		client := New("test-key", settings.DefaultInnerModel, noopMeter{})
		client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return makeResponse(http.StatusBadRequest, nil, nil), nil
		})}
		client.Sleep = func(context.Context, time.Duration) error {
			t.Fatalf("unexpected sleep on 400")
			return nil
		}
		_, err := client.Respond(context.Background(), agent.Request{
			Instructions:    "Be concise.",
			MaxOutputTokens: 256,
			Messages:        []transcript.Message{{Role: transcript.RoleUser, Parts: []transcript.Part{{Text: "Hi"}}}},
		})
		if err == nil {
			t.Fatalf("expected error")
		}
		if attempts.Load() != 1 {
			t.Fatalf("expected 1 attempt, got %d", attempts.Load())
		}
	})

	t.Run("respects ctx cancellation", func(t *testing.T) {
		var attempts atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		client := New("test-key", settings.DefaultInnerModel, noopMeter{})
		client.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			attempts.Add(1)
			return makeResponse(http.StatusServiceUnavailable, map[string]string{"Retry-After": "9"}, nil), nil
		})}
		client.Sleep = func(c context.Context, _ time.Duration) error {
			cancel()
			return c.Err()
		}
		_, err := client.Respond(ctx, agent.Request{
			Instructions:    "Be concise.",
			MaxOutputTokens: 256,
			Messages:        []transcript.Message{{Role: transcript.RoleUser, Parts: []transcript.Part{{Text: "Hi"}}}},
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		if attempts.Load() != 1 {
			t.Fatalf("expected 1 attempt, got %d", attempts.Load())
		}
	})
}
