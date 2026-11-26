import { runtimeHeaders } from "./credentials";
import { iapIdentity } from "./iap";
import { oidcIdentity } from "./oidc";
import { localDevelopment, runtimeMode, runtimeTarget, type RuntimeTarget } from "./target";
import { SERVICE_UNAVAILABLE } from "./messages";
import { isSignedOut } from "./session";

const LOCAL_EMAIL = (process.env.NORI_LOCAL_EMAIL?.trim() || process.env.OWNER_EMAIL?.trim() || "").toLowerCase();
type Identity = { email: string };

const lastRejection = new Map<string, number>();
function rejected(stage: string, status?: number): null {
  if (Date.now() - (lastRejection.get(stage) ?? 0) >= 60_000) {
    lastRejection.set(stage, Date.now());
    console.warn(JSON.stringify({ severity: "WARNING", message: "nori.auth.rejected", stage, ...(status ? { status } : {}) }));
  }
  return null;
}

async function identify(headers: Headers): Promise<Identity | null> {
  if (localDevelopment(headers)) return LOCAL_EMAIL ? { email: LOCAL_EMAIL } : null;
  return providerIdentity(headers);
}

export type ScopedRuntime = RuntimeTarget & { userID: string };
type AccessFailure = { status: 403 | 429 | 503; error: string; retryAfter?: string };
const denied: AccessFailure = { status: 403, error: "Sign in with an account that has access to Nori." };
const unavailable: AccessFailure = { status: 503, error: SERVICE_UNAVAILABLE };

export function accessFailureResponse(failure: AccessFailure, plainText = false) {
  const headers = new Headers({ "Cache-Control": "no-store" });
  if (failure.retryAfter) headers.set("Retry-After", failure.retryAfter);
  const init = { status: failure.status, headers };
  return plainText ? new Response(failure.error, init) : Response.json({ error: failure.error }, init);
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
    const response = await fetch(new URL("api/users/resolve", target.base), {
      method: "POST", headers: { "Content-Type": "application/json", ...await runtimeHeaders(target.mode) },
      body: JSON.stringify({ email: identity.email }), cache: "no-store", redirect: "error",
      signal: AbortSignal.timeout(10_000),
    });
    if (!response.ok) {
      rejected("user_resolution", response.status);
      void response.body?.cancel().catch(() => undefined);
      if (response.status === 429) {
        const retryAfter = response.headers.get("retry-after") ?? "";
        return { status: 429, error: "Nori is busy. Try again shortly.", retryAfter: /^\d{1,6}$/.test(retryAfter) ? retryAfter : undefined };
      }
      return response.status === 403 ? denied : unavailable;
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
  if (provider === "oidc") return oidcIdentity(headers);
  if (provider === "iap") return iapIdentity(headers);
  return null;
}
