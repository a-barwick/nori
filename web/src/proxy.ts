import { NextResponse, type NextRequest } from "next/server";
import { accessFailureResponse, resolveUser } from "@/lib/runtime/auth";
import { reconnectingPage } from "@/lib/runtime/reconnecting";
import { isSignedOut } from "@/lib/runtime/session";

export async function proxy(request: NextRequest) {
  if (request.nextUrl.pathname === "/signed-out") {
    return isSignedOut(request.headers) ? NextResponse.next() : NextResponse.redirect(new URL("/", request.url));
  }
  if (isSignedOut(request.headers)) return NextResponse.redirect(new URL("/signed-out", request.url));
  const user = await resolveUser(request.headers);
  if ("status" in user) {
    const document = request.headers.get("sec-fetch-dest") === "document" && !request.headers.has("rsc");
    return document && user.status !== 403 ? reconnectingPage(user.status, user.retryAfter) : accessFailureResponse(user, true);
  }
  const headers = new Headers(request.headers);
  headers.delete("x-nori-user");
  headers.set("x-nori-resource-scope", user.userID);
  headers.delete("x-serverless-authorization");
  return NextResponse.next({ request: { headers } });
}

export const config = {
  matcher: ["/((?!api/|sentry-tunnel$|_next/static|_next/image|favicon.ico).*)"],
};
