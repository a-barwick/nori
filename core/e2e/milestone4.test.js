import { beforeAll, expect, test } from "bun:test";
import { spawnSync } from "node:child_process";
import { randomBytes, createHash } from "node:crypto";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { resolve } from "node:path";

// Real-provider journeys use an already running isolated local stack. Invoke
// through infrastructure/spend/run.py; this file never loads credentials or
// changes provider/model settings. The storage journey is separately unpaid.
const configured = process.env.NORI_M4_E2E_API_URL;
const target = loopbackURL(configured || "http://127.0.0.1", "NORI_M4_E2E_API_URL");
const journey = configured ? test : test.skip;
const storageJourney = process.env.NORI_M4_STORAGE_E2E === "1" ? test : test.skip;
const terminal = (run) => ["completed", "partial", "unverified", "cancelled", "failed"].includes(run?.status);
const waiting = (run) => run?.status === "running" && run.question?.kind === "input" && !run.question.resolved_by;
const emulator = process.env.NORI_M4_E2E_FIRESTORE_URL;
const documents = emulator ? loopbackURL(emulator, "NORI_M4_E2E_FIRESTORE_URL") : null;
const temporal = process.env.NORI_M4_E2E_TEMPORAL_ADDRESS;
const created = new Set();
const prefix = `M4_${randomBytes(5).toString("hex")}`;

