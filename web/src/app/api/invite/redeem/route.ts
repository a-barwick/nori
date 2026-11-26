import { accessFailureResponse, redeemInvite, sameOrigin } from "@/lib/runtime/auth";

export async function POST(request: Request) {
  if (!sameOrigin(request)) {
    return Response.json({ error: "Forbidden." }, { status: 403, headers: { "Cache-Control": "no-store" } });
  }
  let inviteCode = "";
  try {
    const body = await request.json() as { invite_code?: unknown; code?: unknown };
    if (typeof body.invite_code === "string") inviteCode = body.invite_code;
    else if (typeof body.code === "string") inviteCode = body.code;
  } catch {
    return Response.json({ error: "Send a valid JSON object." }, { status: 400, headers: { "Cache-Control": "no-store" } });
  }
  const result = await redeemInvite(request.headers, inviteCode);
  if ("status" in result) {
    return accessFailureResponse(result);
  }
  return Response.json({ id: result.userID }, { status: 200, headers: { "Cache-Control": "no-store" } });
}
