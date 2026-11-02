import { runtimeRead } from "@/lib/runtime/server";

export async function GET(request: Request, context: { params: Promise<{ operation: string }> }) {
  const { operation } = await context.params;
  return runtimeRead(request, operation);
}
