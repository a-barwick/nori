import { createServer } from "node:http";
import { spawn } from "node:child_process";
import { cp, mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { once } from "node:events";

const identity = "462cef1f-74a0-4706-94c7-3e624ff5cd43";
const alternate = "5b95e6ef-2c77-46da-9b6a-61e01ed990fd";
const resultID = "d3d34c1a-0ad7-4222-bff4-8cee91d892c5";
await cp(".next/static", ".next/standalone/.next/static", { recursive: true });
const fixture = await mkdtemp(join(tmpdir(), "nori-browser-"));
const sessions = new Map();
const streams = new Map();
const pendingApproval = JSON.parse(await readFile("../contracts/assistant/fixtures/permissions/interaction.approval.valid.json", "utf8"));
pendingApproval.expires_at = new Date(Date.now() + 60 * 60 * 1000).toISOString();
let approval = pendingApproval;
let allowModels = true;
const routinePreview = JSON.parse(await readFile("../contracts/assistant/fixtures/automations/automation-preview.dst.valid.json", "utf8"));
const routineFixture = JSON.parse(await readFile("../contracts/assistant/fixtures/automations/automation.recurring.valid.json", "utf8"));
let routine = routineFixture;
const profile = { id: "browser-chat", version: "v1", label: "Browser chat", backend: "gateway", model: "fixture", lanes: ["chat"], supported_reasoning: ["high"], paid: false };

function snapshot(id) {
  if (!sessions.has(id)) {
    sessions.set(id, { messages: [], results: [], runs: [], paused: false, position: 0, conversation: {
      pending: false, processing: false, input_ids: [], restart_count: 0, frozen: false, revision: 1, consumed: 0,
    }});
  }
  return sessions.get(id);
}

const upstream = createServer(async (req, res) => {
  const url = new URL(req.url, "http://127.0.0.1");
  const json = (value, status = 200) => {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(value));
  };
  const input = async () => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    return JSON.parse(Buffer.concat(chunks).toString());
  };
  if (!req.headers.authorization?.startsWith("Bearer ")) return json({ error: "Unauthorized" }, 401);
  if (url.pathname === "/api/users/resolve") {
    const { email } = await input();
    return json({ id: email === "second@example.com" ? alternate : identity });
  }
  const [, , id, ...parts] = url.pathname.split("/");
  const path = parts.join("/");
  const state = snapshot(id);
  if (path === "v1/capabilities") return json({
    contract: "nori-chat-2", service: "nori", conversationModes: ["assistant"], features: { backgroundTasks: true },
    assistant_features: { work: true, interactions: true, library: true, automations: true },
    ...(id === identity && allowModels ? { model_profiles: { default_chat_profile_id: profile.id, profiles: [profile] } } : {}),
  });
  if (path === "v1/interactions") return json({ revision: approval.revision, items: id === identity && approval.status === "pending" ? [approval] : [] });
  if (id === identity && path === `v1/interactions/${approval.id}`) return json(approval);
  if (id === identity && path === `v1/interactions/${approval.id}/responses`) {
    const answer = await input();
    if (approval.status !== "pending" || answer.expected_revision !== approval.revision || answer.value?.kind !== "approve" || answer.value?.remember_rule) {
      return json({ error: "Unexpected approval request" }, 409);
    }
    approval = { ...approval, status: "resolved", revision: approval.revision + 1 };
    return json({ request_id: answer.request_id, accepted: true, status: "applied", resource: approval });
  }
  if (path === "v1/automations/preview" && req.method === "POST") {
    await input();
    return json(routinePreview);
  }
  if (path === "v1/automations" && req.method === "POST") {
    const change = await input();
    routine = { ...routineFixture, ...change.value };
    return json({ request_id: change.request_id, accepted: true, status: "applied", resource: routine }, 201);
  }
  if (path === `v1/automations/${routine.id}`) return json(routine);
  if (path === "v1/snapshot") return json(state);
  if (path === "v1/events") {
    res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
    res.write(": connected\n\n");
    const open = streams.get(id) ?? new Set();
    streams.set(id, open);
    open.add(res);
    res.on("close", () => open.delete(res));
    return;
  }
  if (path === "api/turn") {
    const send = await input();
    const text = String(send.message);
    const reconnect = text === "Reconnect after acceptance";
    if (text === "Send after access was revoked") allowModels = false;
    if (send.model && !allowModels) return json({ error: "Model selection is not enabled for this account.", code: "model_selection_unavailable" }, 403);
    const reply = { id: send.id + "-assistant", turn_id: send.id, role: "assistant", text: `Reply on ${send.model?.chat_profile_id ?? "default"}`, created_at: new Date().toISOString() };
    state.conversation.revision++;
    state.messages.push({ id: send.id + "-user", turn_id: send.id, role: "user", text, created_at: new Date().toISOString() });
    if (reconnect) state.conversation.pending = true;
    json(state, 202);
    if (reconnect) {
      for (const stream of streams.get(id) ?? []) {
        stream.write("retry: 50\n\n");
        stream.end();
      }
    }
    setTimeout(() => {
      state.messages.push(reply);
      state.conversation.pending = false;
      state.position++;
      for (const stream of streams.get(id) ?? []) stream.write(`id: ${state.position}\nevent: message\ndata: ${JSON.stringify({ message: reply })}\n\n`);
    }, reconnect ? 1_000 : 250);
    return;
  }
  if (path === `v1/results/${resultID}/file`) {
    res.writeHead(200, { "Content-Type": "text/plain", "Content-Disposition": 'attachment; filename="fixture.txt"' });
    res.end("fixture download");
    return;
  }
  if (path === "api/seen") return json({ read_position: { position: state.position, message_id: state.messages.at(-1)?.id, through: new Date().toISOString() } });
  return json({ items: [], revision: 1 });
});

