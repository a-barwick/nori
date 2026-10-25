import { runtimeRequest } from "@/lib/poc/runtime";

export async function POST(request: Request, context: { params: Promise<{ operation: string }> }) {
  const { operation } = await context.params;
  return runtimeRequest(request, operation);
}
