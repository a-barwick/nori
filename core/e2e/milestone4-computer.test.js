import { expect, test } from "bun:test";
import { spawnSync } from "node:child_process";

// Explicit opt-in against an owned, isolated local guest; no model calls.
const configured = process.env.NORI_M4_COMPUTER_E2E_URL;
const guest = new URL(configured || "http://127.0.0.1");
if (guest.protocol !== "http:" || !["127.0.0.1", "localhost", "[::1]"].includes(guest.hostname)) {
  throw new Error("NORI_M4_COMPUTER_E2E_URL must be a loopback HTTP origin.");
}
const fixture = process.env.NORI_M4_FIXTURE_ORIGIN || "http://127.0.0.1:18080";
const container = process.env.NORI_M4_COMPUTER_CONTAINER;
if (container && !/^nori-m4-[a-z0-9-]+$/.test(container)) throw new Error("Use only an owned nori-m4-* container.");
const journey = configured ? test : test.skip;
const unique = crypto.randomUUID().slice(0, 8);
const id = (name) => `m4-${unique}-${name}`;
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
  return text ? JSON.parse(text) : {};
}
async function until(read, accept, label, timeout = 10_000) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    try { last = await read(); if (accept(last)) return last; } catch { /* Startup or an operation may still be pending. */ }
    await Bun.sleep(50);
  }
  throw new Error(`Timed out waiting for ${label}: ${JSON.stringify(last)}`);
}
const receipt = (operation, run) => post("/operation", { id: operation, run_id: run });
const navigate = (run, path, operation) => post("/navigate", { run_id: run, task_id: run, url: fixture + path }, operation);
const completed = (value) => value?.status === "completed";

journey("guest read interruption, sibling isolation, write protection, receipts and restart proof", async () => {
  const first = id("first"), sibling = id("sibling"), shell = id("shell");
  const firstRead = id("first-read"), slowRead = id("slow"), siblingRead = id("sibling-read");
  const shellOperation = id("shell-once");
  let restarted = false;
  try {
    const initial = await navigate(first, "/tucson", firstRead);
    expect(initial.text).toContain("Tucson");
    const slow = request("/navigate", { run_id: first, task_id: first, url: fixture + "/slow" }, slowRead);
    await until(() => receipt(slowRead, first), (value) => value.status === "uncertain", "slow navigation to start");
    await Bun.sleep(250);
    const other = await navigate(sibling, "/phoenix", siblingRead);
    expect(other.text).toContain("Phoenix");
    const cancelStarted = performance.now();
    expect(await post("/operation/cancel", { id: slowRead, run_id: first })).toEqual({ stopped: true });
    const cancelMS = performance.now() - cancelStarted;
    expect(cancelMS, "A warm browser read must acknowledge interruption within two seconds.").toBeLessThan(2_000);
    expect((await slow).status).toBeGreaterThanOrEqual(400);
    const interrupted = await until(() => receipt(slowRead, first), completed, "interrupted read receipt");
    expect(interrupted.read_operation).toBe(true);
    expect(interrupted.stop_confirmed).toBe(true);
    const png = await request("/screenshot", { run_id: sibling, task_id: sibling }, id("sibling-png"));
    expect(png.status).toBe(200);
    const pngBytes = new Uint8Array(await png.arrayBuffer());
    expect([...pngBytes.slice(0, 8)]).toEqual([137, 80, 78, 71, 13, 10, 26, 10]);
    expect(pngBytes.length).toBeGreaterThan(100);
    const siblingReceipt = await receipt(siblingRead, sibling);
    expect(JSON.parse(Buffer.from(siblingReceipt.body, "base64").toString()).text).toContain("Phoenix");

    const body = { run_id: shell, task_id: shell, command: "printf 'once\\n' >> effect-count.txt; sleep 1; cat effect-count.txt", timeout_seconds: 10 };
    const writing = request("/exec", body, shellOperation);
    await until(() => receipt(shellOperation, shell), (value) => value.status === "uncertain", "shell operation to start");
    expect((await request("/operation/cancel", { id: shellOperation, run_id: shell })).status).toBe(409);
    const written = await writing;
    expect(written.status).toBe(200);
    const result = await written.json();
    expect(result.exit_code).toBe(0);
    expect(result.stdout).toBe("once\n");
    // Recover the saved response without another /exec; then replay exactly the
    // same operation and confirm its file effect happened once.
    const saved = await receipt(shellOperation, shell);
    expect(saved.status).toBe("completed");
    expect(JSON.parse(Buffer.from(saved.body, "base64").toString())).toEqual(result);
    expect(await post("/exec", body, shellOperation)).toEqual(result);
    const count = await post("/exec", { run_id: shell, task_id: shell, command: "wc -l < effect-count.txt", timeout_seconds: 10 }, id("count"));
    expect(count.stdout.trim()).toBe("1");
    expect((await request("/operation/cancel", { id: shellOperation, run_id: shell })).status).toBe(409);
    expect((await request("/exec", { ...body, command: "printf duplicate" }, shellOperation)).status).toBe(409);
    console.log(`Read interruption acknowledged in ${Math.round(cancelMS)} ms; sibling PNG ${pngBytes.length} bytes; shell effect count 1.`);

    if (container) {
      const restart = spawnSync("docker", ["restart", container], { encoding: "utf8", timeout: 30_000 });
      expect(restart.status, restart.stderr).toBe(0);
      restarted = true;
      await until(() => fetch(new URL("/healthz", guest)), (response) => response.ok, "guest restart", 20_000);
      expect((await receipt(slowRead, first)).stop_confirmed).toBe(true);
      expect(await post("/operation/cancel", { id: slowRead, run_id: first })).toEqual({ stopped: true });
      expect((await request("/operation/cancel", { id: shellOperation, run_id: shell })).status).toBe(409);
      expect(await post("/exec", body, shellOperation)).toEqual(result);
      const retained = await post("/exec", { run_id: shell, task_id: shell, command: "wc -l < effect-count.txt", timeout_seconds: 10 }, id("retained-count"));
      expect(retained.stdout.trim()).toBe("1");
      console.log("Retained read stop proof and exactly-once shell receipt survived guest restart.");
    }
  } finally {
    for (const run of [first, sibling, shell]) {
      try { await post("/cancel", { run_id: run, task_id: run }); } catch { /* Keep failure output, cleanup only this journey's IDs. */ }
      try { await post("/browser/close", { task_id: run }); } catch { /* The browser may have restarted. */ }
    }
    if (restarted) console.log("Owned guest left running; retained profile and task data preserved.");
  }
}, 90_000);
