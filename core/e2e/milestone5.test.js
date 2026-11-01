import { expect, test } from "bun:test";

// Real-provider journeys use an already running isolated local stack. Invoke
// through infrastructure/spend/run.py; this file never loads credentials or
// changes provider/model settings. Select one with bun test -t to control spend.
const configured = process.env.NORI_M5_E2E_API_URL;
const target = new URL(configured || "http://127.0.0.1");
if (target.protocol !== "http:" || !["localhost", "127.0.0.1", "[::1]"].includes(target.hostname)) throw new Error("NORI_M5_E2E_API_URL must be a loopback HTTP URL.");
const journey = configured ? test : test.skip;
const calendarJourney = process.env.NORI_M5_E2E_CALENDAR_ACCOUNT ? test : test.skip;
const fixture = "http://127.0.0.1:18080";
const terminal = (run) => ["completed", "cancelled", "failed"].includes(run?.status);

async function request(operation, body = {}) {
  return fetch(new URL(`/api/${operation}`, target), {
    method: "POST", headers: { "Content-Type": "application/json", Origin: target.origin },
    body: JSON.stringify(body), signal: AbortSignal.timeout(15_000),
  });
}
async function post(operation, body = {}, expected = 200) {
  const response = await request(operation, body);
  const value = await response.json();
  expect(response.status, `${operation}: HTTP ${response.status}: ${value.error || ""}`).toBe(expected);
  return value;
}
const session = () => post("session");
async function until(read, accept, label, timeout) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await read();
    if (accept(last)) return last;
    await Bun.sleep(1_000);
  }
  throw new Error(`Timed out waiting for ${label}: ${JSON.stringify(last)?.slice(0, 1500)}`);
}
const settled = () => until(session, (value) => !value.conversation.pending && !value.conversation.processing, "conversation to settle", 90_000);
// A real user message, so the outer model has to choose to dispatch it.
async function ask(message) {
  const before = new Set((await session()).runs.map((run) => run.id));
  await post("turn", { id: crypto.randomUUID(), message }, 202);
  const snapshot = await settled();
  const added = snapshot.runs.filter((run) => !before.has(run.id));
  expect(added.length, `The outer loop must dispatch one task for: ${message}`).toBe(1);
  return added[0].id;
}
const task = async (id) => (await session()).runs.find((run) => run.id === id);
const finished = (id, timeout = 240_000) => until(() => task(id), terminal, `task ${id}`, timeout);
const operations = async (id) => (await post("trace", { run_id: id })).steps || [];
const toolNames = async (id) => (await operations(id)).filter((step) => step.kind === "tool").map((step) => step.name);

journey("a public lookup finds and names a dated source without being given a URL", async () => {
  const id = await ask("What's the weather forecast for Tucson on Saturday?");
  const run = await finished(id);
  expect(await toolNames(id)).toContain("web_search");
  expect(run.status, run.error).toBe("completed");
  expect(run.result).toMatch(/tucson/i);
  expect(run.result, "The answer must name where it came from.").toMatch(/\b[a-z0-9-]+\.(com|gov|org|net)\b|weather\.|national weather service|nws|accuweather/i);
  expect(run.spent_usd, "A search charges the task's budget.").toBeGreaterThan(0.005);
}, 300_000);

journey("a screenshot is read for a time that only appears inside an image", async () => {
  const id = await ask(`Open ${fixture}/tracking, take a screenshot of the page, and tell me what time the delivery map shows. It's a local test page.`);
  const run = await finished(id);
  const tools = await toolNames(id);
  expect(tools).toContain("browser_screenshot");
  expect(tools, "The time isn't in the page text, so the image must be read.").toContain("screenshot_inspect");
  expect(run.status, run.error).toBe("completed");
  expect(run.result).toContain("14:30");
  expect(run.artifacts?.length).toBeGreaterThan(0);
}, 300_000);

journey("a backup runs past the shell limit, answers status from progress and verifies the count", async () => {
  const id = await ask("Back up the photos folder by running /workspace/backup-photos.sh on the computer, tell me when it's done, and check the file count in /workspace/archive matches /workspace/photos. It takes about four minutes.");
  const running = await until(() => task(id), (run) => terminal(run) || /Job running/.test(run?.progress || ""), "job progress", 180_000);
  expect(terminal(running), `The job should still be running: ${running.error || ""}`).toBe(false);
  expect(await toolNames(id)).toContain("job_start");
  const before = (await session()).messages.length;
  await post("turn", { id: crypto.randomUUID(), message: "is it finished?" }, 202);
  const status = await settled();
  const reply = status.messages.slice(before).filter((message) => message.role === "assistant").at(-1);
  expect(reply?.text, "Status mid-job must not claim the backup finished.").not.toMatch(/\b(yes|finished|completed|done)\b(?!\s*yet)(?![^.]*\bnot\b)/i);
  const run = await finished(id, 480_000);
  expect(run.status, run.error).toBe("completed");
  expect(run.result, "The answer must report the verified count.").toMatch(/2,?400/);
  const steps = await operations(id);
  const model = steps.filter((step) => step.kind === "model");
  const wait = steps.find((step) => step.kind === "tool" && step.name === "job_wait");
  expect(wait, "The task must wait on the job rather than polling with model calls.").toBeTruthy();
  expect(model.length, "Waiting on a job must not make model calls.").toBeLessThan(12);
}, 720_000);

journey("Stop ends a running job and its child processes through the task", async () => {
  const id = await ask("Run this as a background job on the computer and wait for it: for i in $(seq 1 600); do echo tick $i; sleep 1; done");
  await until(() => task(id), (run) => terminal(run) || /Job running/.test(run?.progress || ""), "job progress", 180_000);
  const started = performance.now();
  await post("command", { id: crypto.randomUUID(), run_id: id, kind: "stop" }, 202);
  const run = await finished(id, 60_000);
  expect(run.status).toBe("cancelled");
  expect(performance.now() - started, "Stop must not wait for the job's own end.").toBeLessThan(60_000);
}, 300_000);

calendarJourney("a calendar question is answered from the connected account and names it", async () => {
  const id = await ask("What's on my calendar Thursday?");
  const run = await finished(id);
  expect(await toolNames(id)).toContain("calendar_read");
  expect(run.status, run.error).toBe("completed");
  expect(run.result).toContain(process.env.NORI_M5_E2E_CALENDAR_ACCOUNT);
}, 300_000);
