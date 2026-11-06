import { resolveUser, userPath, sameOrigin } from "./auth";
import { runtimeAuthorization } from "./credentials";
import { isRuntimeMethod } from "./methods";
import { reportUpstreamFailure, reportUpstreamRejection, rejectionHeaders, upstreamTraceHeaders } from "@/lib/observability/upstream";

export async function runtimeRequest(request: Request, method: string) {
  if (!isRuntimeMethod(method)) return Response.json({ error: "Not found." }, { status: 404 });
  const user = await resolveUser(request.headers, false);
  if (!sameOrigin(request) || !user) {
    return Response.json({ error: "Forbidden." }, { status: 403 });
  }
  const configured = process.env.NORI_RUNTIME_URL;
  if (!configured) {
    return Response.json({ error: "Nori's runtime is not configured yet." }, { status: 503 });
  }
  let target: URL;
  try {
    // Append deliberately: native Agent Runtime's invoke root already ends in /api/.
    target = new URL(`${configured.replace(/\/+$/, "")}/${userPath(user.id, `api/${method}`)}`);
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
