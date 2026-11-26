package conversation

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"nori/internal/httpretry"
	"nori/internal/llmguard"
)

func (m *OpenAIModel) doWithRetry(ctx context.Context, endpoint string, body []byte, onRetry func(statusCode int) error) (*http.Response, error) {
	return httpretry.Do(ctx, httpretry.Config{
		Client:     m.httpClient(),
		MaxRetries: m.MaxRetries,
		BaseDelay:  m.RetryBaseDelay,
		MaxDelay:   m.MaxRetryAfter,
		Now:        m.now,
		Sleep:      m.Sleep,
		ShouldStopRetry: func(res *http.Response) bool {
			return m.BYOKProviderLabel != "" && llmguard.ModelAccessError(res, m.BYOKProviderLabel) != nil
		},
		DrainBody: func(ctx context.Context, res *http.Response) {
			if m.gatewayURL == "" {
				_ = logRefusal(ctx, res)
			} else {
				_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 16<<10))
			}
		},
		OnRetry:   onRetry,
		LogPrefix: "outer model",
	}, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+m.key)
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}
