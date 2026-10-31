#!/usr/bin/env python3
"""Offline process-level checks for the release artifact contract.

The CLI is real. A loopback HTTP endpoint and a fake gcloud executable stand in
for Cloud Storage/Cloud Build at the process boundary; no credentials or cloud
resources are used. Run: python3 core/e2e/release.test.py
"""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]
CLI = ROOT / "infrastructure/release/release.py"
REGISTRY = "us-central1-docker.pkg.dev/bot-111549/nori-poc"
COMMIT = "a" * 40
BUILD = "11111111-1111-4111-8111-111111111111"
NEXT_BUILD = "22222222-2222-4222-8222-222222222222"
RELEASE_BUILD = "33333333-3333-4333-8333-333333333333"
SANDBOX_BUILD = "44444444-4444-4444-8444-444444444444"
IMAGES = {name: f"{REGISTRY}/{name}@sha256:" + digit * 64
          for name, digit in (("web", "b"), ("runtime", "c"), ("worker", "d"))}

# This hooks only the standard-library HTTP boundary of the child process. Any
# host outside the two listed Google APIs is denied instead of reaching a network.
SITECUSTOMIZE = '''\
import os
from urllib.parse import urlsplit
import urllib.request
_original = urllib.request.urlopen
def local_only(request, *args, **kwargs):
    url = request.full_url if isinstance(request, urllib.request.Request) else request
    parsed = urlsplit(url)
    if parsed.scheme != "https" or parsed.netloc not in {"storage.googleapis.com", "cloudbuild.googleapis.com"}:
        raise RuntimeError("unexpected network request")
    target = os.environ["FAKE_API"] + "/" + parsed.netloc + parsed.path
    if parsed.query:
        target += "?" + parsed.query
    if isinstance(request, urllib.request.Request):
        request = urllib.request.Request(target, data=request.data, headers=dict(request.header_items()), method=request.get_method())
    else:
        request = target
    return _original(request, *args, **kwargs)
urllib.request.urlopen = local_only
'''

# The only command the preparation flow may execute. No real gcloud is invoked.
GCLOUD = '''\
#!/usr/bin/env python3
import os, pathlib, sys
args = sys.argv[1:]
if "print-access-token" in args:
    print("local-fake-token")
elif "cp" in args and "--if-generation-match=0" in args:
    i = args.index("cp")
    src, dst = args[i + 1:i + 3]
    if not dst.startswith("gs://fake-release/"):
        sys.exit(3)
    obj = pathlib.Path(os.environ["FAKE_OBJECTS"]) / dst.removeprefix("gs://fake-release/")
    obj.parent.mkdir(parents=True, exist_ok=True)
    try:
        with obj.open("xb") as target:
            target.write(pathlib.Path(src).read_bytes())
    except FileExistsError:
        sys.exit(1)
else:
    sys.exit(4)
'''


