export const SIGNED_OUT_COOKIE = "nori_signed_out";
export const SIGNED_OUT_EVENT_KEY = "nori.signed-out";

export function isSignedOut(headers: Headers) {
  return (headers.get("cookie") ?? "").split(";").some((entry) => entry.trim() === `${SIGNED_OUT_COOKIE}=1`);
}

export function signedOutCookie(value: string, maxAge: number) {
  const secure = process.env.NODE_ENV === "production" ? "; Secure" : "";
  return `${SIGNED_OUT_COOKIE}=${value}; Path=/; HttpOnly; SameSite=Lax; Max-Age=${maxAge}${secure}`;
}