function loopbackURL(value, name) {
  const url = new URL(value);
  if (url.protocol !== "http:" || !["localhost", "127.0.0.1", "[::1]"].includes(url.hostname)) {
    throw new Error(`${name} must be a loopback HTTP URL.`);
  }
  return url;
}
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
const steps = async (id) => (await post("trace", { run_id: id })).steps;
const turn = (message) => post("turn", { id: crypto.randomUUID(), message }, 202);
const command = (id, kind, extra = {}) => post("command", { id: crypto.randomUUID(), run_id: id, kind, ...extra }, 202);
const find = (snapshot, id) => snapshot.runs.find((run) => run.id === id);
async function until(read, accepts, label, timeout = 120_000, interval = 150) {
  const deadline = Date.now() + timeout;
  let last;
  while (Date.now() < deadline) {
    last = await read();
    if (accepts(last)) return last;
    await Bun.sleep(interval);
  }
  throw new Error(`Timed out waiting for ${label}; last state ${JSON.stringify(last).slice(0, 500)}`);
}
const settled = () => until(session, (value) => !value.conversation.pending && !value.conversation.processing, "conversation routing");
async function start(request) {
  const before = new Set((await settled()).runs.map((run) => run.id));
  await turn(`Start exactly one independent task now. Pass these instructions to the worker: ${request}`);
  const snapshot = await until(session, (value) => value.runs.some((run) => !before.has(run.id)), "one accepted task");
  const added = snapshot.runs.filter((run) => !before.has(run.id));
  expect(added, "One request must create one durable task.").toHaveLength(1);
  created.add(added[0].id);
  return added[0].id;
}
async function cleanup(id) {
  if (!id) return;
  const run = find(await session(), id);
  if (!terminal(run)) {
    const response = await request("command", { id: crypto.randomUUID(), run_id: id, kind: "stop" });
    expect([202, 409]).toContain(response.status);
  }
}
function decodeField(field) {
  if ("stringValue" in field) return field.stringValue;
  if ("integerValue" in field) return Number(field.integerValue);
  if ("doubleValue" in field) return Number(field.doubleValue);
  if ("booleanValue" in field) return field.booleanValue;
  if ("bytesValue" in field) return Buffer.from(field.bytesValue, "base64");
  if ("arrayValue" in field) return (field.arrayValue.values || []).map(decodeField);
  if ("mapValue" in field) return decodeDocument(field.mapValue);
  return null;
}
const decodeDocument = (doc) => Object.fromEntries(Object.entries(doc.fields || {}).map(([key, value]) => [key, decodeField(value)]));
async function collection(id, name) {
  if (!documents) return [];
  const url = new URL(`${documents.pathname.replace(/\/$/, "")}/owners/austinbarwickdavis@gmail.com/runs/${id}/${name}`, documents.origin);
  url.searchParams.set("pageSize", "1000");
  const response = await fetch(url, { signal: AbortSignal.timeout(10_000) });
  expect(response.status, `Read durable ${name} records.`).toBe(200);
  const body = await response.json();
  expect(body.nextPageToken, "Compact journeys must inspect every durable record, not a truncated page.").toBeUndefined();
  return (body.documents || []).map((doc) => ({ ...decodeDocument(doc), documentID: doc.name.split("/").at(-1) }));
}
async function verifyCompletion(id, run) {
  if (!documents) return;
  const checks = await collection(id, "completion_checks");
  const current = checks.filter((check) => check.revision === run.revision && check.position === run.position);
  expect(current.length, "A completed task must retain its current completion check.").toBeGreaterThan(0);
  expect(current.some((check) => check.jev_available && check.jev_choice === "yes" && check.jev_score >= 0.8 && !check.record_failures?.length)).toBe(true);
  const blocks = await collection(id, "history");
  expect(blocks.length).toBeGreaterThan(0);
  for (const block of blocks) {
    const hash = createHash("sha256").update(id).update(Buffer.from([0])).update(block.parent).update(Buffer.from([0])).update(block.data).digest("hex");
    expect(hash, "Saved history references bind exact bytes, task and parent.").toBe(block.documentID);
  }
  if (temporal) {
    if (!/^127\.0\.0\.1:\d+$/.test(temporal)) throw new Error("NORI_M4_E2E_TEMPORAL_ADDRESS must be loopback 127.0.0.1:port");
    const result = spawnSync("temporal", ["--address", temporal, "--namespace", process.env.NORI_M4_E2E_TEMPORAL_NAMESPACE || "default", "--output", "json", "workflow", "show", "--workflow-id", `nori-task-${id}`], { encoding: "utf8", timeout: 15_000 });
    expect(result.status, "Temporal history must remain inspectable after completion.").toBe(0);
    const history = JSON.parse(result.stdout);
    const events = Array.isArray(history) ? history : history.events;
    const models = events.map((event) => event.activityTaskScheduledEventAttributes).filter((activity) => activity?.activityType?.name === "ModelRef");
    expect(models.length).toBeGreaterThan(0);
    for (const model of models) {
      for (const payload of model.input?.payloads || []) {
        const input = JSON.parse(Buffer.from(payload.data, "base64").toString());
        expect(input.Contents, "Cumulative transcript must not return to Temporal inputs.").toBeUndefined();
        expect(input.Ref).toMatch(/^[a-f0-9]{64}$/);
      }
    }
  }
}

beforeAll(async () => {
  if (!configured) return;
  const initial = await session();
  expect(initial.runs, "M4 journeys require a fresh isolated database; never run against personal history.").toHaveLength(0);
  expect(initial.paused).toBe(false);
});

