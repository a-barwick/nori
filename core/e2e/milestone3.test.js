import { beforeAll, expect, test } from "bun:test";

// Explicit opt-in: a fresh isolated database, local API/proxy and real providers.
const configured = process.env.NORI_M3_E2E_URL || process.env.NORI_M3_E2E_API_URL;
const target = new URL(configured || "http://127.0.0.1");
if (target.protocol !== "http:" || !["localhost", "127.0.0.1", "[::1]"].includes(target.hostname)) {
  throw new Error("NORI_M3_E2E_URL / NORI_M3_E2E_API_URL must point to a loopback HTTP server.");
}
const journey = configured ? test : test.skip;
const prefix = process.env.NORI_M3_E2E_URL ? "/api/agent" : "/api";
const terminal = (run) => ["completed", "cancelled", "failed"].includes(run?.status);
const waiting = (run) => run?.status === "running" && run.question?.kind === "input" && !run.question.resolved_by;

async function request(operation, body = {}) {
  return fetch(new URL(`${prefix}/${operation}`, target), {
    method: "POST",
    headers: { "Content-Type": "application/json", Origin: target.origin },
    body: JSON.stringify(body),
    signal: AbortSignal.timeout(15_000),
  });
}
async function post(operation, body = {}, status = 200) {
  const response = await request(operation, body);
  const data = await response.json();
  expect(response.status, `${operation}: expected HTTP ${status}, received ${response.status}: ${data.error || ""}`).toBe(status);
  return data;
}
const session = () => post("session");
const trace = async (runID) => (await post("trace", { run_id: runID })).steps;
const turn = (message) => post("turn", { id: crypto.randomUUID(), message }, 202);
const command = (runID, kind, extra = {}) => post("command", { id: crypto.randomUUID(), run_id: runID, kind, ...extra }, 202);

async function until(read, accepts, label, timeout = 120_000, interval = 200) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await read();
    if (accepts(last)) return last;
    await Bun.sleep(interval);
  }
  throw new Error(`Timed out waiting for ${label}. Last state: ${JSON.stringify(last).slice(0, 700)}`);
}
const settled = () => until(session, (value) => !value.conversation.pending && !value.conversation.processing, "conversation routing to settle");
const newRuns = (snapshot, before) => snapshot.runs.filter((run) => !before.has(run.id));

// The suite requires exclusive fresh data; only tasks added by this journey are stopped.
async function cleanup(before) {
  try {
    for (const run of newRuns(await session(), before)) {
      if (terminal(run)) continue;
      const response = await request("command", { id: crypto.randomUUID(), run_id: run.id, kind: "stop" });
      if (!response.ok && response.status !== 409) console.warn(`Cleanup stop for ${run.id}: HTTP ${response.status}`);
    }
  } catch (error) {
    console.warn(`Journey cleanup could not finish: ${error.message}`);
  }
}

beforeAll(async () => {
  if (!configured) return;
  const initial = await session();
  expect(initial.runs, "Milestone 3 E2E requires a fresh isolated database with no tasks.").toHaveLength(0);
  expect(initial.paused, "Start the isolated worker/API unpaused before running this suite.").toBe(false);
});

journey("two browser tasks wait independently; exact answers replay once and targeted Stop affects only its task", async () => {
  const before = new Set((await settled()).runs.map((run) => run.id));
  try {
    await turn("Start exactly two independent tasks now, one for https://example.org and one for https://example.com. " +
      "For EACH task: navigate to its own website with the browser, capture a screenshot, then use wait_for_input to ask whether to finish that website report. " +
      "Use only browser navigation, screenshot and waiting tools; do not run shell commands or fill or submit forms. " +
      "Do not finish either task before its answer. After an answer, write a detailed report of the observed page. " +
      "Each dispatched request must name only its own website; do not combine them into one task.");
    const routed = newRuns(await settled(), before);
    expect(routed, "The outer turn must accept exactly two independent tasks before either worker finishes.").toHaveLength(2);
    const org = routed.find((run) => /example\.org/i.test(run.request));
    const com = routed.find((run) => /example\.com/i.test(run.request));
    expect(org, "One dispatched task must belong to example.org.").toBeDefined();
    expect(com, "One dispatched task must belong to example.com.").toBeDefined();
    expect(org.id).not.toBe(com.id);

    const simultaneous = await until(session, (value) => [org, com].every(({ id }) => {
      const run = value.runs.find((item) => item.id === id);
      return waiting(run) && run.artifacts.some((artifact) => artifact.kind === "screenshot");
    }), "both browser tasks to retain screenshots while simultaneously waiting for input");
    const orgWaiting = simultaneous.runs.find((run) => run.id === org.id);
    const comWaiting = simultaneous.runs.find((run) => run.id === com.id);
    for (const run of [orgWaiting, comWaiting]) {
      const artifact = run.artifacts.find((item) => item.kind === "screenshot");
      const response = await request("artifact", { run_id: run.id, artifact_id: artifact.id });
      expect(response.status, `Screenshot for ${run.id} must be retrievable.`).toBe(200);
      expect(response.headers.get("content-type")).toContain("image/png");
      const bytes = new Uint8Array(await response.arrayBuffer());
      expect([...bytes.slice(0, 8)]).toEqual([137, 80, 78, 71, 13, 10, 26, 10]);
      expect(bytes.length, "Screenshot must contain image data beyond its PNG signature.").toBeGreaterThan(100);
    }

    const answer = { id: crypto.randomUUID(), run_id: org.id, kind: "answer", question_id: orgWaiting.question.id,
      text: "Yes, finish the example.org report now. Describe the observed title, text and purpose in detail, and mention the saved screenshot. Do not change the other task." };
    const accepted = await post("command", answer, 202);
    const duplicate = await post("command", answer, 202);
    for (const field of ["id", "run_id", "kind", "text", "question_id", "position", "revision", "created_at"]) {
      expect(duplicate.command[field], `Replaying the exact answer must retain its accepted ${field}.`).toEqual(accepted.command[field]);
    }
    expect(duplicate.run.position).toBe(accepted.run.position);
    const stale = await request("command", { ...answer, id: crypto.randomUUID() });
    expect(stale.status, "A distinct answer to the already resolved question must be rejected.").toBe(409);

    const finished = await until(session, (value) => terminal(value.runs.find((run) => run.id === org.id)), "example.org report to finish");
    const orgResult = finished.runs.find((run) => run.id === org.id);
    const comStillWaiting = finished.runs.find((run) => run.id === com.id);
    expect(orgResult.status, orgResult.error || "The answered task should complete.").toBe("completed");
    expect(orgResult.result).toMatch(/example(?:\.org| domain)/i);
    expect(orgResult.result.trim().split(/\s+/).length, "The completed report should include useful detail.").toBeGreaterThan(40);
    expect(orgResult.position, "The duplicate and stale answers must not add command positions.").toBe(accepted.run.position);
    for (const field of ["status", "revision", "position", "question", "artifacts"]) {
      expect(comStillWaiting[field], `Answering example.org must leave example.com's ${field} unchanged.`).toEqual(comWaiting[field]);
    }
    await command(com.id, "stop");
    const stopped = await until(session, (value) => terminal(value.runs.find((run) => run.id === com.id)), "targeted example.com Stop");
    expect(stopped.runs.find((run) => run.id === com.id).status).toBe("cancelled");
    expect(stopped.runs.find((run) => run.id === org.id).status).toBe("completed");
    expect(newRuns(stopped, before), "Answers and Stop must not create more tasks.").toHaveLength(2);
  } finally {
    await cleanup(before);
  }
}, 300_000);

