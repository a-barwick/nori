import { forwardRoute } from "@/lib/runtime/forward";

async function handle(request: Request, context: { params: Promise<{ path: string[] }> }) {
  const { path } = await context.params;
  return forwardRoute(request, path[0] === "v1" ? path : ["api", ...path], "local");
}

export { handle as GET, handle as HEAD, handle as POST, handle as PUT, handle as PATCH, handle as DELETE };
