import { runtimeAuthorization } from "./credentials";
import { oidcIdentity } from "./oidc";
import { OAuth2Client } from "google-auth-library";

// Provider verification returns an email; the trusted API maps it to a provisioned user.
// Local development selects an email explicitly, without weakening hosted sign-in.
const LOCAL_EMAIL = (process.env.NORI_LOCAL_EMAIL?.trim() || process.env.OWNER_EMAIL?.trim() || "").toLowerCase();
const LOOPBACK = new Set(["localhost", "127.0.0.1", "[::1]"]);

export type Identity = { email: string };

export function localDevelopment(headers: Headers) {
  if (process.env.NODE_ENV !== "development") return false;
  try {
    return LOOPBACK.has(new URL(`http://${headers.get("host")}`).hostname);
  } catch {
    return false;
  }
}

/** The verified caller. Local development on loopback uses the configured email; otherwise the provider decides. */
export async function identify(headers: Headers): Promise<Identity | null> {
  if (localDevelopment(headers)) return LOCAL_EMAIL ? { email: LOCAL_EMAIL } : null;
  return providerIdentity(headers);
}

export type UserIdentity = Identity & { id: string };

/** This server supplies both the verified email and its own runtime credentials. */
export async function resolveUser(headers: Headers, local = localDevelopment(headers) && !!process.env.NORI_LOCAL_AGENT_URL): Promise<UserIdentity | null> {
  const identity = await identify(headers);
  if (!identity) return null;
  const configured = local ? process.env.NORI_LOCAL_AGENT_URL : process.env.NORI_RUNTIME_URL;
  if (!configured) return null;
  try {
    const base = new URL(configured.endsWith("/") ? configured : `${configured}/`);
    if (base.username || base.password || base.search || base.hash) return null;
    if (local ? base.protocol !== "http:" || !LOOPBACK.has(base.hostname) || base.pathname !== "/" : base.protocol !== "https:") return null;
    const authorization: Record<string, string> = local ? {} : { Authorization: await runtimeAuthorization() };
    const response = await fetch(new URL("api/users/resolve", base), {
      method: "POST", headers: { "Content-Type": "application/json", ...authorization },
      body: JSON.stringify({ email: identity.email }), cache: "no-store", redirect: "error",
      signal: AbortSignal.timeout(10_000),
    });
    if (!response.ok) return null;
    const value = await response.json();
    if (typeof value.id !== "string" || !(value.id === "owner" || /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(value.id))) return null;
    return { ...identity, id: value.id };
  } catch { return null; }
}

export async function isUser(headers: Headers): Promise<boolean> { return !!(await resolveUser(headers)); }

// The ID is resolved here, never copied from a browser header, URL or body.
export function userPath(id: string, path: string) { return `users/${encodeURIComponent(id)}/${path}`; }

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

// Google IAP remains the GCP default; OIDC is selected for other clouds.

const verifier = new OAuth2Client();
let keyCache: { expires: number; keys: Record<string, string> } | undefined;

/** Verify Google's signed assertion; unsigned identity headers are never trusted. */
async function providerIdentity(headers: Headers): Promise<Identity | null> {
 const cloud = process.env.NORI_CLOUD || "gcp";
 if (!["gcp", "aws", "local"].includes(cloud)) return null;
 const provider = process.env.NORI_AUTH_PROVIDER || (cloud === "gcp" ? "iap" : "oidc");
 if (provider === "oidc") return oidcIdentity(headers);
 if (provider !== "iap") return null;
  const assertion = headers.get("x-goog-iap-jwt-assertion");
  const audience = process.env.IAP_AUDIENCE;
  if (!assertion || !audience) return null;
  try {
    const jwtHeader = JSON.parse(Buffer.from(assertion.split(".")[0], "base64url").toString());
    if (jwtHeader.alg !== "ES256" || typeof jwtHeader.kid !== "string") return null;
    if (!keyCache || keyCache.expires < Date.now() || !keyCache.keys[jwtHeader.kid]) {
      const { pubkeys } = await verifier.getIapPublicKeys();
      keyCache = { keys: pubkeys, expires: Date.now() + 5 * 60_000 };
    }
    const ticket = await verifier.verifySignedJwtWithCertsAsync(
      assertion, keyCache.keys, audience, ["https://cloud.google.com/iap"], 660,
    );
    const claims = ticket.getPayload();
    const now = Date.now() / 1000;
    const valid = !!claims && typeof claims.email === "string" && typeof claims.sub === "string" &&
      typeof claims.iat === "number" && typeof claims.exp === "number" &&
      claims.iat <= now + 30 && claims.exp >= now - 30 && claims.exp - claims.iat <= 660;
    return valid ? { email: claims.email as string } : null;
  } catch {
    return null;
  }
}
