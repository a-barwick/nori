"""Shared setup: the runtime API through the operator tool, with the main checkout's .env."""
import os
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
common = subprocess.run(["git", "rev-parse", "--path-format=absolute", "--git-common-dir"], cwd=REPO, capture_output=True, text=True, check=True).stdout.strip()
os.environ.setdefault("NORI_ENV_FILE", str(Path(common).parent / ".env"))
sys.path.insert(0, str(REPO / "core/infrastructure/poc"))
import poc  # noqa: E402

ROOT = poc.config()["runtime_url"].rstrip("/")


def call(op, body):
    return poc.api("POST", f"{ROOT}/api/{op}", body)
