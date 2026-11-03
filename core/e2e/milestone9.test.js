import { afterAll, beforeAll, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Unpaid: schedules, monitors and closed-app email through the real API, Firestore emulator and Temporal, with a
// scripted outer model, a scripted task model and a loopback Gmail. Due times are moved by editing next_at.
// NORI_M9_E2E=1 FIRESTORE_EMULATOR_HOST=127.0.0.1:8095 TEMPORAL_ADDRESS=127.0.0.1:7245 bun test core/e2e/milestone9.test.js
const live = process.env.NORI_M9_E2E === "1";
const journey = (name, run, timeout = 120_000) => (live ? test : test.skip)(name, run, timeout);
const core = resolve(import.meta.dir, "..");
const unique = crypto.randomUUID().slice(0, 8);
const emulator = process.env.FIRESTORE_EMULATOR_HOST;
const temporalAddress = process.env.TEMPORAL_ADDRESS;
const database = `nori-m9-${unique}`;
const deployment = `nori-m9-${unique}`;
const build = `m9-${unique}`;
const documents = `http://${emulator}/v1/projects/bot-111549/databases/${database}/documents`;
const owner = `${documents}/owners/austinbarwickdavis@gmail.com`;
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
    await delay(200);
  }
  throw new Error(`Timed out waiting for ${label}: ${JSON.stringify(last)?.slice(0, 1500)}`);
}
async function body(req) {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  const data = Buffer.concat(chunks).toString();
  return data ? JSON.parse(data) : {};
}
function reply(res, status, value) {
  res.writeHead(status, { "Content-Type": "application/json" });
  res.end(JSON.stringify(value ?? {}));
}

// Austin's local date and time, as the outer model would resolve them from the advisory context.
const local = (at) => {
  const parts = Object.fromEntries(new Intl.DateTimeFormat("en-CA", { timeZone: "America/Los_Angeles", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23" })
    .formatToParts(at).map((p) => [p.type, p.value]));
  return { date: `${parts.year}-${parts.month}-${parts.day}`, time: `${parts.hour}:${parts.minute}` };
};
const rule = (repeat, at) => ({ repeat, start: local(at).date, time: local(at).time, days: [], month_day: null, every_hours: null, ends: null, holidays: null });

// Scripted outer model. User turns pick their action from the message; notification turns answer in plain text.
const outer = { requests: [] };
function advisory(request) {
  const message = request.messages.find((m) => String(m.content).startsWith("Advisory context for the current inputs: "));
  return JSON.parse(String(message.content).slice("Advisory context for the current inputs: ".length));
}
function userTurn(request) {
  const text = request.messages.filter((m) => m.role === "user").map((m) => String(m.content)).at(-1) ?? "";
  const later = new Date(Date.now() + 10 * 60_000);
  if (text.includes("remind me")) return { actions: [{ name: "schedule", arguments: { kind: "reminder", text: "Time to take the laundry out.", ...rule("once", later) } }], text: "" };
  if (text.includes("watch")) return { actions: [{ name: "schedule", arguments: { kind: "monitor", text: "script:monitor Watch the black 1 TB laptop; tell me only if it drops below $600 before tax. Stop once it does.", ...rule("daily", later), ends: local(new Date(Date.now() + 14 * 86_400_000)).date } }], text: "I'll check once a day." };
  if (text.includes("briefing")) return { actions: [{ name: "schedule", arguments: { kind: "task", text: "script:briefing Send Austin a short morning briefing.", ...rule("daily", later) } }], text: "" };
  if (text.includes("only once")) {
    const task = advisory(request).time_and_schedules.schedules.find((s) => s.kind === "task" && s.status === "active");
    return { actions: [{ name: "change_schedule", arguments: { schedule_id: task.id, change: "run_now", date: null, until: null } }], text: "Running it once now." };
  }
  return { actions: [], text: "Noted." };
}
async function outerHandler(req, res) {
  const request = await body(req);
  outer.requests.push(request);
  const content = request.response_format ? JSON.stringify(userTurn(request)) : "Here's what the scheduled work found.";
  reply(res, 200, {
    id: "chat", model: "gpt-6.1-sol", service_tier: "default",
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, prompt_tokens_details: { cached_tokens: 0 }, completion_tokens_details: { reasoning_tokens: 0 } },
    choices: [{ message: { role: "assistant", content }, finish_reason: "stop" }],
  });
}

// Scripted Terra: a monitor check answers with inner.monitor, anything else with a briefing.
const inner = { monitor: "NOTHING_NEW\nStill $629.", requests: [] };
async function modelHandler(req, res) {
  const request = await body(req);
  inner.requests.push(request);
  const text = JSON.stringify(request.input ?? "");
  const answer = text.includes("script:monitor") ? inner.monitor : "Clear skies and no meetings before ten.";
  reply(res, 200, { status: "completed", usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2, cost: 0.0001 },
    output: [{ type: "function_call", call_id: `call_${crypto.randomUUID()}`, name: "complete", arguments: JSON.stringify({ answer, evidence: [], unmet: [] }) }] });
}

