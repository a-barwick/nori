import { afterAll, beforeAll, expect, test } from "bun:test";

// Explicit opt-in to fresh local servers and the external Cloud Tasks stand-in.
// Nori, Temporal and the selected model providers execute normally.
const configPath = process.env.NORI_WORKER_E2E_CONFIG;
const config = configPath ? await Bun.file(configPath).json() : {};
const ports = configPath ? await Bun.file(new URL("ports.json", `file://${configPath}`)).json() : {};
const root = configPath?.slice(0, configPath.lastIndexOf("/"));
const journey = configPath ? test : test.skip;
const api = `http://127.0.0.1:${ports.api || 1}/api/`;
const tasks = `http://127.0.0.1:${ports.tasks || 1}`;
const worker = `http://127.0.0.1:${ports.worker || 1}`;
const created = new Set();
if (configPath) {
  if (config.TEMPORAL_LOCAL !== "true" || !config.FIRESTORE_DATABASE_ID?.startsWith("nori-worker-") ||
      !/^127\.0\.0\.1:\d+$/.test(config.FIRESTORE_EMULATOR_HOST || "") ||
      !/^127\.0\.0\.1:\d+$/.test(config.TEMPORAL_ADDRESS || "")) {
    throw new Error("Request worker E2E requires explicit isolated loopback Temporal/Firestore configuration.");
  }
}

