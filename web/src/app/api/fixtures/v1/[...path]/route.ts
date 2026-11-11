import { localDevelopment } from "@/lib/runtime/target";

// Development-only: serves contracts/assistant/fixtures through the assistant API paths. The build inlines NODE_ENV,
// so in production the import below is dead code and the fixture store is not in the server bundle at all.
type Context = { params: Promise<{ path: string[] }> };
const load = process.env.NODE_ENV === "development" ? () => import("@/lib/assistant/fixtures.server") : undefined;
const unavailable = (request: Request) => !load || !localDevelopment(request.headers);

export async function GET(request: Request, context: Context) {
  if (unavailable(request) || !load) return Response.json({ error: "Not found." }, { status: 404 });
  const { fixtureCallback, fixtureGET } = await load();
  const { path } = await context.params;
  const url = new URL(request.url);
  if (path.join("/") === "connections/oauth/callback") return fixtureCallback(url);
  return fixtureGET(path, url);
}

async function write(request: Request, context: Context) {
  if (unavailable(request) || !load) return Response.json({ error: "Not found." }, { status: 404 });
  const { fixturePreview, fixtureWrite, resetFixtures } = await load();
  const { path } = await context.params;
  const body = await request.json().catch(() => ({}));
  if (path.join("/") === "reset") { resetFixtures(); return Response.json({ ok: true }); }
  if (path.join("/") === "automations/preview") return fixturePreview(body);
  return fixtureWrite(request.method, path, body);
}

export { write as POST, write as PUT };
