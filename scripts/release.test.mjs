import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const release = fileURLToPath(new URL("./release.sh", import.meta.url));

function git(repo, ...args) {
  return execFileSync("git", ["-C", repo, ...args], { encoding: "utf8" }).trim();
}

function commitFile(repo, path, contents) {
  mkdirSync(join(repo, path, ".."), { recursive: true });
  writeFileSync(join(repo, path), contents);
  git(repo, "add", path);
  git(repo, "commit", "-q", "-m", `change ${path}`);
  return git(repo, "rev-parse", "HEAD");
}

function fixture(t, { change, live = "base", failDeploy = "", badRevisions = "", args = [], green = null } = {}) {
  const dir = mkdtempSync(join(tmpdir(), "nori-release-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const repo = join(dir, "repo");
  const bin = join(dir, "bin");
  mkdirSync(repo);
  mkdirSync(bin);
  git(repo, "init", "-q", "-b", "main");
  git(repo, "config", "user.email", "fixture@example.com");
  git(repo, "config", "user.name", "Fixture");
  for (const path of ["core/main.go", "web/page.tsx", "contracts/api.json", "ios/App.swift"]) {
    mkdirSync(join(repo, path, ".."), { recursive: true });
    writeFileSync(join(repo, path), "v1\n");
  }
  git(repo, "add", ".");
  git(repo, "commit", "-q", "-m", "base");
  const base = git(repo, "rev-parse", "HEAD");
  const changes = Array.isArray(change) ? change : change ? [change] : [];
  const commits = changes.map((path) => commitFile(repo, path, "v2\n"));
  const head = commits.at(-1) ?? base;
  const liveCommit = live === "base" ? base : live === "head" ? head : live;
  const greenCommits = green ? green.map((index) => commits[index]) : null;
  const released = greenCommits?.at(-1) ?? head;

  const log = join(dir, "log");
  writeFileSync(log, "");
  const command = (name, source) => writeFileSync(join(bin, name), "#!/bin/bash\n" + source, { mode: 0o755 });
  command("gcloud", `
printf 'gcloud %s\\n' "$*" >> "$FIXTURE_LOG"
case "$1 $2 $3" in
  "run services describe")
    printf '{"status":{"traffic":[{"revisionName":"%s-live","percent":100},{"revisionName":"%s-new","tag":"%s","url":"https://%s-new.example"}]}}' "$4" "$4" "$FIXTURE_TAG" "$4" ;;
  "run revisions describe")
    case "$4" in
      *-live) printf '{"metadata":{"labels":{"nori-commit":"%s","nori-prev":"%s-older"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}' "$FIXTURE_LIVE" "\${4%-live}" ;;
      *) printf '{"metadata":{"labels":{}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}' ;;
    esac ;;
  "run deploy "*) [ "$3" != "$FIXTURE_FAIL_DEPLOY" ] || exit 1 ;;
  "logging read "*) [ -z "$FIXTURE_BAD" ] || printf '%s\\n' $FIXTURE_BAD ;;
esac
exit 0`);
  command("ci-status", `for sha in $FIXTURE_GREEN; do [ "$sha" = "$1" ] && exit 0; done; exit 1`);
  command("curl", `
url="\${@: -1}"
case "$url" in
  */readyz|*/api/health) printf 200 ;;
  *) printf 403 ;;
esac`);

  const result = spawnSync("bash", [release, ...args], {
    encoding: "utf8",
    env: {
      PATH: `${bin}:${process.env.PATH}`,
      HOME: dir,
      FIXTURE_LOG: log,
      FIXTURE_TAG: `release-${released.slice(0, 12)}`,
      FIXTURE_GREEN: (greenCommits ?? []).join(" "),
      NORI_CI_STATUS_CMD: join(bin, "ci-status"),
      FIXTURE_LIVE: liveCommit,
      FIXTURE_FAIL_DEPLOY: failDeploy,
      FIXTURE_BAD: badRevisions,
      GITHUB_ACTIONS: "true",
      NORI_RELEASE_CI_GATED: greenCommits ? "0" : "1",
      NORI_RELEASE_REPO: repo,
      NORI_RELEASE_BAKE_SECONDS: "0",
    },
  });
  const calls = readFileSync(log, "utf8");
  return { result, calls, head, released, deployed: [...calls.matchAll(/gcloud run deploy (\S+)/g)].map((m) => m[1]) };
}

test("a web change releases only nori-web and nori-web-mac with the CI image", (t) => {
  const { result, calls, head, deployed } = fixture(t, { change: "web/page.tsx" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.deepEqual(deployed, ["nori-web", "nori-web-mac"]);
  assert.doesNotMatch(calls, /jobs execute/);
  assert.match(calls, new RegExp(`deploy nori-web --image=\\S+/nori-web:${head} --no-traffic --tag=release-${head.slice(0, 12)}`));
  assert.match(calls, new RegExp(`--update-labels=nori-commit=${head},nori-prev=nori-web-live`));
  assert.match(calls, /--startup-probe=httpGet\.path=\/api\/health/);
  assert.match(calls, /update-traffic nori-web-mac .*--to-revisions=nori-web-mac-new=100/);
});

test("a core change migrates first, then releases the API and worker", (t) => {
  const { result, calls, deployed } = fixture(t, { change: "core/main.go" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.deepEqual(deployed, ["nori-core", "nori-worker"]);
  assert.ok(calls.indexOf("jobs execute nori-migrate") < calls.indexOf("run deploy nori-core"));
  assert.match(calls, /deploy nori-worker .*httpGet\.path=\/readyz/);
});

test("a contracts change releases both groups, core first", (t) => {
  const { result, deployed } = fixture(t, { change: "contracts/api.json" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.deepEqual(deployed, ["nori-core", "nori-worker", "nori-web", "nori-web-mac"]);
});

test("an iOS-only change releases nothing", (t) => {
  const { result, calls } = fixture(t, { change: "ios/App.swift" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(result.stdout, /Nothing to release/);
  assert.doesNotMatch(calls, /run deploy|update-traffic/);
});

test("services without a release label get both groups", (t) => {
  const { result, deployed } = fixture(t, { change: "ios/App.swift", live: "" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.deepEqual(deployed, ["nori-core", "nori-worker", "nori-web", "nori-web-mac"]);
});

test("an older commit never replaces a newer live release", (t) => {
  const { result, calls } = fixture(t, { change: "web/page.tsx", live: "head" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.doesNotMatch(calls, /run deploy/);
});

test("a failed stage promotes nothing in its group and removes the tags", (t) => {
  const { result, calls } = fixture(t, { change: "core/main.go", failDeploy: "nori-worker" });
  assert.equal(result.status, 1);
  assert.doesNotMatch(calls, /--to-revisions=nori-core-new=100/);
  assert.match(calls, /update-traffic nori-core .*--remove-tags=release-/);
  assert.match(calls, /update-traffic nori-worker .*--remove-tags=release-/);
});

test("errors during bake revert only the group that produced them", (t) => {
  const { result, calls } = fixture(t, { change: "contracts/api.json", badRevisions: "nori-web-new" });
  assert.equal(result.status, 1);
  assert.match(calls, /update-traffic nori-web .*--to-revisions=nori-web-live=100/);
  assert.match(calls, /update-traffic nori-web-mac .*--to-revisions=nori-web-mac-live=100/);
  assert.doesNotMatch(calls, /--to-revisions=nori-core-live=100/);
});

test("rollback moves each service in the group to its recorded previous revision", (t) => {
  const { result, calls } = fixture(t, { args: ["rollback", "web"] });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(calls, /update-traffic nori-web .*--to-revisions=nori-web-older=100/);
  assert.match(calls, /update-traffic nori-web-mac .*--to-revisions=nori-web-mac-older=100/);
  assert.doesNotMatch(calls, /nori-core/);
});

test("--latest-green releases the newest commit with passing CI, skipping a red head", (t) => {
  const { result, calls, released, head, deployed } = fixture(t, {
    change: ["web/page.tsx", "core/main.go"],
    green: [0],
    args: ["--latest-green"],
  });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.notEqual(released, head);
  assert.deepEqual(deployed, ["nori-web", "nori-web-mac"]);
  assert.match(calls, new RegExp(`deploy nori-web --image=\\S+/nori-web:${released} `));
});

test("--latest-green with no green commit releases nothing", (t) => {
  const { result, calls } = fixture(t, { change: ["web/page.tsx"], green: [], args: ["--latest-green"] });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(result.stdout, /Nothing to release/);
  assert.doesNotMatch(calls, /run deploy/);
});
