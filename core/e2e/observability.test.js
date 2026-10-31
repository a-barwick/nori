import { afterAll, beforeAll, expect, test } from "bun:test";
import { spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { createServer as createHTTPServer } from "node:http";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { createServer as createTCPServer } from "node:net";
import { tmpdir } from "node:os";
import { isAbsolute, resolve } from "node:path";
import { parseEnv } from "node:util";
import { gunzipSync, inflateSync } from "node:zlib";

// Opt in with NORI_OBSERVABILITY_E2E=1, FIRESTORE_EMULATOR_HOST and
// TEMPORAL_ADDRESS set to loopback emulators, and NORI_OBSERVABILITY_MODEL_ENV_FILE
// set to the existing model credential file. The real selected providers are
// used, but telemetry is sent only to a local collector.
const live = process.env.NORI_OBSERVABILITY_E2E === "1";
const journey = live ? test : test.skip;
const core = resolve(import.meta.dir, "..");
const rootEnv = resolve(core, "../.env");
const modelEnv = process.env.NORI_OBSERVABILITY_MODEL_ENV_FILE;
const suffix = randomBytes(6).toString("hex");
const project = `nori-obs-${suffix}`;
const databases = [];
const processes = new Set();
const packets = [];
let directory, binary, collector, collectorPort, rejectExports = false, exportAttempts = 0, credentials, privateValues;

function loopback(value, name) {
  if (!value || !/^127\.0\.0\.1:\d+$/.test(value)) throw new Error(`${name} must be a loopback 127.0.0.1:port`);
  return value;
}
const firestore = live ? loopback(process.env.FIRESTORE_EMULATOR_HOST, "FIRESTORE_EMULATOR_HOST") : "";
const temporal = live ? loopback(process.env.TEMPORAL_ADDRESS, "TEMPORAL_ADDRESS") : "";
const delay = (ms) => new Promise((done) => setTimeout(done, ms));

async function until(read, accepts, label, timeout = 60_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      const value = await read();
      if (accepts(value)) return value;
    } catch { /* A local process or asynchronous result may not be ready yet. */ }
    await delay(200);
  }
  throw new Error(`Timed out waiting for ${label}`);
}

async function freePort() {
  const socket = createTCPServer();
  await new Promise((done, fail) => socket.once("error", fail).listen(0, "127.0.0.1", done));
  const port = socket.address().port;
  await new Promise((done, fail) => socket.close((error) => error ? fail(error) : done()));
  return port;
}

function decodeBody(body, encoding) {
  if (encoding === "gzip") return gunzipSync(body);
  if (encoding === "deflate") return inflateSync(body);
  if (encoding && encoding !== "identity") throw new Error("Unexpected collector compression");
  return body;
}

// Decode only the length-delimited OTLP trace nesting and each span's ID/name.
// IDs are binary in protobuf, so a string search cannot prove span correlation.
function protobufFields(bytes) {
  const fields = [];
  let offset = 0;
  function varint() {
    let value = 0n, shift = 0n;
    while (offset < bytes.length && shift <= 63n) {
      const byte = bytes[offset++];
      value |= BigInt(byte & 0x7f) << shift;
      if (!(byte & 0x80)) return Number(value);
      shift += 7n;
    }
    throw new Error("Invalid OTLP protobuf");
  }
  while (offset < bytes.length) {
    const tag = varint(), field = Math.floor(tag / 8), wire = tag % 8;
    if (!field) throw new Error("Invalid OTLP protobuf field");
    if (wire === 2) {
      const length = varint();
      if (length < 0 || offset + length > bytes.length) throw new Error("Invalid OTLP protobuf length");
      fields.push([field, bytes.subarray(offset, offset + length)]);
      offset += length;
    } else if (wire === 0) varint();
    else if (wire === 1) offset += 8;
    else if (wire === 5) offset += 4;
    else throw new Error("Unsupported OTLP protobuf wire type");
    if (offset > bytes.length) throw new Error("Truncated OTLP protobuf");
  }
  return fields;
}

