#!/usr/bin/env node
/**
 * Full-stack smoke test: boots real cmd/nori-local (backed by an ephemeral Postgres DB and
 * a loopback OpenAI model stub) behind web/scripts/prod-check.mjs and the built Next.js
 * standalone server (.next/standalone/server.js), then verifies health, auth gating,
 * capabilities, snapshot, and a real chat turn delivered over SSE.
 */
import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { randomBytes, randomUUID } from "node:crypto";
import { cp, mkdtemp, readFile, rm } from "node:fs/promises";
import http from "node:http";
import net from "node:net";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const CORE_DIR = join(ROOT, "core");
const WEB_DIR = join(ROOT, "web");
const BASE_PG_URL = process.env.NORI_TEST_POSTGRES_URL;
if (!BASE_PG_URL) {
  console.error("Set NORI_TEST_POSTGRES_URL (loopback Postgres URL, e.g. postgres://nori@127.0.0.1:5432/postgres?sslmode=disable)");
  process.exit(2);
}

const SMOKE_EMAIL = "smoke@example.com";
const SMOKE_MESSAGE = "Hello fullstack smoke";
const SMOKE_REPLY = "Smoke reply from real core";
const SMOKE_WELCOME = "Smoke welcome from real core";
const modelRequests = [];

function freePort() {
  return new Promise((resolvePort, reject) => {
    const srv = net.createServer();
    srv.unref();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address();
      srv.close(() => resolvePort(port));
    });
  });
}

function startModelStub() {
  const server = http.createServer(async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString("utf8")) : {};
    modelRequests.push(`${req.method} ${req.url}`);
    if (req.url === "/v1/chat/completions") {
      const schemaName = body.response_format?.json_schema?.name;
      // Nori's welcome turn runs before the user's first message; answer it with distinct text.
      const text = JSON.stringify(body.messages ?? []).includes(SMOKE_MESSAGE) ? SMOKE_REPLY : SMOKE_WELCOME;
      const content = schemaName === "nori_response" ? JSON.stringify({ actions: [], text }) : text;
      const metadata = {
        model: body.model || "gpt-6.1-terra",
        service_tier: "default",
        usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 },
      };
      if (body.stream) {
        const lines = [
          { choices: [{ delta: { content }, finish_reason: null }] },
          { choices: [{ delta: {}, finish_reason: "stop" }] },
          { choices: [], ...metadata },
        ].map((c) => `data: ${JSON.stringify(c)}\n\n`).join("") + "data: [DONE]\n\n";
        res.writeHead(200, { "Content-Type": "text/event-stream" });
        res.end(lines);
      } else {
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({
          ...metadata,
          choices: [{ message: { role: "assistant", content }, finish_reason: "stop" }],
        }));
      }
      return;
    }
    res.writeHead(404).end();
  });
  return new Promise((resolveServer, reject) => {
    server.on("error", reject);
    server.listen(0, "127.0.0.1", () => resolveServer(server));
  });
}

