package platform

import (
	"fmt"
	"net/http"

	"nori/internal/httpserver"
	"nori/internal/settings"
)

func ServiceHandler(s settings.Settings, h http.Handler, local bool) (http.Handler, error) {
	if local {
		return httpserver.RequireLoopbackHost(h), nil
	}
	if s.Cloud == "local" {
		return nil, fmt.Errorf("NORI_CLOUD=local requires the loopback nori-local entrypoint")
	}
	if err := s.ServiceAuth.Validate(); err != nil {
		return nil, err
	}
	return s.ServiceAuth.Protect(h), nil
}
