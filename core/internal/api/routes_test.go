package api

import (
	"fmt"
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
