import { runtimeRequest } from "@/lib/runtime/server";

export async function POST(request: Request, context: { params: Promise<{ operation: string }> }) {
  const { operation } = await context.params;
  return runtimeRequest(request, operation);
}
