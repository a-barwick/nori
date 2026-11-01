import { GoogleAuth } from "google-auth-library";

const google = new GoogleAuth({ scopes: ["https://www.googleapis.com/auth/cloud-platform"] });

/**
 * The `Authorization` header value for one call from the web server to the runtime.
 *
 * The hosted runtime is a GCP Agent Runtime, which accepts a Google access token for this service's own service
 * account. A runtime somewhere else changes only this function. It must not use the browser's or sign-in provider's
 * credentials, and throws when it cannot produce its own.
 */
export async function runtimeAuthorization(): Promise<string> {
  const token = await google.getAccessToken();
  if (!token) throw new Error("No runtime credentials");
  return `Bearer ${token}`;
}
