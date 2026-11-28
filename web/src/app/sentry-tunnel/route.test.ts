import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, test } from "node:test";
import { IpRateLimiter } from "@/lib/runtime/rate-limit";
import { createSentryTunnelHandler, MAX_SENTRY_ENVELOPE_BYTES, parseSentryDSN } from "./handler";

const CONFIGURED_DSN = "https://45c82826f97d90746eee0c98a9a365db@o4507087334670336.ingest.us.sentry.io/4512349065838592";
const EXPECTED_INGEST =
  "https://o4507087334670336.ingest.us.sentry.io/api/4512349065838592/envelope/?sentry_version=7&sentry_key=45c82826f97d90746eee0c98a9a365db";

const ALLOWED_USER = {
  mode: "hosted" as const,
  base: new URL("https://runtime.example.test/"),
  userID: "11111111-1111-4111-8111-111111111111",
  identity: { email: "user@example.test" },
};

function envelopeRequest(body: string | Uint8Array, extraHeaders: Record<string, string> = {}) {
  const bytes = typeof body === "string" ? new TextEncoder().encode(body) : body;
  const payload = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) as ArrayBuffer;
  const headers: Record<string, string> = {
    origin: "https://app.noribot.chat",
    "sec-fetch-site": "same-origin",
    "content-type": "application/x-sentry-envelope",
    "content-length": String(bytes.byteLength),
    ...extraHeaders,
  };
  return new Request("https://app.noribot.chat/sentry-tunnel", {
    method: "POST",
    headers,
    body: payload,
  });
}

