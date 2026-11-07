import { createRemoteJWKSet, jwtVerify } from "jose";

let discovery: { issuer: string; expires: number; keys: ReturnType<typeof createRemoteJWKSet> } | undefined;

function httpsURL(value: string): URL {
  const url = new URL(value);
  if (url.protocol !== "https:" || url.username || url.password || url.search || url.hash) throw new Error("Invalid OIDC URL");
  return url;
}

async function keysFor(issuer: string) {
  if (discovery?.issuer === issuer && discovery.expires > Date.now()) return discovery.keys;
  const base = httpsURL(issuer);
  const response = await fetch(`${base.toString().replace(/\/$/, "")}/.well-known/openid-configuration`, {
    redirect: "error", signal: AbortSignal.timeout(5_000), cache: "no-store",
  });
  if (!response.ok) throw new Error("OIDC discovery unavailable");
  const metadata = await response.json();
  if (metadata.issuer !== issuer || typeof metadata.jwks_uri !== "string") throw new Error("OIDC issuer mismatch");
  const keys = createRemoteJWKSet(httpsURL(metadata.jwks_uri), { timeoutDuration: 5_000 });
  discovery = { issuer, keys, expires: Date.now() + 5 * 60_000 };
  return keys;
}

function assertion(headers: Headers): string | undefined {
  const authorization = headers.get("authorization");
  if (authorization?.startsWith("Bearer ")) return authorization.slice(7);
  // An OIDC login gateway may put the ID token in a secure, HttpOnly cookie.
  // The app verifies the token itself; it never trusts an email header.
  const name = process.env.NORI_OIDC_COOKIE;
  if (!name) return;
  const entry = headers.get("cookie")?.split(";").map(v => v.trim()).find(v => v.startsWith(`${name}=`));
  return entry ? decodeURIComponent(entry.slice(name.length + 1)) : undefined;
}

export async function oidcIdentity(headers: Headers): Promise<{ email: string } | null> {
  try {
    const issuer = process.env.NORI_OIDC_ISSUER;
    const audience = process.env.NORI_OIDC_AUDIENCE;
    const token = assertion(headers);
    if (!issuer || !audience || !token || token.length > 16_384) return null;
    const { payload } = await jwtVerify(token, await keysFor(issuer), {
      issuer, audience, requiredClaims: ["sub", "iat", "exp"], clockTolerance: 30,
      algorithms: ["RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512", "EdDSA"],
    });
    if (typeof payload.sub !== "string" || !payload.sub || typeof payload.email !== "string" ||
        payload.email_verified !== true || typeof payload.iat !== "number" || typeof payload.exp !== "number" ||
        payload.iat > Date.now() / 1000 + 30 || payload.exp <= payload.iat ||
        (payload.token_use !== undefined && payload.token_use !== "id") ||
        (Array.isArray(payload.aud) && payload.aud.length > 1 && payload.azp !== audience) ||
        (payload.azp !== undefined && payload.azp !== audience)) return null;
    return { email: payload.email.trim().toLowerCase() };
  } catch { return null; }
}
