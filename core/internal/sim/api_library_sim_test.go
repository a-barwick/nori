package sim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"nori/internal/api"
	"nori/internal/connections"
	"nori/internal/conversation"
	"nori/internal/observability"
	"nori/internal/permissions"
	"nori/internal/secret"
	"nori/internal/serviceauth"
	"nori/internal/settings"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

type prefixedArtifacts struct {
	files  *memstore.Artifacts
	pngs   *memstore.Artifacts
	prefix string
}

func (p prefixedArtifacts) PutPNG(ctx context.Context, run, id string, data []byte) error {
	return p.pngs.PutPNG(ctx, run, id, data)
}

func (p prefixedArtifacts) GetPNG(ctx context.Context, run, id string) ([]byte, error) {
	return p.pngs.GetPNG(ctx, run, id)
}

func (p prefixedArtifacts) PutFile(ctx context.Context, key, mediaType string, data []byte) error {
	return p.files.PutFile(ctx, p.prefix+key, mediaType, data)
}

func (p prefixedArtifacts) GetFile(ctx context.Context, key string) ([]byte, error) {
	return p.files.GetFile(ctx, p.prefix+key)
}

func (p prefixedArtifacts) DeleteFile(ctx context.Context, key string) error {
	return p.files.DeleteFile(ctx, p.prefix+key)
}

func (p prefixedArtifacts) Close() error { return nil }

type simOps struct {
	resumeCalls  atomic.Int64
	processCalls atomic.Int64
}

func (o *simOps) Process(context.Context) error {
	o.processCalls.Add(1)
	return nil
}

func (o *simOps) Resume(context.Context) error {
	o.resumeCalls.Add(1)
	return nil
}

func (o *simOps) RepairWake(context.Context) error {
	return nil
}

type simUserEnv struct {
	user      users.User
	store     state.ConversationStore
	conns     *connections.Service
	perms     *permissions.Service
	assistant *api.Assistant
	ops       *simOps
}

type simHTTPWorld struct {
	backend   *memstore.Backend
	artifacts *memstore.Artifacts
	directory *records.Users
	auth      serviceauth.Config
	users     map[string]*simUserEnv
	handler   http.Handler
}

func newSimHTTPWorld(t *testing.T, configure func(u users.User, env *simUserEnv)) *simHTTPWorld {
	t.Helper()
	backend := memstore.New()
	artifacts := memstore.NewArtifacts()
	directory := records.NewSharedUsers(backend)
	authCfg := serviceauth.Config{
		Key:      "0123456789abcdef0123456789abcdef",
		Audience: "nori-sim",
	}
	w := &simHTTPWorld{
		backend:   backend,
		artifacts: artifacts,
		directory: directory,
		auth:      authCfg,
		users:     map[string]*simUserEnv{},
	}
	alice := users.User{
		ID:          "11111111-1111-4111-8111-111111111111",
		Email:       "alice@example.com",
		DisplayName: "Alice",
		TimeZone:    "UTC",
	}
	bob := users.User{
		ID:          "22222222-2222-4222-8222-222222222222",
		Email:       "bob@example.com",
		DisplayName: "Bob",
		TimeZone:    "America/Los_Angeles",
	}
	for _, u := range []users.User{alice, bob} {
		if err := directory.Create(context.Background(), u); err != nil {
			t.Fatalf("create user %s: %v", u.ID, err)
		}
		store := state.Compose(records.NewShared(backend, u, 5.0), prefixedArtifacts{files: artifacts, pngs: memstore.NewArtifacts(), prefix: u.FilePrefix()})
		conn := connections.New(store, secret.Unavailable{}, "http://127.0.0.1:3211/api/v1/connections/oauth/callback")
		conn.UserTimezone = u.TimeZone
		perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow"}
		ops := &simOps{}
		assistant := &api.Assistant{
			Store:       store,
			Connections: conn,
			Permissions: perms,
			Features: map[string]bool{
				"library": true, "search": true, "work": true, "permissions": true, "interactions": true,
			},
		}
		env := &simUserEnv{
			user:      u,
			store:     store,
			conns:     conn,
			perms:     perms,
			assistant: assistant,
			ops:       ops,
		}
		if configure != nil {
			configure(u, env)
		}
		w.users[u.ID] = env
	}
	open := func(_ context.Context, u users.User) (api.UserRoutes, func(), error) {
		env := w.users[u.ID]
		tasks := &conversation.Tasks{Store: env.store}
		h := api.Handler(api.Deps{
			Model:     "sim-model",
			Tasks:     tasks,
			Ops:       env.ops,
			Assistant: env.assistant,
		})
		return api.UserRoutes{Handler: h, Resume: env.ops.Resume}, func() {}, nil
	}
	userHandler := api.UserHandler(directory, open, false, "sim-model")
	w.handler = observability.HTTPHandler(authCfg.Protect(userHandler))
	return w
}

