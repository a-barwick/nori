package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestJSONLogHandlerGCPSeverityMapping(t *testing.T) {
	var buf bytes.Buffer
	handler := traceLogHandler{
		Handler: slog.NewJSONHandler(&buf, &slog.HandlerOptions{
			Level:       slog.LevelDebug,
			ReplaceAttr: gcpReplaceAttr,
		}),
	}
	logger := slog.New(handler)

	cases := []struct {
		name     string
		logFn    func()
		wantSev  string
		wantMsg  string
	}{
		{"debug", func() { logger.Debug("debug event") }, "DEBUG", "debug event"},
		{"info", func() { logger.Info("info event") }, "INFO", "info event"},
		{"warn", func() { logger.Warn("warn event") }, "WARNING", "warn event"},
		{"error", func() { logger.Error("error event") }, "ERROR", "error event"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			tc.logFn()
			var entry map[string]any
			if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
				t.Fatalf("unmarshal log JSON %q: %v", buf.String(), err)
			}
			if got, _ := entry["severity"].(string); got != tc.wantSev {
				t.Fatalf("severity = %q, want %q (json=%s)", got, tc.wantSev, buf.String())
			}
			if _, hasLevel := entry["level"]; hasLevel {
				t.Fatalf("unexpected top-level 'level' key in GCP JSON log: %s", buf.String())
			}
			if got, _ := entry["msg"].(string); got != tc.wantMsg {
				t.Fatalf("msg = %q, want %q", got, tc.wantMsg)
			}
		})
	}

	// Verify newJSONLogHandler emits WARNING and ERROR and preserves nested "level" attributes.
	buf.Reset()
	prodLogger := slog.New(newJSONLogHandler(&buf))
	prodLogger.Warn("nested test", slog.Group("meta", slog.String("level", "nested-value")))
	var nested map[string]any
	if err := json.Unmarshal(buf.Bytes(), &nested); err != nil {
		t.Fatalf("unmarshal nested log JSON: %v", err)
	}
	if nested["severity"] != "WARNING" {
		t.Fatalf("severity = %v, want WARNING", nested["severity"])
	}
	meta, _ := nested["meta"].(map[string]any)
	if meta["level"] != "nested-value" {
		t.Fatalf("expected nested meta.level preserved, got %v", meta)
	}
}

