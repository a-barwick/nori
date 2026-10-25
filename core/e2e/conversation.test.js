import { expect, test } from "bun:test";

// Exercise the running UI proxy, Go runner, Jev, and conversational model.
// Opt in with NORI_E2E_URL=http://127.0.0.1:3210; these are live model calls.
const target = new URL(process.env.NORI_E2E_URL || "http://127.0.0.1:3210");
if (target.protocol !== "http:" || !["localhost", "127.0.0.1", "[::1]"].includes(target.hostname)) {
  throw new Error("NORI_E2E_URL must point to the local development server.");
}
const live = process.env.NORI_E2E_URL ? test : test.skip;

async function post(operation, body = {}) {
  const response = await fetch(new URL(`/api/agent/${operation}`, target), {
    method: "POST",
    headers: { "Content-Type": "application/json", Origin: target.origin },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(100_000),
  });
  const data = await response.json();
  if (!response.ok) throw new Error(`${operation}: HTTP ${response.status}: ${data.error}`);
  return data;
}

function expectNoWork(before, after) {
  expect(after.dispatches).toEqual(before.dispatches);
  expect(after.runs.map((run) => [run.id, run.revision])).toEqual(
    before.runs.map((run) => [run.id, run.revision]),
  );
}

live("ordinary chat creates no task and retrying its ID creates no duplicate turn", async () => {
  const before = await post("session");
  const input = { id: crypto.randomUUID(), message: "What is 2 + 2? Reply with only the number. Just answer; do not start work." };
  const after = await post("turn", input);
  expectNoWork(before, after);
  expect(after.classification_hint.available).toBe(true);
  expect(after.classification_hint.intent).toBe("ask");
  const reply = after.messages.find((message) => message.id === `${input.id}-assistant`);
  expect(reply.text.trim()).toMatch(/^4\.?$/);

  const replay = await post("turn", input);
  expectNoWork(after, replay);
  expect(replay.messages.filter((message) => message.id.startsWith(input.id))).toEqual(
    after.messages.filter((message) => message.id.startsWith(input.id)),
  );
}, 110_000);

live("a pretend calendar request gets an ask hint and a hypothetical reply without dispatch", async () => {
  const before = await post("session");
  const input = { id: crypto.randomUUID(), message: "whats on my calendar this week? respond with action type and pretend response" };
  const after = await post("turn", input);
  expectNoWork(before, after);
  expect(after.classification_hint.available).toBe(true);
  expect(after.classification_hint.intent).toBe("ask");
  const reply = after.messages.find((message) => message.id === `${input.id}-assistant`);
  expect(reply.text).toMatch(/hypothetical/i);
  expect(reply.text).toMatch(/dispatch/i);
}, 110_000);
