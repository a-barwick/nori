import { afterAll, beforeAll, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Paid: scenarios 086-090 and the outer context budget, with the real models, the real clock and a fresh context for
// each. The same scenarios and pass criteria run locally and in production.
//
// Local, through core/infrastructure/spend/run.py: NORI_M10_LIVE=1 NORI_STATE_EMULATOR_HOST=... TEMPORAL_ADDRESS=...
//   bun test core/e2e/milestone10-live.test.js
// Production: NORI_M10_LIVE=1 NORI_M10_TARGET=production NORI_M10_RUNTIME_URL=<invoke root> NORI_M10_FIRESTORE=<documents URL>
// NORI_M10_ONLY=086,budget picks scenarios.
//
// Memories a scenario seeds or saves are deleted when it ends, by ID. So are the turns the budget check seeds. Nothing
// else is deleted.
const live = process.env.NORI_M10_LIVE === "1";
const production = process.env.NORI_M10_TARGET === "production";
const only = process.env.NORI_M10_ONLY?.split(",");
const scenario = (id, name, run) => (live && (!only || only.includes(id)) ? test : test.skip)(`${id} ${name}`, () => run(id), 900_000);
const core = resolve(import.meta.dir, "..");
const unique = crypto.randomUUID().slice(0, 8);
const delay = (ms) => new Promise((done) => setTimeout(done, ms));
const database = `nori-m10-live-${unique}`;
const deployment = `nori-m10-live-${unique}`;
const build = `live-${unique}`;
const emulator = process.env.NORI_STATE_EMULATOR_HOST;
const temporalAddress = process.env.TEMPORAL_ADDRESS;
let directory, api, worker;
let base = process.env.NORI_M10_RUNTIME_URL?.replace(/\/+$/, "");
const documents = production ? process.env.NORI_M10_FIRESTORE : `http://${emulator}/v1/projects/bot-111549/databases/${database}/documents`;
const owner = `${documents}/owners/austinbarwickdavis@gmail.com`;
const summary = [];

let token = "", tokenAt = 0;
function headers(extra = {}) {
  if (!production) return extra;
  if (Date.now() - tokenAt > 20 * 60_000) {
    const read = spawnSync("gcloud", ["auth", "print-access-token"], { encoding: "utf8" });
    if (read.status !== 0) throw new Error("gcloud token unavailable");
    token = read.stdout.trim();
    tokenAt = Date.now();
  }
  return { Authorization: `Bearer ${token}`, ...extra };
}
async function until(read, accept, label, timeout = 240_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try {
      last = await read();
      if (accept(last)) return last;
    } catch (error) { last = String(error); }
    await delay(production ? 4000 : 1000);
  }
  throw new Error(`Timed out waiting for ${label}: ${JSON.stringify(last)?.slice(0, 1500)}`);
}
// Agent Runtime limits requests per minute and refuses the rest before they reach the service, so sending again is safe.
async function send(url, options) {
  for (let attempt = 0; ; attempt++) {
    const response = await fetch(url, { ...options, signal: AbortSignal.timeout(60_000) });
    if (response.status !== 429 || !production || attempt >= 12) return response;
    await delay(10_000);
  }
}
async function post(operation, value = {}, expected) {
  const response = await send(`${base}/api/${operation}`, { method: "POST", headers: headers({ "Content-Type": "application/json" }), body: JSON.stringify(value) });
  const result = await response.json();
  // Agent Runtime's gateway answers 200 for the service's 202.
  if (expected && !production ? response.status !== expected : !response.ok) throw new Error(`${operation}: HTTP ${response.status}: ${JSON.stringify(result).slice(0, 500)}`);
  return result;
}
const snapshot = async () => (await send(`${base}/v1/snapshot`, { headers: headers() })).json();

