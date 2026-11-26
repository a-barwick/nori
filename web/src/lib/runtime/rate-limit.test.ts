import { describe, expect, test } from "bun:test";
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

    expect(limiter.check("203.0.113.10")).toEqual({ allowed: true, remaining: 2 });
    expect(limiter.check("203.0.113.10")).toEqual({ allowed: true, remaining: 1 });
    expect(limiter.check("203.0.113.10")).toEqual({ allowed: true, remaining: 0 });

    const blocked = limiter.check("203.0.113.10");
    expect(blocked.allowed).toBe(false);
    if (!blocked.allowed) {
      expect(blocked.retryAfterSeconds).toBe(1);
    }

    // Advance 1 second: 1 token refills.
    nowMs += 1000;
    expect(limiter.check("203.0.113.10")).toEqual({ allowed: true, remaining: 0 });

    // Other IPs remain unaffected.
    expect(limiter.check("198.51.100.2")).toEqual({ allowed: true, remaining: 2 });
  });

  test("evicts least-recently-used entries when maxEntries is reached", () => {
    let nowMs = 1_000_000;
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
    expect(limiter.size).toBe(2);

    // Adding 10.0.0.3 evicts 10.0.0.2 while keeping 10.0.0.1.
    limiter.check("10.0.0.3");
    expect(limiter.size).toBe(2);
    expect(limiter.check("10.0.0.1").allowed).toBe(false);
    // 10.0.0.2 was evicted and gets a fresh bucket.
    expect(limiter.check("10.0.0.2")).toEqual({ allowed: true, remaining: 1 });
  });

  test("resets idle entries past idleTtlMs", () => {
    let nowMs = 1_000_000;
    const limiter = new IpRateLimiter({
      requestsPerMinute: 6,
      burst: 1,
      idleTtlMs: 5_000,
      now: () => nowMs,
    });

    expect(limiter.check("10.0.0.9").allowed).toBe(true);
    expect(limiter.check("10.0.0.9").allowed).toBe(false);

    nowMs += 6_000;
    expect(limiter.check("10.0.0.9")).toEqual({ allowed: true, remaining: 0 });
  });

  test("disabled limiter always allows requests", () => {
    const limiter = new IpRateLimiter({ enabled: false, requestsPerMinute: 1, burst: 1 });
    for (let i = 0; i < 20; i++) {
      expect(limiter.check("203.0.113.5").allowed).toBe(true);
    }
  });

  test("extractClientIp reads X-Forwarded-For and X-Real-IP", () => {
    expect(extractClientIp(new Headers({ "x-forwarded-for": "203.0.113.5, 10.0.0.1" }))).toBe("203.0.113.5");
    expect(extractClientIp(new Headers({ "x-real-ip": "198.51.100.7" }))).toBe("198.51.100.7");
    expect(extractClientIp(new Headers())).toBe("unknown");
  });

  test("rateLimitResponse formats 429 JSON response with Retry-After", async () => {
    const res = rateLimitResponse(4.2);
    expect(res.status).toBe(429);
    expect(res.headers.get("retry-after")).toBe("5");
    expect(res.headers.get("cache-control")).toBe("no-store");
    const body = (await res.json()) as { error: string; code: string };
    expect(body.code).toBe("rate_limited");
    expect(body.error.length).toBeGreaterThan(0);
  });

  test("createIpRateLimiterFromEnv respects disable flag and custom limits", () => {
    const disabled = createIpRateLimiterFromEnv({ NORI_WEB_RATE_LIMIT_ENABLED: "false" });
    expect(disabled.check("1.2.3.4").allowed).toBe(true);

    const tight = createIpRateLimiterFromEnv({
      NORI_WEB_RATE_LIMIT_PER_MIN: "60",
      NORI_WEB_RATE_LIMIT_BURST: "1",
    });
    expect(tight.check("1.2.3.4").allowed).toBe(true);
    expect(tight.check("1.2.3.4").allowed).toBe(false);
  });
});
