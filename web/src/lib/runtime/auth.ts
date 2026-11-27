import { runtimeHeaders } from "./credentials";
import { iapIdentity } from "./iap";
import { oidcIdentity } from "./oidc";
import { localDevelopment, runtimeMode, runtimeTarget, type RuntimeTarget } from "./target";
import { SERVICE_UNAVAILABLE } from "./messages";
import { isSignedOut } from "./session";
import type { VerifiedIdentity } from "./identity";

const LOCAL_EMAIL = (process.env.NORI_LOCAL_EMAIL?.trim() || process.env.OWNER_EMAIL?.trim() || "").toLowerCase();
type Identity = { email: string; name?: string } & Partial<Pick<VerifiedIdentity, "issuer" | "subject" | "emailAuthoritative">>;

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

export type ScopedRuntime = RuntimeTarget & { userID: string; identity: Identity };
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
const inFlight = new Map<string, Promise<ScopedRuntime | AccessFailure>>();

function identityKey(target: RuntimeTarget, identity: Identity): string {
  return JSON.stringify([target.mode, target.base.toString(), identity.issuer ?? "local", identity.subject ?? identity.email, identity.email]);
}

export function identityHeaders(identity: Identity): Record<string, string> {
  if (identity.issuer === undefined || identity.subject === undefined || identity.emailAuthoritative === undefined) return {};
  return {
    "X-Nori-Identity-Issuer": identity.issuer,
    "X-Nori-Identity-Subject": identity.subject,
    "X-Nori-Identity-Email-Authoritative": String(identity.emailAuthoritative),
  };
}

export function evictResolvedUser(userID: string): void {
  for (const [key, entry] of resolved) {
    if (entry.userID === userID) {
      resolved.delete(key);
    }
  }
}

const USER_ID_PATTERN = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/;

async function requestUserAccount(
  headers: Headers,
  target: RuntimeTarget | Response,
  options: {
    endpoint: string;
    stage: string;
    extraPayload?: Record<string, string>;
    useCache?: boolean;
    onClientError: (response: Response) => Promise<AccessFailure>;
  },
): Promise<ScopedRuntime | AccessFailure> {
  if (isSignedOut(headers)) return denied;
  if (target instanceof Response) return unavailable;
  if (target.mode === "local" && !localDevelopment(headers)) return denied;
  const identity = await identify(headers);
  if (!identity) return denied;
  const key = identityKey(target, identity);
  if (options.useCache) {
    const known = resolved.get(key);
    if (known && known.expires > Date.now()) return { ...target, userID: known.userID, identity };
    const pending = inFlight.get(key);
    if (pending) return pending;
  }
  const run = async (): Promise<ScopedRuntime | AccessFailure> => {
    try {
      const payload: Record<string, string> = { email: identity.email, ...options.extraPayload };
      if (identity.name) payload.name = identity.name;
      const response = await fetch(new URL(options.endpoint, target.base), {
        method: "POST",
        headers: { "Content-Type": "application/json", ...await runtimeHeaders(target.mode), ...identityHeaders(identity) },
        body: JSON.stringify(payload),
        cache: "no-store",
        redirect: "error",
        signal: AbortSignal.timeout(10_000),
      });
      if (!response.ok) {
        rejected(options.stage, response.status);
        if (response.status === 429) {
          void response.body?.cancel().catch(() => undefined);
          const retryAfter = response.headers.get("retry-after") ?? "";
          return { status: 429, error: "Nori is busy. Try again shortly.", retryAfter: /^\d{1,6}$/.test(retryAfter) ? retryAfter : undefined };
        }
        if (response.status === 400 || response.status === 403) {
          return await options.onClientError(response);
        }
        void response.body?.cancel().catch(() => undefined);
        return unavailable;
      }
      const value = await response.json();
      if (typeof value.id !== "string" || !(value.id === "owner" || USER_ID_PATTERN.test(value.id))) return unavailable;
      if (resolved.size >= 1000) resolved.clear();
      if (target.mode === "local" || response.headers.get("x-nori-identity-bound") === "1") {
        resolved.set(key, { userID: value.id, expires: Date.now() + RESOLVED_TTL_MS });
      }
      return { ...target, userID: value.id, identity };
    } catch {
      rejected(`${options.stage}_connection`);
      return unavailable;
    }
  };
  if (!options.useCache) return run();
  const promise = run().finally(() => {
    if (inFlight.get(key) === promise) inFlight.delete(key);
  });
  inFlight.set(key, promise);
  return promise;
}

export async function resolveUser(headers: Headers, target = runtimeTarget(runtimeMode(headers))): Promise<ScopedRuntime | AccessFailure> {
  return requestUserAccount(headers, target, {
    endpoint: "api/users/resolve",
    stage: "user_resolution",
    useCache: true,
    onClientError: async (response) => {
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
    },
  });
}

export async function redeemInvite(
  headers: Headers,
  inviteCode: string,
  target = runtimeTarget(runtimeMode(headers)),
): Promise<ScopedRuntime | AccessFailure> {
  const trimmedCode = inviteCode.trim();
  if (!trimmedCode) {
    if (isSignedOut(headers)) return denied;
    if (target instanceof Response) return unavailable;
    if (target.mode === "local" && !localDevelopment(headers)) return denied;
    if (!(await identify(headers))) return denied;
    return { status: 400, error: "Invite code is required.", code: "invite_code_required" };
  }
  return requestUserAccount(headers, target, {
    endpoint: "api/users/redeem",
    stage: "invite_redemption",
    extraPayload: { invite_code: trimmedCode },
    onClientError: async (response) => {
      const status = response.status as 400 | 403;
      try {
        const body = await response.json() as { error?: unknown; code?: unknown };
        return {
          status,
          error: typeof body.error === "string" && body.error ? body.error : "Invalid invite code.",
          ...(typeof body.code === "string" && body.code ? { code: body.code } : {}),
        };
      } catch {
        return { status, error: "Invalid invite code." };
      }
    },
  });
}

export function sameOrigin(request: Pick<Request, "headers">) {
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
