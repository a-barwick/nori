import { isOwner, localDevelopment, sameOrigin, sameOriginRead } from "./auth";
import { isRuntimeRead } from "./methods";
import { runtimeAuthorization } from "./credentials";
import { reportUpstreamFailure, upstreamTraceHeaders } from "@/lib/observability/upstream";

export type Mode = "hosted" | "local";

// A stream ends upstream first. This limit only catches one that stopped responding.
const READ_TIMEOUT_MS = 170_000;
const UPLOAD_TIMEOUT_MS = 170_000;
const MAX_UPLOAD_BYTES = 10 * 1024 * 1024;
const RELAYED = ["content-disposition", "content-security-policy", "content-length"];

function upstreamBase(request: Request, mode: Mode): URL | Response {
  const configured = mode === "local" ? process.env.NORI_LOCAL_AGENT_URL : process.env.NORI_RUNTIME_URL;
  if (!configured) return mode === "local" ? Response.json({ error: "Not found." }, { status: 404 }) : Response.json({ error: "Nori's runtime is not configured yet." }, { status: 503 });
  if (mode === "local" && !localDevelopment(request.headers)) return Response.json({ error: "Not found." }, { status: 404 });
  try {
    const base = new URL(configured.endsWith("/") ? configured : `${configured}/`);
    const valid = mode === "local" ? base.protocol === "http:" && ["127.0.0.1", "localhost", "[::1]"].includes(base.hostname) && base.pathname === "/" : base.protocol === "https:";
    if (!valid || base.username || base.password || base.search || base.hash) throw new Error();
    return base;
  } catch {
    return Response.json({ error: mode === "local" ? "NORI_LOCAL_AGENT_URL must be a loopback HTTP origin." : "Nori's runtime URL is invalid." }, { status: 503 });
  }
}

/** Sends one request to the Go API with this server's own credentials. Browser credentials and IAP headers never go upstream. */
async function forward(request: Request, mode: Mode, path: string, init: RequestInit, timeout: number, allowed: (type: string) => boolean) {
  const base = upstreamBase(request, mode);
  if (base instanceof Response) return base;
  const label = path.split("?")[0].replace(/^v1\/(uploads|results)\/.*/, "v1/$1/file");
  try {
    const authorization: Record<string, string> = mode === "hosted" ? { Authorization: await runtimeAuthorization() } : {};
    const upstream = await fetch(new URL(path, base), {
      ...init, headers: { ...(init.headers as Record<string, string>), ...authorization, ...upstreamTraceHeaders(request) },
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(timeout)]), redirect: "error", cache: "no-store",
    });
    if (upstream.status >= 500) reportUpstreamFailure(label, mode, upstream.status);
    const type = upstream.headers.get("content-type") || "application/json";
    if (!allowed(type)) {
      void upstream.body?.cancel().catch(() => undefined);
      return Response.json({ error: "The runtime returned an unsupported response." }, { status: 502 });
    }
    const headers: Record<string, string> = { "Content-Type": type, "Cache-Control": "no-cache, no-transform", "X-Accel-Buffering": "no", "X-Content-Type-Options": "nosniff" };
    for (const name of RELAYED) {
      const value = upstream.headers.get(name);
      if (value) headers[name] = value;
    }
    return new Response(upstream.body, { status: upstream.status, headers });
  } catch {
    if (!request.signal.aborted) reportUpstreamFailure(label, mode);
    return Response.json({ error: "Connection to Nori's runtime was interrupted." }, { status: 502 });
  }
}

async function allowedCaller(request: Request, mode: Mode, sameSite: (request: Request) => boolean) {
  if (!sameSite(request)) return false;
  return mode === "local" ? localDevelopment(request.headers) : isOwner(request.headers);
}

const forbidden = () => Response.json({ error: "Forbidden." }, { status: 403 });

/** `GET /v1/snapshot` and `GET /v1/events`. EventSource sends its resume position as a header, and Agent Runtime only passes the URL on. */
export async function readRoute(request: Request, mode: Mode, name: string) {
  if (!isRuntimeRead(name)) return Response.json({ error: "Not found." }, { status: 404 });
  if (!(await allowedCaller(request, mode, sameOriginRead))) return forbidden();
  const incoming = new URL(request.url).searchParams;
  const query = new URLSearchParams();
  const after = request.headers.get("last-event-id") || incoming.get("after");
  if (after) query.set("after", after);
  const before = incoming.get("before");
  if (before) query.set("before", before);
  return forward(request, mode, `v1/${name}${query.size ? `?${query}` : ""}`, {}, READ_TIMEOUT_MS,
    (type) => type.includes("application/json") || type.startsWith("text/event-stream"));
}

