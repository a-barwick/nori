import { conversationRoute } from "@/lib/runtime/forward";

async function handle(request: Request, context: { params: Promise<{ path: string[] }> }) {
  return conversationRoute(request, (await context.params).path);
}

export { handle as GET, handle as POST, handle as DELETE };
