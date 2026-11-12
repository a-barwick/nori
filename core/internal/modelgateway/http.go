// Package modelgateway handles errors from the deployment-owned model endpoint.
package modelgateway

import (
	"fmt"
	"net/http"
)

const Provider = "gateway"

// HTTPError carries the status and a repair hint. Provider bodies and headers
// may contain secrets or account details and are excluded from diagnostics.
type HTTPError int

func (e HTTPError) HTTPStatusCode() int { return int(e) }

func (e HTTPError) Error() string {
	switch int(e) {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("model gateway rejected access (HTTP %d). Check NORI_MODEL_GATEWAY_API_KEY and model access", e)
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
		return fmt.Sprintf("model gateway rejected the request (HTTP %d). Check the API base path, model alias and OpenAI API compatibility", e)
	case http.StatusTooManyRequests:
		return fmt.Sprintf("model gateway rate limit reached (HTTP %d). Check the deployment quota", e)
	default:
		return fmt.Sprintf("model gateway returned HTTP %d. Check NORI_MODEL_GATEWAY_URL and gateway availability", e)
	}
}
