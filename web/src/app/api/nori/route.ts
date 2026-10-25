export function POST() {
  return Response.json({ error: "This demo endpoint has been retired. Use the Nori runtime." }, { status: 410 });
}
