import { GoogleAuth } from "google-auth-library";
import { SignJWT } from "jose";
import type { Mode } from "./target";

const google = new GoogleAuth({ scopes: ["https://www.googleapis.com/auth/cloud-platform"] });

export async function runtimeHeaders(mode: Mode): Promise<Record<string, string>> {
  return mode === "hosted" ? { Authorization: await runtimeAuthorization() } : {};
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
