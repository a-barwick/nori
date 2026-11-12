import { accessFailureResponse, resolveUser, sameOrigin, sameOriginRead, type ScopedRuntime } from "./auth";
import { browserRoute } from "./routes";
import { runtimeHeaders } from "./credentials";
import { localDevelopment, runtimeMode, runtimeTarget, type Mode } from "./target";
import { reportUpstreamFailure, reportUpstreamRejection, rejectionHeaders, upstreamTraceHeaders } from "@/lib/observability/upstream";

// A stream ends upstream first. This limit only catches one that stopped responding.
const READ_TIMEOUT_MS = 170_000;
const RELAYED = ["content-disposition", "content-security-policy", "content-length"];
const mediaType = (type: string) => type.split(";", 1)[0].trim().toLowerCase();
const json = (type: string) => mediaType(type) === "application/json";
const forbidden = () => Response.json({ error: "Forbidden." }, { status: 403 });
const notFound = () => Response.json({ error: "Not found." }, { status: 404 });

type RelayPolicy = {
  timeout: number;
  allowed: (type: string) => boolean;
  operation?: string;
  label: string;
};

async function relay(request: Request, { base, userID, mode }: ScopedRuntime, path: string, init: RequestInit, { timeout, allowed, operation, label }: RelayPolicy) {
  try {
    const trace = upstreamTraceHeaders(request);
    // Construct a fresh header set so neither browser credentials nor IAP headers reach the runtime.
    const upstream = await fetch(new URL(`users/${encodeURIComponent(userID)}/${path}`, base), {
      ...init, headers: { ...(init.headers as Record<string, string>), ...await runtimeHeaders(mode, userID), ...trace },
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(timeout)]), redirect: "error", cache: "no-store",
    });
    if (upstream.status >= 500) reportUpstreamFailure(label, mode, upstream.status);
    if (mode === "hosted" && upstream.status === 429) {
      reportUpstreamRejection(operation ?? label, request, trace, upstream.headers);
      void upstream.body?.cancel().catch(() => undefined);
      // Platforms can return private HTML error bodies. Relay only the throttling signal and sanitized headers.
      return Response.json({ error: "Nori is busy. Try again shortly." }, {
        status: 429,
        headers: { "Cache-Control": "no-store", ...rejectionHeaders(upstream.headers) },
      });
    }
    const type = upstream.headers.get("content-type") || "application/json";
    if (!allowed(type)) {
      void upstream.body?.cancel().catch(() => undefined);
      return Response.json({ error: "The runtime returned an unsupported response." }, { status: 502 });
    }
    const headers: Record<string, string> = { "Content-Type": type, "Cache-Control": operation ? "no-store" : "no-cache, no-transform" };
    if (!operation || mode === "hosted") headers["X-Content-Type-Options"] = "nosniff";
    if (!operation) {
      headers["X-Accel-Buffering"] = "no";
      for (const name of RELAYED) {
        const value = upstream.headers.get(name);
        if (value) headers[name] = value;
      }
    }
    return new Response(upstream.body, { status: upstream.status, headers });
  } catch {
    if (!request.signal.aborted) reportUpstreamFailure(label, mode);
    const error = !operation ? "Connection to Nori's runtime was interrupted."
      : mode === "local" ? "Could not reach the local agent. Check that the Go runner is running, then retry."
      : "Connection to Nori's runtime was interrupted. Refresh history to confirm the task's status.";
    return Response.json({ error }, { status: 502 });
  }
}

// The hosted runtime allows few queries per minute, and every open tab polls the same small lists. Answers to those
// reads are shared per user for a few seconds, so a second tab or device costs no extra runtime queries. Any change
// the user makes through this tier drops their shared answers, so their own actions show at once.
const SHARED_READS = new Set(["v1/capabilities", "v1/assistant/summary", "v1/interactions?status=pending", "v1/work", "v1/integrations", "v1/connections"]);
const SHARED_TTL_MS = 8_000;
const sharedReads = new Map<string, { expires: number; text: Promise<string | null> }>();
const SHARED_HEADERS = { "Content-Type": "application/json", "Cache-Control": "no-cache, no-transform", "X-Content-Type-Options": "nosniff" };

function forgetSharedReads(userID: string) {
  for (const key of sharedReads.keys()) if (key.startsWith(userID + "\n")) sharedReads.delete(key);
}

async function sharedRead(request: Request, user: ScopedRuntime, path: string, policy: RelayPolicy) {
  const key = user.userID + "\n" + path;
  const hit = sharedReads.get(key);
  if (hit && hit.expires > Date.now()) {
    const text = await hit.text;
    if (text !== null) return new Response(text, { status: 200, headers: SHARED_HEADERS });
  }
  let settle: (value: string | null) => void = () => undefined;
  const text = new Promise<string | null>((resolve) => { settle = resolve; });
  if (sharedReads.size >= 2000) sharedReads.clear();
  const entry = { expires: Date.now() + SHARED_TTL_MS, text };
  sharedReads.set(key, entry);
  const drop = () => { if (sharedReads.get(key) === entry) sharedReads.delete(key); };
  try {
    const response = await relay(request, user, path, {}, policy);
    if (response.status !== 200 || !json(response.headers.get("content-type") ?? "")) {
      drop();
      settle(null);
      return response;
    }
    const value = await response.text();
    settle(value);
    return new Response(value, { status: 200, headers: SHARED_HEADERS });
  } catch (error) {
    drop();
    settle(null);
    throw error;
  }
}

