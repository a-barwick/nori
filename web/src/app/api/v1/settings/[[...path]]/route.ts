import { assistantRoute } from "@/lib/runtime/forward";

async function handle(request: Request, context: { params: Promise<{ path?: string[] }> }) {
  const path = (await context.params).path ?? [];
  if (path[0] !== "model-keys") {
    return Response.json({ error: "Not found." }, { status: 404 });
  }
  return assistantRoute(request, "settings", path);
}

export { handle as GET, handle as PUT, handle as DELETE };
