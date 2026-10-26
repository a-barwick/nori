import { afterAll, beforeAll, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Opt in: NORI_DURABLE_E2E=1 FIRESTORE_EMULATOR_HOST=127.0.0.1:8085
// TEMPORAL_ADDRESS=127.0.0.1:7235 bun test core/e2e/durable.test.js
// Uses real Vertex/ADC, existing local emulators, and isolated task/data/processes.
const live = process.env.NORI_DURABLE_E2E === "1";
const journey = live ? test : test.skip;
const core = resolve(import.meta.dir, "..");
const owner = "austinbarwickdavis@gmail.com";
const unique = crypto.randomUUID().slice(0, 8);
const database = `nori-e2e-${unique}`;
const deployment = `nori-e2e-${unique}`;
const build = `e2e-${unique}`;
const runs = [];
let directory, address, api, worker, env;

function localAddress(value, name) {
  if (!value || !/^127\.0\.0\.1:\d+$/.test(value)) throw new Error(`${name} must be an explicit 127.0.0.1:port`);
  return value;
}
const emulator = live ? localAddress(process.env.FIRESTORE_EMULATOR_HOST, "FIRESTORE_EMULATOR_HOST") : "";
const temporalAddress = live ? localAddress(process.env.TEMPORAL_ADDRESS, "TEMPORAL_ADDRESS") : "";
const documents = `http://${emulator}/v1/projects/bot-111549/databases/${database}/documents`;
const delay = (ms) => new Promise((done) => setTimeout(done, ms));

async function until(read, accepts, label, timeout = 45_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      const value = await read();
      if (accepts(value)) return value;
    } catch { /* Processes and their local services may still be starting. */ }
    await delay(75);
  }
  throw new Error(`Timed out waiting for ${label}`);
}

function cli(...args) {
  const result = spawnSync("temporal", ["--address", temporalAddress, "--namespace", "default", "--output", "json", ...args], { encoding: "utf8", timeout: 10_000 });
  if (result.status !== 0) throw new Error(`Temporal ${args.slice(0, 2).join(" ")} failed`);
  return result.stdout.trim() ? JSON.parse(result.stdout) : {};
}
function processFor(name, additions = {}) {
  const child = spawn(resolve(directory, name), [], { cwd: core, env: { ...env, ...additions }, stdio: "ignore" });
  child.exited = new Promise((done) => child.once("exit", done));
  return child;
}
async function stop(child, signal = "SIGTERM") {
  if (child && child.exitCode === null && child.signalCode === null) child.kill(signal);
  if (child) await child.exited;
}
async function control(paused) {
  const response = await fetch(`${documents}/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: paused }, expires_at: { stringValue: "2000-01-01T00:00:00Z" } } }) });
  expect(response.ok).toBe(true);
}
async function post(operation, body = {}) {
  const response = await fetch(`${address}/api/${operation}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body), signal: AbortSignal.timeout(10_000) });
  const result = await response.json();
  if (!response.ok) throw new Error(`${operation}: HTTP ${response.status}: ${result.error}`);
  return result;
}
async function run(id) {
  return (await post("session")).runs.find((item) => item.id === id);
}
function history(id) {
  const result = cli("workflow", "show", "--workflow-id", `nori-task-${id}`);
  return Array.isArray(result) ? result : result.events;
}
function modelCalls(id) {
  return history(id).filter((event) => event.activityTaskScheduledEventAttributes?.activityType?.name === "Model").length;
}
async function input(id, message) {
  return post("input", { id: crypto.randomUUID(), run_id: id, message });
}
async function startWorker(prices = {}) {
  worker = processFor("nori-worker", prices);
  await until(() => cli("worker", "deployment", "describe-version", "--deployment-name", deployment, "--build-id", build), () => true, "worker registration");
}

beforeAll(async () => {
  if (!live) return;
  directory = mkdtempSync(resolve(tmpdir(), "nori-durable-e2e-"));
  for (const name of ["nori", "nori-worker"]) {
    const result = spawnSync("go", ["build", "-o", resolve(directory, name), `./cmd/${name}`], { cwd: core, encoding: "utf8", timeout: 90_000 });
    if (result.status !== 0) throw new Error(`Build ${name} failed: ${result.stderr}`);
  }
  const listener = createServer();
  await new Promise((done) => listener.listen(0, "127.0.0.1", done));
  const port = listener.address().port;
  await new Promise((done) => listener.close(done));
  address = `http://127.0.0.1:${port}`;
  env = { ...process.env, NORI_ENV_FILE: process.env.NORI_E2E_ENV_FILE || resolve(core, "../.env"), NORI_PROJECT_ID: "bot-111549", FIRESTORE_DATABASE_ID: database, FIRESTORE_EMULATOR_HOST: emulator, ARTIFACT_BUCKET: "nori-e2e-unused", NORI_EXECUTION_MODE: "temporal", BIND_HOST: "127.0.0.1", PORT: String(port), TEMPORAL_ADDRESS: temporalAddress, TEMPORAL_NAMESPACE: "default", TEMPORAL_LOCAL: "true", TEMPORAL_CLOUD_RUN_ID: "false", TEMPORAL_TASK_QUEUE: deployment, TEMPORAL_DEPLOYMENT_NAME: deployment, TEMPORAL_BUILD_ID: build, COMPUTER_ZONE: "invalid-e2e-zone", COMPUTER_INSTANCE: "no-computer-e2e" };
  await control(false);
  api = processFor("nori");
  await until(() => fetch(`${address}/healthz`), (response) => response.ok, "isolated API");
  // Deliberately high configured accounting price exercises a budget wait with
  // one real model response; it does not change the provider's actual price.
  await startWorker({ MODEL_INPUT_USD_PER_MTOK: "4000", MODEL_OUTPUT_USD_PER_MTOK: "1.50" });
  cli("worker", "deployment", "set-current-version", "--deployment-name", deployment, "--build-id", build, "--yes");
}, 120_000);

