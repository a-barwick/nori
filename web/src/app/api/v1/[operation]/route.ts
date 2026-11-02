import { readRoute } from "@/lib/runtime/forward";

export async function GET(request: Request, context: { params: Promise<{ operation: string }> }) {
  const { operation } = await context.params;
  return readRoute(request, "hosted", operation);
}
