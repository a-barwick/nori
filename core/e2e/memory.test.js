import { afterAll, beforeAll, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Unpaid: memory and the outer context budget through the real API, Firestore emulator and Temporal, with scripted
// models. NORI_M10_E2E=1 FIRESTORE_EMULATOR_HOST=127.0.0.1:8095 TEMPORAL_ADDRESS=127.0.0.1:7245 bun test core/e2e/memory.test.js
const live = process.env.NORI_M10_E2E === "1";
const journey = live ? test : test.skip;
const core = resolve(import.meta.dir, "..");
const unique = crypto.randomUUID().slice(0, 8);
const emulator = process.env.FIRESTORE_EMULATOR_HOST;
const temporalAddress = process.env.TEMPORAL_ADDRESS;
const database = `nori-m10-${unique}`;
const deployment = `nori-m10-${unique}`;
const build = `m10-${unique}`;
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
    await delay(100);
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

// Scripted outer model. onUser returns the actions for a user turn; the text is outer.text.
const outer = { calls: 0, requests: [], text: "Noted.", onUser: null, afterReply: null };
async function outerHandler(req, res) {
  const request = await body(req);
  outer.calls++;
  outer.requests.push(request);
  const last = request.messages.findLast((message) => message.role === "user")?.content ?? "";
  let content = "Noted.";
  if (request.response_format) {
    const actions = outer.onUser?.(String(last), request) ?? [];
    content = JSON.stringify({ actions, text: outer.text });
  }
  reply(res, 200, {
    id: "chat", model: "gpt-6.1-sol", service_tier: "default",
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, prompt_tokens_details: { cached_tokens: 0 }, completion_tokens_details: { reasoning_tokens: 0 } },
    choices: [{ message: { role: "assistant", content }, finish_reason: "stop" }],
  });
  outer.afterReply?.();
}

// Scripted Terra: summary requests go to summarize; task requests run scripts like milestone 8.
const terra = { summaries: [], summarize: null };
const scripts = {};
async function modelHandler(req, res) {
  const request = await body(req);
  const usage = { input_tokens: 1, output_tokens: 1, total_tokens: 2, cost: 0.0001 };
  const call = (name, args) => reply(res, 200, { status: "completed", usage, output: [{ type: "function_call", call_id: `call_${crypto.randomUUID()}`, name, arguments: JSON.stringify(args) }] });
  if (request.tools?.[0]?.name === "save_summary") {
    terra.summaries.push(request);
    const prompt = JSON.parse(request.input[0].content);
    return call("save_summary", terra.summarize(prompt));
  }
  const items = Array.isArray(request.input) ? request.input : [];
  const users = items.filter((item) => item.role === "user").map((item) => typeof item.content === "string" ? item.content : item.content?.map((part) => part.text).join("")).filter(Boolean);
  const outputs = items.filter((item) => item.type === "function_call_output").map((item) => JSON.parse(item.output));
  const name = [...users.join("\n").matchAll(/script:(\w+)/g)].at(-1)?.[1];
  const next = scripts[name]?.(outputs, users) ?? { name: "complete", args: { answer: "No script.", evidence: [], unmet: [] } };
  call(next.name, next.args);
}

