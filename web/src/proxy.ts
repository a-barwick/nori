import { NextResponse, type NextRequest } from "next/server";
import { accessFailureResponse, resolveUser } from "@/lib/runtime/auth";
import { reconnectingPage } from "@/lib/runtime/reconnecting";

export async function proxy(request: NextRequest) {
  const user = await resolveUser(request.headers);
  if ("status" in user) {
    // A screen opened while the service is down gets one calm page that checks again on its own, not bare text.
    const document = request.headers.get("sec-fetch-dest") === "document" && !request.headers.has("rsc");
    return document && user.status !== 403 ? reconnectingPage(user.status, user.retryAfter) : accessFailureResponse(user, true);
  }
  const headers = new Headers(request.headers);
  headers.delete("x-nori-user");
  // Strip proxy-injected authorization headers so they can never be forwarded as upstream credentials.
  headers.delete("x-serverless-authorization");
  return NextResponse.next({ request: { headers } });
}

export const config = {
  // API and envelope handlers check the caller and resolve the user themselves.
  matcher: ["/((?!api/|sentry-tunnel$|_next/static|_next/image|favicon.ico).*)"],
};
