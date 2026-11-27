package homeai

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIPRateLimiter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lim := newIPRateLimiter()
	lim.now = func() time.Time { return now }

	h := lim.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// 60 requests from the same IPv6 /64 succeed, 61st is throttled.
	for i := range 60 {
		req := httptest.NewRequest(http.MethodPost, "https://relay.noribot.chat/oauth/token", nil)
		req.Header.Set("X-Forwarded-For", "2001:db8:1:2::1")
		if i%2 == 1 {
			req.Header.Set("X-Forwarded-For", "2001:db8:1:2::dead:beef")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d got %d, want 200", i+1, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "https://relay.noribot.chat/oauth/token", nil)
	req.Header.Set("X-Forwarded-For", "2001:db8:1:2::99")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("61st request got %d, want 429", rec.Code)
	}

	// Health checks bypass the limiter.
	for _, path := range []string{"/healthz", "/readyz"} {
		hreq := httptest.NewRequest(http.MethodGet, "https://relay.noribot.chat"+path, nil)
		hreq.Header.Set("X-Forwarded-For", "2001:db8:1:2::99")
		hrec := httptest.NewRecorder()
		h.ServeHTTP(hrec, hreq)
		if hrec.Code != http.StatusOK {
			t.Fatalf("%s got %d, want 200", path, hrec.Code)
		}
	}

	// Another IP is unaffected.
	other := httptest.NewRequest(http.MethodPost, "https://relay.noribot.chat/oauth/token", nil)
	other.Header.Set("X-Forwarded-For", "198.51.100.7")
	orec := httptest.NewRecorder()
	h.ServeHTTP(orec, other)
	if orec.Code != http.StatusOK {
		t.Fatalf("other IP got %d, want 200", orec.Code)
	}
}
