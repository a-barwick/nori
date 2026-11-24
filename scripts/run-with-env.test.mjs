import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { buildChildEnv } from "./run-with-env.mjs";

const SCRIPT_PATH = resolve(fileURLToPath(new URL("./run-with-env.mjs", import.meta.url)));

const FORBIDDEN_SECRETS = [
  "AWS_ACCESS_KEY_ID",
  "AWS_SECRET_ACCESS_KEY",
  "AWS_SESSION_TOKEN",
  "AWS_REGION",
  "NORI_POSTGRES_URL",
  "NORI_POSTGRES_LISTEN_URL",
  "NORI_CREDENTIAL_KEY",
  "NORI_CREDENTIAL_KEY_SECRET",
  "NORI_CREDENTIAL_KEY_FILE",
  "OPENAI_API_KEY",
  "OPENROUTER_API_KEY",
  "OPENROUTER_MANAGEMENT_KEY",
  "ANTHROPIC_API_KEY",
  "ANTHROPIC_AUTH_TOKEN",
  "TEMPORAL_API_KEY",
  "TEMPORAL_API_KEY_SECRET_VERSION",
  "TEMPORAL_BILLING_API_KEY",
  "JEV_API_KEY",
  "BRAVE_SEARCH_API_KEY",
  "ARIZE_API_KEY",
  "NORI_MODEL_GATEWAY_API_KEY",
  "NORI_USAGE_TOKEN",
  "NORI_SAFE_PUBLIC_FLAG",
  "UNLISTED_RANDOM_VAR",
];