// Fake Google: enough for an email task to park on its approval question.
const google = { sent: [] };
async function googleHandler(req, res) {
  const url = new URL(req.url, "http://fake");
  if (url.pathname === "/token") return reply(res, 200, { access_token: "fake", token_type: "Bearer", expires_in: 3600 });
  if (url.pathname === "/gmail/v1/users/me/messages/send") { google.sent.push(await body(req)); return reply(res, 200, { id: "m1" }); }
  if (url.pathname === "/gmail/v1/users/me/messages") return reply(res, 200, { messages: [] });
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
  const child = spawn(resolve(directory, name), [], { cwd: core, env: childEnv, stdio: process.env.NORI_M10_LOGS ? "inherit" : "ignore" });
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
async function post(operation, value = {}, expected) {
  const response = await fetch(`${address}/api/${operation}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(value), signal: AbortSignal.timeout(60_000) });
  const result = await response.json();
  if (expected ? response.status !== expected : !response.ok) throw new Error(`${operation}: HTTP ${response.status}: ${JSON.stringify(result)}`);
  return result;
}
const snapshot = async () => (await fetch(`${address}/v1/snapshot`)).json();
const settled = () => until(snapshot, (now) => !now.conversation.pending && !now.conversation.processing, "the conversation to settle", 90_000);
async function say(message) {
  const id = crypto.randomUUID();
  await post("turn", { id, message }, 202);
  const now = await until(snapshot, (value) => !value.conversation.pending && !value.conversation.processing && value.messages.some((m) => m.id === `${id}-user`), "a reply", 90_000);
  const turn = now.messages.find((m) => m.id === `${id}-user`).turn_id;
  return { id, now, reply: now.messages.findLast((m) => m.role === "assistant" && m.turn_id === turn) };
}
// Starts a fresh context the way the live scenarios do: everything before it is hidden from the outer model.
async function freshContext() {
  const at = new Date().toISOString();
  const response = await fetch(`${owner}/conversation/control?updateMask.fieldPaths=context_from`, {
    method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { context_from: { timestampValue: at } } }),
  });
  if (!response.ok) throw new Error(`fresh context: HTTP ${response.status}`);
  await delay(20);
}
const field = (value) => value.stringValue ?? value.integerValue ?? value.timestampValue ?? value.booleanValue;
async function memories() {
  const response = await fetch(`${owner}/memories`);
  const data = await response.json();
  return (data.documents ?? []).map((doc) => Object.fromEntries(Object.entries(doc.fields).map(([key, value]) => [key, field(value)])));
}
async function seedMemory(text, about = "Austin") {
  const id = crypto.randomUUID();
  const now = new Date().toISOString();
  const fields = { id: { stringValue: id }, text: { stringValue: text }, about: { stringValue: about }, source_input_id: { stringValue: "seed" }, source_text: { stringValue: text }, created_at: { timestampValue: now }, updated_at: { timestampValue: now } };
  const response = await fetch(`${owner}/memories?documentId=${id}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields }) });
  if (!response.ok) throw new Error(`seed memory: HTTP ${response.status}`);
  return id;
}
async function clearMemories() {
  for (const memory of await memories()) await fetch(`${owner}/memories/${memory.id}`, { method: "DELETE" });
}
const lastContext = () => JSON.stringify(outer.requests.findLast((request) => request.response_format).messages);
const memoryShown = () => outer.requests.findLast((request) => request.response_format).messages.find((m) => m.role === "system" && m.content.startsWith("Saved memories"))?.content;

