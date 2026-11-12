import { GoogleAuth } from "google-auth-library";
import { SignJWT } from "jose";
import type { Mode } from "./target";

const google = new GoogleAuth({ scopes: ["https://www.googleapis.com/auth/cloud-platform"] });

export async function runtimeHeaders(mode: Mode, userID = ""): Promise<Record<string, string>> {
  const headers: Record<string, string> = mode === "hosted" ? { Authorization: await runtimeAuthorization() } : {};
  const key = process.env.NORI_WEB_CALLER_KEY;
  if (key) {
    const signingKey = new TextEncoder().encode(key);
    if (signingKey.length < 32) throw new Error("The web caller signing key is too short");
    headers["X-Nori-Service-Assertion"] = await new SignJWT({}).setProtectedHeader({ alg: "HS256" })
      .setIssuer("nori-browser").setSubject(userID).setAudience("nori-core")
      .setIssuedAt().setExpirationTime("60s").sign(signingKey);
  }
  return headers;
}

async function runtimeAuthorization(): Promise<string> {
  const cloud = process.env.NORI_CLOUD || "gcp";
  if (cloud === "aws") {
    const key = new TextEncoder().encode(process.env.NORI_SERVICE_TOKEN_KEY || "");
    const audience = process.env.NORI_SERVICE_AUDIENCE || "nori";
    if (key.length < 32 || !audience.trim()) throw new Error("Service authentication is not configured");
    const token = await new SignJWT({}).setProtectedHeader({ alg: "HS256", typ: "JWT" })
      .setIssuer("nori-service").setAudience(audience).setIssuedAt().setExpirationTime("60s").sign(key);
    return `Bearer ${token}`;
  }
  if (cloud !== "gcp") throw new Error("Local deployments require the loopback development proxy");
  const token = await google.getAccessToken();
  if (!token) throw new Error("No runtime credentials");
  return `Bearer ${token}`;
}
