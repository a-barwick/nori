import { macAccessPolicy } from "@/lib/runtime/macAccess";

export async function GET() {
  const source = process.env.NORI_OIDC_IAP_POLICY_RESOURCE;
  const ready = source === undefined || (await macAccessPolicy(source)) !== null;
  return Response.json({ status: ready ? "ok" : "unavailable" }, {
    status: ready ? 200 : 503,
    headers: { "Cache-Control": "no-store" },
  });
}