async function waitFor(label, fn, timeoutMs = 30_000) {
  const deadline = Date.now() + timeoutMs;
  let lastErr;
  while (Date.now() < deadline) {
    try {
      const result = await fn();
      if (result) return result;
    } catch (err) {
      lastErr = err;
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error(`Timed out waiting for ${label}${lastErr ? `: ${lastErr.message}` : ""}`);
}

async function main() {
  const work = await mkdtemp(join(tmpdir(), "nori-fullstack-smoke-"));
  const dbName = `nori_sim_${randomBytes(5).toString("hex")}`;
  const adminURL = new URL(BASE_PG_URL);
  if (!["127.0.0.1", "localhost"].includes(adminURL.hostname)) {
    throw new Error(`Refusing non-loopback Postgres host: ${adminURL.hostname}`);
  }
  const dbURL = new URL(BASE_PG_URL);
  dbURL.pathname = `/${dbName}`;

  const procs = [];
  let modelStub;
  const binDir = process.env.NORI_SMOKE_BIN_DIR || join(work, "bin");
  const startProc = (cmd, args, opts) => {
    const child = spawn(cmd, args, { stdio: ["ignore", "pipe", "pipe"], ...opts });
    let logs = "";
    const append = (chunk) => { logs = (logs + chunk.toString("utf8")).slice(-8000); };
    child.stdout.on("data", append);
    child.stderr.on("data", append);
    child.getLogs = () => logs;
    procs.push(child);
    return child;
  };

  try {
    if (!process.env.NORI_SMOKE_BIN_DIR) {
      execFileSync("go", ["build", "-o", `${binDir}/`, "./cmd/nori-local", "./cmd/nori-user", "./cmd/nori-sim"], {
        cwd: CORE_DIR,
        stdio: "inherit",
      });
    }

    // Create isolated ephemeral Postgres database using nori-sim's pgx helper (no psql dependency).
    execFileSync(join(binDir, "nori-sim"), ["--create-db", dbURL.toString()], { stdio: "inherit" });

    modelStub = await startModelStub();
    const modelURL = `http://127.0.0.1:${modelStub.address().port}`;
    const corePort = await freePort();
    const webPort = await freePort();

    const coreEnv = {
      ...process.env,
      NORI_ENV_FILE: "/dev/null",
      NORI_CLOUD: "local",
      NORI_ENV_PROFILE: "test",
      NORI_STORE_BACKEND: "postgres",
      NORI_POSTGRES_URL: dbURL.toString(),
      NORI_LOCAL_ARTIFACT_DIR: join(work, "artifacts"),
      NORI_BACKGROUND_TASKS_ENABLED: "true",
      NORI_JEV_ENABLED: "false",
      NORI_CREDENTIAL_STORE: "database",
      NORI_CREDENTIAL_KEY_FILE: join(work, "cred.key"),
      NORI_MODEL_PROVIDER: "openai",
      NORI_OUTER_PROVIDER: "openai",
      NORI_INNER_PROVIDER: "openai",
      NORI_AGENT_MODEL: "gpt-6.1-sol",
      MODEL_NAME: "gpt-6.1-terra",
      OPENAI_TEST_API_KEY: "nori-loopback-stub",
      NORI_MODEL_TEST_URL: modelURL,
      PORT: String(corePort),
    };

    const userID = randomUUID();
    execFileSync(join(binDir, "nori-user"), ["-id", userID, "-email", SMOKE_EMAIL, "-name", "Smoke User"], {
      env: coreEnv,
      stdio: "inherit",
    });
    execFileSync(
      join(binDir, "nori-user"),
      ["bind-identity", "--id", userID, "--issuer", "https://127.0.0.1:9443", "--subject", `prod-check:${SMOKE_EMAIL}`],
      { env: coreEnv, stdio: "inherit" },
    );

    const core = startProc(join(binDir, "nori-local"), [], { cwd: work, env: coreEnv });
    await waitFor("nori-local /readyz", async () => {
      if (core.exitCode !== null) throw new Error(`nori-local exited ${core.exitCode}:\n${core.getLogs()}`);
      const res = await fetch(`http://127.0.0.1:${corePort}/readyz`);
      return res.status === 200;
    });

    const prodCheckDir = join(work, "prod-check");
    const bridge = startProc(
      process.execPath,
      [join(WEB_DIR, "scripts/prod-check.mjs"), `http://127.0.0.1:${corePort}`, SMOKE_EMAIL],
      { cwd: WEB_DIR, env: { ...process.env, NORI_PROD_CHECK_DIR: prodCheckDir } },
    );
    const serviceKey = await waitFor("prod-check service-key", async () => {
      if (bridge.exitCode !== null) throw new Error(`prod-check exited ${bridge.exitCode}:\n${bridge.getLogs()}`);
      const key = (await readFile(join(prodCheckDir, "service-key"), "utf8")).trim();
      return key || null;
    });

    await cp(join(WEB_DIR, ".next/static"), join(WEB_DIR, ".next/standalone/.next/static"), { recursive: true });
    const web = startProc(process.execPath, [join(WEB_DIR, ".next/standalone/server.js")], {
      cwd: WEB_DIR,
      env: {
        ...process.env,
        HOSTNAME: "127.0.0.1",
        PORT: String(webPort),
        NORI_CLOUD: "aws",
        NORI_AUTH_PROVIDER: "oidc",
        NORI_OIDC_ISSUER: "https://127.0.0.1:9443",
        NORI_OIDC_AUDIENCE: "nori-prod-check",
        NORI_OIDC_COOKIE: "nori_prod_check",
        NORI_RUNTIME_URL: "https://127.0.0.1:9444",
        NORI_SERVICE_TOKEN_KEY: serviceKey,
        NORI_APP_ORIGIN: `http://127.0.0.1:${webPort}`,
        NODE_EXTRA_CA_CERTS: join(prodCheckDir, "ca.pem"),
      },
    });
    const webBase = `http://127.0.0.1:${webPort}`;
    await waitFor("nori-web /api/health", async () => {
      if (web.exitCode !== null) throw new Error(`nori-web exited ${web.exitCode}:\n${web.getLogs()}`);
      const res = await fetch(`${webBase}/api/health`);
      return res.status === 200;
    });

    // 1. Unauthenticated API call must be rejected.
    const unauth = await fetch(`${webBase}/api/v1/capabilities`, { headers: { "Sec-Fetch-Site": "same-origin" } });
    assert.equal(unauth.status, 403, `Expected 403 unauthenticated, got ${unauth.status}`);

    // 2. Sign in through prod-check OIDC redirect to obtain the session cookie.
    const signIn = await fetch(`http://127.0.0.1:9445/sign-in?to=${encodeURIComponent(`${webBase}/`)}`, {
      redirect: "manual",
    });
    assert.equal(signIn.status, 302);
    const cookie = (signIn.headers.get("set-cookie") || "").split(";")[0];
    assert.match(cookie, /^nori_prod_check=/, "Missing OIDC session cookie");

    // 3. Capabilities and snapshot through real Web -> Core proxy.
    const browser = { Cookie: cookie, "Sec-Fetch-Site": "same-origin" };
    const capsRes = await fetch(`${webBase}/api/v1/capabilities`, { headers: browser });
    assert.equal(capsRes.status, 200, `capabilities status=${capsRes.status}: ${capsRes.status === 200 ? "" : await capsRes.clone().text()}`);
    const caps = await capsRes.json();
    assert.equal(caps.service, "nori");
    assert.equal(caps.contract, "nori-chat-2");

    const snapRes = await fetch(`${webBase}/api/v1/snapshot`, { headers: browser });
    assert.equal(snapRes.status, 200, `snapshot status=${snapRes.status}`);
    const snap = await snapRes.json();

    // 4. Open real SSE stream from the snapshot cursor and wait for Nori's welcome,
    //    as a new user does before typing.
    const sseController = new AbortController();
    const sseRes = await fetch(`${webBase}/api/v1/events?after=${snap.position ?? 0}`, {
      headers: { ...browser, Accept: "text/event-stream" },
      signal: sseController.signal,
    });
    assert.equal(sseRes.status, 200, `events status=${sseRes.status}`);
    const reader = sseRes.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    const readUntil = async (text) => {
      const deadline = Date.now() + 20_000;
      while (!buffer.includes(text) && Date.now() < deadline) {
        let timer;
        const { value, done } = await Promise.race([
          reader.read(),
          new Promise((_, reject) => { timer = setTimeout(() => reject(new Error("SSE read timeout")), Math.max(1000, deadline - Date.now())); }),
        ]).finally(() => clearTimeout(timer));
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
      }
      assert.ok(buffer.includes(text), `Did not receive "${text}" over SSE. Buffer:\n${buffer}`);
    };
    const alreadyWelcomed = (snap.messages ?? []).some((m) => m.role === "assistant" && m.text?.includes(SMOKE_WELCOME));
    if (!alreadyWelcomed) {
      await readUntil(SMOKE_WELCOME);
    }

    // 5. Send a turn through Web -> Core -> ModelStub and assert the assistant reply arrives.

    const turnRes = await fetch(`${webBase}/api/turn`, {
      method: "POST",
      headers: {
        ...browser,
        Origin: webBase,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({ id: randomUUID(), message: SMOKE_MESSAGE }),
    });
    assert.equal(turnRes.status, 202, `turn status=${turnRes.status}: ${await turnRes.text()}`);

    await readUntil(SMOKE_REPLY);
    sseController.abort();
    console.log("Full-stack Web <-> Core smoke passed.");
  } catch (err) {
    console.error(`--- model stub requests ---\n${modelRequests.join("\n") || "(none)"}`);
    for (const child of procs) console.error(`--- ${child.spawnargs.slice(0, 2).join(" ")} (last 8000 bytes) ---\n${child.getLogs()}`);
    throw err;
  } finally {
    for (const child of procs) {
      if (child.exitCode === null) child.kill("SIGTERM");
    }
    modelStub?.closeAllConnections?.();
    modelStub?.close();
    try {
      execFileSync(join(binDir, "nori-sim"), ["--drop-db", dbURL.toString()], { stdio: "ignore" });
    } catch {}
    await rm(work, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
