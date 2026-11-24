import { assistantRoute } from "@/lib/runtime/forward";

async function handle(request: Request, context: { params: Promise<{ path?: string[] }> }) {
  return assistantRoute(request, "operations", (await context.params).path ?? []);
}

export { handle as GET, handle as POST, handle as PUT, handle as DELETE };