func (w *simHTTPWorld) token(t *testing.T) string {
	t.Helper()
	tok, err := w.auth.Token()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return tok
}

func (w *simHTTPWorld) do(t *testing.T, method, path, token, contentType string, body []byte, extraHeaders map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, req)
	return rec
}

func seedRun(t *testing.T, store state.ConversationStore, runID, request string) state.Run {
	t.Helper()
	ctx := context.Background()
	receipts, err := store.AcceptCommandBatch(ctx, []state.Command{{
		ID:    uuid.NewString(),
		RunID: runID,
		Kind:  state.CommandStart,
		Text:  request,
	}}, nil)
	if err != nil {
		t.Fatalf("seed run %s: %v", runID, err)
	}
	return receipts[0].Run
}

type recordingSpanProcessor struct {
	mu      sync.Mutex
	ended   []sdktrace.ReadOnlySpan
	flushes atomic.Int64
}

func (p *recordingSpanProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (p *recordingSpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	p.mu.Lock()
	p.ended = append(p.ended, s)
	p.mu.Unlock()
}

func (p *recordingSpanProcessor) Shutdown(context.Context) error { return nil }

func (p *recordingSpanProcessor) ForceFlush(context.Context) error {
	p.flushes.Add(1)
	return nil
}

func (p *recordingSpanProcessor) spans() []sdktrace.ReadOnlySpan {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]sdktrace.ReadOnlySpan(nil), p.ended...)
}

