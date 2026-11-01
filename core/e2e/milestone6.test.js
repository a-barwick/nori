import { afterAll, beforeAll, beforeEach, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Unpaid: the real workflow, Firestore emulator and Temporal, with a scripted model and a fake Google.
// NORI_M6_E2E=1 FIRESTORE_EMULATOR_HOST=127.0.0.1:8095 TEMPORAL_ADDRESS=127.0.0.1:7245 bun test core/e2e/milestone6.test.js
const live = process.env.NORI_M6_E2E === "1";
const journey = live ? test : test.skip;
const core = resolve(import.meta.dir, "..");
const unique = crypto.randomUUID().slice(0, 8);
const emulator = process.env.FIRESTORE_EMULATOR_HOST;
const temporalAddress = process.env.TEMPORAL_ADDRESS;
const database = `nori-m6-${unique}`;
const deployment = `nori-m6-${unique}`;
const build = `m6-${unique}`;
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
  const name = users.join("\n").match(/script:(\w+)/)?.[1];
  const call = scripts[name]?.(outputs, users) ?? { name: "complete", args: { answer: "No script.", evidence: [], unmet: [] } };
  reply(res, 200, {
    status: "completed", usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2, cost: 0.0001 },
    output: [{ type: "function_call", call_id: `call_${crypto.randomUUID()}`, name: call.name, arguments: JSON.stringify(call.args) }],
  });
}

// Scripted outer model: "task: ..." dispatches, "approve <id> <question>" and "decline ..."
// decide. A turn without a response format is a task event, which returns plain text.
const outer = { eventContexts: [] };
async function outerHandler(req, res) {
  const request = await body(req);
  const text = JSON.stringify(request.messages);
  const last = request.messages.findLast((message) => message.role === "user")?.content ?? "";
  let content = "Noted.";
  if (request.response_format) {
    const actions = [];
    const decide = String(last).match(/\b(approve|decline) (\S+) (\S+)/);
    if (outer.forceApprove) actions.push({ name: "approve", arguments: outer.forceApprove });
    else if (String(last).includes("task:")) actions.push({ name: "dispatch", arguments: { request: String(last) } });
    else if (decide) actions.push({ name: decide[1], arguments: { run_id: decide[2], question_id: decide[3] } });
    content = JSON.stringify({ actions, text: actions.length ? "" : "Noted." });
  } else {
    outer.eventContexts.push(text);
    // A task event's turn must have no way to approve.
    expect(request.response_format).toBeUndefined();
  }
  reply(res, 200, {
    id: "chat", model: "gpt-6.1-sol", service_tier: "default",
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, prompt_tokens_details: { cached_tokens: 0 }, completion_tokens_details: { reasoning_tokens: 0 } },
    choices: [{ message: { role: "assistant", content }, finish_reason: "stop" }],
  });
}

