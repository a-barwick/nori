import { localDevelopment, sameOriginRead } from "@/lib/runtime/auth";
import { isRuntimeRead } from "@/lib/runtime/methods";
import { readQuery, streamHeaders } from "@/lib/runtime/server";
import { reportUpstreamFailure, upstreamTraceHeaders } from "@/lib/observability/upstream";

export async function GET(request: Request, context: { params: Promise<{ operation: string }> }) {
  const { operation } = await context.params;
  const configured = process.env.NORI_LOCAL_AGENT_URL;
  if (!configured || !localDevelopment(request.headers) || !isRuntimeRead(operation)) {
    return Response.json({ error: "Not found." }, { status: 404 });
  }
  if (!sameOriginRead(request)) return Response.json({ error: "Forbidden." }, { status: 403 });
  let base: URL;
  try {
    base = new URL(configured);
    if (base.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(base.hostname) ||
      base.username || base.password || base.search || base.hash || base.pathname !== "/") throw new Error();
  } catch {
    return Response.json({ error: "NORI_LOCAL_AGENT_URL must be a loopback HTTP origin." }, { status: 503 });
  }
  try {
    const upstream = await fetch(new URL(`/v1/${operation}${readQuery(request)}`, base), {
      headers: upstreamTraceHeaders(request), cache: "no-store", redirect: "error",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(170_000)]),
    });
    if (upstream.status >= 500) reportUpstreamFailure(operation, "local", upstream.status);
    return new Response(upstream.body, { status: upstream.status, headers: streamHeaders(upstream) });
  } catch {
    if (!request.signal.aborted) reportUpstreamFailure(operation, "local");
    return Response.json({ error: "Could not reach the local agent. Check that the Go runner is running, then retry." }, { status: 502 });
  }
}
