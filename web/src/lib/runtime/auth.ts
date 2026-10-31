import { OAuth2Client } from "google-auth-library";

const OWNER = "austinbarwickdavis@gmail.com";
const verifier = new OAuth2Client();
const LOOPBACK = new Set(["localhost", "127.0.0.1", "[::1]"]);
let keyCache: { expires: number; keys: Record<string, string> } | undefined;

export function localDevelopment(headers: Headers) {
  if (process.env.NODE_ENV !== "development") return false;
  try {
    return LOOPBACK.has(new URL(`http://${headers.get("host")}`).hostname);
  } catch {
    return false;
  }
}

/** Verify Google's signed assertion; unsigned identity headers are never trusted. */
export async function isOwner(headers: Headers): Promise<boolean> {
  if (localDevelopment(headers)) return true;
  const assertion = headers.get("x-goog-iap-jwt-assertion");
  const audience = process.env.IAP_AUDIENCE;
  if (!assertion || !audience) return false;
  try {
    const jwtHeader = JSON.parse(Buffer.from(assertion.split(".")[0], "base64url").toString());
    if (jwtHeader.alg !== "ES256" || typeof jwtHeader.kid !== "string") return false;
    if (!keyCache || keyCache.expires < Date.now() || !keyCache.keys[jwtHeader.kid]) {
      const { pubkeys } = await verifier.getIapPublicKeys();
      keyCache = { keys: pubkeys, expires: Date.now() + 5 * 60_000 };
    }
    const ticket = await verifier.verifySignedJwtWithCertsAsync(
      assertion, keyCache.keys, audience, ["https://cloud.google.com/iap"], 660,
    );
    const claims = ticket.getPayload();
    const now = Date.now() / 1000;
    return !!claims && claims.email === OWNER && typeof claims.sub === "string" &&
      typeof claims.iat === "number" && typeof claims.exp === "number" &&
      claims.iat <= now + 30 && claims.exp >= now - 30 && claims.exp - claims.iat <= 660;
  } catch {
    return false;
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