func TestAPIMultiTenantIsolationAndAuthMiddleware(t *testing.T) {
	ctx := context.Background()
	stop := observability.Init(ctx, "nori-sim", settings.Settings{Cloud: "local"})
	defer func() { _ = stop(ctx) }()

	flushProc := &recordingSpanProcessor{}
	if tp, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		tp.RegisterSpanProcessor(flushProc)
	}
	spanProc := &recordingSpanProcessor{}
	testTP := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())),
		sdktrace.WithSpanProcessor(spanProc),
	)
	prevTP := otel.GetTracerProvider()
	otel.SetTracerProvider(testTP)
	defer otel.SetTracerProvider(prevTP)

	w := newSimHTTPWorld(t, nil)
	aliceID := "11111111-1111-4111-8111-111111111111"
	bobID := "22222222-2222-4222-8222-222222222222"
	aliceEnv := w.users[aliceID]
	bobEnv := w.users[bobID]

	bobRunID := uuid.NewString()
	seedRun(t, bobEnv.store, bobRunID, "Bob secret strategy")
	bobResultID := uuid.NewString()
	if err := bobEnv.store.PutFile(ctx, state.ResultKey(bobResultID), "text/plain", []byte("bob private payload")); err != nil {
		t.Fatalf("put bob file: %v", err)
	}
	if _, err := bobEnv.store.SaveResult(ctx, state.Result{
		ID: bobResultID, RootID: bobResultID, Version: 1, RunID: bobRunID, Revision: 1,
		Kind: state.ResultFile, Title: "Bob Confidential", Text: "bob-unique-secret-term",
		Name: "secret.txt", MediaType: "text/plain", Size: 19, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save bob result: %v", err)
	}

	bobUploadID := uuid.NewString()
	tok := w.token(t)
	upRec := w.do(t, "POST", "/users/"+bobID+"/v1/uploads?id="+bobUploadID+"&filename=bob.txt", tok, "text/plain", []byte("bob upload bytes"), nil)
	if upRec.Code != http.StatusOK {
		t.Fatalf("bob upload expected 200, got %d: %s", upRec.Code, upRec.Body.String())
	}

	aliceGetRes := w.do(t, "GET", "/users/"+aliceID+"/v1/library/"+bobResultID, tok, "", nil, nil)
	if aliceGetRes.Code != http.StatusNotFound {
		t.Fatalf("alice reading bob library item expected 404, got %d", aliceGetRes.Code)
	}
	aliceDownloadRes := w.do(t, "GET", "/users/"+aliceID+"/v1/library/"+bobResultID+"/file", tok, "", nil, nil)
	if aliceDownloadRes.Code != http.StatusNotFound {
		t.Fatalf("alice downloading bob library file expected 404, got %d", aliceDownloadRes.Code)
	}
	aliceDownloadUpload := w.do(t, "GET", "/users/"+aliceID+"/v1/uploads/"+bobUploadID, tok, "", nil, nil)
	if aliceDownloadUpload.Code != http.StatusNotFound {
		t.Fatalf("alice downloading bob upload expected 404, got %d", aliceDownloadUpload.Code)
	}
	delPayload, _ := json.Marshal(map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value":             map[string]any{"action": "delete", "scope": "version"},
	})
	aliceDeleteBob := w.do(t, "POST", "/users/"+aliceID+"/v1/library/"+bobResultID+"/actions", tok, "application/json", delPayload, nil)
	if aliceDeleteBob.Code != http.StatusNotFound {
		t.Fatalf("alice deleting bob library item expected 404, got %d", aliceDeleteBob.Code)
	}
	aliceSearch := w.do(t, "GET", "/users/"+aliceID+"/v1/search?q=bob-unique-secret-term", tok, "", nil, nil)
	if aliceSearch.Code != http.StatusOK {
		t.Fatalf("alice search expected 200, got %d", aliceSearch.Code)
	}
	var searchOut struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(aliceSearch.Body.Bytes(), &searchOut); err != nil {
		t.Fatalf("decode alice search: %v", err)
	}
	if len(searchOut.Items) != 0 {
		t.Fatalf("expected 0 search hits for alice, got %d", len(searchOut.Items))
	}

	bobDownload := w.do(t, "GET", "/users/"+bobID+"/v1/library/"+bobResultID+"/file", tok, "", nil, nil)
	if bobDownload.Code != http.StatusOK || bobDownload.Body.String() != "bob private payload" {
		t.Fatalf("bob downloading own file failed: %d %q", bobDownload.Code, bobDownload.Body.String())
	}

	rawReq := httptest.NewRequest("GET", "/users/"+bobID+"/v1/library/"+bobResultID, nil)
	rawReq.URL.RawPath = "/users/" + bobID + "/v1/library/" + bobResultID
	rawReq.Header.Set("Authorization", "Bearer "+tok)
	rawRec := httptest.NewRecorder()
	w.handler.ServeHTTP(rawRec, rawReq)
	if rawRec.Code != http.StatusOK {
		t.Fatalf("request with RawPath expected 200, got %d: %s", rawRec.Code, rawRec.Body.String())
	}

	emptyUserResume := w.do(t, "POST", "/users//api/resume", tok, "application/json", []byte(`{}`), nil)
	if emptyUserResume.Code != http.StatusNotFound {
		t.Fatalf("POST /users//api/resume expected 404, got %d: %s", emptyUserResume.Code, emptyUserResume.Body.String())
	}
	if aliceEnv.ops.resumeCalls.Load() != 0 || bobEnv.ops.resumeCalls.Load() != 0 {
		t.Fatalf("empty user resume unexpectedly called user Resume")
	}

	traceHeader := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	flushesBefore := flushProc.flushes.Load()
	unauthRec := w.do(t, "POST", "/users/"+aliceID+"/api/resume", "", "application/json", []byte(`{}`), map[string]string{
		"traceparent": traceHeader,
	})
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request expected 401, got %d", unauthRec.Code)
	}
	if flushProc.flushes.Load() != flushesBefore {
		t.Fatalf("unauthenticated 401 triggered trace flush: before=%d after=%d", flushesBefore, flushProc.flushes.Load())
	}
	if len(spanProc.spans()) != 0 {
		t.Fatalf("unauthenticated request with remote traceparent=01 should not be force-sampled, got %d spans", len(spanProc.spans()))
	}

	badAuthRec := w.do(t, "POST", "/users/"+aliceID+"/api/resume", "not-a-valid-jwt", "application/json", []byte(`{}`), map[string]string{
		"traceparent": traceHeader,
	})
	if badAuthRec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer token expected 401, got %d", badAuthRec.Code)
	}
	if flushProc.flushes.Load() != flushesBefore {
		t.Fatalf("invalid bearer 401 triggered trace flush: before=%d after=%d", flushesBefore, flushProc.flushes.Load())
	}

	now := time.Now()
	wrongAlgTok, err := jwt.NewWithClaims(jwt.SigningMethodHS384, jwt.RegisteredClaims{
		Issuer:    "nori-service",
		Audience:  jwt.ClaimStrings{w.auth.Audience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Second)),
	}).SignedString([]byte(w.auth.Key))
	if err != nil {
		t.Fatalf("sign wrong alg: %v", err)
	}
	wrongAudTok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    "nori-service",
		Audience:  jwt.ClaimStrings{"wrong-audience"},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Second)),
	}).SignedString([]byte(w.auth.Key))
	if err != nil {
		t.Fatalf("sign wrong aud: %v", err)
	}
	expiredTok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    "nori-service",
		Audience:  jwt.ClaimStrings{w.auth.Audience},
		IssuedAt:  jwt.NewNumericDate(now.Add(-2 * time.Minute)),
		ExpiresAt: jwt.NewNumericDate(now.Add(-time.Minute)),
	}).SignedString([]byte(w.auth.Key))
	if err != nil {
		t.Fatalf("sign expired: %v", err)
	}
	for name, badTok := range map[string]string{
		"wrong_alg": wrongAlgTok,
		"wrong_aud": wrongAudTok,
		"expired":   expiredTok,
	} {
		rec := w.do(t, "POST", "/users/"+aliceID+"/api/resume", badTok, "application/json", []byte(`{}`), nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s token expected 401, got %d", name, rec.Code)
		}
		if flushProc.flushes.Load() != flushesBefore {
			t.Fatalf("%s 401 triggered trace flush", name)
		}
	}

	authResume := w.do(t, "POST", "/users/"+aliceID+"/api/resume", tok, "application/json", []byte(`{}`), map[string]string{
		"traceparent":   traceHeader,
		"X-Nori-Client": "ios",
	})
	if authResume.Code != http.StatusOK {
		t.Fatalf("authenticated resume expected 200, got %d: %s", authResume.Code, authResume.Body.String())
	}
	if flushProc.flushes.Load() <= flushesBefore {
		t.Fatalf("authenticated safeRoute request expected trace flush")
	}
	recorded := spanProc.spans()
	var matchedSpan sdktrace.ReadOnlySpan
	for _, sp := range recorded {
		if sp.Name() == "HTTP /api/resume" {
			matchedSpan = sp
		}
	}
	if matchedSpan == nil {
		t.Fatalf("expected HTTP /api/resume span for /users/<id>/api/resume, got %d spans", len(recorded))
	}
	if matchedSpan.SpanContext().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("expected propagated trace ID, got %s", matchedSpan.SpanContext().TraceID().String())
	}
	foundRoute := false
	for _, attr := range matchedSpan.Attributes() {
		if string(attr.Key) == "http.route" {
			if attr.Value.AsString() != "/api/resume" {
				t.Fatalf("expected http.route=/api/resume, got %q", attr.Value.AsString())
			}
			if strings.Contains(attr.Value.AsString(), aliceID) {
				t.Fatalf("user ID leaked into http.route: %q", attr.Value.AsString())
			}
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Fatalf("expected http.route attribute on span")
	}
	tags := make(map[string]string)
	for _, attr := range matchedSpan.Attributes() {
		tags[string(attr.Key)] = attr.Value.AsString()
	}
	if tags["nori.user.id"] != aliceID || tags["nori.client.platform"] != "ios" {
		t.Fatalf("expected authorized iOS correlation on HTTP span, got %v", tags)
	}
}

