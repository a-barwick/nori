import { accessFailureResponse, resolveUser, sameOrigin, sameOriginRead, type ScopedRuntime } from "./auth";
import { isRuntimeMethod, isRuntimeRead } from "./methods";
import { runtimeHeaders } from "./credentials";
import { localDevelopment, runtimeMode, runtimeTarget, type Mode } from "./target";
import { reportUpstreamFailure, reportUpstreamRejection, rejectionHeaders, upstreamTraceHeaders } from "@/lib/observability/upstream";
import { MAX_FILE_BYTES } from "../submission";

// A stream ends upstream first. This limit only catches one that stopped responding.
const READ_TIMEOUT_MS = 170_000;
const RELAYED = ["content-disposition", "content-security-policy", "content-length"];
const json = (type: string) => type.includes("application/json");
const eventStream = (type: string) => type.startsWith("text/event-stream");
const forbidden = () => Response.json({ error: "Forbidden." }, { status: 403 });
const notFound = () => Response.json({ error: "Not found." }, { status: 404 });

type RelayPolicy = {
  timeout: number;
  allowed: (type: string) => boolean;
  operation?: string;
};

async function relay(request: Request, { base, userID, mode }: ScopedRuntime, path: string, init: RequestInit, { timeout, allowed, operation }: RelayPolicy) {
  const label = operation ?? path.split("?")[0].replace(/^v1\/(uploads|results)\/.*/, "v1/$1/file").replace(/^v1\/conversations\/[^/]+/, "v1/conversations/item");
  try {
    const trace = upstreamTraceHeaders(request);
    // Construct a fresh header set so neither browser credentials nor IAP headers reach the runtime.
    const upstream = await fetch(new URL(`users/${encodeURIComponent(userID)}/${path}`, base), {
      ...init, headers: { ...(init.headers as Record<string, string>), ...await runtimeHeaders(mode), ...trace },
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

async function forward(request: Request, mode: Mode, path: string, init: RequestInit, allowed: RelayPolicy["allowed"]) {
  const target = runtimeTarget(mode);
  if (target instanceof Response) return target;
  const user = await resolveUser(request.headers, target);
  if ("status" in user) return accessFailureResponse(user);
  return relay(request, user, path, init, { timeout: READ_TIMEOUT_MS, allowed });
}

function allowedCaller(request: Request, mode: Mode, sameSite: (request: Request) => boolean) {
  return sameSite(request) && (mode !== "local" || localDevelopment(request.headers));
}

export async function operationRoute(request: Request, mode: Mode, operation: string) {
  const local = mode === "local";
  if (!isRuntimeMethod(operation) || (local && runtimeMode(request.headers) !== "local")) return notFound();
  if (!sameOrigin(request)) return forbidden();
  const target = runtimeTarget(mode);
  if (target instanceof Response) return local ? target : forbidden();
  const user = await resolveUser(request.headers, target);
  if ("status" in user) return accessFailureResponse(user);
  let body: string;
  try {
    body = JSON.stringify(await request.json());
    if (body.length > (local ? 64_000 : 128_000)) return Response.json({ error: "Request is too large." }, { status: 413 });
  } catch {
    return Response.json({ error: local ? "Send a JSON object." : "Request body must be JSON." }, { status: 400 });
  }
  return relay(request, user, `api/${operation}`, {
    method: "POST", headers: { "Content-Type": "application/json" }, body,
  }, {
    timeout: local ? 100_000 : 145_000,
    allowed: (type) => local || json(type) || type.startsWith("image/png"),
    operation,
  });
}

function historyQuery(request: Request, afterAllowed: boolean, beforeAllowed: boolean) {
  const incoming = new URL(request.url).searchParams;
  const query = new URLSearchParams();
  // Agent Runtime does not relay EventSource's Last-Event-ID header.
  const after = afterAllowed && (request.headers.get("last-event-id") || incoming.get("after"));
  if (after) query.set("after", after);
  const before = beforeAllowed && incoming.get("before");
  if (before) query.set("before", before);
  return query.size ? `?${query}` : "";
}

export async function readRoute(request: Request, mode: Mode, name: string) {
  if (!isRuntimeRead(name)) return notFound();
  if (!allowedCaller(request, mode, sameOriginRead)) return forbidden();
  return forward(request, mode, `v1/${name}${historyQuery(request, true, true)}`, {},
    (type) => json(type) || eventStream(type));
}

export async function uploadRoute(request: Request, mode: Mode, path = "v1/uploads") {
  if (!allowedCaller(request, mode, sameOrigin)) return forbidden();
  if (Number(request.headers.get("content-length") ?? 0) > MAX_FILE_BYTES) return Response.json({ error: "A file can be at most 10 MB." }, { status: 413 });
  const incoming = new URL(request.url).searchParams;
  const query = new URLSearchParams({ id: incoming.get("id") ?? "", filename: incoming.get("filename") ?? "" });
  // Buffer the file so an interrupted upload does not reach the service.
  let body: ArrayBuffer;
  try { body = await request.arrayBuffer(); } catch {
    return Response.json({ error: "The upload was interrupted, so nothing was saved. Try again.", retryable: true }, { status: 400 });
  }
  if (body.byteLength > MAX_FILE_BYTES) return Response.json({ error: "A file can be at most 10 MB." }, { status: 413 });
  return forward(request, mode, `${path}?${query}`, { method: "POST", body, headers: { "Content-Type": request.headers.get("content-type") || "application/octet-stream" } }, json);
}

export async function fileRoute(request: Request, mode: Mode, path: string) {
  if (!allowedCaller(request, mode, sameOriginRead)) return forbidden();
  return forward(request, mode, path, {}, (type) => /^(application\/json|application\/pdf|application\/octet-stream|image\/(png|jpeg|webp)|text\/csv|text\/plain)/.test(type));
}

export async function conversationRoute(request: Request, segments: string[]) {
  const mode = runtimeMode(request.headers);
  const [id, operation] = segments;
  const validID = id === "assistant" || /^[a-f0-9-]{36}$/.test(id || "");
  if (validID && operation === "uploads") {
    const path = `v1/conversations/${encodeURIComponent(id)}/uploads`;
    if (segments.length === 2 && request.method === "POST") return uploadRoute(request, mode, path);
    if (segments.length === 3 && /^[a-f0-9-]{36}$/.test(segments[2]) && request.method === "GET") return fileRoute(request, mode, `${path}/${segments[2]}`);
  }
  const read = request.method === "GET" && (segments.length === 0 || (validID && (segments.length === 1 || (segments.length === 2 && ["events", "messages"].includes(operation)))));
  const write = request.method === "POST" && (segments.length === 0 || (validID && segments.length === 2 && ["messages", "cancel"].includes(operation)));
  const remove = request.method === "DELETE" && segments.length === 1 && id !== "assistant" && validID;
  if (!read && !write && !remove) return notFound();
  if (!allowedCaller(request, mode, read ? sameOriginRead : sameOrigin)) return forbidden();
  const query = historyQuery(request, operation === "events", read && operation === "messages");
  let body: string | undefined;
  if (write) {
    if (Number(request.headers.get("content-length") ?? 0) > 128_000) return Response.json({ error: "Request is too large." }, { status: 413 });
    try {
      const value = await request.text();
      if (new TextEncoder().encode(value).length > 128_000) return Response.json({ error: "Request is too large." }, { status: 413 });
      body = value ? JSON.stringify(JSON.parse(value)) : "{}";
    } catch { return Response.json({ error: "Request body must be JSON." }, { status: 400 }); }
  }
  const path = ["v1/conversations", ...segments.map(encodeURIComponent)].join("/") + query;
  const init = write ? { method: "POST", body, headers: { "Content-Type": "application/json" } } : { method: request.method };
  return forward(request, mode, path, init, type => json(type) || (read && operation === "events" && eventStream(type)));
}

export async function capabilitiesRoute(request: Request) {
  if (!sameOriginRead(request)) return forbidden();
  return forward(request, runtimeMode(request.headers), "v1/capabilities", {}, json);
}

// Assistant management API (contract r1+). Each family is an explicit allowlist; the
// browser never chooses an arbitrary upstream path.
const ASSISTANT_FAMILIES = new Set([
  "integrations", "connections", "interactions", "work", "automations", "goals", "library", "search", "permissions",
  "preferences", "delivery-destinations", "plugins", "mcp", "computer", "assistant", "mutations", "operations", "memory",
  "inbox", "skills", "tools", "mcp-clients",
]);
const SEGMENT = /^[A-Za-z0-9._:@-]{1,200}$/;

export async function assistantRoute(request: Request, family: string, segments: string[]) {
  const mode = runtimeMode(request.headers);
  if (!ASSISTANT_FAMILIES.has(family) || segments.length > 4 || !segments.every((segment) => SEGMENT.test(segment))) return notFound();
  const read = request.method === "GET";
  if (!read && !["POST", "PUT", "DELETE"].includes(request.method)) return notFound();
  const incoming = new URL(request.url).searchParams;
  const path = ["v1", family, ...segments].map(encodeURIComponent).join("/") + (incoming.size ? `?${incoming}` : "");
  // The provider's redirect is a cross-site top-level navigation, so it cannot pass the same-origin check. Core
  // validates the single-use state within this user and the PKCE verifier; only a navigation is accepted here.
  if (family === "connections" && segments.join("/") === "oauth/callback") {
    if (!read || request.headers.get("sec-fetch-mode") !== "navigate") return forbidden();
    return oauthCallback(request, mode, path);
  }
  if (!allowedCaller(request, mode, read ? sameOriginRead : sameOrigin)) return forbidden();
  let body: string | undefined;
  if (!read) {
    if (Number(request.headers.get("content-length") ?? 0) > 128_000) return Response.json({ error: "Request is too large." }, { status: 413 });
    try {
      const value = await request.text();
      if (new TextEncoder().encode(value).length > 128_000) return Response.json({ error: "Request is too large." }, { status: 413 });
      body = value ? JSON.stringify(JSON.parse(value)) : "{}";
    } catch { return Response.json({ error: "Request body must be JSON." }, { status: 400 }); }
  }
  const init: RequestInit = read ? {} : { method: request.method, body, headers: { "Content-Type": "application/json" } };
  return forward(request, mode, path, init, (type) => json(type) || (read && family === "library" && /^(application\/(pdf|octet-stream)|image\/(png|jpeg|webp)|text\/(plain|markdown|csv))/.test(type)) || (read && family === "computer" && /^image\/(png|jpeg|webp)/.test(type)));
}

// The provider redirects the browser here. Core validates state within this user only and
// answers 303 to the attempt's relative return_to; only a relative Location is passed on.
async function oauthCallback(request: Request, mode: Mode, path: string) {
  const target = runtimeTarget(mode);
  if (target instanceof Response) return target;
  const user = await resolveUser(request.headers, target);
  if ("status" in user) return accessFailureResponse(user);
  try {
    const upstream = await fetch(new URL(`users/${encodeURIComponent(user.userID)}/${path}`, user.base), {
      headers: { ...await runtimeHeaders(mode), ...upstreamTraceHeaders(request) }, redirect: "manual", cache: "no-store",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(30_000)]),
    });
    void upstream.body?.cancel().catch(() => undefined);
    const location = upstream.headers.get("location") ?? "";
    const safe = location.startsWith("/") && !location.startsWith("//") ? location : "/apps?result=failed";
    return new Response(null, { status: 303, headers: { Location: safe, "Cache-Control": "no-store" } });
  } catch {
    return new Response(null, { status: 303, headers: { Location: "/apps?result=failed", "Cache-Control": "no-store" } });
  }
}
