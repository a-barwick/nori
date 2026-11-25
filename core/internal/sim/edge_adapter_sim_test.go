package sim_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"nori/internal/api"
	"nori/internal/app"
	"nori/internal/connections"
	"nori/internal/conversation"
	"nori/internal/extensions"
	"nori/internal/permissions"
	"nori/internal/secret"
	"nori/internal/serviceauth"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
	"nori/internal/wake"
)

type stubCatalogIntegration struct{}

func (stubCatalogIntegration) Info() connections.Info {
	return connections.Info{ID: "stub-connector", Title: "Stub Connector"}
}

func (stubCatalogIntegration) OAuth(string) *oauth2.Config { return nil }

func (stubCatalogIntegration) Identify(context.Context, *http.Client) (connections.Account, error) {
	return connections.Account{}, nil
}

func (stubCatalogIntegration) Revoke(context.Context, *oauth2.Token) error { return nil }

func (stubCatalogIntegration) Resources(context.Context, *http.Client) ([]connections.Resource, error) {
	return nil, nil
}

func (stubCatalogIntegration) Definitions() []extensions.Definition { return nil }

type edgeAdapterUser struct {
	user      users.User
	store     state.ConversationStore
	tasks     *conversation.Tasks
	runner    *conversation.Runner
	conns     *connections.Service
	perms     *permissions.Service
	assistant *api.Assistant
	ops       *simOps
}

type edgePipeRequestMsg struct {
	Type    string            `json:"type"`
	ID      int               `json:"id"`
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	BodyB64 string            `json:"body_b64,omitempty"`
}

