import { SignJWT } from "jose";
import type { Mode } from "./target";

export async function runtimeHeaders(mode: Mode): Promise<Record<string, string>> {
  return mode === "hosted" ? { Authorization: await runtimeAuthorization() } : {};
}

async function runtimeAuthorization(): Promise<string> {
  const cloud = process.env.NORI_CLOUD || "aws";
  if (cloud === "aws" || cloud === "gcp") {
    const key = new TextEncoder().encode(process.env.NORI_SERVICE_TOKEN_KEY || "");
    const audience = process.env.NORI_SERVICE_AUDIENCE || "nori";
    if (key.length < 32 || !audience.trim()) throw new Error("Service authentication is not configured");
    const token = await new SignJWT({}).setProtectedHeader({ alg: "HS256", typ: "JWT" })
      .setIssuer("nori-service").setAudience(audience).setIssuedAt().setExpirationTime("60s").sign(key);
    return `Bearer ${token}`;
  }
  throw new Error("Local deployments require the loopback development proxy");
}