journey("request_input persists across reload and one exact answer selects the observed Home bill", async () => {
  let id;
  const timedCommand = async (body, label, expected = 202) => {
    const started = performance.now();
    const result = await post("command", body, expected);
    const elapsedMS = performance.now() - started;
    console.info(JSON.stringify({ measurement: "warm_command_acceptance_ms", scenario: "bills", label, elapsed_ms: Number(elapsedMS.toFixed(2)) }));
    expect(elapsedMS, `Warm durable command acceptance: ${label}`).toBeLessThanOrEqual(1000);
    return result;
  };
  try {
    id = await start(`Open http://127.0.0.1:18080/bills and report the selected bill's account, dollar amount, and reference for bill request ${prefix}. ` +
      "Before opening the page, use request_input to ask whether I mean Home or Studio, and wait for my answer. After my selection, use browser_navigate to read the actual bill page, then complete with only the selected account's observed bill details and cite the tool evidence. Do not use shell commands or change any data.");
    const paused = find(await until(session, (value) => waiting(find(value, id)), "a durable Home or Studio question"), id);
    expect(paused.question.text).toMatch(/Home/i);
    expect(paused.question.text).toMatch(/Studio/i);
    await post("resume");
    const reloaded = find(await session(), id);
    expect(reloaded.question, "Reload and worker wake preserve the exact open question.").toEqual(paused.question);
    expect(reloaded.revision).toBe(paused.revision);
    const answer = { id: crypto.randomUUID(), run_id: id, kind: "answer", question_id: paused.question.id, text: "Home. Read the bill page now and report the Home account's amount and reference." };
    const receipt = await timedCommand(answer, "answer");
    const replay = await timedCommand(answer, "duplicate_answer");
    expect(replay.command).toEqual(receipt.command);
    await timedCommand({ ...answer, id: crypto.randomUUID() }, "stale_answer", 409);
    const done = find(await until(session, (value) => terminal(find(value, id)), "verified Home bill completion after the exact answer"), id);
    expect(done.status, done.error || "The answered task must finish with verified completion.").toBe("completed");
    expect(done.result).toMatch(/Home/i);
    expect(done.result).toMatch(/\$\s*42\b|\b42\s*(?:USD|dollars)\b/i);
    expect(done.result).toContain("HOME-01");
    expect(done.result).not.toContain("STUDIO-02");
    expect(done.position).toBe(receipt.run.position);
    expect(done.question?.resolved_by || !done.question).toBeTruthy();
    const trace = await steps(id);
    expect(trace.some((step) => step.kind === "tool" && step.name === "browser_navigate" && step.arguments?.includes("http://127.0.0.1:18080/bills")), "Bill details must come from the actual browser fixture.").toBe(true);
    if (documents) {
      const operations = await collection(id, "operations");
      expect(operations.some((operation) => operation.name === "browser_navigate" && operation.outcome === "confirmed" && operation.effect_class === "read" && /^[a-f0-9]{64}$/.test(operation.result_ref)), "A confirmed browser operation must retain the observed bill evidence.").toBe(true);
      expect(operations.every((operation) => operation.effect_class === "read"), "Reading a bill must not attempt writes or arbitrary shell actions.").toBe(true);
    }
    await verifyCompletion(id, done);
  } finally { await cleanup(id); }
}, 240_000);