type edgePipeResponseMsg struct {
	Type    string            `json:"type"`
	ID      int               `json:"id"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	DataB64 string            `json:"data_b64,omitempty"`
}

type edgePipeResponseWriter struct {
	id         int
	header     http.Header
	status     int
	wroteHead  bool
	send       func(edgePipeResponseMsg)
	headerLock sync.Mutex
}

func newEdgePipeResponseWriter(id int, send func(edgePipeResponseMsg)) *edgePipeResponseWriter {
	return &edgePipeResponseWriter{
		id:     id,
		header: make(http.Header),
		status: http.StatusOK,
		send:   send,
	}
}

func (w *edgePipeResponseWriter) Header() http.Header {
	return w.header
}

func (w *edgePipeResponseWriter) sendHeadLocked() {
	if w.wroteHead {
		return
	}
	w.wroteHead = true
	flat := make(map[string]string, len(w.header))
	for k, vals := range w.header {
		if len(vals) > 0 {
			flat[strings.ToLower(k)] = strings.Join(vals, ", ")
		}
	}
	w.send(edgePipeResponseMsg{
		Type:    "head",
		ID:      w.id,
		Status:  w.status,
		Headers: flat,
	})
}

func (w *edgePipeResponseWriter) WriteHeader(code int) {
	w.headerLock.Lock()
	defer w.headerLock.Unlock()
	if w.wroteHead {
		return
	}
	w.status = code
	w.sendHeadLocked()
}

func (w *edgePipeResponseWriter) Write(p []byte) (int, error) {
	w.headerLock.Lock()
	w.sendHeadLocked()
	w.headerLock.Unlock()
	if len(p) > 0 {
		w.send(edgePipeResponseMsg{
			Type:    "chunk",
			ID:      w.id,
			DataB64: base64.StdEncoding.EncodeToString(p),
		})
	}
	return len(p), nil
}

func (w *edgePipeResponseWriter) Flush() {
	w.headerLock.Lock()
	w.sendHeadLocked()
	w.headerLock.Unlock()
}

func (w *edgePipeResponseWriter) finish() {
	w.headerLock.Lock()
	w.sendHeadLocked()
	w.headerLock.Unlock()
	w.send(edgePipeResponseMsg{Type: "end", ID: w.id})
}

func loopbackTCPAvailable(serverURL string) bool {
	addr := strings.TrimPrefix(serverURL, "http://")
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func runNodeWithPipeBridge(ctx context.Context, handler http.Handler, nodeScript string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, "node", "--input-type=module", "-e", nodeScript)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}

	var writeMu sync.Mutex
	enc := json.NewEncoder(stdin)
	send := func(msg edgePipeResponseMsg) {
		writeMu.Lock()
		_ = enc.Encode(msg)
		writeMu.Unlock()
	}

	var cancelMu sync.Mutex
	cancels := map[int]context.CancelFunc{}
	var wg sync.WaitGroup

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg edgePipeRequestMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "cancel":
			cancelMu.Lock()
			if fn, ok := cancels[msg.ID]; ok {
				fn()
				delete(cancels, msg.ID)
			}
			cancelMu.Unlock()
		case "req":
			reqCtx, cancel := context.WithCancel(ctx)
			cancelMu.Lock()
			cancels[msg.ID] = cancel
			cancelMu.Unlock()
			wg.Add(1)
			go func(m edgePipeRequestMsg, rctx context.Context, rcancel context.CancelFunc) {
				defer wg.Done()
				defer func() {
					rcancel()
					cancelMu.Lock()
					delete(cancels, m.ID)
					cancelMu.Unlock()
				}()
				var bodyReader io.Reader
				if m.BodyB64 != "" {
					raw, decErr := base64.StdEncoding.DecodeString(m.BodyB64)
					if decErr == nil {
						bodyReader = bytes.NewReader(raw)
					}
				}
				req, reqErr := http.NewRequestWithContext(rctx, m.Method, m.URL, bodyReader)
				if reqErr != nil {
					send(edgePipeResponseMsg{Type: "head", ID: m.ID, Status: http.StatusBadRequest})
					send(edgePipeResponseMsg{Type: "end", ID: m.ID})
					return
				}
				for k, v := range m.Headers {
					req.Header.Set(k, v)
				}
				rw := newEdgePipeResponseWriter(m.ID, send)
				handler.ServeHTTP(rw, req)
				rw.finish()
			}(msg, reqCtx, cancel)
		}
	}

	cancelMu.Lock()
	for id, fn := range cancels {
		fn()
		delete(cancels, id)
	}
	cancelMu.Unlock()
	wg.Wait()
	_ = stdin.Close()
	waitErr := cmd.Wait()
	return stderr.String(), waitErr
}

func repoRootFromSim(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func TestEdgeAdapterFlowF12(t *testing.T) {
	ctx := context.Background()
	backend := memstore.New()
	artifacts := memstore.NewArtifacts()
	directory := records.NewSharedUsers(backend)
	authCfg := serviceauth.Config{
		Key:      "0123456789abcdef0123456789abcdef",
		Audience: "nori-edge-sim",
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
		TimeZone:    "UTC",
	}

	envs := map[string]*edgeAdapterUser{}
	for _, u := range []users.User{alice, bob} {
		if err := directory.Create(ctx, u); err != nil {
			t.Fatalf("create user %s: %v", u.ID, err)
		}
		store := state.Compose(
			records.NewShared(backend, u, 5.0),
			prefixedArtifacts{files: artifacts, pngs: memstore.NewArtifacts(), prefix: u.FilePrefix()},
		)
		conn := connections.New(store, secret.Unavailable{}, "http://127.0.0.1:3211/api/v1/connections/oauth/callback")
		conn.UserTimezone = u.TimeZone
		perms := &permissions.Service{Store: store, UnattendedWrites: "confirm_writes", UserSends: "allow"}
		tasks := &conversation.Tasks{Store: store}
		runner := conversation.New(nil, nil, nil, tasks)
		ops := &simOps{}
		assistant := &api.Assistant{
			Store:       store,
			Connections: conn,
			Permissions: perms,
			Features:    app.AssistantFeatures(conn, false),
		}
		envs[u.ID] = &edgeAdapterUser{
			user:      u,
			store:     store,
			tasks:     tasks,
			runner:    runner,
			conns:     conn,
			perms:     perms,
			assistant: assistant,
			ops:       ops,
		}
	}

	open := func(_ context.Context, u users.User) (api.UserRoutes, func(), error) {
		env := envs[u.ID]
		h := api.Handler(api.Deps{
			Model:     "sim-model",
			Runner:    env.runner,
			Tasks:     env.tasks,
			Waker:     wake.Disabled{},
			Ops:       env.ops,
			Assistant: env.assistant,
		})
		return api.UserRoutes{Handler: h, Resume: env.ops.Resume}, func() {}, nil
	}

	handler := authCfg.Protect(api.UserHandler(directory, open, false, "sim-model"))
	server := httptest.NewServer(handler)
	defer server.Close()
	usePipe := !loopbackTCPAvailable(server.URL)

	aliceEnv := envs[alice.ID]
	bobEnv := envs[bob.ID]

	aliceWorkingRunID := uuid.NewString()
	seedRun(t, aliceEnv.store, aliceWorkingRunID, "Alice active analysis")

	aliceFinishedRunID := uuid.NewString()
	aliceFinishedSeed := seedRun(t, aliceEnv.store, aliceFinishedRunID, "Alice completed report")
	if err := aliceEnv.store.Incorporate(ctx, state.IncorporateRequest{
		RunID:    aliceFinishedRunID,
		Revision: aliceFinishedSeed.Revision,
		Position: state.TaskPosition(aliceFinishedSeed),
	}); err != nil {
		t.Fatalf("incorporate alice run: %v", err)
	}
	if _, err := aliceEnv.store.Publish(ctx, state.PublishRequest{
		RunID:         aliceFinishedRunID,
		Revision:      aliceFinishedSeed.Revision,
		Status:        state.RunCompleted,
		Text:          "Alice report done",
		BypassRouting: true,
	}); err != nil {
		t.Fatalf("finish alice run: %v", err)
	}

	aliceResultID := uuid.NewString()
	aliceHTML := []byte("<html><body><script>alert(1)</script>report</body></html>")
	if err := aliceEnv.store.PutFile(ctx, state.ResultKey(aliceResultID), "text/html", aliceHTML); err != nil {
		t.Fatalf("put alice file: %v", err)
	}
	if _, err := aliceEnv.store.SaveResult(ctx, state.Result{
		ID: aliceResultID, RootID: aliceResultID, Version: 1, RunID: aliceFinishedRunID, Revision: 1,
		Kind: state.ResultFile, Title: "Alice HTML Export", Text: "report",
		Name: "report.html", MediaType: "text/html", Size: int64(len(aliceHTML)), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save alice result: %v", err)
	}

	bobRunID := uuid.NewString()
	seedRun(t, bobEnv.store, bobRunID, "Bob private task")
	bobResultID := uuid.NewString()
	bobPayload := []byte("bob-confidential-bytes")
	if err := bobEnv.store.PutFile(ctx, state.ResultKey(bobResultID), "text/plain", bobPayload); err != nil {
		t.Fatalf("put bob file: %v", err)
	}
	if _, err := bobEnv.store.SaveResult(ctx, state.Result{
		ID: bobResultID, RootID: bobResultID, Version: 1, RunID: bobRunID, Revision: 1,
		Kind: state.ResultFile, Title: "Bob Private Note", Text: "confidential",
		Name: "bob.txt", MediaType: "text/plain", Size: int64(len(bobPayload)), CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save bob result: %v", err)
	}

	tok, err := authCfg.Token()
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}

	var capBody []byte
	if usePipe {
		capReq := httptest.NewRequest(http.MethodGet, "/users/"+alice.ID+"/v1/capabilities", nil)
		capReq.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, capReq)
		if rec.Code != http.StatusOK {
			t.Fatalf("capabilities status = %d, want 200", rec.Code)
		}
		capBody = rec.Body.Bytes()
	} else {
		capReq, err := http.NewRequest(http.MethodGet, server.URL+"/users/"+alice.ID+"/v1/capabilities", nil)
		if err != nil {
			t.Fatalf("build capabilities request: %v", err)
		}
		capReq.Header.Set("Authorization", "Bearer "+tok)
		capResp, err := http.DefaultClient.Do(capReq)
		if err != nil {
			t.Fatalf("get capabilities: %v", err)
		}
		defer capResp.Body.Close()
		if capResp.StatusCode != http.StatusOK {
			t.Fatalf("capabilities status = %d, want 200", capResp.StatusCode)
		}
		capBody, err = io.ReadAll(capResp.Body)
		if err != nil {
			t.Fatalf("read capabilities: %v", err)
		}
	}
	var caps struct {
		AssistantFeatures map[string]bool `json:"assistant_features"`
	}
	if err := json.Unmarshal(capBody, &caps); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if caps.AssistantFeatures["connections"] {
		t.Fatalf("expected assistant_features.connections to be false with zero integrations, got true")
	}
	if !caps.AssistantFeatures["mcp"] || !caps.AssistantFeatures["permissions"] {
		t.Fatalf("expected mcp and permissions features to remain true, got %+v", caps.AssistantFeatures)
	}

	populatedConns := connections.New(aliceEnv.store, secret.Unavailable{}, "http://127.0.0.1:3211/api/v1/connections/oauth/callback", stubCatalogIntegration{})
	if !app.AssistantFeatures(populatedConns, false)["connections"] {
		t.Fatalf("expected AssistantFeatures connections=true when an integration is registered")
	}
	if app.AssistantFeatures(nil, false)["connections"] {
		t.Fatalf("expected AssistantFeatures connections=false when connections service is nil")
	}

	root := repoRootFromSim(t)
	adapterPath := filepath.Join(root, "integrations", "embed", "adapter.mjs")
	cfgJSON, err := json.Marshal(map[string]any{
		"adapterPath":        adapterPath,
		"runtimeBase":        server.URL,
		"usePipe":            usePipe,
		"token":              tok,
		"aliceID":            alice.ID,
		"bobID":              bob.ID,
		"aliceWorkingRunID":  aliceWorkingRunID,
		"aliceFinishedRunID": aliceFinishedRunID,
		"aliceResultID":      aliceResultID,
		"aliceHTML":          string(aliceHTML),
		"bobRunID":           bobRunID,
		"bobResultID":        bobResultID,
		"bobPayload":         string(bobPayload),
	})
	if err != nil {
		t.Fatalf("marshal node config: %v", err)
	}

	nodeScript := `
import assert from "node:assert/strict";
import readline from "node:readline";
import { pathToFileURL } from "node:url";

const cfg = JSON.parse(process.env.EDGE_ADAPTER_CFG);

if (cfg.usePipe) {
  let nextId = 1;
  const inflight = new Map();
  const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
  rl.on("line", (line) => {
    if (!line.trim()) return;
    const msg = JSON.parse(line);
    const entry = inflight.get(msg.id);
    if (!entry) return;
    if (msg.type === "head") {
      let controller;
      const stream = new ReadableStream({
        start(c) { controller = c; },
        cancel() {
          process.stdout.write(JSON.stringify({ type: "cancel", id: msg.id }) + "\n");
          inflight.delete(msg.id);
        },
      });
      entry.controller = controller;
      entry.resolve(new Response(stream, { status: msg.status, headers: msg.headers || {} }));
    } else if (msg.type === "chunk" && entry.controller) {
      try {
        entry.controller.enqueue(new Uint8Array(Buffer.from(msg.data_b64, "base64")));
      } catch {}
    } else if (msg.type === "end") {
      if (entry.controller) {
        try { entry.controller.close(); } catch {}
      }
      inflight.delete(msg.id);
    }
  });
  globalThis.fetch = async (input, init = {}) => {
    const id = nextId++;
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const method = init.method || "GET";
    const headers = {};
    if (init.headers) {
      new Headers(init.headers).forEach((value, key) => { headers[key] = value; });
    }
    let body_b64 = "";
    if (typeof init.body === "string") {
      body_b64 = Buffer.from(init.body, "utf8").toString("base64");
    } else if (init.body instanceof Uint8Array) {
      body_b64 = Buffer.from(init.body).toString("base64");
    } else if (init.body instanceof ArrayBuffer) {
      body_b64 = Buffer.from(init.body).toString("base64");
    }
    return await new Promise((resolve, reject) => {
      const entry = { resolve, reject, controller: null };
      inflight.set(id, entry);
      if (init.signal) {
        const onAbort = () => {
          process.stdout.write(JSON.stringify({ type: "cancel", id }) + "\n");
          if (entry.controller) {
            try { entry.controller.error(new DOMException("Aborted", "AbortError")); } catch {}
          } else {
            reject(new DOMException("Aborted", "AbortError"));
          }
          inflight.delete(id);
        };
        if (init.signal.aborted) {
          onAbort();
          return;
        }
        init.signal.addEventListener("abort", onAbort, { once: true });
      }
      process.stdout.write(JSON.stringify({ type: "req", id, method, url, headers, body_b64 }) + "\n");
    });
  };
}

const { createNoriHandler } = await import(pathToFileURL(cfg.adapterPath).href);

let authShouldThrow = false;

const handle = createNoriHandler({
  runtimeBase: cfg.runtimeBase,
  bindings: [
    { issuer: "https://host.test", subject: "sub-alice", noriUserId: cfg.aliceID },
    { issuer: "https://host.test", subject: "sub-bob", noriUserId: cfg.bobID },
  ],
  async verifyIdentity(request) {
    const who = request.headers.get("x-test-user");
    if (who === "alice") return { issuer: "https://host.test", subject: "sub-alice" };
    if (who === "bob") return { issuer: "https://host.test", subject: "sub-bob" };
    return null;
  },
  async serviceAuthorization() {
    if (authShouldThrow) throw new Error("token minter unavailable");
    return "Bearer " + cfg.token;
  },
});

const call = (who, path, init = {}) => {
  const headers = new Headers(init.headers || {});
  if (who) headers.set("x-test-user", who);
  return handle(new Request("https://host.test" + path, { ...init, headers }));
};

const msgId = "33333333-3333-4333-8333-333333333333";
const postRes = await call("alice", "/api/nori/messages", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: JSON.stringify({ id: msgId, text: "Hello Nori from Host" }),
});
assert.equal(postRes.status, 202, "POST /api/nori/messages status");
const postBody = await postRes.json();
assert.equal(postBody.accepted, true);
assert.equal(postBody.state, "accepted");
assert.equal(postBody.turnId, msgId);

