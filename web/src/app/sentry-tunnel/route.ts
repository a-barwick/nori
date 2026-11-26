import { POST as forwardEnvelope } from "@sentry/nextjs/tunnel";
import { accessFailureResponse, resolveUser, sameOrigin } from "@/lib/runtime/auth";

export async function POST(request: Request) {
  if (!sameOrigin(request)) {
    return new Response("Forbidden.", { status: 403 });
  }
  const user = await resolveUser(request.headers);
  if ("status" in user) return accessFailureResponse(user);
  const length = Number(request.headers.get("content-length"));
  if (!Number.isSafeInteger(length) || length <= 0 || length > 1_048_576) {
    return new Response("Invalid envelope size.", { status: 413 });
  }
  let body: ArrayBuffer;
  try { body = await request.arrayBuffer(); }
  catch { return new Response("Incomplete envelope.", { status: 400 }); }
  if (body.byteLength !== length) return new Response("Incomplete envelope.", { status: 400 });
  const envelope = new Request(request.url, {
    method: "POST",
    headers: { "content-length": String(length) },
    body,
  });
  return forwardEnvelope(envelope);
}
