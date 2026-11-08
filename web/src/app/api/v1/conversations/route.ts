import { conversationRoute } from "@/lib/runtime/forward";

function handle(request: Request) {
  return conversationRoute(request, []);
}

export { handle as GET, handle as POST };
