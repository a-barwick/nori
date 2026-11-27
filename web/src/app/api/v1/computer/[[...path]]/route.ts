import { assistantRoute } from "@/lib/runtime/forward";

async function handle(request: Request, context: { params: Promise<{ path?: string[] }> }) {
  const path = (await context.params).path ?? [];
  return assistantRoute(request, "computer", path);
}

export { handle as GET, handle as POST, handle as PATCH, handle as DELETE };
