import { fixtureCallback, fixtureGET, fixturePreview, fixtureWrite, resetFixtures } from "@/lib/assistant/fixtures.server";
import { localDevelopment } from "@/lib/runtime/target";

// Development-only: serves contracts/assistant/fixtures through the assistant API paths.
type Context = { params: Promise<{ path: string[] }> };
const unavailable = (request: Request) => process.env.NODE_ENV !== "development" || !localDevelopment(request.headers);

export async function GET(request: Request, context: Context) {
  if (unavailable(request)) return Response.json({ error: "Not found." }, { status: 404 });
  const { path } = await context.params;
  const url = new URL(request.url);
  if (path.join("/") === "connections/oauth/callback") return fixtureCallback(url);
  return fixtureGET(path, url);
}

async function write(request: Request, context: Context) {
  if (unavailable(request)) return Response.json({ error: "Not found." }, { status: 404 });
  const { path } = await context.params;
  const body = await request.json().catch(() => ({}));
  if (path.join("/") === "reset") { resetFixtures(); return Response.json({ ok: true }); }
  if (path.join("/") === "automations/preview") return fixturePreview(body);
  return fixtureWrite(request.method, path, body);
}

export { write as POST, write as PUT };
