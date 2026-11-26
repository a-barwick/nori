import { runtimeHeaders } from "./credentials";
import { iapIdentity } from "./iap";
import { oidcIdentity } from "./oidc";
import { localDevelopment, runtimeMode, runtimeTarget, type RuntimeTarget } from "./target";
import { SERVICE_UNAVAILABLE } from "./messages";
import { isSignedOut } from "./session";

const LOCAL_EMAIL = (process.env.NORI_LOCAL_EMAIL?.trim() || process.env.OWNER_EMAIL?.trim() || "").toLowerCase();
type Identity = { email: string; name?: string };

const lastRejection = new Map<string, number>();
function rejected(stage: string, status?: number): null {
  if (Date.now() - (lastRejection.get(stage) ?? 0) >= 60_000) {
    lastRejection.set(stage, Date.now());
    console.warn(JSON.stringify({ severity: "WARNING", message: "nori.auth.rejected", stage, ...(status ? { status } : {}) }));
  }
  return null;
}

function extractVerifiedOIDCName(headers: Headers): string | undefined {
  let token: string | undefined;
  const authorization = headers.get("authorization");
  if (authorization?.startsWith("Bearer ")) {
    token = authorization.slice(7);
  } else {
    const cookieName = process.env.NORI_OIDC_COOKIE;
    if (cookieName) {
      const entry = headers.get("cookie")?.split(";").map(v => v.trim()).find(v => v.startsWith(`${cookieName}=`));
      token = entry ? decodeURIComponent(entry.slice(cookieName.length + 1)) : undefined;
    }
  }
  if (!token) return undefined;
  const parts = token.split(".");
  if (parts.length < 2) return undefined;
  try {
    const payload = JSON.parse(Buffer.from(parts[1], "base64url").toString("utf8")) as { name?: unknown };
    if (typeof payload.name === "string" && payload.name.trim()) {
      return payload.name.trim();
    }
  } catch {
    return undefined;
  }
  return undefined;
}

async function identify(headers: Headers): Promise<Identity | null> {
  if (localDevelopment(headers)) return LOCAL_EMAIL ? { email: LOCAL_EMAIL } : null;
  return providerIdentity(headers);
}

export type ScopedRuntime = RuntimeTarget & { userID: string };
export type AccessFailure = { status: 400 | 403 | 429 | 503; error: string; code?: string; retryAfter?: string };
const denied: AccessFailure = { status: 403, error: "Sign in with an account that has access to Nori." };
const unavailable: AccessFailure = { status: 503, error: SERVICE_UNAVAILABLE };

export function accessFailureResponse(failure: AccessFailure, plainText = false) {
  const headers = new Headers({ "Cache-Control": "no-store" });
  if (failure.retryAfter) headers.set("Retry-After", failure.retryAfter);
  const init = { status: failure.status, headers };
  return plainText
    ? new Response(failure.error, init)
    : Response.json(failure.code ? { error: failure.error, code: failure.code } : { error: failure.error }, init);
}

const RESOLVED_TTL_MS = 5 * 60_000;
const resolved = new Map<string, { userID: string; expires: number }>();

export function evictResolvedUser(userID: string): void {
  for (const [key, entry] of resolved) {
    if (entry.userID === userID) {
      resolved.delete(key);
    }
  }
}

export async function resolveUser(headers: Headers, target = runtimeTarget(runtimeMode(headers))): Promise<ScopedRuntime | AccessFailure> {
  if (isSignedOut(headers)) return denied;
  if (target instanceof Response) return unavailable;
  if (target.mode === "local" && !localDevelopment(headers)) return denied;
  const identity = await identify(headers);
  if (!identity) return denied;
  const key = target.mode + "\n" + target.base + "\n" + identity.email;
  const known = resolved.get(key);
  if (known && known.expires > Date.now()) return { ...target, userID: known.userID };
  try {
    const payload: Record<string, string> = { email: identity.email };
    if (identity.name) payload.name = identity.name;
    const response = await fetch(new URL("api/users/resolve", target.base), {
      method: "POST", headers: { "Content-Type": "application/json", ...await runtimeHeaders(target.mode) },
      body: JSON.stringify(payload), cache: "no-store", redirect: "error",
      signal: AbortSignal.timeout(10_000),
    });
    if (!response.ok) {
      rejected("user_resolution", response.status);
      if (response.status === 429) {
        void response.body?.cancel().catch(() => undefined);
        const retryAfter = response.headers.get("retry-after") ?? "";
        return { status: 429, error: "Nori is busy. Try again shortly.", retryAfter: /^\d{1,6}$/.test(retryAfter) ? retryAfter : undefined };
      }
      if (response.status === 403) {
        try {
          const body = await response.json() as { error?: unknown; code?: unknown };
          if (body.code === "invite_required") {
            return {
              status: 403,
              error: typeof body.error === "string" && body.error ? body.error : "An invite code is required to access Nori.",
              code: "invite_required",
            };
          }
        } catch {
          // Fall through to generic denied response.
        }
        return denied;
      }
      void response.body?.cancel().catch(() => undefined);
      return unavailable;
    }
    const value = await response.json();
    if (typeof value.id !== "string" || !(value.id === "owner" || /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(value.id))) return unavailable;
    if (resolved.size >= 1000) resolved.clear();
    resolved.set(key, { userID: value.id, expires: Date.now() + RESOLVED_TTL_MS });
    return { ...target, userID: value.id };
  } catch {
    rejected("user_resolution_connection");
    return unavailable;
  }
}