function serve(handler) {
  return new Promise((done) => {
    const server = createServer((req, res) => handler(req, res).catch(() => res.destroy()));
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
  const child = spawn(resolve(directory, name), [], { cwd: core, env: childEnv, stdio: process.env.NORI_M6_LOGS ? "inherit" : "ignore" });
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
const lunch = { title: "Lunch with Jamie", start: future(24), end: future(25), description: "", private: "false" };
const createThenFinish = (args = lunch) => (outputs) => outputs.length ? finish(`Outcome: ${outputs.at(-1).outcome}`, outputs, outputs.at(-1).outcome === "uncertain" ? ["The write could not be confirmed."] : []) : { name: "calendar_create", args };

beforeAll(async () => {
  if (!live) return;
  directory = mkdtempSync(resolve(tmpdir(), "nori-m6-"));
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
beforeEach(() => { if (live) resetGoogle(); });

journey("an approved create runs once, reports its evidence, and no model call runs while parked", async () => {
  scripts.create = createThenFinish();
  const id = await dispatch("create");
  const run = await awaiting(id);
  expect(run.question.text).toContain("Lunch with Jamie");
  expect(run.question.text).toContain(account);
  expect(google.writes).toHaveLength(0);
  const calls = inner.calls;
  await delay(1_500);
  expect(inner.calls).toBe(calls);
  expect(writes(await operations(id))[0].outcome).toBe("prepared");

  await command("approve", id, run.question);
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(done.result).toContain("confirmed");
  expect(google.writes).toHaveLength(1);
  expect(google.writes[0].query).toContain("sendUpdates=none");
  expect(google.writes[0].event.attendees).toBeUndefined();
  expect(writes(await operations(id))[0].outcome).toBe("confirmed");
  // The turn that reports the end carries the action record and has no way to approve.
  await until(() => outer.eventContexts.join(""), (text) => text.includes("Lunch with Jamie"), "outer record of the action");
}, 120_000);

journey("a repeated approve and a stale question can't send twice", async () => {
  scripts.double = createThenFinish();
  const id = await dispatch("double");
  const run = await awaiting(id);
  const commandId = crypto.randomUUID();
  const first = { id: commandId, run_id: id, kind: "approve", question_id: run.question.id };
  await post("command", first, 202);
  await post("command", first, 202); // The same delivery again is a replay.
  await post("command", { ...first, id: crypto.randomUUID() }, 409); // A second click finds the question resolved.
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(1);
  await post("command", { ...first, id: crypto.randomUUID() }, 409);
  expect(google.writes).toHaveLength(1);
}, 120_000);

journey("a decline sends nothing, and the same action is refused if proposed again", async () => {
  let proposals = 0;
  scripts.decline = (outputs) => {
    if (proposals++ < 2) return { name: "calendar_create", args: lunch };
    return finish("Declined.", outputs);
  };
  const id = await dispatch("decline");
  const run = await awaiting(id);
  await command("decline", id, run.question);
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(0);
  expect(writes(await operations(id)).map((op) => op.outcome)).toEqual(["declined", "failed"]);
}, 120_000);

journey("a correction voids the approval, even one already sent, and only the corrected action runs", async () => {
  const later = { ...lunch, start: future(30), end: future(31) };
  scripts.correct = (outputs, users) => {
    const corrected = users.some((text) => text.includes("move it later"));
    if (corrected && !outputs.some((item) => item.outcome === "confirmed")) return { name: "calendar_create", args: later };
    if (outputs.some((item) => item.outcome === "confirmed")) return finish("Done.", outputs);
    return { name: "calendar_create", args: lunch };
  };
  const id = await dispatch("correct");
  const first = await awaiting(id);
  // The worker is down, so the approve and the correction both wait for the next sync.
  await stop(worker);
  await command("approve", id, first.question);
  await command("steer", id, undefined, "move it later");
  await startWorker();
  const second = await until(() => task(id), (run) => run?.question?.kind === "approval" && run.question.id !== first.question.id && !run.question.resolved_by, "second proposal");
  expect(google.writes).toHaveLength(0);
  await post("command", { id: crypto.randomUUID(), run_id: id, kind: "approve", question_id: first.question.id }, 409);
  await command("approve", id, second.question);
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes.map((item) => item.event.start.dateTime)).toEqual([new Date(later.start).toISOString().replace(/\.\d+Z$/, "Z")]);
  expect(writes(await operations(id)).map((op) => op.outcome)).toEqual(["voided", "confirmed"]);
}, 150_000);

journey("a lost response is checked and not repeated, and a later task finds the event", async () => {
  google.fault.dropAfterCommit = true;
  scripts.lost = createThenFinish();
  const id = await dispatch("lost");
  await command("approve", id, (await awaiting(id)).question);
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(1);
  expect(google.events.size).toBe(1);
  expect(writes(await operations(id))[0].outcome).toBe("confirmed");

  google.fault = {};
  scripts.again = createThenFinish();
  const repeat = await dispatch("again");
  const repeated = await finished(repeat);
  expect(repeated.status, repeated.error).toBe("completed");
  expect(repeated.question).toBeUndefined();
  expect(google.writes).toHaveLength(1);
  expect(google.events.size).toBe(1);
}, 150_000);

journey("a worker killed after the provider commits checks and doesn't send again", async () => {
  google.fault.holdAfterCommit = true;
  scripts.crashAfter = createThenFinish();
  const id = await dispatch("crashAfter");
  await command("approve", id, (await awaiting(id)).question);
  await until(() => google.writes.length, (count) => count === 1, "write in flight");
  await killWorker();
  await startWorker();
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(1);
  expect(google.events.size).toBe(1);
  expect(writes(await operations(id))[0].outcome).toBe("confirmed");
}, 150_000);

journey("a worker killed before the provider commits reports the write as not done", async () => {
  google.fault.holdBeforeCommit = true;
  scripts.crashBefore = createThenFinish();
  const id = await dispatch("crashBefore");
  await command("approve", id, (await awaiting(id)).question);
  await until(() => google.writes.length, (count) => count === 1, "write in flight");
  await killWorker();
  await startWorker();
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(1);
  expect(google.events.size).toBe(0);
  expect(writes(await operations(id))[0].outcome).toBe("failed");
  expect(done.result).toContain("failed");
}, 150_000);

journey("a provider that can't be read leaves the write uncertain, and the task asks and ends incomplete", async () => {
  Object.assign(google.fault, { dropAfterCommit: true, readError: true });
  scripts.uncertain = createThenFinish();
  const id = await dispatch("uncertain");
  await command("approve", id, (await awaiting(id)).question);
  const asked = await awaiting(id, "input");
  expect(asked.question.text).toMatch(/uncertain/i);
  await command("answer", id, asked.question, "Leave it.");
  const done = await finished(id);
  expect(done.status).toBe("failed");
  expect(done.error).toMatch(/unmet/i);
  expect(google.writes).toHaveLength(1);
  expect(writes(await operations(id))[0].outcome).toBe("uncertain");
}, 150_000);

journey("Stop while parked sends nothing", async () => {
  scripts.stopParked = createThenFinish();
  const id = await dispatch("stopParked");
  await awaiting(id);
  await command("stop", id);
  const done = await finished(id);
  expect(done.status).toBe("cancelled");
  expect(google.writes).toHaveLength(0);
  expect(writes(await operations(id))[0].outcome).toBe("prepared");
}, 120_000);

journey("Stop during a write lets it finish, and the record shows it", async () => {
  google.fault.holdAfterCommit = true;
  scripts.stopWrite = createThenFinish();
  const id = await dispatch("stopWrite");
  await command("approve", id, (await awaiting(id)).question);
  await until(() => google.writes.length, (count) => count === 1, "write in flight");
  await command("stop", id);
  await delay(2_500);
  for (const go of google.release.splice(0)) go();
  const done = await finished(id);
  expect(done.status).toBe("cancelled");
  expect(google.events.size).toBe(1);
  expect(writes(await operations(id))[0].outcome).toBe("confirmed");
  await until(() => outer.eventContexts.join(""), (text) => text.includes(id) && text.includes("confirmed"), "stopped task's record in the outer context");
}, 120_000);

journey("an approval survives a restart while parked and still runs once", async () => {
  scripts.restart = createThenFinish();
  const id = await dispatch("restart");
  const run = await awaiting(id);
  await killWorker();
  await command("approve", id, run.question);
  await startWorker();
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(1);
}, 150_000);

journey("an approval accepted before a pause runs once after the pause ends", async () => {
  scripts.paused = createThenFinish();
  const id = await dispatch("paused");
  const run = await awaiting(id);
  await stop(worker);
  await command("approve", id, run.question);
  const pause = (paused) => fetch(`${documents}/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: paused } } }) });
  await pause(true);
  worker = start("nori-worker");
  await until(() => worker.exitCode, (code) => code !== null, "paused worker exit");
  expect(google.writes).toHaveLength(0);
  await pause(false);
  await startWorker();
  await post("resume");
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(1);
}, 150_000);

journey("a pause during preparation leaves no open record, and the task can still finish", async () => {
  google.fault.holdRead = true;
  scripts.pausedPrepare = (outputs) => outputs.length ? finish("Stopped before asking.", outputs) : { name: "calendar_create", args: lunch };
  const id = await dispatch("pausedPrepare");
  await until(() => google.reads, (count) => count > 0, "prepare's read");
  const pause = (paused) => fetch(`${documents}/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: paused } } }) });
  await pause(true);
  await until(async () => writes(await operations(id))[0]?.outcome, (outcome) => outcome === "failed", "closed record");
  await stop(worker);
  await pause(false);
  google.fault = {};
  await startWorker();
  await post("resume");
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(0);
}, 150_000);

