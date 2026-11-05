import { conversationRoute, conversationMode } from "@/lib/runtime/forward";

export async function GET(request: Request) {
  return conversationRoute(request, conversationMode(request), []);
}

export async function POST(request: Request) {
  return conversationRoute(request, conversationMode(request), []);
}
