import { afterAll, beforeAll, beforeEach, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Paid: scenarios 019, 020, 021, 027 and 094 with the real models and Google, against a calendar kept for testing.
// Run through core/infrastructure/spend/run.py with NORI_M6_LIVE=1, NORI_M6_LIVE_CALENDAR, NORI_M6_LIVE_EMAIL,
// NORI_STATE_EMULATOR_HOST, TEMPORAL_ADDRESS and NORI_ENV_FILE. Google traffic passes through a local relay,
// so scenario 094 can lose one real response.
const live = process.env.NORI_M6_LIVE === "1";
const journey = live ? test : test.skip;
const core = resolve(import.meta.dir, "..");
const unique = crypto.randomUUID().slice(0, 8);
const calendar = process.env.NORI_M6_LIVE_CALENDAR;
const email = process.env.NORI_M6_LIVE_EMAIL;
const emulator = process.env.NORI_STATE_EMULATOR_HOST;
const temporalAddress = process.env.TEMPORAL_ADDRESS;
const deployment = `nori-m6-live-${unique}`;
const build = `live-${unique}`;
const zone = "America/Los_Angeles";
const delay = (ms) => new Promise((done) => setTimeout(done, ms));
let directory, address, api, worker, relay, token;
const created = [];
const relayState = { dropNextInsert: false, inserts: 0 };

if (live && !/@group\.calendar\.google\.com$/.test(calendar || "")) throw new Error("NORI_M6_LIVE_CALENDAR must be a secondary calendar, never the primary one");

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
const onCalendar = async () => (await google("GET", `/events?singleEvents=true&timeMin=2026-11-01T00:00:00Z&timeMax=2026-12-01T00:00:00Z`)).items;
const instant = (value) => new Date(value).getTime();
async function clearCalendar() {
  for (const event of await onCalendar()) await google("DELETE", `/events/${event.id}`);
}

function start(name, extra) {
  const env = { ...process.env, ...extra };
  if (!env.PORT) delete env.PORT;
  const child = spawn(resolve(directory, name), [], { cwd: core, env, stdio: process.env.NORI_M6_LOGS ? "inherit" : "ignore" });
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
  directory = mkdtempSync(resolve(tmpdir(), "nori-m6-live-"));
  for (const name of ["nori-local", "nori-worker"]) {
    const built = spawnSync("go", ["build", "-o", resolve(directory, name), `./cmd/${name}`], { cwd: core, encoding: "utf8", timeout: 180_000 });
    if (built.status !== 0) throw new Error(`Build ${name} failed: ${built.stderr}`);
  }
  relay = createServer((req, res) => relayHandler(req, res).catch(() => res.destroy()));
  await new Promise((done) => relay.listen(0, "127.0.0.1", done));
  const relayURL = `http://127.0.0.1:${relay.address().port}`;
  const port = 20000 + Math.floor(Math.random() * 20000);
  address = `http://127.0.0.1:${port}`;
  const shared = {
    FIRESTORE_DATABASE_ID: `nori-m6-live-${unique}`, NORI_LOCAL_ARTIFACT_DIR: resolve(directory, "artifacts"), TEMPORAL_LOCAL: "true", TEMPORAL_NAMESPACE: "default",
    TEMPORAL_TASK_QUEUE: deployment, TEMPORAL_DEPLOYMENT_NAME: deployment, TEMPORAL_BUILD_ID: build, COMPUTER_ZONE: "invalid-e2e-zone", COMPUTER_INSTANCE: "no-computer-e2e", NORI_EXECUTION_MODE: "temporal",
    NORI_TIMEZONE: zone,
  };
  const control = await fetch(`http://${emulator}/v1/projects/bot-111549/databases/${shared.FIRESTORE_DATABASE_ID}/documents/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: false } } }) });
  if (!control.ok) throw new Error("control document could not be set");
  api = start("nori-local", { ...shared, PORT: String(port) });
  await until(() => fetch(`${address}/healthz`), (response) => response.ok, "API");
  // The scenarios' default clock is Friday November 6, 2026 at 09:00 Los Angeles.
  worker = start("nori-worker", { ...shared, PORT: "", NORI_E2E_GOOGLE_URL: relayURL, GOOGLE_CALENDAR_WRITE_ID: calendar, NORI_E2E_NOW: "2026-11-06T09:00:00-08:00" });
  await until(() => cli("worker", "deployment", "describe-version", "--deployment-name", deployment, "--build-id", build), () => true, "worker registration");
  cli("worker", "deployment", "set-current-version", "--deployment-name", deployment, "--build-id", build, "--yes");
  await clearCalendar();
}, 300_000);

afterAll(async () => {
  if (!live) return;
  await stop(worker);
  await stop(api);
  relay?.close();
  await clearCalendar().catch(() => {});
  if (directory) rmSync(directory, { recursive: true, force: true });
});

beforeEach(async () => { if (live) await clearCalendar(); });

journey("019: moves one appointment and doesn't claim the dentist agreed", async () => {
  await seed("Dentist appointment", "2026-11-09T09:30:00-08:00", "2026-11-09T10:30:00-08:00");
  const before = await known();
  await say("move my dentist thing to next friday");
  await say("2pm");
  await say("just change my calendar, i'll call them");
  const run = await parked(before);
  expect(run.question?.kind, JSON.stringify(run)).toBe("approval");
  expect(run.question.text).toMatch(/Nov 13/);
  const done = await approve(run);
  expect(done.status, done.error).toBe("completed");
  const [event] = (await onCalendar()).filter((item) => /dentist/i.test(item.summary));
  expect(instant(event.start.dateTime)).toBe(instant("2026-11-13T14:00:00-08:00"));
  expect(instant(event.end.dateTime)).toBe(instant("2026-11-13T15:00:00-08:00"));
  const text = await report("019");
  expect(text).not.toMatch(/dentist (has )?(accepted|confirmed|agreed)/i);
  await clearCalendar();
}, 600_000);

journey("020: creates a private hold, converting London time once, with no invitation", async () => {
  const before = await known();
  await say("Put a 30-minute call with Priya on my calendar next Tuesday at 4 p.m. London time. No invitation needed; it's just a hold.");
  const run = await parked(before);
  expect(run.question?.kind, JSON.stringify(run)).toBe("approval");
  expect(run.question.text).toMatch(/Nov 10/);
  expect(run.question.text).toMatch(/8:00 AM/);
  const done = await approve(run);
  expect(done.status, done.error).toBe("completed");
  const [event] = await onCalendar();
  expect(instant(event.start.dateTime)).toBe(instant("2026-11-10T08:00:00-08:00"));
  expect(instant(event.end.dateTime)).toBe(instant("2026-11-10T08:30:00-08:00"));
  expect(event.attendees).toBeUndefined();
  expect(event.visibility).toBe("private");
  expect(event.description ?? "").toMatch(/London/i);
  await report("020");
  await clearCalendar();
}, 600_000);

journey("021: asks which checkup, then moves only the vet one a week later", async () => {
  await seed("Checkup", "2026-11-09T10:00:00-08:00", "2026-11-09T10:45:00-08:00");
  const vet = await seed("Checkup", "2026-11-11T15:00:00-08:00", "2026-11-11T15:50:00-08:00");
  await google("PATCH", `/events/${vet.id}`, { location: "City Vet" });
  const before = await known();
  await say("Can you move the checkup to next week?");
  const asked = await parked(before);
  expect(asked.question?.kind, JSON.stringify(asked)).toBe("input");
  expect((await onCalendar()).every((item) => item.etag)).toBe(true);
  await say("The vet one. Same day and time, just the following week.");
  const run = await parked(before);
  expect(run.question?.kind, JSON.stringify(run)).toBe("approval");
  const done = await approve(run);
  expect(done.status, done.error).toBe("completed");
  const events = await onCalendar();
  const moved = events.find((item) => item.id === vet.id);
  expect(instant(moved.start.dateTime)).toBe(instant("2026-11-18T15:00:00-08:00"));
  expect(instant(moved.end.dateTime)).toBe(instant("2026-11-18T15:50:00-08:00"));
  expect(instant(events.find((item) => item.id !== vet.id).start.dateTime)).toBe(instant("2026-11-09T10:00:00-08:00"));
  await report("021");
  await clearCalendar();
}, 600_000);

journey("027: sends only the corrected email, once, after approval", async () => {
  const before = await known();
  const subjectMark = `Nori check ${unique}`;
  await say(`draft sam a note that i'll be 10 min late. sam's address is ${email}. use the subject "${subjectMark}"`);
  await say("say 15 actually");
  await say("send that");
  const run = await parked(before);
  expect(run.question?.kind, JSON.stringify(run)).toBe("approval");
  expect(run.question.text).toContain(email);
  expect(run.question.text).toMatch(/15/);
  expect(run.question.text).not.toMatch(/\b10\b/);
  const done = await approve(run);
  expect(done.status, done.error).toBe("completed");
  console.log(`027 subject: ${subjectMark}`);
  await report("027");
}, 600_000);

journey("094: a lost response ends with one event and an accurate explanation", async () => {
  const inserts = relayState.inserts;
  relayState.dropNextInsert = true;
  const before = await known();
  await say("Add lunch with Jamie tomorrow at noon for an hour.");
  const run = await parked(before);
  expect(run.question?.kind, JSON.stringify(run)).toBe("approval");
  const done = await approve(run);
  expect(relayState.dropNextInsert).toBe(false);
  expect(relayState.inserts).toBe(inserts + 1);
  console.log("094 first task:", done.status, done.result);
  await report("094 after the lost response");
  const after = await known();
  await say("That looked like it failed. Try again?");
  await until(session, (now) => now.runs.every((item) => terminal(item) || item.question), "follow-up to settle", 240_000);
  const repeat = (await session()).runs.find((item) => !after.has(item.id));
  if (repeat?.question?.kind === "approval") throw new Error(`A repeat asked to write again: ${repeat.question.text}`);
  const text = await report("094");
  const lunches = (await onCalendar()).filter((item) => /jamie/i.test(item.summary));
  expect(lunches).toHaveLength(1);
  expect(relayState.inserts).toBe(inserts + 1);
  expect(instant(lunches[0].start.dateTime)).toBe(instant("2026-11-07T12:00:00-08:00"));
  expect(text.length).toBeGreaterThan(0);
  await clearCalendar();
}, 600_000);

journey("a real stale move: Google refuses the version the user saw", async () => {
  const event = await seed("Haircut", "2026-11-12T11:00:00-08:00", "2026-11-12T11:30:00-08:00");
  const before = await known();
  await say("Move my haircut to Monday November 16 at 11am.");
  const run = await parked(before);
  expect(run.question?.kind, JSON.stringify(run)).toBe("approval");
  await google("PATCH", `/events/${event.id}`, { location: "Changed after the card appeared" });
  await post("command", { id: crypto.randomUUID(), run_id: run.id, kind: "approve", question_id: run.question.id }, 202);
  await until(session, (now) => { const current = now.runs.find((item) => item.id === run.id); return terminal(current) || (current.question && current.question.id !== run.question.id && current.incorporated === current.position); }, "the refused move", 240_000);
  expect(instant((await onCalendar()).find((item) => item.id === event.id).start.dateTime)).toBe(instant("2026-11-12T11:00:00-08:00"));
  await report("stale move");
  await post("cancel", { id: run.id }).catch(() => {});
  await clearCalendar();
}, 600_000);
