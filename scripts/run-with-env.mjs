#!/usr/bin/env node
// Keep the source of configuration singular without passing management keys
// to applications. Values already supplied by the spend wrapper win.
import { readFileSync } from "node:fs";
import { parseEnv } from "node:util";
import { spawn } from "node:child_process";
const [file, command, ...args] = process.argv.slice(2);
if (!file || !command) throw new Error("Usage: run-with-env.mjs ROOT_ENV COMMAND [ARGS...]");
const env = { ...parseEnv(readFileSync(file, "utf8")), ...process.env };
for (const key of Object.keys(env)) {
  // Empty prevents framework dotenv loading from restoring a filtered value.
  if (["OPENROUTER_MANAGEMENT_KEY", "TEMPORAL_BILLING_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "NORI_SPEND_SIGNING_KEY", "NORI_SPEND_FEATURE_ISSUER_TOKEN", "NORI_SPEND_CI_ISSUER_TOKEN"].includes(key) || key.startsWith("NORI_SPEND_LEASE_")) env[key] = "";
}
const child = spawn(command, args, { env, stdio: "inherit" });
for (const signal of ["SIGTERM", "SIGINT"]) process.on(signal, () => child.kill(signal));
child.on("error", () => { console.error("Could not start configured command."); process.exitCode = 1; });
child.on("exit", (code) => { process.exitCode = code ?? 1; });
