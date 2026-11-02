import { afterAll, beforeAll, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Unpaid: the real workflow, Firestore emulator and Temporal, with scripted models and a stand-in computer.
// NORI_M8_E2E=1 FIRESTORE_EMULATOR_HOST=127.0.0.1:8095 TEMPORAL_ADDRESS=127.0.0.1:7245 bun test core/e2e/milestone8.test.js
const live = process.env.NORI_M8_E2E === "1";
const journey = live ? test : test.skip;
const core = resolve(import.meta.dir, "..");
const unique = crypto.randomUUID().slice(0, 8);
const emulator = process.env.FIRESTORE_EMULATOR_HOST;
const temporalAddress = process.env.TEMPORAL_ADDRESS;
const database = `nori-m8-${unique}`;
const deployment = `nori-m8-${unique}`;
const build = `m8-${unique}`;
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

// Scripted inner model: each task's request names a script, and the script sees every tool result so far.
const scripts = {};
const inner = { calls: 0, requests: [] };
const citable = (outputs) => outputs.filter((item) => ["confirmed", "submitted", "failed", "declined"].includes(item.outcome)).map((item) => item.operation_id);
const finish = (answer, outputs, unmet = []) => ({ name: "complete", args: { answer, evidence: citable(outputs), unmet } });
async function modelHandler(req, res) {
  const request = await body(req);
  inner.calls++;
  inner.requests.push(request);
  const items = Array.isArray(request.input) ? request.input : [];
  const users = items.filter((item) => item.role === "user").map((item) => typeof item.content === "string" ? item.content : item.content?.map((part) => part.text).join("")).filter(Boolean);
  const outputs = items.filter((item) => item.type === "function_call_output").map((item) => JSON.parse(item.output));
  const name = [...users.join("\n").matchAll(/script:(\w+)/g)].at(-1)?.[1];
  const call = await scripts[name]?.(outputs, users) ?? { name: "complete", args: { answer: "No script.", evidence: [], unmet: [] } };
  reply(res, 200, {
    status: "completed", usage: { input_tokens: 1, output_tokens: 1, total_tokens: 2, cost: 0.0001 },
    output: [{ type: "function_call", call_id: `call_${crypto.randomUUID()}`, name: call.name, arguments: JSON.stringify(call.args) }],
  });
}

// Scripted outer model: "task: ..." dispatches. A turn with no action returns plain text.
const outer = { calls: 0, requests: [] };
async function outerHandler(req, res) {
  const request = await body(req);
  outer.calls++;
  outer.requests.push(request);
  const last = request.messages.findLast((message) => message.role === "user")?.content ?? "";
  const variants = request.response_format?.json_schema.schema.properties.actions.items.anyOf;
  let content = "Noted.";
  if (variants) {
    const actions = outer.onUser?.(String(last), request) ?? (String(last).includes("task:") ? [{ name: "dispatch", arguments: { request: String(last), for: null, question_id: null } }] : []);
    content = JSON.stringify({ actions, text: actions.length ? "" : "Noted." });
  }
  reply(res, 200, {
    id: "chat", model: "gpt-6.1-sol", service_tier: "default",
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2, prompt_tokens_details: { cached_tokens: 0 }, completion_tokens_details: { reasoning_tokens: 0 } },
    choices: [{ message: { role: "assistant", content }, finish_reason: "stop" }],
  });
}

