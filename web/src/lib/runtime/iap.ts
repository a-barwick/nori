import { createRemoteJWKSet, jwtVerify } from "jose";

const IAP_ISSUER = "https://cloud.google.com/iap";
const IAP_JWKS_URI = new URL("https://www.gstatic.com/iap/verify/public_key-jwk");

let cachedKeys: ReturnType<typeof createRemoteJWKSet> | undefined;

function iapKeys() {
  cachedKeys ??= createRemoteJWKSet(IAP_JWKS_URI, { timeoutDuration: 5_000 });
  return cachedKeys;
}

export async function iapIdentity(headers: Headers): Promise<{ email: string } | null> {
  try {
    const audience = process.env.IAP_AUDIENCE?.trim();
    const token = headers.get("x-goog-iap-jwt-assertion")?.trim();
    if (!audience || !token || token.length > 16_384) return null;
    const { payload } = await jwtVerify(token, iapKeys(), {
      issuer: IAP_ISSUER,
      audience,
      requiredClaims: ["sub", "email", "iat", "exp"],
      clockTolerance: 30,
      algorithms: ["ES256"],
    });
    if (
      typeof payload.sub !== "string" ||
      !payload.sub ||
      typeof payload.email !== "string" ||
      !payload.email.trim() ||
      typeof payload.iat !== "number" ||
      typeof payload.exp !== "number" ||
      payload.iat > Date.now() / 1000 + 30 ||
      payload.exp <= payload.iat
    ) {
      return null;
    }
    return { email: payload.email.trim().toLowerCase() };
  } catch {
    return null;
  }
}