journey("an unrelated message can't carry a chat approval, whatever the model returns", async () => {
  scripts.gate = createThenFinish();
  const id = await dispatch("gate");
  const run = await awaiting(id);
  outer.forceApprove = { run_id: id, question_id: run.question.id };
  await post("turn", { id: crypto.randomUUID(), message: "what's the weather like" }, 202);
  await until(session, (now) => !now.conversation.pending && !now.conversation.processing, "turn to settle");
  outer.forceApprove = null;
  const current = await task(id);
  expect(current.question?.resolved_by).toBeUndefined();
  expect(google.writes).toHaveLength(0);
  await command("stop", id);
  await finished(id);
}, 120_000);

journey("a sent email beyond the checked window is uncertain, never reported as not sent", async () => {
  google.fault.dropBeforeCommit = true;
  for (let i = 0; i < 10; i++) google.mail.push({ id: `old${i}`, labelIds: ["SENT"], payload: { headers: [{ name: "X-Nori-Action", value: `old${i}` }] } });
  scripts.window = (outputs) => outputs.length ? finish(`Outcome: ${outputs.at(-1).outcome}`, outputs, outputs.at(-1).outcome === "uncertain" ? ["Unknown."] : []) : { name: "email_send", args: { to: "sam@example.test", subject: "Window", body: "Hello" } };
  const id = await dispatch("window");
  await command("approve", id, (await awaiting(id)).question);
  const asked = await awaiting(id, "input");
  expect(writes(await operations(id))[0].outcome).toBe("uncertain");
  await command("answer", id, asked.question, "Leave it.");
  const done = await finished(id);
  expect(done.status).toBe("failed");
}, 150_000);