export async function redeemInvite(
  headers: Headers,
  inviteCode: string,
  target = runtimeTarget(runtimeMode(headers)),
): Promise<ScopedRuntime | AccessFailure> {
  if (isSignedOut(headers)) return denied;
  if (target instanceof Response) return unavailable;
  if (target.mode === "local" && !localDevelopment(headers)) return denied;
  const identity = await identify(headers);
  if (!identity) return denied;
  const trimmedCode = inviteCode.trim();
  if (!trimmedCode) {
    return { status: 400, error: "Invite code is required.", code: "invite_code_required" };
  }
  const key = target.mode + "\n" + target.base + "\n" + identity.email;
  try {
    const payload: Record<string, string> = { email: identity.email, invite_code: trimmedCode };
    if (identity.name) payload.name = identity.name;
    const response = await fetch(new URL("api/users/redeem", target.base), {
      method: "POST",
      headers: { "Content-Type": "application/json", ...await runtimeHeaders(target.mode) },
      body: JSON.stringify(payload),
      cache: "no-store",
      redirect: "error",
      signal: AbortSignal.timeout(10_000),
    });
    if (!response.ok) {
      rejected("invite_redemption", response.status);
      if (response.status === 429) {
        void response.body?.cancel().catch(() => undefined);
        const retryAfter = response.headers.get("retry-after") ?? "";
        return { status: 429, error: "Nori is busy. Try again shortly.", retryAfter: /^\d{1,6}$/.test(retryAfter) ? retryAfter : undefined };
      }
      if (response.status === 400 || response.status === 403) {
        try {
          const body = await response.json() as { error?: unknown; code?: unknown };
          return {
            status: response.status,
            error: typeof body.error === "string" && body.error ? body.error : "Invalid invite code.",
            ...(typeof body.code === "string" && body.code ? { code: body.code } : {}),
          };
        } catch {
          return { status: response.status, error: "Invalid invite code." };
        }
      }
      void response.body?.cancel().catch(() => undefined);
      return unavailable;
    }
    const value = await response.json();
    if (typeof value.id !== "string" || !(value.id === "owner" || /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(value.id))) return unavailable;
    if (resolved.size >= 1000) resolved.clear();
    resolved.set(key, { userID: value.id, expires: Date.now() + RESOLVED_TTL_MS });
    return { ...target, userID: value.id };
  } catch {
    rejected("invite_redemption_connection");
    return unavailable;
  }
}

export function sameOrigin(request: Request) {
  const origin = request.headers.get("origin");
  if (!origin) return false;
  const site = request.headers.get("sec-fetch-site");
  if (site && site !== "same-origin") return false;
  try {
    const expected = process.env.NORI_APP_ORIGIN ||
      `${localDevelopment(request.headers) ? "http" : "https"}://${request.headers.get("host")}`;
    return origin === new URL(expected).origin;
  } catch {
    return false;
  }
}

export function sameOriginRead(request: Request): boolean {
  return request.headers.get("sec-fetch-site") === "same-origin";
}

async function providerIdentity(headers: Headers): Promise<Identity | null> {
  const cloud = process.env.NORI_CLOUD || "aws";
  if (!["aws", "gcp", "local"].includes(cloud)) return null;
  const provider = process.env.NORI_AUTH_PROVIDER || "oidc";
  if (provider === "oidc") {
    const id = await oidcIdentity(headers);
    if (!id) return null;
    const name = extractVerifiedOIDCName(headers);
    return name ? { email: id.email, name } : id;
  }
  if (provider === "iap") return iapIdentity(headers);
  return null;
}
