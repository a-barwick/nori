import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { exportJWK, generateKeyPair, SignJWT } from "jose";
import { accessFailureResponse, resolveUser, sameOrigin, sameOriginRead } from "./auth";
import { oidcIdentity } from "./oidc";
import { runtimeTarget } from "./target";

const RUN_ID = crypto.randomUUID();
const ISSUER = `https://oidc-auth-${RUN_ID}.example.test`;
const JWKS_URI = `${ISSUER}/jwks.json`;
const AUDIENCE = "nori-web-test";
const RUNTIME_URL = `https://runtime-auth-${RUN_ID}.example.test`;
const SERVICE_KEY = "test-service-token-key-at-least-32-bytes-long!";

const ENV_KEYS = [
  "NODE_ENV",
  "NORI_CLOUD",
  "NORI_AUTH_PROVIDER",
  "NORI_OIDC_ISSUER",
  "NORI_OIDC_AUDIENCE",
  "NORI_OIDC_COOKIE",
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
    return { privateKey, jwks: { keys: [{ ...jwk, kid: "auth-k1", alg: "ES256", use: "sig" }] } };
  })();
  return keypairPromise;
}

async function signIDToken(claims: Record<string, unknown>, opts: { issuer?: string; audience?: string | string[]; expiresIn?: string } = {}) {
  const { privateKey } = await getKeys();
  return new SignJWT(claims)
    .setProtectedHeader({ alg: "ES256", kid: "auth-k1" })
    .setIssuer(opts.issuer ?? ISSUER)
    .setAudience(opts.audience ?? AUDIENCE)
    .setIssuedAt()
    .setExpirationTime(opts.expiresIn ?? "5m")
    .sign(privateKey);
}

const env = process.env as Record<string, string | undefined>;

describe("sameOrigin and sameOriginRead CSRF checks", () => {
  const savedEnv: Partial<Record<(typeof ENV_KEYS)[number], string | undefined>> = {};

  beforeEach(() => {
    for (const k of ENV_KEYS) savedEnv[k] = env[k];
    env.NODE_ENV = "production";
    env.NORI_APP_ORIGIN = "https://app.nori.example";
  });

  afterEach(() => {
    for (const k of ENV_KEYS) {
      if (savedEnv[k] === undefined) delete env[k];
      else env[k] = savedEnv[k];
    }
  });

  test("sameOrigin requires Origin matching expected app origin and rejects cross-site Sec-Fetch-Site", () => {
    const ok = new Request("https://app.nori.example/api/turn", {
      method: "POST",
      headers: { origin: "https://app.nori.example", "sec-fetch-site": "same-origin" },
    });
    assert.equal(sameOrigin(ok), true);

    const noOrigin = new Request("https://app.nori.example/api/turn", {
      method: "POST",
      headers: { "sec-fetch-site": "same-origin" },
    });
    assert.equal(sameOrigin(noOrigin), false);

    const wrongOrigin = new Request("https://app.nori.example/api/turn", {
      method: "POST",
      headers: { origin: "https://evil.example", "sec-fetch-site": "same-origin" },
    });
    assert.equal(sameOrigin(wrongOrigin), false);

    const crossSite = new Request("https://app.nori.example/api/turn", {
      method: "POST",
      headers: { origin: "https://app.nori.example", "sec-fetch-site": "cross-site" },
    });
    assert.equal(sameOrigin(crossSite), false);

    const sameSite = new Request("https://app.nori.example/api/turn", {
      method: "POST",
      headers: { origin: "https://app.nori.example", "sec-fetch-site": "same-site" },
    });
    assert.equal(sameOrigin(sameSite), false);
  });

  test("sameOrigin falls back to https://Host in production and http://Host in local development", () => {
    delete process.env.NORI_APP_ORIGIN;
    const prodHttps = new Request("https://nori.example/api/turn", {
      method: "POST",
      headers: { host: "nori.example", origin: "https://nori.example" },
    });
    assert.equal(sameOrigin(prodHttps), true);

    const prodHttp = new Request("https://nori.example/api/turn", {
      method: "POST",
      headers: { host: "nori.example", origin: "http://nori.example" },
    });
    assert.equal(sameOrigin(prodHttp), false);

    env.NODE_ENV = "development";
    const localHttp = new Request("http://127.0.0.1:3210/api/turn", {
      method: "POST",
      headers: { host: "127.0.0.1:3210", origin: "http://127.0.0.1:3210" },
    });
    assert.equal(sameOrigin(localHttp), true);
  });

  test("sameOriginRead strictly requires Sec-Fetch-Site: same-origin", () => {
    assert.equal(sameOriginRead(new Request("https://app.nori.example/api/v1/work", { headers: { "sec-fetch-site": "same-origin" } })), true);
    assert.equal(sameOriginRead(new Request("https://app.nori.example/api/v1/work", { headers: { "sec-fetch-site": "cross-site" } })), false);
    assert.equal(sameOriginRead(new Request("https://app.nori.example/api/v1/work", { headers: { "sec-fetch-site": "none" } })), false);
    assert.equal(sameOriginRead(new Request("https://app.nori.example/api/v1/work")), false);
  });
});