function withTempDir(fn) {
  const dir = mkdtempSync(join(tmpdir(), "nori-run-with-env-"));
  try {
    return fn(dir);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}

test("buildChildEnv allows web and runtime vars and omits secrets completely", () => {
  const dotEnv = {
    NEXT_PUBLIC_SENTRY_DSN: "https://pub@sentry.example/1",
    SENTRY_WEB_DSN: "https://web@sentry.example/1",
    NORI_LOCAL_AGENT_URL: "http://127.0.0.1:8080",
    NORI_OIDC_ISSUER: "https://issuer.example",
    NORI_SERVICE_AUDIENCE: "nori",
    NORI_APP_ORIGIN: "https://app.example",
    NORI_CLOUD: "local",
    NORI_RUNTIME_URL: "https://runtime.example",
    NORI_AUTH_PROVIDER: "oidc",
    NORI_DEV_ORIGINS: "http://127.0.0.1:3210",
    OWNER_EMAIL: "owner@example.com",
    PORT: "3210",
    HOSTNAME: "127.0.0.1",
    NODE_ENV: "development",
    // Runtime-only keys in .env must NOT override or inject runtime env.
    PATH: "/evil/path",
    NODE_OPTIONS: "--require=/evil.js",
  };
  const parentEnv = {
    PATH: "/usr/bin:/bin",
    HOME: "/tmp/home",
    LANG: "en_US.UTF-8",
    LC_ALL: "en_US.UTF-8",
    TZ: "UTC",
    NODE_ENV: "production",
    NORI_LOCAL_AGENT_URL: "http://127.0.0.1:9090",
    CI: "true",
    XDG_CACHE_HOME: "/tmp/xdg-cache",
  };
  for (const key of FORBIDDEN_SECRETS) {
    dotEnv[key] = `dotenv-secret-${key}`;
    parentEnv[key] = `parent-secret-${key}`;
  }

  const result = buildChildEnv(dotEnv, parentEnv);

  // Allowed web vars pass through, with parentEnv taking precedence over .env.
  assert.equal(result.NEXT_PUBLIC_SENTRY_DSN, "https://pub@sentry.example/1");
  assert.equal(result.SENTRY_WEB_DSN, "https://web@sentry.example/1");
  assert.equal(result.NORI_LOCAL_AGENT_URL, "http://127.0.0.1:9090");
  assert.equal(result.NORI_OIDC_ISSUER, "https://issuer.example");
  assert.equal(result.NORI_SERVICE_AUDIENCE, "nori");
  assert.equal(result.NORI_APP_ORIGIN, "https://app.example");
  assert.equal(result.NORI_CLOUD, "local");
  assert.equal(result.NORI_RUNTIME_URL, "https://runtime.example");
  assert.equal(result.NORI_AUTH_PROVIDER, "oidc");
  assert.equal(result.NORI_DEV_ORIGINS, "http://127.0.0.1:3210");
  assert.equal(result.OWNER_EMAIL, "owner@example.com");
  assert.equal(result.PORT, "3210");
  assert.equal(result.HOSTNAME, "127.0.0.1");
  assert.equal(result.NODE_ENV, "production");

  // Runtime vars come from parentEnv only (never from .env).
  assert.equal(result.PATH, "/usr/bin:/bin");
  assert.equal(result.HOME, "/tmp/home");
  assert.equal(result.LC_ALL, "en_US.UTF-8");
  assert.equal(result.XDG_CACHE_HOME, "/tmp/xdg-cache");
  assert.equal("NODE_OPTIONS" in result, false);

  // Every forbidden secret is completely absent (not blanked to "").
  for (const key of FORBIDDEN_SECRETS) {
    assert.equal(key in result, false, `expected ${key} to be absent from child env`);
  }
});

test("CLI strips secrets from both .env and inherited process.env while passing web vars", () => {
  withTempDir((dir) => {
    const envFile = join(dir, ".env");
    const lines = [
      "NEXT_PUBLIC_SENTRY_ENVIRONMENT=staging",
      "SENTRY_RELEASE=rel-123",
      "NORI_LOCAL_AGENT_URL=http://127.0.0.1:8080",
      "NORI_LOCAL_EMAIL=dev@example.test",
      "NORI_OIDC_COOKIE=nori_id",
      "NORI_SERVICE_TOKEN_KEY=shared-web-api-key",
      "NORI_APP_ORIGIN=https://nori.example.test",
      "NORI_CLOUD=local",
      "NORI_RUNTIME_URL=https://api.example.test",
      "NORI_AUTH_PROVIDER=oidc",
      "NORI_DEV_ORIGINS=http://localhost:3210",
      "OWNER_EMAIL=owner@example.test",
      "PORT=3210",
      "HOSTNAME=127.0.0.1",
      ...FORBIDDEN_SECRETS.map((k) => `${k}=dotenv-secret-${k}`),
    ];
    writeFileSync(envFile, `${lines.join("\n")}\n`, { mode: 0o600 });

    const parentEnv = {
      PATH: process.env.PATH,
      HOME: process.env.HOME,
      TMPDIR: process.env.TMPDIR,
      NODE_ENV: "test",
      NORI_LOCAL_AGENT_URL: "http://127.0.0.1:18080",
    };
    for (const key of FORBIDDEN_SECRETS) {
      parentEnv[key] = `inherited-secret-${key}`;
    }

    const stdout = execFileSync(
      process.execPath,
      [SCRIPT_PATH, envFile, process.execPath, "-e", "process.stdout.write(JSON.stringify(process.env))"],
      { env: parentEnv, encoding: "utf8" },
    );
    const childEnv = JSON.parse(stdout);

    assert.equal(childEnv.NEXT_PUBLIC_SENTRY_ENVIRONMENT, "staging");
    assert.equal(childEnv.SENTRY_RELEASE, "rel-123");
    assert.equal(childEnv.NORI_LOCAL_AGENT_URL, "http://127.0.0.1:18080");
    assert.equal(childEnv.NORI_LOCAL_EMAIL, "dev@example.test");
    assert.equal(childEnv.NORI_OIDC_COOKIE, "nori_id");
    assert.equal(childEnv.NORI_SERVICE_TOKEN_KEY, "shared-web-api-key");
    assert.equal(childEnv.NORI_APP_ORIGIN, "https://nori.example.test");
    assert.equal(childEnv.NORI_CLOUD, "local");
    assert.equal(childEnv.NORI_RUNTIME_URL, "https://api.example.test");
    assert.equal(childEnv.NORI_AUTH_PROVIDER, "oidc");
    assert.equal(childEnv.NORI_DEV_ORIGINS, "http://localhost:3210");
    assert.equal(childEnv.OWNER_EMAIL, "owner@example.test");
    assert.equal(childEnv.PORT, "3210");
    assert.equal(childEnv.HOSTNAME, "127.0.0.1");
    assert.equal(childEnv.NODE_ENV, "test");

    for (const key of FORBIDDEN_SECRETS) {
      assert.equal(
        Object.prototype.hasOwnProperty.call(childEnv, key),
        false,
        `expected ${key} to be deleted (absent, not empty string), got ${JSON.stringify(childEnv[key])}`,
      );
    }
  });
});

test("CLI prints a helpful error without ENOENT stack trace when env file is missing", () => {
  withTempDir((dir) => {
    const missing = join(dir, "missing.env");
    let caught;
    try {
      execFileSync(process.execPath, [SCRIPT_PATH, missing, process.execPath, "-e", "process.exit(0)"], {
        encoding: "utf8",
        stdio: ["ignore", "pipe", "pipe"],
      });
    } catch (err) {
      caught = err;
    }
    assert.ok(caught, "expected non-zero exit for missing env file");
    assert.equal(caught.status, 1);
    assert.match(caught.stderr, /\.env\.example/);
    assert.doesNotMatch(caught.stderr, /ENOENT|at readFileSync/);
  });
});
