#!/usr/bin/env python3
"""Rehearse GCS state recovery locally, without writing to a backend.

Run with owner gcloud authentication and Terraform installed. Only GCS GETs are
issued. State bytes stay in a private temporary directory and are removed on
exit; stdout contains metadata only. No init, plan, apply, import or push runs.

For an actual incident, stop all state writers first, download the selected
gs://bucket/object#generation into an owner-only file, and reconcile its resource
ownership against live infrastructure before deliberately restoring state. An
older state can omit resources created since it was saved. This rehearsal proves
retrievability and local Terraform readability, not that rollback is appropriate.
"""
import base64
import hashlib
import json
from pathlib import Path
import ssl
import subprocess
import sys
import tempfile
from urllib.error import HTTPError
from urllib.parse import quote, urlencode
from urllib.request import Request, urlopen

BUCKET = "bot-111549-nori-ci-state"
OBJECTS = (
    "nori/poc/default.tfstate",
    "nori/temporal/default.tfstate",
    "nori/sandbox/foundation/default.tfstate",
    "nori/sandbox/poc/default.tfstate",
    "nori/sandbox/temporal/default.tfstate",
)
BASE = f"https://storage.googleapis.com/storage/v1/b/{BUCKET}"
SSL = ssl.create_default_context(cafile="/etc/ssl/cert.pem") if sys.platform == "darwin" else ssl.create_default_context()


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


class Storage:
    def __init__(self):
        result = subprocess.run(["gcloud", "auth", "print-access-token"], capture_output=True, text=True)
        require(result.returncode == 0 and result.stdout.strip(), "Owner gcloud authentication is required")
        self.token = result.stdout.strip()

    def get(self, path="", query=None, media=False):
        url = BASE + path + ("?" + urlencode(query) if query else "")
        try:
            with urlopen(Request(url, headers={"Authorization": "Bearer " + self.token}), context=SSL, timeout=60) as response:
                data = response.read()
        except HTTPError as error:
            raise RuntimeError(f"GCS GET failed: HTTP {error.code}") from None
        return data if media else json.loads(data)

    def versions(self, name):
        query = {"prefix": name, "versions": "true"}
        result = []
        while True:
            page = self.get("/o", query)
            result.extend(item for item in page.get("items", []) if item["name"] == name)
            if not page.get("nextPageToken"):
                return sorted(result, key=lambda item: int(item["generation"]))
            query["pageToken"] = page["nextPageToken"]


def inspect(storage, metadata, directory):
    name, generation = metadata["name"], metadata["generation"]
    raw = storage.get("/o/" + quote(name, safe=""), {"generation": generation, "alt": "media"}, media=True)
    require(len(raw) == int(metadata["size"]), name + ": size mismatch")
    digest = base64.b64encode(hashlib.md5(raw).digest()).decode()
    require(metadata.get("md5Hash") == digest, name + ": checksum mismatch")
    state = json.loads(raw)
    require(state.get("version") == 4 and state.get("lineage") and isinstance(state.get("serial"), int), name + ": invalid state header")
    path = directory / "recovered.tfstate"
    # TemporaryDirectory is mode 700; create the sensitive state file as 600.
    with path.open("xb") as stream:
        path.chmod(0o600)
        stream.write(raw)
    try:
        parsed = subprocess.run(["terraform", "state", "list", "-state=" + str(path)], cwd=directory, capture_output=True, text=True)
        require(parsed.returncode == 0, name + ": Terraform cannot read recovered state")
        count = len(parsed.stdout.splitlines())
    finally:
        path.unlink()
    return {"generation": generation, "created": metadata["timeCreated"], "serial": state["serial"],
            "lineage": state["lineage"], "resources": count, "sha256": hashlib.sha256(raw).hexdigest()}


def main():
    storage = Storage()
    bucket = storage.get()
    require(bucket.get("versioning", {}).get("enabled") is True, "State bucket versioning is disabled")
    soft_delete = int(bucket.get("softDeletePolicy", {}).get("retentionDurationSeconds", 0))
    require(soft_delete >= 604800, "State bucket soft delete is less than seven days")
    report = {"bucket": BUCKET, "versioning": True, "soft_delete_seconds": soft_delete, "states": []}
    with tempfile.TemporaryDirectory(prefix="nori-state-recovery-") as temporary:
        directory = Path(temporary)
        for name in OBJECTS:
            versions = storage.versions(name)
            live = [item for item in versions if not item.get("timeDeleted")]
            require(len(live) == 1, name + ": expected exactly one live generation")
            current = inspect(storage, live[0], directory)
            require(current["resources"], name + ": current state has no resources")
            # Initial backend migration creates empty state. It is history, but
            # cannot recover ownership; choose the newest populated version.
            previous = None
            for candidate in reversed(versions):
                if candidate.get("timeDeleted"):
                    recovered = inspect(storage, candidate, directory)
                    if recovered["resources"]:
                        previous = recovered
                        break
            require(previous, name + ": no populated historical generation")
            require(previous["lineage"] == current["lineage"], name + ": historical lineage differs")
            require(previous["serial"] <= current["serial"], name + ": historical serial exceeds current state")
            report["states"].append({"object": name, "generations": len(versions), "current": current, "recovered_previous": previous})
        # Concurrent state writes make this evidence stale. Never acquire, remove
        # or force-unlock the deploy/Terraform locks just to run a read rehearsal.
        for item in report["states"]:
            latest = storage.get("/o/" + quote(item["object"], safe=""))
            require(latest["generation"] == item["current"]["generation"], item["object"] + ": live generation changed during rehearsal; coordinate with its writer")
        require(storage.get()["metageneration"] == bucket["metageneration"], "Bucket protection changed during rehearsal")
    report["result"] = "PASS: previous generations recovered and read locally; live generations unchanged"
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError) as error:
        # Never emit Terraform stderr, auth output, state values or HTTP bodies.
        print("Recovery rehearsal failed: " + str(error), file=sys.stderr)
        sys.exit(1)
