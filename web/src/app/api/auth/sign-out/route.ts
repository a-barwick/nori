import { sameOrigin } from "@/lib/runtime/auth";
import { signedOutCookie } from "@/lib/runtime/session";

export async function POST(request: Request) {
  if (!sameOrigin(request)) return Response.json({ error: "Forbidden." }, { status: 403 });
  const location = process.env.NORI_AUTH_PROVIDER === "iap"
    ? "/signed-out?gcp-iap-mode=CLEAR_LOGIN_COOKIE"
    : "/signed-out";
  const headers = new Headers({ Location: location, "Cache-Control": "no-store" });
  headers.append("Set-Cookie", signedOutCookie("1", 60 * 60 * 24 * 365));
  const oidcCookie = process.env.NORI_OIDC_COOKIE;
  if (oidcCookie && /^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/.test(oidcCookie)) {
    headers.append("Set-Cookie", `${oidcCookie}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0${process.env.NODE_ENV === "production" ? "; Secure" : ""}`);
  }
  return new Response(null, { status: 303, headers });
}
