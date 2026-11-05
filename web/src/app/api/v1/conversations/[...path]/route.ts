import { conversationRoute, conversationMode } from "@/lib/runtime/forward";

export async function GET(request: Request, context: { params: Promise<{ path: string[] }> }) {
  return conversationRoute(request, conversationMode(request), (await context.params).path);
}

export async function POST(request: Request, context: { params: Promise<{ path: string[] }> }) {
  return conversationRoute(request, conversationMode(request), (await context.params).path);
}
