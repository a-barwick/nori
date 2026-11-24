import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { exportJWK, generateKeyPair, SignJWT } from "jose";
import { NextRequest } from "next/server";
import { config, proxy } from "./proxy";

const RUN_ID = crypto.randomUUID();
const ISSUER = `https://oidc-proxy-${RUN_ID}.example.test`;
const JWKS_URI = `${ISSUER}/jwks.json`;
const AUDIENCE = "nori-proxy-test";
const RUNTIME_URL = `https://runtime-proxy-${RUN_ID}.example.test`;
const SERVICE_KEY = "test-service-token-key-at-least-32-bytes-long!";

const ENV_KEYS = [
  "NODE_ENV",
  "NORI_CLOUD",
  "NORI_AUTH_PROVIDER",
  "NORI_OIDC_ISSUER",
  "NORI_OIDC_AUDIENCE",
  "NORI_RUNTIME_URL",
  "NORI_SERVICE_TOKEN_KEY",
  "NORI_SERVICE_AUDIENCE",
] as const;

let keypairPromise: Promise<{ privateKey: CryptoKey; jwks: { keys: Record<string, unknown>[] } }> | undefined;
function getKeys() {
  keypairPromise ??= (async () => {
    const { privateKey, publicKey } = await generateKeyPair("ES256");
    const jwk = await exportJWK(publicKey);
    return { privateKey, jwks: { keys: [{ ...jwk, kid: "proxy-k1", alg: "ES256", use: "sig" }] } };
  })();
  return keypairPromise;
}

async function bearerFor(email: string) {
  const { privateKey } = await getKeys();
  return new SignJWT({ sub: email, email, email_verified: true })
    .setProtectedHeader({ alg: "ES256", kid: "proxy-k1" })
    .setIssuer(ISSUER)
    .setAudience(AUDIENCE)
    .setIssuedAt()
    .setExpirationTime("5m")
    .sign(privateKey);
}

describe("proxy matcher exemptions", () => {
  const matcher = new RegExp(`^${config.matcher[0]}$`);

  test("exempts /sentry-tunnel, /api/* (including /api/health), static assets, and favicon", () => {
    for (const exempt of [
      "/sentry-tunnel",
      "/api/health",
      "/api/v1/snapshot",
      "/api/turn",
      "/_next/static/chunks/main.js",
      "/_next/image",
      "/favicon.ico",
    ]) {
      assert.equal(matcher.test(exempt), false, `expected ${exempt} to bypass proxy`);
    }
  });

  test("protects application routes and non-exact sentry-tunnel paths", () => {
    for (const guarded of ["/", "/work", "/apps", "/library", "/sentry-tunnel/extra", "/api"]) {
      assert.equal(matcher.test(guarded), true, `expected ${guarded} to be guarded by proxy`);
    }
  });
});

const env = process.env as Record<string, string | undefined>;

describe("proxy auth gate and header stripping", () => {
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

  async function mockResolve(handler: (email: string) => Response) {
    const { jwks } = await getKeys();
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url === `${ISSUER}/.well-known/openid-configuration`) {
        return Response.json({ issuer: ISSUER, jwks_uri: JWKS_URI });
      }
      if (url === JWKS_URI) return Response.json(jwks);
      if (url === `${RUNTIME_URL}/api/users/resolve`) {
        const body = JSON.parse(String(init?.body ?? "{}")) as { email?: string };
        return handler(body.email ?? "");
      }
      throw new Error(`Unexpected fetch URL: ${url}`);
    }) as unknown as typeof fetch;
  }

  test("returns plain-text 403 for unauthenticated document requests (never reconnecting page)", async () => {
    await mockResolve(() => Response.json({ error: "denied" }, { status: 403 }));
    const req = new NextRequest("https://app.nori.example/work", {
      headers: { "sec-fetch-dest": "document" },
    });
    const res = await proxy(req);
    assert.equal(res.status, 403);
    assert.equal(res.headers.get("cache-control"), "no-store");
    assert.equal((res.headers.get("content-type") ?? "").includes("text/html"), false);
    assert.equal((await res.text()).includes("Sign in"), true);
  });

  test("returns HTML reconnecting page on 503/429 document navigation and plain text on RSC requests", async () => {
    await mockResolve((email) => {
      if (email === "busy@example.test") return new Response("", { status: 429, headers: { "retry-after": "12" } });
      return new Response("", { status: 503 });
    });

    const downToken = await bearerFor("down@example.test");
    const doc503 = await proxy(new NextRequest("https://app.nori.example/", {
      headers: { authorization: `Bearer ${downToken}`, "sec-fetch-dest": "document" },
    }));
    assert.equal(doc503.status, 503);
    assert.equal(doc503.headers.get("content-type"), "text/html; charset=utf-8");
    assert.equal((await doc503.text()).includes("Reconnecting to Nori"), true);

    // RSC navigation must receive plain text, not the full HTML reconnecting shell
    const rsc503 = await proxy(new NextRequest("https://app.nori.example/", {
      headers: { authorization: `Bearer ${downToken}`, "sec-fetch-dest": "document", rsc: "1" },
    }));
    assert.equal(rsc503.status, 503);
    assert.equal((rsc503.headers.get("content-type") ?? "").includes("text/html"), false);
    assert.equal((await rsc503.text()).includes("<!doctype html>"), false);

    // 429 document navigation renders busy HTML page with Retry-After preserved
    const busyToken = await bearerFor("busy@example.test");
    const doc429 = await proxy(new NextRequest("https://app.nori.example/", {
      headers: { authorization: `Bearer ${busyToken}`, "sec-fetch-dest": "document" },
    }));
    assert.equal(doc429.status, 429);
    assert.equal(doc429.headers.get("retry-after"), "12");
    assert.equal((await doc429.text()).includes("Nori is busy"), true);
  });

  test("strips spoofed x-nori-user and x-serverless-authorization headers on authenticated pass-through", async () => {
    await mockResolve(() => Response.json({ id: "33333333-3333-4333-8333-333333333333" }));
    const token = await bearerFor("ok@example.test");
    const req = new NextRequest("https://app.nori.example/work", {
      headers: {
        authorization: `Bearer ${token}`,
        "x-nori-user": "spoofed-admin",
        "x-serverless-authorization": "Bearer spoofed-iap",
        "x-custom-ok": "keep-me",
      },
    });
    const res = await proxy(req);
    assert.equal(res.headers.get("x-middleware-next"), "1");
    assert.equal(res.headers.get("x-middleware-request-x-nori-user"), null);
    assert.equal(res.headers.get("x-middleware-request-x-serverless-authorization"), null);
    assert.equal(res.headers.get("x-middleware-request-x-custom-ok"), "keep-me");
  });
});