journey("a decline and a correction in one sync stay declined", async () => {
  let proposals = 0;
  scripts.declineSteer = (outputs) => proposals++ < 2 ? { name: "calendar_create", args: lunch } : finish("Done.", outputs);
  const id = await dispatch("declineSteer");
  const run = await awaiting(id);
  await stop(worker);
  await command("decline", id, run.question);
  await command("steer", id, undefined, "never mind the rest");
  await startWorker();
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(0);
  expect(writes(await operations(id)).map((op) => op.outcome)).toEqual(["declined", "failed"]);
}, 150_000);

journey("a chat approval comes through a user's turn and decides that exact question", async () => {
  scripts.chat = createThenFinish();
  const id = await dispatch("chat");
  const run = await awaiting(id);
  await post("turn", { id: crypto.randomUUID(), message: `approve ${id} ${run.question.id}` }, 202);
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(1);
}, 120_000);

journey("a move keeps the event's length, carries the version the user saw, and fails if the event changed", async () => {
  const start = new Date(future(48)), end = new Date(start.getTime() + 90 * 60_000);
  const event = { id: "dentist", summary: "Dentist", status: "confirmed", etag: `"v1"`, organizer: { self: true }, start: { dateTime: start.toISOString() }, end: { dateTime: end.toISOString() } };
  google.events.set("dentist", event);
  const target = future(96);
  scripts.move = (outputs) => outputs.length ? finish(`Outcome: ${outputs.at(-1).outcome}`, outputs) : { name: "calendar_move", args: { event_id: "dentist", start: target, end: "" } };
  const id = await dispatch("move");
  const run = await awaiting(id);
  expect(run.question.text).toContain("Dentist");
  await command("approve", id, run.question);
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes[0].ifMatch).toBe(`"v1"`);
  expect(google.writes[0].query).toContain("sendUpdates=none");
  expect(new Date(event.end.dateTime) - new Date(event.start.dateTime)).toBe(90 * 60_000);

  scripts.moveStale = (outputs) => outputs.length ? finish(`Outcome: ${outputs.at(-1).outcome}`, outputs) : { name: "calendar_move", args: { event_id: "dentist", start: future(120), end: "" } };
  const stale = await dispatch("moveStale");
  const parked = await awaiting(stale);
  event.etag = `"changed"`;
  await command("approve", stale, parked.question);
  await finished(stale);
  expect(writes(await operations(stale))[0].outcome).toBe("failed");
  expect(new Date(event.start.dateTime).toISOString().slice(0, 13)).toBe(target.slice(0, 13));
}, 180_000);