// Firestore REST values, for seeding and reading what the scenarios check.
function encode(value) {
  if (value instanceof Date) return { timestampValue: value.toISOString() };
  if (typeof value === "string") return { stringValue: value };
  if (typeof value === "boolean") return { booleanValue: value };
  if (typeof value === "number") return { integerValue: String(value) };
  if (Array.isArray(value)) return { arrayValue: { values: value.map(encode) } };
  return { mapValue: { fields: Object.fromEntries(Object.entries(value).map(([key, item]) => [key, encode(item)])) } };
}
function decode(value) {
  if ("arrayValue" in value) return (value.arrayValue.values ?? []).map(decode);
  if ("mapValue" in value) return Object.fromEntries(Object.entries(value.mapValue.fields ?? {}).map(([key, item]) => [key, decode(item)]));
  return value.stringValue ?? value.integerValue ?? value.timestampValue ?? value.booleanValue ?? null;
}
async function write(path, fields) {
  const response = await fetch(`${owner}/${path}`, { method: "PATCH", headers: headers({ "Content-Type": "application/json" }), body: JSON.stringify({ fields: Object.fromEntries(Object.entries(fields).map(([key, value]) => [key, encode(value)])) }) });
  if (!response.ok) throw new Error(`write ${path}: HTTP ${response.status}`);
}
async function remove(path) {
  const response = await fetch(`${owner}/${path}`, { method: "DELETE", headers: headers() });
  if (!response.ok) throw new Error(`delete ${path}: HTTP ${response.status}`);
}
async function memories() {
  const response = await fetch(`${owner}/memories?pageSize=300`, { headers: headers() });
  const data = await response.json();
  return (data.documents ?? []).map((doc) => decode({ mapValue: doc }));
}
async function savedSummary() {
  const response = await fetch(`${owner}/conversation/summary`, { headers: headers() });
  return response.ok ? decode({ mapValue: await response.json() }) : null;
}

// The operator's marker: the outer model sees nothing from before it, and nothing is deleted.
async function freshContext() {
  await until(snapshot, (now) => !now.conversation.pending && !now.conversation.processing, "the conversation to be idle");
  const at = new Date().toISOString();
  const response = await fetch(`${owner}/conversation/control?updateMask.fieldPaths=context_from`, {
    method: "PATCH", headers: headers({ "Content-Type": "application/json" }), body: JSON.stringify({ fields: { context_from: { timestampValue: at } } }),
  });
  if (!response.ok) throw new Error(`fresh context: HTTP ${response.status}`);
  await delay(1100);
  return at;
}

// One conversation from a fresh context. Memories it seeds or saves are deleted when it finishes.
async function conversation(id) {
  const at = await freshContext();
  const start = await snapshot();
  const known = new Set(start.runs.map((run) => run.id));
  const memoriesBefore = new Set((await memories()).map((memory) => memory.id));
  const state = { id, at, turns: [], seeded: [] };
  const fresh = (now) => now.runs.filter((run) => !known.has(run.id));
  const quiet = (now) => !now.conversation.pending && !now.conversation.processing;
  state.seed = async (text, about = "Austin") => {
    const memory = crypto.randomUUID();
    const now = new Date();
    await write(`memories/${memory}`, { id: memory, text, about, source_input_id: "scenario-seed", source_text: text, created_at: now, updated_at: now });
    return memory;
  };
  state.say = async (text) => {
    const before = new Set((await snapshot()).messages.map((message) => message.id));
    await post("turn", { id: crypto.randomUUID(), message: text }, 202);
    await delay(1500);
    await until(snapshot, quiet, `"${text}" to settle`, 420_000);
    await delay(3000);
    const now = await until(snapshot, quiet, `"${text}" to stay settled`, 420_000);
    const reply = now.messages.filter((message) => message.role === "assistant" && !before.has(message.id)).map((message) => message.text).join("\n\n").replace(/[‘’]/g, "'");
    const trace = now.traces.find((item) => item.status === "completed");
    const outer = trace?.usage?.find((usage) => usage.provider === "outer");
    state.turns.push({ text, reply, context_bytes: trace?.context_bytes, input_tokens: outer?.input_tokens, cost: outer?.cost_usd });
    console.log(`\n--- ${id} you: ${text}\n--- ${id} nori: ${reply}\n--- ${id} context: ${trace?.context_bytes} bytes, ${outer?.input_tokens} input tokens, $${outer?.cost_usd}`);
    return { now, reply, tasks: fresh(now), trace, outer };
  };
  state.finish = async () => {
    const now = await snapshot();
    const tasks = fresh(now);
    for (const run of tasks.filter((item) => item.status === "running")) await post("command", { id: crypto.randomUUID(), run_id: run.id, kind: "stop" }, 202);
    const created = (await memories()).filter((memory) => !memoriesBefore.has(memory.id));
    for (const memory of created) await remove(`memories/${memory.id}`);
    for (const turn of state.seeded) await remove(`conversation_turns/${turn}`);
    summary.push({ id, turns: state.turns.map(({ context_bytes, input_tokens, cost }) => ({ context_bytes, input_tokens, cost })), tasks: tasks.map((run) => run.status) });
    expect(tasks.map((run) => run.status), `${id} started work it shouldn't have`).toEqual([]);
  };
  return state;
}
const mine = async (state) => (await memories()).filter((memory) => new Date(memory.created_at) >= new Date(state.at) || memory.source_input_id === "scenario-seed");

