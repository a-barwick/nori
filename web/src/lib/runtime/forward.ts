import { accessFailureResponse, evictResolvedUser, identityHeaders, resolveUser, sameOrigin, sameOriginRead, type ScopedRuntime } from "./auth";
import { isRuntimeMethod, isRuntimeRead } from "./methods";
import { runtimeHeaders } from "./credentials";
import { signedOutCookie } from "./session";
import { localDevelopment, runtimeMode, runtimeTarget, type Mode } from "./target";
import { reportUpstreamFailure, reportUpstreamRejection, rejectionHeaders, upstreamTraceHeaders } from "@/lib/observability/upstream";
import { MAX_FILE_BYTES } from "../submission";
import { readBytesLimited, readTextLimited } from "./read-body";

const READ_TIMEOUT_MS = 170_000;
const MAX_CONCURRENT_UPLOADS = 10;
let activeUploads = 0;
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

async function relay(request: Request, { base, userID, mode, identity }: ScopedRuntime, path: string, init: RequestInit, { timeout, allowed, operation }: RelayPolicy) {
  const label = operation ?? path.split("?")[0].replace(/^v1\/(uploads|results)\/.*/, "v1/$1/file");
  try {
    const trace = upstreamTraceHeaders(request);
    const deleteIdentity = mode === "hosted" && init.method === "POST" && path.split("?")[0] === "v1/settings/account/delete"
      ? { ...identityHeaders(identity), "X-Nori-Identity-Email": encodeURIComponent(identity.email) }
      : {};
    const upstream = await fetch(new URL(`users/${encodeURIComponent(userID)}/${path}`, base), {
      ...init, headers: { ...(init.headers as Record<string, string>), ...await runtimeHeaders(mode), ...trace, ...deleteIdentity, "X-Nori-Client": "web" },
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(timeout)]), redirect: "error", cache: "no-store",
    });
    if (upstream.status >= 500) reportUpstreamFailure(label, mode, userID, upstream.status);
    if (mode === "hosted" && upstream.status === 429) {
      reportUpstreamRejection(operation ?? label, request, trace, upstream.headers);
      void upstream.body?.cancel().catch(() => undefined);
      return Response.json({ error: "Nori is busy. Try again shortly." }, {
        status: 429,
        headers: { "Cache-Control": "no-store", ...rejectionHeaders(upstream.headers) },
      });
    }
    if (upstream.status === 204) {
      return new Response(null, {
        status: 204,
        headers: { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" },
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
    if (!request.signal.aborted) reportUpstreamFailure(label, mode, userID);
    const error = !operation ? "Connection to Nori's runtime was interrupted."
      : mode === "local" ? "Could not reach the local agent. Check that the Go runner is running, then retry."
      : "Connection to Nori's runtime was interrupted. Refresh history to confirm the task's status.";
    return Response.json({ error }, { status: 502 });
  }
}

const SHARED_READS = new Set(["v1/capabilities", "v1/integrations", "v1/connections"]);
const SHARED_TTL_MS = 8_000;
const sharedReads = new Map<string, { expires: number; text: Promise<string | null> }>();
const SHARED_HEADERS = { "Content-Type": "application/json", "Cache-Control": "no-cache, no-transform", "X-Content-Type-Options": "nosniff" };

function forgetSharedReads(userID: string) {
  for (const key of sharedReads.keys()) if (key.startsWith(userID + "\n")) sharedReads.delete(key);
}

async function sharedRead(request: Request, user: ScopedRuntime, path: string, allowed: RelayPolicy["allowed"]) {
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
    const response = await relay(request, user, path, {}, { timeout: READ_TIMEOUT_MS, allowed });
    if (response.status !== 200 || !json(response.headers.get("content-type") ?? "")) {
      drop();
      settle(null);
      return response;
    }
    const value = await response.text();
    settle(value);
    return new Response(value, { status: 200, headers: SHARED_HEADERS });
  } catch {
    drop();
    settle(null);
    return Response.json({ error: "Connection to Nori's runtime was interrupted." }, { status: 502 });
  }
}

async function forward(request: Request, mode: Mode, path: string, init: RequestInit, allowed: RelayPolicy["allowed"]) {
  const target = runtimeTarget(mode);
  if (target instanceof Response) return target;
  const user = await resolveUser(request.headers, target);
  if ("status" in user) return accessFailureResponse(user);
  return forwardForUser(request, user, path, init, allowed);
}

async function forwardForUser(request: Request, user: ScopedRuntime, path: string, init: RequestInit, allowed: RelayPolicy["allowed"]) {
  const { mode } = user;
  const read = !init.method || init.method === "GET";
  if (read && mode === "hosted" && SHARED_READS.has(path)) return sharedRead(request, user, path, allowed);
  const response = await relay(request, user, path, init, { timeout: READ_TIMEOUT_MS, allowed });
  if (!read) forgetSharedReads(user.userID);
  if (init.method === "POST" && path.split("?")[0] === "v1/settings/account/delete" && (response.status === 204 || response.status === 200)) {
    evictResolvedUser(user.userID);
    const headers = new Headers(response.headers);
    headers.set("Cache-Control", "no-store");
    headers.append("Set-Cookie", signedOutCookie("1", 60 * 60 * 24 * 365));
    const oidcCookie = process.env.NORI_OIDC_COOKIE;
    if (oidcCookie && /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/.test(oidcCookie)) {
      headers.append("Set-Cookie", `${oidcCookie}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0${process.env.NODE_ENV === "production" ? "; Secure" : ""}`);
    }
    return new Response(response.status === 204 ? null : response.body, { status: response.status, headers });
  }
  return response;
}

function allowedCaller(request: Request, mode: Mode, sameSite: (request: Request) => boolean) {
  return sameSite(request) && (mode !== "local" || localDevelopment(request.headers));
}

const SEGMENT = /^[A-Za-z0-9._:@-]{1,200}$/;

export function validSegment(segment: string) {
  return segment !== "" && segment !== "." && segment !== ".." && SEGMENT.test(segment);
}

export async function operationRoute(request: Request, operation: string) {
  const mode = runtimeMode(request.headers);
  const local = mode === "local";
  if (!isRuntimeMethod(operation)) return notFound();
  if (!sameOrigin(request)) return forbidden();
  const target = runtimeTarget(mode);
  if (target instanceof Response) return local ? target : forbidden();
  const user = await resolveUser(request.headers, target);
  if ("status" in user) return accessFailureResponse(user);
  const limit = local ? 64_000 : 128_000;
  const raw = await readTextLimited(request, limit);
  if (!raw.ok) {
    if (raw.status === 413) return Response.json({ error: "Request is too large." }, { status: 413 });
    return Response.json({ error: local ? "Send a JSON object." : "Request body must be JSON." }, { status: 400 });
  }
  let body: string;
  try {
    body = JSON.stringify(JSON.parse(raw.value));
  } catch {
    return Response.json({ error: local ? "Send a JSON object." : "Request body must be JSON." }, { status: 400 });
  }
  if (new TextEncoder().encode(body).length > limit) return Response.json({ error: "Request is too large." }, { status: 413 });
  const response = await relay(request, user, `api/${operation}`, {
    method: "POST", headers: { "Content-Type": "application/json" }, body,
  }, {
    timeout: local ? 100_000 : 145_000,
    allowed: (type) => local || json(type) || type.startsWith("image/png"),
    operation,
  });
  forgetSharedReads(user.userID);
  return response;
}

function historyQuery(request: Request, afterAllowed: boolean, beforeAllowed: boolean) {
  const incoming = new URL(request.url).searchParams;
  const query = new URLSearchParams();
  const after = afterAllowed && (request.headers.get("last-event-id") || incoming.get("after"));
  if (after) query.set("after", after);
  const before = beforeAllowed && incoming.get("before");
  if (before) query.set("before", before);
  return query.size ? `?${query}` : "";
}

export async function readRoute(request: Request, name: string) {
  const mode = runtimeMode(request.headers);
  if (!isRuntimeRead(name)) return notFound();
  if (!allowedCaller(request, mode, sameOriginRead)) return forbidden();
  return forward(request, mode, `v1/${name}${historyQuery(request, true, true)}`, {},
    (type) => json(type) || eventStream(type));
}

export async function uploadRoute(request: Request, path = "v1/uploads") {
  const mode = runtimeMode(request.headers);
  if (!allowedCaller(request, mode, sameOrigin)) return forbidden();
  const target = runtimeTarget(mode);
  if (target instanceof Response) return target;
  const user = await resolveUser(request.headers, target);
  if ("status" in user) return accessFailureResponse(user);
  if (activeUploads >= MAX_CONCURRENT_UPLOADS) {
    return Response.json({ error: "Nori is busy. Try uploading again shortly.", retryable: true }, {
      status: 429,
      headers: { "Cache-Control": "no-store", "Retry-After": "5" },
    });
  }
  activeUploads++;
  try {
    const incoming = new URL(request.url).searchParams;
    const query = new URLSearchParams({ id: incoming.get("id") ?? "", filename: incoming.get("filename") ?? "" });
    const read = await readBytesLimited(request, MAX_FILE_BYTES);
    if (!read.ok) {
      if (read.status === 413) return Response.json({ error: "A file can be at most 10 MB." }, { status: 413 });
      return Response.json({ error: "The upload was interrupted, so nothing was saved. Try again.", retryable: true }, { status: 400 });
    }
    return await forwardForUser(request, user, `${path}?${query}`, { method: "POST", body: read.value, headers: { "Content-Type": request.headers.get("content-type") || "application/octet-stream" } }, json);
  } finally {
    activeUploads--;
  }
}

export async function fileRoute(request: Request, path: string) {
  const mode = runtimeMode(request.headers);
  if (!allowedCaller(request, mode, sameOriginRead)) return forbidden();
  const segments = path.split("/");
  if (segments.length < 3 || segments[0] !== "v1" || !segments.every(validSegment)) return notFound();
  return forward(request, mode, path, {}, (type) => /^(application\/json|application\/pdf|application\/octet-stream|image\/(png|jpeg|webp)|text\/csv|text\/plain)/.test(type));
}

export async function capabilitiesRoute(request: Request) {
  if (!sameOriginRead(request)) return forbidden();
  return forward(request, runtimeMode(request.headers), "v1/capabilities", {}, json);
}

const ASSISTANT_FAMILIES = new Set([
  "integrations", "connections", "interactions", "work", "automations", "library", "search", "permissions",
  "preferences", "mcp", "assistant", "mutations", "operations", "memory", "inbox", "tools", "settings",
]);

export async function assistantRoute(request: Request, family: string, segments: string[]) {
  const mode = runtimeMode(request.headers);
  if (!ASSISTANT_FAMILIES.has(family) || segments.length > 4 || !segments.every(validSegment)) return notFound();
  const read = request.method === "GET";
  if (!read && !["POST", "PUT", "DELETE"].includes(request.method)) return notFound();
  const incoming = new URL(request.url).searchParams;
  const path = ["v1", family, ...segments].map(encodeURIComponent).join("/") + (incoming.size ? `?${incoming}` : "");
  if (family === "connections" && segments.join("/") === "oauth/callback") {
    if (!read) return forbidden();
    if (incoming.get("state")?.startsWith("gw1.")) return googleWorkspaceCallback(request, mode, incoming);
    if (request.headers.get("sec-fetch-mode") !== "navigate") return forbidden();
    return oauthCallback(request, mode, path);
  }
  if (!allowedCaller(request, mode, read ? sameOriginRead : sameOrigin)) return forbidden();
  let body: string | undefined;
  if (!read) {
    const raw = await readTextLimited(request, 128_000);
    if (!raw.ok) {
      return Response.json({ error: raw.status === 413 ? "Request is too large." : "Request body must be JSON." }, { status: raw.status });
    }
    try {
      body = raw.value ? JSON.stringify(JSON.parse(raw.value)) : "{}";
    } catch {
      return Response.json({ error: "Request body must be JSON." }, { status: 400 });
    }
    if (new TextEncoder().encode(body).length > 128_000) return Response.json({ error: "Request is too large." }, { status: 413 });
  }
  const init: RequestInit = read ? {} : { method: request.method, body, headers: { "Content-Type": "application/json" } };
  return forward(request, mode, path, init, (type) => json(type) || (read && family === "library" && /^(application\/(pdf|octet-stream)|image\/(png|jpeg|webp)|text\/(plain|markdown|csv))/.test(type)));
}

async function googleWorkspaceCallback(request: Request, mode: Mode, params: URLSearchParams) {
  const fail = () => new Response(null, { status: 303, headers: { Location: "/apps?result=failed", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer" } });
  const target = runtimeTarget(mode);
  if (target instanceof Response) return fail();
  const query = new URLSearchParams();
  for (const key of ["state", "code", "error", "picked_file_ids"]) {
    const value = params.get(key);
    if (value !== null) query.set(key, value);
  }
  if (query.toString().length > 12_000) return fail();
  try {
    const upstream = await fetch(new URL(`api/oauth/google-workspace/callback?${query}`, target.base), {
      headers: { ...await runtimeHeaders(mode), ...upstreamTraceHeaders(request), "X-Nori-Redirect": "json" },
      redirect: "error", cache: "no-store", signal: AbortSignal.any([request.signal, AbortSignal.timeout(30_000)]),
    });
    if (!upstream.ok || !json(upstream.headers.get("content-type") ?? "")) {
      void upstream.body?.cancel().catch(() => undefined);
      return fail();
    }
    const { location } = await upstream.json() as { location?: unknown };
    if (typeof location !== "string" || location.includes("\\")) return fail();
    if ((!location.startsWith("/") || location.startsWith("//")) && !location.startsWith("nori://oauth/callback?")) return fail();
    return new Response(null, { status: 303, headers: { Location: location, "Cache-Control": "no-store", "Referrer-Policy": "no-referrer" } });
  } catch {
    return fail();
  }
}

async function oauthCallback(request: Request, mode: Mode, path: string) {
  const target = runtimeTarget(mode);
  if (target instanceof Response) return target;
  const user = await resolveUser(request.headers, target);
  if ("status" in user) return accessFailureResponse(user);
  try {
    const upstream = await fetch(new URL(`users/${encodeURIComponent(user.userID)}/${path}${path.includes("?") ? "&" : "?"}nori_redirect=json`, user.base), {
      headers: { ...await runtimeHeaders(mode), ...upstreamTraceHeaders(request), "X-Nori-Client": "web", "X-Nori-Redirect": "json" }, redirect: "manual", cache: "no-store",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(30_000)]),
    });
    let location = upstream.headers.get("location") ?? "";
    if (!location && upstream.ok && json(upstream.headers.get("content-type") ?? "")) {
      const value = await upstream.json().catch(() => null) as { location?: unknown } | null;
      if (typeof value?.location === "string") location = value.location;
    } else void upstream.body?.cancel().catch(() => undefined);
    const safe = location.startsWith("/") && !location.startsWith("//") && !location.startsWith("/\\") && !location.includes("\\") ? location : "/apps?result=failed";
    return new Response(null, { status: 303, headers: { Location: safe, "Cache-Control": "no-store" } });
  } catch {
    return new Response(null, { status: 303, headers: { Location: "/apps?result=failed", "Cache-Control": "no-store" } });
  }
}
