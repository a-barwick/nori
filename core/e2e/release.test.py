#!/usr/bin/env python3
"""Offline process-level checks for the GitHub release artifact contract.

The release CLI is real. A standard-library audit hook denies subprocess and
network access in its child process; no credentials or cloud resources are used.
Actual deployment and shutdown require separate hosted workflow verification.
Run: python3 core/e2e/release.test.py
"""
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
CLI = ROOT / "infrastructure/release/release.py"
REGISTRY = "us-central1-docker.pkg.dev/bot-111549/nori-poc"
COMMIT = "a" * 40
BUILD = "12345678901-2"
IMAGES = {name: f"{REGISTRY}/{name}@sha256:" + digit * 64
          for name, digit in (("web", "b"), ("runtime", "c"), ("worker", "d"))}

# Deny external boundaries rather than mocking any release application modules.
# Record every attempt so rejected inputs must fail before an external action.
SITECUSTOMIZE = '''\
import os
import sys
def deny_external(event, args):
    if event in {"subprocess.Popen", "socket.__new__", "socket.connect", "socket.getaddrinfo",
                 "socket.gethostbyname", "socket.gethostbyaddr", "os.system", "os.exec", "os.posix_spawn"}:
        with open(os.environ["RELEASE_BOUNDARY_LOG"], "a") as log:
            log.write(event + "\\n")
        raise RuntimeError("Offline release check forbids external access")
sys.addaudithook(deny_external)
'''


class ReleaseProcessTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="nori-release-test-")
        self.directory = Path(self.temp.name)
        (self.directory / "sitecustomize.py").write_text(SITECUSTOMIZE)
        self.boundary_log = self.directory / "external-access.log"
        self.binary = self.directory / "computer-agent"
        self.binary.write_bytes(b"deterministic computer artifact fixture\n")
        # Explicit, minimal environment prevents inheriting developer credentials.
        self.env = {"PATH": "/usr/bin:/bin", "NORI_ENV_FILE": str(self.directory / "absent.env"),
                    "PYTHONPATH": str(self.directory), "RELEASE_BOUNDARY_LOG": str(self.boundary_log)}

    def tearDown(self):
        self.temp.cleanup()

    def cli(self, *args, env=None):
        return subprocess.run([sys.executable, str(CLI), *map(str, args)],
                              env=env or self.env, text=True, capture_output=True, timeout=20)

    def assert_no_external_access(self):
        self.assertFalse(self.boundary_log.exists(),
                         "Offline validation must reject before any subprocess or network access")

    def manifest_args(self, build=BUILD, **images):
        refs = {**IMAGES, **images}
        return ["--commit", COMMIT, "--build-id", build, "--web", refs["web"],
                "--runtime", refs["runtime"], "--worker", refs["worker"], "--computer", self.binary]

    def make_manifest(self):
        made = self.cli("manifest", *self.manifest_args())
        self.assertEqual(made.returncode, 0, made.stderr)
        path = self.directory / "manifest.json"
        path.write_text(made.stdout)
        return path, json.loads(made.stdout)

    def test_github_artifact_round_trip_rejects_mutable_or_corrupt_references(self):
        path, manifest = self.make_manifest()
        sha = hashlib.sha256(self.binary.read_bytes()).hexdigest()
        self.assertEqual(manifest["schema"], 2)
        self.assertEqual(manifest["commit"], COMMIT)
        self.assertEqual(manifest["build_id"], BUILD)
        self.assertEqual(manifest["computer_sha256"], sha)
        self.assertEqual(manifest["computer_object"], "computer/" + sha)
        self.assertEqual({name: manifest[name] for name in IMAGES}, IMAGES)
        inspected = self.cli("inspect", path)
        self.assertEqual(inspected.returncode, 0, inspected.stderr)
        report = json.loads(inspected.stdout)
        self.assertTrue(report["valid"])
        self.assertEqual(report["commit"], COMMIT)
        self.assertEqual(report["pipeline_timeout_seconds"], 1800)
        self.assertEqual(report["smoke_timeout_seconds"], 600)
        self.assertEqual(report["failure_policy"], "pause_then_restore_before_smoke")
        for component in IMAGES:
            for invalid in (REGISTRY + "/" + component + ":latest",
                            "other.registry/" + component + "@sha256:" + "b" * 64,
                            IMAGES["runtime"] if component != "runtime" else IMAGES["web"]):
                with self.subTest(component=component, invalid=invalid):
                    rejected = self.cli("manifest", *self.manifest_args(**{component: invalid}))
                    self.assertNotEqual(rejected.returncode, 0)
        for invalid in ("0-1", "1-0", "01-1", "1-01", "11111111-1111-4111-8111-111111111111"):
            with self.subTest(build_id=invalid):
                self.assertNotEqual(self.cli("manifest", *self.manifest_args(build=invalid)).returncode, 0)
        for key, invalid in (("schema", 1), ("commit", "a" * 12), ("build_id", "1-0"),
                             ("computer_sha256", "invalid"), ("computer_object", "computer/" + "0" * 64),
                             ("worker", REGISTRY + "/worker:latest")):
            with self.subTest(corrupt_field=key):
                path.write_text(json.dumps({**manifest, key: invalid}))
                self.assertNotEqual(self.cli("inspect", path).returncode, 0)
        self.assert_no_external_access()

    def test_deploy_rejects_foreign_github_identity_source_and_binary_before_cloud(self):
        path, manifest = self.make_manifest()
        trusted = {**self.env, "GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": "a-barwick/nori",
                   "GITHUB_REF": "refs/heads/main", "GITHUB_SHA": COMMIT,
                   "GITHUB_RUN_ID": BUILD.split("-")[0], "GITHUB_RUN_ATTEMPT": BUILD.split("-")[1],
                   "GITHUB_WORKFLOW_REF": "a-barwick/nori/.github/workflows/release.yml@refs/heads/main",
                   "GITHUB_EVENT_NAME": "push"}
        def deploy(environment="sandbox", smoke="full", env=None):
            return self.cli("deploy", "--manifest", path, "--computer", self.binary,
                            "--environment", environment, "--smoke", smoke, env=env or trusted)
        def rejected_before_cloud(result, reason):
            self.assertNotEqual(result.returncode, 0)
            self.assertIn(reason, result.stderr, "Reject for the stated contract violation")
            self.assert_no_external_access()
        cases = (("GITHUB_ACTIONS", "false"), ("GITHUB_REPOSITORY", "other/nori"),
                 ("GITHUB_REF", "refs/heads/feature"), ("GITHUB_SHA", "f" * 40),
                 ("GITHUB_RUN_ID", "12345678902"), ("GITHUB_RUN_ATTEMPT", "3"),
                 ("GITHUB_WORKFLOW_REF", "a-barwick/nori/.github/workflows/other.yml@refs/heads/main"),
                 ("GITHUB_EVENT_NAME", "pull_request"))
        rejected_before_cloud(deploy(env=self.env), "identity mismatch: GITHUB_ACTIONS")
        for key, invalid in cases:
            with self.subTest(identity_field=key):
                reason = ("different GitHub run" if key in {"GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT"}
                          else "trusted main event" if key == "GITHUB_EVENT_NAME"
                          else "identity mismatch: " + key)
                rejected_before_cloud(deploy(env={**trusted, key: invalid}), reason)
        for environment, smoke in (("sandbox", "health"), ("production", "full")):
            with self.subTest(environment=environment, smoke=smoke):
                rejected_before_cloud(deploy(environment=environment, smoke=smoke),
                                      "Full task validation belongs only in sandbox")
        self.binary.write_bytes(b"changed after the manifest was generated\n")
        for environment, smoke in (("sandbox", "full"), ("production", "health")):
            with self.subTest(corrupt_binary_environment=environment):
                rejected_before_cloud(deploy(environment=environment, smoke=smoke),
                                      "Computer artifact checksum mismatch")
        # A different source or run cannot reuse an otherwise valid artifact.
        self.binary.write_bytes(b"deterministic computer artifact fixture\n")
        for key, invalid in (("commit", "f" * 40), ("build_id", "12345678902-1")):
            with self.subTest(artifact_identity=key):
                path.write_text(json.dumps({**manifest, key: invalid}))
                reason = "identity mismatch: GITHUB_SHA" if key == "commit" else "different GitHub run"
                rejected_before_cloud(deploy(), reason)

    def test_saved_request_worker_plan_preserves_retained_workers(self):
        build = "ci-example-12345678"
        old_url = "https://nori-task-retained-8273801370.us-west1.run.app"
        new_url = "https://nori-task-" + build + "-8273801370.us-west1.run.app"
        before = {"name": "nori-task-retained", "generation": 1, "etag": "old",
                  "scaling": [{"min_instance_count": 0, "max_instance_count": 1}],
                  "template": [{"revision": "old-generated-revision", "service_account": "nori-poc-runtime@bot-111549.iam.gserviceaccount.com",
                                "containers": [{"image": IMAGES["worker"], "build_info": [], "env": [
                                    {"name": "NORI_WORKER_SERVICES", "value": json.dumps({"retained": old_url})},
                                    {"name": "FIRESTORE_DATABASE_ID", "value": "nori-poc"}]}]}]}
        after = copy.deepcopy(before)
        after.update(generation=None, etag=None)
        after["template"][0]["revision"] = None
        after["template"][0]["containers"][0]["env"][0]["value"] = json.dumps({"retained": old_url, build: new_url})
        changes = [{"address": 'google_cloud_run_v2_service.worker["retained"]', "type": "google_cloud_run_v2_service", "name": "worker",
                    "change": {"actions": ["update"], "before": before, "after": after}},
                   {"address": 'google_cloud_run_v2_service.worker["' + build + '"]', "change": {"actions": ["create"]}},
                   {"address": 'google_cloud_run_v2_service_iam_member.worker_invoke["' + build + '"]', "change": {"actions": ["create"]}}]
        path = self.directory / "worker-plan.json"
        def check(resources):
            path.write_text(json.dumps({"resource_changes": resources}))
            return self.cli("check-worker-plan", path, "--build-id", build)
        accepted = check(changes)
        self.assertEqual(accepted.returncode, 0, accepted.stderr)
        self.assertTrue(json.loads(accepted.stdout)["valid"])
        for label in ("image", "database", "removed-route", "delete", "replacement", "state-move", "old-pool"):
            bad = copy.deepcopy(changes)
            container = bad[0]["change"]["after"]["template"][0]["containers"][0]
            if label == "image": container["image"] = IMAGES["worker"].replace("d" * 64, "e" * 64)
            elif label == "database": container["env"][1]["value"] = "nori-sandbox"
            elif label == "removed-route": container["env"][0]["value"] = json.dumps({build: new_url})
            elif label == "delete": bad[0]["change"]["actions"] = ["delete"]
            elif label == "replacement": bad[0]["change"]["actions"] = ["delete", "create"]
            elif label == "state-move": bad[0]["previous_address"] = "old.address"
            elif label == "old-pool": bad[1]["address"] = 'google_cloud_run_v2_worker_pool.worker["' + build + '"]'
            with self.subTest(label=label):
                self.assertNotEqual(check(bad).returncode, 0)
        self.assert_no_external_access()


if __name__ == "__main__":
    unittest.main()
