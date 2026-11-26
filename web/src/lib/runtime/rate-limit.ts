export type IpRateLimitOptions = {
  enabled?: boolean;
  requestsPerMinute?: number;
  burst?: number;
  maxEntries?: number;
  idleTtlMs?: number;
  now?: () => number;
};

export type RateLimitDecision =
  | { allowed: true; remaining: number }
  | { allowed: false; retryAfterSeconds: number };

type Bucket = {
  tokens: number;
  lastRefillMs: number;
  lastSeenMs: number;
};

const DEFAULT_REQUESTS_PER_MINUTE = 120;
const DEFAULT_BURST = 30;
const DEFAULT_MAX_ENTRIES = 5000;
const DEFAULT_IDLE_TTL_MS = 15 * 60 * 1000;

/**
 * Bounded-LRU token-bucket rate limiter keyed by client IP for pre-auth BFF protection.
 */
export class IpRateLimiter {
  private readonly enabled: boolean;
  private readonly refillPerMs: number;
  private readonly burst: number;
  private readonly maxEntries: number;
  private readonly idleTtlMs: number;
  private readonly now: () => number;
  private readonly buckets = new Map<string, Bucket>();

  constructor(options: IpRateLimitOptions = {}) {
    this.enabled = options.enabled ?? true;
    const rpm = options.requestsPerMinute ?? DEFAULT_REQUESTS_PER_MINUTE;
    const burst = options.burst ?? DEFAULT_BURST;
    this.refillPerMs = rpm > 0 ? rpm / 60_000 : 0;
    this.burst = burst > 0 ? burst : Math.max(1, rpm);
    this.maxEntries = Math.max(1, options.maxEntries ?? DEFAULT_MAX_ENTRIES);
    this.idleTtlMs = Math.max(0, options.idleTtlMs ?? DEFAULT_IDLE_TTL_MS);
    this.now = options.now ?? (() => Date.now());
  }

  check(key: string): RateLimitDecision {
    if (!this.enabled || this.refillPerMs <= 0) {
      return { allowed: true, remaining: this.burst };
    }

    const normalizedKey = key.trim() || "unknown";
    const nowMs = this.now();
    let bucket = this.buckets.get(normalizedKey);

    if (bucket) {
      // Refresh LRU order on access.
      this.buckets.delete(normalizedKey);
      if (this.idleTtlMs > 0 && nowMs - bucket.lastSeenMs > this.idleTtlMs) {
        bucket = { tokens: this.burst, lastRefillMs: nowMs, lastSeenMs: nowMs };
      } else {
        const elapsedMs = Math.max(0, nowMs - bucket.lastRefillMs);
        bucket.tokens = Math.min(this.burst, bucket.tokens + elapsedMs * this.refillPerMs);
        bucket.lastRefillMs = nowMs;
        bucket.lastSeenMs = nowMs;
      }
    } else {
      this.evictIfFull(nowMs);
      bucket = { tokens: this.burst, lastRefillMs: nowMs, lastSeenMs: nowMs };
    }

    this.buckets.set(normalizedKey, bucket);

    if (bucket.tokens >= 1) {
      bucket.tokens -= 1;
      return { allowed: true, remaining: Math.floor(bucket.tokens) };
    }

    const needed = 1 - bucket.tokens;
    const retryAfterSeconds = Math.max(1, Math.ceil(needed / (this.refillPerMs * 1000)));
    return { allowed: false, retryAfterSeconds };
  }

  checkRequest(headers: Headers): RateLimitDecision {
    return this.check(extractClientIp(headers));
  }

  get size(): number {
    return this.buckets.size;
  }

  clear(): void {
    this.buckets.clear();
  }

  private evictIfFull(nowMs: number): void {
    if (this.buckets.size < this.maxEntries) return;

    if (this.idleTtlMs > 0) {
      for (const [k, b] of this.buckets) {
        if (nowMs - b.lastSeenMs > this.idleTtlMs) {
          this.buckets.delete(k);
        }
        if (this.buckets.size < this.maxEntries) return;
      }
    }

    const oldest = this.buckets.keys().next();
    if (!oldest.done) {
      this.buckets.delete(oldest.value);
    }
  }
}

/**
 * Extracts the client IP from standard proxy headers (Cloud Run / Cloud Load Balancing).
 */
export function extractClientIp(headers: Headers): string {
  const forwarded = headers.get("x-forwarded-for");
  if (forwarded) {
    const first = forwarded.split(",")[0]?.trim();
    if (first) return first;
  }
  const realIp = headers.get("x-real-ip")?.trim();
  if (realIp) return realIp;
  return "unknown";
}

export function rateLimitResponse(retryAfterSeconds: number): Response {
  const retry = Math.max(1, Math.ceil(retryAfterSeconds));
  return new Response(
    JSON.stringify({
      error: "Too many requests. Try again shortly.",
      code: "rate_limited",
    }),
    {
      status: 429,
      headers: {
        "Content-Type": "application/json; charset=utf-8",
        "Cache-Control": "no-store",
        "Retry-After": String(retry),
      },
    },
  );
}

function parsePositiveInt(raw: string | undefined, fallback: number): number {
  if (!raw || raw.trim() === "") return fallback;
  const parsed = Number.parseInt(raw.trim(), 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

export function createIpRateLimiterFromEnv(
  env: Record<string, string | undefined> = process.env,
): IpRateLimiter {
  const enabledRaw = env.NORI_WEB_RATE_LIMIT_ENABLED?.trim().toLowerCase();
  const enabled = enabledRaw !== "false" && enabledRaw !== "0";
  return new IpRateLimiter({
    enabled,
    requestsPerMinute: parsePositiveInt(env.NORI_WEB_RATE_LIMIT_PER_MIN, DEFAULT_REQUESTS_PER_MINUTE),
    burst: parsePositiveInt(env.NORI_WEB_RATE_LIMIT_BURST, DEFAULT_BURST),
    maxEntries: parsePositiveInt(env.NORI_WEB_RATE_LIMIT_MAX_ENTRIES, DEFAULT_MAX_ENTRIES),
  });
}
