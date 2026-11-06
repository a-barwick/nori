import { afterAll, beforeAll, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Unpaid: the proactive calendar check through the real API, Firestore emulator and Temporal, with a scripted outer
// model, a loopback Google Calendar and a loopback Routes API, on the real clock.
// NORI_M11_E2E=1 FIRESTORE_EMULATOR_HOST=127.0.0.1:8095 TEMPORAL_ADDRESS=127.0.0.1:7245 bun test core/e2e/proactive-calendar.test.js
const live = process.env.NORI_M11_E2E === "1";
const journey = (name, run, timeout = 120_000) => (live ? test : test.skip)(name, run, timeout);
const core = resolve(import.meta.dir, "..");
const unique = crypto.randomUUID().slice(0, 8);
const emulator = process.env.FIRESTORE_EMULATOR_HOST;
const temporalAddress = process.env.TEMPORAL_ADDRESS;
const database = `nori-m11-${unique}`;
const deployment = `nori-m11-${unique}`;
const build = `m11-${unique}`;
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

// Scripted outer model. A planning request is the one whose instructions describe the calendar check; it answers with
// the time plan.at() gives. User turns reply with outer.text after outer.userDelay.
const outer = { plans: [], users: 0, userDelay: 0, text: "Noted." };
const plan = { at: () => new Date(Date.now() + 2000) };
async function outerHandler(req, res) {
  const request = await body(req);
  let content = "Noted.";
  if (String(request.messages?.[0]?.content).includes("scheduled calendar check found a travel conflict")) {
    outer.plans.push(request);
    content = JSON.stringify({ text: "Heads up: your two events are too close to drive between. Want me to look for another time?", deliver_at: plan.at().toISOString(), timing: "scripted" });
  } else if (request.response_format) {
    outer.users++;
    await delay(outer.userDelay);
    content = JSON.stringify({ actions: [], text: outer.text });
  }
  reply(res, 200, {
    id: "chat", model: "gpt-6.1-sol", service_tier: "default",
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, prompt_tokens_details: { cached_tokens: 0 }, completion_tokens_details: { reasoning_tokens: 0 } },
    choices: [{ message: { role: "assistant", content }, finish_reason: "stop" }],
  });
}

// Scripted Terra: a prep task reads nothing and finishes with its answer.
async function modelHandler(req, res) {
  await body(req);
  const usage = { input_tokens: 1, output_tokens: 1, total_tokens: 2, cost: 0.0001 };
  reply(res, 200, { status: "completed", usage, output: [{ type: "function_call", call_id: `call_${crypto.randomUUID()}`, name: "complete", arguments: JSON.stringify({ answer: "Friday at 3 looks open.", evidence: [], unmet: [] }) }] });
}

// Loopback Google Calendar with one primary calendar. tokenFail makes the refresh token refused, as an expired one is.
const google = { events: [], tokenFail: false, reads: 0 };
async function googleHandler(req, res) {
  const url = new URL(req.url, "http://fake");
  if (url.pathname === "/token") {
    if (google.tokenFail) return reply(res, 400, { error: "invalid_grant", error_description: "Token has been expired or revoked." });
    return reply(res, 200, { access_token: "fake", token_type: "Bearer", expires_in: 3600 });
  }
  if (url.pathname === "/calendar/v3/users/me/calendarList") return reply(res, 200, { items: [{ id: "owner@example.test", summary: "Austin", primary: true, timeZone: "America/Los_Angeles", accessRole: "owner" }] });
  if (url.pathname.startsWith("/calendar/v3/calendars/")) {
    google.reads++;
    return reply(res, 200, { items: google.events.map((e) => ({ id: e.id, summary: e.title, location: e.location, status: "confirmed", start: { dateTime: e.start.toISOString() }, end: { dateTime: e.end.toISOString() } })) });
  }
  reply(res, 404);
}

// Loopback Routes API: 35 minutes between any two different places, after routes.wait.
const routes = { calls: 0, wait: 0 };
async function routesHandler(req, res) {
  const request = await body(req);
  routes.calls++;
  await delay(routes.wait);
  if (!request.origin?.address || !request.destination?.address || req.headers["x-goog-api-key"] !== "fake-routes") return reply(res, 400, {});
  reply(res, 200, { routes: [{ duration: "2100s" }] });
}

