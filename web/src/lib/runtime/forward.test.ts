import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { exportJWK, generateKeyPair, SignJWT } from "jose";
import { asFetch } from "@/lib/test-fetch";
import { MAX_FILE_BYTES } from "../submission";
import { assistantRoute, fileRoute, operationRoute, uploadRoute, validSegment } from "./forward";
import * as settingsRoute from "@/app/api/v1/settings/[[...path]]/route";
import { POST as redeemInviteRoute } from "@/app/api/invite/redeem/route";

const RUN_ID = crypto.randomUUID();
const ISSUER = `https://oidc-forward-${RUN_ID}.example.test`;
const JWKS_URI = `${ISSUER}/jwks.json`;
const AUDIENCE = "nori-forward-test";
const RUNTIME_URL = `https://runtime-forward-${RUN_ID}.example.test`;
const APP_ORIGIN = "https://app.nori.example";
const SERVICE_KEY = "test-service-token-key-at-least-32-bytes-long!";

const ENV_KEYS = [
  "NODE_ENV",
  "NORI_CLOUD",
  "NORI_AUTH_PROVIDER",
  "NORI_OIDC_ISSUER",
  "NORI_OIDC_AUDIENCE",
  "NORI_APP_ORIGIN",
  "NORI_RUNTIME_URL",
  "NORI_LOCAL_AGENT_URL",
  "NORI_SERVICE_TOKEN_KEY",
  "NORI_SERVICE_AUDIENCE",
] as const;

let keypairPromise: Promise<{ privateKey: CryptoKey; jwks: { keys: Record<string, unknown>[] } }> | undefined;
function getKeys() {
  keypairPromise ??= (async () => {
    const { privateKey, publicKey } = await generateKeyPair("ES256");
    const jwk = await exportJWK(publicKey);
    return { privateKey, jwks: { keys: [{ ...jwk, kid: "fwd-k1", alg: "ES256", use: "sig" }] } };
  })();
  return keypairPromise;
}

async function authHeaders(email: string, extra: Record<string, string> = {}) {
  const { privateKey } = await getKeys();
  const token = await new SignJWT({ sub: email, email, email_verified: true })
    .setProtectedHeader({ alg: "ES256", kid: "fwd-k1" })
    .setIssuer(ISSUER)
    .setAudience(AUDIENCE)
    .setIssuedAt()
    .setExpirationTime("5m")
    .sign(privateKey);
  return {
    authorization: `Bearer ${token}`,
    origin: APP_ORIGIN,
    "sec-fetch-site": "same-origin",
    ...extra,
  };
}

const env = process.env as Record<string, string | undefined>;