await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
const log = { stdio: "inherit" };
const bridge = spawn(process.execPath, ["scripts/prod-check.mjs", `http://127.0.0.1:${upstream.address().port}`, "frontend@example.com", "second@example.com"], { ...log, env: { ...process.env, NORI_PROD_CHECK_DIR: fixture } });
let web;
async function shutdown() {
  web?.kill();
  bridge.kill();
  upstream.closeAllConnections();
  upstream.close();
  await rm(fixture, { recursive: true, force: true });
}
let stopping = false;
for (const signal of ["SIGINT", "SIGTERM"]) process.on(signal, () => {
  if (stopping) return;
  stopping = true;
  void shutdown().finally(() => process.exit());
});
let serviceKey;
for (let i = 0; i < 100; i++) {
  if (bridge.exitCode != null) break;
  try {
    serviceKey = (await readFile(join(fixture, "service-key"), "utf8")).trim();
    if (serviceKey) break;
  } catch {}
  await new Promise(resolve => setTimeout(resolve, 100));
}
if (!serviceKey) {
  await shutdown();
  throw new Error("Hosted OIDC fixture did not start");
}

web = spawn(process.execPath, [".next/standalone/server.js"], {
  ...log,
  env: {
    ...process.env, HOSTNAME: "127.0.0.1", PORT: "4317", NORI_CLOUD: "aws", NORI_AUTH_PROVIDER: "oidc",
    NORI_OIDC_ISSUER: "https://127.0.0.1:9443", NORI_OIDC_AUDIENCE: "nori-prod-check", NORI_OIDC_COOKIE: "nori_prod_check",
    NORI_RUNTIME_URL: "https://127.0.0.1:9444", NORI_SERVICE_TOKEN_KEY: serviceKey,
    NORI_APP_ORIGIN: "http://127.0.0.1:4317", NODE_EXTRA_CA_CERTS: join(fixture, "ca.pem"),
  },
});
const [code] = await once(web, "exit");
await shutdown();
process.exitCode = typeof code === "number" ? code : 1;
