import { assistantRoute } from "@/lib/runtime/forward";

async function handle(request: Request, context: { params: Promise<{ path?: string[] }> }) {
  const path = (await context.params).path ?? [];
  const isAccountDelete = request.method === "POST" && path.length === 2 && path[0] === "account" && path[1] === "delete";
  if (request.method === "GET" && path.length === 1 && path[0] === "api-costs") {
    const response = await assistantRoute(request, "settings", path);
    const headers = new Headers(response.headers);
    headers.set("Cache-Control", "no-store");
    return new Response(response.body, { status: response.status, statusText: response.statusText, headers });
  }
  if (path[0] !== "model-keys" && path[0] !== "home-connectors" && !(path[0] === "models" && path.length === 1) &&
      !(request.method === "POST" && path.length === 1 && path[0] === "reset") && !isAccountDelete) {
    return Response.json({ error: "Not found." }, { status: 404 });
  }
  return assistantRoute(request, "settings", path);
}

export { handle as GET, handle as POST, handle as PUT, handle as DELETE };
