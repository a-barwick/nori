import { afterAll, beforeAll, beforeEach, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Unpaid: the real workflow, Firestore emulator and Temporal, with a scripted model and a fake Google.
// NORI_M7_E2E=1 FIRESTORE_EMULATOR_HOST=127.0.0.1:8095 TEMPORAL_ADDRESS=127.0.0.1:7245 bun test core/e2e/milestone7.test.js
const live = process.env.NORI_M7_E2E === "1";
const journey = live ? test : test.skip;
const core = resolve(import.meta.dir, "..");
const unique = crypto.randomUUID().slice(0, 8);
const emulator = process.env.FIRESTORE_EMULATOR_HOST;
const temporalAddress = process.env.TEMPORAL_ADDRESS;
const database = `nori-m7-${unique}`;
const deployment = `nori-m7-${unique}`;
const build = `m7-${unique}`;
const account = "owner@example.test";
const documents = `http://${emulator}/v1/projects/bot-111549/databases/${database}/documents`;
const delay = (ms) => new Promise((done) => setTimeout(done, ms));
let directory, address, api, worker, env;
const servers = [];

async function until(read, accept, label, timeout = 45_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try {
      last = await read();
      if (accept(last)) return last;
    } catch (error) { last = String(error); }
    await delay(100);
  }
  throw new Error(`Timed out waiting for ${label}: ${JSON.stringify(last)?.slice(0, 1500)}`);
}

// Fake Google: one write calendar and a sent-mail box, with faults a test can set.
const google = { events: new Map(), mail: [], writes: [], reads: 0, fault: {}, release: [] };
function resetGoogle() {
  Object.assign(google, { events: new Map(), mail: [], writes: [], reads: 0, fault: {} });
  for (const go of google.release.splice(0)) go();
}
function held() { return new Promise((done) => google.release.push(done)); }
async function body(req) {
  let data = "";
  for await (const chunk of req) data += chunk;
  return data ? JSON.parse(data) : {};
}
function reply(res, status, value) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(value ?? {}));
}
async function googleHandler(req, res) {
  const url = new URL(req.url, "http://fake");
  if (url.pathname === "/token") return reply(res, 200, { access_token: "fake", token_type: "Bearer", expires_in: 3600 });
  const calendarMatch = url.pathname.match(/^\/calendar\/v3\/calendars\/([^/]+)(?:\/events(?:\/(.+))?)?$/);
  if (url.pathname.endsWith("/users/me/calendarList")) {
    return reply(res, 200, { items: [{ id: account, summary: "Personal", primary: true, selected: true, timeZone: "America/Los_Angeles" }] });
  }
  if (calendarMatch && !url.pathname.includes("/events")) return reply(res, 200, { id: account, summary: "Personal" });
  if (calendarMatch && req.method === "GET" && !calendarMatch[2]) {
    return reply(res, 200, { items: [...google.events.values()].filter((event) => event.status !== "cancelled") });
  }
  if (calendarMatch && req.method === "GET") {
    google.reads++;
    if (google.fault.holdRead) { const { fault } = google; await held(); if (fault !== google.fault) return res.destroy(); }
    if (google.fault.readError && google.writes.length) return reply(res, 500);
    const event = google.events.get(decodeURIComponent(calendarMatch[2]));
    return event ? reply(res, 200, event) : reply(res, 404, { error: "notFound" });
  }
  if (calendarMatch && req.method === "POST") {
    const event = await body(req);
    google.writes.push({ kind: "insert", event, query: url.search });
    return write(res, () => {
      if (google.events.has(event.id)) return [409, { error: "duplicate" }];
      const saved = { ...event, status: "confirmed", etag: `"v1"`, organizer: { self: true } };
      google.events.set(event.id, saved);
      return [200, saved];
    });
  }
  if (calendarMatch && req.method === "PATCH") {
    const id = decodeURIComponent(calendarMatch[2]);
    const patch = await body(req);
    google.writes.push({ kind: "patch", id, patch, ifMatch: req.headers["if-match"], query: url.search });
    return write(res, () => {
      const event = google.events.get(id);
      if (!event) return [404, {}];
      if (req.headers["if-match"] !== event.etag) return [412, { error: "conditionNotMet" }];
      Object.assign(event, patch, { etag: `"v${Date.now()}"` });
      return [200, event];
    });
  }
  if (url.pathname === "/gmail/v1/users/me/messages/send") {
    const { raw } = await body(req);
    const text = Buffer.from(raw, "base64url").toString();
    const header = (name) => text.match(new RegExp(`^${name}: (.*)$`, "mi"))?.[1]?.trim();
    google.writes.push({ kind: "send", to: header("To"), subject: header("Subject") });
    return write(res, () => {
      const id = `m${google.mail.length + 1}`;
      google.mail.unshift({ id, labelIds: ["SENT"], payload: { headers: ["X-Nori-Action", "To", "Subject"].map((name) => ({ name, value: header(name) })) } });
      return [200, { id }];
    });
  }
  if (url.pathname === "/gmail/v1/users/me/messages") return reply(res, 200, { messages: google.mail.map(({ id }) => ({ id })) });
  const mail = url.pathname.match(/^\/gmail\/v1\/users\/me\/messages\/(.+)$/);
  if (mail) {
    const message = google.mail.find((item) => item.id === mail[1]);
    return message ? reply(res, 200, message) : reply(res, 404);
  }
  reply(res, 404);
}
// Faults: dropBeforeCommit and dropAfterCommit lose the response, holdBeforeCommit and
// holdAfterCommit leave the request open, and status refuses the write.
async function write(res, commit) {
  const { fault } = google;
  if (fault.status) return reply(res, fault.status, {});
  if (fault.holdBeforeCommit) await held();
  // A request held across a reset belongs to a worker that was killed, so it never commits.
  if (fault.dropBeforeCommit || fault !== google.fault) return res.destroy();
  const [status, value] = commit();
  if (fault.holdAfterCommit) await held();
  if (fault.dropAfterCommit) return res.destroy();
  reply(res, status, value);
}

