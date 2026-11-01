import { expect, test } from "bun:test";
import { spawnSync } from "node:child_process";

// Explicit opt-in against an owned, isolated local guest; no model calls.
const configured = process.env.NORI_M5_COMPUTER_E2E_URL;
const guest = new URL(configured || "http://127.0.0.1");
if (guest.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(guest.hostname)) {
  throw new Error("NORI_M5_COMPUTER_E2E_URL must be a loopback HTTP origin.");
}
const container = process.env.NORI_M5_COMPUTER_CONTAINER;
if (container && !/^nori-m5-[a-z0-9-]+$/.test(container)) throw new Error("Use only an owned nori-m5-* container.");
const journey = configured ? test : test.skip;
const unique = crypto.randomUUID().slice(0, 8);
const id = (name) => `m5-${unique}-${name}`;

async function request(path, body, operation) {
  return fetch(new URL(path, guest), {
    method: "POST", headers: { "Content-Type": "application/json", ...(operation ? { "Nori-Operation-ID": operation } : {}) },
    body: JSON.stringify(body), signal: AbortSignal.timeout(30_000),
  });
}
async function post(path, body, operation, expected = 200) {
  const response = await request(path, body, operation);
  const text = await response.text();
  expect(response.status, `${path}: ${text.slice(0, 300)}`).toBe(expected);
  return text && expected === 200 ? JSON.parse(text) : {};
}
const start = (task, job, command) => post("/job/start", { run_id: `${task}-r1`, task_id: task, job_id: job, command }, job);
const status = (task, job) => post("/job/status", { task_id: task, job_id: job });
const shell = (task, command) => post("/exec", { run_id: `${task}-shell`, task_id: task, command, timeout_seconds: 10 });
async function until(read, accept, label, timeout = 15_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try { last = await read(); if (accept(last)) return last; } catch { /* The guest may still be restarting. */ }
    await Bun.sleep(100);
  }
  throw new Error(`Timed out waiting for ${label}: ${JSON.stringify(last)}`);
}
const activity = async () => (await fetch(new URL("/activity", guest))).json();

journey("a job outlives the shell limit, reports progress and its own exit code", async () => {
  const task = id("long"), job = id("long-job");
  const started = await start(task, job, "for i in $(seq 1 11); do echo step $i; sleep 5; done; echo finished > result.txt; exit 3");
  expect(started.state).toBe("running");
  expect(started.job_id).toBe(job);
  expect((await activity()).active, "A running job must hold off the idle stop.").toBeGreaterThan(0);
  await Bun.sleep(20_000);
  const midway = await status(task, job);
  expect(midway.state).toBe("running");
  expect(midway.output_tail).toContain("step 4");
  const ended = await until(() => status(task, job), (value) => value.state !== "running", "long job to end", 60_000);
  expect(ended.state).toBe("finished");
  expect(ended.exit_code).toBe(3);
  expect(ended.elapsed_seconds, "The job must run past the 45-second shell limit.").toBeGreaterThanOrEqual(50);
  expect(ended.output_tail).toContain("step 11");
  expect((await shell(task, "cat result.txt")).stdout, "A job runs in its task's folder.").toContain("finished");
}, 120_000);

journey("a repeated start replays its receipt and never runs the job twice", async () => {
  const task = id("once"), job = id("once-job");
  const command = "echo started >> count.txt; sleep 30";
  const first = await start(task, job, command);
  const replay = await start(task, job, command);
  expect(replay.job_id).toBe(first.job_id);
  await Bun.sleep(500);
  expect((await shell(task, "wc -l < count.txt")).stdout.trim()).toBe("1");
  const stopped = await post("/job/stop", { task_id: task, job_id: job });
  expect(stopped.state).toBe("stopped");
});

