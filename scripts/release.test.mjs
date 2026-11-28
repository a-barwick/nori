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

function fixture(
  t,
  {
    change,
    live = "base",
    failDeploy = "",
    badRevisions = "",
    webHealthStatus = "200",
    webRootStatus = "200",
    siteRootStatus = "200",
    missingImages = "",
    missingServices = "",
    failLogReads = 0,
    args = [],
    green = null,
    bakeSeconds = "0",
  } = {},
) {
  const dir = mkdtempSync(join(tmpdir(), "nori-release-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const repo = join(dir, "repo");
  const bin = join(dir, "bin");
  mkdirSync(repo);
  mkdirSync(bin);
  git(repo, "init", "-q", "-b", "main");
  git(repo, "config", "user.email", "fixture@example.com");
  git(repo, "config", "user.name", "Fixture");
  git(repo, "config", "commit.gpgsign", "false");
  for (const path of ["core/main.go", "web/page.tsx", "contracts/api.json", "ios/App.swift", "site/index.html"]) {
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
is_missing_service() { for svc in $FIXTURE_MISSING_SERVICES; do [ "$svc" = "$1" ] && return 0; done; return 1; }
case "$1 $2 $3" in
  "artifacts docker images")
    for name in $FIXTURE_MISSING_IMAGES; do case "$5" in */$name:*) exit 1 ;; esac; done ;;
  "run services list")
    svc="\${4#--filter=metadata.name=}"
    is_missing_service "$svc" || printf '%s\n' "$svc" ;;
  "run services describe")
    is_missing_service "$4" && exit 1
    printf '{"status":{"traffic":[{"revisionName":"%s-live","percent":100},{"revisionName":"%s-new","tag":"%s","url":"https://%s-new.example"}]}}' "$4" "$4" "$FIXTURE_TAG" "$4" ;;
  "run revisions describe")
    case "$4" in
      *-live) printf '{"metadata":{"labels":{"nori-commit":"%s","nori-prev":"%s-older"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}' "$FIXTURE_LIVE" "\${4%-live}" ;;
      *) printf '{"metadata":{"labels":{}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}' ;;
    esac ;;
  "run deploy "*) [ "$3" != "$FIXTURE_FAIL_DEPLOY" ] || exit 1 ;;
  "logging read "*)
    if [ "\${FIXTURE_FAIL_LOG_READS:-0}" -gt 0 ]; then
      count=0
      [ -f "$FIXTURE_LOG_FAIL_FILE" ] && count=$(cat "$FIXTURE_LOG_FAIL_FILE")
      if [ "$count" -lt "$FIXTURE_FAIL_LOG_READS" ]; then
        echo $((count + 1)) > "$FIXTURE_LOG_FAIL_FILE"
        exit 1
      fi
    fi
    [ -z "$FIXTURE_BAD" ] || printf '%s\\n' $FIXTURE_BAD ;;
esac
exit 0`);
  command("ci-status", `printf 'ci-status %s\\n' "$*" >> "$FIXTURE_LOG"\nfor sha in $FIXTURE_GREEN; do [ "$sha" = "$1" ] && exit 0; done; exit 1`);
  command("curl", `
url="\${@: -1}"
case "$url" in
  *nori-web-new.example/api/health) printf '%s' "$FIXTURE_WEB_HEALTH_STATUS" ;;
  */readyz|*/api/health) printf 200 ;;
  *nori-web-new.example/) printf '%s' "$FIXTURE_WEB_ROOT_STATUS" ;;
  *nori-site-new.example/privacy) printf 200 ;;
  *nori-site-new.example/) printf '%s' "$FIXTURE_SITE_ROOT_STATUS" ;;
  *) printf 403 ;;