beforeAll(async () => {
  if (!live) return;
  directory = mkdtempSync(resolve(tmpdir(), "nori-m10-"));
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
  };
  const control = await fetch(`${documents}/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: false } } }) });
  if (!control.ok) throw new Error("control document could not be set");
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

const remember = (text, replaces = null, about = "Austin") => ({ name: "remember", arguments: { text, about, replaces } });

journey("a saved memory reaches a fresh conversation", async () => {
  await clearMemories();
  outer.onUser = (last) => last.includes("Please remember") ? [remember("Austin likes his coffee decaf after lunch.")] : [];
  outer.text = "Got it.";
  await say("Please remember that I like my coffee decaf after lunch.");
  const saved = await memories();
  expect(saved.map((m) => m.text)).toEqual(["Austin likes his coffee decaf after lunch."]);
  expect(saved[0].source_text).toBe("Please remember that I like my coffee decaf after lunch.");
  await freshContext();
  await say("What kind of coffee should I get? It's about three.");
  expect(memoryShown()).toContain("decaf after lunch");
  expect(lastContext()).not.toContain("Please remember that I like my coffee");
}, 120_000);

journey("a newer preference replaces the older one it conflicts with", async () => {
  await clearMemories();
  const old = await seedMemory("Austin is vegetarian.");
  outer.onUser = (last) => last.includes("i eat fish now") ? [remember("Austin eats fish but no other meat.", old)] : [];
  await say("i eat fish now btw. still no other meat. update that for next time");
  const saved = await memories();
  expect(saved.map((m) => [m.id, m.text])).toEqual([[old, "Austin eats fish but no other meat."]]);
  await freshContext();
  await say("what's an easy dinner for tonight?");
  expect(memoryShown()).toContain("eats fish but no other meat");
  expect(memoryShown()).not.toContain("vegetarian");
}, 120_000);

journey("forgetting deletes only the named memory, and forgetting what was never saved changes nothing", async () => {
  await clearMemories();
  const address = await seedMemory("Austin's old home address is 12 Elm Street.");
  const short = await seedMemory("Austin prefers short answers.");
  outer.onUser = (last) => last.includes("Forget my old home address") ? [{ name: "forget", arguments: { memory_id: address } }] : [];
  outer.text = "I've deleted your old address from saved memory. Earlier messages in this chat still contain it.";
  const forgot = await say("Forget my old home address.");
  expect((await memories()).map((m) => m.id)).toEqual([short]);
  expect(forgot.reply.text).toContain("still contain it");
  // The real model can only name saved IDs; the runtime refuses anything else and says so.
  outer.onUser = (last) => last.includes("shoe size") ? [{ name: "forget", arguments: { memory_id: crypto.randomUUID() } }] : [];
  outer.text = "Done, I've forgotten your shoe size.";
  const never = await say("Forget my shoe size.");
  expect(never.reply.text).toContain("I couldn't change what I remember");
  expect(never.reply.text).not.toContain("forgotten your shoe size");
  expect((await memories()).map((m) => m.id)).toEqual([short]);
}, 120_000);

journey("a remembered permission stays a preference: approval still needs Austin's yes", async () => {
  await clearMemories();
  outer.onUser = (last) => last.includes("Remember you can") ? [remember("Austin says Nori can book without asking him first.")] : (last.includes("task:") ? [{ name: "dispatch", arguments: { request: last, for: null, question_id: null, attachments: [], revises: null } }] : []);
  outer.text = "I've saved that as your preference. Tasks will still ask before booking or sending, and I'll still need your yes.";
  await say("Remember you can book without asking me.");
  expect((await memories()).length).toBe(1);
  scripts.mail = (outputs) => outputs.at(-1)?.outcome ? { name: "complete", args: { answer: "Sent.", evidence: [], unmet: [] } } : { name: "email_send", args: { to: "tom@example.test", subject: "Booking", body: "Please book it." } };
  outer.text = "";
  const before = new Set((await snapshot()).runs.map((run) => run.id));
  await say("task: script:mail");
  const run = await until(async () => (await snapshot()).runs.find((r) => !before.has(r.id)), (r) => r?.question?.kind === "approval" && !r.question.resolved_by, "the approval question", 90_000);
  // A turn that doesn't say yes can't approve, whatever memory says.
  outer.onUser = (last) => last.includes("weather") ? [{ name: "approve", arguments: { run_id: run.id, question_id: run.question.id } }] : [];
  outer.text = "Approved, since you said I can book without asking.";
  const turn = await say("How's the weather looking?");
  expect(turn.reply.text).toContain("Nothing was approved");
  const after = (await snapshot()).runs.find((r) => r.id === run.id);
  expect(after.question.resolved_by ?? "").toBe("");
  expect(google.sent).toEqual([]);
  await post("command", { id: crypto.randomUUID(), run_id: run.id, kind: "stop" }, 202);
}, 180_000);

journey("a restart in the middle of a memory write saves it once", async () => {
  await clearMemories();
  outer.text = "Saved.";
  outer.onUser = (last) => last.includes("window seats") ? [remember("Austin prefers window seats.")] : [];
  // Kill the API as soon as the reply leaves the model, while it saves and publishes the change.
  outer.afterReply = () => { outer.afterReply = null; setTimeout(() => api.kill("SIGKILL"), Math.floor(Math.random() * 20)); };
  const callsBefore = outer.calls;
  const id = crypto.randomUUID();
  await post("turn", { id, message: "Remember I like window seats." }, 202);
  await until(() => api.exitCode !== null || api.signalCode !== null, Boolean, "the API to die");
  await api.exited;
  await startAPI();
  const now = await until(snapshot, (value) => value.messages.some((m) => m.id === `${id}-user`) && value.messages.some((m) => m.role === "assistant" && m.turn_id === value.messages.find((u) => u.id === `${id}-user`).turn_id), "the reply after restart", 120_000);
  const turn = now.messages.find((m) => m.id === `${id}-user`).turn_id;
  expect(now.messages.filter((m) => m.role === "assistant" && m.turn_id === turn).length).toBe(1);
  const saved = await memories();
  expect(saved.map((m) => m.text)).toEqual(["Austin prefers window seats."]);
  console.log(`restart journey: ${outer.calls - callsBefore} outer call(s)`);
}, 180_000);

journey("a long conversation stays within budget and an early correction survives compaction", async () => {
  await clearMemories();
  await freshContext();
  outer.onUser = () => [];
  outer.afterReply = null;
  // Long replies make each turn about 2 KB, like the real conversation.
  outer.text = "Here is a long and careful answer. ".repeat(50);
  let correctionID = null;
  terra.summarize = (prompt) => {
    const items = [...prompt.previous_summary.items];
    for (const turn of prompt.turns) {
      for (const message of turn.messages) {
        if (message.from === "user" && message.text.includes("never before 10am")) {
          correctionID = message.input_id;
          items.push({ kind: "correction", quote: "never book anything before 10am", input_id: message.input_id, note: "replaces the 9am rule" });
          // A paraphrase falls back to Austin's whole message; a quote from a stranger is dropped.
          items.push({ kind: "constraint", quote: "meetings only after ten", input_id: message.input_id, note: "" });
          items.push({ kind: "fact", quote: "made up", input_id: "not-austin", note: "" });
        }
      }
    }
    return { items, gist: `Talked through ${prompt.turns.length} more turns of planning.` };
  };
  await say("Book my meetings no earlier than 9am.");
  await say("Correction: never before 10am. That replaces the 9am rule — never book anything before 10am.");
  for (let i = 0; i < 38; i++) await say(`Planning question number ${i}: what should I think about next?`);
  const traces = (await snapshot()).traces.filter((trace) => trace.status === "completed");
  const sizes = traces.map((trace) => trace.context_bytes);
  console.log(`context bytes, newest first: ${sizes.join(" ")}`);
  expect(Math.max(...sizes)).toBeLessThanOrEqual(40 << 10);
  // Flat: across the newest twenty replies the input barely moves, while the conversation keeps growing.
  const newest = sizes.slice(0, 20);
  expect(Math.max(...newest) - Math.min(...newest)).toBeLessThanOrEqual(1 << 10);
  expect(terra.summaries.length).toBeGreaterThan(0);
  expect(terra.summaries[0].model).toBe("openai/gpt-6.1-terra");
  expect(terra.summaries[0].reasoning).toEqual({ effort: "medium" });
  const context = outer.requests.findLast((request) => request.response_format).messages;
  const summary = context.find((m) => m.role === "system" && m.content.startsWith("Summary of the earlier conversation"))?.content ?? "";
  expect(summary).toContain("never book anything before 10am");
  expect(summary).toContain("Correction: never before 10am.");
  expect(summary).not.toContain("made up");
  expect(correctionID).not.toBeNull();
  // The correction's own turn is no longer replayed word for word; only the summary carries it.
  expect(context.filter((m) => m.role === "user" && m.content.includes("never before 10am"))).toEqual([]);
}, 600_000);

journey("ordinary conversation survives retries and an API restart through the compatibility route", async () => {
  await freshContext();
  outer.onUser = () => [];
  outer.text = "Four.";
  const before = await settled();
  const innerBefore = terra.summaries.length;
  const input = { id: crypto.randomUUID(), message: "What is two plus two? Just answer without starting work." };
  await post("turn", input, 202);
  const after = await settled();
  const user = after.messages.find((message) => message.id === `${input.id}-user`);
  expect(user.text).toBe(input.message);
  const answer = after.messages.find((message) => message.role === "assistant" && message.turn_id === user.turn_id);
  expect(answer.text).toBe("Four.");
  expect(after.runs).toEqual(before.runs);
  expect(after.dispatches).toEqual(before.dispatches);
  expect(terra.summaries.length).toBe(innerBefore);
  const calls = outer.calls;
  const usage = after.usage;
  await post("turn", input, 202);
  const replay = await settled();
  expect(replay.messages).toEqual(after.messages);
  expect(replay.usage).toEqual(usage);
  expect(outer.calls).toBe(calls);
  await post("turn", { ...input, message: "Conflicting text." }, 409);
  await stop(api);
  await startAPI();
  await post("turn", input, 202);
  const reopened = await post("session");
  expect(reopened.messages).toEqual(after.messages);
  expect(reopened.runs).toEqual(before.runs);
  expect(reopened.usage).toEqual(usage);
  expect(outer.calls).toBe(calls);
  outer.text = "Noted.";
}, 120_000);