journey("stop kills child processes, stays scoped to its task, and repeats safely", async () => {
  const task = id("stop"), other = id("other");
  const job = id("stop-job"), sibling = id("sibling-job");
  await start(task, job, "(setsid sleep 300 & echo $! > child.pid; wait) & wait");
  await start(other, sibling, "sleep 300");
  const pid = (await until(() => shell(task, "cat child.pid"), (value) => /\d+/.test(value.stdout), "child pid")).stdout.trim();
  expect((await shell(task, `kill -0 ${pid} && echo alive`)).stdout).toContain("alive");
  await post("/job/status", { task_id: other, job_id: job }, undefined, 404);
  const stoppedAt = performance.now();
  expect(await post("/job/stop-task", { task_id: task })).toEqual({ stopped: true });
  expect(performance.now() - stoppedAt, "Stop must confirm within the job stop deadline.").toBeLessThan(10_000);
  expect((await status(task, job)).state).toBe("stopped");
  // A killed orphan stays a zombie where the guest runs as PID 1 with no init to reap it.
  expect((await shell(task, `state=$(sed 's/.*) //' /proc/${pid}/stat 2>/dev/null | cut -d' ' -f1); case "$state" in ""|Z) echo gone;; *) echo alive;; esac`)).stdout).toContain("gone");
  expect(await post("/job/stop-task", { task_id: task })).toEqual({ stopped: true });
  expect((await status(other, sibling)).state, "Stopping one task leaves its sibling running.").toBe("running");
  expect((await post("/job/stop", { task_id: other, job_id: sibling })).state).toBe("stopped");
});

journey("a command that exits at once and a chatty command are both reported correctly", async () => {
  const task = id("edges"), fast = id("fast"), chatty = id("chatty");
  await start(task, fast, "echo hello");
  const ended = await until(() => status(task, fast), (value) => value.state !== "running", "fast job");
  expect(ended.exit_code).toBe(0);
  expect(ended.output_tail).toContain("hello");
  await start(task, chatty, "head -c 20000000 /dev/zero | tr '\\0' x");
  const full = await until(() => status(task, chatty), (value) => value.state !== "running", "chatty job", 30_000);
  expect(full.exit_code, "A chatty command must not block on its output.").toBe(0);
  expect(full.output_truncated).toBe(true);
  expect(full.output_tail.length).toBeLessThanOrEqual(4096);
}, 60_000);

journey("a job is limited per task and refused after its run is cancelled", async () => {
  const task = id("limit");
  const jobs = [1, 2, 3, 4].map((n) => id(`limit-${n}`));
  for (const job of jobs) await start(task, job, "sleep 120");
  await post("/job/start", { run_id: `${task}-r1`, task_id: task, job_id: id("limit-5"), command: "sleep 1" }, id("limit-5"), 429);
  expect(await post("/job/stop-task", { task_id: task })).toEqual({ stopped: true });
  expect(await post("/cancel", { run_id: `${task}-r9`, task_id: task })).toEqual({ stopped: true });
  await post("/job/start", { run_id: `${task}-r9`, task_id: task, job_id: id("limit-6"), command: "sleep 1" }, id("limit-6"), 409);
});

journey("a page that fails to load doesn't block the task's final stop", async () => {
  const task = id("badpage"), run = `${task}-r1`;
  await post("/navigate", { run_id: run, task_id: task, url: "http://127.0.0.1:1/" }, id("badpage-nav"), 502);
  expect(await post("/cancel", { run_id: run, task_id: task })).toEqual({ stopped: true });
});

const restartJourney = configured && container ? test : test.skip;
restartJourney("a guest restart reports a running job as lost, never finished", async () => {
  const task = id("restart"), job = id("restart-job");
  await start(task, job, "sleep 600; echo finished");
  expect((await status(task, job)).state).toBe("running");
  const restart = spawnSync("docker", ["restart", container], { encoding: "utf8", timeout: 60_000 });
  expect(restart.status, restart.stderr).toBe(0);
  const after = await until(() => status(task, job), (value) => value.state !== "running", "lost job after restart", 40_000);
  expect(after.state).toBe("lost");
  expect(after.exit_code).toBeUndefined();
}, 120_000);
