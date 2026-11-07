import { POST as forwardEnvelope } from "@sentry/nextjs/tunnel";
import { isUser, sameOrigin } from "@/lib/runtime/auth";

export async function POST(request: Request) {
  if (!sameOrigin(request) || !(await isUser(request.headers))) {
    return new Response("Forbidden.", { status: 403 });
  }
  const length = Number(request.headers.get("content-length"));
  if (!Number.isSafeInteger(length) || length <= 0 || length > 1_048_576) {
    return new Response("Invalid envelope size.", { status: 413 });
  }
  let body: ArrayBuffer;
  try { body = await request.arrayBuffer(); }
  catch { return new Response("Incomplete envelope.", { status: 400 }); }
  if (body.byteLength !== length) return new Response("Incomplete envelope.", { status: 400 });
  // The SDK validates the envelope DSN against the configured server DSN.
  // Remove even diagnostic headers (IP, referrer, user agent) before it forwards.
  const envelope = new Request(request.url, {
    method: "POST",
    headers: { "content-length": String(length) },
    body,
  });
  return forwardEnvelope(envelope);
}