esac`);

  const env = {
    PATH: `${bin}:${process.env.PATH}`,
    HOME: dir,
    FIXTURE_LOG: log,
    FIXTURE_TAG: `release-${released.slice(0, 12)}`,
    FIXTURE_GREEN: (greenCommits ?? []).join(" "),
    NORI_CI_STATUS_CMD: join(bin, "ci-status"),
    FIXTURE_LIVE: liveCommit,
    FIXTURE_FAIL_DEPLOY: failDeploy,
    FIXTURE_BAD: badRevisions,
    FIXTURE_WEB_HEALTH_STATUS: webHealthStatus,
    FIXTURE_WEB_ROOT_STATUS: webRootStatus,
    FIXTURE_SITE_ROOT_STATUS: siteRootStatus,
    FIXTURE_MISSING_IMAGES: missingImages,
    FIXTURE_MISSING_SERVICES: missingServices,
    FIXTURE_FAIL_LOG_READS: String(failLogReads),
    FIXTURE_LOG_FAIL_FILE: join(dir, "log-fail-count"),
    GITHUB_ACTIONS: "true",
    NORI_RELEASE_CI_GATED: greenCommits ? "0" : "1",
    NORI_RELEASE_REPO: repo,
    NORI_RELEASE_LOG_RETRY_SLEEP: "0",
  };
  if (bakeSeconds !== null) {
    env.NORI_RELEASE_BAKE_SECONDS = bakeSeconds;
  }
  const result = spawnSync("bash", [release, ...args], {
    encoding: "utf8",
    env,
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

test("a core change migrates first, then releases nori-core", (t) => {
  const { result, calls, deployed } = fixture(t, { change: "core/main.go" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.deepEqual(deployed, ["nori-core"]);
  assert.ok(calls.indexOf("jobs execute nori-migrate") < calls.indexOf("run deploy nori-core"));
  assert.match(calls, /deploy nori-core .*httpGet\.path=\/readyz/);
});

test("a contracts change releases both groups, core first", (t) => {
  const { result, deployed } = fixture(t, { change: "contracts/api.json" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.deepEqual(deployed, ["nori-core", "nori-web", "nori-web-mac"]);
});

test("an iOS-only change releases nothing", (t) => {
  const { result, calls } = fixture(t, { change: "ios/App.swift" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(result.stdout, /Nothing to release/);
  assert.doesNotMatch(calls, /run deploy|update-traffic/);
});

test("services without a release label get every group", (t) => {
  const { result, deployed } = fixture(t, { change: "ios/App.swift", live: "" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.deepEqual(deployed, ["nori-core", "nori-web", "nori-web-mac", "nori-site"]);
});

test("an older commit never replaces a newer live release", (t) => {
  const { result, calls } = fixture(t, { change: "web/page.tsx", live: "head" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.doesNotMatch(calls, /run deploy/);
});

test("a failed stage promotes nothing in its group and removes the tags", (t) => {
  const { result, calls } = fixture(t, { change: "web/page.tsx", failDeploy: "nori-web-mac" });
  assert.equal(result.status, 1);
  assert.doesNotMatch(calls, /--to-revisions=nori-web-new=100/);
  assert.match(calls, /update-traffic nori-web .*--remove-tags=release-/);
  assert.match(calls, /update-traffic nori-web-mac .*--remove-tags=release-/);
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
  assert.match(result.stderr, new RegExp(`WARNING: main HEAD ${head.slice(0, 12)} is not green/built; falling back to ${released.slice(0, 12)}`));
  assert.deepEqual(deployed, ["nori-web", "nori-web-mac"]);
  assert.match(calls, new RegExp(`deploy nori-web --image=\\S+/nori-web:${released} `));
});

test("default release fails loudly when HEAD has not passed CI instead of falling back", (t) => {
  const { result, calls, head } = fixture(t, {
    change: ["web/page.tsx", "core/main.go"],
    green: [0],
    args: ["--quick"],
  });
  assert.equal(result.status, 1);
  assert.match(result.stderr, new RegExp(`CI has not passed on ${head}`));
  assert.doesNotMatch(calls, /run deploy/);
});

test("--latest-green finds a green commit on the second parent of a reverse merge", (t) => {
  const dir = mkdtempSync(join(tmpdir(), "nori-release-merge-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const repo = join(dir, "repo");
  const bin = join(dir, "bin");
  mkdirSync(repo);
  mkdirSync(bin);
  git(repo, "init", "-q", "-b", "main");
  git(repo, "config", "user.email", "fixture@example.com");
  git(repo, "config", "user.name", "Fixture");
  git(repo, "config", "commit.gpgsign", "false");
  for (const path of ["core/main.go", "web/page.tsx", "contracts/api.json", "site/index.html"]) {
    mkdirSync(join(repo, path, ".."), { recursive: true });
    writeFileSync(join(repo, path), "v1\n");
  }
  git(repo, "add", ".");
  git(repo, "commit", "-q", "-m", "base");
  const base = git(repo, "rev-parse", "HEAD");

  // Feature branch diverges from base.
  git(repo, "checkout", "-q", "-b", "feature");
  commitFile(repo, "core/main.go", "v2\n");

  // Meanwhile, a green web commit lands on main.
  git(repo, "checkout", "-q", "main");
  const greenWeb = commitFile(repo, "web/page.tsx", "v2\n");

  // Reverse merge: main is merged into feature (so greenWeb is the 2nd parent),
  // then fast-forwarded onto main.
  git(repo, "checkout", "-q", "feature");
  git(repo, "merge", "-q", "--no-ff", "main", "-m", "merge main into feature");
  const mergeHead = git(repo, "rev-parse", "HEAD");
  git(repo, "checkout", "-q", "main");
  git(repo, "merge", "-q", "--ff-only", "feature");

  const log = join(dir, "log");
  writeFileSync(log, "");
  const command = (name, source) => writeFileSync(join(bin, name), "#!/bin/bash\n" + source, { mode: 0o755 });
  command("gcloud", `