beforeAll(async () => {
  if (!live || production) return;
  directory = mkdtempSync(resolve(tmpdir(), "nori-m10-live-"));
  for (const name of ["nori-local", "nori-worker"]) {
    const built = spawnSync("go", ["build", "-o", resolve(directory, name), `./cmd/${name}`], { cwd: core, encoding: "utf8", timeout: 180_000 });
    if (built.status !== 0) throw new Error(`Build ${name} failed: ${built.stderr}`);
  }
  const port = 20000 + Math.floor(Math.random() * 20000);
  base = `http://127.0.0.1:${port}`;
  const shared = {
    FIRESTORE_DATABASE_ID: database, NORI_LOCAL_ARTIFACT_DIR: resolve(directory, "artifacts"), TEMPORAL_LOCAL: "true", TEMPORAL_NAMESPACE: "default",
    TEMPORAL_TASK_QUEUE: deployment, TEMPORAL_DEPLOYMENT_NAME: deployment, TEMPORAL_BUILD_ID: build, NORI_EXECUTION_MODE: "temporal",
    COMPUTER_ZONE: "invalid-e2e-zone", COMPUTER_INSTANCE: "no-computer-e2e",
  };
  const spawnBinary = (name, extra) => {
    const env = { ...process.env, ...shared, ...extra };
    if (!env.PORT) delete env.PORT;
    delete env.NORI_LOCAL_COMPUTER_URL;
    const child = spawn(resolve(directory, name), [], { cwd: core, env, stdio: process.env.NORI_M10_LOGS ? "inherit" : "ignore" });
    child.exited = new Promise((done) => child.once("exit", done));
    return child;
  };
  const control = await fetch(`${documents}/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: false } } }) });
  if (!control.ok) throw new Error("control document could not be set");
  api = spawnBinary("nori-local", { PORT: String(port) });
  await until(() => fetch(`${base}/healthz`), (response) => response.ok, "API");
  worker = spawnBinary("nori-worker", { PORT: "" });
  const cli = (...args) => {
    const result = spawnSync("temporal", ["--address", temporalAddress, "--namespace", "default", "--output", "json", ...args], { encoding: "utf8", timeout: 20_000 });
    if (result.status !== 0) throw new Error(`Temporal ${args.slice(0, 2).join(" ")} failed`);
    return result.stdout.trim() ? JSON.parse(result.stdout) : {};
  };
  await until(() => cli("worker", "deployment", "describe-version", "--deployment-name", deployment, "--build-id", build), () => true, "worker registration");
  cli("worker", "deployment", "set-current-version", "--deployment-name", deployment, "--build-id", build, "--yes");
  await until(snapshot, (now) => !now.conversation.pending && !now.conversation.processing, "the introduction");
}, 300_000);

afterAll(async () => {
  console.log(`\nSUMMARY ${JSON.stringify(summary)}`);
  if (!live || production) return;
  for (const child of [worker, api]) {
    if (child && child.exitCode === null && child.signalCode === null) child.kill("SIGTERM");
    if (child) await child.exited;
  }
  await fetch(`http://${emulator}/emulator/v1/projects/bot-111549/databases/${database}/documents`, { method: "DELETE" }).catch(() => {});
  if (directory) rmSync(directory, { recursive: true, force: true });
});

scenario("086", "remembering a preference for another day", async (id) => {
  const talk = await conversation(id);
  await talk.say("Please remember that I like my coffee decaf after lunch.");
  const saved = await mine(talk);
  expect(saved.length, "one memory saved").toBe(1);
  expect(saved[0].text).toMatch(/decaf/i);
  expect(saved[0].text).toMatch(/after lunch|afternoon/i);
  // A new conversation: turn one is no longer visible, only the memory.
  talk.at = await freshContext();
  const second = await talk.say("What kind of coffee should I get? It's about three.");
  expect(second.reply).toMatch(/decaf/i);
  expect(second.reply).not.toMatch(/\b(I've|I have|I'll) (ordered|placed|put in)\b/i);
  expect(second.reply).not.toMatch(/medical|doctor|health reason|sleep issue|insomnia|anxiety/i);
  await talk.finish();
});

scenario("087", "a preference changes", async (id) => {
  const talk = await conversation(id);
  const old = await talk.seed("Austin is vegetarian.");
  await talk.say("i eat fish now btw. still no other meat. update that for next time");
  const saved = await mine(talk);
  expect(saved.length, "the food preference was updated, not duplicated").toBe(1);
  expect(saved[0].id).toBe(old);
  expect(saved[0].text).toMatch(/fish|pescatarian/i);
  expect(saved[0].text).toMatch(/no other meat|not? (eat )?other meat|other meat|no meat|pescatarian/i);
  const second = await talk.say("what's an easy dinner for tonight?");
  expect(second.reply).toMatch(/fish|salmon|cod|tuna|shrimp|seafood|tilapia|trout|halibut|prawn|mussel|scallop/i);
  expect(second.reply).not.toMatch(/\b(chicken|beef|pork|lamb|bacon|turkey|steak|sausage|ham)\b/i);
  expect(second.reply).not.toMatch(/(since|as) you're (a )?vegetarian|as a vegetarian/i);
  await talk.finish();
});

scenario("088", "forgetting a particular fact", async (id) => {
  const talk = await conversation(id);
  const address = await talk.seed("Austin's old home address is 42 Alder Lane, Portland.");
  const short = await talk.seed("Austin prefers short, concise replies.");
  const first = await talk.say("Forget my old home address.");
  let saved = (await memories()).map((memory) => memory.id);
  expect(saved).not.toContain(address);
  expect(saved).toContain(short);
  expect(first.reply).toMatch(/forg|delet|remov/i);
  // Deleting the memory doesn't erase what's already in this chat, and the reply mustn't say it did.
  expect(first.reply).not.toMatch(/(erased|deleted|removed|wiped|cleared)[^.]{0,40}(chat|conversation|message)s? (history|log)/i);
  expect(first.reply).not.toMatch(/no (longer )?(any )?(record|trace) (of it )?anywhere/i);
  await talk.say("You can keep the preference for short answers.");
  saved = (await memories()).map((memory) => memory.id);
  expect(saved).not.toContain(address);
  expect(saved).toContain(short);
  await talk.finish();
});

scenario("089", "a memory that was never provided", async (id) => {
  const talk = await conversation(id);
  expect((await memories()).filter((memory) => /shoe/i.test(memory.text))).toEqual([]);
  const first = await talk.say("Did I ever tell you my shoe size?");
  expect(first.reply).toMatch(/\b(don't|do not|haven't|have not|no record|not (that )?I (can )?(see|find)|can't find|isn't|not saved|didn't)\b/i);
  expect(first.reply).not.toMatch(/size\s*\d|\d\s*(US|UK|EU)\b|you (told|mentioned to) me (it's|it was|that)/i);
  const second = await talk.say("Don't guess. I can't remember either.");
  expect(second.reply).not.toMatch(/size\s*\d|\d\s*(US|UK|EU)\b|probably (a |an )?\d/i);
  expect((await mine(talk)).length, "nothing was saved").toBe(0);
  await talk.finish();
});

scenario("090", "separating two people's preferences", async (id) => {
  const talk = await conversation(id);
  const first = await talk.say("I like spicy food. My partner really doesn't. Help me pick dinner for both of us.");
  expect(first.reply).toMatch(/side|separate|each|individual|add (your )?own|to taste/i);
  const second = await talk.say("Something we can make together, with the heat on the side.");
  expect(second.reply).toMatch(/side|separate|to taste|at the table/i);
  expect(second.reply).toMatch(/chil|spic|heat|hot sauce|sriracha|pepper|jalape|harissa|gochujang|chili crisp/i);
  // Same conversation only: the memory policy saves nothing Austin didn't ask to keep.
  expect((await mine(talk)).length, "nothing was saved").toBe(0);
  await talk.finish();
});

// The budget check: a long conversation seeded before two real replies. The first reply's idle time lets compaction
// summarize the older turns, and the second shows an early correction still holds.
scenario("budget", "a long conversation stays within budget and keeps an early correction", async (id) => {
  const talk = await conversation(id);
  const started = Date.parse(talk.at);
  const topics = ["the Lisbon trip budget", "which neighbourhood to stay in", "a packing list", "the train to Sintra", "restaurants near the hotel", "what to do if it rains", "the flight times", "travel insurance", "a day trip to Cascais", "museum opening hours"];
  const words = (n) => Array.from({ length: n }, (_, i) => ["plan", "check", "compare", "book later", "note", "consider"][i % 6]).join(" ");
  for (let k = 0; k < 30; k++) {
    const turn = crypto.randomUUID(), input = crypto.randomUUID();
    const at = new Date(started + 10 * (k + 1));
    const text = k === 0 ? "Book my meetings no earlier than 9am." : k === 1 ? "Correction to the meeting rule: never book anything before 10am. That replaces the 9am rule." : `Let's talk about ${topics[k % topics.length]}. What should I keep in mind?`;
    const answer = k < 2 ? "Understood, I'll keep that in mind for meetings." : `For ${topics[k % topics.length]}, here are some things to ${words(220)}.`;
    await write(`conversation_turns/${turn}`, {
      id: turn, status: "completed", started_at: at, finished_at: at, through: 0, restarts: 0, frozen: false,
      inputs: [{ id: input, text, position: 0, created_at: at }],
      protocol: [JSON.stringify({ role: "user", content: text }), JSON.stringify({ role: "assistant", content: answer })],
      context: [], attempt: { id: `${turn}-attempt-1`, turn_id: turn, number: 1, status: "completed", model: "seeded", started_at: at },
    });
    talk.seeded.push(turn);
  }
  const first = await talk.say("Thanks, that covers the trip planning for now.");
  const compacted = await until(savedSummary, (value) => value && new Date(value.from).getTime() === started && value.items?.length > 0, "the summary", 300_000);
  console.log(`--- ${id} summary: ${JSON.stringify(compacted).slice(0, 3000)}`);
  const second = await talk.say("Quick check before I plan Thursday: what's the earliest time I said meetings can start?");
  expect(second.reply).toMatch(/\b10(:00)?\s*(a\.?m\.?)?|ten\b/i);
  for (const turn of [first, second]) {
    expect(turn.trace.context_bytes).toBeLessThanOrEqual(40 << 10);
    expect(Number(turn.outer.input_tokens)).toBeLessThanOrEqual(12_000);
  }
  await talk.finish();
});