function exportedSpans() {
  const spans = [];
  for (const packet of packets.filter((item) => item.path.endsWith("/integration/otlp/v1/traces/"))) {
    for (const [, resource] of protobufFields(packet.body).filter(([field]) => field === 1)) {
      for (const [, scope] of protobufFields(resource).filter(([field]) => field === 2)) {
        for (const [, span] of protobufFields(scope).filter(([field]) => field === 2)) {
          const fields = protobufFields(span);
          const trace = fields.find(([field]) => field === 1)?.[1];
          const name = fields.find(([field]) => field === 5)?.[1];
          if (trace && name) spans.push({ trace: trace.toString("hex"), name: name.toString("utf8") });
        }
      }
    }
  }
  return spans;
}

function exportedErrors() {
  const errors = [];
  for (const packet of packets.filter((item) => item.path.endsWith("/envelope/"))) {
    for (const line of packet.body.toString("utf8").split("\n")) {
      try {
        const object = JSON.parse(line);
        if (Array.isArray(object?.exception) || Array.isArray(object?.exception?.values)) errors.push(object);
      } catch { /* Envelope item headers and non-JSON data are not error events. */ }
    }
  }
  return errors;
}

async function stop(child) {
  if (!child) return;
  if (child.exitCode === null && child.signalCode === null) child.kill("SIGTERM");
  await Promise.race([child.exited, delay(8_000)]);
  if (child.exitCode === null && child.signalCode === null) {
    child.kill("SIGKILL");
    await child.exited;
  }
  processes.delete(child);
}

async function startAPI(index) {
  const database = `nori-obs-${suffix}-${index}`;
  if (!/^nori-obs-[a-f0-9]{12}-[12]$/.test(database) || !/^nori-obs-[a-f0-9]{12}$/.test(project)) throw new Error("Unsafe emulator isolation settings");
  databases.push(database);
  const documents = `http://${firestore}/v1/projects/${project}/databases/${database}/documents`;
  const control = await fetch(`${documents}/system/control`, {
    method: "PATCH", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ fields: { paused: { booleanValue: true } } }),
  });
  if (!control.ok) throw new Error(`Could not prepare isolated emulator control: HTTP ${control.status}`);
  const port = await freePort();
  const address = `http://127.0.0.1:${port}`;
  const queue = `nori-obs-${suffix}-${index}`;
  const child = spawn(binary, [], { cwd: core, stdio: "ignore", env: {
    ...process.env,
    NORI_ENV_FILE: rootEnv, NORI_TASK_ENV_FILE: rootEnv,
    OPENROUTER_API_KEY: credentials.OPENROUTER_API_KEY, JEV_API_KEY: credentials.JEV_API_KEY,
    NORI_AGENT_BASE_URL: "https://openrouter.ai/api/v1", NORI_AGENT_MODEL: "anthropic/claude-opus-5.5",
    NORI_PROJECT_ID: project, FIRESTORE_DATABASE_ID: database, FIRESTORE_EMULATOR_HOST: firestore,
    NORI_LOCAL_ARTIFACT_DIR: resolve(directory, `artifacts-${index}`), ARTIFACT_BUCKET: "unused-e2e",
    PORT: String(port), TEMPORAL_ADDRESS: temporal, TEMPORAL_NAMESPACE: "default", TEMPORAL_LOCAL: "true",
    TEMPORAL_CLOUD_RUN_ID: "false", TEMPORAL_API_KEY: "", TEMPORAL_TASK_QUEUE: queue,
    TEMPORAL_DEPLOYMENT_NAME: queue, TEMPORAL_BUILD_ID: queue,
    NORI_LOCAL_COMPUTER_URL: "", COMPUTER_ZONE: "invalid-e2e-zone", COMPUTER_INSTANCE: "unused-e2e",
    SENTRY_DSN: `http://public@127.0.0.1:${collectorPort}/1`, SENTRY_ENVIRONMENT: "e2e", SENTRY_TRACES_SAMPLE_RATE: "1",
    NORI_SENTRY_SPANS_PER_HOUR: "500", NORI_SENTRY_ERRORS_PER_HOUR: "50", NORI_SENTRY_LOGS_PER_HOUR: "100", NORI_SENTRY_METRICS_PER_HOUR: "200",
    ARIZE_SPACE_ID: "", ARIZE_API_KEY: "", ARIZE_OTLP_ENDPOINT: "", NORI_ARIZE_CAPTURE_CONTENT: "true",
  } });
  child.exited = new Promise((done) => child.once("exit", done));
  processes.add(child);
  await until(() => fetch(`${address}/healthz`, { signal: AbortSignal.timeout(1_000) }), (response) => response.ok, "isolated local API", 30_000);
  return { child, address };
}