const convRes = await call("alice", "/api/nori/conversation");
assert.equal(convRes.status, 200, "GET /api/nori/conversation status");
const convBody = await convRes.json();
assert.ok(convBody.position >= 1, "conversation position >= 1");
assert.ok(
  convBody.messages.some((m) => (m.id === msgId || m.turnId === msgId) && m.text === "Hello Nori from Host"),
  "conversation contains posted message",
);

const sseAbort = new AbortController();
const sseRes = await call("alice", "/api/nori/events?after=0", { signal: sseAbort.signal });
assert.equal(sseRes.status, 200, "GET /api/nori/events status");
assert.ok((sseRes.headers.get("content-type") || "").startsWith("text/event-stream"));
const reader = sseRes.body.getReader();
const decoder = new TextDecoder();
let sseText = "";
while (!sseText.includes(msgId)) {
  const { value, done } = await reader.read();
  if (done) break;
  sseText += decoder.decode(value, { stream: true });
}
sseAbort.abort();
await reader.cancel().catch(() => {});
assert.ok(sseText.includes(msgId), "SSE stream delivered message event");

const workWorkingRes = await call("alice", "/api/nori/work?bucket=working");
assert.equal(workWorkingRes.status, 200);
const workWorking = await workWorkingRes.json();
assert.deepEqual(
  workWorking.items.map((i) => i.task_id),
  [cfg.aliceWorkingRunID],
  "bucket=working returns only Alice working run",
);

