import { afterAll, beforeAll, beforeEach, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Paid: scenarios 107, 108 and 109 with the real models and Google, against a calendar kept for testing.
// Run through core/infrastructure/spend/run.py with NORI_M7_LIVE=1, NORI_M7_LIVE_CALENDAR, NORI_M7_LIVE_EMAIL,
// NORI_STATE_EMULATOR_HOST, TEMPORAL_ADDRESS and NORI_ENV_FILE. Google traffic passes through a local relay,
// so scenario 094 can lose one real response.
const live = process.env.NORI_M7_LIVE === "1";
const journey = live ? test : test.skip;
const core = resolve(import.meta.dir, "..");
const unique = crypto.randomUUID().slice(0, 8);
const calendar = process.env.NORI_M7_LIVE_CALENDAR;
const email = process.env.NORI_M7_LIVE_EMAIL;
const emulator = process.env.NORI_STATE_EMULATOR_HOST;
const temporalAddress = process.env.TEMPORAL_ADDRESS;
const deployment = `nori-m7-live-${unique}`;
const build = `live-${unique}`;
const zone = "America/Los_Angeles";
const delay = (ms) => new Promise((done) => setTimeout(done, ms));
let directory, address, api, worker, relay, guest, token;
const created = [];
const relayState = { dropNextInsert: false, inserts: 0 };

if (live && !/@group\.calendar\.google\.com$/.test(calendar || "")) throw new Error("NORI_M7_LIVE_CALENDAR must be a secondary calendar, never the primary one");

async function until(read, accept, label, timeout = 120_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try {
      last = await read();
      if (accept(last)) return last;
    } catch (error) { last = String(error); }
    await delay(500);
  }
  throw new Error(`Timed out waiting for ${label}: ${JSON.stringify(last)?.slice(0, 2000)}`);
}

// The relay forwards everything to Google, and can drop one insert's response after Google has committed it.
async function relayHandler(req, res) {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  const host = req.url === "/token" ? "https://oauth2.googleapis.com" : req.url.startsWith("/gmail/") ? "https://gmail.googleapis.com" : "https://www.googleapis.com";
  const headers = Object.fromEntries(["authorization", "content-type", "if-match"].filter((name) => req.headers[name]).map((name) => [name, req.headers[name]]));
  const upstream = await fetch(host + req.url, { method: req.method, headers, body: chunks.length ? Buffer.concat(chunks) : undefined });
  const body = Buffer.from(await upstream.arrayBuffer());
  const insert = req.method === "POST" && req.url.includes("/events");
  if (insert) relayState.inserts++;
  if (insert && relayState.dropNextInsert) {
    relayState.dropNextInsert = false;
    return res.destroy();
  }
  res.writeHead(upstream.status, { "Content-Type": upstream.headers.get("content-type") || "application/json" });
  res.end(body);
}

// No computer runs locally, so this stands in for one. It reads pages with a plain fetch and refuses shell commands.
async function guestHandler(req, res) {
  let data = "";
  for await (const chunk of req) data += chunk;
  const send = (status, value) => { res.writeHead(status, { "Content-Type": "application/json" }); res.end(JSON.stringify(value)); };
  if (req.url === "/healthz") return send(200, { ready: true, operation_receipts_v1: true, browser_task_tabs_v1: true, shell_jobs_v1: true });
  if (req.url === "/navigate") {
    const { url } = JSON.parse(data);
    try {
      const page = await fetch(url, { signal: AbortSignal.timeout(15_000), headers: { "User-Agent": "Mozilla/5.0" } });
      const text = (await page.text()).replace(/<(script|style)[\s\S]*?<\/\1>/gi, " ").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").slice(0, 4_000);
      return send(200, { url, title: "", text, status: page.status });
    } catch { return send(200, { url, title: "", text: "The page could not be read.", status: 0 }); }
  }
  if (req.url === "/exec") return send(200, { exit_code: 127, stdout: "", stderr: "No shell is available in this environment." });
  if (req.url === "/operation") return send(404, {});
  return send(200, { stopped: true, closed: true });
}

