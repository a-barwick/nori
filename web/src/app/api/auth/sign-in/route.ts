import { sameOrigin } from "@/lib/runtime/auth";
import { signedOutCookie } from "@/lib/runtime/session";

export async function POST(request: Request) {
  if (!sameOrigin(request)) return Response.json({ error: "Forbidden." }, { status: 403 });
  return new Response(null, {
    status: 303,
    headers: {
      Location: "/",
      "Cache-Control": "no-store",
      "Set-Cookie": signedOutCookie("", 0),
    },
  });
}