const workFinishedRes = await call("alice", "/api/nori/work?bucket=finished");
assert.equal(workFinishedRes.status, 200);
const workFinished = await workFinishedRes.json();
assert.deepEqual(
  workFinished.items.map((i) => i.task_id),
  [cfg.aliceFinishedRunID],
  "bucket=finished returns only Alice finished run",
);

const workActiveRes = await call("alice", "/api/nori/work?bucket=active");
assert.equal(workActiveRes.status, 200);
const workActive = await workActiveRes.json();
assert.equal(workActive.items.length, 0, "bucket=active filters cleanly");

const resMeta = await call("alice", "/api/nori/results/" + cfg.aliceResultID);
assert.equal(resMeta.status, 200);
const resMetaBody = await resMeta.json();
assert.equal(resMetaBody.id, cfg.aliceResultID);

const resFile = await call("alice", "/api/nori/results/" + cfg.aliceResultID + "/file");
assert.equal(resFile.status, 200);
assert.equal(resFile.headers.get("content-security-policy"), "sandbox; default-src 'none'");
assert.equal(resFile.headers.get("x-content-type-options"), "nosniff");
assert.equal(resFile.headers.get("cache-control"), "private, no-store");
assert.ok((resFile.headers.get("content-disposition") || "").includes("attachment"));
assert.equal(await resFile.text(), cfg.aliceHTML);