printf 'gcloud %s\\n' "$*" >> "$FIXTURE_LOG"
case "$1 $2 $3" in
  "run services list") printf '%s\\n' "\${4#--filter=metadata.name=}" ;;
  "run services describe") printf '{"status":{"traffic":[{"revisionName":"%s-live","percent":100},{"revisionName":"%s-new","tag":"%s","url":"https://%s-new.example"}]}}' "$4" "$4" "$FIXTURE_TAG" "$4" ;;
  "run revisions describe")
    case "$4" in
      *-live) printf '{"metadata":{"labels":{"nori-commit":"%s","nori-prev":"%s-older"}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}' "$FIXTURE_LIVE" "\${4%-live}" ;;
      *) printf '{"metadata":{"labels":{}},"status":{"conditions":[{"type":"Ready","status":"True"}]}}' ;;
    esac ;;
esac
exit 0`);
  command("ci-status", `[ "$1" = "${greenWeb}" ] || [ "$1" = "${base}" ]`);
  command("curl", `
url="\${@: -1}"
case "$url" in
  */readyz|*/api/health|*nori-web-new.example/) printf 200 ;;
  *) printf 403 ;;
esac`);

  const result = spawnSync("bash", [release, "--latest-green"], {
    encoding: "utf8",
    env: {
      PATH: `${bin}:${process.env.PATH}`,
      HOME: dir,
      FIXTURE_LOG: log,
      FIXTURE_TAG: `release-${greenWeb.slice(0, 12)}`,
      FIXTURE_LIVE: base,
      NORI_CI_STATUS_CMD: join(bin, "ci-status"),
      GITHUB_ACTIONS: "true",
      NORI_RELEASE_CI_GATED: "0",
      NORI_RELEASE_REPO: repo,
      NORI_RELEASE_BAKE_SECONDS: "0",
    },
  });
  const calls = readFileSync(log, "utf8");
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(result.stderr, new RegExp(`WARNING: main HEAD ${mergeHead.slice(0, 12)} is not green/built; falling back to ${greenWeb.slice(0, 12)}`));
  assert.match(calls, new RegExp(`deploy nori-web --image=\\S+/nori-web:${greenWeb} `));
  assert.doesNotMatch(calls, /deploy nori-core /);
});

test("--latest-green with no green commit releases nothing", (t) => {
  const { result, calls } = fixture(t, { change: ["web/page.tsx"], green: [], args: ["--latest-green"] });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(result.stdout, /Nothing to release/);
  assert.doesNotMatch(calls, /run deploy/);
});

test("a 500 on nori-web root blocks promotion and removes tags", (t) => {
  const { result, calls } = fixture(t, { change: "web/page.tsx", webRootStatus: "500" });
  assert.equal(result.status, 1);
  assert.doesNotMatch(calls, /--to-revisions=nori-web-new=100/);
  assert.match(calls, /update-traffic nori-web .*--remove-tags=release-/);
});

test("IAP 302 redirects on nori-web health and root pass staged checks", (t) => {
  const { result, calls } = fixture(t, { change: "web/page.tsx", webHealthStatus: "302", webRootStatus: "302" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(calls, /update-traffic nori-web .*--to-revisions=nori-web-new=100/);
});

test("transient gcloud logging read errors during bake are retried", (t) => {
  const { result } = fixture(t, { change: "web/page.tsx", failLogReads: 2 });
  assert.equal(result.status, 0, result.stderr + result.stdout);
});

test("a site change releases only nori-site after checking / and /privacy", (t) => {
  const { result, calls, head, deployed } = fixture(t, { change: "site/index.html" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.deepEqual(deployed, ["nori-site"]);
  assert.doesNotMatch(calls, /jobs execute/);
  assert.match(calls, new RegExp(`deploy nori-site --image=\\S+/nori-site:${head} --no-traffic --tag=release-${head.slice(0, 12)}`));
  assert.match(calls, /deploy nori-site .*--startup-probe=httpGet\.path=\/healthz,/);
  assert.match(calls, /update-traffic nori-site .*--to-revisions=nori-site-new=100/);
  assert.match(result.stdout, /site: release/);
});

test("a site root that isn't 200 blocks promotion and removes the tag", (t) => {
  const { result, calls } = fixture(t, { change: "site/index.html", siteRootStatus: "403" });
  assert.equal(result.status, 1);
  assert.doesNotMatch(calls, /--to-revisions=nori-site-new=100/);
  assert.match(calls, /update-traffic nori-site .*--remove-tags=release-/);
});

test("a commit without a nori-site image still releases web and skips the site", (t) => {
  const { result, deployed } = fixture(t, { change: ["site/index.html", "web/page.tsx"], missingImages: "nori-site" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.deepEqual(deployed, ["nori-web", "nori-web-mac"]);
  assert.match(result.stdout, /site: skip \(no nori-site image for /);
});

test("--site with no nori-site image fails before deploying anything", (t) => {
  const { result, calls } = fixture(t, { change: "web/page.tsx", missingImages: "nori-site", args: ["--site"] });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /nori-site:\S+ does not exist/);
  assert.doesNotMatch(calls, /run deploy/);
});

test("--latest-green does not require a nori-site image", (t) => {
  const { result, released, deployed } = fixture(t, {
    change: ["web/page.tsx"],
    green: [0],
    missingImages: "nori-site",
    args: ["--latest-green"],
  });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(result.stdout, new RegExp(`Release ${released.slice(0, 12)}`));
  assert.deepEqual(deployed, ["nori-web", "nori-web-mac"]);
});

test("a missing nori-site service is skipped while web still releases", (t) => {
  const { result, calls, deployed } = fixture(t, { change: ["site/index.html", "web/page.tsx"], missingServices: "nori-site" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.deepEqual(deployed, ["nori-web", "nori-web-mac"]);
  assert.match(result.stdout, /site: skip \(nori-site does not exist yet/);
  assert.doesNotMatch(calls, /deploy nori-site/);
});

test("--site with a missing nori-site service fails before deploying anything", (t) => {
  const { result, calls } = fixture(t, { change: "site/index.html", missingServices: "nori-site", args: ["--site"] });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /cannot release site: nori-site does not exist yet/);
  assert.doesNotMatch(calls, /run deploy/);
});

test("--dry-run --site prints the staging and promotion steps without running them", (t) => {
  const { result, calls, head } = fixture(t, { change: "ios/App.swift", args: ["--dry-run", "--site"] });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(result.stdout, /site: release \(unchanged, forced\)/);
  assert.match(result.stdout, new RegExp(`\\+ cloud_run deploy nori-site --image=\\S+/nori-site:${head} --no-traffic`));
  assert.match(result.stdout, /\+ cloud_run services update-traffic nori-site --to-revisions=nori-site-staged=100/);
  assert.doesNotMatch(calls, /run deploy|update-traffic/);
});

test("rollback site moves nori-site to its previous revision", (t) => {
  const { result, calls } = fixture(t, { args: ["rollback", "site"] });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(calls, /update-traffic nori-site .*--to-revisions=nori-site-older=100/);
  assert.doesNotMatch(calls, /update-traffic nori-web/);
});

test("rollback all skips nori-site when it doesn't exist", (t) => {
  const { result, calls } = fixture(t, { args: ["rollback", "all"], missingServices: "nori-site" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(calls, /update-traffic nori-web-mac .*--to-revisions=nori-web-mac-older=100/);
  assert.doesNotMatch(calls, /update-traffic nori-site/);
});

test("ci-status.sh --exclude-job ignores excluded job failures while still gating on them by default", (t) => {
  const dir = mkdtempSync(join(tmpdir(), "nori-ci-status-test-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const bin = join(dir, "bin");
  mkdirSync(bin);
  const ciStatus = fileURLToPath(new URL("./ci-status.sh", import.meta.url));
  writeFileSync(
    join(bin, "gh"),
    `#!/bin/bash