// Stands in for the computer. A command naming "hold" waits until a journey releases it.
const guest = { release: [], commands: [] };
async function guestHandler(req, res) {
  const sent = await body(req).catch(() => ({}));
  if (req.url === "/healthz") return reply(res, 200, { ready: true, operation_receipts_v1: true, browser_task_tabs_v1: true, shell_jobs_v1: true });
  if (req.url === "/exec") {
    guest.commands.push(sent.command);
    if (sent.command.includes("hold")) await new Promise((done) => guest.release.push(done));
    return reply(res, 200, { exit_code: 0, stdout: "ran", stderr: "" });
  }
  if (req.url === "/operation") return reply(res, 404, {});
  reply(res, 200, { stopped: true, closed: true });
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
  const child = spawn(resolve(directory, name), [], { cwd: core, env: childEnv, stdio: process.env.NORI_M8_LOGS ? "inherit" : "ignore" });
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
async function post(operation, value = {}, expected) {
  const response = await fetch(`${address}/api/${operation}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(value), signal: AbortSignal.timeout(15_000) });
  const result = await response.json();
  if (expected ? response.status !== expected : !response.ok) throw new Error(`${operation}: HTTP ${response.status}: ${JSON.stringify(result)}`);
  return result;
}
const snapshot = async () => (await fetch(`${address}/v1/snapshot`)).json();
const task = async (id) => (await snapshot()).runs.find((run) => run.id === id);
const terminal = (run) => ["completed", "failed", "cancelled"].includes(run?.status);
const finished = (id) => until(() => task(id), terminal, `task ${id} to end`, 90_000);
async function dispatch(script) {
  const before = new Set((await snapshot()).runs.map((run) => run.id));
  await post("turn", { id: crypto.randomUUID(), message: `task: script:${script}` }, 202);
  const value = await until(snapshot, (now) => now.runs.some((run) => !before.has(run.id)), "dispatch");
  return value.runs.find((run) => !before.has(run.id)).id;
}
const running = (id) => until(() => task(id), (run) => run?.status === "running", `task ${id} to run`);
const settled = () => until(snapshot, (now) => !now.conversation.pending && !now.conversation.processing, "the conversation to settle");

// A client for the stream: it reads saved events as they arrive and can stop at any point.
function follow(query = "", headers = {}) {
  const controller = new AbortController();
  const events = [];
  const done = (async () => {
    const response = await fetch(`${address}/v1/events${query}`, { headers, signal: controller.signal });
    if (!response.ok) throw new Error(`events: HTTP ${response.status}`);
    let buffer = "";
    for await (const chunk of response.body.pipeThrough(new TextDecoderStream())) {
      buffer += chunk;
      let end;
      while ((end = buffer.indexOf("\n\n")) >= 0) {
        const frame = buffer.slice(0, end);
        buffer = buffer.slice(end + 2);
        if (frame.startsWith(":")) continue;
        const field = (name) => frame.split("\n").find((line) => line.startsWith(`${name}: `))?.slice(name.length + 2);
        events.push({ id: Number(field("id") ?? 0), kind: field("event"), data: JSON.parse(field("data")) });
      }
    }
  })().catch((error) => { if (error.name !== "AbortError") throw error; });
  return { events, close: () => { controller.abort(); return done; }, done };
}

beforeAll(async () => {
  if (!live) return;
  directory = mkdtempSync(resolve(tmpdir(), "nori-m8-"));
  for (const name of ["nori-local", "nori-worker"]) {
    const built = spawnSync("go", ["build", "-o", resolve(directory, name), `./cmd/${name}`], { cwd: core, encoding: "utf8", timeout: 180_000 });
    if (built.status !== 0) throw new Error(`Build ${name} failed: ${built.stderr}`);
  }
  writeFileSync(resolve(directory, "empty.env"), "");
  const [modelURL, outerURL, guestURL] = await Promise.all([serve(modelHandler), serve(outerHandler), serve(guestHandler)]);
  const port = 20000 + Math.floor(Math.random() * 20000);
  address = `http://127.0.0.1:${port}`;
  env = {
    PATH: process.env.PATH, HOME: process.env.HOME, NORI_ENV_FILE: resolve(directory, "empty.env"), NORI_PROJECT_ID: "bot-111549", FIRESTORE_DATABASE_ID: database,
    FIRESTORE_EMULATOR_HOST: emulator, NORI_LOCAL_ARTIFACT_DIR: resolve(directory, "artifacts"), PORT: String(port), TEMPORAL_ADDRESS: temporalAddress, TEMPORAL_NAMESPACE: "default", TEMPORAL_LOCAL: "true",
    TEMPORAL_TASK_QUEUE: deployment, TEMPORAL_DEPLOYMENT_NAME: deployment, TEMPORAL_BUILD_ID: build, COMPUTER_ZONE: "invalid-e2e-zone", COMPUTER_INSTANCE: "no-computer-e2e", NORI_EXECUTION_MODE: "temporal",
    OPENAI_API_KEY: "fake", JEV_API_KEY: "fake", OPENROUTER_API_KEY: "fake", NORI_E2E_MODEL_URL: modelURL, NORI_E2E_OPENAI_URL: outerURL, NORI_LOCAL_COMPUTER_URL: guestURL,
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
  for (const go of guest.release.splice(0)) go();
  await fetch(`http://${emulator}/emulator/v1/projects/bot-111549/databases/${database}/documents`, { method: "DELETE" }).catch(() => {});
  if (directory) rmSync(directory, { recursive: true, force: true });
});

const shell = (command) => ({ name: "shell", args: { command, read_only: false } });
const seq = (...steps) => (outputs) => steps[outputs.length]?.(outputs) ?? finish("Done.", outputs);

journey("replies, task progress and results arrive on the stream in order, with no other reads", async () => {
  const start = await snapshot();
  const stream = follow(`?after=${start.position}`);
  scripts.stream = seq(() => shell("hold-first"), () => shell("second"));
  const id = await dispatch("stream");
  await until(() => guest.release.length, (count) => count > 0, "the held command");
  const midway = stream.events.filter((event) => event.kind === "task" && event.data.run.id === id);
  expect(midway.length, "progress arrives while the task runs").toBeGreaterThan(0);
  expect(midway.at(-1).data.run.status).toBe("running");
  guest.release.shift()();
  await finished(id);
  await settled();
  const reply = await until(() => stream.events.filter((event) => event.kind === "message" && event.data.message.role === "assistant"), (found) => found.length >= 1, "the reply on the stream");
  expect(reply.at(-1).data.message.text).toBeTruthy();
  const last = await until(() => stream.events.filter((event) => event.kind === "task" && event.data.run.id === id).at(-1), (event) => event.data.run.status === "completed", "the result on the stream");
  expect(last.data.run.result).toBe("Done.");
  const ids = stream.events.filter((event) => event.id).map((event) => event.id);
  expect(ids).toEqual([...ids].sort((a, b) => a - b));
  await stream.close();
}, 120_000);

journey("a reconnect resumes after the last event, from the URL or the header, and misses nothing", async () => {
  const first = follow(`?after=${(await snapshot()).position}`);
  await post("turn", { id: crypto.randomUUID(), message: "first message" }, 202);
  await settled();
  await until(() => first.events.filter((event) => event.kind === "message"), (found) => found.length >= 2, "the first turn on the stream");
  const lastSeen = Math.max(...first.events.map((event) => event.id));
  await first.close();
  await post("turn", { id: crypto.randomUUID(), message: "sent while disconnected" }, 202);
  await settled();
  for (const stream of [follow(`?after=${lastSeen}`), follow("", { "Last-Event-ID": String(lastSeen) })]) {
    const found = await until(() => stream.events.filter((event) => event.kind === "message"), (messages) => messages.length >= 2, "the missed messages");
    expect(found[0].data.message.text).toBe("sent while disconnected");
    expect(Math.min(...stream.events.filter((event) => event.id).map((event) => event.id))).toBeGreaterThan(lastSeen);
    await stream.close();
  }
}, 60_000);

journey("a stale or unusable position gets a reset, not a guess", async () => {
  const { position } = await snapshot();
  for (const query of [`?after=${position + 1000}`, "?after=nonsense", "?after=-4", ""]) {
    const stream = follow(query);
    await stream.done;
    expect(stream.events.map((event) => event.kind), query).toEqual(["reset"]);
    expect(stream.events[0].data.position).toBe(position);
  }
}, 30_000);

journey("the removed status route is gone, and the snapshot reports the log position", async () => {
  const response = await fetch(`${address}/api/status`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
  expect(response.status).toBe(404);
  const before = await snapshot();
  await post("turn", { id: crypto.randomUUID(), message: "position" }, 202);
  await settled();
  expect((await snapshot()).position).toBeGreaterThan(before.position);
}, 30_000);
