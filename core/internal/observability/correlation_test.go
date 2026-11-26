package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getsentry/sentry-go"
	"go.opentelemetry.io/otel/attribute"
)

func TestExtractUserPathAndClientPlatform(t *testing.T) {
	if got := normalizeUserPath("/users/owner/api/turn"); got != "/api/turn" {
		t.Fatalf("got normalized path %q, want /api/turn", got)
	}
	if got := normalizeUserPath("/readyz"); got != "/readyz" {
		t.Fatalf("got normalized path %q, want /readyz", got)
	}

	if got := safeClientPlatform("iOS"); got != "ios" {
		t.Fatalf("got %q, want ios", got)
	}
	if got := safeClientPlatform("web"); got != "web" {
		t.Fatalf("got %q, want web", got)
	}
	if got := safeClientPlatform("malicious-header"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestScrubEventPreservesUserIDAndCorrelationTags(t *testing.T) {
	event := sentry.NewEvent()
	event.User = sentry.User{
		ID:       "11111111-1111-4111-8111-111111111111",
		Email:    "secret@example.com",
		Username: "secretuser",
	}
	event.Tags = map[string]string{
		"nori.error.category":  "task.failed",
		"nori.error.reason":    "other",
		"nori.user.id":         "11111111-1111-4111-8111-111111111111",
		"nori.task.id":         "tsk_999",
		"nori.client.platform": "ios",
		"unallowed.tag":        "drop-me",
	}

	scrubbed := scrubEvent(event)
	if scrubbed.User.ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("expected User.ID preserved, got %q", scrubbed.User.ID)
	}
	if scrubbed.User.Email != "" || scrubbed.User.Username != "" {
		t.Fatalf("expected email/username stripped, got %+v", scrubbed.User)
	}
	if scrubbed.Tags["nori.user.id"] != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("expected nori.user.id preserved, got %q", scrubbed.Tags["nori.user.id"])
	}
	if scrubbed.Tags["nori.task.id"] != "tsk_999" {
		t.Fatalf("expected nori.task.id preserved, got %q", scrubbed.Tags["nori.task.id"])
	}
	if scrubbed.Tags["nori.client.platform"] != "ios" {
		t.Fatalf("expected nori.client.platform preserved, got %q", scrubbed.Tags["nori.client.platform"])
	}
	if _, exists := scrubbed.Tags["unallowed.tag"]; exists {
		t.Fatalf("expected unallowed.tag stripped")
	}
}

func TestHTTPHandlerUsesOnlyAuthorizedUserID(t *testing.T) {
	handler := HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if got := len(correlationAttributes(ctx)); got != 1 {
			t.Fatalf("expected only the client platform before authorization, got %v", correlationAttributes(ctx))
		}
		BindHTTPUser(ctx, "owner")
		attrs := correlationAttributes(ctx)
		if len(attrs) != 2 || attrs[0] != attribute.String("nori.client.platform", "ios") ||
			attrs[1] != attribute.String("nori.user.id", "owner") {
			t.Fatalf("expected correlation with the authorized user, got %v", attrs)
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/users/untrusted/api/turn", nil)
	req.Header.Set("X-Nori-Client", "ios")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if _, ok := allowedAttribute(attribute.String("nori.user.id", "someone@example.com"), false); ok {
		t.Fatal("expected a non-directory user ID to be dropped")
	}
	if _, ok := allowedAttribute(attribute.String("nori.client.platform", "arbitrary"), false); ok {
		t.Fatal("expected an unknown platform to be dropped")
	}
}