journey("a correction during real model work preserves trip constraints and produces a detailed Portland result", async () => {
  const before = new Set((await settled()).runs.map((run) => run.id));
  try {
    await turn("Start one writing task: plan a Seattle trip for November 14–16, 2026 with a $500 total budget. " +
      "First use wait_for_input to ask what kind of hotel I want; do not draft the itinerary until I answer. " +
      "Then write a detailed approximately 900-word itinerary with daily activities, lodging, meals and an itemized budget. " +
      "Use your own knowledge without browser or shell tools, do not book anything, and do not ask further questions after the hotel answer.");
    const routed = newRuns(await settled(), before);
    expect(routed, "Trip planning must dispatch exactly one task.").toHaveLength(1);
    const id = routed[0].id;
    const waitingSession = await until(session, (value) => waiting(value.runs.find((run) => run.id === id)), "the trip task's hotel question");
    const hotel = waitingSession.runs.find((run) => run.id === id);
    expect(hotel.question.text).toMatch(/hotel|lodg|accommodat/i);
    const priorModels = new Set((await trace(id)).filter((step) => step.kind === "model").map((step) => step.id));
    await command(id, "answer", { question_id: hotel.question.id, text: "A budget hotel downtown. Please write the full approximately 900-word plan now, using all the original dates and budget." });
    await until(() => trace(id), (steps) => steps.some((step) => step.kind === "model" && step.status === "running" && !priorModels.has(step.id)),
      "an actual model request after the hotel answer, before sending the correction", 120_000, 50);
    await turn(`Correction for the existing trip task ${id}: change the destination from Seattle to Portland, Oregon. ` +
      "Keep November 14–16, 2026, the $500 total budget, the budget hotel downtown, and the approximately 900-word detailed itinerary. " +
      "Apply this to that same task, start no additional task, and complete the corrected plan without tools or further questions.");
    const finished = await until(session, (value) => !value.conversation.pending && !value.conversation.processing && terminal(value.runs.find((run) => run.id === id)),
      "the corrected Portland plan to finish", 300_000);
    const result = finished.runs.find((run) => run.id === id);
    expect(result.status, result.error || "The corrected trip task should complete.").toBe("completed");
    expect(result.result.slice(0, 400), "The result must identify Portland as the destination.").toMatch(/\bPortland\b/i);
    expect(result.result).toMatch(/Nov(?:ember)?\.?\s*14(?:th)?/i);
    expect(result.result).toMatch(/\b16(?:th)?\b/);
    expect(result.result).toMatch(/500/);
    expect(result.result).toMatch(/hotel/i);
    expect(result.result).toMatch(/downtown/i);
    expect(result.result.trim().split(/\s+/).length, "The corrected result must retain a detailed answer, beyond the old small output allowance.").toBeGreaterThan(500);
    expect(newRuns(finished, before), "The conversation correction must steer the existing task rather than start another.").toHaveLength(1);
    const steps = await trace(id);
    expect(steps.some((step) => step.kind === "model" && step.status === "completed" && step.revision === result.revision &&
      (step.output_tokens || 0) + (step.thinking_tokens || 0) > 1024), "A successful corrected model response must exceed 1024 combined output/thinking tokens.").toBe(true);
  } finally {
    await cleanup(before);
  }
}, 480_000);