// Scripted inner model: each task's first message names a script, and the script sees
// every tool result so far.
const scripts = {};
const costs = {};
const inner = { calls: 0 };
function citable(outputs) {
  return outputs.filter((item) => ["confirmed", "submitted", "failed", "declined"].includes(item.outcome)).map((item) => item.operation_id);
}
const finish = (answer, outputs, unmet = []) => ({ name: "complete", args: { answer, evidence: citable(outputs), unmet } });
async function modelHandler(req, res) {
  const request = await body(req);
  inner.calls++;
  const items = Array.isArray(request.input) ? request.input : [];
  const users = items.filter((item) => item.role === "user").map((item) => typeof item.content === "string" ? item.content : item.content?.map((part) => part.text).join("")).filter(Boolean);
  const outputs = items.filter((item) => item.type === "function_call_output").map((item) => JSON.parse(item.output));
  inner.lastOutputs = outputs;
  const name = users.join("\n").match(/script:(\w+)/)?.[1];
  const call = scripts[name]?.(outputs, users) ?? { name: "complete", args: { answer: "No script.", evidence: [], unmet: [] } };
  reply(res, 200, {
    status: "completed", usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2, cost: costs[name] ?? 0.0001 },
    output: [{ type: "function_call", call_id: `call_${crypto.randomUUID()}`, name: call.name, arguments: JSON.stringify(call.args) }],
  });
}

// Scripted outer model: "task: ..." dispatches. A restricted turn offers only dispatch and answer, and
// outer.onWork decides it. A turn with neither is a notification, which returns plain text.
const outer = { calls: 0, workTurns: 0, notifications: 0 };
const subtask = (parent, script) => ({ name: "dispatch", arguments: { request: `script:${script}`, for: parent } });
async function outerHandler(req, res) {
  const request = await body(req);
  outer.calls++;
  const last = request.messages.findLast((message) => message.role === "user")?.content ?? "";
  const variants = request.response_format?.json_schema.schema.properties.actions.items.anyOf;
  let content = "Noted.";
  if (variants && variants.some((v) => v.properties.name.enum[0] === "answer") && !variants.some((v) => v.properties.name.enum[0] === "stop")) {
    outer.workTurns++;
    const events = request.messages.filter((message) => message.role === "system" && message.content.startsWith("A task's work request"))
      .map((message) => JSON.parse(message.content.slice(message.content.indexOf("\n") + 1)));
    const event = events.at(-1);
    outer.lastSchema = variants;
    const actions = outer.onWork?.(event) ?? [];
    content = JSON.stringify({ actions, text: actions.length ? "" : "Which one should it be?" });
  } else if (variants) {
    const actions = outer.onUser?.(String(last)) ?? (String(last).includes("task:") ? [{ name: "dispatch", arguments: { request: String(last), for: null } }] : []);
    content = JSON.stringify({ actions, text: actions.length ? "" : "Noted." });
  } else {
    outer.notifications++;
  }
  reply(res, 200, {
    id: "chat", model: "gpt-6.1-sol", service_tier: "default",
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, prompt_tokens_details: { cached_tokens: 0 }, completion_tokens_details: { reasoning_tokens: 0 } },
    choices: [{ message: { role: "assistant", content }, finish_reason: "stop" }],
  });
}