if [[ "$1 $2" == "run list" ]]; then
  echo 42
elif [[ "$1 $2" == "run view" ]]; then
  printf '{"jobs":[{"name":"Check (Go, Contracts, Web, Scripts)","conclusion":"success"},{"name":"Swift package and iOS app","conclusion":"failure"},{"name":"Build images","conclusion":"skipped"}]}\\n'
fi`,
    { mode: 0o755 },
  );
  const env = { PATH: `${bin}:${process.env.PATH}` };
  const all = spawnSync("bash", [ciStatus, "a".repeat(40)], { encoding: "utf8", env });
  assert.equal(all.status, 1);
  assert.equal(all.stdout.trim(), "failure");
  const excluded = spawnSync("bash", [ciStatus, "a".repeat(40), "--exclude-job", "Swift package and iOS app"], { encoding: "utf8", env });
  assert.equal(excluded.status, 0, excluded.stderr + excluded.stdout);
  assert.equal(excluded.stdout.trim(), "success");
});

test("--quick excludes Deep Deterministic Simulation from CI status and defaults bake to 60s", (t) => {
  const normal = fixture(t, { change: ["web/page.tsx"], green: [0], args: ["--latest-green"] });
  assert.equal(normal.result.status, 0, normal.result.stderr + normal.result.stdout);
  assert.match(normal.calls, /ci-status [0-9a-f]{40} --exclude-job Swift package and iOS app\n/);
  assert.doesNotMatch(normal.calls, /Deep Deterministic Simulation/);

  const quick = fixture(t, {
    change: ["web/page.tsx"],
    green: [0],
    args: ["--latest-green", "--quick"],
    bakeSeconds: null,
    badRevisions: "nori-web-new",
  });
  assert.equal(quick.result.status, 1);
  assert.match(quick.calls, /ci-status [0-9a-f]{40} --exclude-job Swift package and iOS app --exclude-job Deep Deterministic Simulation\n/);
  assert.match(quick.result.stdout, /Baking nori-web nori-web-mac for 60s/);
});

test("all release groups stage before any group is promoted", (t) => {
  const { result, calls } = fixture(t, { change: "contracts/api.json" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  const lastDeploy = calls.lastIndexOf("gcloud run deploy ");
  const firstPromote = calls.indexOf("--to-revisions=");
  assert.ok(lastDeploy !== -1 && firstPromote !== -1, calls);
  assert.ok(lastDeploy < firstPromote, `expected all deploys before promotions:\n${calls}`);
});

test("a web staging failure prevents core promotion and untags all staged services", (t) => {
  const { result, calls } = fixture(t, { change: "contracts/api.json", failDeploy: "nori-web" });
  assert.equal(result.status, 1);
  assert.doesNotMatch(calls, /--to-revisions=nori-core-new=100/);
  assert.doesNotMatch(calls, /--to-revisions=nori-web-new=100/);
  assert.match(calls, /update-traffic nori-core .*--remove-tags=release-/);
  assert.match(calls, /update-traffic nori-web .*--remove-tags=release-/);
  assert.match(calls, /update-traffic nori-web-mac .*--remove-tags=release-/);
});

test("bake filter checks stdout/stderr application ERROR logs alongside 5xx and crashes", (t) => {
  const { result, calls } = fixture(t, { change: "web/page.tsx" });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(calls, /logging read .*logName:\("run\.googleapis\.com%2Fstdout" OR "run\.googleapis\.com%2Fstderr"\) AND \(severity>=ERROR OR jsonPayload\.level="ERROR"\)/);
});

test("--plan-groups prints changed groups without checking CI or images", (t) => {
  const { result, calls } = fixture(t, {
    change: "web/page.tsx",
    green: [],
    missingImages: "nori-core nori-web nori-site",
    args: ["--plan-groups"],
  });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.equal(result.stdout.trim(), "web");
  assert.doesNotMatch(calls, /ci-status /);
  assert.doesNotMatch(calls, /artifacts docker images/);
  assert.doesNotMatch(calls, /run deploy /);
});

test("--only restricts release to explicitly selected groups even when others changed", (t) => {
  const { result, calls } = fixture(t, {
    change: "contracts/api.json",
    missingImages: "nori-web",
    args: ["--force-unverified", "--only", "--core"],
  });
  assert.equal(result.status, 0, result.stderr + result.stdout);
  assert.match(calls, /run deploy nori-core /);
  assert.doesNotMatch(calls, /run deploy nori-web /);
  assert.doesNotMatch(calls, /run deploy nori-web-mac /);
});

test("--only without a group flag fails", (t) => {
  const { result } = fixture(t, { change: "web/page.tsx", args: ["--only"] });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /--only requires at least one of --core, --web, or --site/);
});