const hugePayload = JSON.stringify({ id: "44444444-4444-4444-8444-444444444444", text: "a".repeat(70000) });
const oversizedRes = await call("alice", "/api/nori/messages", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: hugePayload,
});
assert.equal(oversizedRes.status, 413, "oversized body returns 413");

const chunkedStream = new ReadableStream({
  start(controller) {
    const encoder = new TextEncoder();
    controller.enqueue(encoder.encode('{"id":"55555555-5555-4555-8555-555555555555","text":"'));
    for (let i = 0; i < 10; i++) {
      controller.enqueue(encoder.encode("b".repeat(8192)));
    }
    controller.enqueue(encoder.encode('"}'));
    controller.close();
  },
});
const chunkedOversizedRes = await call("alice", "/api/nori/messages", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: chunkedStream,
  duplex: "half",
});
assert.equal(chunkedOversizedRes.status, 413, "chunked oversized body returns 413");

const malformedRes = await call("alice", "/api/nori/messages", {
  method: "POST",
  headers: { "content-type": "application/json" },
  body: "{not valid json",
});
assert.equal(malformedRes.status, 400, "malformed JSON still returns 400");

authShouldThrow = true;
const authErrRes = await call("alice", "/api/nori/conversation");
authShouldThrow = false;
assert.equal(authErrRes.status, 503, "serviceAuthorization throw returns 503");
const authErrBody = await authErrRes.json();
assert.equal(authErrBody.error, "Nori service authentication is unavailable.");