describe("Sentry tunnel guard and Cloud Run bypass regression", () => {
  test("next.config.ts does not set tunnelRoute and instrumentation-client.ts explicitly targets /sentry-tunnel", () => {
    const nextConfigSrc = readFileSync(path.resolve(process.cwd(), "next.config.ts"), "utf8");
    assert.doesNotMatch(nextConfigSrc, /^\s*tunnelRoute\s*:/m);
    const clientInitSrc = readFileSync(path.resolve(process.cwd(), "src/instrumentation-client.ts"), "utf8");
    assert.match(clientInitSrc, /tunnel:\s*["']\/sentry-tunnel["']/);
  });

  test("parseSentryDSN requires https, valid key, and numeric project id", () => {
    const parsed = parseSentryDSN(CONFIGURED_DSN);
    assert.ok(parsed);
    assert.equal(parsed.ingestURL, EXPECTED_INGEST);
    assert.equal(parseSentryDSN("http://45c82826f97d90746eee0c98a9a365db@o123.ingest.us.sentry.io/456"), null);
    assert.equal(parseSentryDSN("https://45c82826f97d90746eee0c98a9a365db@o123.ingest.us.sentry.io/not-numeric"), null);
    assert.equal(parseSentryDSN("https://45c82826f97d90746eee0c98a9a365db:pass@o123.ingest.us.sentry.io/456"), null);
    assert.equal(parseSentryDSN("https://o123.ingest.us.sentry.io/456"), null);
  });

  test("rejects rate-limited requests with 429 before forwarding", async () => {
    let forwarded = 0;
    const limiter = new IpRateLimiter({ requestsPerMinute: 1, burst: 1, now: () => 1_000_000 });
    const handler = createSentryTunnelHandler({
      rateLimiter: limiter,
      sameOrigin: () => true,
      resolveUser: async () => ALLOWED_USER,
      configuredDsn: () => CONFIGURED_DSN,
      fetchImpl: (async () => {
        forwarded++;
        return new Response("{}", { status: 200 });
      }) as typeof fetch,
    });

    const validBody = `${JSON.stringify({ dsn: CONFIGURED_DSN })}\n{"type":"event"}\n{}`;
    const first = await handler(envelopeRequest(validBody, { "x-forwarded-for": "203.0.113.5" }));
    assert.equal(first.status, 200);
    assert.equal(forwarded, 1);

    const second = await handler(envelopeRequest(validBody, { "x-forwarded-for": "203.0.113.5" }));
    assert.equal(second.status, 429);
    assert.ok(second.headers.get("retry-after"));
    assert.equal(forwarded, 1);
  });

  test("rejects cross-origin and unauthenticated/suspended requests with 403", async () => {
    let forwarded = 0;
    const baseDeps = {
      rateLimiter: new IpRateLimiter({ requestsPerMinute: 100, burst: 100 }),
      configuredDsn: () => CONFIGURED_DSN,
      fetchImpl: (async () => {
        forwarded++;
        return new Response("{}", { status: 200 });
      }) as typeof fetch,
    };

    const crossOriginHandler = createSentryTunnelHandler({
      ...baseDeps,
      sameOrigin: () => false,
      resolveUser: async () => ALLOWED_USER,
    });
    const crossRes = await crossOriginHandler(envelopeRequest(`${JSON.stringify({ dsn: CONFIGURED_DSN })}\n`));
    assert.equal(crossRes.status, 403);

    const suspendedHandler = createSentryTunnelHandler({
      ...baseDeps,
      sameOrigin: () => true,
      resolveUser: async () => ({ status: 403, error: "Account suspended.", code: "account_suspended" }),
    });
    const suspRes = await suspendedHandler(envelopeRequest(`${JSON.stringify({ dsn: CONFIGURED_DSN })}\n`));
    assert.equal(suspRes.status, 403);
    assert.deepEqual(await suspRes.json(), { error: "Account suspended.", code: "account_suspended" });
    assert.equal(forwarded, 0);
  });

  test("rejects non-matching DSN (host, project, or key) with 403 and malformed envelopes with 400", async () => {
    let forwarded = 0;
    const handler = createSentryTunnelHandler({
      rateLimiter: new IpRateLimiter({ requestsPerMinute: 100, burst: 100 }),
      sameOrigin: () => true,
      resolveUser: async () => ALLOWED_USER,
      configuredDsn: () => CONFIGURED_DSN,
      fetchImpl: (async () => {
        forwarded++;
        return new Response("{}", { status: 200 });
      }) as typeof fetch,
    });

    const malformed = await handler(envelopeRequest("not-json\n{}"));
    assert.equal(malformed.status, 400);

    const missingDsn = await handler(envelopeRequest(`${JSON.stringify({ event_id: "abc" })}\n{}`));
    assert.equal(missingDsn.status, 400);

    for (const badDsn of [
      "https://45c82826f97d90746eee0c98a9a365db@evil.example.com/4512349065838592",
      "https://45c82826f97d90746eee0c98a9a365db@o4507087334670336.ingest.us.sentry.io/9999999999999999",
      "https://00000000000000000000000000000000@o4507087334670336.ingest.us.sentry.io/4512349065838592",
      "http://169.254.169.254/latest/meta-data",
    ]) {
      const res = await handler(envelopeRequest(`${JSON.stringify({ dsn: badDsn })}\n{}`));
      assert.equal(res.status, 403, `expected 403 for ${badDsn}`);
    }
    assert.equal(forwarded, 0);
  });

  test("rejects oversized envelopes (both Content-Length and streamed) with 413", async () => {
    let forwarded = 0;
    const handler = createSentryTunnelHandler({
      rateLimiter: new IpRateLimiter({ requestsPerMinute: 100, burst: 100 }),
      sameOrigin: () => true,
      resolveUser: async () => ALLOWED_USER,
      configuredDsn: () => CONFIGURED_DSN,
      maxBytes: 1024,
      fetchImpl: (async () => {
        forwarded++;
        return new Response("{}", { status: 200 });
      }) as typeof fetch,
    });

    const bigBody = `${JSON.stringify({ dsn: CONFIGURED_DSN })}\n${"x".repeat(1025)}`;
    const withLength = await handler(envelopeRequest(bigBody));
    assert.equal(withLength.status, 413);

    const streamed = new Request("https://app.noribot.chat/sentry-tunnel", {
      method: "POST",
      headers: {
        origin: "https://app.noribot.chat",
        "sec-fetch-site": "same-origin",
        "content-type": "application/x-sentry-envelope",
      },
      body: new ReadableStream({
        start(controller) {
          controller.enqueue(new TextEncoder().encode(bigBody));
          controller.close();
        },
      }),
      duplex: "half",
    } as RequestInit);
    const streamedRes = await handler(streamed);
    assert.equal(streamedRes.status, 413);
    assert.equal(forwarded, 0);
    assert.equal(MAX_SENTRY_ENVELOPE_BYTES, 262_144);
  });

  test("forwards valid matching DSN envelope cleanly without leaking auth or cookie headers", async () => {
    const calls: { url: string; headers: Headers; body: string }[] = [];
    const handler = createSentryTunnelHandler({
      rateLimiter: new IpRateLimiter({ requestsPerMinute: 100, burst: 100 }),
      sameOrigin: () => true,
      resolveUser: async () => ALLOWED_USER,
      configuredDsn: () => CONFIGURED_DSN,
      fetchImpl: (async (input: RequestInfo | URL, init?: RequestInit) => {
        const body =
          init?.body instanceof ArrayBuffer
            ? new TextDecoder().decode(new Uint8Array(init.body))
            : init?.body instanceof Uint8Array
              ? new TextDecoder().decode(init.body)
              : String(init?.body ?? "");
        calls.push({ url: String(input), headers: new Headers(init?.headers), body });
        return new Response(JSON.stringify({ id: "evt123" }), {
          status: 200,
          headers: { "content-type": "application/json", "x-sentry-rate-limits": "60::organization", "set-cookie": "secret=1" },
        });
      }) as typeof fetch,
    });

    const payload = `${JSON.stringify({ dsn: CONFIGURED_DSN })}\n{"type":"event"}\n{"message":"ok"}`;
    const res = await handler(
      envelopeRequest(payload, {
        cookie: "nori_id_token=super-secret",
        authorization: "Bearer secret-jwt",
      }),
    );
    assert.equal(res.status, 200);
    assert.equal(res.headers.get("x-sentry-rate-limits"), "60::organization");
    assert.equal(res.headers.get("set-cookie"), null);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, EXPECTED_INGEST);
    assert.equal(calls[0].headers.get("cookie"), null);
    assert.equal(calls[0].headers.get("authorization"), null);
    assert.equal(calls[0].headers.get("content-type"), "application/x-sentry-envelope");
    assert.equal(calls[0].body, payload);
  });
});