/** `POST /v1/uploads`. The whole file is read here first, so a dropped upload never reaches the service. */
export async function uploadRoute(request: Request, mode: Mode) {
  if (!(await allowedCaller(request, mode, sameOrigin))) return forbidden();
  if (Number(request.headers.get("content-length") ?? 0) > MAX_UPLOAD_BYTES) return Response.json({ error: "A file can be at most 10 MB." }, { status: 413 });
  const incoming = new URL(request.url).searchParams;
  const query = new URLSearchParams({ id: incoming.get("id") ?? "", filename: incoming.get("filename") ?? "" });
  let body: ArrayBuffer;
  try { body = await request.arrayBuffer(); } catch {
    return Response.json({ error: "The upload was interrupted, so nothing was saved. Try again.", retryable: true }, { status: 400 });
  }
  if (body.byteLength > MAX_UPLOAD_BYTES) return Response.json({ error: "A file can be at most 10 MB." }, { status: 413 });
  return forward(request, mode, `v1/uploads?${query}`, { method: "POST", body, headers: { "Content-Type": request.headers.get("content-type") || "application/octet-stream" } },
    UPLOAD_TIMEOUT_MS, (type) => type.includes("application/json"));
}

/** `GET /v1/uploads/<id>`: the file bytes, with the service's attachment and sandbox headers. */
export async function fileRoute(request: Request, mode: Mode, path: string) {
  if (!(await allowedCaller(request, mode, sameOriginRead))) return forbidden();
  return forward(request, mode, path, {}, READ_TIMEOUT_MS, (type) => /^(application\/json|application\/pdf|application\/octet-stream|image\/(png|jpeg|webp)|text\/csv|text\/plain)/.test(type));
}

/** Additive conversation API. Each route is allowlisted; caller headers never become runtime credentials. */
export async function conversationRoute(request: Request, mode: Mode, segments: string[]) {
  const [id, operation] = segments;
  const validID = id === "assistant" || /^[a-f0-9-]{36}$/.test(id || "");
  const read = request.method === "GET" && (segments.length === 0 || (validID && (segments.length === 1 || (segments.length === 2 && operation === "events"))));
  const write = request.method === "POST" && (segments.length === 0 || (validID && segments.length === 2 && ["messages", "cancel", "approvals"].includes(operation)));
  if (!read && !write) return Response.json({ error: "Not found." }, { status: 404 });
  if (!(await allowedCaller(request, mode, read ? sameOriginRead : sameOrigin))) return forbidden();
  const query = new URLSearchParams();
  if (operation === "events") {
    const incoming = new URL(request.url).searchParams;
    const after = request.headers.get("last-event-id") || incoming.get("after");
    if (after) query.set("after", after);
  }
  let body: string | undefined;
  if (write) {
    if (Number(request.headers.get("content-length") ?? 0) > 128_000) return Response.json({ error: "Request is too large." }, { status: 413 });
    try {
      const value = await request.text();
      if (new TextEncoder().encode(value).length > 128_000) return Response.json({ error: "Request is too large." }, { status: 413 });
      body = value ? JSON.stringify(JSON.parse(value)) : "{}";
    } catch { return Response.json({ error: "Request body must be JSON." }, { status: 400 }); }
  }
  return forward(request, mode, `v1/conversations${segments.length ? "/" + segments.map(encodeURIComponent).join("/") : ""}${query.size ? `?${query}` : ""}`,
    write ? { method: "POST", body, headers: { "Content-Type": "application/json" } } : {}, READ_TIMEOUT_MS,
    type => type.includes("application/json") || (read && operation === "events" && type.startsWith("text/event-stream")));
}

export async function capabilitiesRoute(request: Request, mode: Mode) {
  if (!(await allowedCaller(request, mode, sameOriginRead))) return forbidden();
  return forward(request, mode, "v1/capabilities", {}, READ_TIMEOUT_MS, type => type.includes("application/json"));
}

/** The built chat and the assistant select the same local service during development. */
export function conversationMode(request: Request): Mode {
  return localDevelopment(request.headers) && process.env.NORI_LOCAL_AGENT_URL ? "local" : "hosted";
}