for (const badPath of [
  "/api/nori/results/..",
  "/api/nori/results/%2e%2e",
  "/api/nori/results/%2e%2e/file",
  "/api/nori/results/./file",
  "/api/nori/work/..%2f..",
]) {
  const badRes = await call("alice", badPath);
  assert.ok(badRes.status === 400 || badRes.status === 404, "path traversal rejected for " + badPath + ", got " + badRes.status);
}

const overflowCursorRes = await call("alice", "/api/nori/events?after=9223372036854775808");
assert.equal(overflowCursorRes.status, 400, "cursor > MaxInt64 returns 400");
assert.equal((await overflowCursorRes.json()).error, "Invalid event cursor.");

const maxInt64CursorRes = await call("alice", "/api/nori/events?after=9223372036854775807");
assert.equal(maxInt64CursorRes.status, 400, "cursor == MaxInt64 passes adapter and reaches core ahead-of-position check");
assert.equal((await maxInt64CursorRes.json()).error, "Event position is ahead of the conversation.");

const aliceReadsBobResult = await call("alice", "/api/nori/results/" + cfg.bobResultID);
assert.equal(aliceReadsBobResult.status, 404, "Alice cannot read Bob result metadata");
const aliceReadsBobFile = await call("alice", "/api/nori/results/" + cfg.bobResultID + "/file");
assert.equal(aliceReadsBobFile.status, 404, "Alice cannot read Bob result file");
const aliceReadsBobWork = await call("alice", "/api/nori/work/" + cfg.bobRunID);
assert.equal(aliceReadsBobWork.status, 404, "Alice cannot read Bob work item");

const bobReadsOwnFile = await call("bob", "/api/nori/results/" + cfg.bobResultID + "/file");
assert.equal(bobReadsOwnFile.status, 200, "Bob can read own result file");
assert.equal(await bobReadsOwnFile.text(), cfg.bobPayload);
const bobReadsAliceFile = await call("bob", "/api/nori/results/" + cfg.aliceResultID + "/file");
assert.equal(bobReadsAliceFile.status, 404, "Bob cannot read Alice result file");

