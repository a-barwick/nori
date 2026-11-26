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
const preferences = new Map();
const modelKeys = new Map();
const modelSelections = new Map();
const resetPending = new Set();
const failNextReset = new Set();
const modelProviders = [
  { provider: "openai", label: "OpenAI" },
  { provider: "anthropic", label: "Anthropic" },
  { provider: "google", label: "Google" },
  { provider: "vercel_ai_gateway", label: "Vercel AI Gateway" },
  { provider: "openrouter", label: "OpenRouter" },
];
const pendingApproval = {
  id: "int_5",
  revision: 1,
  task_id: "task_9",
  instruction_revision: 3,
  kind: "approval",
  status: "pending",
  prompt: "Send the invite?",
  action_digest: "sha256:act1",
  payload: {
    summary: "Invite alex@example.com to 'Dentist'",
    effect: "sensitive_send",
    account: { connection_id: "conn_google_personal", label: "personal@example.com" },
    resources: [{ connection_id: "conn_google_personal", kind: "calendar", provider_id: "primary", provider_revision: "etag-7" }],
    recipients: ["alex@example.com"],
    material_effects: ["Alex receives an email invitation"],
  },
  expires_at: new Date(Date.now() + 60 * 60 * 1000).toISOString(),
  created_at: "2026-11-09T09:00:00Z",
};
const approvals = new Map([[identity, pendingApproval]]);
let allowModels = true;
const routinePreview = {
  next_runs: ["2026-10-26T15:00:00Z", "2026-11-02T16:00:00Z", "2026-11-09T16:00:00Z"],
  timezone: "America/Los_Angeles",
  proposed_rules: [{
    effect: "allow",
    tool_ids: ["nori.google/calendar/events.list"],
    connection_ids: ["conn_google_personal"],
    resource_ids: ["primary"],
    action_classes: ["read"],
  }],
  authority_summary: "Read events on Personal (primary)",
  missed_run_explanation: "If Nori is unavailable at 08:00, this run is skipped.",
  warnings: [{
    code: "dst_shift",
    message: "Daylight saving time ends on Nov 1; the run stays at 08:00 local time.",
  }],
};
const routineFixture = {
  id: "auto_1",
  revision: 3,
  title: "Morning briefing",
  instruction: "Summarize today's agenda, urgent mail and due tasks.",
  status: "enabled",
  trigger: {
    kind: "recurring",
    local_start: "2026-11-10T07:30",
    timezone: "Europe/London",
    rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR",
  },
  connection_ids: ["conn_google_personal"],
  budget_usd: 0.5,
  max_active_runs: 1,
  missed_run: "latest",
  execution_window_seconds: 3600,
  destination_ids: ["dest_inapp", "dest_push"],
  next_at: "2026-11-10T07:30:00Z",
  created_at: "2026-11-09T09:00:00Z",
  updated_at: "2026-11-09T09:00:00Z",
  authority_rule_ids: ["rule_auto_1"],
};
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
  if (!preferences.has(id)) preferences.set(id, {
    revision: 1, timezone: "UTC", working_hours: [], default_destination_ids: [], account_defaults: {},
    budgets: { task_usd: 5 }, onboarding_completed_at: "2026-01-01T00:00:00Z",
  });
  if (path === "v1/preferences") {
    if (req.method === "PUT") {
      if (resetPending.has(id)) return json({ code: "data_reset_pending", error: "Finish the reset to continue." }, 409);
      const change = await input();
      const current = preferences.get(id);
      const updated = { ...change.value, revision: current.revision + 1, ...(current.data_reset_at ? { data_reset_at: current.data_reset_at } : {}) };
      preferences.set(id, updated);
      return json({ request_id: change.request_id, accepted: true, status: "applied", resource: updated });
    }
    return json({ ...preferences.get(id), ...(resetPending.has(id) ? { data_reset_pending: true } : {}) });
  }
  if (path === "v1/settings/reset" && req.method === "POST") {
    const body = await input();
    if (body.confirmation !== "RESET") return json({ error: "Type RESET to confirm." }, 400);
    if (body.resume_pending && !resetPending.has(id)) return json({ code: "data_reset_not_pending", error: "There is no reset to resume." }, 409);
    if (failNextReset.delete(id)) {
      resetPending.add(id);
      return json({ code: "data_reset_pending", error: "Finish the reset to continue." }, 502);
    }
    resetPending.delete(id);
    const position = state.position + 1;
    sessions.delete(id);
    modelKeys.delete(id);
    modelSelections.delete(id);
    const cleared = snapshot(id);
    cleared.position = position;
    cleared.messages.push({ id: "intro", role: "assistant", text: "Hello from Nori", created_at: new Date().toISOString() });
    const dataResetAt = new Date().toISOString();
    preferences.set(id, {
      revision: 1, timezone: "UTC", working_hours: [], default_destination_ids: [], account_defaults: {},
      budgets: { task_usd: 5 }, data_reset_at: dataResetAt,
    });
    for (const stream of streams.get(id) ?? []) stream.write(`id: ${position}\nevent: reset\ndata: ${JSON.stringify({ position })}\n\n`);
    res.writeHead(204).end();
    return;
  }
  if (resetPending.has(id) && req.method !== "GET" && req.method !== "HEAD") {
    return json({ code: "data_reset_pending", error: "Finish the reset to continue." }, 409);
  }
  if (path === "v1/settings/model-keys") return json({
    byok_required: true,
    providers: modelProviders.map((provider) => ({ ...provider, configured: modelKeys.get(id)?.has(provider.provider) ?? false })),
  });
  if (path === "v1/settings/models") return json({
    model_profiles: { profiles: [] }, selection: modelSelections.get(id) ?? {},
    revision: modelSelections.has(id) ? 1 : 0, recommendations: {},
  });
  if (path === "v1/capabilities") return json({
    contract: "nori-chat-2", service: "nori", conversationModes: ["assistant"], features: { backgroundTasks: true },
    assistant_features: { work: true, interactions: true, library: true, automations: true },
    ...(id === identity && allowModels ? { model_profiles: { default_chat_profile_id: profile.id, profiles: [profile] } } : {}),
  });
  const approval = approvals.get(id);
  if (path === "v1/interactions") return json({ items: approval?.status === "pending" ? [approval] : [] });
  if (approval && path === `v1/interactions/${approval.id}`) return json(approval);
  if (approval && path === `v1/interactions/${approval.id}/responses`) {
    const answer = await input();
    if (approval.status !== "pending" || answer.expected_revision !== approval.revision || answer.value?.kind !== "approve" || answer.value?.remember_rule) {
      return json({ error: "Unexpected approval request" }, 409);
    }
    const resolved = { ...approval, status: "resolved", revision: approval.revision + 1 };
    approvals.set(id, resolved);
    return json({ request_id: answer.request_id, accepted: true, status: "applied", resource: resolved });
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
const control = createServer((req, res) => {
  if (req.method === "POST" && req.url === "/prepare-reset-alternate") {
    preferences.set(alternate, {
      revision: 1, timezone: "UTC", working_hours: [], default_destination_ids: [], account_defaults: {},
      budgets: { task_usd: 5 }, onboarding_completed_at: "2026-01-01T00:00:00Z",
    });
    sessions.delete(alternate);
    modelKeys.delete(alternate);
    modelSelections.delete(alternate);
    resetPending.delete(alternate);
    failNextReset.delete(alternate);
    res.writeHead(204).end();
    return;
  }
  if (req.method === "POST" && req.url === "/interrupt-reset-alternate") {
    failNextReset.add(alternate);
    res.writeHead(204).end();
    return;
  }
  if (req.method === "POST" && req.url === "/pending-reset-alternate") {
    resetPending.add(alternate);
    res.writeHead(204).end();
    return;
  }
  if (req.method === "POST" && req.url === "/finish-reset-alternate-with-new-data") {
    resetPending.delete(alternate);
    preferences.set(alternate, { ...preferences.get(alternate), data_reset_at: new Date().toISOString() });
    modelKeys.set(alternate, new Set(["google"]));
    modelSelections.set(alternate, { chat_profile_id: "google-neon", task_profile_id: "google-hydrogen" });
    res.writeHead(204).end();
    return;
  }
  if (req.method === "POST" && req.url === "/seed-models") {
    modelKeys.set(identity, new Set(modelProviders.map((provider) => provider.provider)));
    modelSelections.set(identity, {
      chat_profile_id: "anthropic-opus", chat_reasoning: "low",
      task_profile_id: "anthropic-haiku", task_reasoning: "low",
    });
    res.writeHead(204).end();
    return;
  }
  if (req.method === "POST" && req.url === "/reset-alternate") {
    approvals.delete(alternate);
    sessions.delete(alternate);
    res.writeHead(204).end();
    return;
  }
  if (req.method !== "POST" || req.url !== "/worker-approval") {
    res.writeHead(404).end();
    return;
  }
  const state = snapshot(alternate);
  approvals.set(alternate, { ...pendingApproval });
  state.position++;
  const run = {
    id: pendingApproval.task_id, status: "running", cancel_requested: false, position: state.position,
    started_at: new Date().toISOString(), events: [], artifacts: [],
  };
  for (const stream of streams.get(alternate) ?? []) {
    stream.write(`id: ${state.position}\nevent: task\ndata: ${JSON.stringify({ run })}\n\n`);
  }
  res.writeHead(204).end();
});
const controlPort = Number(process.env.NORI_E2E_CONTROL_PORT ?? "4318");
const appPort = process.env.NORI_E2E_APP_PORT ?? "4317";
const appOrigin = process.env.NORI_E2E_APP_URL ?? `http://127.0.0.1:${appPort}`;
await new Promise(resolve => control.listen(controlPort, "127.0.0.1", resolve));
const log = { stdio: "inherit" };
const bridge = spawn(process.execPath, ["scripts/prod-check.mjs", `http://127.0.0.1:${upstream.address().port}`, "frontend@example.com", "second@example.com"], { ...log, env: { ...process.env, NORI_PROD_CHECK_DIR: fixture } });
let web;
async function shutdown() {
  web?.kill();
  bridge.kill();
  upstream.closeAllConnections();
  upstream.close();
  control.closeAllConnections();
  control.close();
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
    ...process.env, HOSTNAME: "127.0.0.1", PORT: appPort, NORI_CLOUD: "aws", NORI_AUTH_PROVIDER: "oidc",
    NORI_OIDC_ISSUER: "https://127.0.0.1:9443", NORI_OIDC_AUDIENCE: "nori-prod-check", NORI_OIDC_COOKIE: "nori_prod_check",
    NORI_RUNTIME_URL: "https://127.0.0.1:9444", NORI_SERVICE_TOKEN_KEY: serviceKey,
    NORI_APP_ORIGIN: appOrigin, NODE_EXTRA_CA_CERTS: join(fixture, "ca.pem"),
  },
});
const [code] = await once(web, "exit");
await shutdown();
process.exitCode = typeof code === "number" ? code : 1;
