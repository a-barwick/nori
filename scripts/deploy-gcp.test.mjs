import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const deploy = fileURLToPath(new URL("./deploy-gcp.sh", import.meta.url));
const deployMac = fileURLToPath(new URL("./deploy-gcp-mac-web.sh", import.meta.url));

function runFixture(t, opts = {}) {
  const {
    coreReady = true,
    apiReady = true,
    workerReady = true,
    smoke = false,
    extraEnv = {},
  } = opts;
  const dir = mkdtempSync(join(tmpdir(), "nori-deploy-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const log = join(dir, "log");
  writeFileSync(log, "");
  const command = (name, source) => writeFileSync(join(dir, name), "#!/bin/sh\n" + source, { mode: 0o755 });
  command("git", 'case "$2" in --abbrev-ref) echo main;; --short) echo fixture;; esac');
  command("docker", 'printf "image %s\\n" "$*" >> "$FIXTURE_LOG"');
  command("gcloud", `
printf "gcloud %s\\n" "$*" >> "$FIXTURE_LOG"
if [ "$2" = revisions ] && [ "$3" = describe ]; then
  if [ "$WORKER_READY" = "true" ]; then
    printf 'Ready\tTrue\n'
  else
    printf 'Ready\tFalse\n'
  fi
elif [ "$3" = describe ]; then
  case "$*" in
    *--flatten=*) printf 'release-fixture\\t%s-fixture\\thttps://release-fixture---%s.example\\n' "$4" "$4" ;;
    *) printf 'https://%s.example\\n' "$4" ;;
  esac
fi`);
  command("curl", `
printf "curl %s\\n" "$*" >> "$FIXTURE_LOG"
case " $* " in
  *" -H @- "*)
    expected="$(printf '%s\\n' 'Authorization: Bearer fixture-identity-token' 'Sec-Fetch-Site: same-origin')"
    [ "$(cat)" = "$expected" ] || exit 1 ;;
esac
case "$*" in
  *nori-core.example/readyz) [ "$CORE_READY_EXIT" = 0 ] || exit "$CORE_READY_EXIT" ;;
  *nori-api.example/readyz) [ "$API_READY_EXIT" = 0 ] || exit "$API_READY_EXIT" ;;
esac
case " $* " in
  *" --write-out %{http_code} "*) printf '200' ;;
esac`);
  const token = join(dir, "smoke-token");
  if (smoke) writeFileSync(token, "fixture-identity-token\n", { mode: 0o600 });
  const result = spawnSync("bash", [deploy, "--skip-tests"], {
    encoding: "utf8",
    env: {
      PATH: dir + ":" + process.env.PATH,
      GCP_PROJECT: "fixture-project",
      GCP_AR_REPO: "fixture-repo",
      FIXTURE_LOG: log,
      CORE_READY_EXIT: coreReady ? "0" : "22",
      API_READY_EXIT: apiReady ? "0" : "22",
      WORKER_READY: workerReady ? "true" : "false",
      ...(smoke ? { NORI_DEPLOY_SMOKE_TOKEN_FILE: token } : {}),
      ...extraEnv,
    },
  });
  return { result, log: readFileSync(log, "utf8") };
}

test("combined rollout deploys nori-core and nori-web with NORI_CLOUD=gcp", (t) => {
  const { result, log } = runFixture(t, { extraEnv: { NORI_DEPLOY_TOPOLOGY: "combined" } });
  assert.equal(result.status, 0, result.stderr);
  assert.match(
    log,
    /deploy nori-core.*--update-env-vars=\^\|\^NORI_CLOUD=gcp[\s\S]+nori-core\.example\/readyz[\s\S]+deploy nori-web.*--update-env-vars=NORI_CLOUD=gcp[\s\S]+\/api\/health/,
  );
  assert.doesNotMatch(log, /jobs update|deploy nori-worker/);
});

test("default split rollout runs mandatory migration job, nori-core (api), nori-worker (revision-ready), and nori-web in order", (t) => {
  const { result, log } = runFixture(t);
  assert.equal(result.status, 0, result.stderr);
  assert.match(
    log,
    /jobs update nori-migrate.*--args=--migrate-only.*NORI_CLOUD=gcp[\s\S]+jobs execute nori-migrate[\s\S]+deploy nori-core[\s\S]+nori-core\.example\/readyz[\s\S]+deploy nori-worker[\s\S]+revisions describe nori-worker-fixture[\s\S]+deploy nori-web[\s\S]+\/api\/health/,
  );
  assert.match(log, /deploy nori-core.*--update-env-vars=\^\|\^NORI_CLOUD=gcp\|NORI_ROLE=api\|NORI_POSTGRES_MAX_CONNS=8/);
  assert.match(
    log,
    /deploy nori-worker.*--args=--metrics-addr=0\.0\.0\.0:8080.*--ingress=internal.*--no-cpu-throttling.*--min-instances=1.*--update-env-vars=\^\|\^NORI_CLOUD=gcp\|NORI_ROLE=worker\|NORI_POSTGRES_MAX_CONNS=10/,
  );
  assert.doesNotMatch(log, /curl.*nori-worker\.example\/readyz/);
});

test("API readiness failure stops before nori-worker and nori-web", (t) => {
  const { result, log } = runFixture(t, {
    coreReady: false,
  });
  assert.equal(result.status, 1);
  assert.match(log, /nori-core\.example\/readyz/);
  assert.match(log, /update-traffic nori-core.*--remove-tags=release-fixture/);
  assert.doesNotMatch(log, /--to-revisions=|deploy nori-worker|deploy nori-web/);
});

test("nori-worker revision readiness failure stops before updating nori-web", (t) => {
  const { result, log } = runFixture(t, {
    coreReady: true,
    workerReady: false,
  });
  assert.equal(result.status, 1);
  assert.match(log, /revisions describe nori-worker-fixture/);
  assert.match(log, /update-traffic nori-worker.*--remove-tags=release-fixture/);
  assert.doesNotMatch(log, /deploy nori-web/);
});

test("split rollout supports custom migration job, NORI_CLOUD=aws, listen URL secret, and authenticated smoke gate", (t) => {
  const { result, log } = runFixture(t, {
    smoke: true,
    extraEnv: {
      NORI_CLOUD: "aws",
      NORI_GCP_API_SERVICE: "nori-api",
      NORI_GCP_MIGRATION_JOB: "fixture-migrate",
      NORI_API_POSTGRES_MAX_CONNS: "12",
      NORI_WORKER_POSTGRES_MAX_CONNS: "14",
      NORI_POSTGRES_LISTEN_URL_SECRET: "nori-postgres-listen-url:latest",
    },
  });
  assert.equal(result.status, 0, result.stderr);
  assert.match(
    log,
    /jobs update fixture-migrate.*NORI_CLOUD=aws[\s\S]+jobs execute fixture-migrate[\s\S]+deploy nori-api[\s\S]+deploy nori-worker[\s\S]+deploy nori-web[\s\S]+-H @- https:\/\/nori-web\.example\/api\/v1\/capabilities/,
  );
  assert.match(log, /deploy nori-api.*--update-secrets=NORI_POSTGRES_LISTEN_URL=nori-postgres-listen-url:latest/);
  assert.match(log, /deploy nori-worker.*--update-secrets=NORI_POSTGRES_LISTEN_URL=nori-postgres-listen-url:latest/);
  assert.doesNotMatch(result.stdout + result.stderr + log, /fixture-identity-token/);
});

test("rejects invalid worker min-instances or empty migration job in split mode", (t) => {
  const zeroWorker = runFixture(t, {
    extraEnv: { NORI_DEPLOY_TOPOLOGY: "split", NORI_WORKER_MIN_INSTANCES: "0" },
  });
  assert.equal(zeroWorker.result.status, 1);
  assert.match(zeroWorker.result.stderr, /NORI_WORKER_MIN_INSTANCES must be an integer >= 1/);

  const noMigrate = runFixture(t, {
    extraEnv: { NORI_DEPLOY_TOPOLOGY: "split", NORI_GCP_MIGRATION_JOB: "" },
  });
  assert.equal(noMigrate.result.status, 1);
  assert.match(noMigrate.result.stderr, /NORI_GCP_MIGRATION_JOB must be set/);
});

test("Mac rollout requires a version-pinned pilot identity secret", () => {
  const dryRun = (secret) => spawnSync("bash", [deployMac, "--dry-run"], {
    encoding: "utf8",
    env: {
      ...process.env,
      GCP_PROJECT: "fixture-project",
      GCP_AR_REPO: "fixture-repo",
      NORI_MAC_GOOGLE_CLIENT_ID: "desktop-fixture.apps.googleusercontent.com",
      NORI_MAC_GOOGLE_IDENTITIES_SECRET: secret,
    },
  });

  const missing = dryRun("");
  assert.notEqual(missing.status, 0);
  assert.match(missing.stderr, /NORI_MAC_GOOGLE_IDENTITIES_SECRET/);

  const unpinned = dryRun("nori-mac-pilot:latest");
  assert.equal(unpinned.status, 2);

  const pinned = dryRun("nori-mac-pilot:2");
  assert.equal(pinned.status, 0, pinned.stderr);
  assert.match(pinned.stdout, /identity allowlist secret/);
});

test("deploy scripts require explicit GCP_PROJECT and GCP_AR_REPO", (t) => {
  const missingProject = runFixture(t, { extraEnv: { GCP_PROJECT: "" } });
  assert.notEqual(missingProject.result.status, 0);
  assert.match(missingProject.result.stderr, /GCP_PROJECT/);

  const missingRepo = runFixture(t, { extraEnv: { GCP_AR_REPO: "" } });
  assert.notEqual(missingRepo.result.status, 0);
  assert.match(missingRepo.result.stderr, /GCP_AR_REPO/);
});