process.exit(0);
`

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	env := append(os.Environ(), "EDGE_ADAPTER_CFG="+string(cfgJSON))
	if usePipe {
		stderrOut, runErr := runNodeWithPipeBridge(cmdCtx, handler, nodeScript, env)
		if runErr != nil {
			t.Fatalf("node embed adapter E2E (pipe bridge) failed: %v\nstderr:\n%s", runErr, stderrOut)
		}
		return
	}
	cmd := exec.CommandContext(cmdCtx, "node", "--input-type=module", "-e", nodeScript)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("node embed adapter E2E failed: %v\noutput:\n%s", err, out.String())
	}
}

func TestEdgeRunWithEnvSecretHygiene(t *testing.T) {
	root := repoRootFromSim(t)
	scriptPath := filepath.Join(root, "scripts", "run-with-env.mjs")

	missingPath := filepath.Join(t.TempDir(), "missing.env")
	missingCmd := exec.Command("node", scriptPath, missingPath, "node", "-e", "process.exit(0)")
	var missingOut bytes.Buffer
	missingCmd.Stdout = &missingOut
	missingCmd.Stderr = &missingOut
	if err := missingCmd.Run(); err == nil {
		t.Fatalf("expected non-zero exit for missing .env file")
	}
	msg := missingOut.String()
	if !strings.Contains(msg, ".env.example") {
		t.Fatalf("expected missing .env message to mention .env.example, got: %q", msg)
	}
	if strings.Contains(msg, "ENOENT") || strings.Contains(msg, "at readFileSync") {
		t.Fatalf("expected clean error without ENOENT stack trace, got: %q", msg)
	}

	strippedKeys := []string{
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"OPENROUTER_MANAGEMENT_KEY",
		"TEMPORAL_BILLING_API_KEY",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_PROD_API_KEY",
		"ANTHROPIC_DEV_API_KEY",
		"ANTHROPIC_TEST_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"OPENAI_API_KEY",
		"OPENAI_PROD_API_KEY",
		"OPENAI_DEV_API_KEY",
		"OPENAI_TEST_API_KEY",
		"OPENROUTER_PROD_API_KEY",
		"OPENROUTER_DEV_API_KEY",
		"OPENROUTER_TEST_API_KEY",
		"NORI_POSTGRES_URL",
		"NORI_POSTGRES_LISTEN_URL",
		"NORI_CREDENTIAL_KEY",
		"NORI_CREDENTIAL_KEY_SECRET",
		"NORI_CREDENTIAL_KEY_FILE",
		"TEMPORAL_API_KEY",
		"TEMPORAL_API_KEY_SECRET_VERSION",
		"OPENROUTER_API_KEY",
		"JEV_API_KEY",
		"BRAVE_SEARCH_API_KEY",
		"NORI_MODEL_GATEWAY_API_KEY",
		"NORI_USAGE_TOKEN",
		"NORI_SAFE_PUBLIC_FLAG",
	}

	var envLines []string
	for _, key := range strippedKeys {
		envLines = append(envLines, key+"=secret-value-"+key)
	}
	envLines = append(envLines,
		"NEXT_PUBLIC_SENTRY_ENVIRONMENT=staging",
		"NORI_APP_ORIGIN=https://nori.example.test",
	)
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte(strings.Join(envLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write temp env: %v", err)
	}

	probeCmd := exec.Command("node", scriptPath, envPath, "node", "-e", "process.stdout.write(JSON.stringify(process.env))")
	probeCmd.Env = append(os.Environ(),
		"AWS_SECRET_ACCESS_KEY=parent-aws-secret",
		"NORI_POSTGRES_URL=postgres://parent-secret",
	)
	var probeOut bytes.Buffer
	var probeErr bytes.Buffer
	probeCmd.Stdout = &probeOut
	probeCmd.Stderr = &probeErr
	if err := probeCmd.Run(); err != nil {
		t.Fatalf("run-with-env probe failed: %v\nstderr: %s", err, probeErr.String())
	}
	var childEnv map[string]string
	if err := json.Unmarshal(probeOut.Bytes(), &childEnv); err != nil {
		t.Fatalf("decode child env: %v", err)
	}
	for _, key := range strippedKeys {
		if val, ok := childEnv[key]; ok {
			t.Fatalf("expected unallowed key %s to be absent from child env, got %q", key, val)
		}
	}
	if childEnv["NEXT_PUBLIC_SENTRY_ENVIRONMENT"] != "staging" {
		t.Fatalf("expected allowlisted web var NEXT_PUBLIC_SENTRY_ENVIRONMENT to pass through, got %q", childEnv["NEXT_PUBLIC_SENTRY_ENVIRONMENT"])
	}
	if childEnv["NORI_APP_ORIGIN"] != "https://nori.example.test" {
		t.Fatalf("expected allowlisted web var NORI_APP_ORIGIN to pass through, got %q", childEnv["NORI_APP_ORIGIN"])
	}
}