afterAll(async () => {
  if (!live) return;
  for (const id of runs) {
    try {
      if ((await run(id))?.status === "running") cli("workflow", "terminate", "--workflow-id", `nori-task-${id}`, "--reason", "Isolated E2E cleanup");
    } catch { /* Cleanup is limited to this suite's own run IDs. */ }
  }
  await stop(worker);
  await stop(api);
  await fetch(`http://${emulator}/emulator/v1/projects/bot-111549/databases/${database}/documents`, { method: "DELETE" });
  if (directory) rmSync(directory, { recursive: true, force: true });
});

journey("a durable question survives pause/restart and only explicit continue grants more budget", async () => {
  const id = crypto.randomUUID(); runs.push(id);
  await post("run", { id, message: "Help me choose a title. First use wait_for_input to ask exactly: What is the title about? Wait for my reply. Do not use the computer or any other tool. After I answer return only the title." });
  const waiting = await until(() => run(id), (value) => value?.waiting && /title/i.test(value.waiting), "real model question");
  expect(waiting.temporal).toBe(true);
  expect(waiting.spent_usd).toBeGreaterThan(1);
  expect(waiting.budget_usd).toBe(1);
  expect(modelCalls(id)).toBe(1);
  const described = cli("workflow", "describe", "--workflow-id", `nori-task-${id}`);
  expect(described.executionConfig.workflowExecutionTimeout).toBe("0s");

  await control(true);
  await until(() => worker.exitCode, (code) => code !== null, "paused worker exit");
  expect((await run(id)).status).toBe("running");
  expect((await run(id)).waiting).toBe(waiting.waiting);
  const blocked = await fetch(`${address}/api/run`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: crypto.randomUUID(), message: "Reply Ready." }) });
  expect(blocked.ok).toBe(false);
  expect(modelCalls(id)).toBe(1);

  await control(false);
  await startWorker({ MODEL_INPUT_USD_PER_MTOK: "0.25", MODEL_OUTPUT_USD_PER_MTOK: "1.50" });
  await post("resume");
  await input(id, "The title is Budget verified. Reply with that title only.");
  await until(() => run(id), (value) => value?.incorporated === value.revision && /model budget/i.test(value.waiting || ""), "budget question after restart");
  await input(id, "Correction: keep the same title.");
  const unchanged = await until(() => run(id), (value) => value?.revision === 3 && value.incorporated === 3 && value.waiting, "correction without a grant");
  expect(unchanged.budget_usd).toBe(1);
  expect(unchanged.spent_usd).toBe(waiting.spent_usd);
  expect(modelCalls(id)).toBe(1);

  let current = unchanged, grants = 0;
  while (current.status === "running" && grants < 16) {
    grants++;
    await input(id, "continue");
    current = await until(() => run(id), (value) => value?.budget_usd === 1 + grants && (value.status !== "running" || value.waiting), "explicit grant or completion");
  }
  expect(current.status).toBe("completed");
  expect(current.id).toBe(id);
  expect(current.budget_usd).toBe(1 + grants);
  expect(current.cost_unknown).not.toBe(true);
  expect(modelCalls(id)).toBe(2);
  const answer = (await post("session")).messages.find((message) => message.id === `${id}-assistant`);
  expect(answer.text).toMatch(/Budget verified/i);
}, 150_000);

journey("a worker crash during a real model request records unknown cost without replaying it", async () => {
  const id = crypto.randomUUID(); runs.push(id);
  await post("run", { id, message: "For a local recovery check, write 350 numbered full sentences about everyday objects. Include the entire list; do not use tools or ask questions." });
  await until(() => cli("workflow", "describe", "--workflow-id", `nori-task-${id}`), (value) => value.pendingActivities?.some((item) => item.activityType?.name === "Model" && item.state === "PENDING_ACTIVITY_STATE_STARTED"), "model activity in flight");
  await stop(worker, "SIGKILL");
  await startWorker({ MODEL_INPUT_USD_PER_MTOK: "0.25", MODEL_OUTPUT_USD_PER_MTOK: "1.50" });
  const failed = await until(() => run(id), (value) => value?.status === "failed", "unknown-cost terminal result");
  expect(failed.cost_unknown).toBe(true);
  expect(failed.error).toMatch(/cost is unknown/i);
  expect(failed.budget_usd).toBe(1);
  expect(modelCalls(id)).toBe(1);
  expect(history(id).some((event) => event.eventType === "EVENT_TYPE_ACTIVITY_TASK_TIMED_OUT")).toBe(true);
  expect((await post("session")).messages.some((message) => message.id === `${id}-assistant`)).toBe(false);
}, 90_000);
