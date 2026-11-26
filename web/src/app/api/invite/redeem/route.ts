import { accessFailureResponse, redeemInvite, sameOrigin } from "@/lib/runtime/auth";
import { createIpRateLimiterFromEnv, rateLimitResponse } from "@/lib/runtime/rate-limit";
import { readTextLimited } from "@/lib/runtime/read-body";

const inviteRateLimiter = createIpRateLimiterFromEnv();

export async function POST(request: Request) {
  const limit = inviteRateLimiter.checkRequest(request.headers);
  if (!limit.allowed) {
    return rateLimitResponse(limit.retryAfterSeconds);
  }
  if (!sameOrigin(request)) {
    return Response.json({ error: "Forbidden." }, { status: 403, headers: { "Cache-Control": "no-store" } });
  }
  const raw = await readTextLimited(request, 128_000);
  if (!raw.ok) {
    const error = raw.status === 413 ? "Request is too large." : "Send a valid JSON object.";
    return Response.json({ error }, { status: raw.status, headers: { "Cache-Control": "no-store" } });
  }
  let inviteCode = "";
  try {
    const body = JSON.parse(raw.value) as { invite_code?: unknown; code?: unknown };
    if (typeof body.invite_code === "string") inviteCode = body.invite_code;
    else if (typeof body.code === "string") inviteCode = body.code;
  } catch {
    return Response.json({ error: "Send a valid JSON object." }, { status: 400, headers: { "Cache-Control": "no-store" } });
  }
  const result = await redeemInvite(request.headers, inviteCode);
  if ("status" in result) {
    return accessFailureResponse(result);
  }
  return Response.json({ id: result.userID }, { status: 200, headers: { "Cache-Control": "no-store" } });
}
