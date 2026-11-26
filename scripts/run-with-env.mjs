#!/usr/bin/env node
import { readFileSync, realpathSync } from "node:fs";
import { resolve } from "node:path";
import { parseEnv } from "node:util";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

export const ALLOWED_WEB_KEYS = new Set([
  "NORI_APP_ORIGIN",
  "NORI_CLOUD",
  "NORI_RUNTIME_URL",
  "NORI_AUTH_PROVIDER",
  "NORI_DEV_ORIGINS",
  "OWNER_EMAIL",
  "NEXT_RUNTIME",
  "NEXT_TELEMETRY_DISABLED",
  "PORT",
  "HOSTNAME",
  "NODE_ENV",
]);

export const ALLOWED_WEB_PREFIXES = [
  "NEXT_PUBLIC_",
  "SENTRY_",
  "NORI_LOCAL_",
  "NORI_OIDC_",
  "NORI_SERVICE_",
];

export const ALLOWED_RUNTIME_KEYS = new Set([
  "PATH",
  "HOME",
  "USER",
  "LOGNAME",
  "TMPDIR",
  "TEMP",
  "TMP",
  "SHELL",
  "LANG",
  "TZ",
  "CI",
  "TERM",
  "COLORTERM",
  "FORCE_COLOR",
  "NO_COLOR",
  "PWD",
  "SHLVL",
]);

export const ALLOWED_RUNTIME_PREFIXES = [
  "LC_",
  "NODE_",
  "BUN_",
  "SSL_CERT_",
  "XDG_",
];

export function isAllowedWebKey(key) {
  return ALLOWED_WEB_KEYS.has(key) || ALLOWED_WEB_PREFIXES.some((prefix) => key.startsWith(prefix));
}

export function isAllowedRuntimeKey(key) {
  return ALLOWED_RUNTIME_KEYS.has(key) || ALLOWED_RUNTIME_PREFIXES.some((prefix) => key.startsWith(prefix));
}

export function buildChildEnv(dotEnv = {}, parentEnv = process.env) {
  const env = {};
  for (const [key, value] of Object.entries(dotEnv)) {
    if (value !== undefined && isAllowedWebKey(key)) {
      env[key] = value;
    }
  }
  for (const [key, value] of Object.entries(parentEnv)) {
    if (value !== undefined && (isAllowedWebKey(key) || isAllowedRuntimeKey(key))) {
      env[key] = value;
    }
  }
  for (const key of Object.keys(env)) {
    if (!isAllowedWebKey(key) && !isAllowedRuntimeKey(key)) {
      delete env[key];
    }
  }
  return env;
}

function main() {
  const [file, command, ...args] = process.argv.slice(2);
  if (!file || !command) {
    console.error("Usage: run-with-env.mjs ROOT_ENV COMMAND [ARGS...]");
    process.exit(1);
  }

  let raw;
  try {
    raw = readFileSync(file, "utf8");
  } catch (err) {
    if (err && err.code === "ENOENT") {
      console.error(`Missing ${file}. Copy .env.example to .env and fill in the required values before running this command.`);
      process.exit(1);
    }
    console.error(`Could not read ${file}: ${err instanceof Error ? err.message : String(err)}`);
    process.exit(1);
  }

  const env = buildChildEnv(parseEnv(raw), process.env);
  const child = spawn(command, args, { env, stdio: "inherit" });
  for (const signal of ["SIGTERM", "SIGINT"]) process.on(signal, () => child.kill(signal));
  child.on("error", () => { console.error("Could not start configured command."); process.exitCode = 1; });
  child.on("exit", (code) => { process.exitCode = code ?? 1; });
}

function isMainModule() {
  if (!process.argv[1]) return false;
  try {
    return realpathSync(fileURLToPath(import.meta.url)) === realpathSync(resolve(process.argv[1]));
  } catch {
    return resolve(fileURLToPath(import.meta.url)) === resolve(process.argv[1]);
  }
}

if (isMainModule()) {
  main();
}