async function refreshToken() {
  const response = await fetch("https://oauth2.googleapis.com/token", { method: "POST", body: new URLSearchParams({ client_id: process.env.GOOGLE_CALENDAR_CLIENT_ID, client_secret: process.env.GOOGLE_CALENDAR_CLIENT_SECRET, refresh_token: process.env.GOOGLE_CALENDAR_REFRESH_TOKEN, grant_type: "refresh_token" }) });
  token = (await response.json()).access_token;
}
async function google(method, path, body, base = `https://www.googleapis.com/calendar/v3/calendars/${encodeURIComponent(calendar)}`) {
  const response = await fetch(base + path, { method, headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }, body: body ? JSON.stringify(body) : undefined });
  if (response.status === 204) return {};
  const value = await response.json();
  if (!response.ok) throw new Error(`Google ${method} ${path}: ${response.status}`);
  return value;
}
async function seed(title, start, end) {
  const event = await google("POST", "/events", { summary: title, start: { dateTime: start, timeZone: zone }, end: { dateTime: end, timeZone: zone } });
  created.push(event.id);
  return event;
}
const onCalendar = async () => {
  const start = new Date(Date.now() - 86_400_000).toISOString(), end = new Date(Date.now() + 30 * 86_400_000).toISOString();
  return (await google("GET", `/events?singleEvents=true&timeMin=${start}&timeMax=${end}`)).items;
};
const instant = (value) => new Date(value).getTime();
async function clearCalendar() {
  for (const event of await onCalendar()) await google("DELETE", `/events/${event.id}`);
}

