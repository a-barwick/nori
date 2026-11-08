import { operationRoute } from "@/lib/runtime/forward";

export async function POST(request: Request, context: { params: Promise<{ operation: string }> }) {
  const { operation } = await context.params;
  return operationRoute(request, "local", operation);
}
