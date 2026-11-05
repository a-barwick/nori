import { OAuth2Client } from "google-auth-library";

/*
 * Who is calling, and where from. Everything else in the web app uses these three functions:
 *
 *   identify(headers)   the verified caller, or null
 *   isOwner(headers)    identify(headers) is the owner
 *   sameOrigin(request) the browser request came from this app's own origin
 *
 * Nori has one owner, named by OWNER_EMAIL. Without it nobody outside local development is the owner. Sign-in is Google IAP today. To use another provider, replace
 * `providerIdentity` below: it must return an email only after checking the provider's signature, audience, issuer and
 * expiry, and must never trust an unsigned header.
 */
const OWNER = (process.env.OWNER_EMAIL ?? "").trim().toLowerCase();
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

/** The verified caller. Local development on loopback is the owner; otherwise the provider decides. */
export async function identify(headers: Headers): Promise<Identity | null> {
  if (localDevelopment(headers)) return { email: OWNER || "local-owner" };
  return providerIdentity(headers);
}

export async function isOwner(headers: Headers): Promise<boolean> {
  if (localDevelopment(headers)) return true;
  const email = (await identify(headers))?.email?.toLowerCase();
  return !!OWNER && email === OWNER;
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

/** A browser's EventSource and same-origin GET send no Origin header, so a read checks Sec-Fetch-Site. */
export function sameOriginRead(request: Request): boolean {
  return request.headers.get("sec-fetch-site") === "same-origin";
}

// ---- Google IAP: the only provider-specific code -------------------------------------------------------------------

const verifier = new OAuth2Client();
let keyCache: { expires: number; keys: Record<string, string> } | undefined;

/** Verify Google's signed assertion; unsigned identity headers are never trusted. */
async function providerIdentity(headers: Headers): Promise<Identity | null> {
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