journey("a move is refused on an event with other attendees, without asking", async () => {
  const at = future(48);
  google.events.set("meeting", { id: "meeting", summary: "Team", status: "confirmed", etag: `"v1"`, start: { dateTime: at }, end: { dateTime: future(49) }, attendees: [{ self: true }, { email: "other@example.test" }] });
  scripts.team = (outputs) => outputs.length ? finish("Can't move it.", outputs) : { name: "calendar_move", args: { event_id: "meeting", start: future(72), end: "" } };
  const id = await dispatch("team");
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.writes).toHaveLength(0);
  expect(writes(await operations(id))[0].outcome).toBe("failed");
}, 120_000);

journey("a token without write access is reported, and doesn't block a later write", async () => {
  google.fault.status = 403;
  scripts.scope = (outputs) => outputs.some((item) => item.outcome === "confirmed") || outputs.length > 1 ? finish("Done.", outputs) : { name: "calendar_create", args: outputs.length ? { ...lunch, title: "Second" } : lunch };
  const id = await dispatch("scope");
  await command("approve", id, (await awaiting(id)).question);
  const second = await until(() => task(id), (run) => run?.question?.kind === "approval" && run.question.text.includes("Second"), "later proposal");
  expect(writes(await operations(id))[0].outcome).toBe("failed");
  google.fault = {};
  await command("approve", id, second.question);
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(writes(await operations(id)).map((op) => op.outcome)).toEqual(["failed", "confirmed"]);
}, 150_000);

journey("an email is sent once after approval, and a lost response finds it in sent mail", async () => {
  google.fault.dropAfterCommit = true;
  const mail = { to: "sam@example.test", subject: "Running late", body: "I'll be 15 minutes late." };
  scripts.email = (outputs) => outputs.length ? finish(`Outcome: ${outputs.at(-1).outcome}`, outputs) : { name: "email_send", args: mail };
  const id = await dispatch("email");
  const run = await awaiting(id);
  expect(run.question.text).toContain("15 minutes");
  expect(run.question.text).toContain("sam@example.test");
  await command("approve", id, run.question);
  const done = await finished(id);
  expect(done.status, done.error).toBe("completed");
  expect(google.mail).toHaveLength(1);
  expect(writes(await operations(id))[0].outcome).toBe("confirmed");

  google.fault = {};
  scripts.emailAgain = scripts.email;
  const repeat = await dispatch("emailAgain");
  const repeated = await finished(repeat);
  expect(repeated.status, repeated.error).toBe("completed");
  expect(google.mail).toHaveLength(1);
}, 150_000);

journey("a closed workflow with a write in flight says the write may have happened", async () => {
  google.fault.holdAfterCommit = true;
  scripts.closed = createThenFinish();
  const id = await dispatch("closed");
  await command("approve", id, (await awaiting(id)).question);
  await until(() => google.writes.length, (count) => count === 1, "write in flight");
  await killWorker();
  cli("workflow", "terminate", "--workflow-id", `nori-task-${id}`, "--reason", "Milestone 6 recovery check");
  await startWorker();
  const done = await finished(id);
  expect(done.status).toBe("failed");
  expect(done.error).toMatch(/may have happened/);
  expect(writes(await operations(id))[0].outcome).toBe("attempted");
}, 150_000);