async function post(address, operation, body, headers = {}) {
  const response = await fetch(`${address}/api/${operation}?private=NORI_PRIVATE_QUERY_${suffix}`, {
    method: "POST", headers: { "Content-Type": "application/json", ...headers },
    body: JSON.stringify(body), signal: AbortSignal.timeout(12_000),
  });
  const data = await response.json();
  if (!response.ok) throw new Error(`${operation} failed with HTTP ${response.status}`);
  return { status: response.status, data };
}

async function visibleReply(address, id) {
  return until(async () => {
    const session = (await post(address, "session", {})).data;
    const input = session.messages.find((message) => message.id === `${id}-user`);
    const attempt = session.traces?.find((trace) => trace.input_ids.includes(id) && ["completed", "error"].includes(trace.status));
    const reply = attempt && session.messages.find((message) => message.role === "assistant" && !message.run_id && message.turn_id === attempt.turn_id);
    return { session, input, attempt, reply };
  }, (value) => !!(value.input && value.attempt && value.reply), "durable conversation reply", 90_000);
}

beforeAll(async () => {
  if (!live) return;
  if (!modelEnv || !isAbsolute(modelEnv)) throw new Error("NORI_OBSERVABILITY_MODEL_ENV_FILE must name an existing absolute model credential file");
  credentials = parseEnv(readFileSync(modelEnv, "utf8"));
  const telemetry = parseEnv(readFileSync(rootEnv, "utf8"));
  if (!credentials.OPENROUTER_API_KEY || !credentials.JEV_API_KEY) throw new Error("Selected OpenRouter and Jev credentials are required in the existing model environment");
  privateValues = [credentials.OPENROUTER_API_KEY, credentials.JEV_API_KEY, telemetry.ARIZE_API_KEY, telemetry.ARIZE_SPACE_ID,
    `NORI_PRIVATE_CHAT_${suffix}`, `NORI_PRIVATE_QUERY_${suffix}`, `NORI_PRIVATE_AUTH_${suffix}`, `NORI_PRIVATE_COOKIE_${suffix}`, `NORI_PRIVATE_URL_${suffix}`].filter(Boolean);
  directory = mkdtempSync(resolve(tmpdir(), "nori-observability-e2e-"));
  binary = resolve(directory, "nori-local");
  const build = spawnSync("go", ["build", "-o", binary, "./cmd/nori-local"], { cwd: core, env: process.env, stdio: "ignore", timeout: 120_000 });
  if (build.status !== 0) throw new Error("Could not build the isolated Go API");
  collector = createHTTPServer((request, response) => {
    exportAttempts++;
    if (rejectExports) { request.socket.destroy(); return; }
    const chunks = [];
    request.on("data", (chunk) => chunks.push(chunk));
    request.on("end", () => {
      try {
        packets.push({ path: request.url, body: decodeBody(Buffer.concat(chunks), request.headers["content-encoding"]), headers: JSON.stringify(request.headers) });
        response.writeHead(200, { "Content-Type": request.url.endsWith("/envelope/") ? "application/json" : "application/x-protobuf" });
        response.end(request.url.endsWith("/envelope/") ? "{}" : "");
      } catch { response.writeHead(400); response.end(); }
    });
  });
  await new Promise((done, fail) => collector.once("error", fail).listen(0, "127.0.0.1", done));
  collectorPort = collector.address().port;
}, 140_000);

