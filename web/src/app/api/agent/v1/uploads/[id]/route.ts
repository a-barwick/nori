import { fileRoute } from "@/lib/runtime/forward";

export async function GET(request: Request, context: { params: Promise<{ id: string }> }) {
  const { id } = await context.params;
  return fileRoute(request, "local", `v1/uploads/${encodeURIComponent(id)}`);
}