function serve(handler) {
  return new Promise((done) => {
    const server = createServer((req, res) => handler(req, res).catch((error) => { console.error(error); res.destroy(); }));
    server.listen(0, "127.0.0.1", () => { servers.push(server); done(`http://127.0.0.1:${server.address().port}`); });
  });
}
function cli(...args) {
  const result = spawnSync("temporal", ["--address", temporalAddress, "--namespace", process.env.TEMPORAL_NAMESPACE ?? "default", "--output", "json", ...args], { encoding: "utf8", timeout: 20_000 });
  if (result.status !== 0) throw new Error(`Temporal ${args.slice(0, 2).join(" ")} failed: ${result.stderr}`);
  return result.stdout.trim() ? JSON.parse(result.stdout) : {};
}
function start(name) {
  const childEnv = { ...env };
  if (name === "nori-worker") delete childEnv.PORT;
  const child = spawn(resolve(directory, name), [], { cwd: core, env: childEnv, stdio: process.env.NORI_M11_LOGS ? "inherit" : "ignore" });
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
const check = async (manual = true) => {
  const result = await post("proactive", { manual });
  if (result.status !== 200) throw new Error(`check: HTTP ${result.status}: ${JSON.stringify(result.body)}`);
  return result.body;
};

// Firestore REST reads of what the journeys check.
const field = (value) => {
  if (value.mapValue) return Object.fromEntries(Object.entries(value.mapValue.fields ?? {}).map(([key, inner]) => [key, field(inner)]));
  if (value.arrayValue) return (value.arrayValue.values ?? []).map(field);
  return value.stringValue ?? value.integerValue ?? value.timestampValue ?? value.booleanValue ?? value.doubleValue ?? null;
};
async function collection(name) {
  const data = await (await fetch(`${owner}/${name}?pageSize=300`)).json();
  return (data.documents ?? []).map((doc) => Object.fromEntries(Object.entries(doc.fields).map(([key, value]) => [key, field(value)])));
}
const offers = () => collection("proactive_offers");
const offerMessages = async () => (await snapshot()).messages.filter((m) => m.parts?.some((p) => p.type === "offer"));
async function resetDay() {
  for (const day of await collection("proactive_days")) await fetch(`${owner}/proactive_days/${day.day}`, { method: "DELETE" });
}
async function setPaused(paused) {
  const response = await fetch(`${documents}/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: paused } } }) });
  if (!response.ok) throw new Error("control document could not be set");
}

// A chain of timed events, each 15 minutes after the last at a different place, starting hours from now.
function chain(count, hoursAhead, tag) {
  const base = Date.now() + hoursAhead * 3_600_000;
  return Array.from({ length: count }, (_, i) => ({
    id: `${tag}-${i}`, title: `${tag} event ${i}`, location: `${100 + i} ${tag} St, Oakland, CA`,
    start: new Date(base + i * 75 * 60_000), end: new Date(base + i * 75 * 60_000 + 60 * 60_000),
  }));
}

beforeAll(async () => {
  if (!live) return;
  directory = mkdtempSync(resolve(tmpdir(), "nori-m11-"));
  for (const name of ["nori-local", "nori-worker"]) {
    const built = spawnSync("go", ["build", "-o", resolve(directory, name), `./cmd/${name}`], { cwd: core, encoding: "utf8", timeout: 180_000 });
    if (built.status !== 0) throw new Error(`Build ${name} failed: ${built.stderr}`);
  }
  writeFileSync(resolve(directory, "empty.env"), "");
  const [modelURL, outerURL, googleURL, routesURL] = await Promise.all([serve(modelHandler), serve(outerHandler), serve(googleHandler), serve(routesHandler)]);
  const port = 20000 + Math.floor(Math.random() * 20000);
  address = `http://127.0.0.1:${port}`;
  env = {
    PATH: process.env.PATH, HOME: process.env.HOME, NORI_ENV_FILE: resolve(directory, "empty.env"), NORI_PROJECT_ID: "bot-111549", FIRESTORE_DATABASE_ID: database,
    FIRESTORE_EMULATOR_HOST: emulator, NORI_LOCAL_ARTIFACT_DIR: resolve(directory, "artifacts"), PORT: String(port), TEMPORAL_ADDRESS: temporalAddress, TEMPORAL_NAMESPACE: process.env.TEMPORAL_NAMESPACE ?? "default", TEMPORAL_LOCAL: "true",
    TEMPORAL_TASK_QUEUE: deployment, TEMPORAL_DEPLOYMENT_NAME: deployment, TEMPORAL_BUILD_ID: build, COMPUTER_ZONE: "invalid-e2e-zone", COMPUTER_INSTANCE: "no-computer-e2e", NORI_EXECUTION_MODE: "temporal",
    OPENAI_API_KEY: "fake", JEV_API_KEY: "fake", OPENROUTER_API_KEY: "fake", NORI_E2E_MODEL_URL: modelURL, NORI_E2E_OPENAI_URL: outerURL,
    NORI_E2E_GOOGLE_URL: googleURL, GOOGLE_CALENDAR_CLIENT_ID: "fake", GOOGLE_CALENDAR_CLIENT_SECRET: "fake", GOOGLE_CALENDAR_REFRESH_TOKEN: "fake", GOOGLE_CALENDAR_ACCOUNT: "owner@example.test",
    NORI_E2E_ROUTES_URL: routesURL, GOOGLE_ROUTES_API_KEY: "fake-routes",
  };
  await setPaused(false);
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

journey("a day with nothing to flag makes no model call and no message", async () => {
  const start = new Date(Date.now() + 5 * 3_600_000);
  google.events = [
    { id: "same-a", title: "Standup", location: "1 Main St, Oakland", start, end: new Date(+start + 30 * 60_000) },
    { id: "same-b", title: "Review", location: "1 main st,  oakland", start: new Date(+start + 35 * 60_000), end: new Date(+start + 60 * 60_000) },
    { id: "virtual", title: "Call", location: "https://zoom.us/j/1", start: new Date(+start + 65 * 60_000), end: new Date(+start + 90 * 60_000) },
    { id: "nowhere", title: "Lunch", location: "", start: new Date(+start + 95 * 60_000), end: new Date(+start + 120 * 60_000) },
  ];
  const before = { plans: outer.plans.length, routes: routes.calls, messages: (await snapshot()).messages.length };
  const result = await check();
  expect(result).toMatchObject({ status: "done", ran: true, events: 4, found: 0, offers: [] });
  expect(outer.plans.length).toBe(before.plans);
  expect(routes.calls).toBe(before.routes);
  expect((await snapshot()).messages.length).toBe(before.messages);
});

journey("a conflict makes one offer with its reason, prepared by a quiet read-only task, and Accept sends a yes", async () => {
  google.events = chain(2, 6, "dentist");
  const result = await check();
  expect(result).toMatchObject({ status: "done", found: 1 });
  const id = result.offers[0];
  const message = await until(offerMessages, (list) => list.some((m) => m.id === `turn-${id}-assistant`), "the offer message");
  const shown = message.find((m) => m.id === `turn-${id}-assistant`);
  expect(shown.parts.find((p) => p.type === "offer")).toMatchObject({ id, status: "open" });
  const saved = (await offers()).find((o) => o.id === id);
  expect(saved.status).toBe("delivered");
  expect(saved.finding.drive_minutes).toBe("35");
  expect(saved.finding.gap_minutes).toBe("15");
  // The prep task can only read, and neither its question nor its end reaches the conversation.
  const runs = await collection("runs");
  const prep = runs.find((run) => run.id === saved.prep_run_id);
  expect(prep).toMatchObject({ offer: id, read_only: true, budget_usd: 0.05 });
  // Delivery stops a prep task that hasn't finished, since nothing waits on it after that.
  await until(async () => (await collection("runs")).find((run) => run.id === prep.id), (run) => run.status !== "running", "the prep task to end");
  await delay(1000);
  const inputs = await collection("conversation_inputs");
  expect(inputs.some((input) => input.event?.run_id === prep.id)).toBe(false);
  expect(runs.filter((run) => run.id !== prep.id && !run.offer).length).toBe(0);
  // The planning call saw the finding and nothing granting permission.
  expect(JSON.stringify(outer.plans.at(-1).messages)).toContain("dentist event 0");
  const users = outer.users;
  const accepted = await post("offer", { id, decision: "accepted" });
  expect(accepted.body).toMatchObject({ id, status: "accepted" });
  await until(snapshot, (now) => now.messages.some((m) => m.role === "user" && m.text === "Yes, please look for another time."), "the accepted yes");
  await settled();
  expect(outer.users).toBe(users + 1);
  const card = (await offerMessages()).find((m) => m.id === `turn-${id}-assistant`);
  expect(card.parts.find((p) => p.type === "offer").status).toBe("accepted");
  expect((await post("offer", { id, decision: "dismissed" })).status).toBe(409);
});

journey("two runs that find the same conflict make one offer, and a dismissal stops repeats", async () => {
  await resetDay();
  await setPaused(true);
  google.events = chain(2, 8, "school");
  const plans = outer.plans.length;
  const first = await check();
  const id = first.offers[0];
  await until(offerMessages, (list) => list.some((m) => m.id === `turn-${id}-assistant`), "the offer message");
  const second = await check();
  expect(second.offers).toEqual([id]);
  expect(outer.plans.length).toBe(plans + 1);
  // Paused production plans the offer without a prep task, and says so.
  const saved = (await offers()).find((o) => o.id === id);
  expect(saved.prep_run_id ?? "").toBe("");
  expect(saved.text).toContain("haven't looked for other times");
  expect((await post("offer", { id, decision: "dismissed" })).body.status).toBe("dismissed");
  const messages = (await snapshot()).messages.length;
  await check();
  await delay(3000);
  expect((await snapshot()).messages.length).toBe(messages);
  expect((await offers()).find((o) => o.id === id).status).toBe("dismissed");
  // A moved event is a new conflict.
  google.events[1] = { ...google.events[1], start: new Date(+google.events[1].start + 5 * 60_000) };
  const moved = await check();
  expect(moved.offers[0]).not.toBe(id);
  await setPaused(false);
});

journey("an offer due mid-conversation waits for the reply", async () => {
  await resetDay();
  google.events = chain(2, 10, "gym");
  outer.userDelay = 5000;
  plan.at = () => new Date(Date.now() + 500);
  const turn = crypto.randomUUID();
  expect((await post("turn", { id: turn, message: "what's the weather saturday?" })).status).toBe(202);
  await until(snapshot, (now) => now.conversation.processing, "the reply to start");
  const result = await check();
  await settled();
  await until(offerMessages, (list) => list.some((m) => m.id === `turn-${result.offers[0]}-assistant`), "the offer message");
  const now = await snapshot();
  const userTurn = now.messages.find((m) => m.id === `${turn}-user`).turn_id;
  const answer = now.messages.find((m) => m.role === "assistant" && m.turn_id === userTurn);
  const offer = now.messages.find((m) => m.id === `turn-${result.offers[0]}-assistant`);
  expect(answer.text).toBe("Noted.");
  expect(Date.parse(offer.created_at)).toBeGreaterThan(Date.parse(answer.created_at));
  expect(now.messages.filter((m) => m.id === offer.id).length).toBe(1);
  outer.userDelay = 0;
  plan.at = () => new Date(Date.now() + 2000);
});

journey("the daily cap holds, and the planning call sees saved memories", async () => {
  await resetDay();
  const memory = crypto.randomUUID();
  const at = new Date().toISOString();
  await fetch(`${owner}/memories?documentId=${memory}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: {
    id: { stringValue: memory }, text: { stringValue: "Austin is usually up by 6am." }, about: { stringValue: "Austin" }, source_input_id: { stringValue: "seed" },
    source_text: { stringValue: "I'm usually up by 6" }, created_at: { timestampValue: at }, updated_at: { timestampValue: at } } }) });
  google.events = chain(6, 12, "busy");
  const result = await check();
  expect(result.found).toBe(5);
  expect(JSON.stringify(outer.plans.at(-1).messages)).toContain("usually up by 6am");
  const ids = new Set(result.offers);
  const settledOffers = await until(async () => (await offers()).filter((o) => ids.has(o.id)), (list) => list.every((o) => o.status !== "planned"), "every offer to be delivered or skipped");
  expect(settledOffers.filter((o) => o.status === "delivered").length).toBe(3);
  expect(settledOffers.filter((o) => o.status === "skipped").every((o) => o.skip_reason.includes("already delivered"))).toBe(true);
  // At most three prep tasks a day, each counted at its whole budget against the day's cap.
  expect((await collection("runs")).filter((run) => ids.has(run.offer)).length).toBe(3);
  const [day] = await collection("proactive_days");
  expect(Number(day.preps)).toBe(3);
  expect(Number(day.spent_usd)).toBeLessThanOrEqual(0.2);
  await fetch(`${owner}/memories/${memory}`, { method: "DELETE" });
});

journey("an expired calendar token ends the check with no offer", async () => {
  await resetDay();
  google.tokenFail = true;
  const plans = outer.plans.length;
  const messages = (await snapshot()).messages.length;
  const result = await check();
  expect(result.status).toBe("failed");
  expect(result.error).toContain("calendar");
  expect(outer.plans.length).toBe(plans);
  await delay(2000);
  expect((await snapshot()).messages.length).toBe(messages);
  google.tokenFail = false;
});

journey("a restart in the middle of a scheduled check lets a later call finish it once", async () => {
  await resetDay();
  google.events = chain(2, 14, "restart");
  routes.wait = 8000;
  const calls = routes.calls;
  const first = post("proactive", { manual: false }).catch((error) => ({ error: String(error) }));
  await until(async () => routes.calls, (now) => now > calls, "the check to reach routes");
  await delay(500);
  await stop(api, "SIGKILL");
  await first;
  routes.wait = 0;
  await startAPI();
  expect((await post("proactive", { manual: false })).status).toBe(409);
  const result = await until(() => post("proactive", { manual: false }), (r) => r.status === 200, "the lease to lapse", 120_000);
  expect(result.body).toMatchObject({ status: "done", ran: true, found: 1 });
  const again = await check(false);
  expect(again.ran).toBe(false);
  const id = result.body.offers[0];
  await until(offerMessages, (list) => list.filter((m) => m.id === `turn-${id}-assistant`).length === 1, "one offer message");
}, 200_000);