journey("a held shorts answer waits for the corrected Tucson weather fixture result", async () => {
  let id;
  const timedCommand = async (kind, extra, label) => {
    const started = performance.now();
    const result = await command(id, kind, extra);
    const elapsedMS = performance.now() - started;
    console.info(JSON.stringify({ measurement: "warm_command_acceptance_ms", scenario: "weather_hold", label, elapsed_ms: Number(elapsedMS.toFixed(2)) }));
    expect(elapsedMS, `Warm durable command acceptance: ${label}`).toBeLessThanOrEqual(1000);
    return result;
  };
  try {
    id = await start(`Read the synthetic Saturday weather fixture for city request ${prefix} and report the city, high, low, and conditions from the actual page. ` +
      "First use request_input to ask which city I want, and wait for my answer. The fixture URLs are http://127.0.0.1:18080/phoenix for Phoenix and http://127.0.0.1:18080/tucson for Tucson. After I answer, use browser_navigate on the selected URL, then complete with concise observed weather details and cite the saved evidence. Do not use existing weather knowledge, run shell commands, or change any data.");
    const paused = find(await until(session, (value) => waiting(find(value, id)), "the weather city question"), id);
    await turn(`When existing task ${id} is finished, answer whether I should wear shorts for the Saturday weather in its final result, and name the city and high temperature supporting your advice. Hold this question for that task; do not change its instructions or answer its city question, and start no new task.`);
    const held = find(await until(session, (value) => find(value, id)?.holds?.some((hold) => hold.status === "pending"), "a pending held shorts question"), id);
    expect(held.question).toEqual(paused.question);
    expect(held.revision).toBe(paused.revision);
    expect(held.position).toBe(paused.position);
    const holdID = held.holds.find((hold) => hold.status === "pending").id;
    const prior = new Set((await steps(id)).filter((step) => step.kind === "model").map((step) => step.id));
    await timedCommand("answer", { question_id: paused.question.id, text: "Phoenix. Read http://127.0.0.1:18080/phoenix and report its Saturday weather now." }, "city_answer");
    await until(() => steps(id), (value) => value.some((step) => step.kind === "model" && step.status === "running" && !prior.has(step.id)), "an admitted model request before the correction", 120_000, 40);
    const corrected = await timedCommand("steer", { text: "Change the city to Tucson. Read http://127.0.0.1:18080/tucson using browser_navigate and base the entire final weather answer on that fixture. Report Tucson's Saturday high, low, and conditions, not Phoenix's. Cite the actual tool evidence and complete without further questions. Do not run shell commands or change data." }, "city_correction");
    const finished = await until(session, (value) => {
      const run = find(value, id);
      return terminal(run) && run.holds?.some((hold) => hold.id === holdID && hold.status === "answered");
    }, "the corrected Tucson result and its held shorts answer", 180_000);
    const done = find(finished, id);
    expect(done.status, done.error).toBe("completed");
    expect(done.revision).toBe(corrected.run.revision);
    expect(done.result).toMatch(/Tucson/i);
    expect(done.result).toMatch(/\b82\s*(?:°\s*)?F\b|\b82\s*degrees(?:\s*Fahrenheit)?\b/i);
    expect(done.result).toMatch(/\b58\s*(?:°\s*)?F\b|\b58\s*degrees(?:\s*Fahrenheit)?\b/i);
    expect(done.result).toMatch(/sunny/i);
    expect(done.result).toMatch(/breez/i);
    expect(done.result).not.toMatch(/Phoenix|\b94\s*(?:°\s*)?F\b/i);
    const trace = await steps(id);
    expect(trace.some((step) => step.kind === "tool" && step.name === "browser_navigate" && step.arguments?.includes("http://127.0.0.1:18080/tucson")), "The corrected answer must read the actual Tucson fixture.").toBe(true);
    const hold = done.holds.find((item) => item.id === holdID);
    const answer = finished.messages.find((message) => message.role === "assistant" && message.turn_id === hold.answered_turn_id);
    expect(answer?.text, "Hold acknowledgment must name an actual delivered answer.").toMatch(/Tucson/i);
    expect(answer.text).toMatch(/shorts/i);
    expect(answer.text).toMatch(/\b82\b/);
    expect(answer.text).not.toMatch(/Phoenix|\b94\s*(?:°\s*)?F\b/i);
    expect(finished.runs.filter((run) => !created.has(run.id)), "The held question creates no additional task.").toHaveLength(0);
    if (documents) {
      const operations = await collection(id, "operations");
      expect(operations.some((operation) => operation.name === "browser_navigate" && operation.outcome === "confirmed" && operation.effect_class === "read" && /^[a-f0-9]{64}$/.test(operation.result_ref)), "Weather advice requires confirmed saved browser evidence.").toBe(true);
      expect(operations.every((operation) => operation.effect_class === "read")).toBe(true);
    }
    await verifyCompletion(id, done);
  } finally { await cleanup(id); }
}, 360_000);