describe("forward.ts path validation, body limits, OAuth sanitization, and shared read cache", () => {
  const savedEnv: Partial<Record<(typeof ENV_KEYS)[number], string | undefined>> = {};
  let origFetch: typeof globalThis.fetch;
  let origWarn: typeof console.warn;

  beforeEach(() => {
    for (const k of ENV_KEYS) savedEnv[k] = env[k];
    origFetch = globalThis.fetch;
    origWarn = console.warn;
    console.warn = () => undefined;

    env.NODE_ENV = "production";
    env.NORI_CLOUD = "aws";
    env.NORI_AUTH_PROVIDER = "oidc";
    env.NORI_OIDC_ISSUER = ISSUER;
    env.NORI_OIDC_AUDIENCE = AUDIENCE;
    env.NORI_APP_ORIGIN = APP_ORIGIN;
    env.NORI_RUNTIME_URL = RUNTIME_URL;
    env.NORI_SERVICE_TOKEN_KEY = SERVICE_KEY;
    env.NORI_SERVICE_AUDIENCE = "nori";
  });

  test("the Google Workspace browser callback routes without a Nori session and only returns safe redirects", async () => {
    const state = "gw1.signed-test-state";
    const callback = new URL("/api/v1/connections/oauth/callback", APP_ORIGIN);
    callback.search = new URLSearchParams({ state, code: "test-code", picked_file_ids: "drive-file", ignored: "extra" }).toString();
    let location = "nori://oauth/callback?result=drive_files_selected&connection=conn_google_workspace";
    let calls = 0;
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      calls++;
      const url = new URL(input instanceof Request ? input.url : input);
      assert.equal(url.origin, RUNTIME_URL);
      assert.equal(url.pathname, "/api/oauth/google-workspace/callback");
      assert.deepEqual(Object.fromEntries(url.searchParams), { state, code: "test-code", picked_file_ids: "drive-file" });
      const headers = new Headers(init?.headers);
      assert.match(headers.get("authorization") ?? "", /^Bearer /);
      assert.equal(headers.get("x-nori-redirect"), "json");
      return Response.json({ location });
    }) as typeof fetch;
    const request = () => assistantRoute(new Request(callback), "connections", ["oauth", "callback"]);
    const resolved = await request();
    assert.equal(resolved.status, 303);
    assert.equal(resolved.headers.get("location"), location);
    assert.equal(resolved.headers.get("cache-control"), "no-store");
    location = "https://elsewhere.example.test";
    assert.equal((await request()).headers.get("location"), "/apps?result=failed");
    assert.equal(calls, 2);
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
    console.warn = origWarn;
    for (const k of ENV_KEYS) {
      if (savedEnv[k] === undefined) delete env[k];
      else env[k] = savedEnv[k];
    }
  });

  async function mockUpstream(
    users: Record<string, string>,
    onRuntime: (url: URL, init?: RequestInit) => Response | Promise<Response>,
  ) {
    const { jwks } = await getKeys();
    globalThis.fetch = asFetch(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url === `${ISSUER}/.well-known/openid-configuration`) {
        return Response.json({ issuer: ISSUER, jwks_uri: JWKS_URI });
      }
      if (url === JWKS_URI) return Response.json(jwks);
      if (url === `${RUNTIME_URL}/api/users/resolve`) {
        const body = JSON.parse(String(init?.body ?? "{}")) as { email?: string };
        const id = body.email ? users[body.email] : undefined;
        return id ? Response.json({ id }) : Response.json({ error: "forbidden" }, { status: 403 });
      }
      if (url.startsWith(`${RUNTIME_URL}/users/`)) {
        return onRuntime(new URL(url), init);
      }
      throw new Error(`Unexpected fetch URL: ${url}`);
    });
  }

  test("validSegment and assistantRoute reject path traversal, encoded dots, invalid chars, and unknown families with 404", async () => {
    assert.equal(validSegment(""), false);
    assert.equal(validSegment("."), false);
    assert.equal(validSegment(".."), false);
    assert.equal(validSegment("%2e%2e"), false);
    assert.equal(validSegment("a/b"), false);
    assert.equal(validSegment("a\\b"), false);
    assert.equal(validSegment("bad space"), false);
    assert.equal(validSegment("a".repeat(201)), false);
    assert.equal(validSegment("a".repeat(200)), true);
    assert.equal(validSegment("task_123:rev@v1-ok.json"), true);

    const headers = { "sec-fetch-site": "same-origin" };
    for (const bad of [".", "..", "%2e%2e", "foo/bar", "bad?query", "a;b"]) {
      const res = await assistantRoute(new Request(`${APP_ORIGIN}/api/v1/work`, { headers }), "work", [bad]);
      assert.equal(res.status, 404, `expected 404 for segment ${bad}`);
    }

    const unknownFamily = await assistantRoute(new Request(`${APP_ORIGIN}/api/v1/evil`, { headers }), "evil", []);
    assert.equal(unknownFamily.status, 404);

    const tooDeep = await assistantRoute(new Request(`${APP_ORIGIN}/api/v1/work`, { headers }), "work", ["a", "b", "c", "d", "e"]);
    assert.equal(tooDeep.status, 404);

    const badMethod = await assistantRoute(new Request(`${APP_ORIGIN}/api/v1/work`, { method: "PATCH", headers }), "work", []);
    assert.equal(badMethod.status, 404);

    const badFilePath = await fileRoute(new Request(`${APP_ORIGIN}/api/v1/uploads/..`, { headers }), "v1/uploads/..");
    assert.equal(badFilePath.status, 404);
  });

  test("uploadRoute enforces 10 MB body limit via both Content-Length and streaming chunks (413)", async () => {
    const email = "upload-user@example.test";
    const userID = "22222222-2222-4222-8222-222222222201";
    let runtimeCalled = false;

    await mockUpstream({ [email]: userID }, async (_url, init) => {
      runtimeCalled = true;
      assert.equal(new Headers(init?.headers).get("x-nori-client"), "web");
      const buf = init?.body as ArrayBuffer;
      return Response.json({ id: "u1", size: buf.byteLength });
    });

    const tooBigHeader = new Request(`${APP_ORIGIN}/api/v1/uploads?id=u1&filename=big.bin`, {
      method: "POST",
      headers: await authHeaders(email, { "content-length": String(MAX_FILE_BYTES + 1) }),
      body: "tiny",
    });
    const headerRes = await uploadRoute(tooBigHeader);
    assert.equal(headerRes.status, 413);
    assert.equal(runtimeCalled, false);

    let cancelled = false;
    let chunksSent = 0;
    const hugeStream = new ReadableStream<Uint8Array>({
      pull(controller) {
        chunksSent++;
        controller.enqueue(new Uint8Array(1024 * 1024));
      },
      cancel() { cancelled = true; },
    });
    const streamReq = new Request(`${APP_ORIGIN}/api/v1/uploads?id=u2&filename=stream.bin`, {
      method: "POST",
      headers: await authHeaders(email, { "content-type": "application/octet-stream", "x-nori-client": "ios" }),
      body: hugeStream,
    });
    const streamRes = await uploadRoute(streamReq);
    assert.equal(streamRes.status, 413);
    assert.equal(cancelled, true);
    assert.equal(chunksSent >= 11 && chunksSent <= 12, true);
    assert.equal(runtimeCalled, false);

    const okReq = new Request(`${APP_ORIGIN}/api/v1/uploads?id=u3&filename=note.txt`, {
      method: "POST",
      headers: await authHeaders(email, { "content-type": "text/plain" }),
      body: "hello nori",
    });
    const okRes = await uploadRoute(okReq);
    assert.equal(okRes.status, 200);
    assert.equal(runtimeCalled, true);
    assert.deepEqual(await okRes.json(), { id: "u1", size: 10 });
  });

  test("uploads authenticate before reading the request body", async () => {
    const request = new Request(`${APP_ORIGIN}/api/v1/uploads?id=u1&filename=note.txt`, {
      method: "POST",
      headers: { origin: APP_ORIGIN, "sec-fetch-site": "same-origin" },
      body: "private upload",
    });
    const response = await uploadRoute(request);
    assert.equal(response.status, 403);
    assert.equal(request.bodyUsed, false);
  });

  test("upload buffering is bounded per process and slots are released", async () => {
    const email = "upload-concurrency@example.test";
    const userID = "22222222-2222-4222-8222-222222222212";
    await mockUpstream({ [email]: userID }, () => Response.json({ ok: true }));
    const headers = await authHeaders(email);
    const controllers: ReadableStreamDefaultController<Uint8Array>[] = [];
    const requests = Array.from({ length: 10 }, (_, index) => new Request(`${APP_ORIGIN}/api/v1/uploads?id=${index}`, {
      method: "POST",
      headers,
      body: new ReadableStream<Uint8Array>({ start(controller) { controllers.push(controller); } }),
    }));
    const pending = requests.map((request) => uploadRoute(request));
    try {
      for (let attempt = 0; attempt < 100 && !requests.every((request) => request.bodyUsed); attempt++) {
        await new Promise((resolve) => setTimeout(resolve, 0));
      }
      assert.equal(requests.every((request) => request.bodyUsed), true);
      const overflow = new Request(`${APP_ORIGIN}/api/v1/uploads?id=overflow`, { method: "POST", headers, body: "data" });
      assert.equal((await uploadRoute(overflow)).status, 429);
      assert.equal(overflow.bodyUsed, false);
    } finally {
      for (const controller of controllers) controller.close();
      await Promise.all(pending);
    }
    const afterRelease = new Request(`${APP_ORIGIN}/api/v1/uploads?id=after`, { method: "POST", headers, body: "data" });
    assert.equal((await uploadRoute(afterRelease)).status, 200);
  });

  test("invite redemption bounds declared and streamed bodies before parsing", async () => {
    const headers = { origin: APP_ORIGIN, "sec-fetch-site": "same-origin" };
    const oversized = new Request(`${APP_ORIGIN}/api/invite/redeem`, {
      method: "POST",
      headers: { ...headers, "content-length": "128001" },
      body: "{}",
    });
    assert.equal((await redeemInviteRoute(oversized)).status, 413);

    let cancelled = false;
    const stream = new ReadableStream<Uint8Array>({
      pull(controller) { controller.enqueue(new Uint8Array(64_000)); },
      cancel() { cancelled = true; },
    });
    const streamed = new Request(`${APP_ORIGIN}/api/invite/redeem`, {
      method: "POST",
      headers,
      body: stream,
    });
    assert.equal((await redeemInviteRoute(streamed)).status, 413);
    assert.equal(cancelled, true);

    const malformed = new Request(`${APP_ORIGIN}/api/invite/redeem`, {
      method: "POST",
      headers,
      body: "{invalid",
    });
    assert.equal((await redeemInviteRoute(malformed)).status, 400);
    const valid = new Request(`${APP_ORIGIN}/api/invite/redeem`, {
      method: "POST",
      headers,
      body: JSON.stringify({ invite_code: "NORI-TEST" }),
    });
    assert.equal((await redeemInviteRoute(valid)).status, 403);
  });

  test("assistantRoute and operationRoute enforce JSON body size caps (413) and reject malformed JSON (400)", async () => {
    const email = "json-cap-user@example.test";
    const userID = "22222222-2222-4222-8222-222222222202";
    await mockUpstream({ [email]: userID }, () => Response.json({ ok: true }));

    const hugeJson = JSON.stringify({ data: "x".repeat(130_000) });
    const bigAssistant = await assistantRoute(
      new Request(`${APP_ORIGIN}/api/v1/work`, {
        method: "POST",
        headers: await authHeaders(email, { "content-type": "application/json" }),
        body: hugeJson,
      }),
      "work",
      [],
    );
    assert.equal(bigAssistant.status, 413);

    const badJson = await assistantRoute(
      new Request(`${APP_ORIGIN}/api/v1/work`, {
        method: "POST",
        headers: await authHeaders(email, { "content-type": "application/json" }),
        body: "{not-json",
      }),
      "work",
      [],
    );
    assert.equal(badJson.status, 400);

    const unknownOp = await operationRoute(
      new Request(`${APP_ORIGIN}/api/nope`, { method: "POST", headers: await authHeaders(email), body: "{}" }),
      "nope",
    );
    assert.equal(unknownOp.status, 404);

    const bigOp = await operationRoute(
      new Request(`${APP_ORIGIN}/api/turn`, { method: "POST", headers: await authHeaders(email), body: hugeJson }),
      "turn",
    );
    assert.equal(bigOp.status, 413);

    const retryOp = await operationRoute(
      new Request(`${APP_ORIGIN}/api/retry`, { method: "POST", headers: await authHeaders(email), body: JSON.stringify({ turn_id: "t1" }) }),
      "retry",
    );
    assert.equal(retryOp.status, 200);
  });

  test("OAuth callback requires GET navigate and sanitizes unsafe redirect Locations", async () => {
    const email = "oauth-user@example.test";
    const userID = "22222222-2222-4222-8222-222222222203";
    let upstreamLocation: string | null = null;
    let jsonFallback = false;
    let throwError = false;

    await mockUpstream({ [email]: userID }, (url) => {
      assert.equal(url.searchParams.get("nori_redirect"), "json");
      if (throwError) throw new Error("network down");
      if (jsonFallback) return Response.json({ location: upstreamLocation });
      return new Response(null, { status: 303, headers: upstreamLocation ? { location: upstreamLocation } : {} });
    });

    const nonNav = await assistantRoute(
      new Request(`${APP_ORIGIN}/api/v1/connections/oauth/callback?code=1`, {
        method: "GET",
        headers: await authHeaders(email, { "sec-fetch-mode": "cors" }),
      }),
      "connections",
      ["oauth", "callback"],
    );
    assert.equal(nonNav.status, 403);

    upstreamLocation = "/apps?result=connected";
    const safeRes = await assistantRoute(
      new Request(`${APP_ORIGIN}/api/v1/connections/oauth/callback?code=1`, {
        method: "GET",
        headers: await authHeaders(email, { "sec-fetch-mode": "navigate" }),
      }),
      "connections",
      ["oauth", "callback"],
    );
    assert.equal(safeRes.status, 303);
    assert.equal(safeRes.headers.get("location"), "/apps?result=connected");

    jsonFallback = true;
    upstreamLocation = "/apps?result=json-ok";
    const jsonRes = await assistantRoute(
      new Request(`${APP_ORIGIN}/api/v1/connections/oauth/callback?code=2`, {
        method: "GET",
        headers: await authHeaders(email, { "sec-fetch-mode": "navigate" }),
      }),
      "connections",
      ["oauth", "callback"],
    );
    assert.equal(jsonRes.status, 303);
    assert.equal(jsonRes.headers.get("location"), "/apps?result=json-ok");

    jsonFallback = false;
    for (const unsafe of ["//evil.com/phish", "/\\evil.com/phish", "/apps\\evil", "https://evil.com/steal", "javascript:alert(1)"]) {
      upstreamLocation = unsafe;
      const res = await assistantRoute(
        new Request(`${APP_ORIGIN}/api/v1/connections/oauth/callback?code=3`, {
          method: "GET",
          headers: await authHeaders(email, { "sec-fetch-mode": "navigate" }),
        }),
        "connections",
        ["oauth", "callback"],
      );
      assert.equal(res.status, 303);
      assert.equal(res.headers.get("location"), "/apps?result=failed", `expected sanitized redirect for ${unsafe}`);
    }

    throwError = true;
    const errRes = await assistantRoute(
      new Request(`${APP_ORIGIN}/api/v1/connections/oauth/callback?code=4`, {
        method: "GET",
        headers: await authHeaders(email, { "sec-fetch-mode": "navigate" }),
      }),
      "connections",
      ["oauth", "callback"],
    );
    assert.equal(errRes.status, 303);
    assert.equal(errRes.headers.get("location"), "/apps?result=failed");
  });

  test("sharedRead caches 200 JSON reads, drops errors instead of caching failures, and clears on mutation", async () => {
    const email = "cache-user@example.test";
    const userID = crypto.randomUUID();
    let summaryCalls = 0;
    let mode: "error500" | "error429" | "html" | "ok" = "error500";

    await mockUpstream({ [email]: userID }, (url, init) => {
      if (init?.method === "POST") return Response.json({ ok: true });
      if (url.pathname.endsWith("/v1/integrations")) {
        summaryCalls++;
        if (mode === "error500") return Response.json({ error: "db glitch" }, { status: 500 });
        if (mode === "error429") return new Response("<html>private rate limit</html>", { status: 429, headers: { "retry-after": "5", "content-type": "text/html" } });
        if (mode === "html") return new Response("<html>oops</html>", { status: 200, headers: { "content-type": "text/html" } });
        return Response.json({ attention: summaryCalls });
      }
      return Response.json({ ok: true });
    });

    const getIntegrations = async () =>
      assistantRoute(
        new Request(`${APP_ORIGIN}/api/v1/integrations`, { headers: await authHeaders(email) }),
        "integrations",
        [],
      );

    const r1 = await getIntegrations();
    assert.equal(r1.status, 500);
    assert.equal(summaryCalls, 1);

    mode = "error429";
    const r2 = await getIntegrations();
    assert.equal(r2.status, 429);
    assert.equal(r2.headers.get("retry-after"), "5");
    assert.deepEqual(await r2.json(), { error: "Nori is busy. Try again shortly." });
    assert.equal(summaryCalls, 2);

    mode = "html";
    const r3 = await getIntegrations();
    assert.equal(r3.status, 502);
    assert.equal(summaryCalls, 3);

    mode = "ok";
    const r4 = await getIntegrations();
    assert.equal(r4.status, 200);
    assert.deepEqual(await r4.json(), { attention: 4 });

    const r5 = await getIntegrations();
    assert.equal(r5.status, 200);
    assert.deepEqual(await r5.json(), { attention: 4 });
    assert.equal(summaryCalls, 4);

    const mutateRes = await assistantRoute(
      new Request(`${APP_ORIGIN}/api/v1/work`, {
        method: "POST",
        headers: await authHeaders(email, { "content-type": "application/json" }),
        body: JSON.stringify({ action: "refresh" }),
      }),
      "work",
      [],
    );
    assert.equal(mutateRes.status, 200);

    const r6 = await getIntegrations();
    assert.equal(r6.status, 200);
    assert.deepEqual(await r6.json(), { attention: 5 });
    assert.equal(summaryCalls, 5);
  });

  test("worker changes are visible on the next pending, work, and summary reads", async () => {
    const email = "worker-refresh@example.test";
    const userID = crypto.randomUUID();
    let pending = false;
    await mockUpstream({ [email]: userID }, (url) => {
      if (url.pathname.endsWith("/v1/assistant/summary")) return Response.json({ needs_you: pending ? 1 : 0 });
      return Response.json({ items: pending ? [{ id: "worker-created" }] : [] });
    });
    const headers = await authHeaders(email);
    const read = async (path: string, family: string, segments: string[]) => {
      const response = await assistantRoute(new Request(`${APP_ORIGIN}/api/v1/${path}`, { headers }), family, segments);
      assert.equal(response.status, 200);
      return response.json();
    };

    assert.deepEqual(await read("interactions?status=pending", "interactions", []), { items: [] });
    assert.deepEqual(await read("work", "work", []), { items: [] });
    assert.deepEqual(await read("assistant/summary", "assistant", ["summary"]), { needs_you: 0 });

    pending = true;
    assert.deepEqual(await read("interactions?status=pending", "interactions", []), { items: [{ id: "worker-created" }] });
    assert.deepEqual(await read("work", "work", []), { items: [{ id: "worker-created" }] });
    assert.deepEqual(await read("assistant/summary", "assistant", ["summary"]), { needs_you: 1 });
  });

  test("sharedRead unblocks concurrent waiters cleanly when an in-flight read fails", async () => {
    const email = "cache-race-user@example.test";
    const userID = crypto.randomUUID();
    let calls = 0;
    let markFirstEntered!: () => void;
    const firstEntered = new Promise<void>((resolve) => { markFirstEntered = resolve; });
    let releaseFirst!: () => void;
    const firstGate = new Promise<void>((resolve) => { releaseFirst = resolve; });

    await mockUpstream({ [email]: userID }, async (url) => {
      if (url.pathname.endsWith("/v1/connections")) {
        return Response.json({ items: [] });
      }
      calls++;
      if (calls === 1) {
        markFirstEntered();
        await firstGate;
        throw new Error("upstream socket reset");
      }
      return Response.json({ recovered: true });
    });

    const headers = await authHeaders(email);
    const warm = await assistantRoute(new Request(`${APP_ORIGIN}/api/v1/connections`, { headers }), "connections", []);
    assert.equal(warm.status, 200);

    const first = assistantRoute(new Request(`${APP_ORIGIN}/api/v1/integrations`, { headers }), "integrations", []);
    await firstEntered;

    const second = assistantRoute(new Request(`${APP_ORIGIN}/api/v1/integrations`, { headers }), "integrations", []);
    await new Promise((resolve) => setTimeout(resolve, 25));
    assert.equal(calls, 1);

    releaseFirst();
    const [res1, res2] = await Promise.all([first, second]);
    assert.equal(res1.status, 502);
    assert.equal(res2.status, 200);
    assert.deepEqual(await res2.json(), { recovered: true });
    assert.equal(calls, 2);
  });

  test("assistantRoute proxies settings/model-keys GET, PUT, and DELETE to user-scoped runtime path", async () => {
    const email = "byok-user@example.test";
    const userID = "22222222-2222-4222-8222-222222222209";
    const seen: { method: string; pathname: string; body?: string }[] = [];

    await mockUpstream({ [email]: userID }, async (url, init) => {
      seen.push({
        method: init?.method ?? "GET",
        pathname: url.pathname,
        body: typeof init?.body === "string" ? init.body : undefined,
      });
      if (init?.method === "PUT") {
        return Response.json({
          provider: "vercel_ai_gateway",
          label: "Vercel AI Gateway",
          configured: true,
          last4: "a1b2",
        });
      }
      if (init?.method === "DELETE") {
        return Response.json({
          provider: "vercel_ai_gateway",
          label: "Vercel AI Gateway",
          configured: false,
        });
      }
      return Response.json({
        byok_required: true,
        providers: [{ provider: "vercel_ai_gateway", label: "Vercel AI Gateway", configured: false }],
      });
    });

    const headers = await authHeaders(email, { "content-type": "application/json" });
    const getRes = await assistantRoute(
      new Request(`${APP_ORIGIN}/api/v1/settings/model-keys`, { method: "GET", headers }),
      "settings",
      ["model-keys"],
    );
    assert.equal(getRes.status, 200);
    assert.equal((await getRes.json()).byok_required, true);

    const putRes = await assistantRoute(
      new Request(`${APP_ORIGIN}/api/v1/settings/model-keys/vercel_ai_gateway`, {
        method: "PUT",
        headers,
        body: JSON.stringify({ api_key: "vck_secret_a1b2" }),
      }),
      "settings",
      ["model-keys", "vercel_ai_gateway"],
    );
    assert.equal(putRes.status, 200);
    assert.equal((await putRes.json()).last4, "a1b2");

    const delRes = await assistantRoute(
      new Request(`${APP_ORIGIN}/api/v1/settings/model-keys/vercel_ai_gateway`, { method: "DELETE", headers }),
      "settings",
      ["model-keys", "vercel_ai_gateway"],
    );
    assert.equal(delRes.status, 200);
    assert.equal((await delRes.json()).configured, false);

    assert.deepEqual(seen, [
      { method: "GET", pathname: `/users/${userID}/v1/settings/model-keys`, body: undefined },
      {
        method: "PUT",
        pathname: `/users/${userID}/v1/settings/model-keys/vercel_ai_gateway`,
        body: JSON.stringify({ api_key: "vck_secret_a1b2" }),
      },
      { method: "DELETE", pathname: `/users/${userID}/v1/settings/model-keys/vercel_ai_gateway`, body: "{}" },
    ]);
  });

  test("settings route forwards model settings and only POST to reset", async () => {
    assert.equal(typeof settingsRoute.GET, "function");
    assert.equal(typeof settingsRoute.PUT, "function");
    assert.equal(typeof settingsRoute.DELETE, "function");
    assert.equal(typeof settingsRoute.POST, "function");

    const headers = { "sec-fetch-site": "same-origin" };
    const rootRes = await settingsRoute.GET(
      new Request(`${APP_ORIGIN}/api/v1/settings`, { method: "GET", headers }),
      { params: Promise.resolve({}) },
    );
    assert.equal(rootRes.status, 404);

    const otherRes = await settingsRoute.GET(
      new Request(`${APP_ORIGIN}/api/v1/settings/internal`, { method: "GET", headers }),
      { params: Promise.resolve({ path: ["internal"] }) },
    );
    assert.equal(otherRes.status, 404);

    const resetRead = await settingsRoute.GET(
      new Request(`${APP_ORIGIN}/api/v1/settings/reset`, { method: "GET", headers }),
      { params: Promise.resolve({ path: ["reset"] }) },
    );
    assert.equal(resetRead.status, 404);

    const email = "settings-route-user@example.test";
    const userID = "22222222-2222-4222-8222-222222222210";
    await mockUpstream({ [email]: userID }, () =>
      Response.json({ byok_required: false, providers: [] }),
    );
    const okRes = await settingsRoute.GET(
      new Request(`${APP_ORIGIN}/api/v1/settings/model-keys`, {
        method: "GET",
        headers: await authHeaders(email),
      }),
      { params: Promise.resolve({ path: ["model-keys"] }) },
    );
    assert.equal(okRes.status, 200);

    const modelsRes = await settingsRoute.GET(
      new Request(`${APP_ORIGIN}/api/v1/settings/models`, { method: "GET", headers: await authHeaders(email) }),
      { params: Promise.resolve({ path: ["models"] }) },
    );
    assert.equal(modelsRes.status, 200);

    const nestedModelsRes = await settingsRoute.GET(
      new Request(`${APP_ORIGIN}/api/v1/settings/models/extra`, { method: "GET", headers }),
      { params: Promise.resolve({ path: ["models", "extra"] }) },
    );
    assert.equal(nestedModelsRes.status, 404);
  });

  test("Home AI pairing preserves a bodyless 204 and rejects an unexpected response body", async () => {
    const email = "home-pairing@example.test";
    const userID = "22222222-2222-4222-8222-222222222213";
    const path = ["home-connectors", "pairing", "approve"];
    const headers = await authHeaders(email, { "content-type": "application/json" });
    let response = new Response(null, { status: 204, headers: { "content-type": "text/plain; charset=UTF-8" } });
    await mockUpstream({ [email]: userID }, (url) => {
      assert.equal(url.pathname, `/users/${userID}/v1/settings/${path.join("/")}`);
      return response;
    });
    const approve = () => settingsRoute.POST(
      new Request(`${APP_ORIGIN}/api/v1/settings/${path.join("/")}`, {
        method: "POST", headers, body: JSON.stringify({ user_code: "ABCD-EFGH" }),
      }),
      { params: Promise.resolve({ path }) },
    );

    const accepted = await approve();
    assert.equal(accepted.status, 204);
    assert.equal(accepted.body, null);
    assert.equal(accepted.headers.get("content-type"), null);

    response = new Response("unexpected", { headers: { "content-type": "text/plain" } });
    assert.equal((await approve()).status, 502);
  });

  test("settings reset forwards the confirmation to the signed-in user and relays 204", async () => {
    const email = "settings-reset-user@example.test";
    const userID = "22222222-2222-4222-8222-222222222211";
    let seen: { path: string; body?: string } | undefined;
    await mockUpstream({ [email]: userID }, (url, init) => {
      seen = { path: url.pathname, body: typeof init?.body === "string" ? init.body : undefined };
      return new Response(null, { status: 204 });
    });
    const response = await settingsRoute.POST(
      new Request(`${APP_ORIGIN}/api/v1/settings/reset`, {
        method: "POST", headers: await authHeaders(email, { "content-type": "application/json" }),
        body: JSON.stringify({ confirmation: "RESET" }),
      }),
      { params: Promise.resolve({ path: ["reset"] }) },
    );
    assert.equal(response.status, 204);
    assert.deepEqual(seen, { path: `/users/${userID}/v1/settings/reset`, body: JSON.stringify({ confirmation: "RESET" }) });
  });

  test("settings account delete forwards POST, evicts resolved user cache, clears session cookies, and rejects GET", async () => {
    const email = "settings+delete-user@example.test";
    const userID = "22222222-2222-4222-8222-222222222212";
    const activeUsers: Record<string, string> = { [email]: userID };
    let seen: { path: string; body?: string; issuer: string | null; subject: string | null; email: string | null } | undefined;

    await mockUpstream(activeUsers, (url, init) => {
      const headers = new Headers(init?.headers);
      seen = {
        path: url.pathname,
        body: typeof init?.body === "string" ? init.body : undefined,
        issuer: headers.get("x-nori-identity-issuer"),
        subject: headers.get("x-nori-identity-subject"),
        email: headers.get("x-nori-identity-email"),
      };
      delete activeUsers[email];
      return new Response(null, { status: 204 });
    });

    const getRes = await settingsRoute.GET(
      new Request(`${APP_ORIGIN}/api/v1/settings/account/delete`, {
        method: "GET",
        headers: await authHeaders(email),
      }),
      { params: Promise.resolve({ path: ["account", "delete"] }) },
    );
    assert.equal(getRes.status, 404);

    const response = await settingsRoute.POST(
      new Request(`${APP_ORIGIN}/api/v1/settings/account/delete`, {
        method: "POST",
        headers: await authHeaders(email, { "content-type": "application/json", "x-nori-identity-email": "spoofed@example.test" }),
        body: JSON.stringify({ confirmation: email }),
      }),
      { params: Promise.resolve({ path: ["account", "delete"] }) },
    );
    assert.equal(response.status, 204);
    assert.deepEqual(seen, {
      path: `/users/${userID}/v1/settings/account/delete`,
      body: JSON.stringify({ confirmation: email }),
      issuer: ISSUER,
      subject: email,
      email: encodeURIComponent(email),
    });
    const setCookie = response.headers.get("set-cookie") ?? "";
    assert.match(setCookie, /nori_signed_out=1/);

    // Resolved user cache must be evicted immediately so a subsequent request without the signed-out cookie is rejected.
    const afterDelete = await settingsRoute.GET(
      new Request(`${APP_ORIGIN}/api/v1/settings/model-keys`, {
        method: "GET",
        headers: await authHeaders(email),
      }),
      { params: Promise.resolve({ path: ["model-keys"] }) },
    );
    assert.equal(afterDelete.status, 403);
  });
});
