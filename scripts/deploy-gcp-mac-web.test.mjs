import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

const deployMac = fileURLToPath(new URL("./deploy-gcp-mac-web.sh", import.meta.url));

test("Mac rollout requires a version-pinned pilot identity secret", () => {
  const dryRun = (secret) => spawnSync("bash", [deployMac, "--dry-run"], {
    encoding: "utf8",
    env: {
      ...process.env,
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
