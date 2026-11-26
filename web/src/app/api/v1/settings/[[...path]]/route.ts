import { assistantRoute } from "@/lib/runtime/forward";

async function handle(request: Request, context: { params: Promise<{ path?: string[] }> }) {
  const path = (await context.params).path ?? [];
  const isAccountDelete = request.method === "POST" && path.length === 2 && path[0] === "account" && path[1] === "delete";
  if (path[0] !== "model-keys" && !(path[0] === "models" && path.length === 1) &&
      !(request.method === "POST" && path.length === 1 && path[0] === "reset") && !isAccountDelete) {
    return Response.json({ error: "Not found." }, { status: 404 });
  }
  return assistantRoute(request, "settings", path);
}

export { handle as GET, handle as POST, handle as PUT, handle as DELETE };