async function readBody(request: Request, maxBytes: number): Promise<ArrayBuffer> {
  if (Number(request.headers.get("content-length") ?? 0) > maxBytes) throw new RangeError();
  const reader = request.body?.getReader();
  if (!reader) return new ArrayBuffer(0);
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    while (true) {
      const { value, done } = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > maxBytes) {
        void reader.cancel().catch(() => undefined);
        throw new RangeError();
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const body = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return body.buffer;
}

export async function forwardRoute(request: Request, segments: string[], mode = runtimeMode(request.headers)) {
  const rule = browserRoute(request.method, segments);
  if (!rule || (mode === "local" && !localDevelopment(request.headers))) return notFound();
  const read = request.method === "GET" || request.method === "HEAD";
  const operation = rule.path.startsWith("/api/") ? rule.path.slice(5) : undefined;
  const oauth = rule.path === "/v1/connections/oauth/callback";
  if (oauth) {
    if (request.headers.get("sec-fetch-mode") !== "navigate") return forbidden();
  } else if (!(read ? sameOriginRead(request) : sameOrigin(request))) return forbidden();

  const query = operation ? new URLSearchParams() : new URL(request.url).searchParams;
  if (rule.response_media_types.includes("text/event-stream")) {
    const after = request.headers.get("last-event-id");
    if (after) query.set("after", after);
  }
  const path = segments.map(encodeURIComponent).join("/") + (query.size ? `?${query}` : "");
  if (oauth) return oauthCallback(request, mode, path);
  const target = runtimeTarget(mode);
  if (target instanceof Response) return target;
  const user = await resolveUser(request.headers, target);
  if ("status" in user) return accessFailureResponse(user);

  const init: RequestInit = { method: request.method };
  if (rule.body && rule.max_request_bytes) {
    try {
      const buffer = await readBody(request, rule.max_request_bytes);
      if (rule.body === "upload") {
        init.body = buffer;
        init.headers = { "Content-Type": request.headers.get("content-type") || "application/octet-stream" };
      } else {
        const text = new TextDecoder().decode(buffer);
        init.body = text || "{}";
        JSON.parse(init.body);
        init.headers = { "Content-Type": "application/json" };
      }
    } catch (error) {
      if (error instanceof RangeError) return Response.json({ error: "Request is too large." }, { status: 413 });
      return rule.body === "upload"
        ? Response.json({ error: "The upload was interrupted, so nothing was saved. Try again.", retryable: true }, { status: 400 })
        : Response.json({ error: "Request body must be JSON." }, { status: 400 });
    }
  }
  const policy: RelayPolicy = {
    label: rule.path.slice(1),
    operation,
    timeout: operation ? mode === "local" ? 100_000 : 145_000 : READ_TIMEOUT_MS,
    allowed: type => rule.response_media_types.includes(mediaType(type)),
  };
  if (request.method === "GET" && mode === "hosted" && SHARED_READS.has(path)) return sharedRead(request, user, path, policy);
  const response = await relay(request, user, path, init, policy);
  if (!read) forgetSharedReads(user.userID);
  return response;
}

// The provider redirects the browser here. Core validates state within this user only and
// answers 303 to the attempt's relative return_to; only a relative Location is passed on.
async function oauthCallback(request: Request, mode: Mode, path: string) {
  const target = runtimeTarget(mode);
  if (target instanceof Response) return target;
  const user = await resolveUser(request.headers, target);
  if ("status" in user) return accessFailureResponse(user);
  try {
    const upstream = await fetch(new URL(`users/${encodeURIComponent(user.userID)}/${path}${path.includes("?") ? "&" : "?"}nori_redirect=json`, user.base), {
      // The hosted gateway drops a redirect's Location, so core returns the destination as JSON when asked.
      headers: { ...await runtimeHeaders(mode, user.userID), ...upstreamTraceHeaders(request), "X-Nori-Redirect": "json" }, redirect: "manual", cache: "no-store",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(30_000)]),
    });
    let location = upstream.headers.get("location") ?? "";
    if (!location && upstream.ok && json(upstream.headers.get("content-type") ?? "")) {
      const value = await upstream.json().catch(() => null) as { location?: unknown } | null;
      if (typeof value?.location === "string") location = value.location;
    } else void upstream.body?.cancel().catch(() => undefined);
    const localPath = location.startsWith("/") && !location.startsWith("//") && !location.includes("\\");
    const safe = localPath ? location : "/apps?result=failed";
    return new Response(null, { status: 303, headers: { Location: safe, "Cache-Control": "no-store" } });
  } catch {
    return new Response(null, { status: 303, headers: { Location: "/apps?result=failed", "Cache-Control": "no-store" } });
  }
}
