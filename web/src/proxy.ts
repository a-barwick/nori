import { NextResponse, type NextRequest } from "next/server";
import { accessFailureResponse, resolveUser } from "@/lib/runtime/auth";

export async function proxy(request: NextRequest) {
  const user = await resolveUser(request.headers);
  if ("status" in user) return accessFailureResponse(user, true);
  const headers = new Headers(request.headers);
  headers.delete("x-nori-user");
  // IAP uses this to invoke Cloud Run. It must never become upstream credentials.
  headers.delete("x-serverless-authorization");
  return NextResponse.next({ request: { headers } });
}

export const config = {
  // API and envelope handlers check the caller and resolve the user themselves.
  matcher: ["/((?!api/|sentry-tunnel$|_next/static|_next/image|favicon.ico).*)"],
};
