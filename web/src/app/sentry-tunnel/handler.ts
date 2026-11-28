import { accessFailureResponse, resolveUser, sameOrigin, type AccessFailure, type ScopedRuntime } from "@/lib/runtime/auth";
import { createIpRateLimiterFromEnv, rateLimitResponse, type RateLimitDecision } from "@/lib/runtime/rate-limit";
import { readBytesLimited } from "@/lib/runtime/read-body";

export const MAX_SENTRY_ENVELOPE_BYTES = 262_144;
const MAX_ENVELOPE_HEADER_BYTES = 16_384;
const FORWARD_TIMEOUT_MS = 15_000;
const FORWARDED_RESPONSE_HEADERS = ["content-type", "retry-after", "x-sentry-rate-limits"] as const;

export type ParsedSentryDSN = {
  raw: string;
  protocol: "https:";
  host: string;
  publicKey: string;
  projectId: string;
  ingestURL: string;
};

export function parseSentryDSN(raw: string | undefined | null): ParsedSentryDSN | null {
  const trimmed = raw?.trim();
  if (!trimmed) return null;
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return null;
  }
  if (parsed.protocol !== "https:") return null;
  if (!parsed.hostname || parsed.password || parsed.search || parsed.hash) return null;
  if (!/^[A-Za-z0-9_-]{8,128}$/.test(parsed.username)) return null;
  const pathMatch = /^\/(\d+)$/.exec(parsed.pathname);
  if (!pathMatch) return null;
  const projectId = pathMatch[1];
  const host = parsed.host.toLowerCase();
  return {
    raw: trimmed,
    protocol: "https:",
    host,
    publicKey: parsed.username,
    projectId,
    ingestURL: `https://${host}/api/${projectId}/envelope/?sentry_version=7&sentry_key=${encodeURIComponent(parsed.username)}`,
  };
}

export function validateEnvelopeHeader(
  body: Uint8Array,
  allowed: ParsedSentryDSN,
): { ok: true } | { ok: false; status: 400 | 403; message: string } {
  if (body.byteLength === 0) {
    return { ok: false, status: 400, message: "Empty envelope." };
  }
  const newline = body.indexOf(0x0a);
  const headerSlice = newline === -1 ? body : body.subarray(0, newline);
  if (headerSlice.byteLength === 0 || headerSlice.byteLength > MAX_ENVELOPE_HEADER_BYTES) {
    return { ok: false, status: 400, message: "Invalid envelope header." };
  }
  let header: unknown;
  try {
    header = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(headerSlice));
  } catch {
    return { ok: false, status: 400, message: "Invalid envelope header." };
  }
  if (!header || typeof header !== "object" || Array.isArray(header)) {
    return { ok: false, status: 400, message: "Invalid envelope header." };
  }
  const dsn = (header as { dsn?: unknown }).dsn;
  if (typeof dsn !== "string" || !dsn.trim()) {
    return { ok: false, status: 400, message: "Envelope DSN is required." };
  }
  const incoming = parseSentryDSN(dsn);
  if (
    !incoming ||
    incoming.host !== allowed.host ||
    incoming.projectId !== allowed.projectId ||
    incoming.publicKey !== allowed.publicKey
  ) {
    return { ok: false, status: 403, message: "DSN not allowed." };
  }
  return { ok: true };
}

export type SentryTunnelDeps = {
  rateLimiter: { checkRequest: (headers: Headers) => RateLimitDecision };
  sameOrigin: (request: Pick<Request, "headers">) => boolean;
  resolveUser: (headers: Headers) => Promise<ScopedRuntime | AccessFailure>;
  configuredDsn: () => string | undefined;
  fetchImpl: typeof fetch;
  maxBytes: number;
};

export function createSentryTunnelHandler(overrides: Partial<SentryTunnelDeps> = {}) {
  const deps: SentryTunnelDeps = {
    rateLimiter: overrides.rateLimiter ?? createIpRateLimiterFromEnv(),
    sameOrigin: overrides.sameOrigin ?? sameOrigin,
    resolveUser: overrides.resolveUser ?? ((headers) => resolveUser(headers)),
    configuredDsn: overrides.configuredDsn ?? (() => process.env.SENTRY_WEB_DSN || process.env.NEXT_PUBLIC_SENTRY_DSN),
    fetchImpl: overrides.fetchImpl ?? ((input, init) => fetch(input, init)),
    maxBytes: overrides.maxBytes ?? MAX_SENTRY_ENVELOPE_BYTES,
  };

  return async function handleSentryTunnelPost(request: Request): Promise<Response> {
    const limit = deps.rateLimiter.checkRequest(request.headers);
    if (!limit.allowed) {
      return rateLimitResponse(limit.retryAfterSeconds);
    }
    if (!deps.sameOrigin(request)) {
      return new Response("Forbidden.", { status: 403, headers: { "Cache-Control": "no-store" } });
    }
    const user = await deps.resolveUser(request.headers);
    if ("status" in user) {
      return accessFailureResponse(user);
    }

    const allowed = parseSentryDSN(deps.configuredDsn());
    if (!allowed) {
      return new Response("Sentry tunnel is not configured.", { status: 503, headers: { "Cache-Control": "no-store" } });
    }

    const rawLength = request.headers.get("content-length");
    let declaredLength: number | undefined;
    if (rawLength !== null) {
      if (!/^\d+$/.test(rawLength.trim())) {
        return new Response("Invalid envelope size.", { status: 400, headers: { "Cache-Control": "no-store" } });
      }
      const parsed = Number(rawLength.trim());
      if (!Number.isSafeInteger(parsed) || parsed <= 0) {
        return new Response("Invalid envelope size.", { status: 400, headers: { "Cache-Control": "no-store" } });
      }
      if (parsed > deps.maxBytes) {
        return new Response("Envelope too large.", { status: 413, headers: { "Cache-Control": "no-store" } });
      }
      declaredLength = parsed;
    }

    const body = await readBytesLimited(request, deps.maxBytes);
    if (!body.ok) {
      return new Response(body.status === 413 ? "Envelope too large." : "Incomplete envelope.", {
        status: body.status,
        headers: { "Cache-Control": "no-store" },
      });
    }
    if (body.value.byteLength === 0) {
      return new Response("Empty envelope.", { status: 400, headers: { "Cache-Control": "no-store" } });
    }
    if (declaredLength !== undefined && body.value.byteLength !== declaredLength) {
      return new Response("Incomplete envelope.", { status: 400, headers: { "Cache-Control": "no-store" } });
    }

    const validation = validateEnvelopeHeader(new Uint8Array(body.value), allowed);
    if (!validation.ok) {
      return new Response(validation.message, {
        status: validation.status,
        headers: { "Cache-Control": "no-store" },
      });
    }

    try {
      const upstream = await deps.fetchImpl(allowed.ingestURL, {
        method: "POST",
        headers: { "Content-Type": "application/x-sentry-envelope" },
        body: body.value,
        cache: "no-store",
        redirect: "error",
        signal: AbortSignal.timeout(FORWARD_TIMEOUT_MS),
      });
      const headers = new Headers({ "Cache-Control": "no-store" });
      for (const name of FORWARDED_RESPONSE_HEADERS) {
        const value = upstream.headers.get(name);
        if (value) headers.set(name, value);
      }
      const responseBody = await upstream.arrayBuffer().catch(() => new ArrayBuffer(0));
      return new Response(responseBody, { status: upstream.status, headers });
    } catch {
      return new Response("Failed to forward envelope.", { status: 502, headers: { "Cache-Control": "no-store" } });
    }
  };
}