async function post(url, body = {}) {
  const response = await fetch(url, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal: AbortSignal.timeout(30_000) });
  const text = await response.text();
  let value;
  try { value = text ? JSON.parse(text) : {}; } catch { value = { text }; }
  expect(response.ok, `${url}: HTTP ${response.status}: ${text.slice(0, 300)}`).toBe(true);
  return value;
}
const session = () => post(`${api}session`);
const queue = async () => (await fetch(tasks)).json();
async function until(read, accepts, label, timeout = 150_000) {
  const deadline = Date.now() + timeout;
  let value;
  while (Date.now() < deadline) {
    value = await read();
    if (accepts(value)) return value;
    await Bun.sleep(100);
  }
  throw new Error(`Timed out waiting for ${label}: ${String(JSON.stringify(value)).slice(0, 500)}`);
}
const settled = () => until(session, (s) => !s.conversation.pending && !s.conversation.processing, "outer routing");
const idle = () => until(queue, (q) => q.pending.length === 0, "worker requests to finish");
const terminal = (r) => ["completed", "failed", "cancelled"].includes(r?.status);
const waiting = (r) => r?.status === "running" && r.question?.kind === "input" && !r.question.resolved_by;
const find = async (id) => (await session()).runs.find((r) => r.id === id);
const command = (id, kind, extra = {}) => post(`${api}command`, { id: crypto.randomUUID(), run_id: id, kind, ...extra });
async function start(message) {
  const existing = new Set((await settled()).runs.map((r) => r.id));
  await post(`${api}turn`, { id: crypto.randomUUID(), message: `${message} Include a brief acknowledgment when starting the task.` });
  const run = await until(async () => {
    const s = await session();
    const run = s.runs.find((r) => !existing.has(r.id));
    if (!run && !s.conversation.pending && !s.conversation.processing && s.traces[0]?.status === "error") {
      throw new Error(`Outer task admission failed before worker execution: ${s.traces[0].error}`);
    }
    return run;
  }, Boolean, "accepted task");
  created.add(run.id);
  return run;
}
async function temporal(operation, id) {
  const env = { ...process.env, TEMPORAL_TLS: "false" };
  delete env.TEMPORAL_API_KEY;
  const child = Bun.spawn(["temporal", "--address", config.TEMPORAL_ADDRESS, "--namespace", config.TEMPORAL_NAMESPACE, "--output", "json", "workflow", operation, "--workflow-id", `nori-task-${id}`], { env, stdout: "pipe", stderr: "pipe" });
  const [stdout, stderr, code] = await Promise.all([new Response(child.stdout).text(), new Response(child.stderr).text(), child.exited]);
  expect(code, stderr).toBe(0);
  return JSON.parse(stdout);
}
const describe = (id) => temporal("describe", id);
const history = (id) => temporal("show", id);
function models(h) {
  const result = new Map();
  for (const e of h.events) {
    const a = e.activityTaskScheduledEventAttributes;
    if (a?.activityType.name === "Model") result.set(e.eventId, { id: a.activityId, policy: a.retryPolicy, attempts: [], completions: [] });
  }
  for (const e of h.events) {
    const a = e.activityTaskStartedEventAttributes || e.activityTaskCompletedEventAttributes || e.activityTaskTimedOutEventAttributes || e.activityTaskFailedEventAttributes;
    const model = a && result.get(a.scheduledEventId);
    if (!model) continue;
    if (e.activityTaskStartedEventAttributes) { model.attempts.push(a.attempt); model.startedAt = e.eventTime; }
    else if (e.activityTaskCompletedEventAttributes) { model.completions.push(JSON.parse(Buffer.from(a.result.payloads[0].data, "base64").toString())); model.completedAt = e.eventTime; }
    else model.failure = e.eventType;
  }
  return [...result.values()];
}
function assertNoPaidReplay(h) {
  for (const m of models(h)) {
    expect(m.policy.maximumAttempts).toBe(1);
    expect(m.attempts).toEqual([1]);
    expect(m.failure).toBeUndefined();
    expect(m.completions).toHaveLength(1);
    expect(m.completions[0].Failure).toBe("");
    expect(m.completions[0].Decision.CostKnown).toBe(true);
  }
}
const firestore = () => `http://${config.FIRESTORE_EMULATOR_HOST}/v1/projects/${config.NORI_PROJECT_ID}/databases/${config.FIRESTORE_DATABASE_ID}/documents/`;
async function pause(value) {
  const response = await fetch(`${firestore()}system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: value } } }) });
  expect(response.ok).toBe(true);
}

beforeAll(async () => {
  if (!configPath) return;
  const s = await session();
  expect(s.runs, "Use a fresh database, never a saved demo or personal conversation.").toHaveLength(0);
  expect(s.paused).toBe(false);
  expect((await queue()).pending).toHaveLength(0);
  await post(`${tasks}/release`);
});

afterAll(async () => {
  if (!configPath) return;
  await pause(false);
  await post(`${tasks}/release`);
  for (const id of created) {
    if (!terminal(await find(id))) await command(id, "stop");
  }
  await idle();
});

journey("admitted model work drains; a late command and exact answer resume the same pinned workflow", async () => {
  const logStart = (await Bun.file(`${root}/worker.log`).text()).length;
  const run = await start("Start exactly one task now. Design a small desk organizer using only reasoning, then use request_input to ask what color I want. Do not browse or run shell commands. Do not finish until I answer; after my answer give the final concise description and finish.");
  await until(async () => (await Bun.file(`${root}/worker.log`).text()).slice(logStart), (s) => /phase=draining active=1/.test(s), "a real admitted activity to outlive polling");
  await command(run.id, "steer", { text: "Use exactly three compartments. Continue by asking the color question before finishing." });
  const question = await until(() => find(run.id), waiting, "color question after the late command");
  expect(question.revision).toBeGreaterThan(run.revision);
  await settled();
  await idle();
  const before = await describe(run.id);
  expect(before.workflowExecutionInfo.versioningInfo.deploymentVersion.buildId).toBe(config.TEMPORAL_BUILD_ID);
  await command(run.id, "answer", { question_id: question.question.id, text: "Navy blue" });
  const done = await until(() => find(run.id), terminal, "same task to finish after answer");
  expect(done.status).toBe("completed");
  await settled();
  await idle();
  const after = await describe(run.id);
  expect(after.workflowExecutionInfo.execution).toEqual(before.workflowExecutionInfo.execution);
  expect(after.workflowExecutionInfo.versioningInfo.deploymentVersion).toEqual(before.workflowExecutionInfo.versioningInfo.deploymentVersion);
  const h = await history(run.id);
  assertNoPaidReplay(h);
  const log = (await Bun.file(`${root}/worker.log`).text()).slice(logStart);
  // Compare elapsed wall-clock time within the Go log, anchored to Temporal's
  // UTC start. Bun's test timezone can differ from the application process.
  const lines = log.split("\n");
  const wall = (s) => Date.parse(`${s.slice(0, 19).replaceAll("/", "-").replace(" ", "T")}Z`);
  const cutoffs = lines.filter((s) => /phase=draining active=1/.test(s)).map(wall);
  expect(models(h).some((m) => {
    const started = lines.find((s) => s.includes(`ActivityID ${m.id} ActivityType Model`));
    return started && cutoffs.some((t) => {
      const elapsed = t - wall(started);
      return elapsed >= 0 && elapsed < Date.parse(m.completedAt) - Date.parse(m.startedAt);
    });
  }), "A real model call must complete after polling stopped.").toBe(true);
  expect(models(h).length).toBeGreaterThanOrEqual(2); // Revision/answer calls are distinct work, not Activity retries.
  expect((await session()).messages.some((m) => m.role === "assistant" && /navy blue/i.test(m.text))).toBe(true);
}, 360_000);

journey("cold commands survive delivery wait; paused Stop and terminal wake identities survive resume", async () => {
  await post(`${tasks}/hold`);
  const dispatchedBefore = (await queue()).deliveries.length;
  const run = await start("Start exactly one task now. Name a compact desk organizer, then use request_input to ask what color I want. Do not browse or run shell commands. Wait until I answer.");
  await command(run.id, "steer", { text: "The organizer name must include Pebble. Ask the color question and wait." });
  expect((await queue()).deliveries.length).toBe(dispatchedBefore);
  await post(`${tasks}/release`);
  const question = await until(() => find(run.id), waiting, "both cold commands to be incorporated");
  expect(question.question.text.toLowerCase()).toContain("pebble");
  await settled();
  await idle();
  const before = await describe(run.id);
  const paidBefore = models(await history(run.id)).length;
  await post(`${tasks}/hold`); // Native operator fences delivery before application admission.
  await pause(true);
  const acceptedBefore = (await queue()).accepted.length;
  const stopped = await command(run.id, "stop");
  await until(() => find(run.id), (r) => r.status === "cancelled", "paused terminal projection");
  const commandURL = `${firestore()}owners/austinbarwickdavis%40gmail.com/task_commands/${stopped.command.id}`;
  const receipt = async () => (await (await fetch(commandURL)).json()).fields.delivered.booleanValue;
  expect(await receipt()).toBe(false);
  expect((await queue()).accepted.length).toBe(acceptedBefore);
  expect((await describe(run.id)).workflowExecutionInfo.status).toBe("WORKFLOW_EXECUTION_STATUS_RUNNING");
  await pause(false);
  await post(`${api}resume`);
  expect(await receipt()).toBe(true);
  // Cloud Tasks notifications can expire during a long pause. The cancelled
  // Firestore record is no longer open, but its Temporal execution is durable.
  const expired = await post(`${tasks}/expire`);
  expect(expired.expired.length).toBeGreaterThan(0);
  expect((await queue()).pending).toHaveLength(0);
  await post(`${api}resume`);
  expect((await queue()).pending.some((t) => JSON.parse(Buffer.from(t.task.httpRequest.body, "base64").toString()).workflow_ids?.includes(`nori-task-${run.id}`))).toBe(true);
  await post(`${tasks}/release`);
  const closed = await until(() => describe(run.id), (d) => d.workflowExecutionInfo.status !== "WORKFLOW_EXECUTION_STATUS_RUNNING", "parked Stop workflow to close");
  await idle();
  expect(closed.workflowExecutionInfo.execution).toEqual(before.workflowExecutionInfo.execution);
  expect((await find(run.id)).status).toBe("cancelled");
  expect(models(await history(run.id)).length).toBe(paidBefore);
  // A dispatch already in flight when pause closes admission must retain its
  // exact IDs even though Firestore no longer lists this terminal task as open.
  await post(`${tasks}/hold`);
  await pause(true);
  const logStart = (await Bun.file(`${root}/worker.log`).text()).length;
  await post(`${worker}/run`, { workflow_ids: [`nori-task-${run.id}`] });
  const retained = (await queue()).pending;
  expect(retained.some((t) => JSON.parse(Buffer.from(t.task.httpRequest.body, "base64").toString()).workflow_ids?.includes(`nori-task-${run.id}`))).toBe(true);
  expect((await Bun.file(`${root}/worker.log`).text()).slice(logStart)).not.toContain("Started Worker");
  await pause(false);
  await post(`${api}resume`);
  await post(`${tasks}/release`);
  await idle();
  expect(models(await history(run.id)).length).toBe(paidBefore);
}, 360_000);
