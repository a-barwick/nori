import { NextResponse, type NextRequest } from "next/server";
import { accessFailureResponse, resolveUser } from "@/lib/runtime/auth";
import { reconnectingPage } from "@/lib/runtime/reconnecting";
import { isSignedOut } from "@/lib/runtime/session";
import { applySecurityHeaders, buildAppCsp, CSP_NONCE_HEADER, generateCspNonce } from "@/lib/securityHeaders";

const PUBLIC_PATHS = new Set(["/privacy", "/terms"]);

export async function proxy(request: NextRequest) {
  const nonce = generateCspNonce();
  const csp = buildAppCsp(nonce, { isDev: process.env.NODE_ENV !== "production" });
  const requestHeaders = new Headers(request.headers);
  requestHeaders.set(CSP_NONCE_HEADER, nonce);
  requestHeaders.set("Content-Security-Policy", csp);

  const finalize = (response: Response) => {
    applySecurityHeaders(response, request, csp);
    return response;
  };

  if (PUBLIC_PATHS.has(request.nextUrl.pathname)) {
    return finalize(NextResponse.next({ request: { headers: requestHeaders } }));
  }
  if (request.nextUrl.pathname === "/signed-out") {
    return finalize(
      isSignedOut(request.headers)
        ? NextResponse.next({ request: { headers: requestHeaders } })
        : NextResponse.redirect(new URL("/", request.url)),
    );
  }
  if (isSignedOut(request.headers)) {
    return finalize(NextResponse.redirect(new URL("/signed-out", request.url)));
  }
  const user = await resolveUser(request.headers);
  if ("status" in user) {
    const document = request.headers.get("sec-fetch-dest") === "document" && !request.headers.has("rsc");
    return finalize(
      document && user.status !== 403
        ? reconnectingPage(user.status, user.retryAfter, nonce)
        : accessFailureResponse(user, true),
    );
  }
  requestHeaders.delete("x-nori-user");
  requestHeaders.set("x-nori-resource-scope", user.userID);
  requestHeaders.delete("x-serverless-authorization");
  return finalize(NextResponse.next({ request: { headers: requestHeaders } }));
}

export const config = {
  matcher: ["/((?!api/|sentry-tunnel$|_next/static|_next/image|favicon.ico).*)"],
};