// Loopback Google: a token, the Gmail profile and sent mail.
const gmail = { sent: [] };
async function googleHandler(req, res) {
  const url = new URL(req.url, "http://fake");
  if (url.pathname === "/token") return reply(res, 200, { access_token: "fake", token_type: "Bearer", expires_in: 3600 });
  if (url.pathname === "/gmail/v1/users/me/profile") return reply(res, 200, { emailAddress: "owner@example.test" });
  if (url.pathname === "/gmail/v1/users/me/messages/send") {
    const request = await body(req);
    const raw = Buffer.from(request.raw, "base64url").toString();
    gmail.sent.push(raw);
    return reply(res, 200, { id: `sent-${gmail.sent.length}` });
  }
  reply(res, 404);
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
  const childEnv = { ...env };
  if (name === "nori-worker") delete childEnv.PORT;
  const child = spawn(resolve(directory, name), [], { cwd: core, env: childEnv, stdio: process.env.NORI_M9_LOGS ? "inherit" : "ignore" });
  child.exited = new Promise((done) => child.once("exit", done));
  return child;
}
async function stop(child, signal = "SIGTERM") {
  if (child && child.exitCode === null && child.signalCode === null) child.kill(signal);
  if (child) await child.exited;
}
async function startAPI() {
  api = start("nori-local");
  await until(() => fetch(`${address}/healthz`), (response) => response.ok, "API");
}
async function post(operation, value = {}) {
  const response = await fetch(`${address}/api/${operation}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(value), signal: AbortSignal.timeout(200_000) });
  return { status: response.status, body: await response.json() };
}
const snapshot = async () => (await fetch(`${address}/v1/snapshot`)).json();
const settled = () => until(snapshot, (now) => !now.conversation.pending && !now.conversation.processing, "the conversation to settle", 90_000);
async function say(message) {
  expect((await post("turn", { id: crypto.randomUUID(), message })).status).toBe(202);
  await settled();
}

// Firestore REST reads and edits.
const field = (value) => {
  if (value.mapValue) return Object.fromEntries(Object.entries(value.mapValue.fields ?? {}).map(([key, inner]) => [key, field(inner)]));
  if (value.arrayValue) return (value.arrayValue.values ?? []).map(field);
  return value.stringValue ?? value.integerValue ?? value.timestampValue ?? value.booleanValue ?? value.doubleValue ?? null;
};
async function collection(name) {
  const data = await (await fetch(`${owner}/${name}?pageSize=300`)).json();
  return (data.documents ?? []).map((doc) => Object.fromEntries(Object.entries(doc.fields).map(([key, value]) => [key, field(value)])));
}
async function setTime(name, id, path, at) {
  const response = await fetch(`${owner}/${name}/${id}?updateMask.fieldPaths=${path}`, { method: "PATCH", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ fields: { [path]: { timestampValue: at.toISOString() } } }) });
  if (!response.ok) throw new Error(`${name}/${id} could not be edited`);
}
// A schedule is made due by moving its rule and next time a minute back, as if it had been set for then.
async function makeDue(id, ago = 5000) {
  const at = new Date(Date.now() - ago), was = local(new Date(+at - 60_000));
  const response = await fetch(`${owner}/schedules/${id}?updateMask.fieldPaths=next_at&updateMask.fieldPaths=rule.start&updateMask.fieldPaths=rule.time`, {
    method: "PATCH", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ fields: { next_at: { timestampValue: at.toISOString() }, rule: { mapValue: { fields: { start: { stringValue: was.date }, time: { stringValue: was.time } } } } } }) });
  if (!response.ok) throw new Error(`schedules/${id} could not be edited`);
}
const schedule = async (kind) => (await collection("schedules")).filter((s) => s.kind === kind).sort((a, b) => Date.parse(a.created_at) - Date.parse(b.created_at)).at(-1);
const notification = async (messageID) => (await collection("notifications")).find((n) => n.message_id === messageID);
const scheduledRuns = async (id) => (await collection("runs")).filter((run) => run.schedule === id);
const scheduleMessages = async (id) => (await snapshot()).messages.filter((m) => m.id.startsWith(`turn-schedule-${id}-`));
// Quiet hours that cover now, or a window hours away.
const quietWindow = (cover) => {
  const from = new Date(Date.now() + (cover ? -3_600_000 : 3 * 3_600_000)), to = new Date(+from + 2 * 3_600_000);
  return `${local(from).time}-${local(to).time}`;
};

beforeAll(async () => {
  if (!live) return;
  directory = mkdtempSync(resolve(tmpdir(), "nori-m9-"));
  for (const name of ["nori-local", "nori-worker"]) {
    const built = spawnSync("go", ["build", "-o", resolve(directory, name), `./cmd/${name}`], { cwd: core, encoding: "utf8", timeout: 180_000 });
    if (built.status !== 0) throw new Error(`Build ${name} failed: ${built.stderr}`);
  }
  writeFileSync(resolve(directory, "empty.env"), "");
  const [modelURL, outerURL, googleURL] = await Promise.all([serve(modelHandler), serve(outerHandler), serve(googleHandler)]);
  const port = 20000 + Math.floor(Math.random() * 20000);
  address = `http://127.0.0.1:${port}`;
  env = {
    PATH: process.env.PATH, HOME: process.env.HOME, NORI_ENV_FILE: resolve(directory, "empty.env"), NORI_PROJECT_ID: "bot-111549", FIRESTORE_DATABASE_ID: database,
    FIRESTORE_EMULATOR_HOST: emulator, NORI_LOCAL_ARTIFACT_DIR: resolve(directory, "artifacts"), PORT: String(port), TEMPORAL_ADDRESS: temporalAddress, TEMPORAL_NAMESPACE: "default", TEMPORAL_LOCAL: "true",
    TEMPORAL_TASK_QUEUE: deployment, TEMPORAL_DEPLOYMENT_NAME: deployment, TEMPORAL_BUILD_ID: build, COMPUTER_ZONE: "invalid-e2e-zone", COMPUTER_INSTANCE: "no-computer-e2e", NORI_EXECUTION_MODE: "temporal",
    OPENAI_API_KEY: "fake", JEV_API_KEY: "fake", OPENROUTER_API_KEY: "fake", NORI_E2E_MODEL_URL: modelURL, NORI_E2E_OPENAI_URL: outerURL,
    NORI_E2E_GOOGLE_URL: googleURL, GOOGLE_CALENDAR_CLIENT_ID: "fake", GOOGLE_CALENDAR_CLIENT_SECRET: "fake", GOOGLE_CALENDAR_REFRESH_TOKEN: "fake", GOOGLE_CALENDAR_ACCOUNT: "owner@example.test",
    NORI_QUIET_HOURS: quietWindow(false), NORI_APP_URL: "https://nori.example.test",
  };
  await fetch(`${documents}/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: false } } }) });
  await startAPI();
  worker = start("nori-worker");
  await until(() => cli("worker", "deployment", "describe-version", "--deployment-name", deployment, "--build-id", build), () => true, "worker registration");
  cli("worker", "deployment", "set-current-version", "--deployment-name", deployment, "--build-id", build, "--yes");
  await settled();
}, 300_000);

afterAll(async () => {
  if (!live) return;
  await stop(worker);
  await stop(api);
  for (const server of servers) server.close();
  await fetch(`http://${emulator}/emulator/v1/projects/bot-111549/databases/${database}/documents`, { method: "DELETE" }).catch(() => {});
});

journey("a reminder is saved at Austin's local time, delivered once when due, and emailed when no browser showed it", async () => {
  const asked = Date.now();
  await say("remind me to take the laundry out in 10 min");
  const saved = await schedule("reminder");
  expect(saved).toMatchObject({ status: "active", text: "Time to take the laundry out.", time_zone: "America/Los_Angeles", source_text: "remind me to take the laundry out in 10 min" });
  expect(Math.abs(Date.parse(saved.next_at) - (asked + 10 * 60_000))).toBeLessThan(2 * 60_000);
  // The model wrote no text, so the reply says when the first one is.
  expect((await snapshot()).messages.at(-1).text).toContain("Done. The first one is");
  // The advisory context gave the outer model the time and the saved schedule.
  const context = advisory(outer.requests.at(-1)).time_and_schedules;
  expect(context.now).toContain("America/Los_Angeles");
  await makeDue(saved.id);
  const [shown] = await until(() => scheduleMessages(saved.id), (list) => list.length === 1, "the reminder message");
  expect(shown.text).toBe("Time to take the laundry out.");
  await delay(3000);
  expect((await scheduleMessages(saved.id)).length).toBe(1);
  expect(await schedule("reminder")).toMatchObject({ status: "finished", runs: "1" });
  const pending = await notification(shown.id);
  expect(pending).toMatchObject({ status: "pending", urgent: true });
  await setTime("notifications", pending.id, "send_after", new Date(Date.now() - 1000));
  await until(() => notification(shown.id), (n) => n.status === "sent", "the email");
  expect(gmail.sent.length).toBe(1);
  expect(gmail.sent[0]).toContain("To: owner@example.test");
  expect(gmail.sent[0]).toContain("Nori: Time to take the laundry out.");
});

journey("a reminder a browser already showed sends no email", async () => {
  await say("remind me to take the laundry out in 10 min");
  const saved = await schedule("reminder");
  await makeDue(saved.id);
  const [shown] = await until(() => scheduleMessages(saved.id), (list) => list.length === 1, "the reminder message");
  expect((await post("seen", { message_id: shown.id })).status).toBe(200);
  const sent = gmail.sent.length;
  await setTime("notifications", `notify-${shown.id}`, "send_after", new Date(Date.now() - 1000));
  await until(() => notification(shown.id), (n) => n.status === "seen", "the notification to be dropped");
  expect(gmail.sent.length).toBe(sent);
});

journey("a monitor stays quiet until it finds something, alerts once and stops, and quiet hours hold the email", async () => {
  await say("watch the laptop price for two weeks");
  const saved = await schedule("monitor");
  expect(saved.rule.ends).toBeTruthy();
  const inputs = async () => (await collection("conversation_inputs")).filter((input) => input.event);
  const before = (await inputs()).length;
  inner.monitor = "NOTHING_NEW\nStill $629.";
  await makeDue(saved.id);
  const [quiet] = await until(() => scheduledRuns(saved.id), (runs) => runs.length === 1 && runs[0].status === "completed", "the quiet check");
  expect(quiet).toMatchObject({ quiet: true, read_only: true });
  expect(JSON.stringify(inner.requests.at(-1).input)).toContain("NOTHING_NEW when nothing he asked to hear about happened");
  await delay(3000);
  expect((await inputs()).length).toBe(before);
  expect(await schedule("monitor")).toMatchObject({ status: "active", runs: "1" });

  // Quiet hours that cover now hold back a result's email. A reminder Austin set would still go out.
  await stop(api);
  env.NORI_QUIET_HOURS = quietWindow(true);
  await startAPI();
  inner.monitor = "ALERT_AND_STOP\nThe black 1 TB model is $599 before tax.";
  await makeDue(saved.id);
  const runs = await until(() => scheduledRuns(saved.id), (list) => list.length === 2 && list.every((run) => run.status === "completed"), "the alerting check");
  const alert = runs.find((run) => run.id !== quiet.id);
  await until(inputs, (list) => list.some((input) => input.event.run_id === alert.id), "the alert to reach the conversation");
  await settled();
  const finished = await schedule("monitor");
  expect(finished.status).toBe("finished");
  expect(finished.reported.at(-1)).toContain("$599");
  const reply = (await snapshot()).messages.filter((m) => m.role === "assistant").at(-1);
  expect(reply.text).toBe("Here's what the scheduled work found.");
  const pending = await notification(reply.id);
  expect(pending).toMatchObject({ status: "pending" });
  const sent = gmail.sent.length;
  await setTime("notifications", pending.id, "send_after", new Date(Date.now() - 1000));
  const held = await until(() => notification(reply.id), (n) => Date.parse(n.send_after) > Date.now(), "the email to be held");
  expect(held.status).toBe("pending");
  expect(gmail.sent.length).toBe(sent);
  await stop(api);
  env.NORI_QUIET_HOURS = quietWindow(false);
  await startAPI();
});

journey("a missed scheduled task isn't run on its own, and run_now runs it once", async () => {
  await say("send me a morning briefing every day");
  const saved = await schedule("task");
  await makeDue(saved.id, 2 * 3_600_000);
  const [notice] = await until(() => scheduleMessages(saved.id), (list) => list.length === 1, "the missed notice");
  expect(notice.text).toContain("I didn't run your scheduled task");
  expect((await scheduledRuns(saved.id)).length).toBe(0);
  const missed = await schedule("task");
  expect(missed.missed.length).toBe(1);
  expect(Date.parse(missed.next_at)).toBeGreaterThan(Date.now());
  await say("do it now, but only once");
  const [run] = await until(() => scheduledRuns(saved.id), (runs) => runs.length === 1 && runs[0].status === "completed", "the catch-up run");
  expect(run.quiet ?? false).toBe(false);
  expect(run.request).toContain("Send Austin a short morning briefing.");
  await delay(4000);
  expect((await scheduledRuns(saved.id)).length).toBe(1);
  const after = await schedule("task");
  expect(after).toMatchObject({ status: "active", last_run_id: run.id });
  expect(after.missed[0].handled).toBe(true);
});
