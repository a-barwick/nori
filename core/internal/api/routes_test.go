package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouteRegistrationRequiresPolicy(t *testing.T) {
	defer func() {
		const want = "missing or invalid route rule for GET /unclassified"
		if got := fmt.Sprint(recover()); got != want {
			t.Fatalf("registration panic = %q, want %q", got, want)
		}
		t.Log(want)
	}()
	newRouteMux(true, false).handle("GET /unclassified", RoutePolicy{}, nil)
}

func TestUnavailableManifestRouteRetains404(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/v1/example", nil)
	rule := RouteRule{Method: "GET", Path: "/v1/example", RoutePolicy: browserRead}
	(AccessConfig{Local: true}).serve(response, request, rule, http.NotFoundHandler())
	if response.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", response.Code)
	}
	var body apiError
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Code != "not_found" {
		t.Fatalf("expected a typed not_found response, got %s", response.Body.String())
	}
}
