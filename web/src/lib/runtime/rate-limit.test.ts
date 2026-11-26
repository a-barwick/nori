import assert from "node:assert/strict";
import { describe, test } from "node:test";
import {
  IpRateLimiter,
  createIpRateLimiterFromEnv,
  extractClientIp,
  rateLimitResponse,
} from "./rate-limit";

describe("IpRateLimiter", () => {
  test("allows burst requests and rejects excess with Retry-After", () => {
    let nowMs = 1_000_000;
    const limiter = new IpRateLimiter({
      requestsPerMinute: 60, // 1 req/sec
      burst: 3,
      now: () => nowMs,
    });

    assert.deepEqual(limiter.check("203.0.113.10"), { allowed: true, remaining: 2 });
    assert.deepEqual(limiter.check("203.0.113.10"), { allowed: true, remaining: 1 });
    assert.deepEqual(limiter.check("203.0.113.10"), { allowed: true, remaining: 0 });

    const blocked = limiter.check("203.0.113.10");
    assert.equal(blocked.allowed, false);
    if (!blocked.allowed) {
      assert.equal(blocked.retryAfterSeconds, 1);
    }

    // Advance 1 second: 1 token refills.
    nowMs += 1000;
    assert.deepEqual(limiter.check("203.0.113.10"), { allowed: true, remaining: 0 });

    // Other IPs remain unaffected.
    assert.deepEqual(limiter.check("198.51.100.2"), { allowed: true, remaining: 2 });
  });

  test("evicts least-recently-used entries when maxEntries is reached", () => {
    const nowMs = 1_000_000;
    const limiter = new IpRateLimiter({
      requestsPerMinute: 60,
      burst: 2,
      maxEntries: 2,
      now: () => nowMs,
    });

    limiter.check("10.0.0.1");
    limiter.check("10.0.0.1"); // exhausted
    limiter.check("10.0.0.2");
    // Touch 10.0.0.1 so 10.0.0.2 becomes LRU.
    limiter.check("10.0.0.1");
    assert.equal(limiter.size, 2);

    // Adding 10.0.0.3 evicts 10.0.0.2 while keeping 10.0.0.1.
    limiter.check("10.0.0.3");
    assert.equal(limiter.size, 2);
    assert.equal(limiter.check("10.0.0.1").allowed, false);
    // 10.0.0.2 was evicted and gets a fresh bucket.
    assert.deepEqual(limiter.check("10.0.0.2"), { allowed: true, remaining: 1 });
  });

  test("resets idle entries past idleTtlMs", () => {
    let nowMs = 1_000_000;
    const limiter = new IpRateLimiter({
      requestsPerMinute: 6,
      burst: 1,
      idleTtlMs: 5_000,
      now: () => nowMs,
    });

    assert.equal(limiter.check("10.0.0.9").allowed, true);
    assert.equal(limiter.check("10.0.0.9").allowed, false);

    nowMs += 6_000;
    assert.deepEqual(limiter.check("10.0.0.9"), { allowed: true, remaining: 0 });
  });

  test("disabled limiter always allows requests", () => {
    const limiter = new IpRateLimiter({ enabled: false, requestsPerMinute: 1, burst: 1 });
    for (let i = 0; i < 20; i++) {
      assert.equal(limiter.check("203.0.113.5").allowed, true);
    }
  });

  test("extractClientIp reads rightmost X-Forwarded-For hop and X-Real-IP", () => {
    assert.equal(extractClientIp(new Headers({ "x-forwarded-for": "198.51.100.99, 203.0.113.5" })), "203.0.113.5");
    assert.equal(extractClientIp(new Headers({ "x-real-ip": "198.51.100.7" })), "198.51.100.7");
    assert.equal(extractClientIp(new Headers()), "unknown");
  });

  test("rateLimitResponse formats 429 JSON response with Retry-After", async () => {
    const res = rateLimitResponse(4.2);
    assert.equal(res.status, 429);
    assert.equal(res.headers.get("retry-after"), "5");
    assert.equal(res.headers.get("cache-control"), "no-store");
    const body = (await res.json()) as { error: string; code: string };
    assert.equal(body.code, "rate_limited");
    assert.ok(body.error.length > 0);
  });

  test("createIpRateLimiterFromEnv respects disable flag and custom limits", () => {
    const disabled = createIpRateLimiterFromEnv({ NORI_WEB_RATE_LIMIT_ENABLED: "false" });
    assert.equal(disabled.check("1.2.3.4").allowed, true);

    const tight = createIpRateLimiterFromEnv({
      NORI_WEB_RATE_LIMIT_PER_MIN: "60",
      NORI_WEB_RATE_LIMIT_BURST: "1",
    });
    assert.equal(tight.check("1.2.3.4").allowed, true);
    assert.equal(tight.check("1.2.3.4").allowed, false);
  });
});
