#!/usr/bin/env python3
"""Native-key lifecycle journeys; only external provider APIs are simulated."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import urllib.parse
from unittest.mock import patch

import keys


class KeyJourneys(unittest.TestCase):
    def setUp(self):
        self.documents, self.native_keys, self.versions = {}, {}, []
        self.revision = 0
        self.tmp = tempfile.TemporaryDirectory()
        self.repo = Path(self.tmp.name)
        subprocess.run(["git", "init", "-q", str(self.repo)], check=True)
        (self.repo / ".gitignore").write_text(".env\n")
        subprocess.run(["git", "-C", str(self.repo), "add", ".gitignore"], check=True)
        subprocess.run(["git", "-C", str(self.repo), "-c", "user.name=E2E", "-c", "user.email=e2e@invalid",
                        "commit", "-qm", "Initial"], check=True)
        self.env = self.repo / ".env"
        self.env.write_text("OPENROUTER_MANAGEMENT_KEY=fake-management\n")
        self.env.chmod(0o600)
        self.cloud_patch = patch.object(keys, "cloud_api", side_effect=self.cloud)
        self.native_patch = patch.object(keys, "native", side_effect=self.native)
        self.cloud_patch.start()
        self.native_patch.start()

    def tearDown(self):
        self.cloud_patch.stop()
        self.native_patch.stop()
        self.tmp.cleanup()

    def cloud(self, method, url, body=None):
        path, _, query = url.partition("?")
        if path.endswith(":addVersion"):
            self.versions.append(body)
            return {"name": keys.KEY_SECRET + "/versions/" + str(len(self.versions))}
        params = urllib.parse.parse_qs(query)
        if path == keys.SCOPES and method == "GET":
            return {"documents": [value for key, value in self.documents.items() if key != "global"]}
        scope = params["documentId"][0] if method == "POST" else path.rsplit("/", 1)[-1]
        if method == "POST":
            if scope in self.documents:
                raise keys.APIError(409)
            self.documents[scope] = {"fields": copy.deepcopy(body["fields"])}
        elif scope not in self.documents:
            raise keys.APIError(404)
        elif method == "PATCH":
            if params.get("currentDocument.updateTime", [self.documents[scope]["updateTime"]])[0] != self.documents[scope]["updateTime"]:
                raise keys.APIError(409)
            self.documents[scope]["fields"].update(copy.deepcopy(body["fields"]))
        if method != "GET":
            self.revision += 1
            self.documents[scope]["updateTime"] = str(self.revision)
        return copy.deepcopy(self.documents[scope])

    def native(self, method, path, body=None):
        if path.endswith("/budgets"):
            return {"include_byok_in_budgets": True, "data": [
                {"reset_interval": "daily", "limit_usd": 2}, {"reset_interval": "monthly", "limit_usd": 20}]}
        if path == "/keys":
            name = str(len(self.native_keys))
            self.native_keys[name] = {**body, "disabled": False}
            return {"data": {"hash": name}, "key": "fake-inference-" + name}
        self.native_keys[path.rsplit("/", 1)[-1]].update(body)
        return {"data": {}}

    def test_ci_retry_and_cleanup_share_one_allowance(self):
        commit = "a" * 40
        lease = keys.ensure_ci(commit)
        self.assertTrue(lease["ready"])
        self.assertEqual(lease["limit_usd"], .1)
        scope = lease["scope"]
        # Simulates the existing Firestore ledger after a bounded model call.
        keys.update(scope, {"used_nanos": 90_000_000})
        keys.close_ci(scope)
        self.assertFalse(keys.status(scope)["ready"])
        self.assertTrue(self.native_keys["0"]["disabled"])
        retry = keys.ensure_ci(commit)
        self.assertEqual(retry["scope"], scope)
        self.assertEqual(retry["secret_version"], lease["secret_version"])
        self.assertEqual(retry["accounted_usd"], .09)
        self.assertEqual(len(self.native_keys), 1)
        self.assertEqual(len(self.versions), 1)
        keys.update(scope, {"used_nanos": 100_000_000})
        with self.assertRaises(keys.Denied):
            keys.ensure_ci(commit)
        keys.close_ci(scope)
        keys.close_ci(scope)
        keys.close_ci("f" * 64)  # A failed issue with no scope is safe to close.
        self.assertEqual(len(self.native_keys), 1)

    def test_feature_progress_only_increases_to_one_dollar(self):
        for index in range(4):
            if index:
                keys.update(scope, {"used_nanos": int(index * .25 * keys.SCALE)})
                with self.assertRaises(keys.Denied):
                    keys.ensure_feature("chat", self.env, self.repo)
                (self.repo / "main.go").write_text(f"package main // revision {index}\n")
            value = keys.ensure_feature("chat", self.env, self.repo)
            scope = value["scope"]
            self.assertEqual(value["limit_usd"], .25 * (index + 1))
            self.assertEqual(self.native_keys["0"]["limit"], value["limit_usd"])
        keys.update(scope, {"used_nanos": keys.SCALE})
        (self.repo / "main.go").write_text("package main // another change\n")
        with self.assertRaises(keys.Denied):
            keys.ensure_feature("chat", self.env, self.repo)
        self.assertEqual(len(self.native_keys), 1)
        self.assertEqual(self.env.stat().st_mode & 0o777, 0o600)
        self.assertTrue(keys.read_env(self.env).get("NORI_SPEND_LEASE_" + scope.upper()))


if __name__ == "__main__":
    unittest.main()