journey("a failed command returns its observed failure without a duplicate action or a success claim", async () => {
  let id;
  try {
    const marker = `${prefix}_FAILED_CHECK`;
    id = await start(`Run exactly one shell command: printf '${marker}\\n'; exit 7. This deliberately fails a verification check. ` +
      "Never retry or run another shell command. Explain the observed nonzero exit truthfully, then request_input to ask what I want to do next. Do not claim verification succeeded or the requested work is complete.");
    const failed = find(await until(session, (value) => waiting(find(value, id)) || terminal(find(value, id)), "truthful failed-verification handling"), id);
    expect(failed.status, failed.error || "A failed check needs a decision, not a success result.").toBe("running");
    expect(failed.question.text).toMatch(/fail|exit|7|nonzero|unsuccessful/i);
    expect(failed.result || "").not.toMatch(/verification succeeded|successfully verified/i);
    const trace = await steps(id);
    const shell = trace.filter((step) => step.kind === "tool" && step.name === "shell");
    expect(shell, "Failed writes and arbitrary shell actions are never replayed automatically.").toHaveLength(1);
    if (documents) {
      const operations = await collection(id, "operations");
      expect(operations).toHaveLength(1);
      expect(operations[0].name).toBe("shell");
      expect(operations[0].outcome).toBe("failed");
      expect(operations[0].effect_class).toBe("unknown");
      expect(operations[0].attempt).toBe(1);
      expect(operations[0].result_ref).toMatch(/^[a-f0-9]{64}$/);
    }
    await post("resume");
    expect(find(await session(), id).question).toEqual(failed.question);
    expect((await steps(id)).filter((step) => step.kind === "tool" && step.name === "shell")).toHaveLength(1);
  } finally { await cleanup(id); }
}, 240_000);

storageJourney("real Firestore preserves exact task evidence, refuses foreign/tampered history and protects terminal records", async () => {
  const host = process.env.FIRESTORE_EMULATOR_HOST;
  if (!/^127\.0\.0\.1:\d+$/.test(host || "")) throw new Error("Unpaid storage E2E needs FIRESTORE_EMULATOR_HOST=127.0.0.1:port");
  const suffix = randomBytes(6).toString("hex");
  const project = `nori-m4-storage-${suffix}`, database = `m4-storage-${suffix}`;
  const base = `http://${host}/v1/projects/${project}/databases/${database}/documents`;
  const artifacts = mkdtempSync(resolve(tmpdir(), "nori-m4-storage-"));
  try {
    const control = await fetch(`${base}/system/control`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ fields: { paused: { booleanValue: false } } }) });
    expect(control.ok).toBe(true);
    const env = Object.fromEntries(["PATH", "HOME", "TMPDIR", "GOPATH", "GOCACHE", "GOROOT", "SSL_CERT_FILE", "SSL_CERT_DIR"].filter((key) => process.env[key]).map((key) => [key, process.env[key]]));
    const run = spawnSync("go", ["run", "./e2e/milestone4-storage"], {
      cwd: resolve(import.meta.dir, ".."), env: { ...env, FIRESTORE_EMULATOR_HOST: host, NORI_M4_STORAGE_PROJECT: project, NORI_M4_STORAGE_DATABASE: database, TEMPORAL_LOCAL: "true", NORI_LOCAL_ARTIFACT_DIR: artifacts,
        NORI_M4_STORAGE_TEMPORAL_ADDRESS: process.env.NORI_M4_STORAGE_TEMPORAL_ADDRESS || "" },
      encoding: "utf8", timeout: 90_000,
    });
    expect(run.status, `Real storage boundary executable: ${run.stderr.slice(0, 800)}`).toBe(0);
    const report = JSON.parse(run.stdout);
    for (const [name, passed] of Object.entries(report)) expect(passed, name).toBe(true);
    expect(Object.keys(report).length).toBeGreaterThanOrEqual(8);
  } finally {
    const cleaned = await fetch(`http://${host}/emulator/v1/projects/${project}/databases/${database}/documents`, { method: "DELETE" });
    expect(cleaned.ok, "Only this unique unpaid fixture database is removed.").toBe(true);
    rmSync(artifacts, { recursive: true, force: true });
  }
}, 100_000);