func TestObservabilityRedactionAndSessionEnv(t *testing.T) {
	ctx := context.Background()
	stopAWS := observability.Init(ctx, "nori-sim", settings.Settings{
		Cloud: "aws",
		Observability: settings.Observability{
			CaptureContent: true,
		},
	})
	defer func() { _ = stopAWS(ctx) }()

	if got := observability.SessionID(); got != "nori-production-conversation" {
		t.Fatalf("expected nori-production-conversation for NORI_CLOUD=aws, got %q", got)
	}

	dynamicSecret := "DYNAMIC_SECRET_TOKEN_9876543210"
	observability.RegisterCredentials(dynamicSecret)

	boundaryPrefix := strings.Repeat("a", 8186)
	awsBoundary := boundaryPrefix + "AKIAIOSFODNN7EXAMPLE" + " tail"
	redactedAWS := observability.RedactText(awsBoundary, 8192)
	if strings.Contains(redactedAWS, "AKIA") || strings.Contains(redactedAWS, "EXAMPLE") {
		t.Fatalf("AWS key across 8192-byte boundary leaked: %q", redactedAWS[len(redactedAWS)-32:])
	}

	credBoundary := boundaryPrefix + dynamicSecret + " end"
	redactedCred := observability.RedactText(credBoundary, 8192)
	if strings.Contains(redactedCred, "DYNAMIC_") || strings.Contains(redactedCred, "9876543210") {
		t.Fatalf("registered credential across 8192-byte boundary leaked: %q", redactedCred[len(redactedCred)-32:])
	}

	collapsible := strings.Repeat("https://example.com/very-long-url-segment-1234567890 ", 160) + "AKIAIOSFODNN7EXAMPLE " + dynamicSecret
	if len(collapsible) <= 8192 {
		t.Fatalf("expected collapsible test payload > 8192 bytes, got %d", len(collapsible))
	}
	attr, ok := observability.ContentAttribute("input.value", collapsible)
	if !ok {
		t.Fatalf("expected ContentAttribute to return ok=true when CaptureContent is enabled")
	}
	if strings.Contains(attr.Value.AsString(), "AKIA") || strings.Contains(attr.Value.AsString(), "DYNAMIC_") {
		t.Fatalf("ContentAttribute leaked secret past 8192 bytes: %q", attr.Value.AsString())
	}

	cases := []struct {
		name   string
		input  string
		secret string
	}{
		{name: "postgresql", input: "db=postgresql://nori:supersecret@db.internal:5432/nori?sslmode=require ok", secret: "supersecret"},
		{name: "postgres", input: "db=postgres://nori:pgpass123@db.internal:5432/nori ok", secret: "pgpass123"},
		{name: "redis", input: "cache=redis://:redissecret@cache.internal:6379/0 ok", secret: "redissecret"},
		{name: "amqp", input: "mq=amqp://user:mqsecret@broker.internal:5672/vhost ok", secret: "mqsecret"},
		{name: "aws_akia", input: "key AKIAIOSFODNN7EXAMPLE done", secret: "AKIAIOSFODNN7EXAMPLE"},
		{name: "aws_asia", input: "key ASIAIOSFODNN7EXAMPLE done", secret: "ASIAIOSFODNN7EXAMPLE"},
		{name: "github_ghp", input: "tok ghp_1234567890abcdef1234567890abcdef1234 done", secret: "ghp_1234567890abcdef"},
		{name: "github_pat", input: "tok github_pat_11ABCDEFG_1234567890abcdef123456 done", secret: "github_pat_11ABCDEFG"},
		{name: "slack_xoxb", input: "tok xoxb-1234567890-1234567890123-abcdefghijklmnop done", secret: "xoxb-1234567890"},
		{name: "registered", input: "loaded " + dynamicSecret + " done", secret: dynamicSecret},
	}
	for _, tc := range cases {
		out := observability.RedactText(tc.input, 512)
		if strings.Contains(out, tc.secret) {
			t.Fatalf("%s: secret %q leaked in %q", tc.name, tc.secret, out)
		}
		if !strings.Contains(out, "[redacted]") {
			t.Fatalf("%s: expected [redacted] marker in %q", tc.name, out)
		}
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(n int) {
			defer wg.Done()
			observability.RegisterCredentials("CONCURRENT_SECRET_VALUE_" + uuid.NewString())
		}(i)
		go func() {
			defer wg.Done()
			_ = observability.SessionID()
			_ = observability.RedactText("checking "+dynamicSecret, 256)
		}()
	}
	wg.Wait()

	stopLocal := observability.Init(ctx, "nori-sim", settings.Settings{Cloud: "local"})
	defer func() { _ = stopLocal(ctx) }()
	if got := observability.SessionID(); got != "nori-development-conversation" {
		t.Fatalf("expected nori-development-conversation for local cloud, got %q", got)
	}
}

