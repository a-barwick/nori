import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const deploy = fileURLToPath(new URL("./deploy-gcp.sh", import.meta.url));
function runFixture(t, ready, smoke = false) {
  const dir = mkdtempSync(join(tmpdir(), "nori-deploy-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const log = join(dir, "log");
  const command = (name, source) => writeFileSync(join(dir, name), "#!/bin/sh\n" + source, { mode: 0o755 });
  command("git", 'case "$2" in --abbrev-ref) echo main;; --short) echo fixture;; esac');
  command("docker", 'printf "image %s\\n" "$*" >> "$FIXTURE_LOG"');
  command("gcloud", 'printf "gcloud %s\\n" "$*" >> "$FIXTURE_LOG"; if [ "$3" = describe ]; then printf "https://%s.example\\n" "$4"; fi');
  command("curl", 'printf "curl %s\\n" "$*" >> "$FIXTURE_LOG"; case "$*" in */readyz) exit "$READY_EXIT";; esac');
  const token = join(dir, "smoke-token");
  if (smoke) writeFileSync(token, "fixture-identity-token\n", { mode: 0o600 });
  const result = spawnSync("bash", [deploy, "--skip-tests"], {
    encoding: "utf8",
    env: { PATH: dir + ":" + process.env.PATH, FIXTURE_LOG: log, READY_EXIT: ready ? "0" : "22", ...(smoke ? { NORI_DEPLOY_SMOKE_TOKEN_FILE: token, NORI_GCP_MIGRATION_JOB: "fixture-migrate" } : {}) },
  });
  return { result, log: readFileSync(log, "utf8") };
}

test("core readiness gates the web rollout and web health gates success", (t) => {
  const { result, log } = runFixture(t, true);
  assert.equal(result.status, 0, result.stderr);
  assert.match(log, /deploy nori-core[\s\S]+\/readyz[\s\S]+deploy nori-web[\s\S]+\/api\/health/);
});

test("core readiness failure stops before updating web", (t) => {
  const { result, log } = runFixture(t, false);
  assert.equal(result.status, 22);
  assert.match(log, /\/readyz/);
  assert.doesNotMatch(log, /deploy nori-web/);
});

test("the configured migration and authenticated smoke gate run in rollout order", (t) => {
  const { result, log } = runFixture(t, true, true);
  assert.equal(result.status, 0, result.stderr);
  assert.match(log, /jobs update fixture-migrate[\s\S]+jobs execute fixture-migrate[\s\S]+deploy nori-core[\s\S]+\/readyz[\s\S]+deploy nori-web[\s\S]+\/api\/health[\s\S]+-H @- https:\/\/nori-web.example\/api\/v1\/capabilities/);
  assert.doesNotMatch(result.stdout + result.stderr + log, /fixture-identity-token/);
});