func TestSafeRouteAndFailedPollRouteV1Normalization(t *testing.T) {
	safeCases := []struct {
		rawPath string
		want    string
	}{
		{"/api/users/resolve", "/api/users/resolve"},
		{"/api/users/redeem", "/api/users/redeem"},
		{"/api/oauth/google-workspace/callback", "/api/oauth/google-workspace/callback"},
		{"/users/u1/v1/capabilities", "/v1/capabilities"},
		{"/users/u1/v1/snapshot", "/v1/snapshot"},
		{"/users/u1/v1/conversations/assistant", "/v1/conversations/assistant"},
		{"/users/u1/v1/conversations/assistant/messages", "/v1/conversations/assistant/messages"},
		{"/users/u1/v1/conversations/assistant/operations", "/v1/conversations/assistant/operations"},
		{"/users/u1/v1/conversations/assistant/cancel", "/v1/conversations/assistant/cancel"},
		{"/users/u1/v1/conversations/assistant/retry", "/v1/conversations/assistant/retry"},
		{"/users/u1/v1/conversations/assistant/uploads", "/v1/conversations/assistant/uploads"},
		{"/users/u1/v1/conversations/assistant/uploads/upl_12345", "/v1/conversations/assistant/uploads/{id}"},
		{"/users/u1/v1/uploads/upl_999", "/v1/uploads/{id}"},
		{"/users/u1/v1/results/res_42", "/v1/results/{id}"},
		{"/users/u1/v1/results/res_42/file", "/v1/results/{id}/file"},
		{"/users/u1/v1/work", "/v1/work"},
		{"/users/u1/v1/work/tsk_abc123", "/v1/work/{id}"},
		{"/users/u1/v1/work/tsk_abc123/operations", "/v1/work/{id}/operations"},
		{"/users/u1/v1/work/tsk_abc123/actions", "/v1/work/{id}/actions"},
		{"/users/u1/v1/connections/oauth/callback", "/v1/connections/oauth/callback"},
		{"/users/u1/v1/connections/conn_1/actions", "/v1/connections/{id}/actions"},
		{"/users/u1/v1/connections/conn_1/resources", "/v1/connections/{id}/resources"},
		{"/users/u1/v1/connections/conn_1/google-drive/picker", "/v1/connections/{id}/google-drive/picker"},
		{"/users/u1/v1/tools/google/gmail.send", "/v1/tools/{id}"},
		{"/users/u1/v1/memory/actions", "/v1/memory/actions"},
		{"/users/u1/v1/memory/search", "/v1/memory/search"},
		{"/users/u1/v1/memory/logs/2026-11-27", "/v1/memory/logs/{date}"},
		{"/users/u1/v1/memory/logs/2026-11-27/entry_9/actions", "/v1/memory/logs/{date}/{id}/actions"},
		{"/users/u1/v1/memory/working/item_7/actions", "/v1/memory/working/{id}/actions"},
		{"/users/u1/v1/memory/mem_123/actions", "/v1/memory/{id}/actions"},
		{"/users/u1/v1/automations/preview", "/v1/automations/preview"},
		{"/users/u1/v1/automations/auto_1/occurrences", "/v1/automations/{id}/occurrences"},
		{"/users/u1/v1/inbox/inb_1/actions", "/v1/inbox/{id}/actions"},
		{"/users/u1/v1/mcp/srv_1/credential", "/v1/mcp/{id}/credential"},
		{"/users/u1/v1/computer/sessions/sess_1", "/v1/computer/sessions/{id}"},
		{"/users/u1/v1/settings/reset", "/v1/settings/reset"},
		{"/users/u1/v1/settings/account/delete", "/v1/settings/account/delete"},
		{"/users/u1/v1/settings/model-keys/anthropic", "/v1/settings/model-keys/{id}"},
		{"/users/u1/v1/settings/home-connectors/pairing/approve", "/v1/settings/home-connectors/pairing/approve"},
		{"/users/u1/v1/settings/home-connectors/hc_1/test", "/v1/settings/home-connectors/{id}/test"},
		{"/users/u1/v1/library/doc_1/versions", "/v1/library/{id}/versions"},
		{"/users/u1/v1/mutations/req_1", "/v1/mutations/{id}"},
		{"/users/u1/v1/interactions/int_1/responses", "/v1/interactions/{id}/responses"},
		{"/users/u1/v1/permissions/perm_1/actions", "/v1/permissions/{id}/actions"},
		{"/users/u1/v1/assistant/summary", "/v1/assistant/summary"},
		{"/users/u1/v1/diagnostics/actions", "/v1/diagnostics/actions"},
	}
	for _, tc := range safeCases {
		got := safeRoute(normalizeUserPath(tc.rawPath))
		if got != tc.want {
			t.Errorf("safeRoute(normalizeUserPath(%q)) = %q, want %q", tc.rawPath, got, tc.want)
		}
	}

	// Streaming/poll/unknown routes must NOT produce safeRoute spans.
	nonSafe := []string{
		"/healthz",
		"/readyz",
		"/metrics",
		"/users/u1/v1/events",
		"/users/u1/v1/conversations/assistant/events",
		"/users/u1/v1/computer/sessions/sess_1/ws",
		"/users/u1/v1/unknown-endpoint/secret-id",
		"/users/u1/v1/work//actions",
	}
	for _, raw := range nonSafe {
		if got := safeRoute(normalizeUserPath(raw)); got != "" {
			t.Errorf("safeRoute(normalizeUserPath(%q)) = %q, want empty", raw, got)
		}
	}

	// Streaming routes must still get low-cardinality http.route on failure.
	pollCases := []struct {
		rawPath string
		want    string
	}{
		{"/users/u1/v1/events", "/v1/events"},
		{"/users/u1/v1/conversations/assistant/events", "/v1/conversations/assistant/events"},
		{"/users/u1/v1/computer/sessions/sess_42/ws", "/v1/computer/sessions/{id}/ws"},
		{"/users/u1/v1/unknown-endpoint/secret-id", "unknown"},
	}
	for _, tc := range pollCases {
		if got := failedPollRoute(normalizeUserPath(tc.rawPath)); got != tc.want {
			t.Errorf("failedPollRoute(normalizeUserPath(%q)) = %q, want %q", tc.rawPath, got, tc.want)
		}
	}

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if got := safeMethod(method); got != method {
			t.Errorf("safeMethod(%q) = %q, want %q", method, got, method)
		}
	}
	if got := safeMethod("PROPFIND"); got != "OTHER" {
		t.Errorf("safeMethod(PROPFIND) = %q, want OTHER", got)
	}
}

func TestHTTPHandlerEmitsOTelSpanForV1RouteWithNormalizedID(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	prevTP := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer func() {
		otel.SetTracerProvider(prevTP)
		_ = tp.Shutdown(context.Background())
	}()

	handler := HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/users/11111111-1111-4111-8111-111111111111/v1/work/tsk_secret_98765/actions", nil)
	req.Header.Set("X-Nori-Client", "web")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	sp := spans[0]
	if sp.Name() != "HTTP /v1/work/{id}/actions" {
		t.Fatalf("span name = %q, want %q", sp.Name(), "HTTP /v1/work/{id}/actions")
	}
	var gotRoute, gotMethod string
	var gotStatus int64
	for _, attr := range sp.Attributes() {
		switch string(attr.Key) {
		case "http.route":
			gotRoute = attr.Value.AsString()
		case "http.request.method":
			gotMethod = attr.Value.AsString()
		case "http.response.status_code":
			gotStatus = attr.Value.AsInt64()
		}
	}
	if gotRoute != "/v1/work/{id}/actions" {
		t.Fatalf("http.route = %q, want /v1/work/{id}/actions", gotRoute)
	}
	if strings.Contains(gotRoute, "tsk_secret_98765") || strings.Contains(gotRoute, "11111111") {
		t.Fatalf("dynamic ID leaked into http.route: %q", gotRoute)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("http.request.method = %q, want POST", gotMethod)
	}
	if gotStatus != http.StatusOK {
		t.Fatalf("http.response.status_code = %d, want 200", gotStatus)
	}
}