function serve(handler) {
  return new Promise((done) => {
    const server = createServer((req, res) => handler(req, res).catch((error) => { console.error(error); res.destroy(); }));
    server.listen(0, "127.0.0.1", () => { servers.push(server); done(`http://127.0.0.1:${server.address().port}`); });
  });
}
function cli(...args) {
  const result = spawnSync("temporal", ["--address", temporalAddress, "--namespace", "default", "--output", "json", ...args], { encoding: "utf8", timeout: 20_000 });
  if (result.status !== 0) throw new Error(`Temporal ${args.slice(0, 2).join(" ")} failed: ${result.stderr}`);
  return result.stdout.trim() ? JSON.parse(result.stdout) : {};
}
function start(name) {
  // Without PORT the worker polls until stopped.
  const childEnv = { ...env };
  if (name === "nori-worker") delete childEnv.PORT;
  const child = spawn(resolve(directory, name), [], { cwd: core, env: childEnv, stdio: process.env.NORI_M7_LOGS ? "inherit" : "ignore" });
  child.exited = new Promise((done) => child.once("exit", done));
  return child;
}
async function stop(child, signal = "SIGTERM") {
  if (child && child.exitCode === null && child.signalCode === null) child.kill(signal);
  if (child) await child.exited;
}
async function startWorker() {
  worker = start("nori-worker");
  await until(() => cli("worker", "deployment", "describe-version", "--deployment-name", deployment, "--build-id", build), () => true, "worker registration");
}
const killWorker = () => stop(worker, "SIGKILL");
async function post(operation, value = {}, expected) {
  const response = await fetch(`${address}/api/${operation}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(value), signal: AbortSignal.timeout(15_000) });
  const result = await response.json();
  if (expected ? response.status !== expected : !response.ok) throw new Error(`${operation}: HTTP ${response.status}: ${JSON.stringify(result)}`);
  return result;
}
const session = () => post("session");
const task = async (id) => (await session()).runs.find((run) => run.id === id);
const terminal = (run) => ["completed", "failed", "cancelled"].includes(run?.status);
const finished = (id) => until(() => task(id), terminal, `task ${id} to end`, 90_000);
async function dispatch(script) {
  const before = new Set((await session()).runs.map((run) => run.id));
  await post("turn", { id: crypto.randomUUID(), message: `task: script:${script}` }, 202);
  const value = await until(session, (now) => now.runs.some((run) => !before.has(run.id)), "dispatch");
  return value.runs.find((run) => !before.has(run.id)).id;
}
const awaiting = (id, kind = "approval") => until(() => task(id), (run) => run?.question?.kind === kind && !run.question.resolved_by && run.incorporated === run.position, `${kind} question`);
const command = (kind, id, question, text) => post("command", { id: crypto.randomUUID(), run_id: id, kind, question_id: question?.id, text }, 202);
async function operations(id) {
  const response = await fetch(`${documents}/owners/austinbarwickdavis@gmail.com/runs/${id}/operations`);
  const { documents: docs = [] } = await response.json();
  return docs.map((doc) => ({ name: doc.fields.name?.stringValue, outcome: doc.fields.outcome?.stringValue, approvedBy: doc.fields.action?.mapValue?.fields?.approved_by?.stringValue }));
}
const writes = (ops) => ops.filter((op) => ["calendar_create", "calendar_move", "email_send"].includes(op.name));
const future = (hours) => new Date(Date.now() + hours * 3_600_000).toISOString().replace(/\.\d+Z$/, "Z");
const request = (args) => ({ name: "request_work", args: { request: "the lookup", why: "the answer depends on it", ...args } });
const lunch = { title: "Lunch with Jamie", start: future(24), end: future(25), description: "", private: "false" };
const createThenFinish = (args = lunch) => (outputs) => outputs.length ? finish(`Outcome: ${outputs.at(-1).outcome}`, outputs, outputs.at(-1).outcome === "uncertain" ? ["The write could not be confirmed."] : []) : { name: "calendar_create", args };

beforeAll(async () => {
  if (!live) return;
  directory = mkdtempSync(resolve(tmpdir(), "nori-m7-"));
  for (const name of ["nori-local", "nori-worker"]) {
    const built = spawnSync("go", ["build", "-o", resolve(directory, name), `./cmd/${name}`], { cwd: core, encoding: "utf8", timeout: 180_000 });
    if (built.status !== 0) throw new Error(`Build ${name} failed: ${built.stderr}`);
  }
  writeFileSync(resolve(directory, "empty.env"), "");
  const [googleURL, modelURL, outerURL] = await Promise.all([serve(googleHandler), serve(modelHandler), serve(outerHandler)]);
  const port = 20000 + Math.floor(Math.random() * 20000);
  address = `http://127.0.0.1:${port}`;
  env = {
    PATH: process.env.PATH, HOME: process.env.HOME, NORI_ENV_FILE: resolve(directory, "empty.env"), NORI_PROJECT_ID: "bot-111549", FIRESTORE_DATABASE_ID: database,
    FIRESTORE_EMULATOR_HOST: emulator, NORI_LOCAL_ARTIFACT_DIR: resolve(directory, "artifacts"), PORT: String(port), TEMPORAL_ADDRESS: temporalAddress, TEMPORAL_NAMESPACE: "default", TEMPORAL_LOCAL: "true",
    TEMPORAL_TASK_QUEUE: deployment, TEMPORAL_DEPLOYMENT_NAME: deployment, TEMPORAL_BUILD_ID: build, COMPUTER_ZONE: "invalid-e2e-zone", COMPUTER_INSTANCE: "no-computer-e2e", NORI_EXECUTION_MODE: "temporal",
    OPENAI_API_KEY: "fake", JEV_API_KEY: "fake", OPENROUTER_API_KEY: "fake", NORI_E2E_GOOGLE_URL: googleURL, NORI_E2E_MODEL_URL: modelURL, NORI_E2E_OPENAI_URL: outerURL,
    GOOGLE_CALENDAR_CLIENT_ID: "fake", GOOGLE_CALENDAR_CLIENT_SECRET: "fake", GOOGLE_CALENDAR_REFRESH_TOKEN: "fake", GOOGLE_CALENDAR_ACCOUNT: account,
  };
  const control = await fetch(`${documents}/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: false } } }) });
  if (!control.ok) throw new Error("control document could not be set");
  api = start("nori-local");
  await until(() => fetch(`${address}/healthz`), (response) => response.ok, "API");
  await startWorker();
  cli("worker", "deployment", "set-current-version", "--deployment-name", deployment, "--build-id", build, "--yes");
}, 300_000);

afterAll(async () => {
  if (!live) return;
  await stop(worker);
  await stop(api);
  for (const server of servers) server.close();
  for (const go of google.release.splice(0)) go();
  await fetch(`http://${emulator}/emulator/v1/projects/bot-111549/databases/${database}/documents`, { method: "DELETE" }).catch(() => {});
  if (directory) rmSync(directory, { recursive: true, force: true });
});
beforeEach(() => {
  if (!live) return;
  resetGoogle();
  outer.onWork = outer.onUser = undefined;
});

const subtasksOf = async (id) => (await task(id)).subtasks?.map((link) => link.id) ?? [];
const running = (id) => until(() => task(id), (run) => run?.status === "running", `task ${id} to run`);
// A parent that asks once, then finishes citing whatever came back.
const asks = (args) => (outputs) => outputs.length ? finish(`Parent saw: ${JSON.stringify(outputs.at(-1).subtasks?.map((item) => item.answer) ?? outputs.at(-1).answer)}`, outputs) : request(args);
const answers = (text) => (outputs) => finish(text, outputs);

journey("the result comes back as the parent's request_work result, with no model call on delivery", async () => {
  scripts.parent107 = asks();
  scripts.child107 = answers("Rain from 1 p.m.");
  costs.child107 = 0.05;
  outer.onWork = (event) => [subtask(event.run_id, "child107")];
  const id = await dispatch("parent107");
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(done.result).toContain("Rain from 1 p.m.");
  const [child] = await subtasksOf(id);
  const finishedChild = await task(child);
  expect(finishedChild.parent_id).toBe(id);
  expect(finishedChild.status).toBe("completed");
  // One result, delivered to the original call.
  const parentOutputs = inner.lastOutputs;
  expect(parentOutputs.filter((item) => item.subtasks)).toHaveLength(1);
  expect(done.subtask_spent_usd).toBeCloseTo(0.05, 5);
  // Outer calls: the user's turn, the work request, and the parent's result. Nothing for the subtask's end.
  await until(session, (value) => !value.conversation.pending && !value.conversation.processing, "conversation to settle");
  expect(outer.workTurns).toBeGreaterThan(0);
  const ops = await operations(id);
  expect(ops.find((op) => op.name === "request_work").outcome).toBe("confirmed");
}, 120_000);
