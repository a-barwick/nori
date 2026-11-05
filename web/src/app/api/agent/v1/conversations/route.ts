import { conversationRoute } from "@/lib/runtime/forward";

export async function GET(request: Request) {
  return conversationRoute(request, "local", []);
}

export async function POST(request: Request) {
  return conversationRoute(request, "local", []);
}