describe("oidcIdentity and resolveUser", () => {
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
    env.NORI_OIDC_COOKIE = "nori_id_token";
    env.NORI_RUNTIME_URL = RUNTIME_URL;
    env.NORI_SERVICE_TOKEN_KEY = SERVICE_KEY;
    env.NORI_SERVICE_AUDIENCE = "nori";
  });

  afterEach(() => {
    globalThis.fetch = origFetch;
    console.warn = origWarn;
    for (const k of ENV_KEYS) {
      if (savedEnv[k] === undefined) delete env[k];
      else env[k] = savedEnv[k];
    }
  });

  async function installOIDCFetch(onResolve?: (req: Request) => Response | Promise<Response>) {
    const { jwks } = await getKeys();
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url === `${ISSUER}/.well-known/openid-configuration`) {
        return Response.json({ issuer: ISSUER, jwks_uri: JWKS_URI });
      }
      if (url === JWKS_URI) {
        return Response.json(jwks);
      }
      if (url.startsWith(`${RUNTIME_URL}/api/users/resolve`) && onResolve) {
        return onResolve(new Request(url, init));
      }
      throw new Error(`Unexpected fetch URL: ${url}`);
    }) as unknown as typeof fetch;
  }

  test("verifies valid Bearer and HttpOnly cookie ID tokens and rejects unverified or non-ID tokens", async () => {
    await installOIDCFetch();

    const valid = await signIDToken({ sub: "user-1", email: "  Alice@Example.COM ", email_verified: true, token_use: "id" });
    assert.deepEqual(await oidcIdentity(new Headers({ authorization: `Bearer ${valid}` })), { email: "alice@example.com" });
    assert.deepEqual(
      await oidcIdentity(new Headers({ cookie: `other=1; nori_id_token=${encodeURIComponent(valid)}` })),
      { email: "alice@example.com" },
    );

    // Reject unverified email
    const unverified = await signIDToken({ sub: "user-2", email: "bob@example.com", email_verified: false });
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${unverified}` })), null);

    // Reject access token (token_use !== "id")
    const accessToken = await signIDToken({ sub: "user-3", email: "bob@example.com", email_verified: true, token_use: "access" });
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${accessToken}` })), null);

    // Reject multi-audience token when azp does not match audience
    const wrongAzp = await signIDToken(
      { sub: "user-4", email: "bob@example.com", email_verified: true, azp: "other-client" },
      { audience: [AUDIENCE, "second-client"] },
    );
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${wrongAzp}` })), null);

    // Reject symmetric HS256 token
    const symmetric = await new SignJWT({ sub: "user-5", email: "bob@example.com", email_verified: true })
      .setProtectedHeader({ alg: "HS256" })
      .setIssuer(ISSUER)
      .setAudience(AUDIENCE)
      .setIssuedAt()
      .setExpirationTime("5m")
      .sign(new TextEncoder().encode(SERVICE_KEY));
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${symmetric}` })), null);

    // Reject oversized token (>16 KB)
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${"a".repeat(16_400)}` })), null);
  });

  test("resolveUser caches valid UUID resolutions and handles 403, 429 Retry-After, and invalid IDs", async () => {
    let resolveCalls = 0;
    const validUUID = "11111111-1111-4111-8111-111111111111";

    await installOIDCFetch(async (req) => {
      resolveCalls++;
      assert.equal(req.headers.get("authorization")?.startsWith("Bearer "), true);
      const body = await req.json() as { email: string };
      if (body.email === "cached@example.test") return Response.json({ id: validUUID });
      if (body.email === "denied@example.test") return Response.json({ error: "no access" }, { status: 403 });
      if (body.email === "busy@example.test") return new Response("", { status: 429, headers: { "retry-after": "15" } });
      if (body.email === "busy-bad-retry@example.test") return new Response("", { status: 429, headers: { "retry-after": "not-a-number" } });
      if (body.email === "bad-id@example.test") return Response.json({ id: "../escape" });
      return Response.json({ error: "boom" }, { status: 500 });
    });

    // Missing token -> 403 without hitting runtime
    const unauth = await resolveUser(new Headers());
    assert.equal("status" in unauth && unauth.status, 403);
    assert.equal(resolveCalls, 0);

    // Valid user -> resolved once, then served from TTL cache
    const okToken = await signIDToken({ sub: "s1", email: "cached@example.test", email_verified: true });
    const first = await resolveUser(new Headers({ authorization: `Bearer ${okToken}` }));
    assert.equal("userID" in first && first.userID, validUUID);
    const second = await resolveUser(new Headers({ authorization: `Bearer ${okToken}` }));
    assert.equal("userID" in second && second.userID, validUUID);
    assert.equal(resolveCalls, 1);

    // 403 from runtime -> denied
    const deniedToken = await signIDToken({ sub: "s2", email: "denied@example.test", email_verified: true });
    const denied = await resolveUser(new Headers({ authorization: `Bearer ${deniedToken}` }));
    assert.equal("status" in denied && denied.status, 403);

    // 429 with valid numeric Retry-After -> preserved
    const busyToken = await signIDToken({ sub: "s3", email: "busy@example.test", email_verified: true });
    const busy = await resolveUser(new Headers({ authorization: `Bearer ${busyToken}` }));
    assert.equal("status" in busy && busy.status, 429);
    assert.equal("retryAfter" in busy && busy.retryAfter, "15");
    const busyRes = accessFailureResponse(busy as { status: 429; error: string; retryAfter?: string });
    assert.equal(busyRes.status, 429);
    assert.equal(busyRes.headers.get("retry-after"), "15");
    assert.equal(busyRes.headers.get("cache-control"), "no-store");

    // 429 with invalid Retry-After -> sanitized away
    const badRetryToken = await signIDToken({ sub: "s4", email: "busy-bad-retry@example.test", email_verified: true });
    const badRetry = await resolveUser(new Headers({ authorization: `Bearer ${badRetryToken}` }));
    assert.equal("status" in badRetry && badRetry.status, 429);
    assert.equal("retryAfter" in badRetry && badRetry.retryAfter, undefined);

    // Invalid user ID format returned by runtime -> 503 unavailable
    const badIdToken = await signIDToken({ sub: "s5", email: "bad-id@example.test", email_verified: true });
    const badId = await resolveUser(new Headers({ authorization: `Bearer ${badIdToken}` }));
    assert.equal("status" in badId && badId.status, 503);
  });

  test("runtimeTarget enforces https in hosted mode and loopback http in local mode", () => {
    process.env.NORI_RUNTIME_URL = "http://insecure.example";
    const hostedHttp = runtimeTarget("hosted");
    assert.equal(hostedHttp instanceof Response && hostedHttp.status, 503);

    process.env.NORI_LOCAL_AGENT_URL = "http://10.0.0.5:8080";
    const localNonLoopback = runtimeTarget("local");
    assert.equal(localNonLoopback instanceof Response && localNonLoopback.status, 503);

    process.env.NORI_LOCAL_AGENT_URL = "http://127.0.0.1:8080";
    const localOk = runtimeTarget("local");
    assert.equal(localOk instanceof Response, false);
  });
});
