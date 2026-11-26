import assert from "node:assert/strict";
import { createServer } from "node:http";
import { test } from "node:test";
import { once } from "node:events";
import { createNoriHandler } from "./adapter.mjs";

const user = "d9a08398-afac-49d8-a76e-5139d308a6fd";
const inputID = "c922e358-0dcc-4220-938e-407f726a904c";

test("host adapter preserves scoped reads, queued mutations, event streams and downloads", async (t) => {
  const paths = [];
  const runtime = createServer(async (req, res) => {
    paths.push(req.url);
    const json = (status, body) => {
      res.writeHead(status, { "Content-Type": "application/json" });
      res.end(JSON.stringify(body));
    };
    assert.equal(req.headers.authorization, "Bearer fixture-service");
    const path = req.url.slice(`/users/${user}`.length);
    switch (path) {
      case "/v1/conversations/assistant":
        return json(200, { id: "assistant" });
      case "/v1/conversations/assistant/messages": {
        const chunks = [];
        for await (const chunk of req) chunks.push(chunk);
        assert.equal(JSON.parse(Buffer.concat(chunks).toString()).id, inputID);
        return json(503, { accepted: true });
      }
      case "/v1/conversations/assistant/events?after=42":
        res.writeHead(200, { "Content-Type": "text/event-stream" });
        return res.end("id: 43\nevent: message\ndata: {}\n\n");
      case `/v1/results/${inputID}/file`:
        res.writeHead(200, { "Content-Type": "application/octet-stream", "Content-Disposition": 'attachment; filename="result.txt"' });
        return res.end("fixture");
      default:
        return json(404, {});
    }
  });
  runtime.listen(0, "127.0.0.1");
  await once(runtime, "listening");
  t.after(() => { runtime.closeAllConnections(); runtime.close(); });
  const handler = createNoriHandler({
    runtimeBase: `http://127.0.0.1:${runtime.address().port}`,
    verifyIdentity: (request) => request.headers.has("x-fixture-user") ? { issuer: "fixture", subject: "one" } : null,
    bindings: [{ issuer: "fixture", subject: "one", noriUserId: user }],
    serviceAuthorization: async () => "Bearer fixture-service",
  });
  const request = (path, options = {}) => new Request("https://embed.example/api/nori/" + path, {
    ...options, headers: { "x-fixture-user": "one", ...options.headers },
  });
  const denied = await handler(new Request("https://embed.example/api/nori/conversation"));
  assert.equal(denied.status, 401);
  const snapshot = await handler(request("conversation"));
  assert.equal(snapshot.status, 200);
  assert.deepEqual(await snapshot.json(), { id: "assistant" });
  const queued = await handler(request("messages", {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ id: inputID, text: "fixture message" }),
  }));
  assert.equal(queued.status, 202);
  assert.deepEqual(await queued.json(), { accepted: true, turnId: inputID, retryable: true, state: "queued" });
  const stream = await handler(request("events", { headers: { "Last-Event-ID": "42" } }));
  assert.equal(stream.headers.get("content-type"), "text/event-stream");
  assert.match(await stream.text(), /^id: 43/m);
  const file = await handler(request(`results/${inputID}/file`));
  assert.equal(file.headers.get("content-disposition"), 'attachment; filename="result.txt"');
  assert.equal(await file.text(), "fixture");
  assert.equal(paths.length, 4);
  assert.ok(paths.every((path) => path.startsWith(`/users/${user}/`)));
});
