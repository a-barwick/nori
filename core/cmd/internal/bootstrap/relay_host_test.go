package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWithRelayHost(t *testing.T) {
	shutdownCtx, cancelShutdown := context.WithCancel(context.Background())
	defer cancelShutdown()

	var relayCtx context.Context
	relay := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayCtx = r.Context()
		w.Header().Set("X-Handler", "relay")
		w.WriteHeader(http.StatusOK)
	})
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Handler", "api")
		w.WriteHeader(http.StatusUnauthorized)
	})

	h := withRelayHost(shutdownCtx, "relay.noribot.chat", relay, fallback)

	// Matching Host (with port and mixed case) routes to relay ahead of fallback.
	req := httptest.NewRequest(http.MethodGet, "https://relay.noribot.chat/readyz", nil)
	req.Host = "Relay.Noribot.Chat:443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("X-Handler") != "relay" || rec.Code != http.StatusOK {
		t.Fatalf("relay host got handler=%q code=%d, want relay/200", rec.Header().Get("X-Handler"), rec.Code)
	}
	if relayCtx == nil {
		t.Fatal("expected relay context to be captured")
	}

	// Other Host routes to fallback (Protect).
	apiReq := httptest.NewRequest(http.MethodGet, "https://nori-core.run.app/v1/users", nil)
	apiReq.Host = "nori-core.run.app"
	apiRec := httptest.NewRecorder()
	h.ServeHTTP(apiRec, apiReq)
	if apiRec.Header().Get("X-Handler") != "api" || apiRec.Code != http.StatusUnauthorized {
		t.Fatalf("api host got handler=%q code=%d, want api/401", apiRec.Header().Get("X-Handler"), apiRec.Code)
	}

	// Shutdown cancels in-flight relay request context.
	inFlight := make(chan struct{})
	done := make(chan error, 1)
	blockingRelay := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(inFlight)
		<-r.Context().Done()
		done <- r.Context().Err()
	})
	h2 := withRelayHost(shutdownCtx, "relay.noribot.chat", blockingRelay, fallback)
	go func() {
		r := httptest.NewRequest(http.MethodGet, "https://relay.noribot.chat/v1/connector/socket", nil)
		r.Host = "relay.noribot.chat"
		h2.ServeHTTP(httptest.NewRecorder(), r)
	}()
	<-inFlight
	cancelShutdown()
	if err := <-done; err != context.Canceled {
		t.Fatalf("shutdown err = %v, want context.Canceled", err)
	}
}
