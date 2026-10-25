import { localDevelopment, sameOrigin } from "@/lib/poc/auth";

export async function POST(request: Request, context: { params: Promise<{ operation: string }> }) {
  const { operation } = await context.params;
  const configured = process.env.NORI_LOCAL_AGENT_URL;
  if (!configured || !localDevelopment(request.headers) || !["session", "turn"].includes(operation)) {
    return Response.json({ error: "Not found." }, { status: 404 });
  }
  if (!sameOrigin(request)) return Response.json({ error: "Forbidden." }, { status: 403 });
  let base: URL;
  try {
    base = new URL(configured);
    if (base.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(base.hostname) ||
      base.username || base.password || base.search || base.hash || base.pathname !== "/") throw new Error();
  } catch {
    return Response.json({ error: "NORI_LOCAL_AGENT_URL must be a loopback HTTP origin." }, { status: 503 });
  }
  let body: string;
  try {
    body = JSON.stringify(await request.json());
    if (body.length > 64_000) return Response.json({ error: "Request is too large." }, { status: 413 });
  } catch {
    return Response.json({ error: "Send a JSON object." }, { status: 400 });
  }
  try {
    const response = await fetch(new URL(`/api/${operation}`, base), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body,
      cache: "no-store",
      redirect: "error",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(80_000)]),
    });
    return new Response(response.body, {
      status: response.status,
      headers: { "Content-Type": "application/json", "Cache-Control": "no-store" },
    });
  } catch {
    return Response.json({ error: "Could not reach the local agent. Check that the Go runner is running, then retry." }, { status: 502 });
  }
}