class FakeAPI(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def reply(self, status, data):
        if not isinstance(data, bytes):
            data = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        parsed = urlsplit(self.path)
        path = parsed.path
        for name, identity in (("nori-main-prepare", "trusted-trigger"), ("nori-sandbox", "sandbox-trigger"), ("nori-release", "release-trigger")):
            if path.endswith("/triggers/" + name):
                return self.reply(200, {"id": identity})
        if "/builds/" in path:
            build = self.server.builds.get(path.rsplit("/", 1)[-1])
            return self.reply(200, build) if build else self.reply(404, {})
        base = "/storage.googleapis.com/storage/v1/b/fake-release/o"
        if path == base:
            prefix = parse_qs(parsed.query).get("prefix", [""])[0]
            names = [str(p.relative_to(self.server.objects)) for p in self.server.objects.rglob("*") if p.is_file()]
            return self.reply(200, {"items": [{"name": n} for n in names if n.startswith(prefix)]})
        if path.startswith(base + "/"):
            obj = self.server.objects / unquote(path[len(base) + 1:])
            return self.reply(200, obj.read_bytes()) if obj.is_file() else self.reply(404, {})
        return self.reply(404, {})

    def do_POST(self):
        parsed = urlsplit(self.path)
        if parsed.path != "/storage.googleapis.com/upload/storage/v1/b/fake-release/o":
            return self.reply(404, {})
        query = parse_qs(parsed.query)
        name = query["name"][0]
        obj = self.server.objects / name
        self.server.writes.append(name)
        if query.get("ifGenerationMatch") == ["0"] and obj.exists():
            return self.reply(412, {})
        obj.parent.mkdir(parents=True, exist_ok=True)
        obj.write_bytes(self.rfile.read(int(self.headers["Content-Length"])))
        self.reply(200, {"generation": "1"})


class ReleaseProcessTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="nori-release-test-")
        self.directory = Path(self.temp.name)
        self.objects = self.directory / "objects"
        self.objects.mkdir()
        self.bin = self.directory / "bin"
        self.bin.mkdir()
        (self.directory / "sitecustomize.py").write_text(SITECUSTOMIZE)
        fake_gcloud = self.bin / "gcloud"
        fake_gcloud.write_text(GCLOUD)
        fake_gcloud.chmod(0o755)
        self.assertTrue(os.access(fake_gcloud, os.X_OK), "Fixture directory must allow executables; refusing a real gcloud fallback")
        self.binary = self.directory / "computer-agent"
        self.binary.write_bytes(b"deterministic computer artifact fixture\n")
        self.http = ThreadingHTTPServer(("127.0.0.1", 0), FakeAPI)
        self.http.objects = self.objects
        self.http.builds = {}
        self.http.writes = []
        self.thread = threading.Thread(target=self.http.serve_forever, daemon=True)
        self.thread.start()
        # Explicit, minimal environment prevents inheriting developer credentials.
        self.env = {"PATH": str(self.bin) + ":/usr/bin:/bin",
                    "NORI_ENV_FILE": str(self.directory / "absent.env"),
                    "PYTHONPATH": str(self.directory), "FAKE_OBJECTS": str(self.objects),
                    "FAKE_API": f"http://127.0.0.1:{self.http.server_port}",
                    "NORI_RELEASE_BUCKET": "fake-release", "NORI_BUILD_REGION": "us-central1",
                    "BRANCH_NAME": "main", "COMMIT_SHA": COMMIT, "BUILD_ID": BUILD}

    def tearDown(self):
        self.http.shutdown()
        self.http.server_close()
        self.thread.join()
        self.temp.cleanup()

    def cli(self, *args, env=None):
        return subprocess.run([sys.executable, str(CLI), *map(str, args)],
                              env=env or self.env, text=True, capture_output=True, timeout=20)

    def manifest_args(self, build=BUILD, **images):
        refs = {**IMAGES, **images}
        return ["--commit", COMMIT, "--build-id", build, "--web", refs["web"],
                "--runtime", refs["runtime"], "--worker", refs["worker"], "--computer", self.binary]

    def test_manifest_round_trip_rejects_mutable_or_corrupt_references(self):
        made = self.cli("manifest", *self.manifest_args())
        self.assertEqual(made.returncode, 0, made.stderr)
        manifest = json.loads(made.stdout)
        sha = hashlib.sha256(self.binary.read_bytes()).hexdigest()
        self.assertEqual(manifest["computer_sha256"], sha)
        self.assertEqual(manifest["computer_object"], "computer/" + sha)
        path = self.directory / "manifest.json"
        path.write_text(made.stdout)
        inspected = self.cli("inspect", path)
        self.assertEqual(inspected.returncode, 0, inspected.stderr)
        report = json.loads(inspected.stdout)
        self.assertFalse(report["cloud_provenance_verified"])
        self.assertEqual(report["pipeline_timeout_seconds"], 1800)
        self.assertEqual(report["smoke_timeout_seconds"], 600)
        self.assertEqual(report["failure_policy"], "pause_then_restore_before_smoke")
        tagged = self.cli("manifest", *self.manifest_args(web=REGISTRY + "/web:latest"))
        self.assertNotEqual(tagged.returncode, 0)
        manifest["computer_object"] = "computer/" + "0" * 64
        path.write_text(json.dumps(manifest))
        self.assertNotEqual(self.cli("inspect", path).returncode, 0)
        self.assertEqual(self.http.writes, [])

    def test_prepare_is_immutable_and_untrusted_candidate_never_deploys(self):
        first = self.cli("prepare", *self.manifest_args())
        self.assertEqual(first.returncode, 0, first.stderr)
        second_env = {**self.env, "BUILD_ID": NEXT_BUILD}
        second = self.cli("prepare", *self.manifest_args(build=NEXT_BUILD, worker=REGISTRY + "/worker@sha256:" + "e" * 64), env=second_env)
        self.assertEqual(second.returncode, 0, second.stderr)
        prefix = self.objects / "candidates" / COMMIT
        candidate = json.loads((prefix / (BUILD + ".json")).read_text())
        self.assertEqual(candidate["computer_sha256"], hashlib.sha256(self.binary.read_bytes()).hexdigest())
        self.assertTrue((prefix / (NEXT_BUILD + ".json")).is_file())
        self.assertNotEqual(self.cli("prepare", *self.manifest_args()).returncode, 0)
        results = [{"name": ref.split("@")[0] + ":tag", "digest": ref.split("@")[1]}
                   for ref in IMAGES.values()]
        good = {"status": "SUCCESS", "buildTriggerId": "trusted-trigger", "finishTime": "2026-01-01T00:00:00Z",
                "substitutions": {"BRANCH_NAME": "main", "COMMIT_SHA": COMMIT},
                "sourceProvenance": {"resolvedConnectedRepository": {"revision": COMMIT}},
                "results": {"images": results}}
        cases = [
            ("failed build", {**good, "status": "FAILURE"}, "Preparation build did not succeed"),
            ("foreign trigger", {**good, "buildTriggerId": "other-trigger"}, "untrusted trigger"),
            ("non-main branch", {**good, "substitutions": {"BRANCH_NAME": "feature", "COMMIT_SHA": COMMIT}}, "not for this main commit"),
            ("wrong source", {**good, "sourceProvenance": {"resolvedConnectedRepository": {"revision": "f" * 40}}}, "source provenance"),
            ("wrong image", {**good, "results": {"images": results[:-1]}}, "did not attest"),
        ]
        sandbox_build = {"status": "WORKING", "buildTriggerId": "sandbox-trigger",
                         "serviceAccount": "projects/bot-111549/serviceAccounts/nori-ci-sandbox@bot-111549.iam.gserviceaccount.com",
                         "substitutions": {"_SOURCE_COMMIT": COMMIT, "_PREPARE_BUILD_ID": BUILD}}
        sandbox_env = {**self.env, "BUILD_ID": SANDBOX_BUILD, "NORI_ENVIRONMENT": "sandbox"}
        sandbox_args = ["release", "--commit", COMMIT, "--prepare-build", BUILD, "--check-only"]
        for label, bad, message in cases:
            with self.subTest(label=label):
                self.http.builds = {BUILD: bad, NEXT_BUILD: bad, SANDBOX_BUILD: sandbox_build}
                self.http.writes.clear()
                rejected = self.cli(*sandbox_args, env=sandbox_env)
                self.assertNotEqual(rejected.returncode, 0)
                self.assertIn(message, rejected.stderr)
                self.assertEqual(self.http.writes, [], "untrusted artifacts must fail before any release journal or mutation")
                self.assertFalse((self.objects / "deployments/sandbox/lock.json").exists())

        # Successful automatic sandbox selection uses the event's preparation,
        # even if another build of the same commit has published newer artifacts.
        self.http.builds = {BUILD: good, SANDBOX_BUILD: sandbox_build}
        accepted = self.cli(*sandbox_args, env=sandbox_env)
        self.assertEqual(accepted.returncode, 0, accepted.stderr)
        selection = json.loads(accepted.stdout)
        self.assertEqual(selection["candidate"], candidate)
        self.assertEqual((selection["web"], selection["computer"], selection["task_queue"], selection["deployment"]),
                         ("nori-sandbox-web", "nori-sandbox-computer", "nori-sandbox-tasks", "nori-sandbox-task"))
        release_build = {"status": "WORKING", "buildTriggerId": "release-trigger",
                         "serviceAccount": "projects/bot-111549/serviceAccounts/nori-ci-deploy@bot-111549.iam.gserviceaccount.com",
                         "substitutions": {"COMMIT_SHA": COMMIT, "TAG_NAME": "v1.2.3"},
                         "sourceProvenance": good["sourceProvenance"]}
        self.http.builds[RELEASE_BUILD] = release_build
        production_env = {**self.env, "BUILD_ID": RELEASE_BUILD, "NORI_ENVIRONMENT": "production"}
        production_args = ["release", "--commit", COMMIT, "--check-only"]
        rejected = self.cli(*production_args, env=production_env)
        self.assertNotEqual(rejected.returncode, 0)
        self.assertIn("No successful sandbox", rejected.stderr)
        # These are external Cloud Storage/Build evidence, not mocked application
        # modules. Actual live deployment/smoke remains a separate demonstration.
        receipt = {"candidate": candidate, "environment": "sandbox", "result": "passed", "phase": "finished",
                   "compute_stopped": True, "build_id": SANDBOX_BUILD,
                   "smoke": [{"check": name, "status": "passed"} for name in ("web_health", "browser_task")]}
        receipt_path = self.objects / "validated" / COMMIT / (SANDBOX_BUILD + ".json")
        receipt_path.parent.mkdir(parents=True)
        receipt_path.write_text(json.dumps(receipt))
        rejected = self.cli(*production_args, env=production_env)
        self.assertNotEqual(rejected.returncode, 0, "an unfinished sandbox build cannot authorize production")
        self.http.builds[SANDBOX_BUILD] = {**sandbox_build, "status": "SUCCESS", "finishTime": "2026-01-01T01:00:00Z"}
        promoted = self.cli(*production_args, env=production_env)
        self.assertEqual(promoted.returncode, 0, promoted.stderr)
        selection = json.loads(promoted.stdout)
        self.assertEqual(selection["environment"], "production")
        self.assertEqual(selection["web"], "nori-web")
        self.assertEqual(selection["candidate"], candidate, "production must reuse the sandbox-tested manifest")
        for tag in ("", "main", "v1.2", "v01.2.3", "v1.2.3-rc1"):
            self.http.builds[RELEASE_BUILD] = {**release_build, "substitutions": {"COMMIT_SHA": COMMIT, "TAG_NAME": tag}}
            rejected = self.cli(*production_args, env=production_env)
            self.assertNotEqual(rejected.returncode, 0)
            self.assertIn("version tag", rejected.stderr)
        self.http.builds[RELEASE_BUILD] = release_build
        for key, value, message in (("compute_stopped", False, "shutdown"), ("candidate", {**candidate, "worker": IMAGES["worker"].replace("d" * 64, "e" * 64)}, "manifest changed")):
            receipt_path.write_text(json.dumps({**receipt, key: value}))
            rejected = self.cli(*production_args, env=production_env)
            self.assertNotEqual(rejected.returncode, 0)
            self.assertIn(message, rejected.stderr)
        self.assertEqual(self.http.writes, [], "selection and rejected promotions must not mutate cloud state")


if __name__ == "__main__":
    unittest.main()
