import { POST as forwardEnvelope } from "@sentry/nextjs/tunnel";
import { accessFailureResponse, resolveUser, sameOrigin } from "@/lib/runtime/auth";
import { createIpRateLimiterFromEnv, rateLimitResponse } from "@/lib/runtime/rate-limit";
import { readBytesLimited } from "@/lib/runtime/read-body";

const tunnelRateLimiter = createIpRateLimiterFromEnv();

export async function POST(request: Request) {
  const limit = tunnelRateLimiter.checkRequest(request.headers);
  if (!limit.allowed) {
    return rateLimitResponse(limit.retryAfterSeconds);
  }
  if (!sameOrigin(request)) {
    return new Response("Forbidden.", { status: 403 });
  }
  const user = await resolveUser(request.headers);
  if ("status" in user) return accessFailureResponse(user);
  const length = Number(request.headers.get("content-length"));
  if (!Number.isSafeInteger(length) || length <= 0 || length > 1_048_576) {
    return new Response("Invalid envelope size.", { status: 413 });
  }
  const body = await readBytesLimited(request, 1_048_576);
  if (!body.ok) return new Response(body.status === 413 ? "Invalid envelope size." : "Incomplete envelope.", { status: body.status });
  if (body.value.byteLength !== length) return new Response("Incomplete envelope.", { status: 400 });
  const envelope = new Request(request.url, {
    method: "POST",
    headers: { "content-length": String(length) },
    body: body.value,
  });
  return forwardEnvelope(envelope);
}
