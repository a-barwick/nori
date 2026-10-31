import { expect, test } from "bun:test";

// Use a fresh, isolated database with the running Go API (NORI_E2E_API_URL)
// or UI proxy (NORI_E2E_URL). These opt-in checks make real model calls.
const target = new URL(process.env.NORI_E2E_URL || process.env.NORI_E2E_API_URL || "http://127.0.0.1:3210");
if (target.protocol !== "http:" || !["localhost", "127.0.0.1", "[::1]"].includes(target.hostname)) {
  throw new Error("NORI_E2E_URL must point to the local development server.");
}
const live = process.env.NORI_E2E_URL || process.env.NORI_E2E_API_URL ? test : test.skip;
const prefix = process.env.NORI_E2E_URL ? "/api/agent" : "/api";

async function post(operation, body = {}) {
  const response = await fetch(new URL(`${prefix}/${operation}`, target), {
    method: "POST",
    headers: { "Content-Type": "application/json", Origin: target.origin },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(100_000),
  });
  const data = await response.json();
  if (!response.ok) throw new Error(`${operation}: HTTP ${response.status}: ${data.error}`);
  return { status: response.status, data };
}

async function settled() {
  const deadline = Date.now() + 90_000;
  while (Date.now() < deadline) {
    const { data } = await post("session");
    if (!data.conversation.pending && !data.conversation.processing) return data;
    await Bun.sleep(200);
  }
  throw new Error("Conversation did not finish within 90 seconds.");
}

function replyFor(session, inputIDs) {
  const turn = session.traces.find((trace) => trace.status === "completed" && inputIDs.every((id) => trace.input_ids.includes(id)));
  expect(turn).toBeDefined();
  expect(turn.model).toBe("meta/muse-spark-1.3");
  const message = session.messages.find((item) => item.role === "assistant" && item.turn_id === turn.turn_id && !item.run_id);
  expect(message).toBeDefined();
  return message;
}

function expectNoWork(before, after) {
  expect(after.dispatches).toEqual(before.dispatches);
  expect(after.runs.map((run) => [run.id, run.revision])).toEqual(
    before.runs.map((run) => [run.id, run.revision]),
  );
}

live("a fresh greeting is saved once, and ordinary chat retries create no duplicate work", async () => {
  const initial = await Promise.all([post("session"), post("session"), post("session")]);
  for (const { data } of initial) {
    expect(data.conversation.revision).toBe(1);
    expect(data.conversation.introducing || data.messages.some((message) => message.role === "assistant")).toBe(true);
  }
  const before = await settled();
  expect(before.messages).toHaveLength(1);
  expect(before.messages[0].role).toBe("assistant");
  expect(before.messages[0].text).toMatch(/\bNori\b/);
  expect(before.conversation.introducing).toBe(false);
  expect(before.usage.filter((usage) => usage.provider === "outer")).toHaveLength(1);
  expect(before.usage.some((usage) => usage.provider === "jev")).toBe(false);
  expect(before.runs).toHaveLength(0);
  const reloaded = await settled();
  expect(reloaded.messages).toEqual(before.messages);
  expect(reloaded.usage).toEqual(before.usage);

  const input = { id: crypto.randomUUID(), message: "What is 2 + 2? Reply with only the number. Just answer; do not start work." };
  expect((await post("turn", input)).status).toBe(202);
  const after = await settled();
  expectNoWork(before, after);
  expect(after.classification_hint.available).toBe(true);
  expect(after.classification_hint.intents).toContain("ask");
  const reply = replyFor(after, [input.id]);
  expect(reply.text.trim()).toMatch(/^4\.?$/);

  expect((await post("turn", input)).status).toBe(202);
  const replay = await settled();
  expectNoWork(after, replay);
  expect(replay.messages).toEqual(after.messages);
  expect(replay.usage).toEqual(after.usage);
  const conflict = await fetch(new URL(`${prefix}/turn`, target), {
    method: "POST", headers: { "Content-Type": "application/json", Origin: target.origin },
    body: JSON.stringify({ ...input, message: "Different text with an already accepted ID." }),
  });
  expect(conflict.status).toBe(409);
}, 200_000);

live("rapid calendar follow-ups produce one ordered hypothetical reply without dispatch", async () => {
  const before = await settled();
  const inputs = [
    "whats on my calendar this week? respond with action type and pretend response",
    "Correction: make the pretend example for Friday specifically. Label it hypothetical; do not access my real calendar or start real work.",
    "Include the name of the actual dispatch action that would be needed to check a connected calendar, but keep the whole pretend answer brief.",
  ].map((message) => ({ id: crypto.randomUUID(), message }));
  for (const input of inputs) expect((await post("turn", input)).status).toBe(202);
  const after = await settled();
  expectNoWork(before, after);
  expect(after.classification_hint.available).toBe(true);
  expect(after.classification_hint.intents).toContain("ask");
  const reply = replyFor(after, inputs.map((input) => input.id));
  expect(reply.text).toMatch(/hypothetical/i);
  expect(reply.text).toMatch(/dispatch/i);
  expect(reply.text).toMatch(/Friday/i);
  expect(after.messages.filter((message) => message.role === "assistant")).toHaveLength(
    before.messages.filter((message) => message.role === "assistant").length + 1,
  );
  const messages = after.messages.filter((message) => inputs.some((input) => message.id === `${input.id}-user`));
  expect(messages.map((message) => message.text)).toEqual(inputs.map((input) => input.message));
}, 110_000);

live("a native tool-only dispatch publishes a receipt acknowledgment and is not replayed", async () => {
  const before = await settled();
  const known = new Set(before.runs.map((run) => run.id));
  const input = { id: crypto.randomUUID(), message: "Use exactly one native dispatch tool call with no assistant text. Start a background task with these instructions: use request_input to ask me which color I prefer, then wait for my answer. Do not use external tools or complete the task before I answer." };
  let runID;
  try {
    await post("turn", input);
    const accepted = await settled();
    const runs = accepted.runs.filter((run) => !known.has(run.id));
    expect(runs).toHaveLength(1);
    runID = runs[0].id;
    const reply = replyFor(accepted, [input.id]);
    expect(reply.text).toBe("I've accepted your task. I'll let you know when it needs your input or has a result.");
    const attempt = accepted.traces.find((trace) => trace.turn_id === reply.turn_id);
    expect(attempt.actions).toHaveLength(1);
    expect(attempt.actions[0].status).toBe("accepted");
    expect(attempt.actions[0].command.run_id).toBe(runID);
    await post("turn", input);
    const replay = await settled();
    expect(replay.runs.filter((run) => !known.has(run.id)).map((run) => run.id)).toEqual([runID]);
    expect(replay.traces.filter((trace) => trace.input_ids.includes(input.id))).toHaveLength(1);
  } finally {
    if (runID) await post("command", { id: crypto.randomUUID(), run_id: runID, kind: "stop" });
  }
}, 120_000);
