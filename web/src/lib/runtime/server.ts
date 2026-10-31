import { GoogleAuth } from "google-auth-library";
import { isOwner, sameOrigin } from "./auth";

const auth = new GoogleAuth({ scopes: ["https://www.googleapis.com/auth/cloud-platform"] });
const METHODS = new Set(["session", "turn", "input", "cancel", "command", "artifact", "trace"]);

export async function runtimeRequest(request: Request, method: string) {
  if (!METHODS.has(method)) return Response.json({ error: "Not found." }, { status: 404 });
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
    const token = await auth.getAccessToken();
    if (!token) throw new Error("No runtime credentials");
    const upstream = await fetch(target, {
      method: "POST",
      // Fresh headers: browser credentials and IAP headers are never forwarded.
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` },
      body,
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(145_000)]),
      redirect: "error",
      cache: "no-store",
    });
    const contentType = upstream.headers.get("content-type") || "application/json";
    if (!contentType.includes("application/json") && !contentType.startsWith("image/png")) {
      return Response.json({ error: "The runtime returned an unsupported response." }, { status: 502 });
    }
    return new Response(upstream.body, {
      status: upstream.status,
      headers: { "Content-Type": contentType, "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" },
    });
  } catch {
    return Response.json({
      error: "Connection to Nori's runtime was interrupted. Refresh history to confirm the task's status.",
    }, { status: 502 });
  }
}
