import { NextResponse, type NextRequest } from "next/server";
import { isUser } from "@/lib/runtime/auth";

export async function proxy(request: NextRequest) {
  if (!(await isUser(request.headers))) {
    return new NextResponse("Nori is private. Sign in through the configured Google IAP endpoint.", {
      status: 403, headers: { "Cache-Control": "no-store" },
    });
  }
  const headers = new Headers(request.headers);
  headers.delete("x-nori-user");
  // IAP uses this to invoke Cloud Run. It must never become upstream credentials.
  headers.delete("x-serverless-authorization");
  return NextResponse.next({ request: { headers } });
}

export const config = {
  matcher: ["/((?!_next/static|_next/image|favicon.ico|api/health$).*)"],
};
