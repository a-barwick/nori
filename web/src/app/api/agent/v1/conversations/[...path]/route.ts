import { conversationRoute } from "@/lib/runtime/forward";

export async function GET(request: Request, context: { params: Promise<{ path: string[] }> }) {
  return conversationRoute(request, "local", (await context.params).path);
}

export async function POST(request: Request, context: { params: Promise<{ path: string[] }> }) {
  return conversationRoute(request, "local", (await context.params).path);
}
