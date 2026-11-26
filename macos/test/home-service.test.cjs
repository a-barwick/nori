"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { HomeConnector, startCommand } = require("../connector.cjs");

function fixture({ paired = false, revocationPending = false, background = "enabled", pairBehavior, pairingStartTimeoutMs = 50 } = {}) {
  const calls = [];
  const json = (value) => ({ result: Promise.resolve(JSON.stringify(value)) });
  const service = new HomeConnector({
    connector(command, options) {
      calls.push(`connector:${command}`);
      if (command === "status") return json({ paired, revocation_pending: revocationPending });
      if (command === "pair" && pairBehavior) return pairBehavior(options);
      return json({ revocation_pending: revocationPending });
    },
    async service(command) {
      calls.push(`service:${command}`);
      return { background };
    },
  }, { pairingStartTimeoutMs });
  return { calls, service };
}

test("Startup fences abandoned pairing before retrying revocation", async () => {
  const { calls, service } = fixture({ paired: false, revocationPending: true });
  assert.deepEqual(await service.retryRevocation(), { revocationPending: true });
  assert.deepEqual(calls, [
    "service:status",
    "connector:status",
    "connector:desktop-disable",
    "service:unregister",
  ]);
});

test("Startup recovery preserves the live Home AI connection", async () => {
  const { calls, service } = fixture({ paired: true, revocationPending: false });
  assert.deepEqual(await service.retryRevocation(), { revocationPending: false });
  assert.deepEqual(calls, [
    "service:status",
    "connector:status",
    "connector:desktop-revoke-pending",
  ]);
});

test("Disconnect unregisters the background agent even while offline", async () => {
  const { calls, service } = fixture({ paired: true, revocationPending: true });
  assert.deepEqual(await service.disconnect(), { revocationPending: true });
  assert.deepEqual(calls, [
    "connector:desktop-disable",
    "service:unregister",
  ]);
});

test("beginPairing rolls back desktop-enable when relay output is invalid", async () => {
  let stopped = false;
  let resolvePair;
  const { calls, service } = fixture({
    pairBehavior(options) {
      queueMicrotask(() => options.onOutput(JSON.stringify({ type: "error" }) + "\n"));
      return {
        result: new Promise((resolve) => { resolvePair = resolve; }),
        stop() {
          stopped = true;
          resolvePair?.(JSON.stringify({ type: "cancelled" }) + "\n");
        },
      };
    },
  });

  await assert.rejects(() => service.beginPairing(), /did not return a pairing code/);
  assert.equal(stopped, true);
  assert.equal(service.pairingInProgress, false);
  assert.deepEqual(calls, [
    "service:status",
    "connector:status",
    "connector:desktop-enable",
    "connector:pair",
    "connector:desktop-disable",
  ]);
});

test("beginPairing rolls back desktop-enable on pairing-start timeout", async () => {
  let stopped = false;
  let rejectPair;
  const { calls, service } = fixture({
    pairingStartTimeoutMs: 25,
    pairBehavior() {
      return {
        result: new Promise((_resolve, reject) => { rejectPair = reject; }),
        stop() {
          stopped = true;
          rejectPair?.(new Error("stopped"));
        },
      };
    },
  });

  await assert.rejects(() => service.beginPairing(), /couldn’t start pairing/i);
  assert.equal(stopped, true);
  assert.equal(service.pairingInProgress, false);
  assert.equal(calls.includes("connector:desktop-disable"), true);
});

test("status waits for exclusive operations and finishPairing is mutually exclusive", async () => {
  let resolvePair;
  let statusCallsDuringFinish = 0;
  let finishResolved = false;

  const { service } = fixture({
    pairBehavior(options) {
      queueMicrotask(() => options.onOutput(JSON.stringify({ type: "pairing", user_code: "ABCD-1234" }) + "\n"));
      return {
        result: new Promise((resolve) => { resolvePair = resolve; }),
        stop() {
          resolvePair?.(JSON.stringify({ type: "cancelled" }) + "\n");
        },
      };
    },
  });

  const grant = await service.beginPairing();
  assert.deepEqual(grant, { user_code: "ABCD-1234" });

  const finishing = service.finishPairing().then(() => { finishResolved = true; });
  await assert.rejects(() => service.finishPairing(), /Finish the current Home AI setup action first/);

  const statusPromise = service.status().then((value) => {
    if (!finishResolved) statusCallsDuringFinish += 1;
    return value;
  });

  resolvePair(JSON.stringify({ type: "pairing", user_code: "ABCD-1234" }) + "\n" + JSON.stringify({ type: "paired" }) + "\n");
  await finishing;
  const status = await statusPromise;
  assert.equal(statusCallsDuringFinish, 0);
  assert.equal(status.background, "enabled");
  assert.equal(service.pairingInProgress, false);
});

test("disconnect cancels an in-flight finishPairing instead of deadlocking", async () => {
  let rejectPair;
  const { calls, service } = fixture({
    pairBehavior(options) {
      queueMicrotask(() => options.onOutput(JSON.stringify({ type: "pairing", user_code: "WXYZ-5678" }) + "\n"));
      return {
        result: new Promise((_resolve, reject) => { rejectPair = reject; }),
        stop() {
          rejectPair?.(new Error("cancelled"));
        },
      };
    },
  });

  await service.beginPairing();
  const finishing = service.finishPairing();
  const disconnected = service.disconnect();

  await assert.rejects(() => finishing, /cancelled/);
  await disconnected;
  assert.equal(service.pairingInProgress, false);
  assert.equal(calls.at(-2), "connector:desktop-disable");
  assert.equal(calls.at(-1), "service:unregister");
});

test("startCommand captures bounded stderr diagnostics on failure", async () => {
  const { result } = startCommand(process.execPath, [
    "-e",
    "process.stderr.write('x'.repeat(40000)); process.exit(2);",
  ]);
  await assert.rejects(result, (error) => {
    assert.match(error.message, /The Mac helper couldn’t complete the request:/);
    assert.equal(error.stderr.length, 16 * 1024);
    return true;
  });
});