afterAll(async () => {
  if (!live) return;
  for (const child of [...processes]) await stop(child);
  if (collector) await new Promise((done) => collector.close(done));
  for (const database of databases) {
    if (/^nori-obs-[a-f0-9]{12}-[12]$/.test(database) && /^nori-obs-[a-f0-9]{12}$/.test(project)) {
      const response = await fetch(`http://${firestore}/emulator/v1/projects/${project}/databases/${database}/documents`, { method: "DELETE" });
      if (!response.ok) throw new Error(`Could not clean isolated emulator database: HTTP ${response.status}`);
    }
  }
  if (directory) rmSync(directory, { recursive: true, force: true });
});

journey("a saved turn produces a traced, private reply or provider-failure event", async () => {
  const { child, address } = await startAPI(1);
  const id = crypto.randomUUID();
  const trace = randomBytes(16).toString("hex"), parent = randomBytes(8).toString("hex");
  const message = `NORI_PRIVATE_CHAT_${suffix} https://example.invalid/NORI_PRIVATE_URL_${suffix} — Reply READY only. Do not start tasks or use tools.`;
  const accepted = await post(address, "turn", { id, message }, {
    traceparent: `00-${trace}-${parent}-01`, Authorization: `Bearer NORI_PRIVATE_AUTH_${suffix}`,
    Cookie: `session=NORI_PRIVATE_COOKIE_${suffix}`, baggage: `private=NORI_PRIVATE_CHAT_${suffix}`,
  });
  expect(accepted.status).toBe(202);
  const result = await visibleReply(address, id);
  expect(result.input.text === message).toBe(true);
  expect(result.reply.text.length > 0).toBe(true);
  expect(result.attempt.model).toBe("anthropic/claude-opus-5.5");
  if (result.attempt.status === "error") expect(result.reply.text.includes("Your message is saved")).toBe(true);
  expect(result.session.runs.length).toBe(0);
  await until(() => exportedSpans(), (spans) => spans.some((span) => span.trace === trace && span.name === "conversation.acceptance_to_reply"), "exported reply trace", 15_000);
  await stop(child);
  const names = exportedSpans().filter((span) => span.trace === trace).map((span) => span.name);
  expect(names.includes("HTTP /api/turn")).toBe(true);
  expect(names.includes("conversation.accept")).toBe(true);
  expect(names.includes("conversation.turn")).toBe(true);
  expect(names.includes("conversation.acceptance_to_reply")).toBe(true);
  if (result.attempt.status === "error") {
    const event = exportedErrors().find((item) => item.tags?.["nori.error.category"] === "model.outer" && item.contexts?.trace?.trace_id === trace);
    expect(!!event).toBe(true);
    const exceptions = Array.isArray(event?.exception) ? event.exception : event?.exception?.values || [];
    expect(exceptions.some((exception) => exception.stacktrace?.frames?.length > 0)).toBe(true);
  }
  for (const packet of packets) for (const value of privateValues) {
    expect(packet.body.includes(Buffer.from(value))).toBe(false);
    expect(packet.headers.includes(value)).toBe(false);
  }
}, 120_000);

journey("an unavailable telemetry receiver does not prevent a durable conversation reply", async () => {
  rejectExports = true;
  const before = exportAttempts;
  const { child, address } = await startAPI(2);
  const id = crypto.randomUUID();
  const message = `NORI_PRIVATE_CHAT_${suffix} — Reply READY only. Do not start tasks or use tools.`;
  const start = performance.now();
  const accepted = await post(address, "turn", { id, message });
  expect(accepted.status).toBe(202);
  expect(performance.now() - start < 10_000).toBe(true);
  const result = await visibleReply(address, id);
  expect(result.input.text === message).toBe(true);
  expect(result.reply.text.length > 0).toBe(true);
  expect(result.session.runs.length).toBe(0);
  expect(exportAttempts > before).toBe(true);
  await stop(child);
}, 120_000);
