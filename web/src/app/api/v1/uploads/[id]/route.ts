import { fileRoute, validSegment } from "@/lib/runtime/forward";

export async function GET(request: Request, context: { params: Promise<{ id: string }> }) {
  const { id } = await context.params;
  if (!validSegment(id)) return Response.json({ error: "Not found." }, { status: 404 });
  return fileRoute(request, `v1/uploads/${encodeURIComponent(id)}`);
}
