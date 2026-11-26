package responses

import (
	"context"
	"net/http"

	"nori/internal/httpretry"
	"nori/internal/llmguard"
)

func (c *Client) doWithRetry(ctx context.Context, buildReq func() (*http.Request, error), onRetry func(statusCode int) error) (*http.Response, error) {
	return httpretry.Do(ctx, httpretry.Config{
		Client:     c.httpClient(),
		MaxRetries: c.MaxRetries,
		BaseDelay:  c.RetryBaseDelay,
		MaxDelay:   c.MaxRetryAfter,
		Now:        c.Now,
		Sleep:      c.Sleep,
		ShouldStopRetry: func(res *http.Response) bool {
			return c.BYOKProviderLabel != "" && llmguard.ModelAccessError(res, c.BYOKProviderLabel) != nil
		},
		OnRetry:   onRetry,
		LogPrefix: "inner model",
	}, buildReq)
}
