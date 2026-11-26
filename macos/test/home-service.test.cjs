"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { HomeConnector } = require("../connector.cjs");

function fixture({ paired, revocationPending }) {
  const calls = [];
  const json = (value) => ({ result: Promise.resolve(JSON.stringify(value)) });
  const service = new HomeConnector({
    connector(command) {
      calls.push(command);
      if (command === "status") return json({ paired, revocation_pending: revocationPending });
      return json({ revocation_pending: revocationPending });
    },
    async service(command) {
      calls.push(command);
      return { background: "enabled" };
    },
  });
  return { calls, service };
}

test("Startup fences abandoned pairing before retrying revocation", async () => {
  const { calls, service } = fixture({ paired: false, revocationPending: true });
  assert.deepEqual(await service.retryRevocation(), { revocationPending: true });
  assert.deepEqual(calls, ["status", "status", "desktop-disable", "unregister"]);
});

test("Startup recovery preserves the live Home AI connection", async () => {
  const { calls, service } = fixture({ paired: true, revocationPending: false });
  assert.deepEqual(await service.retryRevocation(), { revocationPending: false });
  assert.deepEqual(calls, ["status", "status", "desktop-revoke-pending"]);
});

test("Disconnect unregisters the background agent even while offline", async () => {
  const { calls, service } = fixture({ paired: true, revocationPending: true });
  assert.deepEqual(await service.disconnect(), { revocationPending: true });
  assert.deepEqual(calls, ["desktop-disable", "unregister"]);
});
