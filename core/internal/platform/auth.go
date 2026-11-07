package platform

import (
	"fmt"
	"net/http"
	"nori/internal/settings"
)

// ServiceHandler protects trusted internal routes outside GCP IAM. Local callers
// are accepted only by the entrypoints that bind exclusively to loopback.
func ServiceHandler(s settings.Settings, h http.Handler, local bool) (http.Handler, error) {
	if local || s.Cloud == "" || s.Cloud == "gcp" {
		return h, nil
	}
	if s.Cloud == "local" {
		return nil, fmt.Errorf("NORI_CLOUD=local requires the loopback nori-local entrypoint")
	}
	if err := s.ServiceAuth.Validate(); err != nil {
		return nil, err
	}
	return s.ServiceAuth.Protect(h), nil
}
