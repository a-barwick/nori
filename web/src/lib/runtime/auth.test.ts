import assert from "node:assert/strict";
import { afterEach, beforeEach, describe, test } from "node:test";
import { exportJWK, generateKeyPair, SignJWT } from "jose";
import { accessFailureResponse, redeemInvite, resolveUser, sameOrigin, sameOriginRead } from "./auth";
import { oidcIdentity } from "./oidc";
import { iapIdentity } from "./iap";
import { GOOGLE_ISSUER } from "./identity";
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
  "NORI_OIDC_IAP_POLICY_RESOURCE",
  "NORI_OIDC_IAP_ALLOWED_IDENTITIES",
  "NORI_APP_ORIGIN",
  "NORI_RUNTIME_URL",
  "NORI_LOCAL_AGENT_URL",
  "NORI_SERVICE_TOKEN_KEY",
  "NORI_SERVICE_AUDIENCE",
  "IAP_AUDIENCE",
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
    delete env.NORI_OIDC_IAP_POLICY_RESOURCE;
    delete env.NORI_OIDC_IAP_ALLOWED_IDENTITIES;
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

  async function installOIDCFetch(onResolve?: (req: Request) => Response | Promise<Response>, issuer = ISSUER) {
    const { jwks } = await getKeys();
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url === `${issuer}/.well-known/openid-configuration`) {
        return Response.json({ issuer, jwks_uri: JWKS_URI });
      }
      if (url === JWKS_URI) {
        return Response.json(jwks);
      }
      if (url.startsWith(`${RUNTIME_URL}/api/users/resolve`) && onResolve) {
        return onResolve(new Request(url, init));
      }
      if (url.startsWith(`${RUNTIME_URL}/api/users/redeem`) && onResolve) {
        return onResolve(new Request(url, init));
      }
      throw new Error(`Unexpected fetch URL: ${url}`);
    }) as unknown as typeof fetch;
  }

  test("verifies valid Bearer and HttpOnly cookie ID tokens and rejects unverified or non-ID tokens", async () => {
    await installOIDCFetch();

    const valid = await signIDToken({ sub: "user-1", email: "  Alice@Example.COM ", email_verified: true, token_use: "id" });
    const identity = { email: "alice@example.com", issuer: ISSUER, subject: "user-1", emailAuthoritative: false };
    assert.deepEqual(await oidcIdentity(new Headers({ authorization: `Bearer ${valid}` })), identity);
    assert.deepEqual(
      await oidcIdentity(new Headers({ cookie: `other=1; nori_id_token=${encodeURIComponent(valid)}` })),
      identity,
    );

    const unverified = await signIDToken({ sub: "user-2", email: "bob@example.com", email_verified: false });
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${unverified}` })), null);

    const accessToken = await signIDToken({ sub: "user-3", email: "bob@example.com", email_verified: true, token_use: "access" });
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${accessToken}` })), null);

    const wrongAzp = await signIDToken(
      { sub: "user-4", email: "bob@example.com", email_verified: true, azp: "other-client" },
      { audience: [AUDIENCE, "second-client"] },
    );
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${wrongAzp}` })), null);

    const symmetric = await new SignJWT({ sub: "user-5", email: "bob@example.com", email_verified: true })
      .setProtectedHeader({ alg: "HS256" })
      .setIssuer(ISSUER)
      .setAudience(AUDIENCE)
      .setIssuedAt()
      .setExpirationTime("5m")
      .sign(new TextEncoder().encode(SERVICE_KEY));
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${symmetric}` })), null);

    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${"a".repeat(16_400)}` })), null);
  });

  test("uses signed Google identity and only marks Google-hosted email authoritative", async () => {
    env.NORI_OIDC_ISSUER = GOOGLE_ISSUER;
    await installOIDCFetch(undefined, GOOGLE_ISSUER);
    for (const [email, hd, authoritative] of [
      ["person@gmail.com", undefined, true],
      ["person@workspace.test", "workspace.test", true],
      ["person@workspace.test", "other.test", false],
      ["person@thirdparty.test", undefined, false],
    ] as const) {
      const token = await signIDToken({ sub: "google-user", email, email_verified: true, ...(hd ? { hd } : {}) }, { issuer: GOOGLE_ISSUER });
      const identity = await oidcIdentity(new Headers({ authorization: `Bearer ${token}` }));
      assert.equal(identity?.issuer, GOOGLE_ISSUER);
      assert.equal(identity?.subject, "google-user");
      assert.equal(identity?.emailAuthoritative, authoritative);
    }
  });

  test("enforces the Mac IAP policy before returning a verified principal", async () => {
    env.NORI_OIDC_ISSUER = GOOGLE_ISSUER;
    await installOIDCFetch(undefined, GOOGLE_ISSUER);
    const oidcFetch = globalThis.fetch;
    const resource = `projects/${Date.now()}/iap_web/cloud_run-us-central1/services/nori-web`;
    env.NORI_OIDC_IAP_POLICY_RESOURCE = resource;
    env.NORI_OIDC_IAP_ALLOWED_IDENTITIES = JSON.stringify({
      "123": "allowed@gmail.com", "456": "staff@workspace.test", "789": "person@thirdparty.test",
    });
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.startsWith("http://metadata.google.internal/")) {
        return Response.json({ access_token: "test-policy-token" });
      }
      if (url === `https://iap.googleapis.com/v1/${resource}:getIamPolicy`) {
        return Response.json({ bindings: [{ role: "roles/iap.httpsResourceAccessor", members: [
          "user:allowed@gmail.com", "user:staff@workspace.test", "user:person@thirdparty.test",
        ] }] });
      }
      return oidcFetch(input, init);
    }) as unknown as typeof fetch;

    const allowed = await signIDToken({ sub: "123", email: "allowed@gmail.com", email_verified: true }, { issuer: GOOGLE_ISSUER });
    const denied = await signIDToken({ sub: "other-subject", email: "allowed@gmail.com", email_verified: true }, { issuer: GOOGLE_ISSUER });
    const workspace = await signIDToken({ sub: "456", email: "staff@workspace.test", email_verified: true, hd: "workspace.test" }, { issuer: GOOGLE_ISSUER });
    const thirdParty = await signIDToken({ sub: "789", email: "person@thirdparty.test", email_verified: true }, { issuer: GOOGLE_ISSUER });
    assert.deepEqual(await oidcIdentity(new Headers({ authorization: `Bearer ${allowed}` })), {
      email: "allowed@gmail.com", issuer: GOOGLE_ISSUER, subject: "123", emailAuthoritative: true,
    });
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${denied}` })), null);
    assert.equal((await oidcIdentity(new Headers({ authorization: `Bearer ${workspace}` })))?.subject, "456");
    assert.equal((await oidcIdentity(new Headers({ authorization: `Bearer ${thirdParty}` })))?.subject, "789");
    delete env.NORI_OIDC_IAP_ALLOWED_IDENTITIES;
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${allowed}` })), null);
    env.NORI_OIDC_IAP_ALLOWED_IDENTITIES = JSON.stringify({ "123": "allowed@gmail.com" });
    delete env.NORI_OIDC_IAP_POLICY_RESOURCE;
    assert.equal(await oidcIdentity(new Headers({ authorization: `Bearer ${allowed}` })), null);
  });

  test("normalizes signed IAP Google subjects without trusting external identities as Google", async () => {
    const audience = "/projects/example/global/backendServices/example";
    env.IAP_AUDIENCE = audience;
    const { privateKey, jwks } = await getKeys();
    globalThis.fetch = (async () => Response.json(jwks)) as unknown as typeof fetch;
    async function signedIap(claims: Record<string, unknown>) {
      const token = await new SignJWT(claims)
        .setProtectedHeader({ alg: "ES256", kid: "auth-k1" })
        .setIssuer("https://cloud.google.com/iap")
        .setAudience(audience)
        .setIssuedAt()
        .setExpirationTime("5m")
        .sign(privateKey);
      return iapIdentity(new Headers({ "x-goog-iap-jwt-assertion": token }));
    }
    assert.deepEqual(await signedIap({ sub: "accounts.google.com:12345", email: "User@gmail.com" }), {
      issuer: GOOGLE_ISSUER, subject: "12345", email: "user@gmail.com", emailAuthoritative: true,
    });
    assert.deepEqual(await signedIap({ sub: "accounts.google.com:12345", email: "user@workspace.test" }), {
      issuer: GOOGLE_ISSUER, subject: "12345", email: "user@workspace.test", emailAuthoritative: false,
    });
    assert.deepEqual(await signedIap({ sub: "accounts.google.com:12345", email: "user@workspace.test", hd: "workspace.test" }), {
      issuer: GOOGLE_ISSUER, subject: "12345", email: "user@workspace.test", emailAuthoritative: true,
    });
    assert.deepEqual(await signedIap({ sub: "accounts.google.com:12345", email: "user@workspace.test", hd: "other.test" }), {
      issuer: GOOGLE_ISSUER, subject: "12345", email: "user@workspace.test", emailAuthoritative: false,
    });
    assert.deepEqual(await signedIap({ sub: "sts.google.com:12345", email: "user@gmail.com", identity_source: "WORKFORCE_IDENTITY" }), {
      issuer: "https://cloud.google.com/iap", subject: "sts.google.com:12345", email: "user@gmail.com", emailAuthoritative: false,
    });
    assert.equal(await signedIap({ sub: "accounts.google.com:", email: "user@gmail.com" }), null);
  });

  test("resolveUser caches valid UUID resolutions and handles 403, 429 Retry-After, and invalid IDs", async () => {
    let resolveCalls = 0;
    const validUUID = "11111111-1111-4111-8111-111111111111";

    await installOIDCFetch(async (req) => {
      resolveCalls++;
      assert.equal(req.headers.get("authorization")?.startsWith("Bearer "), true);
      const body = await req.json() as { email: string };
      if (body.email === "cached@example.test") return Response.json({ id: validUUID }, { headers: { "X-Nori-Identity-Bound": "1" } });
      if (body.email === "denied@example.test") return Response.json({ error: "no access" }, { status: 403 });
      if (body.email === "busy@example.test") return new Response("", { status: 429, headers: { "retry-after": "15" } });
      if (body.email === "busy-bad-retry@example.test") return new Response("", { status: 429, headers: { "retry-after": "not-a-number" } });
      if (body.email === "bad-id@example.test") return Response.json({ id: "../escape" });
      return Response.json({ error: "boom" }, { status: 500 });
    });

    const unauth = await resolveUser(new Headers());
    assert.equal("status" in unauth && unauth.status, 403);
    assert.equal(resolveCalls, 0);

    const okToken = await signIDToken({ sub: "s1", email: "cached@example.test", email_verified: true });
    const first = await resolveUser(new Headers({ authorization: `Bearer ${okToken}` }));
    assert.equal("userID" in first && first.userID, validUUID);
    const second = await resolveUser(new Headers({ authorization: `Bearer ${okToken}` }));
    assert.equal("userID" in second && second.userID, validUUID);
    assert.equal(resolveCalls, 1);

    const deniedToken = await signIDToken({ sub: "s2", email: "denied@example.test", email_verified: true });
    const denied = await resolveUser(new Headers({ authorization: `Bearer ${deniedToken}` }));
    assert.equal("status" in denied && denied.status, 403);

    const busyToken = await signIDToken({ sub: "s3", email: "busy@example.test", email_verified: true });
    const busy = await resolveUser(new Headers({ authorization: `Bearer ${busyToken}` }));
    assert.equal("status" in busy && busy.status, 429);
    assert.equal("retryAfter" in busy && busy.retryAfter, "15");
    const busyRes = accessFailureResponse(busy as { status: 429; error: string; retryAfter?: string });
    assert.equal(busyRes.status, 429);
    assert.equal(busyRes.headers.get("retry-after"), "15");
    assert.equal(busyRes.headers.get("cache-control"), "no-store");

    const badRetryToken = await signIDToken({ sub: "s4", email: "busy-bad-retry@example.test", email_verified: true });
    const badRetry = await resolveUser(new Headers({ authorization: `Bearer ${badRetryToken}` }));
    assert.equal("status" in badRetry && badRetry.status, 429);
    assert.equal("retryAfter" in badRetry && badRetry.retryAfter, undefined);

    const badIdToken = await signIDToken({ sub: "s5", email: "bad-id@example.test", email_verified: true });
    const badId = await resolveUser(new Headers({ authorization: `Bearer ${badIdToken}` }));
    assert.equal("status" in badId && badId.status, 503);
  });

  test("forwards the verified subject and only caches identity-bound resolutions", async () => {
    const seen: Array<{ issuer: string | null; subject: string | null; authoritative: string | null }> = [];
    let enforceBinding = false;
    await installOIDCFetch((req) => {
      seen.push({
        issuer: req.headers.get("x-nori-identity-issuer"),
        subject: req.headers.get("x-nori-identity-subject"),
        authoritative: req.headers.get("x-nori-identity-email-authoritative"),
      });
      const id = req.headers.get("x-nori-identity-subject") === "cache-subject-b"
        ? "33333333-3333-4333-8333-333333333333"
        : "44444444-4444-4444-8444-444444444444";
      return Response.json({ id }, { headers: enforceBinding ? { "X-Nori-Identity-Bound": "1" } : {} });
    });
    const firstToken = await signIDToken({ sub: "cache-subject-a", email: "same@example.test", email_verified: true });
    const secondToken = await signIDToken({ sub: "cache-subject-b", email: "same@example.test", email_verified: true });
    const renamedToken = await signIDToken({ sub: "cache-subject-a", email: "renamed@example.test", email_verified: true });
    const firstHeaders = new Headers({ authorization: `Bearer ${firstToken}` });
    const secondHeaders = new Headers({ authorization: `Bearer ${secondToken}` });

    await resolveUser(firstHeaders);
    enforceBinding = true;
    const bound = await resolveUser(firstHeaders);
    await resolveUser(firstHeaders);
    const other = await resolveUser(secondHeaders);
    const renamed = await resolveUser(new Headers({ authorization: `Bearer ${renamedToken}` }));
    assert.equal("userID" in bound && bound.userID, "44444444-4444-4444-8444-444444444444");
    assert.equal("userID" in other && other.userID, "33333333-3333-4333-8333-333333333333");
    assert.equal("userID" in renamed && renamed.userID, "44444444-4444-4444-8444-444444444444");
    assert.deepEqual(seen, [
      { issuer: ISSUER, subject: "cache-subject-a", authoritative: "false" },
      { issuer: ISSUER, subject: "cache-subject-a", authoritative: "false" },
      { issuer: ISSUER, subject: "cache-subject-b", authoritative: "false" },
      { issuer: ISSUER, subject: "cache-subject-a", authoritative: "false" },
    ]);
  });

  test("resolveUser preserves invite_required and forwards OIDC name claim; redeemInvite caches provisioned user", async () => {
    const provisionedID = "22222222-2222-4222-8222-222222222222";
    let sawName: string | undefined;
    let sawCode: string | undefined;

    await installOIDCFetch(async (req) => {
      const body = await req.json() as { email: string; name?: string; invite_code?: string };
      sawName = body.name;
      assert.equal(req.headers.get("x-nori-identity-issuer"), ISSUER);
      assert.equal(req.headers.get("x-nori-identity-subject"), "inv-1");
      assert.equal(req.headers.get("x-nori-identity-email-authoritative"), "false");
      if (req.url.endsWith("/api/users/resolve")) {
        return Response.json({ error: "An invite code is required to access Nori.", code: "invite_required" }, { status: 403 });
      }
      sawCode = body.invite_code;
      if (body.invite_code === "NORI-GOOD-CODE") {
        return Response.json({ id: provisionedID }, { headers: { "X-Nori-Identity-Bound": "1" } });
      }
      return Response.json({ error: "Invalid invite code.", code: "invalid_invite" }, { status: 403 });
    });

    const token = await signIDToken({ sub: "inv-1", email: "invited@example.test", email_verified: true, name: "Invited User" });
    const headers = new Headers({ authorization: `Bearer ${token}` });

    const res = await resolveUser(headers);
    assert.equal("status" in res && res.status, 403);
    assert.equal("code" in res && res.code, "invite_required");
    assert.equal(sawName, "Invited User");

    const badRedeem = await redeemInvite(headers, "NORI-BAD-CODE");
    assert.equal("status" in badRedeem && badRedeem.status, 403);
    assert.equal("code" in badRedeem && badRedeem.code, "invalid_invite");

    const okRedeem = await redeemInvite(headers, "NORI-GOOD-CODE");
    assert.equal("userID" in okRedeem && okRedeem.userID, provisionedID);
    assert.equal(sawCode, "NORI-GOOD-CODE");

    // Subsequent resolveUser hits the populated cache without calling upstream.
    const cached = await resolveUser(headers);
    assert.equal("userID" in cached && cached.userID, provisionedID);
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
