import { runtimeHeaders } from "./credentials";
import { oidcIdentity } from "./oidc";
import { localDevelopment, runtimeMode, runtimeTarget, type RuntimeTarget } from "./target";
import { OAuth2Client } from "google-auth-library";

const LOCAL_EMAIL = (process.env.NORI_LOCAL_EMAIL?.trim() || process.env.OWNER_EMAIL?.trim() || "").toLowerCase();
type Identity = { email: string };

const lastRejection = new Map<string, number>();
function rejected(stage: string, status?: number): null {
  if (Date.now() - (lastRejection.get(stage) ?? 0) >= 60_000) {
    lastRejection.set(stage, Date.now());
    // Never log assertions, provider error messages, or user identities.
    console.warn(JSON.stringify({ severity: "WARNING", message: "nori.auth.rejected", stage, ...(status ? { status } : {}) }));
  }
  return null;
}

async function identify(headers: Headers): Promise<Identity | null> {
  if (localDevelopment(headers)) return LOCAL_EMAIL ? { email: LOCAL_EMAIL } : null;
  return providerIdentity(headers);
}

export type ScopedRuntime = RuntimeTarget & { userID: string };

export async function resolveUser(headers: Headers, target = runtimeTarget(runtimeMode(headers))): Promise<ScopedRuntime | null> {
  if (target instanceof Response || (target.mode === "local" && !localDevelopment(headers))) return null;
  const identity = await identify(headers);
  if (!identity) return null;
  try {
    const response = await fetch(new URL("api/users/resolve", target.base), {
      method: "POST", headers: { "Content-Type": "application/json", ...await runtimeHeaders(target.mode) },
      body: JSON.stringify({ email: identity.email }), cache: "no-store", redirect: "error",
      signal: AbortSignal.timeout(10_000),
    });
    if (!response.ok) return rejected("user_resolution", response.status);
    const value = await response.json();
    if (typeof value.id !== "string" || !(value.id === "owner" || /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(value.id))) return null;
    return { ...target, userID: value.id };
  } catch { return rejected("user_resolution_connection"); }
}

export async function isUser(headers: Headers): Promise<boolean> { return !!(await resolveUser(headers)); }

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

/** A browser's EventSource and same-origin GET send no Origin header, so a read checks Sec-Fetch-Site. */
export function sameOriginRead(request: Request): boolean {
  return request.headers.get("sec-fetch-site") === "same-origin";
}

const verifier = new OAuth2Client();
let keyCache: { expires: number; keys: Record<string, string> } | undefined;

async function providerIdentity(headers: Headers): Promise<Identity | null> {
  const cloud = process.env.NORI_CLOUD || "gcp";
  if (!["gcp", "aws", "local"].includes(cloud)) return null;
  const provider = process.env.NORI_AUTH_PROVIDER || (cloud === "gcp" ? "iap" : "oidc");
  if (provider === "oidc") return oidcIdentity(headers);
  if (provider !== "iap") return null;
  const assertion = headers.get("x-goog-iap-jwt-assertion");
  const audience = process.env.IAP_AUDIENCE;
  if (!assertion || !audience) return rejected("iap_configuration");
  let stage = "iap_header";
  try {
    const jwtHeader = JSON.parse(Buffer.from(assertion.split(".")[0], "base64url").toString());
    if (jwtHeader.alg !== "ES256" || typeof jwtHeader.kid !== "string") return null;
    if (!keyCache || keyCache.expires < Date.now() || !keyCache.keys[jwtHeader.kid]) {
      stage = "iap_keys";
      const { pubkeys } = await verifier.getIapPublicKeys();
      keyCache = { keys: pubkeys, expires: Date.now() + 5 * 60_000 };
    }
    stage = "iap_verification";
    const ticket = await verifier.verifySignedJwtWithCertsAsync(
      assertion, keyCache.keys, audience, ["https://cloud.google.com/iap"], 660,
    );
    const claims = ticket.getPayload();
    const now = Date.now() / 1000;
    if (!claims || typeof claims.email !== "string" || typeof claims.sub !== "string" ||
      typeof claims.iat !== "number" || typeof claims.exp !== "number") return rejected("iap_claims");
    if (claims.iat > now + 30) return rejected("iap_claims_early");
    if (claims.exp < now - 30) return rejected("iap_claims_expired");
    if (claims.exp - claims.iat > 660) return rejected("iap_claims_lifetime");
    return { email: claims.email };
  } catch (error) {
    if (stage === "iap_verification" && error instanceof Error) {
      for (const [prefix, reason] of [["Expiration time too far", "lifetime"], ["Token used too early", "early"], ["Token used too late", "expired"], ["Wrong recipient", "audience"], ["Invalid issuer", "issuer"], ["Invalid token signature", "signature"]]) {
        if (error.message.startsWith(prefix)) { stage = `iap_${reason}`; break; }
      }
    }
    return rejected(stage);
  }
}
