import { isOwner, sameOrigin, sameOriginRead } from "./auth";
import { runtimeAuthorization } from "./credentials";
import { isRuntimeMethod, isRuntimeRead } from "./methods";
import { reportUpstreamFailure, reportUpstreamRejection, rejectionHeaders, upstreamTraceHeaders } from "@/lib/observability/upstream";

export async function runtimeRequest(request: Request, method: string) {
  if (!isRuntimeMethod(method)) return Response.json({ error: "Not found." }, { status: 404 });
  if (!sameOrigin(request) || !(await isOwner(request.headers))) {
    return Response.json({ error: "Forbidden." }, { status: 403 });
  }
  const configured = process.env.NORI_RUNTIME_URL;
  if (!configured) {
    return Response.json({ error: "Nori's runtime is not configured yet." }, { status: 503 });
  }
  let target: URL;
  try {
    // Append deliberately: native Agent Runtime's invoke root already ends in /api/.
    target = new URL(`${configured.replace(/\/+$/, "")}/api/${method}`);
    if (target.protocol !== "https:" || target.username || target.password || target.search || target.hash) throw new Error();
  } catch {
    return Response.json({ error: "Nori's runtime URL is invalid." }, { status: 503 });
  }
  let body: string;
  try {
    body = JSON.stringify(await request.json());
    if (body.length > 128_000) return Response.json({ error: "Request is too large." }, { status: 413 });
  } catch {
    return Response.json({ error: "Request body must be JSON." }, { status: 400 });
  }
  try {
    const authorization = await runtimeAuthorization();
    const traceHeaders = upstreamTraceHeaders(request);
    const upstream = await fetch(target, {
      method: "POST",
      // Fresh headers: browser credentials and IAP headers are never forwarded.
      headers: { "Content-Type": "application/json", Authorization: authorization, ...traceHeaders },
      body,
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(145_000)]),
      redirect: "error",
      cache: "no-store",
    });
    if (upstream.status >= 500) reportUpstreamFailure(method, "hosted", upstream.status);
    if (upstream.status === 429) {
      reportUpstreamRejection(method, request, traceHeaders, upstream.headers);
      void upstream.body?.cancel().catch(() => undefined);
      // Platform rejections may be HTML. Preserve the status without forwarding
      // a private upstream body or turning a rate limit into an unrelated 502.
      return Response.json({ error: "Nori is temporarily busy. Refresh history shortly." }, {
        status: 429,
        headers: { "Cache-Control": "no-store", ...rejectionHeaders(upstream.headers) },
      });
    }
    const contentType = upstream.headers.get("content-type") || "application/json";
    if (!contentType.includes("application/json") && !contentType.startsWith("image/png")) {
      return Response.json({ error: "The runtime returned an unsupported response." }, { status: 502 });
    }
    return new Response(upstream.body, {
      status: upstream.status,
      headers: { "Content-Type": contentType, "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" },
    });
  } catch {
    if (!request.signal.aborted) reportUpstreamFailure(method, "hosted");
    return Response.json({
      error: "Connection to Nori's runtime was interrupted. Refresh history to confirm the task's status.",
    }, { status: 502 });
  }
}

// A stream ends upstream first. This limit only catches one that stopped responding.
const READ_TIMEOUT_MS = 170_000;

/** Forwards `GET /v1/<name>`. EventSource sends its resume position as a header, and Agent Runtime only passes the URL on. */
export function readQuery(request: Request) {
  const incoming = new URL(request.url).searchParams;
  const query = new URLSearchParams();
  const after = request.headers.get("last-event-id") || incoming.get("after");
  if (after) query.set("after", after);
  const before = incoming.get("before");
  if (before) query.set("before", before);
  return query.size ? `?${query}` : "";
}

export function streamHeaders(upstream: Response) {
  return {
    "Content-Type": upstream.headers.get("content-type") || "application/json",
    "Cache-Control": "no-cache, no-transform",
    "X-Accel-Buffering": "no",
    "X-Content-Type-Options": "nosniff",
  };
}

export async function runtimeRead(request: Request, name: string) {
  if (!isRuntimeRead(name)) return Response.json({ error: "Not found." }, { status: 404 });
  if (!sameOriginRead(request) || !(await isOwner(request.headers))) {
    return Response.json({ error: "Forbidden." }, { status: 403 });
  }
  const configured = process.env.NORI_RUNTIME_URL;
  if (!configured) return Response.json({ error: "Nori's runtime is not configured yet." }, { status: 503 });
  let target: URL;
  try {
    target = new URL(`${configured.replace(/\/+$/, "")}/v1/${name}${readQuery(request)}`);
    if (target.protocol !== "https:" || target.username || target.password || target.hash) throw new Error();
  } catch {
    return Response.json({ error: "Nori's runtime URL is invalid." }, { status: 503 });
  }
  try {
    const upstream = await fetch(target, {
      headers: { Authorization: await runtimeAuthorization(), ...upstreamTraceHeaders(request) },
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(READ_TIMEOUT_MS)]),
      redirect: "error",
      cache: "no-store",
    });
    if (upstream.status >= 500) reportUpstreamFailure(name, "hosted", upstream.status);
    const type = upstream.headers.get("content-type") || "";
    if (!type.includes("application/json") && !type.startsWith("text/event-stream")) {
      void upstream.body?.cancel().catch(() => undefined);
      return Response.json({ error: "The runtime returned an unsupported response." }, { status: 502 });
    }
    return new Response(upstream.body, { status: upstream.status, headers: streamHeaders(upstream) });
  } catch {
    if (!request.signal.aborted) reportUpstreamFailure(name, "hosted");
    return Response.json({ error: "Connection to Nori's runtime was interrupted." }, { status: 502 });
  }
}