function start(name, extra) {
  const env = { ...process.env, ...extra };
  if (!env.PORT) delete env.PORT;
  const child = spawn(resolve(directory, name), [], { cwd: core, env, stdio: process.env.NORI_M7_LOGS ? "inherit" : "ignore" });
  child.exited = new Promise((done) => child.once("exit", done));
  return child;
}
async function stop(child) {
  if (child && child.exitCode === null && child.signalCode === null) child.kill("SIGTERM");
  if (child) await child.exited;
}
function cli(...args) {
  const result = spawnSync("temporal", ["--address", temporalAddress, "--namespace", "default", "--output", "json", ...args], { encoding: "utf8", timeout: 20_000 });
  if (result.status !== 0) throw new Error(`Temporal ${args.slice(0, 2).join(" ")} failed`);
  return result.stdout.trim() ? JSON.parse(result.stdout) : {};
}
async function post(operation, value = {}, expected) {
  const response = await fetch(`${address}/api/${operation}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(value), signal: AbortSignal.timeout(20_000) });
  const result = await response.json();
  if (expected ? response.status !== expected : !response.ok) throw new Error(`${operation}: HTTP ${response.status}: ${JSON.stringify(result)}`);
  return result;
}
const session = () => post("session");
const settled = () => until(session, (value) => !value.conversation.pending && !value.conversation.processing, "conversation to settle");
const terminal = (run) => ["completed", "failed", "cancelled"].includes(run?.status);
const quiet = (run) => terminal(run) || (run?.question && !run.question.resolved_by && run.incorporated === run.position);
async function say(text) {
  await settled();
  await post("turn", { id: crypto.randomUUID(), message: text }, 202);
  await settled();
}
// The newest task, once it has parked on a question or ended.
async function parked(known) {
  const value = await until(session, (now) => { const run = now.runs.find((item) => !known.has(item.id)); return quiet(run); }, "task to park or end", 180_000);
  return value.runs.find((item) => !known.has(item.id));
}
async function approve(run) {
  await post("command", { id: crypto.randomUUID(), run_id: run.id, kind: "approve", question_id: run.question.id }, 202);
  return until(session, (now) => quiet(now.runs.find((item) => item.id === run.id)) && now.runs.find((item) => item.id === run.id).question?.id !== run.question.id, "task after approval", 180_000).then((now) => now.runs.find((item) => item.id === run.id));
}
const report = async (label) => {
  await settled();
  const { messages } = await session();
  console.log(`\n--- ${label}\n` + messages.slice(-4).map((message) => `${message.role}: ${message.text}`).join("\n"));
  return messages.at(-1)?.text ?? "";
};
const known = async () => new Set((await session()).runs.map((run) => run.id));

beforeAll(async () => {
  if (!live) return;
  // Only this suite's own token refresh reads these. The binaries load the same file themselves.
  for (const line of readFileSync(process.env.NORI_ENV_FILE, "utf8").split("\n")) {
    const found = line.match(/^(?:export )?(GOOGLE_CALENDAR_\w+)=(.*)$/);
    if (found) process.env[found[1]] = found[2].replace(/^["']|["']$/g, "");
  }
  await refreshToken();
  directory = mkdtempSync(resolve(tmpdir(), "nori-m7-live-"));
  for (const name of ["nori-local", "nori-worker"]) {
    const built = spawnSync("go", ["build", "-o", resolve(directory, name), `./cmd/${name}`], { cwd: core, encoding: "utf8", timeout: 180_000 });
    if (built.status !== 0) throw new Error(`Build ${name} failed: ${built.stderr}`);
  }
  relay = createServer((req, res) => relayHandler(req, res).catch(() => res.destroy()));
  await new Promise((done) => relay.listen(0, "127.0.0.1", done));
  const relayURL = `http://127.0.0.1:${relay.address().port}`;
  guest = createServer((req, res) => guestHandler(req, res).catch(() => res.destroy()));
  await new Promise((done) => guest.listen(0, "127.0.0.1", done));
  const port = 20000 + Math.floor(Math.random() * 20000);
  address = `http://127.0.0.1:${port}`;
  const shared = {
    FIRESTORE_DATABASE_ID: `nori-m7-live-${unique}`, NORI_LOCAL_ARTIFACT_DIR: resolve(directory, "artifacts"), TEMPORAL_LOCAL: "true", TEMPORAL_NAMESPACE: "default",
    TEMPORAL_TASK_QUEUE: deployment, TEMPORAL_DEPLOYMENT_NAME: deployment, TEMPORAL_BUILD_ID: build, COMPUTER_ZONE: "invalid-e2e-zone", COMPUTER_INSTANCE: "no-computer-e2e", NORI_EXECUTION_MODE: "temporal",
    NORI_TIMEZONE: zone,
  };
  const control = await fetch(`http://${emulator}/v1/projects/bot-111549/databases/${shared.FIRESTORE_DATABASE_ID}/documents/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: false } } }) });
  if (!control.ok) throw new Error("control document could not be set");
  api = start("nori-local", { ...shared, PORT: String(port) });
  await until(() => fetch(`${address}/healthz`), (response) => response.ok, "API");
  // These scenarios read real forecasts, so they run on the real date.
  worker = start("nori-worker", { ...shared, PORT: "", NORI_E2E_GOOGLE_URL: relayURL, NORI_LOCAL_COMPUTER_URL: `http://127.0.0.1:${guest.address().port}`, GOOGLE_CALENDAR_WRITE_ID: calendar });
  await until(() => cli("worker", "deployment", "describe-version", "--deployment-name", deployment, "--build-id", build), () => true, "worker registration");
  cli("worker", "deployment", "set-current-version", "--deployment-name", deployment, "--build-id", build, "--yes");
  await clearCalendar();
}, 300_000);

afterAll(async () => {
  if (!live) return;
  await stop(worker);
  await stop(api);
  relay?.close();
  guest?.close();
  await clearCalendar().catch(() => {});
  if (directory) rmSync(directory, { recursive: true, force: true });
});

beforeEach(async () => { if (live) await clearCalendar(); });

// The next Saturday and Friday in Los Angeles, as local dates with the zone's offset.
function upcoming(weekday, time) {
  const now = new Date();
  const day = new Date(now.getTime() + (((weekday - now.getUTCDay() + 7) % 7 || 7) * 86_400_000));
  const offset = new Intl.DateTimeFormat("en-US", { timeZone: zone, timeZoneName: "longOffset" }).formatToParts(day).find((part) => part.type === "timeZoneName").value.replace("GMT", "");
  return `${day.toISOString().slice(0, 10)}T${time}:00${offset}`;
}
const known7 = async () => new Set((await session()).runs.map((run) => run.id));
// The newest top-level task, waiting for nothing from its subtasks.
async function newTask(before) {
  const value = await until(session, (now) => now.runs.some((run) => !before.has(run.id) && !run.parent_id), "task to start", 120_000);
  return value.runs.find((run) => !before.has(run.id) && !run.parent_id).id;
}
const get = async (id) => (await session()).runs.find((run) => run.id === id);
const ended = (id) => until(() => get(id), terminal, `task ${id} to end`, 300_000);
const outerCalls = async () => (await session()).usage.filter((item) => item.provider === "outer" && item.request_status !== "denied").length;
const family = async (id) => { const { runs } = await session(); return runs.filter((run) => run.parent_id === id); };
function show(label, value) { console.log(`\n--- ${label}: ${JSON.stringify(value).slice(0, 1500)}`); }

journey("107: a lookup outside the task comes back as the parent's result, with no outer turn for delivery", async () => {
  await seed("Picnic at Lake Merritt, Oakland", upcoming(6, "14:00"), upcoming(6, "16:00"));
  const before = await known7();
  await say("Check my calendar and tell me if Saturday's picnic still makes sense. Keep that task to my calendar. If it needs anything else, have that done separately and give me one answer.");
  const id = await newTask(before);
  const done = await ended(id);
  const children = await family(id);
  show("107 parent", { status: done.status, result: done.result, error: done.error, spent: done.spent_usd, subtask: done.subtask_spent_usd });
  show("107 children", children.map((run) => ({ request: run.request, status: run.status, result: run.result, spent: run.spent_usd })));
  // A real forecast can leave something unconfirmed, and the stand-in computer has no shell. An honest partial answer
  // still shows the path worked, so this accepts one and the report says which it was.
  expect(terminal(done) && done.status !== "cancelled" && (done.status === "completed" || /^Incomplete/.test(done.error ?? "")), done.error).toBe(true);
  expect(done.result).toMatch(/\d+\s*°?F|forecast|weather/i);
  expect(children).toHaveLength(1);
  expect(terminal(children[0])).toBe(true);
  expect(done.subtask_spent_usd).toBeCloseTo(children[0].spent_usd, 6);
  await settled();
  // The user's turn, the work request and the parent's result. The subtask's end adds none.
  const usage = (await session()).usage.filter((item) => item.provider === "outer" && item.input_id !== "nori-introduction");
  expect(usage.length).toBeLessThanOrEqual(3);
  await report("107");
}, 900_000);

journey("108: the outer loop settles what it can from the conversation, asks about the rest, and the same parent finishes", async () => {
  await seed("Haircut", upcoming(6, "17:00"), upcoming(6, "18:00"));
  await seed("Dinner at the Okafors'", upcoming(6, "19:00"), upcoming(6, "21:00"));
  await say("The Okafors moved to 1200 Grand Ave in Oakland, by the way.");
  const before = await known7();
  await say("Can I get from my haircut to dinner on time on Saturday? Just use my calendar in that task, and get anything else some other way.");
  const id = await newTask(before);
  const seen = new Map();
  const replies = [];
  const done = await until(async () => {
    const value = await session();
    const run = value.runs.find((item) => item.id === id);
    if (run?.question) seen.set(run.question.id, { kind: run.question.kind, text: run.question.text.slice(0, 300), resolved: run.question.resolved_by });
    // Reply as the user would, only when Nori has asked and the conversation is quiet.
    const last = value.messages.at(-1);
    const open = run?.question && !run.question.resolved_by && run.incorporated === run.position;
    if (open && !value.conversation.pending && !value.conversation.processing && last?.role === "assistant" && replies.length < 4 && /\?/.test(last.text)) {
      const answer = /haircut|salon|barber/i.test(last.text) ? "It's at the Hair Bar on Piedmont Ave in Oakland." : "That's everything I know. Use your best estimate.";
      replies.push(answer);
      await say(answer);
    }
    return run;
  }, terminal, "parent to end", 400_000);
  const value = await session();
  const started = value.runs.filter((run) => !before.has(run.id));
  show("108", { status: done.status, result: done.result, error: done.error, questions: [...seen.values()], replies, tasks: started.map((run) => ({ parent: run.parent_id, request: run.request, status: run.status })) });
  expect(started.filter((run) => !run.parent_id)).toHaveLength(1);
  expect([...seen.values()].some((question) => question.kind === "work")).toBe(true);
  expect(terminal(done) && done.status !== "cancelled").toBe(true);
  await report("108");
}, 900_000);

const CAKE = "Order cake";
journey("109: a subtask's writes wait for approval, and Stop ends the whole family", async () => {
  // First run: approve each write.
  const startedAt = Date.now();
  const before = await known7();
  await say("See what's on Friday. Only read in that task, and separately add 'Order cake' at 3 p.m. and email me that it's on there.");
  const id = await newTask(before);
  const approvals = [];
  const done = await until(async () => {
    const value = await session();
    for (const run of value.runs.filter((item) => item.parent_id === id)) {
      if (run.question?.kind === "approval" && !run.question.resolved_by && run.incorporated === run.position && !approvals.includes(run.question.id)) {
        expect((await get(id)).status).toBe("running");
        approvals.push(run.question.id);
        show("109 approval", run.question.text);
        await post("command", { id: crypto.randomUUID(), run_id: run.id, kind: "approve", question_id: run.question.id }, 202);
      }
    }
    return value.runs.find((run) => run.id === id);
  }, terminal, "parent after approvals", 500_000);
  const children = await family(id);
  show("109 approve run", { status: done.status, result: done.result, error: done.error, approvals, children: children.map((run) => ({ request: run.request, status: run.status, spent: run.spent_usd })) });
  expect(approvals.length).toBeGreaterThanOrEqual(2);
  expect(children.every((run) => run.parent_id === id)).toBe(true);
  const events = (await onCalendar()).filter((item) => /cake/i.test(item.summary));
  expect(events).toHaveLength(1);
  // The metadata scope can't search, so read the newest sent mail's headers.
  const gmail = "https://gmail.googleapis.com/gmail/v1/users/me";
  const { messages = [] } = await google("GET", "/messages?labelIds=SENT&maxResults=5", undefined, gmail);
  const heads = await Promise.all(messages.map((item) => google("GET", `/messages/${item.id}?format=metadata&metadataHeaders=Subject&metadataHeaders=To&metadataHeaders=Date`, undefined, gmail)));
  const recent = heads.filter((item) => Number(item.internalDate) >= startedAt).map((item) => Object.fromEntries(item.payload.headers.map((header) => [header.name, header.value])));
  show("109 mail", recent);
  expect(recent).toHaveLength(1);
  await report("109 approve");
  await clearCalendar();

  // Second run: stop at the first approval.
  const second = await known7();
  await say("See what's on Friday. Only read in that task, and separately add 'Order cake' at 3 p.m. and email me that it's on there.");
  const parent = await newTask(second);
  await until(async () => (await family(parent)).some((run) => run.question?.kind === "approval" && !run.question.resolved_by), Boolean, "a subtask's approval", 300_000);
  await say("stop all of that");
  const stopped = await ended(parent);
  const stoppedChildren = await family(parent);
  for (const run of stoppedChildren) await ended(run.id);
  show("109 stop run", { status: stopped.status, children: (await family(parent)).map((run) => run.status) });
  expect(stopped.status).toBe("cancelled");
  expect((await family(parent)).every((run) => run.status === "cancelled" || run.status === "completed")).toBe(true);
  expect((await onCalendar()).filter((item) => /cake/i.test(item.summary))).toHaveLength(0);
  expect(Date.now()).toBeGreaterThan(startedAt);
  await report("109 stop");
}, 900_000);
