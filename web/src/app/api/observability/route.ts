import { isUser, sameOrigin } from "@/lib/runtime/auth";
import { browserObservabilityConfig } from "@/lib/observability/config.server";

export async function GET(request: Request) {
  const origin = request.headers.get("origin");
  const site = request.headers.get("sec-fetch-site");
  if (!(await isUser(request.headers)) || (origin && !sameOrigin(request)) || (site && site !== "same-origin")) {
    return Response.json({ error: "Forbidden." }, { status: 403, headers: { "Cache-Control": "no-store" } });
  }
  return Response.json(browserObservabilityConfig(), { headers: { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" } });
}
