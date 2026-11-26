package llmguard

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"nori/internal/modelkeys"
)

type restoredBody struct {
	io.Reader
	io.Closer
}

// ModelAccessError classifies a provider's permanent credential or billing failures.
// It preserves the response body so callers can still handle other errors.
func ModelAccessError(res *http.Response, label string) error {
	if res == nil {
		return nil
	}
	terminal := res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusPaymentRequired
	if !terminal && (res.StatusCode == http.StatusTooManyRequests || res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusNotFound) && res.Body != nil {
		body := res.Body
		data, _ := io.ReadAll(io.LimitReader(body, 32<<10))
		res.Body = restoredBody{Reader: io.MultiReader(bytes.NewReader(data), body), Closer: body}
		message := strings.ToLower(string(data))
		for _, marker := range []string{"insufficient_quota", "insufficient credits", "insufficient_credits", "insufficient funds", "credit balance is too low", "billing_hard_limit", "billing hard limit", "billing quota", "exceeded your current quota", "payment required", "billing_not_active", "api key not valid", "invalid api key", "api_key_invalid", "model_not_found"} {
			if strings.Contains(message, marker) {
				terminal = true
				break
			}
		}
	}
	if !terminal {
		return nil
	}
	return fmt.Errorf("%w Check your %s API key and billing in More → Models, or choose another model.", modelkeys.ErrModelAccess, label)
}
