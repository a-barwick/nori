import { accessFailureResponse, resolveUser, sameOrigin } from "@/lib/runtime/auth";
import { browserObservabilityConfig } from "@/lib/observability/config.server";

export async function GET(request: Request) {
  const origin = request.headers.get("origin");
  const site = request.headers.get("sec-fetch-site");
  if ((origin && !sameOrigin(request)) || (site && site !== "same-origin")) {
    return Response.json({ error: "Forbidden." }, { status: 403, headers: { "Cache-Control": "no-store" } });
  }
  const user = await resolveUser(request.headers);
  if ("status" in user) return accessFailureResponse(user);
  return Response.json(browserObservabilityConfig(), { headers: { "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff" } });
}
