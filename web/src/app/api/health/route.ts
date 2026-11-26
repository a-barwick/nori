import { macAccessPolicy, macAllowedIdentities } from "@/lib/runtime/macAccess";

export async function GET() {
  const source = process.env.NORI_OIDC_IAP_POLICY_RESOURCE;
  const rawIdentities = process.env.NORI_OIDC_IAP_ALLOWED_IDENTITIES;
  const macConfigured = source !== undefined || rawIdentities !== undefined;
  let ready = true;
  if (macConfigured) {
    const identities = macAllowedIdentities(rawIdentities);
    const policy = source && identities ? await macAccessPolicy(source) : null;
    ready = !!policy && !!identities && [...identities.values()].some((email) => policy.has(email));
  }
  return Response.json({ status: ready ? "ok" : "unavailable" }, {
    status: ready ? 200 : 503,
    headers: { "Cache-Control": "no-store" },
  });
}