func TestLibrarySecurityHeadersScreenshotsAllVersionsAndUnicodeSearch(t *testing.T) {
	ctx := context.Background()
	w := newSimHTTPWorld(t, nil)
	aliceID := "11111111-1111-4111-8111-111111111111"
	aliceEnv := w.users[aliceID]
	tok := w.token(t)

	runID := uuid.NewString()
	seedRun(t, aliceEnv.store, runID, "Library test task")

	htmlID := uuid.NewString()
	if err := aliceEnv.store.PutFile(ctx, state.ResultKey(htmlID), "text/html", []byte("<script>alert(1)</script>")); err != nil {
		t.Fatalf("put html: %v", err)
	}
	if _, err := aliceEnv.store.SaveResult(ctx, state.Result{
		ID: htmlID, RootID: htmlID, Version: 1, RunID: runID, Revision: 1,
		Kind: state.ResultFile, Title: "Report HTML", Name: "report.html",
		MediaType: "text/html", Size: 25, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save html result: %v", err)
	}

	svgID := uuid.NewString()
	if err := aliceEnv.store.PutFile(ctx, state.ResultKey(svgID), "image/svg+xml", []byte("<svg onload=alert(1)/>")); err != nil {
		t.Fatalf("put svg: %v", err)
	}
	if _, err := aliceEnv.store.SaveResult(ctx, state.Result{
		ID: svgID, RootID: svgID, Version: 1, RunID: runID, Revision: 1,
		Kind: state.ResultFile, Title: "Diagram SVG", Name: "diagram.svg",
		MediaType: "image/svg+xml", Size: 22, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save svg result: %v", err)
	}

	shotID := uuid.NewString()
	artifactID := uuid.NewString()
	pngBytes := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3, 4}
	if err := aliceEnv.store.PutPNG(ctx, runID, artifactID, pngBytes); err != nil {
		t.Fatalf("put png: %v", err)
	}
	if err := aliceEnv.store.Artifact(ctx, runID, state.Artifact{ID: artifactID, Kind: "screenshot"}); err != nil {
		t.Fatalf("record artifact: %v", err)
	}
	if _, err := aliceEnv.store.SaveResult(ctx, state.Result{
		ID: shotID, RootID: shotID, Version: 1, RunID: runID, Revision: 1,
		Kind: state.ResultScreenshot, Title: "Dashboard Screenshot", ArtifactID: artifactID,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save screenshot result: %v", err)
	}

	assertDownloadHeaders := func(name string, rec *httptest.ResponseRecorder, wantContentType string) {
		t.Helper()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", name, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Security-Policy"); got != "sandbox; default-src 'none'" {
			t.Fatalf("%s: expected CSP sandbox; default-src 'none', got %q", name, got)
		}
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: expected X-Content-Type-Options nosniff, got %q", name, got)
		}
		if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("%s: expected Cache-Control private, no-store, got %q", name, got)
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), wantContentType) {
			t.Fatalf("%s: expected Content-Type %q, got %q", name, wantContentType, rec.Header().Get("Content-Type"))
		}
	}

	assertDownloadHeaders("html", w.do(t, "GET", "/users/"+aliceID+"/v1/library/"+htmlID+"/file", tok, "", nil, nil), "text/html")
	assertDownloadHeaders("svg", w.do(t, "GET", "/users/"+aliceID+"/v1/library/"+svgID+"/file", tok, "", nil, nil), "image/svg+xml")
	shotRec := w.do(t, "GET", "/users/"+aliceID+"/v1/library/"+shotID+"/file", tok, "", nil, nil)
	assertDownloadHeaders("screenshot", shotRec, "image/png")
	if !bytes.Equal(shotRec.Body.Bytes(), pngBytes) {
		t.Fatalf("screenshot bytes mismatch: got %x want %x", shotRec.Body.Bytes(), pngBytes)
	}
	assertDownloadHeaders("md", w.do(t, "GET", "/users/"+aliceID+"/v1/library/"+htmlID+"/file?format=md", tok, "", nil, nil), "text/markdown")

	v1ID := uuid.NewString()
	v2ID := uuid.NewString()
	v3ID := uuid.NewString()
	baseTime := time.Now().UTC().Add(-time.Hour)
	for _, r := range []state.Result{
		{
			ID: v1ID, RootID: v1ID, Version: 1, SupersededBy: v2ID, RunID: runID, Revision: 1,
			Kind: state.ResultAnswer, Title: "Chain Doc v1", Text: "chain-token-v1 unique-chain-marker",
			CreatedAt: baseTime,
		},
		{
			ID: v2ID, RootID: v1ID, Version: 2, Revises: v1ID, SupersededBy: v3ID, RunID: runID, Revision: 1,
			Kind: state.ResultAnswer, Title: "Chain Doc v2", Text: "chain-token-v2 unique-chain-marker",
			CreatedAt: baseTime.Add(time.Minute),
		},
		{
			ID: v3ID, RootID: v1ID, Version: 3, Revises: v2ID, RunID: runID, Revision: 1,
			Kind: state.ResultAnswer, Title: "Chain Doc v3", Text: "chain-token-v3 unique-chain-marker",
			CreatedAt: baseTime.Add(2 * time.Minute),
		},
	} {
		if _, err := aliceEnv.store.SaveResult(ctx, r); err != nil {
			t.Fatalf("save chain version %d: %v", r.Version, err)
		}
	}

	renameV2Body, _ := json.Marshal(map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value":             map[string]any{"title": "Chain Doc v2 Renamed"},
	})
	renameRec := w.do(t, "PUT", "/users/"+aliceID+"/v1/library/"+v2ID, tok, "application/json", renameV2Body, nil)
	if renameRec.Code != http.StatusOK {
		t.Fatalf("rename v2 expected 200, got %d: %s", renameRec.Code, renameRec.Body.String())
	}

	delAllBody, _ := json.Marshal(map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value":             map[string]any{"action": "delete", "scope": "all_versions"},
	})
	delRec := w.do(t, "POST", "/users/"+aliceID+"/v1/library/"+v3ID+"/actions", tok, "application/json", delAllBody, nil)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete all_versions expected 200, got %d: %s", delRec.Code, delRec.Body.String())
	}

	listRec := w.do(t, "GET", "/users/"+aliceID+"/v1/library?versions=all", tok, "", nil, nil)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list library expected 200, got %d", listRec.Code)
	}
	var listed struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode library list: %v", err)
	}
	for _, it := range listed.Items {
		if it.ID == v1ID || it.ID == v2ID || it.ID == v3ID {
			t.Fatalf("deleted version %s still present in library list", it.ID)
		}
	}
	for _, vid := range []string{v1ID, v2ID, v3ID} {
		dl := w.do(t, "GET", "/users/"+aliceID+"/v1/library/"+vid+"/file", tok, "", nil, nil)
		if dl.Code != http.StatusNotFound {
			t.Fatalf("deleted version %s download expected 404, got %d", vid, dl.Code)
		}
	}
	chainSearch := w.do(t, "GET", "/users/"+aliceID+"/v1/search?q=unique-chain-marker", tok, "", nil, nil)
	if chainSearch.Code != http.StatusOK {
		t.Fatalf("search deleted chain expected 200, got %d", chainSearch.Code)
	}
	var chainHits struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(chainSearch.Body.Bytes(), &chainHits); err != nil {
		t.Fatalf("decode chain search: %v", err)
	}
	if len(chainHits.Items) != 0 {
		t.Fatalf("expected 0 search hits after all_versions delete, got %d", len(chainHits.Items))
	}

	unicodeID := uuid.NewString()
	unicodeText := strings.Repeat("Ⱥ", 500) + strings.Repeat("İȺȾ😀漢字", 60) + " unicode-needle-target " + strings.Repeat("🚀日本語İ", 100)
	if _, err := aliceEnv.store.SaveResult(ctx, state.Result{
		ID: unicodeID, RootID: unicodeID, Version: 1, RunID: runID, Revision: 1,
		Kind: state.ResultAnswer, Title: strings.Repeat("😀İ", 90), Text: unicodeText,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save unicode result: %v", err)
	}
	uniSearch := w.do(t, "GET", "/users/"+aliceID+"/v1/search?q=unicode-needle-target", tok, "", nil, nil)
	if uniSearch.Code != http.StatusOK {
		t.Fatalf("unicode search expected 200, got %d: %s", uniSearch.Code, uniSearch.Body.String())
	}
	var uniHits struct {
		Items []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(uniSearch.Body.Bytes(), &uniHits); err != nil {
		t.Fatalf("decode unicode search: %v", err)
	}
	if len(uniHits.Items) == 0 {
		t.Fatalf("expected unicode search hit")
	}
	for _, hit := range uniHits.Items {
		if !utf8.ValidString(hit.Snippet) || !utf8.ValidString(hit.Title) {
			t.Fatalf("invalid UTF-8 in search hit: title=%q snippet=%q", hit.Title, hit.Snippet)
		}
		if hit.ID == unicodeID && !strings.Contains(hit.Snippet, "unicode-needle-target") {
			t.Fatalf("expected snippet to contain matched needle, got %q", hit.Snippet)
		}
	}
}

func TestAPIPanicsNullValuesOversizedJSONAndContinueRetry(t *testing.T) {
	ctx := context.Background()
	aliceID := "11111111-1111-4111-8111-111111111111"

	var continueCalls atomic.Int64
	w := newSimHTTPWorld(t, func(u users.User, env *simUserEnv) {
		if u.ID != aliceID {
			return
		}
		env.assistant.Stop = nil
		env.assistant.Answer = nil
		env.assistant.Continue = func(ctx context.Context, taskID, previous, request string, budget float64) (state.Run, error) {
			if continueCalls.Add(1) == 1 {
				receipts, err := env.store.AcceptCommandBatch(ctx, []state.Command{{
					ID:    uuid.NewString(),
					RunID: taskID,
					Kind:  state.CommandStart,
					Text:  request,
					Origin: &state.TaskOrigin{
						ConversationID:  "assistant",
						ContinuesTaskID: previous,
					},
					BudgetUSD: budget,
				}}, nil)
				if err != nil {
					return state.Run{}, err
				}
				return receipts[0].Run, nil
			}
			return state.Run{}, state.ErrDuplicate
		}
	})
	aliceEnv := w.users[aliceID]
	tok := w.token(t)

	failedRunID := uuid.NewString()
	seedRun(t, aliceEnv.store, failedRunID, "Original task with emoji 🚀"+strings.Repeat("İ", 150))
	if err := aliceEnv.store.Incorporate(ctx, state.IncorporateRequest{RunID: failedRunID, Revision: 1, Position: 1}); err != nil {
		t.Fatalf("incorporate run: %v", err)
	}
	if _, err := aliceEnv.store.Publish(ctx, state.PublishRequest{
		RunID:    failedRunID,
		Revision: 1,
		Status:   state.RunFailed,
		Text:     "spending limit reached",
	}); err != nil {
		t.Fatalf("fail run: %v", err)
	}

	stopBody, _ := json.Marshal(map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 0,
		"value":             map[string]any{"action": "stop"},
	})
	stopRec := w.do(t, "POST", "/users/"+aliceID+"/v1/work/"+failedRunID+"/actions", tok, "application/json", stopBody, nil)
	if stopRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil Stop expected 503, got %d: %s", stopRec.Code, stopRec.Body.String())
	}

	interactionID := uuid.NewString()
	rev := int64(1)
	if _, err := aliceEnv.perms.Open(ctx, permissions.Interaction{
		ID:                  interactionID,
		TaskID:              failedRunID,
		InstructionRevision: &rev,
		Kind:                "question",
		Status:              "pending",
		Prompt:              "Need confirmation",
		Payload:             json.RawMessage(`{"input":"text"}`),
		LegacyQuestionID:    "q-1",
	}, ""); err != nil {
		t.Fatalf("open interaction: %v", err)
	}
	respondBody, _ := json.Marshal(map[string]any{
		"request_id":        uuid.NewString(),
		"expected_revision": 1,
		"value":             map[string]any{"kind": "answer", "text": "go ahead"},
	})
	respRec := w.do(t, "POST", "/users/"+aliceID+"/v1/interactions/"+interactionID+"/responses", tok, "application/json", respondBody, nil)
	if respRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil Answer expected 503, got %d: %s", respRec.Code, respRec.Body.String())
	}

	contReqID := uuid.NewString()
	contBody, _ := json.Marshal(map[string]any{
		"request_id":        contReqID,
		"expected_revision": 0,
		"value":             map[string]any{"action": "continue", "budget_usd": 2.0},
	})
	firstCont := w.do(t, "POST", "/users/"+aliceID+"/v1/work/"+failedRunID+"/actions", tok, "application/json", contBody, nil)
	if firstCont.Code != http.StatusAccepted {
		t.Fatalf("first continue expected 202, got %d: %s", firstCont.Code, firstCont.Body.String())
	}
	secondCont := w.do(t, "POST", "/users/"+aliceID+"/v1/work/"+failedRunID+"/actions", tok, "application/json", contBody, nil)
	if secondCont.Code != http.StatusAccepted {
		t.Fatalf("duplicate continue expected 202, got %d: %s", secondCont.Code, secondCont.Body.String())
	}
	var receipt struct {
		Accepted bool `json:"accepted"`
		Resource struct {
			TaskID string `json:"task_id"`
			Status string `json:"status"`
			Title  string `json:"title"`
		} `json:"resource"`
		Work *state.WorkRef `json:"work"`
	}
	if err := json.Unmarshal(secondCont.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("decode duplicate continue receipt: %v", err)
	}
	if receipt.Resource.TaskID == "" || receipt.Work == nil || receipt.Resource.TaskID != receipt.Work.TaskID {
		t.Fatalf("duplicate continue returned zero-value workItem: %+v", receipt)
	}
	if receipt.Resource.Status != string(state.RunRunning) {
		t.Fatalf("expected running status on duplicate continue, got %q", receipt.Resource.Status)
	}
	if !utf8.ValidString(receipt.Resource.Title) {
		t.Fatalf("clipped title is not valid UTF-8: %q", receipt.Resource.Title)
	}

	nullBody := []byte(`{"request_id":"` + uuid.NewString() + `","expected_revision":0,"value":null}`)
	nullRec := w.do(t, "POST", "/users/"+aliceID+"/v1/work/"+failedRunID+"/actions", tok, "application/json", nullBody, nil)
	if nullRec.Code != http.StatusBadRequest {
		t.Fatalf("value=null expected 400, got %d: %s", nullRec.Code, nullRec.Body.String())
	}

	oversized := []byte(`{"request_id":"` + uuid.NewString() + `","expected_revision":0,"value":{"action":"` + strings.Repeat("x", 70<<10) + `"}}`)
	largeRec := w.do(t, "POST", "/users/"+aliceID+"/v1/work/"+failedRunID+"/actions", tok, "application/json", oversized, nil)
	if largeRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized JSON expected 413, got %d: %s", largeRec.Code, largeRec.Body.String())
	}

	oversizedResolve := []byte(`{"email":"` + strings.Repeat("a", 70<<10) + `@example.com"}`)
	largeResolveRec := w.do(t, "POST", "/api/users/resolve", tok, "application/json", oversizedResolve, nil)
	if largeResolveRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized resolve JSON expected 413, got %d: %s", largeResolveRec.Code, largeResolveRec.Body.String())
	}

	badRedirects := []string{
		`/\evil.com`,
		`//evil.com`,
		`/apps?next=https://evil.com`,
		`/apps#frag`,
		"/apps\r\nX-Injected: 1",
		"/apps\x00",
	}
	for _, bad := range badRedirects {
		redirBody, _ := json.Marshal(map[string]any{
			"request_id":        uuid.NewString(),
			"expected_revision": 0,
			"value":             map[string]any{"action": "sign_in", "return_to": bad},
		})
		redirRec := w.do(t, "POST", "/users/"+aliceID+"/v1/connections/conn-1/actions", tok, "application/json", redirBody, nil)
		if redirRec.Code != http.StatusBadRequest {
			t.Fatalf("redirect %q expected 400, got %d: %s", bad, redirRec.Code, redirRec.Body.String())
		}
	}
}
